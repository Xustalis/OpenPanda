GO ?= go
BIN := bin/panda
VERSION ?= 0.0.1
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web build-webui build-darwin-arm64 build-linux-arm64 build-linux-amd64 build-windows-amd64 \
        test vet race gate run run-local measure clean icons release

all: build

# Default: native platform binary (release: stripped symbols)
build:
	$(GO) build -ldflags "-s -w" -o $(BIN) ./cmd/panda

# Build the web console (webui/app) into webui/panel/dist, where go:embed
# folds it into the panel binary. Requires node/npm.
web:
	cd webui/app && npm install --no-fund --no-audit && npm run build

# Web panel sidecar with the embedded console. `make web` first for a real UI;
# without it the binary embeds the committed placeholder.
build-webui: web
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

# Release: version-tagged binaries for every target platform into dist/.
# One `make web` up front — the embedded console is platform-independent.
release: web release-darwin-arm64 release-linux-arm64 release-linux-amd64 release-windows-amd64

release-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-darwin-arm64 ./cmd/panda

release-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-linux-arm64 ./cmd/panda

release-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-linux-amd64 ./cmd/panda

release-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-windows-amd64.exe ./cmd/panda

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...

# Merge gate (C-10): a PR must pass build + vet + test + race before landing.
gate: build vet test race

# Regenerate the PWA icon set (webui/app/public/icons/) from the stdlib-only generator.
icons:
	$(GO) run ./scripts/genicons

run:
	$(GO) run ./cmd/panda --config config.example.yaml

# Start the daemon + webui sidecar locally with one command (see scripts/run-local.sh).
run-local:
	exec ./scripts/run-local.sh

# Measure steady-state RSS: start core, sample after 2s, stop.
measure:
	@$(MAKE) build
	@./bin/panda --config testdata/mac-config.yaml > /tmp/panda-measure.log 2>&1 & \
		echo $$! > /tmp/panda-measure.pid; \
		sleep 2; \
		ps -o rss= -p $$(cat /tmp/panda-measure.pid) | awk '{printf "RSS: %.2f MB\n", $$1/1024}'; \
		kill -TERM $$(cat /tmp/panda-measure.pid) 2>/dev/null

clean:
	rm -rf bin dist
