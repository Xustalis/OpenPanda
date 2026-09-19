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

# Web dependency install for the gate targets. `npm ci` against the committed
# package-lock.json, not `npm install`: the gate is supposed to answer "does
# this tree build", and `npm install` is free to resolve — and rewrite — a
# different dependency tree than the lockfile every other run and the CI cache
# are pinned to. A dependency change is a deliberate `npm install` plus a
# lockfile commit.
#
# `npm ci` always wipes node_modules and re-downloads, so the gate targets need
# a reachable registry. For offline or metered local work, override it:
#   make web NPM_INSTALL="npm install --no-fund --no-audit"
NPM_INSTALL ?= npm ci --no-fund --no-audit

.PHONY: all build web web-test web-gate build-webui build-darwin-amd64 build-darwin-arm64 build-linux-arm64 build-linux-amd64 build-windows-amd64 build-windows-arm64 \
        release-darwin-amd64 release-darwin-arm64 release-linux-arm64 release-linux-amd64 release-windows-amd64 release-windows-arm64 \
        dev test adapter-test vet fmt fmt-check race race-focused gate gate-all run run-local measure clean icons release package release-local

all: build

# Default: native platform binary (stripped symbols, version from source).
# Run `make web` BEFORE `make build` for a binary that embeds the real web
# console; without it the binary embeds the committed placeholder dist.
# The fmt-check + vet gate prevents "but it compiles on my machine" drift
# where a PR lands with a broken vet warning or unformatted code.
build: fmt-check vet
	$(GO) build -ldflags "$(LDFLAGS_DEV)" -o $(BIN) ./cmd/panda
	@if [ "$$(uname -s)" = "Darwin" ]; then codesign --force --deep --sign - $(BIN) 2>/dev/null || true; fi

# Build the web console (webui/app) into webui/panel/dist/app, where go:embed
# folds it into the panel binary. Requires node/npm. The committed
# dist/index.html placeholder is never touched (vite empties only dist/app).
web:
	cd webui/app && $(NPM_INSTALL) && npm run typecheck && npm run build
	@if [ ! -f webui/panel/dist/app/index.html ]; then \
		echo "make web: dist/app/index.html missing — the build did not land"; exit 1; fi

# Console typecheck + unit tests (node's own runner — no test framework is
# installed). Separate from `make test` because it needs node, which the Go
# gate does not: CI runs it wherever `make web` runs.
web-test:
	cd webui/app && $(NPM_INSTALL) && npm run typecheck && npm test

# The web leg exactly as the CI gate runs it: one install, then typecheck,
# unit tests and the production build. `web-test` and `web` remain available on
# their own, but running those two back to back installs twice and typechecks
# twice for the same answer.
web-gate:
	cd webui/app && $(NPM_INSTALL) && npm run typecheck && npm test && npm run build
	@if [ ! -f webui/panel/dist/app/index.html ]; then \
		echo "web-gate: dist/app/index.html missing — the build did not land"; exit 1; fi

# Web panel sidecar with the embedded console. `make web` first for a real UI;
# without it the binary embeds the committed placeholder.
build-webui: web fmt-check vet
	$(GO) build -ldflags "$(LDFLAGS_DEV)" -o $(BIN)-webui ./webui/cmd/panel

# Cross-compile targets (design doc §4.4)
build-darwin-amd64: fmt-check vet
	GOOS=darwin GOARCH=amd64 $(GO) build -ldflags "-s -w" -o $(BIN)-darwin-amd64 ./cmd/panda

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

# The bundled Python adapters get a black-box leg of their own: fake CLIs on
# PATH assert the argv/env/progress/timeout contracts, so an adapter flag
# change has to be a deliberate contract edit (needs python3; CI runs it as
# its own gate leg).
adapter-test:
	python3 tests/adapter_contract_test.py

# TUI mouse-ownership check. It needs its own leg because the regression it
# catches is invisible to unit tests: it is about the bytes the binary writes to
# the tty (whether it asks for cell-motion reporting, which is what silently
# kills drag-select / double-click / copy). Drives the real binary in a pty, so
# it is skipped where there is no pty — loud, not silent, and CI runs it for
# real on Linux.
tui-pty-test: build
	@if [ "$$(uname -s)" = "Darwin" ] || [ "$$(uname -s)" = "Linux" ]; then \
		PANDA_BIN=$(BIN) python3 scripts/tui-mouse-pty-check.py; \
	else \
		echo "tui-pty-test: skipped — this platform has no pty (run it on macOS/Linux)"; \
	fi

test: adapter-test
	$(GO) test ./...

vet:
	$(GO) vet ./...

# Formatting is part of the gate, not a reviewer's job: gofmt disagreements are
# the one class of diff noise that is entirely mechanical to prevent.
fmt:
	$(GO) fmt ./...

fmt-check:
	@git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "fmt-check: not a git worktree, so 'git ls-files' has nothing to check"; exit 1; }
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

race:
	CGO_ENABLED=1 $(GO) test -race ./...

# race-focused is the race detector scoped to the packages that actually share
# state across goroutines and connections — the CI gate's race leg. `race`
# (full suite) remains the pre-release check; the extra minutes it spends on
# goroutine-free packages buy no detections. CGO is forced on because -race
# requires it and some runners leave it disabled.
race-focused:
	CGO_ENABLED=1 $(GO) test -race ./internal/core/... ./internal/commander/... ./internal/bus/... ./internal/storage/...

# ============================================================================
# CI Gate 清单化 (§稳定性 P1)
#
# 执行顺序：按「成本从低到高」排列，越靠前的检查越快，失败时能快速
# 反馈，避免把机器时间浪费在明知会失败的昂贵步骤上。
#
# gate  — Go-only pipeline（后端特性分支使用）
#   1. fmt-check    (go fmt 格式校验，毫秒级，成本最低)
#   2. vet          (go vet 静态语义检查，秒级)
#   3. build        (实际编译 go build ./...，验证编译通过)
#   4. adapter-test (Python adapter 黑盒契约，秒级)
#   5. test         (go test ./... 单元测试，十秒级)
#   6. race-focused (-race 竞态检测，只覆盖并发热点包，最昂贵，最后才跑)
#
# gate-all — 全栈 pipeline（前端改动 / 合并前自检）
#   在 gate 之上追加：
#   7. web-gate     (npm ci + typecheck + 前端单测 + vite 生产构建，验证 embed 产物)
#   8. tui-pty-test (真实 pty 下的鼠标所有权契约)
#
# 与 CI 的对应关系：
#   gate.yml 的每个 job 都必须能在这里跑出来，否则一个只在 CI 里存在的
#   检查就成了本地不可复现的黑盒。race 腿两边都必须是 race-focused —
#   全量 `make race` 是发版前的超集检查，不是合并门禁（它把绝大部分时间
#   花在本来就不共享状态的包上，换不来任何额外检出）。
#
# 说明：
#   - fmt-check 在 build 目标中也被前置依赖，所以 `make build` 本身
#     就不会放过未格式化的代码；这里单独列出是为报告更清晰。
#   - CGO_ENABLED=0 默认静态化，避免发布二进制出现 libc 依赖。
#   - 版本号从 internal/version/version.go 提取，禁止在 Makefile 中
#     写死 VERSION 默认值，避免版本漂移。
# ============================================================================
gate: fmt-check vet build adapter-test test race-focused

# gate-all is gate + the web pipeline (one install, then typecheck + ui build +
# web tests) + the TUI pty check. CI runs these as separate jobs (see
# .github/workflows/gate.yml); gate-all is the local equivalent for a pre-merge
# pass.
gate-all: gate web-gate tui-pty-test

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
