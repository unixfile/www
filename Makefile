.PHONY: all build run test bench clean install uninstall deb install-deb uninstall-deb

# WORKING_NAME holds the program's name, provisional until a final one is
# chosen. The binary, the package and the paths below take it.
NAME := $(shell cat WORKING_NAME)

all: build

build:
	CGO_ENABLED=0 go build -o build/$(NAME) .

# run serves this directory from build/ with --stamp, on port 8080 beside
# an installed server on port 80. A server already there is replaced by
# this build, serving the same trees, so its open pages go on under the
# new code; the browser opens only when no page of this directory was
# open, and www says which happened.
RUNADDR := 127.31.$(shell expr $$(id -u) - 1000).1:8080

run: build
	@build/$(NAME) --restart --stamp -a $(RUNADDR) .

test:
	go vet ./...
	go test ./...

# bench runs the benchmarks in bench_test.go. BENCH picks them by pattern
# and COUNT repeats each, eg COUNT=10 for benchstat.
BENCH ?= .
COUNT ?= 1

bench:
	go test -run '^$$' -bench '$(BENCH)' -benchmem -count $(COUNT) .

clean:
	rm -rf build

# install and install-deb end by moving running servers to the new binary,
# so no server keeps old code. A server on port 80 stays put under the
# binary from make install, which cannot bind it. install also ships the
# user unit, disabled, which serves .LOCK trees again at login; it never
# uses sudo, so the system template comes only with the package.
USERUNIT := $(HOME)/.config/systemd/user/$(NAME).service

install: build
	ln -sf $(CURDIR)/build/$(NAME) $(HOME)/.local/bin/$(NAME)
	mkdir -p $(dir $(USERUNIT))
	sed 's/@NAME@/$(NAME)/g; s|@BIN@|$(HOME)/.local/bin/$(NAME)|' systemd/user.service.in > $(USERUNIT)
	-systemctl --user daemon-reload
	-build/$(NAME) --restart

uninstall:
	rm -f $(HOME)/.local/bin/$(NAME) $(USERUNIT)
	-systemctl --user daemon-reload

# deb builds a Debian package in build/. The version comes from the last
# commit, its UTC time first so later commits sort higher, until releases
# carry their own; ARCH picks the architecture.
VERSION ?= 0~git$(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y%m%d%H%M%S).$(shell git rev-parse --short HEAD)
ARCH ?= $(shell go env GOARCH)
STAGE := build/deb
DEB := build/$(NAME)_$(VERSION)_$(ARCH).deb

deb:
	rm -rf $(STAGE)
	CGO_ENABLED=0 GOARCH=$(ARCH) go build -trimpath -ldflags='-s -w' -o $(STAGE)/usr/bin/$(NAME) .
	install -D -m 0644 README.md $(STAGE)/usr/share/doc/$(NAME)/README.md
	{ sed '/^@EXPAT@$$/,$$d; s/@NAME@/$(NAME)/g' deb/copyright.in; sed '1,4d; s/^$$/./; s/^/ /' LICENSE; \
	  sed '1,/^@EXPAT@$$/d' deb/copyright.in; sed 's/^$$/./; s/^/ /' assets/fonts/STIXTwoText-OFL.txt; } > $(STAGE)/copyright
	install -D -m 0644 $(STAGE)/copyright $(STAGE)/usr/share/doc/$(NAME)/copyright
	rm $(STAGE)/copyright
	mkdir -p $(STAGE)/usr/share/lintian/overrides
	sed 's/@NAME@/$(NAME)/g' deb/lintian-overrides.in > $(STAGE)/usr/share/lintian/overrides/$(NAME)
	mkdir -p $(STAGE)/usr/lib/systemd/user $(STAGE)/usr/lib/systemd/system
	sed 's/@NAME@/$(NAME)/g; s|@BIN@|/usr/bin/$(NAME)|' systemd/user.service.in > $(STAGE)/usr/lib/systemd/user/$(NAME).service
	sed 's/@NAME@/$(NAME)/g; s|@BIN@|/usr/bin/$(NAME)|' systemd/system@.service.in > $(STAGE)/usr/lib/systemd/system/$(NAME)@.service
	printf '$(NAME) (%s) unstable; urgency=low\n\n  * Build of commit %s.\n\n -- %s  %s\n' \
		'$(VERSION)' "$$(git rev-parse --short HEAD)" "$$(sed -n 's/^Maintainer: //p' deb/control.in)" \
		"$$(git log -1 --format=%cD)" | gzip -9n > $(STAGE)/changelog.gz
	install -D -m 0644 $(STAGE)/changelog.gz $(STAGE)/usr/share/doc/$(NAME)/changelog.gz
	rm $(STAGE)/changelog.gz
	mkdir -p $(STAGE)/DEBIAN
	sed 's/@NAME@/$(NAME)/g' deb/postinst.in > $(STAGE)/DEBIAN/postinst
	sed -e 's/@NAME@/$(NAME)/g' -e 's/@VERSION@/$(VERSION)/' -e 's/@ARCH@/$(ARCH)/' \
		-e "s/@SIZE@/$$(du -sk $(STAGE)/usr | cut -f1)/" deb/control.in > $(STAGE)/DEBIAN/control
	chmod -R u=rwX,go=rX $(STAGE)
	chmod 0755 $(STAGE)/DEBIAN/postinst
	dpkg-deb --root-owner-group --build $(STAGE) $(DEB)

# install-deb installs the package through apt, which pulls in git.
# --reinstall replaces a build of the same commit, which apt would skip as
# already installed. The link from make install goes first: ~/.local/bin
# usually leads PATH, and its binary lacks the capability for port 80.
# The package builds in the background while sudo asks for the password,
# and its output is hidden so it does not break into the prompt. Plain make,
# not $(MAKE), so make -n prints the line instead of running sudo.
install-deb: uninstall
	@make -s deb >/dev/null & build=$$!; \
	sudo -v; auth=$$?; \
	wait $$build && [ $$auth -eq 0 ]
	sudo apt install --reinstall ./$(DEB)
	-/usr/bin/$(NAME) --restart

uninstall-deb:
	sudo apt remove $(NAME)
