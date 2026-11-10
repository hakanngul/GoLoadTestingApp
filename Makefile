BIN     := bin/goloadtest
PKG     := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build run test fmt vet tidy clean

all: fmt vet test build

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/goloadtest

run:
	go run ./cmd/goloadtest

test:
	go test -race -count=1 $(PKG)

fmt:
	gofmt -s -w .

vet:
	go vet $(PKG)

tidy:
	go mod tidy

clean:
	rm -rf bin

# Regenerate gRPC code. Needs protoc + protoc-gen-go + protoc-gen-go-grpc on PATH.
.PHONY: proto
proto:
	protoc -I proto --go_out=. --go_opt=module=github.com/hakanngul/GoLoadTestingApp \
		--go-grpc_out=. --go-grpc_opt=module=github.com/hakanngul/GoLoadTestingApp proto/cluster.proto
