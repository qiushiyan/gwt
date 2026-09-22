PREFIX ?= $(HOME)/.local
BIN := $(PREFIX)/bin/gwt

.PHONY: build install check test
build:
	go build -o gwt ./cmd/gwt

install:
	@mkdir -p "$(PREFIX)/bin"
	@tmp=$$(mktemp "$(BIN).XXXXXX"); trap 'rm -f "$$tmp"' EXIT; \
	go build -o "$$tmp" ./cmd/gwt && chmod 755 "$$tmp" && mv -f "$$tmp" "$(BIN)"

test:
	go test -race ./...

check: test
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)"
