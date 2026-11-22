.PHONY: all build test race fuzz lint fmt tidy clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

all: lint test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/meshd ./cmd/meshd

test:
	go test ./...

race:
	go test -race -count=1 ./...

# Short fuzz smoke run; decoder fuzz targets are added in milestone 1.
fuzz:
	@echo "no fuzz targets yet"

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

tidy:
	go mod tidy

clean:
	rm -rf bin/
