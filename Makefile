GO ?= go
BIN := bin/panda

.PHONY: all build darwin linux-arm linux-amd build-darwin build-linux-arm64 build-linux-amd64 test vet run clean

all: build

# Default: native platform binary
build:
	$(GO) build -o $(BIN) ./cmd/panda

# Cross-compile targets (design doc §4.4)
build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(BIN)-darwin-arm64 ./cmd/panda

build-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -o $(BIN)-linux-arm64 ./cmd/panda

build-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BIN)-linux-amd64 ./cmd/panda

build-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build -o $(BIN)-windows-amd64.exe ./cmd/panda

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

run:
	$(GO) run ./cmd/panda --config config.example.yaml

clean:
	rm -rf bin
