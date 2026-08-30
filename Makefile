GO ?= go
BIN := bin/panda
# VERSION is read from internal/version/version.go by default. Set explicitly
# for release packaging, e.g. `make release VERSION=0.0.7`. Never bump the
# ?= default here: the version.go file is the single source of truth, and
# writing a stale number here is exactly how built binaries show the wrong
# version when built via the default `make build`.
VERSION ?= $(shell sed -n 's/^var Version = "\(.*\)"/\1/p' internal/version/version.go)
# RELEASE_LDFLAGS is the full ldflags bundle used for release targets
# (cross-compiled binaries shipped to users). Development builds use
# LDFLAGS_DEV below: stripped symbols, no version override — they keep the
# version.Version source value (including the -beta / -rc suffixes that a
# VERSION override here would strip).
RELEASE_LDFLAGS := -s -w -X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
LDFLAGS_DEV := -s -w

# Static binaries by default (no cgo). A small minority of users with
# CGO-only dependencies can override via `make build CGO_ENABLED=1`.
CGO_ENABLED ?= 0
export CGO_ENABLED

.PHONY: all build web web-test build-webui build-darwin-arm64 build-linux-arm64 build-linux-amd64 build-windows-amd64 build-windows-arm64 \
        release-darwin-amd64 release-darwin-arm64 release-linux-arm64 release-linux-amd64 release-windows-amd64 release-windows-arm64 \
        dev test vet fmt fmt-check race race-focused gate gate-all run run-local measure clean icons release package release-local

all: build

# Default: native platform binary (stripped symbols, version from source).
# Run `make web` BEFORE `make build` for a binary that embeds the real web
# console; without it the binary embeds the committed placeholder dist.
# The fmt-check + vet gate prevents "but it compiles on my machine" drift
# where a PR lands with a broken vet warning or unformatted code.
build: fmt-check vet
	$(GO) build -ldflags "$(LDFLAGS_DEV)" -o $(BIN) ./cmd/panda

# Build the web console (webui/app) into webui/panel/dist/app, where go:embed
# folds it into the panel binary. Requires node/npm. The committed
# dist/index.html placeholder is never touched (vite empties only dist/app).
web:
	cd webui/app && npm install --no-fund --no-audit && npm run typecheck && npm run build
	@if [ ! -f webui/panel/dist/app/index.html ]; then \
		echo "make web: dist/app/index.html missing — the build did not land"; exit 1; fi

# Console typecheck + unit tests (node's own runner — no test framework is
# installed). Separate from `make test` because it needs node, which the Go
# gate does not: CI runs it wherever `make web` runs.
web-test:
	cd webui/app && npm install --no-fund --no-audit && npm run typecheck && npm test

# Web panel sidecar with the embedded console. `make web` first for a real UI;
# without it the binary embeds the committed placeholder.
build-webui: web fmt-check vet
	$(GO) build -ldflags "$(LDFLAGS_DEV)" -o $(BIN)-webui ./webui/cmd/panel

# Cross-compile targets (design doc §4.4)
build-darwin-arm64: fmt-check vet
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(BIN)-darwin-arm64 ./cmd/panda

build-linux-arm64: fmt-check vet
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(BIN)-linux-arm64 ./cmd/panda

build-linux-amd64: fmt-check vet
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "-s -w" -o $(BIN)-linux-amd64 ./cmd/panda

build-windows-amd64: fmt-check vet
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "-s -w" -o $(BIN)-windows-amd64.exe ./cmd/panda

build-windows-arm64: fmt-check vet
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "-s -w" -o $(BIN)-windows-arm64.exe ./cmd/panda

# Release: version-tagged binaries for every target platform into dist/.
# One `make web` up front — the embedded console is platform-independent.
release: web fmt-check vet release-darwin-amd64 release-darwin-arm64 release-linux-arm64 release-linux-amd64 release-windows-amd64 release-windows-arm64

# release-local is the one-command "build the panel + cross-compile for the
# three desktop platforms and package everything" used by the maintainer
# before a ship. It differs from `release` only in running the packaging
# step (tar.gz/zip + checksums) as the final action.
release-local: release package

release-darwin-amd64:
	GOOS=darwin GOARCH=amd64 $(GO) build -ldflags "$(RELEASE_LDFLAGS)" -o dist/panda-$(VERSION)-darwin-amd64 ./cmd/panda

release-darwin-arm64:
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "$(RELEASE_LDFLAGS)" -o dist/panda-$(VERSION)-darwin-arm64 ./cmd/panda

release-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build -ldflags "$(RELEASE_LDFLAGS)" -o dist/panda-$(VERSION)-linux-arm64 ./cmd/panda

release-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build -ldflags "$(RELEASE_LDFLAGS)" -o dist/panda-$(VERSION)-linux-amd64 ./cmd/panda

release-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags "$(RELEASE_LDFLAGS)" -o dist/panda-$(VERSION)-windows-amd64.exe ./cmd/panda

release-windows-arm64:
	GOOS=windows GOARCH=arm64 $(GO) build -ldflags "$(RELEASE_LDFLAGS)" -o dist/panda-$(VERSION)-windows-arm64.exe ./cmd/panda

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

# race-focused is the race detector scoped to the packages that actually share
# state across goroutines and connections — the CI gate's race leg. `race`
# (full suite) remains the pre-release check; the extra minutes it spends on
# goroutine-free packages buy no detections.
race-focused:
	$(GO) test -race ./internal/core/... ./internal/commander/... ./internal/bus/... ./internal/storage/...

# ============================================================================
# CI Gate 清单化 (§稳定性 P1)
#
# 执行顺序：按「成本从低到高」排列，越靠前的检查越快，失败时能快速
# 反馈，避免把机器时间浪费在明知会失败的昂贵步骤上。
#
# gate  — Go-only pipeline（后端特性分支使用）
#   1. fmt-check   (go fmt 格式校验，毫秒级，成本最低)
#   2. vet         (go vet 静态语义检查，秒级)
#   3. build       (实际编译 go build ./...，验证编译通过)
#   4. test        (go test ./... 单元测试，十秒级)
#   5. race        (-race 竞态检测，分钟级，最昂贵，最后才跑)
#
# gate-all — 全栈 pipeline（CI / 前端改动 / 合并前使用）
#   在 gate 之上追加：
#   6. web-test    (npm typecheck + npm test，TS 类型 + 前端单测)
#   7. web         (npm build，vite 生产构建，验证 embed 产物完整)
#
# 说明：
#   - fmt-check 在 build 目标中也被前置依赖，所以 `make build` 本身
#     就不会放过未格式化的代码；这里单独列出是为 CI 报告更清晰。
#   - CGO_ENABLED=0 默认静态化，避免发布二进制出现 libc 依赖。
#   - 版本号从 internal/version/version.go 提取，禁止在 Makefile 中
#     写死 VERSION 默认值，避免版本漂移。
# ============================================================================
gate: fmt-check vet build test race

# gate-all is gate + the node/web pipeline (typecheck + ui build + web
# tests). CI uses gate-all; a backend-only feature branch can use gate.
gate-all: gate web-test web

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
