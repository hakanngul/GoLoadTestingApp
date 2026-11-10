BIN     := bin/goloadtest
PKG     := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all build ui run serve test fmt vet tidy clean

all: fmt vet test build

ui:
	cd web && pnpm install --frozen-lockfile && pnpm build

build: ui
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/goloadtest

run:
	go run ./cmd/goloadtest

# Local UI on http://localhost:8080: in-memory store (data is lost on exit),
# single node, any target allowed. For development only.
serve: build
	GOLOAD_ALLOWED_TARGETS='*' ./bin/goloadtest serve -grpc-addr ""

test:
	go test -race -count=1 $(PKG)

fmt:
	gofmt -s -w .

vet:
	go vet $(PKG)

tidy:
	go mod tidy

clean:
	rm -rf bin web/dist

# Regenerate gRPC code. Needs protoc + protoc-gen-go + protoc-gen-go-grpc on PATH.
.PHONY: proto
proto:
	protoc -I proto --go_out=. --go_opt=module=github.com/hakanngul/GoLoadTestingApp \
		--go-grpc_out=. --go-grpc_opt=module=github.com/hakanngul/GoLoadTestingApp proto/cluster.proto
