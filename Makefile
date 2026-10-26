GO ?= $(shell command -v go || echo /usr/local/go/bin/go)

.PHONY: build test vet
build:
	CGO_ENABLED=0 $(GO) build -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o neighbeats-radio ./cmd/neighbeats-radio
test:
	$(GO) test -race -count=1 ./...
vet:
	$(GO) vet ./...
