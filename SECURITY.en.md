# Security

English translation of [SECURITY.md](SECURITY.md)（中文版为准）· English

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
- **Self-update supports out-of-band signing (optional).** The default root
  of trust is still GitHub + TLS + the SHA-256 sums in `checksums.txt`.
  Setting `OPENPANDA_UPDATE_PUBKEY` (Ed25519 public key, hex or base64)
  requires releases to carry `checksums.txt.sig` — a detached signature over
  checksums.txt — or the update is refused; `OPENPANDA_UPDATE_REPO` points
  updates at a self-hosted release channel. The code-side capability is
  ready ahead of signing being enabled on the official channel.
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
5. **Loopback listening by default.** The bus listens on loopback by
   default; with no `shared_secret` configured the listener does not start
   at all and the node runs locally only (a security constraint, see
   `docs/install.md`).

## Reporting vulnerabilities

Do not open public issues for flaws in transport authentication, the
permission-tier model, or the secret-redaction layer. Report them
**privately** to the maintainer (see the security contact in the repository
settings), with reproduction steps. See "Reporting security issues" in
[CONTRIBUTING.md](CONTRIBUTING.md).
