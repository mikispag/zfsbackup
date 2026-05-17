.PHONY: build deb pacman tests clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
SIGN     ?= 1
BINARY    = zfsbackup
BUILD_DIR = .

build: $(BINARY)

$(BINARY): $(shell find cmd internal -name '*.go')
	CGO_ENABLED=0 go build -o $(BINARY) ./cmd/zfsbackup

deb: $(BINARY)
	rm -f $(BINARY)_*_amd64.deb
	fpm -t deb -s dir --name $(BINARY) --version $(VERSION) \
		-d zfsutils -d zstd -d mbuffer \
		./$(BINARY)=/usr/bin/$(BINARY)
ifeq ($(SIGN), 1)
	debsigs --sign=origin $(BINARY)_$(VERSION)_amd64.deb
endif

pacman: $(BINARY)
	fpm -t pacman -s dir --name $(BINARY) --version $(VERSION) \
		-d mbuffer -d zstd \
		--pacman-compression zstd \
		--pacman-user root --pacman-group root \
		./$(BINARY)=/usr/bin/$(BINARY)
ifeq ($(SIGN), 1)
	gpg --detach-sign -s $(BINARY)-$(VERSION)-1-x86_64.pkg.tar.zst
endif

tests: unit-tests integration-tests

unit-tests:
	go vet ./...
	go test ./...

integration-tests:
	bats tests/tests.bats

clean:
	rm -f $(BINARY) *.deb *.pkg.tar.zst *.pkg.tar.zst.sig *.deb.sig
