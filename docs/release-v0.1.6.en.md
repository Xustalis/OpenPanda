# v0.1.6 local test and release checklist

[中文](release-v0.1.6.md) · [Install](install.en.md) · [Status](status.en.md)

v0.1.6 is the source release target. A matching `panda version` proves neither publication nor that a resident process has switched to this source. Fill in the actual release date when cutting the release; until then the changelog date is a planned date.

## Findings and fixes

| Trigger | Effect and fix |
| --- | --- |
| Run package.sh or verify-release.sh without a version | Both previously defaulted to 0.1.5. They now use the source version, with an explicit argument taking priority over the VERSION environment variable |
| Run make install-local alone | It could embed an old console. Web generation now precedes Go compilation and installation |
| A fresh HOME or spaces in its path | A missing PATH directory or unquoted path could fail installation. Paths are quoted and directories created |
| Upgrade only the binary and adapters | Voice helpers and drivers could remain stale. They are now synced too; replacements are atomic per file, copy errors fail the command, and config/data/custom helpers are preserved |
| Verify only the six full archives | Missing lite archives could pass. Verification now requires all nine targets and checksum entries |
| A preparation build has a prefilled ship date | It could claim publication prematurely. The date is now empty until release |

Literal token replies in a workspace were also reproduced failing the source-evidence guard. Explicit literal reproduction is now exempt; implementation questions still require evidence. Installer tests now isolate service-manager calls because temporary HOME alone does not isolate the logged-in user's launchctl/systemctl.

## Install the current source

Requires Go 1.26.11+, Node.js/npm and Python 3. Save active work first.

```sh
make install-local
export PATH="$HOME/.local/bin:$PATH"
command -v panda
panda version
cmp bin/panda "$(command -v panda)"
panda doctor
```

An existing `~/.local/share/openpanda/bin` remains the binary destination with a PATH symlink; otherwise the binary goes directly into `~/.local/bin`. Both supported helper layouts are refreshed. Installation does not edit shell startup files, restart processes, or change node config/model credentials. Diagnose doctor failures individually: a missing third-party CLI or unreachable provider does not mean every executor is broken.

## Resident processes and rollback

An existing process retains its old executable after atomic replacement. Confirm `panda queue --state running` is empty and check for active Web/TUI model turns and Side requests. `panda service restart` restarts the console service only; the daemon has a separate service.

| Daemon platform | Restart an already registered service |
| --- | --- |
| macOS | `launchctl kickstart -k "gui/$(id -u)/com.openpanda.node"` |
| Linux | `systemctl --user restart openpanda.service` |
| Windows PowerShell | `schtasks /End /TN OpenPandaNode` then `schtasks /Run /TN OpenPandaNode` |

Use only the command for the actual registered service. Exit manually launched processes normally, then start the new path. Check the Web page and `/api/version` after startup; refreshing revalidates the entry HTML, while a version number alone cannot identify same-version source builds.

Back up the binary and helpers before installation. For rollback, stop the service, copy the saved executable to a temporary file beside its destination, rename it into place and restore the matching helpers. v0.1.6 adds no schema migration over v0.1.5. A v0.1.4 or older binary cannot open a schema-v67 database. Use a consistent database backup rather than copying only the main SQLite file while it is live.

## Test in order

1. Ask a short Web/TUI question. Check progress, answer and any provider-supplied reasoning summary; a provider without summaries need not display reasoning.
2. Refresh during a long answer and stop it. Check original input, partial answer and stop status, then send another turn and confirm messages stay separate.
3. Run a small file task in a disposable project. Check the delivery report, actual steps, paths and verification, then refresh, copy and export. Unknown/partial verification must remain explicit.
4. Test native Codex, Claude Code and any custom relay separately. Confirm each CLI works independently, then check Panda's selected model, endpoint and result. Preserve native configuration rather than requiring official credentials.
5. Test hard device pins, disconnection recovery and artifact return on two devices. Keep task IDs, timestamps, nodes, actual commands and redacted logs for failures.

## Release gates

```sh
make gate-all
sh scripts/check-docs.sh
make site-check
go mod tidy -diff
go build -tags lite ./...
make package
sh scripts/verify-release.sh
sh scripts/test-installers.sh
git diff --check
```

Finish the Web build before packaging or cross-compiling; do not replace Vite assets while Go is embedding them. Official releases require the release key and checksum signature. Unsigned local packages validate packaging only. Merge through a PR, wait for a green Gate on that exact main SHA, then tag, following repository policy. This checklist does not authorize publication.

## Remaining scope

Local checks do not validate every provider, Windows/Linux hardware, real multi-device networking, voice or actuators. Shared-secret/TOFU trust, cross-session writes to the same paths, UPnP/NAT-PMP, punch/dir signature convergence and daemon-to-sidecar mesh metrics IPC remain documented under [Known limits](status.en.md#known-limits). Previously deleted transcripts cannot be reconstructed. Public-site and GitHub Release publication are separate actions.
