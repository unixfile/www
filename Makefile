.PHONY: all build run test clean install uninstall deb install-deb uninstall-deb

all: build

build:
	CGO_ENABLED=0 go build -o build/mdserve .

run: build
	build/mdserve -f --stamp

test:
	go vet ./...
	go test ./...

clean:
	rm -rf build

# install and install-deb end by moving running servers to the new binary,
# so no server keeps old code. A server on port 80 stays put under the
# binary from make install, which cannot bind it.
install: build
	ln -sf $(CURDIR)/build/mdserve $(HOME)/.local/bin/mdserve
	-build/mdserve --restart

uninstall:
	rm -f $(HOME)/.local/bin/mdserve

# deb builds a Debian package in build/. The version comes from the last
# commit, its UTC time first so later commits sort higher, until releases
# carry their own; ARCH picks the architecture.
VERSION ?= 0~git$(shell TZ=UTC git log -1 --format=%cd --date=format-local:%Y%m%d%H%M%S).$(shell git rev-parse --short HEAD)
ARCH ?= $(shell go env GOARCH)
STAGE := build/deb
DEB := build/mdserve_$(VERSION)_$(ARCH).deb

deb:
	rm -rf $(STAGE)
	CGO_ENABLED=0 GOARCH=$(ARCH) go build -trimpath -ldflags='-s -w' -o $(STAGE)/usr/bin/mdserve .
	install -D -m 0644 README.md $(STAGE)/usr/share/doc/mdserve/README.md
	{ sed '/^@EXPAT@$$/,$$d' deb/copyright.in; sed '1,4d; s/^$$/./; s/^/ /' LICENSE; \
	  sed '1,/^@EXPAT@$$/d' deb/copyright.in; sed 's/^$$/./; s/^/ /' assets/fonts/STIXTwoText-OFL.txt; } > $(STAGE)/copyright
	install -D -m 0644 $(STAGE)/copyright $(STAGE)/usr/share/doc/mdserve/copyright
	rm $(STAGE)/copyright
	install -D -m 0644 deb/lintian-overrides $(STAGE)/usr/share/lintian/overrides/mdserve
	printf 'mdserve (%s) unstable; urgency=low\n\n  * Build of commit %s.\n\n -- %s  %s\n' \
		'$(VERSION)' "$$(git rev-parse --short HEAD)" "$$(sed -n 's/^Maintainer: //p' deb/control.in)" \
		"$$(git log -1 --format=%cD)" | gzip -9n > $(STAGE)/changelog.gz
	install -D -m 0644 $(STAGE)/changelog.gz $(STAGE)/usr/share/doc/mdserve/changelog.gz
	rm $(STAGE)/changelog.gz
	mkdir -p $(STAGE)/DEBIAN
	install -m 0755 deb/postinst $(STAGE)/DEBIAN/postinst
	sed -e 's/@VERSION@/$(VERSION)/' -e 's/@ARCH@/$(ARCH)/' \
		-e "s/@SIZE@/$$(du -sk $(STAGE)/usr | cut -f1)/" deb/control.in > $(STAGE)/DEBIAN/control
	chmod -R u=rwX,go=rX $(STAGE)
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
	-/usr/bin/mdserve --restart

uninstall-deb:
	sudo apt remove mdserve
