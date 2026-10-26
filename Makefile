.PHONY: fmt vet test build check

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN := bin/git-rebind

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test -timeout 120s ./...

build:
	mkdir -p bin
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) .

check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "$$files"; \
		exit 1; \
	fi
	go vet ./...
	go test -timeout 120s ./...
	mkdir -p bin
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) .
