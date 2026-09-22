BINARY  := mylos-tango-agent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build build-windows test race vet fmt clean

all: fmt vet test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/agent

## .exe para el servidor Windows de Tango (cross-compila desde Linux)
build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY).exe ./cmd/agent

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal

clean:
	rm -rf bin
