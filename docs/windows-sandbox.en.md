# Windows task sandbox: compatibility boundaries

[中文](windows-sandbox.md) · [Security model](../SECURITY.en.md) · [Example configuration](../config.example.local.yaml)

The Windows backend uses native AppContainer and process-creation APIs on
Windows 10+. Each run receives a fresh `WRITE_RESTRICTED` token, AppContainer
package SID and write-restriction SID. `standard` and `strict` refuse launch
if the backend is unavailable or the policy cannot be established safely;
neither automatically falls back to `off`.

## Modes and temporary permissions

| Mode | Windows behavior |
| --- | --- |
| `off` | Filters the environment and sets cwd, without OS isolation. |
| `standard` | Limits writes to the task directory and explicitly allowed writable paths, and applies denied-write paths. Public `ALL_APPLICATION_PACKAGES` / `ALL_RESTRICTED_APPLICATION_PACKAGES` write ACLs outside those roots do not bypass the write restriction. |
| `strict` | Adds denied-read paths by removing this package's read grants and protecting DACL inheritance at denied-read roots. Refuses policies whose read boundary cannot be established safely. |

New ACL grants target only this run's SIDs: the task directory, explicitly
allowed writable directories/files, and read directories needed by the actual
executable and existing absolute-path arguments. File arguments use their
parent directory; directory arguments use that directory. When needed, these
directories receive inheritable read/execute grants. Arbitrary `PATH` entries
and the whole home directory are not automatically opened merely because they
appear in the environment. Existing public AppContainer read permissions still
follow Windows access checks; `standard` is not an exhaustive allowlist of
every readable file.

Each run creates a private Temp directory
inside its AppContainer profile, points the child's `TEMP`/`TMP`/`TMPDIR` there
and adds this run's write-restriction SID grant only to that private Temp;
profile cleanup removes it without opening the host's entire TEMP directory.
When the caller overrides `LOCALAPPDATA`, the run also provisions that nonce's
projected Temp. Its file identity is journaled, and directory-confined removal
cleans it on exit or recovery.

The launching account must be able to establish the necessary temporary ACLs.
An administrator-owned toolchain directory without usable public read access
causes refusal if this run's SIDs cannot be granted the required read access.
Tools that depend on other ungranted directories, user configuration or runtime
data may also fail. An environment variable naming a path grants no file access.

## Runtime compatibility failures

This backend does not promise to run every Windows program. A real Python 3.14
adapter completed its UTF-8 stdin/stdout protocol and temporary-file write with
`off`; `standard` and `strict` failed before executing adapter code. Loader
tracing showed system `bcrypt.dll` requesting read/write access to
`\Device\KsecDD`, which the fresh write-restriction SID denied, producing
`0xc0000142` (`STATUS_DLL_INIT_FAILED`). The same exit code in another program
does not by itself establish that this device caused its failure.

The backend preserves the exit code and emits a `sandbox backend unavailable`
compatibility diagnosis. Isolation remains enabled; it does not automatically
switch to `off` or retry unconfined. A working host installation of Python does
not establish compatibility with this sandbox. Adding broad restricting SIDs
such as `Everyone` would weaken outside-write protection and is not used as a
compatibility repair.

## Policies that are refused

- `strict` refuses a denied-read root or descendant with public AppContainer/
  package or capability read grants, instead of revoking other applications'
  shared access. This includes `ALL_APPLICATION_PACKAGES`,
  `ALL_RESTRICTED_APPLICATION_PACKAGES` and capability SIDs.
- Unsupported ACLs in a denied-read tree, such as conditional/object ACEs or an
  unrestricted DACL, and nested reparse points cause refusal.
- Multiply-linked files already present in a writable root cause refusal,
  preventing a grant from making a path outside the root writable through the
  same file object. This check also covers explicitly allowed writable files.

These are launch-time policy refusals. Execution does not continue by disabling
isolation, opening the whole home directory or changing other applications'
shared ACLs.

## Network and directory operations

`allow_network: true` grants the relevant AppContainer network capabilities but
still inherits AppContainer loopback restrictions. Local model proxies and other
loopback services may be unreachable. The backend does not change machine-wide
loopback exemptions. `allow_network: false` does not grant those capabilities.

Policy objects remain pinned by handles during execution so ACL operations
refer to the same file objects. Windows sharing rules may therefore prevent
renaming, deleting or atomically replacing relevant roots and protected paths.
Tools that need these operations must account for this compatibility limit;
AppContainer does not replace shared-worktree write locks or `--isolate`.

## Exit and recovery

ACL cleanup is ordered after process-tree exit: remove temporary ACEs for this
run's SIDs, restore DACL inheritance state where needed, and delete this run's
AppContainer profile. It does not overwrite other runs' grants or concurrent
administrator changes with an old ACL snapshot. If process-tree exit cannot be
confirmed, the recovery record is retained rather than removing enforcement
early.

An abnormal exit can leave temporary grants, a profile or inheritance protection
behind. The next sandbox launch attempts recovery using the persistent lease.
Recovery checks file-object identity; it does not blindly restore ACLs on a new
object that replaced the recorded path. Failed recovery keeps its record and
reports failure instead of claiming cleanup succeeded.

Implementation entry points: [`sandbox_windows.go`](../internal/security/sandbox_windows.go),
[`sandbox_windows_acl.go`](../internal/security/sandbox_windows_acl.go),
[`sandbox_windows_read.go`](../internal/security/sandbox_windows_read.go),
[`sandbox_windows_journal.go`](../internal/security/sandbox_windows_journal.go).
