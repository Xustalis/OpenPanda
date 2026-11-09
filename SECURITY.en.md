# Security

Languages: 简体中文（为准）[SECURITY.md](SECURITY.md) · English · [日本語](SECURITY.ja.md) · [Español](SECURITY.es.md) · [Deutsch](SECURITY.de.md)

*English translation of [SECURITY.md](SECURITY.md); if the two disagree, the Chinese version governs.*

OpenPanda is a P2P task-orchestration kernel that runs on your own devices.
This document explains its trust model, deployment red lines, and the
vulnerability-reporting channel. **Read this before connecting any node to a
network.**

## Trust model

- **A single shared secret protects the whole network (P2-8, partially
  mitigated).** The only membership credential on the inter-node bus is
  `shared_secret` (`OPENPANDA_SHARED_SECRET`). The `hello` identity signature
  is `HMAC-SHA256(secret, nodeID + ":" + ts + ":" + nonce)`, binding a
  timestamp and a per-dial nonce to limit replay (see
  [`docs/protocol.md`](docs/protocol.md)). On top of that, every node holds a
  persistent Ed25519 identity key: tier-2 (irreversible) consent travels with
  the task as an `Ed25519(taskID, authorized, ts, ConsentDigest)` signature,
  and the executing side verifies it against the origin node's public key
  recorded in its directory (`internal/core/nodekey.go`); stripped or tampered
  signatures are rejected outright. `panda nodes verify` pins node
  fingerprints manually, and a re-keyed hello from a pinned node is refused.
  **Remaining gap:** a holder of the shared secret can still forge consent
  "from a node whose public key the receiver has never seen" (an inherent
  boundary of multi-hop relay + TOFU — that path prints a warning), and
  unpinned identities are trusted on first use. Therefore the invariant
  "irreversible operations require my approval" only holds on **a single
  machine or a fully trusted LAN**; it stops holding the moment the network
  contains a node you do not fully trust.
- **Transport-encryption boundary.** Since v0.0.11, when both peers advertise
  the `sessaead` capability in `hello` and a shared secret exists, the
  connection arms a per-connection AES-256-GCM right after the handshake —
  the key is HKDF-derived from the shared secret and both hello nonces, bound
  to both identities and fresh per dial; every application frame from then on
  travels sealed, so an on-path observer can neither read the task plane nor
  inject frames. Only links that fail to negotiate session encryption face
  the cleartext gate (evaluated post-handshake, `cleartextOK`): `ws://` dials
  are permitted only to loopback / Tailscale (100.64.0.0/10,
  `fd7a:115e:a214::/48`, `*.ts.net`) or to ranges/hosts listed in
  `network.allow_cleartext_for` — all other destinations must be `wss://`
  (TLS terminated at a reverse proxy), `punch:` peers (the UDP plane), or
  explicitly covered by `network.allow_cleartext`; inbound cleartext is
  judged on the source IP the same way. The UDP datagram plane is wrapped
  end-to-end in AES-256-GCM (keys domain-separated from the shared secret);
  DTN bundle v2+ payloads are likewise AES-256-GCM encrypted and signed, and
  tampered bundles are never delivered. **Cleartext links are only permitted
  on paths already encrypted underneath (loopback / WireGuard / Tailscale /
  TLS proxy), or ones that armed `sessaead` themselves after the handshake.**
- **The audit chain is signed, but anchored inside the database.**
  `task_events` and `audit_log` carry per-row Ed25519 signatures over the
  hash chain (P2-9); verification binds to the local node's public key
  (re-signing after rekeying is rejected), and a signed chain may not regress
  to unsigned rows (anti-truncation). But the signature anchor lives in the
  database itself: **an unsigned history prefix cannot be distinguished from
  one that was wholesale rewritten**. Tightening this further needs external
  attestation (exporting/notarizing the chain head) and remains on the
  roadmap.
- **Self-update verifies a detached Ed25519 signature.** Since
  v0.0.11-preview the official channel ships `checksums.txt.sig` — a
  detached signature over `checksums.txt`. The official release public key
  is compiled into every binary (`internal/version.ReleasePubKey` source
  default; release packaging can rotate it via ldflags), so forged checksums
  travelling the same channel as the archive are refused outright:

  `a84a556bd45ee7743437d7b9b0c4f6e0df34c496f639b5a045d5de484f128560`

  Older builds without a baked key (self-compiled artifacts predating
  v0.0.11-preview) refuse updates by default: set
  `OPENPANDA_UPDATE_PUBKEY=<the key above>` for a verified upgrade onto the
  signed channel, or explicitly `OPENPANDA_UPDATE_UNSIGNED=1` to fall back
  to checksum-only once (hashes and archives cross the same channel — an
  escape hatch, not a posture). `OPENPANDA_UPDATE_REPO` points updates at a
  self-hosted channel; pair it with `OPENPANDA_UPDATE_PUBKEY` set to that
  channel's own key.
- **Skill URL installs are egress-bounded.** `panda skill install <url>` and
  the corresponding panel / MCP paths only permit https (loopback excepted),
  and refuse loopback / private / link-local and other reserved addresses at
  dial time, preventing a node being turned into an intranet-probing
  springboard. A private hub configured via `skills.hub_url` is exempt — that
  is the administrator's own deployment choice.

Consequence: **equal, full trust by default — every node counts as `mesh`
tier, and one compromised node compromises the entire mesh.** Do not extend
the mesh to machines you do not fully control. Since v0.0.11 you can pin a
per-peer trust tier on a peer's identity (`network.peers[].trust` /
`panda nodes trust`, experimental): `guarded` clamps what delegates toward
that peer may carry and admits work it originates under
`network.trust.accept.guarded`, while `open` peers sit in the directory only
and exchange no push-mode delegations. A trust tier is a local, one-sided
assertion — it narrows your exposure toward a peer but does not change the
underlying fact that holding the shared secret means membership.
Screen-capture tasks and their output artifacts carry an even stricter
default: display content moves over mesh links only
(`send.guarded.screen` is the explicit same-owner override) — the executor
stamps `screen_output` at claim time when it resolves a `screen_sensing`
actuator, the mark rides `task_result` so every delegator hop re-stamps its
own row, and the artifact-serving side releases such output to mesh peers
only, participation and signed grants notwithstanding; a trust demotion
cuts an in-flight transfer immediately rather than waiting out the
authorization-cache TTL.

Lifecycle hooks are an optional local extension: off by default; the child inherits a whitelisted environment without model credentials or peer tokens; the command is graded first and a tier-2 command is refused unless `hooks.allow_tier2` is set; every run writes an audit row. Task values reach the command only through `PANDA_HOOK_*` environment variables — placeholders such as `{title}` expand to variable references, not literal text — and on Windows they are passed through `cmd /V:ON` delayed expansion, so a value never enters the command parser.

## Integrity verification

`panda audit verify` (or the panel's `GET /api/audit`) verifies the global
audit chain's hash chain and Ed25519 signatures; `panda audit verify --task
<id>` verifies a single task's event chain. Both bind to the local node's
public key, and missing signatures (within a signed chain), stripped
signatures, or re-signing under a new key are all reported as errors.

## Deployment red lines

Until per-node key distribution / verifiable admission lands (a change to the
trust model, not a patch), the following are mandatory:

1. **Interconnect only over encrypted overlays.** Connect nodes via
   Tailscale / WireGuard or similar; bind `listen_addr` to the overlay
   interface; **never expose the bus port on the public internet or an
   untrusted LAN**. The `sessaead` session cipher gives links on a trusted
   LAN a confidentiality floor, but it authenticates only the shared secret —
   it does not stop active attacks by a secret holder, so the exposure
   surface stays minimal either way. If a link must cross the public
   internet, terminate TLS at a reverse proxy and write the peer as
   `wss://host:port`.
2. **Secrets via environment variables only.** Provide `shared_secret` only
   through `OPENPANDA_SHARED_SECRET`; never write it into `config.yaml` or
   commit it to any repository. Generation example: `openssl rand -hex 32`.
3. **No tier-2 capabilities to unknown remotes.** Grant irreversible
   (tier-2) capabilities in `capabilities.yaml` only to nodes you fully
   trust.
4. **Keep the panel on loopback.** Bind console / panel (`panel_addr`) to
   `127.0.0.1` only; on a non-loopback bind without `panel_token` a one-time
   temporary token is generated with a loud warning — `/api/*` never runs
   bare, but a token over cleartext HTTP can be sniffed on the link, so for
   long-term external exposure configure a stable token or a TLS reverse
   proxy. Do not paste auto-login URLs containing the token into chat or
   ticketing systems.
5. **A new install listens on loopback only; widening it is an explicit
   act.** With `network.exposure_mode: loopback` (the default) the bus, the
   console and the UDP datagram plane all bind `127.0.0.1`, and LAN discovery
   is off. Only `lan` or `public` makes the bus reachable — the datagram plane
   and discovery open with it, and a daemon also generates a `shared_secret`
   and persists it into `config.yaml` at first boot, so the listener always
   starts: an unset secret does not keep the node local. An unknown device can
   open the port and learn the node exists, but cannot pass the HMAC
   membership check or submit a task. Holding that secret is membership, and
   the default trust tier is `mesh`: delegates are accepted automatically,
   which is remote execution. A config written before 0.1.5 that declared its
   own binds resolves to `legacy` and keeps them. On a host with a public IP,
   follow "Public-host deployment" below.

## Public-host deployment

`network.exposure_mode` is the single switch for who may reach this machine:
`loopback` (the default for new installs — every listener bound to loopback),
`lan` (bus reachable on the private network, console stays on loopback),
`public` (reachable from a public interface; requires a stable `shared_secret`
and `panel_token`, disables discovery and refuses cleartext dials) and
`legacy` (keeps the pre-0.1.5 binds exactly as written). Load rewrites
`listen_addr` / `panel_addr` / `udp_listen` / `discovery_*` from it, so
`panda doctor` reports the addresses actually bound. A config that already
declared one of those binds is treated as `legacy`, so upgrading never moves an
existing node's sockets.

`panel_addr` defaults to `127.0.0.1:7840`, so the console itself is not
reachable; the bus, UDP plane and LAN discovery are. `--lan` and
`panda service install --lan` bind the console to `0.0.0.0` as well, where
cleartext HTTP is guarded only by a Bearer token and both the token and task
contents can be sniffed on the path — **do not use this directly on the
public internet**.

Hardening order:

1. **Set `exposure_mode`, then narrow the bind.** On a cloud host use
   `public` (or `lan` plus an overlay address); point `network.listen_addr` at
   the Tailscale / WireGuard overlay interface; set `udp_listen` and
   `discovery_addr` to `off` (a cloud host rarely needs LAN discovery) and
   `discovery_auto_dial` to `false`. Verify with `ss -lntup | grep panda`.
2. **Let the security group admit the overlay only.** Allow inbound traffic
   from the overlay subnet and port only; never expose 7836 / 7837 / 7840 or
   the dynamic preview/devserver ports to `0.0.0.0/0`.
3. **Put the console behind a TLS reverse proxy.** Keep `panel_addr` on
   `127.0.0.1`, terminate TLS at nginx / Caddy, and write peers that cross a
   public link as `wss://host:port` rather than `ws://`.
4. **Credentials and admission.** Inject `shared_secret` only through
   `OPENPANDA_SHARED_SECRET`, set a stable `panel_token` via the environment,
   and confirm the short authentication string by hand in `panda pair` —
   do not skip it with `--yes`. On a non-loopback bind, keep auto-login URLs
   containing the token out of logs, chat and tickets.
5. **Tighten the trust tier.** Configure `guarded` / `open` explicitly for
   peers reached over a public link; do not let the default `mesh`
   "accept delegates automatically" behaviour cover that link. A trust tier
   only tightens policy — it does not replace the secret.

Once the secret leaks, the current model has no per-device revocation:
rotating `shared_secret` exiles every existing node. Per-device authorization
and verifiable admission change the trust model and are outside 0.1.x.

## Reporting vulnerabilities

Do not open public issues for flaws in transport authentication, the
permission-tier model, or the secret-redaction layer. Report them
**privately** to the maintainer (see the security contact in the repository
settings), with reproduction steps. See "Reporting security issues" in
[CONTRIBUTING.md](CONTRIBUTING.md).
