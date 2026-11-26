APP      := androidmtp
MODULE  := github.com/Hashim1999164/androidMTP
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
PREFIX  ?= /usr/local
BINDIR  ?= $(PREFIX)/bin

# Prefer system fuse-t, then user-local install from the fuse-t pkg.
FUSE_PKG ?= $(HOME)/.fuse-t/usr/local/lib/pkgconfig
export PKG_CONFIG_PATH := $(FUSE_PKG):/usr/local/lib/pkgconfig:$(PKG_CONFIG_PATH)
export CGO_CFLAGS  ?= $(shell pkg-config --cflags fuse 2>/dev/null)
export CGO_LDFLAGS ?= $(shell pkg-config --libs fuse 2>/dev/null)

LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"

.PHONY: all build install uninstall test tidy doctor clean release

all: build

build:
	go build -buildvcs=false $(LDFLAGS) -o bin/$(APP) ./cmd/androidmtp

install: build
	install -d $(BINDIR)
	install -m 755 bin/$(APP) $(BINDIR)/$(APP)

uninstall:
	rm -f $(BINDIR)/$(APP)

test:
	go test ./...

tidy:
	go mod tidy

doctor: build
	./bin/$(APP) doctor

clean:
	rm -rf bin dist

release: tidy build
	mkdir -p dist
	cp bin/$(APP) dist/$(APP)-darwin-arm64
	cd dist && shasum -a 256 $(APP)-darwin-arm64 > $(APP)-darwin-arm64.sha256
