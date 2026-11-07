# AGENTS.md

[CLAUDE.md](CLAUDE.md) is the single source of truth for AI agents working in
this repository. Read it before making changes. This file is a short search
router, not a second copy of the rules; the full keyword table is
[How to find things](CLAUDE.md#how-to-find-things) in CLAUDE.md. Code is
authoritative when prose and source disagree.

Grep a keyword, then open the linked section and the code entry point:

- **build, test, `make gate`, CI, package** →
  [Build, test, ship](CLAUDE.md#build-test-ship) · `Makefile` ·
  `.github/workflows/gate.yml`
- **PR, merge, tag, release, commit author, CLA** →
  [Repository gates, PRs and releases](CLAUDE.md#repository-gates-prs-and-releases)
  · `.github/workflows/` · `CLA.md`
- **Windows task, PATH marker, launchd, systemd, `TermsVersionCurrent`,
  `ProgressKind`, `EvUserMsg`, `go` directive** →
  [Cross-artifact contracts](CLAUDE.md#cross-artifact-contracts) ·
  `internal/install/` · `webui/panel/events.go`
- **`panda` subcommand, adapter, TUI keys, `/queue`, `/inject`** →
  [Layout](CLAUDE.md#layout) · `cmd/panda/dispatch.go` · `adapters/_harness.py`
  · `cmd/panda/tui_update.go`
- **config key, per-agent model, concurrency, schema** →
  [Layout](CLAUDE.md#layout) · `internal/config/` · `internal/configsync/` ·
  `internal/storage/migrations.go` · `docs/agent-execution.md`
- **i18n, locale, site page, canonical, `data-i18n`, `check-site.py`** →
  [The marketing site](CLAUDE.md#the-marketing-site-site) ·
  `internal/i18n/` · `webui/app/src/i18n/` · `site/` · `scripts/check-site.py`
- **SSE, trace event, steer, memory graph, endpoint refused** →
  [Cross-artifact contracts](CLAUDE.md#cross-artifact-contracts) ·
  `webui/panel/events.go` · `webui/panel/graph.go` ·
  `internal/commander/inject.go` · `internal/doctor/`
- **secrets, redaction, sandbox, localized docs** →
  [Conventions](CLAUDE.md#conventions) · `internal/security/` ·
  `docs/README.md` · `scripts/check-docs.sh`

Read the relevant CLAUDE.md section before landing anything on `main`,
cutting a `v*` tag, editing `site/`, adding a locale key, or touching an
installer or service identifier.
