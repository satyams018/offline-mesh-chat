.PHONY: all build test race fuzz lint fmt proto tidy clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
FUZZTIME ?= 10s

all: lint test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/meshd ./cmd/meshd

test:
	go test ./...

race:
	go test -race -count=1 ./...

# Short fuzz run of every decoder that parses bytes from strangers.
fuzz:
	go test -run='^$$' -fuzz=FuzzReadFrame -fuzztime=$(FUZZTIME) ./core/link
	go test -run='^$$' -fuzz=FuzzFromProto -fuzztime=$(FUZZTIME) ./core/bundle

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...
	buf format -w

# Lint the schema and regenerate Go code (committed, so builds don't need buf).
proto:
	buf lint
	buf generate

tidy:
	go mod tidy

clean:
	rm -rf bin/
