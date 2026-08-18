# webui — the OpenPanda console

Two consoles live here: the **current one** (Vite + Preact + TypeScript,
embedded into the Go binary via `go:embed`) and the **frozen legacy PWA**
(kept for reference, not developed further).

## Layout

| Path              | What it is                                                            |
| ----------------- | --------------------------------------------------------------------- |
| `app/`            | Console frontend source — Vite + Preact + TS, five-language i18n       |
| `panel/`          | Go panel package: HTTP handlers, SSE hub, `go:embed` of `app` builds   |
| `cmd/panel/`      | Standalone sidecar binary (`panda-webui`) for split deployments        |
| `web/pwa/`        | Legacy PWA assets — **frozen**, kept for reference only                |
| `push/`           | Web Push (VAPID, encryption, subscriptions) — used by both panels      |

## How it ships

`make web` builds `app/` into `panel/dist/app/`, where `go:embed` folds it
into every binary that links the panel package:

- the **daemon** serves it at `network.panel_addr` (loopback by default) and
  the REPL's `/web` command boots it in one click;
- `make build-webui` produces the standalone `panda-webui` sidecar reading
  the same SQLite store.

A committed placeholder in `panel/dist/index.html` keeps `go build` working
without node — vite only ever empties `dist/app`, so a stray `git add -A`
can never leak a hashed index.html into the repo. Ship UI changes only after
a real `make web` build.

## Development

```bash
cd webui/app
npm install
npm run dev         # Vite dev server
npm run typecheck   # tsc --noEmit — required before PRs
npm run build       # outputs to ../panel/dist/app
```

UI strings must go through the i18n layer (`app/src/i18n/`) with the key
present in every locale — English is the fallback, missing keys must not
ship. See [CONTRIBUTING](../CONTRIBUTING.md) for the full conventions.

## The frozen legacy panel

`web/pwa/` is the original hand-rolled PWA from the pre-kernel era. It is
deliberately frozen: no new features, no fixes unless a security issue. The
migration path for anything it did that the new console lacks is a feature
request against `app/`, not a patch to `web/pwa/`.
