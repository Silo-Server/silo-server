.PHONY: proto test

GO_BIN ?= $(CURDIR)/bin
BUF ?= $(GO_BIN)/buf
PROTOC_GEN_GO ?= $(GO_BIN)/protoc-gen-go
PROTOC_GEN_GO_GRPC ?= $(GO_BIN)/protoc-gen-go-grpc

proto:
	@if ! command -v protoc >/dev/null 2>&1; then echo "protoc is required"; exit 1; fi
	@if [ ! -x "$(BUF)" ]; then GOBIN="$(GO_BIN)" go install github.com/bufbuild/buf/cmd/buf@latest; fi
	@if [ ! -x "$(PROTOC_GEN_GO)" ]; then GOBIN="$(GO_BIN)" go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11; fi
	@if [ ! -x "$(PROTOC_GEN_GO_GRPC)" ]; then GOBIN="$(GO_BIN)" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1; fi
	PATH="$(GO_BIN):$$PATH" $(BUF) generate

test:
	go test ./...
