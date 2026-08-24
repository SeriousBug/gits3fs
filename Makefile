# git-s3fs. Builds are pure Go: no cgo, so binaries are portable and static.
GO      ?= go
BIN     ?= bin/git-s3fs
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/SeriousBug/gits3fs/internal/version.Version=$(VERSION)

export CGO_ENABLED = 0

.PHONY: build
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/git-s3fs

.PHONY: install
install:
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/git-s3fs

.PHONY: test
test:
	$(GO) test ./...

.PHONY: race
race:
	CGO_ENABLED=1 $(GO) test -race ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmtcheck
fmtcheck:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: check
check: fmtcheck vet test

# Cross compiled release binaries. No cgo means every target is a plain
# `go build` with GOOS and GOARCH set.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64 freebsd/arm64

.PHONY: dist
dist:
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "building dist/git-s3fs-$$os-$$arch$$ext"; \
		GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/git-s3fs-$$os-$$arch$$ext ./cmd/git-s3fs || exit 1; \
	done

.PHONY: clean
clean:
	rm -rf bin dist
