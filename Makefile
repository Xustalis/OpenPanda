GO ?= go
BIN := bin/panda
VERSION ?= 0.0.3
LDFLAGS := -s -w -X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)

.PHONY: all build web build-webui build-darwin-arm64 build-linux-arm64 build-linux-amd64 build-windows-amd64 build-windows-arm64 \
        release-darwin-amd64 release-darwin-arm64 release-linux-arm64 release-linux-amd64 release-windows-amd64 release-windows-arm64 \
        dev test vet fmt fmt-check race gate run run-local measure clean icons release package

all: build

# Default: native platform binary (release: stripped symbols)
build:
	$(GO) build -ldflags "-s -w" -o $(BIN) ./cmd/panda

# Build the web console (webui/app) into webui/panel/dist/app, where go:embed
# folds it into the panel binary. Requires node/npm. The committed
# dist/index.html placeholder is never touched (vite empties only dist/app).
web:
	cd webui/app && npm install --no-fund --no-audit && npm run build
	@if [ ! -f webui/panel/dist/app/index.html ]; then \
		echo "make web: dist/app/index.html missing — the build did not land"; exit 1; fi

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

build-windows-arm64:
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(BIN)-windows-arm64.exe ./cmd/panda

# Release: version-tagged binaries for every target platform into dist/.
# One `make web` up front — the embedded console is platform-independent.
release: web release-darwin-amd64 release-darwin-arm64 release-linux-arm64 release-linux-amd64 release-windows-amd64 release-windows-arm64

release-darwin-amd64:
	GOOS=darwin GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-darwin-amd64 ./cmd/panda

release-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-darwin-arm64 ./cmd/panda

release-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-linux-arm64 ./cmd/panda

release-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-linux-amd64 ./cmd/panda

release-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-windows-amd64.exe ./cmd/panda

release-windows-arm64:
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o dist/panda-$(VERSION)-windows-arm64.exe ./cmd/panda

# One-command release packaging: cross-compiles every supported platform into
# dist/panda-<version>-<os>-<arch>.tar.gz (unix) / .zip (windows) plus a
# checksums.txt, for GitHub Releases. Run `make web` first so the embedded
# console is baked into every binary.
package:
	./scripts/package.sh $(VERSION)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Formatting is part of the gate, not a reviewer's job: gofmt disagreements are
# the one class of diff noise that is entirely mechanical to prevent.
fmt:
	$(GO) fmt ./...

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

race:
	$(GO) test -race ./...

# Merge gate (C-10): a PR must pass fmt + build + vet + test + race before landing.
gate: fmt-check build vet test race

# Regenerate the PWA icon set (webui/app/public/icons/) from the stdlib-only generator.
icons:
	$(GO) run ./scripts/genicons

# Quick-start: build (if needed) and open the web console with config.yaml.
# One command to see everything — auto token, auto browser, loopback-only.
dev: build
	./bin/panda web --config config.yaml

run:
	$(GO) run ./cmd/panda daemon --config config.yaml

# Start the daemon + webui sidecar locally with one command (see scripts/run-local.sh).
run-local:
	exec ./scripts/run-local.sh

# Measure steady-state RSS: start core, sample after 2s, stop.
measure:
	@$(MAKE) build
	@./bin/panda daemon --config testdata/node-a.yaml > /tmp/panda-measure.log 2>&1 & \
		echo $$! > /tmp/panda-measure.pid; \
		sleep 2; \
		ps -o rss= -p $$(cat /tmp/panda-measure.pid) | awk '{printf "RSS: %.2f MB\n", $$1/1024}'; \
		kill -TERM $$(cat /tmp/panda-measure.pid) 2>/dev/null

clean:
	rm -rf bin dist
