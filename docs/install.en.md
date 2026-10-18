# Installing OpenPanda

English translation of [install.md](install.md)（中文版为准）· English

All three paths give the **same experience** on macOS / Linux / Windows:
a single `panda` executable + agent adapters (`adapters/*.py`) + the
reference actuator drivers (`drivers/panda-*`), installed into the user
directory with no root required.

| Platform | Method                        |
| -------- | ----------------------------- |
| macOS    | `curl … \| sh` (or Homebrew)  |
| Linux    | `curl … \| sh`                |
| Windows  | PowerShell one-liner          |

The installer does four things: **download** the release archive for
your platform/architecture → **SHA-256 verify** → **unpack into the
prefix directory** → **symlink / add to PATH**; then (interactively)
asks whether to **register a boot-time auto-start service**.

## 1. macOS / Linux one-line install

```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

Equivalent explicit invocations:

```bash
sh scripts/install.sh --version 0.0.11   # install a specific version (e.g. stable 0.0.11, the older pre-release 0.0.11-preview; default `latest` = newest stable)
sh scripts/install.sh --prefix /opt/openpanda  # custom install directory
sh scripts/install.sh --lite                   # lite build: no embedded Web console/TUI (Linux only)
sh scripts/install.sh --yes                    # also register auto-start (no prompt)
sh scripts/install.sh --no-service             # do not touch auto-start
```

Default prefix (aligned with Go's XDG/user-directory convention):

```
${XDG_DATA_HOME:-~/.local/share}/openpanda/
├── bin/panda            # the real binary
├── adapters/*.py        # agent adapters (required for daemon-delegated tasks)
├── drivers/panda-*      # reference actuator drivers (servo/mic/camera/serial/screen/GUI input)
├── config.example.yaml
└── capabilities.example-*.yaml
```

`~/.local/bin/panda` is a symlink to the real binary. If the current
shell's PATH does not yet contain `~/.local/bin`, the script — like
`panda install` — appends a marked `export PATH` block to the shell
startup file (`~/.zshrc`, `~/.bashrc`, etc.; with no rc file at all it
creates `~/.profile`), which takes effect in newly opened terminals;
`panda uninstall` removes exactly this marked block.

### Lite builds: Raspberry Pi and pure-CLI devices

```bash
sh scripts/install.sh --lite                  # linux-amd64/arm64/armv7
```

The lite package (`panda-<ver>-lite-linux-<arch>.tar.gz`) drops the
embedded Web console and the Bubble Tea TUI while keeping the daemon,
mesh, DTN, queue, and `panda ask`/`repl` (the classic line interface) —
a smaller binary with no frontend dependency chain, suited to Raspberry
Pi, old-school armv7 (32-bit ARM has only this target), and CLI machines
that cannot run or do not need the Web UI. Running `panda web` on a lite
node shows a notice page; any full build node on the network can serve
the management UI. The source-build equivalents are `make build-lite`
and `build-lite-linux-{amd64,arm64,armv7}`.

## 2. macOS Homebrew

```bash
brew install Xustalis/openpanda/openpanda
```

The formula `deploy/homebrew/openpanda.rb` pulls the binary and adapters
from the GitHub Release and adapts to Apple Silicon (arm64) and Intel
(amd64).

## 3. Windows one-line install

In **PowerShell (5.1+)**:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

Or download first and run:

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.0.11 -Yes
```

Installs to `%LOCALAPPDATA%\OpenPanda\` and adds `bin` to the **user
PATH** (persistent). When run interactively it asks whether to register
a **logon scheduled task** (`schtasks /SC ONLOGON`) running
`panda daemon` in the background.

## 4. From source / Go

```bash
# Source build (requires Go 1.26+; run `make web` first to embed the Web console)
make build            # → bin/panda
go install github.com/Xustalis/OpenPanda/cmd/panda@latest
```

## After install

```bash
panda init      # interactively generate config.yaml and the capability card
panda repl      # enter the interactive command line
panda web       # open the embedded Web console (loopback listener, auto login)
panda doctor    # self-check: binary / PATH / config / adapters / agents ready
```

> The auto-start service launches `panda daemon` (without
> `--config`/`--card`): the daemon discovers configuration the same way
> interactive runs do — the `OPENPANDA_CONFIG_PATH` environment variable
> → the per-user config (macOS `~/Library/Application
> Support/openpanda/config.yaml`, Linux `~/.config/openpanda/config.yaml`,
> i.e. where `panda init` writes) → `/etc/openpanda/config.yaml`; the
> capability card is looked up next to the working directory and the
> config. So run `panda init` first, otherwise the daemon starts with
> default config and no capability card (and cannot execute tasks).

## Auto-start: manual control

- **macOS** (LaunchAgent, user-level, no sudo):

  ```bash
  launchctl unload ~/Library/LaunchAgents/com.openpanda.node.plist   # disable
  launchctl load   ~/Library/LaunchAgents/com.openpanda.node.plist   # enable
  ```

- **Linux** (systemd user unit):

  ```bash
  systemctl --user disable --now openpanda.service   # disable
  systemctl --user enable  --now openpanda.service   # enable
  ```

- **Windows** (logon scheduled task):

  ```powershell
  schtasks /Delete /TN OpenPandaNode /F        # disable and delete
  ```

## Uninstall

The single entry point is `panda uninstall` (identical on all three
platforms) — no more manual directory deletion:

```bash
panda uninstall             # scan → list delete/keep plan → type `confirm` → auto-backup → allowlist delete
```

- **Allowlist deletion**: removes only OpenPanda's own products (the
  binary, the PATH marker block, config.yaml, the database and runtime
  data); `memory/`, `projects/`, `skills/` and other user assets are
  **kept** by default and marked in the listing. Distribution
  directories installed by the one-line script / self-update (`bin/`,
  `adapters/`, `drivers/`, example configs) are also swept — on Linux,
  when they share a root with storage, only distribution files are
  touched and data is kept; a Homebrew install prompts you to run
  `brew uninstall openpanda`, and a source checkout (with
  `go.mod`/`.git`) is never mistaken for an install.

- **Automatic backup**: before deleting, config and data are packed into
  `~/openpanda-backup-<timestamp>.zip` for rollback (`--no-backup`
  skips it).

- **`--dry-run`**: prints the plan and deletes nothing; **`--yes`**:
  skips the confirmation for scripted runs.

### Delete user data too: `--purge`

```bash
panda uninstall --purge
```

`--purge` requires a **second confirmation** beyond the first (type
`purge`): on confirmation, `memory/`, `projects/`, `skills/`, work
directories and other user-level assets are deleted along with the
backup (they are inside the backup zip, so they can still be restored);
declining deletes **nothing**. Combined with `--yes` it skips both
confirmation layers — use with care.

### Backup only, delete nothing: `--backup-only`

```bash
panda uninstall --backup-only
```

Performs just the backup (writes `~/openpanda-backup-<timestamp>.zip`)
and deletes nothing — handy for archiving before an upgrade.

After uninstalling, `panda doctor` re-checks the environment (it should
report `panda` is no longer available).

`panda uninstall` **also disables auto-start** along the way: on macOS
it boots out the LaunchAgent (`launchctl bootout`), on Linux it disables
the user systemd unit (`systemctl --user disable --now`), and on Windows
it deletes the `OpenPandaNode` logon scheduled task (`schtasks
/Delete`). If you registered auto-start manually (e.g. under a different
service name), it is outside the cleanup scope — run the disable command
from "manual control" once more.

## Publishing a new release

1. Merge the change into `main`, confirm that commit's **Gate is green**,
   then tag: `git tag v0.0.11 && git push origin v0.0.11` (two caveats:
   the CHANGELOG must already contain a section for that version or the
   release pipeline refuses to publish; the release pipeline checks the
   Gate conclusion for **the same commit** — if there is no push-triggered
   Gate run, or the conclusion is not success, it refuses to publish and
   reports an error).
2. `.github/workflows/release.yml` builds cross-platform → packages
   `.tar.gz`/`.zip` → generates `checksums.txt` → publishes the GitHub
   Release.
3. Pre-releases (tags with a hyphen, e.g. `v0.0.9-beta`, `v0.0.8-alpha`,
   `v1.0.0-rc1`) are automatically marked **pre-release**: they never
   become the repository's Latest, `releases/latest` still points at the
   last stable release, and the one-line installer only installs stable
   releases; they also do **not** sync the Homebrew tap.
4. Once live:

   - The one-line scripts in the project README / `docs/install.md`
     install the latest **stable** release (to install a pre-release,
     specify it explicitly, e.g. `--version 1.0.0-rc1`);

   - Homebrew users run `brew upgrade openpanda`; only **stable**
     releases produce a formula with a pinned SHA-256 synced to the tap.

## Troubleshooting

- **`panda not found`**: open a new terminal so PATH takes effect, or
  `export PATH="$HOME/.local/bin:$PATH"` manually.

- **Checksum failure**: the download may have been corrupted by a
  proxy/resume — just re-run (the script uses a fresh temporary
  directory).

- **Unsupported system/architecture**: the script reports a clear error;
  releases currently ship `darwin/linux/windows` on `amd64` and `arm64`,
  plus lite builds for `linux-amd64/arm64/armv7` (32-bit ARM is
  lite-only).

- **Daemon won't start**: run `panda doctor` and `panda init` first;
  when `network.shared_secret` is empty the daemon mints one and writes
  it back to the config (`panda init` also pre-generates one) — only if
  minting itself fails does it skip the WS listener and the node falls
  back to standalone (fail-closed, rejecting unauthenticated peers). If
  the node was previously in a mesh and the secret was lost, the daemon
  warns loudly rather than silently rotating the key.

- **A peer across the public internet/NAT can't connect**: when neither
  side can dial the other, configure `punch:<node-id>` (the peer's node
  name) in `network.peers` — hole-punch coordination is relayed through
  already-connected nodes inside the mesh, and both sides' UDP ports
  only need outbound (no inbound mapping). In extreme cases such as
  symmetric NAT where punching fails, retries continue; logs carry the
  `punch offer failed`/`udp:` prefixes for inspection.
  `network.udp_listen: "off"` disables the datagram plane entirely.
