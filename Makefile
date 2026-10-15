.PHONY: all build run test clean install uninstall

all: build

build:
	CGO_ENABLED=0 go build -o build/mdserve .

run: build
	build/mdserve -f

test:
	go vet ./...
	go test ./...

clean:
	rm -rf build

install: build
	ln -sf $(CURDIR)/build/mdserve $(HOME)/.local/bin/mdserve

uninstall:
	rm -f $(HOME)/.local/bin/mdserve
