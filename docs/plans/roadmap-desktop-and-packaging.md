# Roadmap — desktop client & distribution

Phase 4 planning. The end-state product shape is **CLI as kernel, desktop on
top, web as the always-shipped debug console**:

```
desktop (later)  ──►  cli/kernel  ──►  p2p node network
web console      ──►  (embedded in the same binary, /web)
```

Nothing in this document is a commitment; it is the ordered TODO the project
is steering toward, written down so contributions can align with it.

## Stage 1 — where we are (done 2026-08)

- [x] CLI REPL (`panda repl`) — the operator's seat: bare input to the ask
      engine, slash commands over every panel surface, `/web` boots the
      console in one click.
- [x] Embedded web console — Vite + Preact + TS, `go:embed`, single binary,
      five UI languages, SSE live updates.
- [x] Loopback two-node delegation verified end-to-end, including the
      mutual-dial flap fix (`scripts/smoke-delegate` re-runs the round-trip).

## Stage 2 — distribution hardening (next)

- [ ] **Release channel discipline** — `make release` artifacts exist; add
      signed checksums (SHA256SUMS + minisign), a GitHub Release pipeline,
      and a changelog-per-tag policy.
- [ ] **Homebrew tap** (`brew install xenith/tap/openpanda`) — formula built
      from release artifacts; keep `go install github.com/Xustalis/OpenPanda@…`
      working for Go users.
- [ ] **First-run experience** — `panda init` (interactive config + capability
      card generation, today hand-edited) so a new node reaches the network
      without reading two YAML files.
- [ ] **Packaging** — `.deb`/`.rpm` for the SBC use case (Orange Pi et al.),
      systemd unit templates in `contrib/`, LaunchAgent plist for macOS.
- [ ] **Real-device fleet validation** — run the smoke-delegate matrix
      (macOS ↔ linux-arm64 SBC) from the delegation report on real hardware,
      not just loopback node pairs.

## Stage 3 — desktop client

The desktop app is a *shell over the CLI*, never a second implementation:

- [ ] **Technology choice** — evaluate Wails (Go-native, small binaries,
      reuses panel handlers) vs Tauri (Rust core, bigger ecosystem) vs
      Electron (fallback only). Current lean: **Wails**, because the console
      frontend (`webui/app`) and the Go panel package port as-is.
- [ ] **Architecture** — the desktop app bundles the same kernel binary and
      drives it over the same SQLite + panel API the web console uses. Tray
      resident, global hotkey ask box, native notifications bridged from the
      SSE event stream.
- [ ] **Voice entry graduates** — the sidecar pipeline (wake word → STT →
      LLM → TTS) gets a desktop toggle once microphone validation lands.
- [ ] **Auto-update** — desktop builds check the release channel; kernel
      updates preserve the on-disk DB migrations (`user_version` PRAGMA
      already guarantees forward-only schema).

## Stage 4 — beyond

- [ ] **Skill marketplace** — signed `SKILL.md` packages, discovered and
      installed peer-to-peer.
- [ ] **Mobile companion** — read/approve surface (push notifications
      already exist in `webui/push`); no execution on the phone.
- [ ] **Multi-user households** — node identity per person, capability cards
      per device, shared network, isolated memory.

## Non-goals

- No cloud control plane, ever — the network is your devices.
- No UI rewrites ahead of the desktop decision; `webui/app` is the console
      until Stage 3 starts.
- The legacy PWA (`webui/web/pwa`) stays frozen; migration is forward-only.
