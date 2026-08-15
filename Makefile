GO ?= go
BIN := bin/panda

.PHONY: all build build-webui build-darwin-arm64 build-linux-arm64 build-linux-amd64 build-windows-amd64 \
        test vet race gate run measure clean icons

all: build

# Default: native platform binary (release: stripped symbols)
build:
	$(GO) build -ldflags "-s -w" -o $(BIN) ./cmd/panda

# Optional legacy web control panel as a standalone sidecar (webui/README.md).
build-webui:
	$(GO) build -ldflags "-s -w" -o $(BIN)-webui ./webui/cmd/panel

# Cross-compile targets (design doc §4.4)
build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(BIN)-darwin-arm64 ./cmd/panda

build-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(BIN)-linux-arm64 ./cmd/panda

build-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "-s -w" -o $(BIN)-linux-amd64 ./cmd/panda

build-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "-s -w" -o $(BIN)-windows-amd64.exe ./cmd/panda

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...

# Merge gate (C-10): a PR must pass build + vet + test + race before landing.
gate: build vet test race

# Regenerate the PWA icon set (web/pwa/icons/) from the stdlib-only generator.
icons:
	$(GO) run ./scripts/genicons

run:
	$(GO) run ./cmd/panda --config config.example.yaml

# Measure steady-state RSS: start core, sample after 2s, stop.
measure:
	@$(MAKE) build
	@./bin/panda --config testdata/mac-config.yaml > /tmp/panda-measure.log 2>&1 & \
		echo $$! > /tmp/panda-measure.pid; \
		sleep 2; \
		ps -o rss= -p $$(cat /tmp/panda-measure.pid) | awk '{printf "RSS: %.2f MB\n", $$1/1024}'; \
		kill -TERM $$(cat /tmp/panda-measure.pid) 2>/dev/null

clean:
	rm -rf bin
