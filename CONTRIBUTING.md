# Contributing to OpenPanda

Thanks for your interest in making OpenPanda better. This document covers the
toolchain, the engineering gates every change must pass, and the conventions
that keep the codebase readable as it grows.

## Prerequisites

| Tool    | Version | Needed for                    |
| ------- | ------- | ----------------------------- |
| Go      | ≥ 1.26.5 | kernel, CLI, panel, tests     |
| Node.js | ≥ 18    | web console (`webui/app`)     |
| Python  | ≥ 3.10  | voice sidecar, agent adapters |

## Getting started

```bash
git clone https://github.com/Xustalis/OpenPanda
cd openpanda
make run            # daemon with the example config
make web            # build the console into webui/panel/dist/app (go:embed)
make build-webui    # standalone panel sidecar with the console embedded
```

The daemon embeds the web console at build time. Without `make web`, a
placeholder page is embedded so `go build` works without node — run
`make web` before shipping any UI change.

For a quick tour of the interactive surface:

```bash
panda repl          # slash commands: /tasks /approve /projects /nodes /web …
```

## Engineering gates

A pull request is merged only when **all** gates are green. Run them locally
before pushing:

```bash
make gate           # build + vet + test + race (the merge gate)
gofmt -l internal/ cmd/ adapters/ webui/panel/   # must print nothing
cd webui/app && npm run typecheck                # web console changes
```

CI runs the same checks as parallel jobs — `lint` (gofmt + vet), `test`,
`race` (scoped to the concurrency-hot packages via `make race-focused`),
`web` (typecheck + node tests + the embedded-console build) and the
cross-platform matrix — plus this full `make gate` remains the local bar;
keep both green.

- **`go test -race ./...`** must pass — the kernel is a concurrent system
  (peer registry, task store, SSE hub); race detector findings are release
  blockers, not warnings.
- Core modules (`internal/core`, `internal/scheduler`, `internal/storage`)
  should stay above **~60% test coverage** where practical. Bug fixes come
  with a regression test that fails before the fix.
- New wire protocol or delegation behavior gets a loopback test — see
  `internal/core/dedup_test.go` and `scripts/smoke-delegate` for the pattern.

## Code conventions

- **Errors**: wrap with `%w`, check with `errors.Is/As`. Never discard an
  error silently; never log *and* return it from the same function.
- **Comments explain why, not what**. A reader six months from now needs the
  invariant, the trade-off, or the incident reference — not a restatement of
  the code. Non-obvious concurrency decisions (lock ordering, why a close
  happens outside a mutex) must be documented at the site.
- **No dead code, no speculative abstractions.** Three similar lines beat a
  premature interface. Delete unused code instead of commenting it out.
- **Concurrency**: every mutex documents what it guards. No lock is held
  across I/O or a channel send. Senders own closing; a goroutine's owner is
  identifiable from its spawn site.
- **Security**: fail closed. Anything that classifies, authorizes, or
  redacts must default to the restrictive branch (see the Tier model in
  `internal/defense`). New config keys with secret values get the 0600
  chmod-and-warn treatment; never log secrets.

## Commit style

Conventional Commits, matched to the history:

```
feat(cli): interactive REPL — slash commands, /web console, five-language i18n
fix(core): deterministic mutual-dial dedup — end the 1s connect/disconnect flap
feat(web): full console — queue/detail/ask/projects/nodes + go:embed single binary
```

`feat`/`fix`/`docs`/`refactor`/`chore`/`test` with a scope from the top-level
layout (`core`, `cli`, `web`, `scheduler`, `defense`, …). The subject line is
imperative and specific enough to survive in `git log --oneline`.

## Web console (webui/app)

- Stack: Vite + Preact + TypeScript, no runtime dependencies beyond Preact.
- **All user-facing strings go through i18n.** Add the key to every locale in
  `webui/app/src/i18n/` (English is the fallback; missing keys fall through
  to it, so never ship an English-only key and "fix it later").
- The same rule applies to the CLI: `internal/i18n/messages.go`.
- Adding a language: copy the English map, translate, register the locale in
  both `internal/i18n/i18n.go` and `webui/app/src/i18n/index.ts`, and add a
  README link. Keep keys greppable — the key is the identifier.

## Pull requests

1. Fork, branch from `main` (`feat/…`, `fix/…`).
2. Keep PRs small and single-purpose; a feature and its refactor are two PRs.
3. Update `CHANGELOG.md` under `[Unreleased]` — Added / Changed / Fixed.
4. Five-language README updates are only required when you change user-facing
   CLI behavior or add a feature to the feature list; translation of your
   paragraph into the other four README languages is appreciated but not
   gating — maintainers will sync translations.
5. `make gate` green → PR → review.

## Reporting security issues

Do not open a public issue for vulnerabilities in the transport auth, Tier
model, or redaction layers. Report privately to the maintainer (see the
security contact in the repository settings) with reproduction steps. The
audit chain (`panda audit verify`) is there exactly so fixes can be verified
against tampering.

## License

By contributing you agree your work is released under the project's
[MIT License](LICENSE).
