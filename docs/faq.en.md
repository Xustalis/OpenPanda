# Frequently Asked Questions (FAQ)

English translation of [faq.md](faq.md)（中文版为准）· English

Install-related troubleshooting lives in
[`install.md`](install.en.md#troubleshooting); this file collects the
high-frequency questions by usage scenario.

## Getting started

**What's the first step after installing?**

```bash
panda init      # interactive walkthrough: node naming, model setup (writes config.yaml + capability card)
panda repl      # interactive command line (bare `panda` works too)
panda web       # or open the Web console (auto login)
```

`panda doctor` self-checks anytime: binary, PATH, config, database, API
key, adapters, agent CLIs.

**How do `panda` and `panda daemon` relate?**

Bare `panda` / `panda repl` is interactive use; `panda daemon` is the
resident background process responsible for task scheduling, node
heartbeats, and reminders. Day-to-day single-machine Q&A needs only the
REPL/web; receiving remote delegated tasks or running reminders needs
the daemon (it can be registered for auto-start — see
[`install.md`](install.en.md#auto-start-manual-control)).

## Model configuration

**Which model services are supported?**

Any OpenAI-compatible or Anthropic-compatible endpoint. Set `api_type`
to `anthropic` (Messages protocol) or `openai` (Chat Completions
protocol). DeepSeek is recommended (default `deepseek-flash`, over
`https://api.deepseek.com/anthropic`).

**Error "model API key not configured"?**

Three ways to configure: `panda init` walks you through it; the
`panda web` settings page (System → Settings) saves it (saving takes
effect immediately, no restart); or the `OPENPANDA_MODEL_API_KEY`
environment variable.

**Error "API key invalid or lacks permission"?**

The key itself is invalid or lacks rights for that model — check in the
provider's console, update `model.api_key`, and retry.

**Error "model endpoint does not exist"?**

`base_url` or the `model` name is wrong. Use the "test connectivity"
button on the `panda web` settings page to isolate which; DeepSeek's
Anthropic endpoint is `https://api.deepseek.com/anthropic` (no `/v1`).

**Error "model service rate-limited / temporarily unavailable"?**

The entry model retries automatically with exponential backoff; a
persistent failure means continued throttling or a service outage — try
again later.

## Agents

**What is an agent adapter?**

OpenPanda does not run long-horizon tasks itself — it delegates to the
agent CLIs installed on each device (Claude Code, Codex, OpenCode, …);
adapters (`adapters/*.py`) are the bridge scripts for the unified
protocol. `panda agents` lists which agents this machine has and which
are missing, with install commands for the missing ones.

**Adapter error "python3 not found"?**

Adapters need Python 3; check `python3 --version`. On macOS,
`brew install python3`.

**`panda agents` shows an agent missing, but I don't want to install it?**

That's fine — the scheduler only routes tasks to devices that advertise
that agent. Remove the corresponding entry from the capability card.

**How do credentials (API keys) reach an agent?**

Default is `injection.model: auto` — an agent with its own login/key
uses it directly; an agent without its own credentials reuses panda's
configured model. The injection surface is driven by the adapter
registry: every adapter declaring a model-env mapping can be injected
(Claude Code, Codex, OpenCode, Grok Build, DeepSeek Harness, OpenClaw,
ZCode, Hermes, Pi; Claude Code is limited to the DeepSeek Anthropic
endpoint). `panda agents` marks each agent's injection support. Switch
policy with `panda config set injection auto|always|never`.

## Tasks and scheduling

**A task stays `pending`?**

Check in order: is the daemon running (`panda nodes`, the running
column); does the capability card exist (`panda doctor`, the adapters
row); is the target agent installed.

**What is tier-2 authorization?**

Commands come in two tiers: tier-1 (reversible: read files, run builds)
and tier-2 (irreversible: git push, delete). Tier-2 commands are refused
by default — `panda ask --authorize "..."`, the REPL `/authorize`, or a
`tier: 1` declaration in the capability card permits them. Refused tasks
land in the `review` queue with a reason.

**A task entered `review`?**

Irreversible operations or a supervision loop exceeding its budget pause
and wait for human approval: `panda queue` to view, `panda task approve
<id>` / `reject <id>` to decide; the Web console's task card has the
same buttons.

**What is scope drift?**

The entry model declares the file range each task may touch (scope);
writes beyond it during execution are intercepted and turned into
review. If a legitimate operation is wrongly intercepted, check that the
task description explicitly lists the file paths to change.

**How do I run one thing across several machines?**

That's a "plan", not a single task: several sequential stages, each
routed to the machine that suits it, with artifacts from upstream stages
automatically packed and carried downstream. Two entry points — write it
as a file (`panda plan example` produces a template, `panda plan run
x.yaml --dry-run` previews the routing), or just say it (`panda ask
"write a training script, run it on the machine with a GPU, send the
conclusion back"` — the entry model splits it into a plan itself when a
machine switch is needed). `panda plan show <id>` shows each stage's
status and artifact wiring.

Note: no plan stage carries tier-2 consent, so an irreversible action in
a pipeline always stops at `review` for your approval — it never runs
just because you authorized the whole sentence.

**Can parallel stages talk to each other?**

Yes. Sibling stages of one plan, or child tasks of one parent, share a
bounded "message board": an agent can leave a short message (a one-line
status, a finding that avoids duplicate digging, a pointer to an output
file), and the peer reads it at the **next supervision tick** — no
running agent blocks on it. Messages come in three granularities
(`status` / `report` / `artifact_ref`), with a default cap of 8 per task
per episode (`messages.budget` is tunable), expiring after 24 hours.
`panda task board <id>` shows the board a task can read. Two boundaries:
it is a channel within one collaboration domain, not private chat —
directed messages are visible to same-domain members on the sender's
node; delivery is best-effort and lost frames self-heal on the next
send. Design details: `docs/plans/design-task-message-board.md`.

**Can I dispatch tasks by speaking?**

`panda voice` is the hands-free entry (desk-pet / mobile scenarios):
wake word → transcription → the exact same path as `panda ask` → prints
and reads the answer aloud, sharing one persistent session with the
REPL / `ask --continue` (speak on one device, keep asking at the
terminal). It needs local speech dependencies: `pip install openwakeword
faster-whisper SpeechRecognition pyaudio numpy` (the default backend
needs no key); missing dependencies print the reason and exit. `--once`
handles a single utterance, `--mute` prints without speaking. The voice
entry has no `--authorize` — irreversible commands always land in
`review`.

## Multiple devices

**How do devices join a network?**

After installing, run `panda init` on each device, then choose either
joining path: **Bluetooth-style pairing** — `panda pair <peer-address>`
on one device starts and shows a short pairing code, `panda pair
confirm` on the other verifies the same code, and the shared secret is
delivered over an encrypted channel automatically — nothing to copy by
hand; **LAN discovery + fingerprint admission** — on the same LAN,
`panda nodes` lists discovered devices awaiting admission, `panda nodes
admit <id>` admits by fingerprint, and `nodes verify` pins an Ed25519
fingerprint manually. The manual path still works: configure the peer
address (`peers`) and the shared secret (`shared_secret` — injecting via
`OPENPANDA_SHARED_SECRET` only is safer) in the `network` section, and
run `panda daemon` on each. `panda nodes` shows the node directory,
online status, and trust tiers.

**Why was the outward listener refused at startup?**

A security constraint: a non-loopback listener requires `shared_secret`
(or `OPENPANDA_SHARED_SECRET`), otherwise the daemon refuses to start.
Loopback listeners are unrestricted, and the Web panel generates a
temporary token automatically.

**Can two nodes run on the same machine?**

A node identity has a singleton lock — a second daemon diagnoses and
exits. For multiple identities use VM nodes (`node.kind: vm` + explicit
`node.identity`).

**Can it look at the screen and click the mouse (computer use)?**

Yes, with a clear boundary. Nodes with a desktop session advertise two
actuators — `hardware:screen_capture` (screenshot) and
`hardware:gui_input` (click/type/key/scroll, tier-2 because
irreversible); `panda detect` auto-detects the bundled
`drivers/panda-screen` and `drivers/panda-input` (on Windows it resolves
the `panda-*.ps1` in the same directory and runs them via `pwsh -File`,
no git-bash needed); headless daemons (no GUI session) do not advertise
either, so GUI tasks are never routed to nodes that must fail. The
typical chain is "screenshot task → the executing node's agent reads the
image and understands the screen → `gui_input` actions → the screenshot
artifact returns": visual understanding happens inside the executing
node's agent — the orchestrating model itself never sees images.
Screenshot artifacts can be previewed/downloaded via `panda task
artifact <id>` or the artifact section of the console's task detail.
Security defaults: display content only moves over mesh-tier links —
screen tasks from non-mesh origins are refused at admission, artifact
pull/push is released only to mesh peers, and a screenshot artifact
handed to a downstream task is likewise constrained (the
`ArtifactRef.screen` provenance mark cascades along stages)
(`network.trust.send.guarded.screen` is the explicit same-owner
override). On macOS the drivers also require the "screen recording" and
"accessibility" grants in System Settings; when missing, the task
reports the real error instead of failing silently.

## Data and maintenance

**Where is data stored?**

In the per-user directory, independent of the install location: macOS
`~/Library/Application Support/openpanda`, Linux
`~/.local/share/openpanda`, Windows `%LOCALAPPDATA%\openpanda-data`
(deliberately distinct from the install directory
`%LOCALAPPDATA%\OpenPanda`, so case-insensitive NTFS cannot fold them
into one). `memory/`, `projects/`, `skills/` are user assets kept by
default on uninstall. The CLI's own state (REPL history, session
mirrors, Web logs) goes to the platform state directory: Linux
`${XDG_STATE_HOME:-~/.local/state}/openpanda`, macOS `~/Library/
Application Support/openpanda/state`, Windows
`%LOCALAPPDATA%\openpanda-data\state`.

When `storage.work_path` points at a user workspace (including the case
where the daemon is started inside a code directory and adopts it),
runtime scratch — per-task directories, plan stages, landed remote
project copies — is gathered under that workspace's `.openpanda/`
instead of sprawling three visible `tasks/`, `plans/`, `projects/`
directories across the project; git repositories additionally get
`.git/info/exclude` written automatically so `git status` stays clean
(your `.gitignore` is untouched). Under the default layout (work_path
is the data directory) scratch stays at the data directory's top level,
alongside `openpanda.db`.

**How do I upgrade?**

`panda web` → System → Update: check for a new version (with a changelog
summary) → download → apply when idle. The daemon notes new versions in
its log (it cannot self-apply — open the Web console). One-line-script
users can also just re-run the install command. Updates default to the
verified channel: the official release public key is compiled into the
binary, no configuration needed; when an old self-compiled build without
a baked key is refused fail-closed, set
`OPENPANDA_UPDATE_PUBKEY=<the official key in SECURITY.md>` or use
`OPENPANDA_UPDATE_UNSIGNED=1` once to step up into the signed channel.

**How do I uninstall completely?**

`panda uninstall` does allowlist deletion with automatic backup;
`--purge` removes user data too; `--dry-run` only prints the plan.

**The Web console won't open / token lost?**

In loopback mode just re-run `panda web` (the URL carries a temporary
token that the frontend clears from the address bar right after
reading). Outward listening requires `OPENPANDA_PANEL_TOKEN`; all
`/api/*` requests (including the SSE event stream) accept only
`Authorization: Bearer` header auth — `?token=` in the URL is refused,
keeping the token out of browser history and proxy logs.

**How long does REPL session context last?**

Persisted per project shard at
`~/.local/state/openpanda/conversations/<project>.json` (`default.json`
outside projects; the old `conversation.json` migrates on first access);
when the 24k-character budget overflows, overflowing history is folded
into a model-written running digest first (automatic compression), and
only when the model is unreachable does it degrade to trimming the
oldest. `/new` clears, `/history` views, `panda ask --continue`
continues; long threads can also `panda session fork` at any turn.
