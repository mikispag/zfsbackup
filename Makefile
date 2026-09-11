.PHONY: build deb pacman tests unit-tests integration-tests clean FORCE

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
# Package versions must start with a digit; Arch also disallows hyphens.
PACKAGE_VERSION := $(subst -,.,$(patsubst v%,%,$(VERSION)))
ifeq ($(filter 0% 1% 2% 3% 4% 5% 6% 7% 8% 9%,$(PACKAGE_VERSION)),)
PACKAGE_VERSION := 0.$(PACKAGE_VERSION)
endif
SIGN     ?= 1
BINARY    = zfsbackup
BUILD_DIR = .

build: $(BINARY)

$(BINARY): FORCE $(shell find cmd internal -name '*.go') go.mod go.sum
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/zfsbackup

# VERSION and VCS metadata can change without any source file changing.
FORCE:

deb: $(BINARY)
	rm -f $(BINARY)_*_amd64.deb
	fpm -t deb -s dir --name $(BINARY) --version $(PACKAGE_VERSION) \
		-d 'zfsutils-linux >= 2.3' -d zstd -d mbuffer \
		./$(BINARY)=/usr/bin/$(BINARY)
ifeq ($(SIGN), 1)
	debsigs --sign=origin $(BINARY)_$(PACKAGE_VERSION)_amd64.deb
endif

pacman: $(BINARY)
	fpm -t pacman -s dir --name $(BINARY) --version $(PACKAGE_VERSION) \
		-d 'zfs-utils >= 2.3' -d mbuffer -d zstd \
		--pacman-compression zstd \
		--pacman-user root --pacman-group root \
		./$(BINARY)=/usr/bin/$(BINARY)
ifeq ($(SIGN), 1)
	gpg --detach-sign -s $(BINARY)-$(PACKAGE_VERSION)-1-x86_64.pkg.tar.zst
endif

tests: unit-tests integration-tests

unit-tests:
	gofmt -l . | (! grep .)
	go vet ./...
	go test -count=1 ./...

integration-tests:
	bats tests/tests.bats

clean:
	rm -f $(BINARY) *.deb *.pkg.tar.zst *.pkg.tar.zst.sig *.deb.sig
