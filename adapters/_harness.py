#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Shared adapter runtime for PANDA Commander (pi-style harness layering).

Every adapter under adapters/ is a thin "how do I call this CLI" layer on top
of this module; the shared runtime lives here, once:

  * wire contract  — stdin JSON request {prompt, timeout_s, cwd} and stdout
    JSON result {ok, result, exit_code, tokens, cost} (read_request / emit).
    The request may additionally carry {resume, tools_policy} and the result
    {usage, session_id} — all optional, older sides ignore unknown keys.
    "resume" carries the agent session id a previous run returned so a
    follow-up round continues the agent's own conversation instead of
    cold-starting; "tools_policy" is minimal (adapter's safe tool whitelist)
    or extended (no whitelist restriction). "usage" is the structured token
    breakdown; "session_id" is what a later run can resume with.
  * watchdog       — the timeout covers the WHOLE run, not just the tail
    after stdout EOF: the stream loop blocks on the CLI's pipe, so a timer
    thread kills the process tree at the deadline and the closed pipes
    unblock the readers
  * tree cleanup   — the CLI runs in its own process group; kill_tree
    SIGKILLs the whole group so children cannot hold the pipes open
  * diagnostics    — stderr drains on a background thread (a chatty CLI can
    never fill the pipe buffer and deadlock the loop) and is returned for
    failure diagnosis
  * progress       — NDJSON {"type":"progress","note":…} on stderr, parsed
    by the Go harness (internal/commander/adapter.go progressWriter)
  * events         — NDJSON {"type":"event","ev":…,…} on the same channel,
    carrying the agent's structured activity (assistant text, thinking,
    tool_use/tool_result pairs, sub-agent blocks keyed by parent id).
    The Go harness records them as typed task events; the display layer
    renders them as a nested transcript — every agent activity is a node.

The request may additionally carry {system_prompt, result_schema,
mcp_config, max_budget_usd, effort, session} (see Request). "session"
asks the adapter to keep the agent CLI alive across turns: the adapter
then reads {"type":"user","text":…} lines from stdin and emits one result
envelope per turn on stdout instead of exiting after the first.

This module never prints secrets.
"""
import atexit
import hashlib
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

DEFAULT_TIMEOUT = 600


# ---- cross-process probe cache -------------------------------------------
#
# Adapters are one process per task, so a roster/flags probe memoized in a
# module global still pays its subprocess on every dispatch. This cache lets a
# probe's *successful* answer survive between tasks (keyed by the CLI's own
# fingerprint, so an upgrade invalidates it) without ever being required for
# correctness: every read and write failure is a miss. Secrets are never
# stored — callers cache public catalogue/flag data only.

_CACHE_OFF_VALUES = ("0", "false", "no", "off")


def cache_enabled():
    """Whether cross-process caching is on (OPENPANDA_ADAPTER_CACHE=0 disables)."""
    value = os.environ.get("OPENPANDA_ADAPTER_CACHE", "").strip().lower()
    return value not in _CACHE_OFF_VALUES


def cache_dir():
    """The adapter cache directory, created lazily by cache_put."""
    explicit = os.environ.get("OPENPANDA_CACHE_DIR", "").strip()
    if explicit:
        return explicit
    if sys.platform == "win32":
        base = os.environ.get("LOCALAPPDATA", "").strip()
        if base:
            return os.path.join(base, "openpanda", "cache")
    else:
        base = os.environ.get("XDG_CACHE_HOME", "").strip()
        if not base:
            home = os.path.expanduser("~")
            if home and home != "~":
                base = os.path.join(home, ".cache")
        if base:
            return os.path.join(base, "openpanda")
    # No home to write to (service accounts, containers): a per-user temp dir
    # still shares across the tasks of one session.
    if hasattr(os, "getuid"):
        suffix = str(os.getuid())
    else:
        suffix = re.sub(r"[^A-Za-z0-9_.-]", "_", os.environ.get("USERNAME") or "user")
    return os.path.join(tempfile.gettempdir(), "openpanda-cache-" + suffix)


def _cache_file(name, key):
    digest = hashlib.sha256(key.encode("utf-8")).hexdigest()[:40]
    safe = re.sub(r"[^A-Za-z0-9_.-]", "_", name) or "cache"
    return os.path.join(cache_dir(), "adapter-%s-%s.json" % (safe, digest))


def _private_dir(directory):
    """Create the cache directory 0700, and distrust one already there that is
    not ours. The cache holds model rosters and priced catalogues; the temp-dir
    fallback lives in a world-writable tree, where a directory another user
    created first is theirs to swap files in. Returns False when the directory
    cannot be trusted."""
    try:
        os.makedirs(directory, mode=0o700, exist_ok=True)
        st = os.stat(directory)
        if hasattr(os, "getuid") and st.st_uid != os.getuid():
            return False
        if hasattr(os, "getuid") and st.st_mode & 0o077:
            os.chmod(directory, 0o700)
        return True
    except Exception:
        return False


def _trusted_dir(directory):
    """Whether a cache directory may be read from. Unlike _private_dir this
    never creates or repairs anything: a reader only decides. The temp-dir
    fallback sits in a world-writable tree, so a directory another account
    created first would let it plant the roster a probe reads."""
    try:
        if not os.path.isdir(directory):
            return False
        st = os.stat(directory)
        if hasattr(os, "getuid") and st.st_uid != os.getuid():
            return False
        if hasattr(os, "getuid") and st.st_mode & 0o077:
            return False
        return True
    except Exception:
        return False


def cache_get(name, key, ttl_s):
    """Return a cached value for (name, key), or None on miss/expiry/failure."""
    if not cache_enabled() or not key:
        return None
    if not _trusted_dir(cache_dir()):
        return None
    try:
        with open(_cache_file(name, key), "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        if not isinstance(doc, dict):
            return None
        age = time.time() - float(doc.get("ts", 0))
        # A timestamp in the future means the wall clock moved backwards (an NTP
        # correction, a dual-boot reboot into the older system). Treat it as
        # expired: the alternative is an entry that stays "fresh" until the
        # clock catches up, which can be hours.
        if age < 0 or age > ttl_s:
            return None
        return doc.get("value")
    except Exception:
        return None


def cache_put(name, key, value):
    """Best-effort write of one cached value; failures only cost a future miss."""
    if not cache_enabled() or not key:
        return
    try:
        directory = cache_dir()
        if not _private_dir(directory):
            return
        path = _cache_file(name, key)
        tmp = "%s.%d.tmp" % (path, os.getpid())
        # 0600 through os.open: the default umask would leave the catalogue
        # readable by every account on the machine. fsync before the rename so a
        # power loss cannot publish a truncated file.
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump({"ts": time.time(), "value": value}, fh)
            fh.flush()
            os.fsync(fh.fileno())
        os.replace(tmp, path)
    except Exception:
        pass


def file_fingerprint(path):
    """Identity of one file: absolute path, size and nanosecond mtime.

    An upgrade rewrites the binary, so the fingerprint changes and every
    cached probe keyed by it misses — no TTL guesswork about releases.
    Unreadable paths fingerprint as "" (callers then skip the cache).
    """
    if not path:
        return ""
    try:
        st = os.stat(path)
        return "%s|%d|%d" % (os.path.abspath(path), st.st_size, st.st_mtime_ns)
    except Exception:
        return ""


def which_fingerprint(name):
    """Fingerprint the CLI that name resolves to on PATH ("" when absent)."""
    try:
        resolved = shutil.which(name) or ""
    except Exception:
        resolved = ""
    return file_fingerprint(resolved)


# Live child procs, tracked so a cancellation that reaches the adapter as
# SIGTERM can take the CLI tree down with it: the daemon kills the adapter's
# process GROUP, but GROUP_KW puts every CLI in its own session — outside
# that group's reach. Without this a cancelled task left the agent burning
# tokens as an orphan (observed live: opencode ran 50s+ past the cancel).
# Dead members stay in the set (an adapter process is one request, a handful
# of spawns at most); the sweep poll()s each so a pid reused as another
# group's leader is never signalled.
_LIVE_PROCS = set()


def _popen(*args, **kw):
    proc = subprocess.Popen(*args, **kw)
    _LIVE_PROCS.add(proc)
    return proc


def _reap_children():
    for proc in list(_LIVE_PROCS):
        try:
            if proc.poll() is None:
                kill_tree(proc)
        except Exception:
            pass


# Cleanup registered by adapters that materialise state on disk (a throwaway
# HOME, a provider config carrying the API key, a rewritten CLI settings file).
# atexit alone is not enough: _terminate_children exits with os._exit, which
# skips atexit handlers, so a cancelled run — `panda cancel`, a daemon
# shutdown, a watchdog timeout — would leave that state behind, and on Windows
# %TEMP% survives reboots. One list serves both exit paths.
_CLEANUP = []


def at_cleanup(fn, *args, **kwargs):
    """Register work that must run before the process exits, including signals."""
    _CLEANUP.append((fn, args, kwargs))


def _run_cleanup():
    # LIFO, like atexit: a restore registered after a mkdtemp must run before
    # the directory it reads from disappears.
    while _CLEANUP:
        fn, args, kwargs = _CLEANUP.pop()
        try:
            fn(*args, **kwargs)
        except Exception:
            pass


def _terminate_children(signum, _frame):
    _reap_children()
    _run_cleanup()
    os._exit(128 + (signum or 15))


atexit.register(_run_cleanup)
atexit.register(_reap_children)
if sys.platform != "win32":
    signal.signal(signal.SIGTERM, _terminate_children)
    # A stray Ctrl-C group signal reaches the whole foreground group —
    # the default KeyboardInterrupt unwind would exit leaving the CLI
    # running, the same orphan shape as an uncaught SIGTERM.
    signal.signal(signal.SIGINT, _terminate_children)


def _force_utf8_stdio():
    """Pin the adapter's own stdio to UTF-8, in both directions.

    The wire contract is UTF-8 JSON: the Go harness marshals the request as
    UTF-8 and decodes stdout/stderr the same way. Python on Windows defaults
    redirected stdio to the ANSI codepage instead (cp936 on zh-CN — UTF-8
    mode is not the default until 3.15), so without this the request prompt
    decodes as mojibake before main() ever sees it, and every result/event
    line written back is re-encoded as GBK — which the Go JSON decoder
    replaces with U+FFFD runs. errors="replace" keeps a stray undecodable
    input byte from crashing the read path instead of being reported.
    """
    for stream in (sys.stdin, sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except (AttributeError, ValueError, OSError):
            pass


_force_utf8_stdio()


def decode_cli(raw):
    """Decode agent-CLI output bytes: UTF-8 passes through untouched, and on
    Windows anything else falls back to the system ANSI codepage — the same
    GBK-to-redirected-pipe case executil.normalizeWithCodePage covers on the
    Go side for native captures. Other platforms keep the UTF-8 contract
    with U+FFFD markers rather than guessing an encoding."""
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        if sys.platform == "win32":
            return raw.decode("mbcs", errors="replace")
        return raw.decode("utf-8", errors="replace")

# An adapter's whole result travels as one JSON line on stdout, and the Go side
# retains a bounded amount of it (executil.Capture, 8 MiB): a chatty CLI that
# overruns that cap does not get a truncated result, it gets none at all,
# because the surviving bytes are no longer parseable JSON and the run reads as
# "adapter output not JSON" after the work was already done. So the result is
# bounded here, where it is still structured. 200k characters is far more than
# a human reads and far less than the cap.
MAX_RESULT_CHARS = 200_000
TRUNCATION_MARKER = "\n...[中间已截断，完整输出留在执行节点]...\n"

# POSIX: run the CLI in its own process group so a timeout kills the whole
# tree — the CLI's children inherit the stdout pipe and keep it open after
# the parent dies, which would leave the read loop blocked.
# Windows: CREATE_NEW_PROCESS_GROUP is the closest equivalent, and kill_tree
# uses taskkill /T there because there is no killpg.
if sys.platform == "win32":
    GROUP_KW = {"creationflags": getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)}
else:
    GROUP_KW = {"start_new_session": True}


def resolve_argv(cmd):
    """Resolve cmd[0] to something CreateProcess can actually run on
    Windows; POSIX returns cmd untouched.

    npm/bun-installed agent CLIs land on PATH as `name.cmd` shims. A bare
    Popen([name, ...]) then raises FileNotFoundError for a binary the
    user can plainly run (CreateProcess has no PATHEXT search), and even
    when a shim does run, its %* forwarding mangles quoted/newline
    arguments — the task prompt rides argv, so the CLI errors out with
    its usage line instead of doing work. Resolve through PATH (plus the
    global bin dirs a service-session PATH drops), prefer the real .exe
    the shim wraps, and fall back to COMSPEC invocation with
    list2cmdline quoting.
    """
    if sys.platform != "win32" or not cmd:
        return cmd
    exe = cmd[0]
    if os.path.isfile(exe):
        found = exe
    else:
        found = _win_which(exe)
    if not found:
        return cmd  # let the spawn raise FileNotFoundError honestly
    if found.lower().endswith((".cmd", ".bat")):
        prefix = _shim_exe(found)
        if prefix:
            return prefix + cmd[1:]
        # A wrapper the static unwrap cannot read — a localization launcher,
        # for instance, whose body re-execs whatever `claude` PATH resolves to
        # once its own directory is filtered out. Follow the same rule and take
        # the next candidate, because going through the shell is not survivable
        # for this adapter: a batch file's %* re-expansion mangles quoted
        # arguments, and a pretty-printed JSON Schema handed to
        # `claude --json-schema` arrives as its first character alone — the CLI
        # then fails every run with `--json-schema is not valid JSON: JSON Parse
        # error: Unterminated string`, and the task never starts.
        alt = _win_which_next(os.path.basename(exe), os.path.dirname(found))
        if alt and os.path.normcase(alt) != os.path.normcase(found):
            alt_prefix = _shim_exe(alt) if alt.lower().endswith((".cmd", ".bat")) else [alt]
            if alt_prefix:
                return alt_prefix + cmd[1:]
        comspec = os.environ.get("COMSPEC") or "cmd.exe"
        # Nothing else to try: keep the COMSPEC route, with the one concession
        # the shell can honour — no line breaks inside an argument.
        return [comspec, "/d", "/s", "/c",
                _flatten_newlines(subprocess.list2cmdline([found] + cmd[1:]))]
    return [found] + cmd[1:]


def _win_which_next(exe, skip_dir):
    """The next `exe` on PATH, ignoring the directory that already answered.

    A launcher shim that cannot be statically unwrapped usually exists to
    filter itself out of PATH and re-exec the CLI it shadows (the zh-CN
    wrapper for `claude` does exactly this). Resolving the same way here keeps
    the spawn off the shell entirely: the alternative on PATH is either a real
    `.exe` or an npm shim, and both hand their arguments through untouched.
    """
    if not exe:
        return None
    try:
        skip = os.path.normcase(os.path.abspath(skip_dir))
    except OSError:
        skip = ""
    for d in (os.environ.get("PATH") or "").split(os.pathsep):
        if not d:
            continue
        try:
            if skip and os.path.normcase(os.path.abspath(d)) == skip:
                continue
        except OSError:
            pass
        for ext in (".exe", ".cmd", ".bat"):
            cand = os.path.join(d, exe + ext)
            if os.path.isfile(cand):
                return cand
    return None


def _flatten_newlines(arg):
    """Fold line breaks out of one argument on its way to a .cmd shim.

    A batch file re-expands its arguments through %*, and that expansion ends
    an argument at the first line break: a pretty-printed JSON Schema arrived
    as the single character "{" (→ `--json-schema is not valid JSON`), and a
    multi-line `--append-system-prompt` lost every line after the first.
    A break becomes a space; what is lost is whitespace between JSON tokens or
    between the lines of a prose rider, which neither receiver reads as
    content.
    """
    if "\n" not in arg and "\r" not in arg:
        return arg
    return arg.replace("\r\n", " ").replace("\r", " ").replace("\n", " ")


def _win_which(exe):
    """shutil.which plus the npm/bun/scoop global-bin dirs a headless
    session's PATH often drops (Task Scheduler does not inherit the
    interactive user's environment)."""
    try:
        found = shutil.which(exe)
    except Exception:
        found = None
    if found:
        return found
    dirs = []
    appdata = os.environ.get("APPDATA")
    profile = os.environ.get("USERPROFILE")
    programfiles = os.environ.get("ProgramFiles")
    if appdata:
        dirs.append(os.path.join(appdata, "npm"))
    if profile:
        dirs += [os.path.join(profile, "AppData", "Roaming", "npm"),
                 os.path.join(profile, ".bun", "bin"),
                 os.path.join(profile, "scoop", "shims")]
    if programfiles:
        dirs.append(os.path.join(programfiles, "nodejs"))
    for d in dirs:
        for ext in (".exe", ".cmd", ".bat"):
            cand = os.path.join(d, exe + ext)
            if os.path.isfile(cand):
                return cand
    return None


def _shim_exe(shim_path):
    """Resolve what a .cmd/.bat shim launches, as an argv prefix, or None.

    Three shapes, in order: a sibling `name.exe` (bun and some npm
    packages); a quoted .exe path inside the shim body (%~dp0 resolved);
    or the npm cmd-shim form — `"%_prog%" "%dp0%\\node_modules\\..."`
    — which becomes [node, script]. That last case matters most: it
    bypasses cmd.exe and the shim's %* forwarding entirely, so the task
    prompt rides argv with full fidelity instead of being requoted (and
    silently dropped) by cmd."""
    sibling = os.path.splitext(shim_path)[0] + ".exe"
    if os.path.isfile(sibling):
        return [sibling]
    try:
        with open(shim_path, "r", errors="replace") as fh:
            body = fh.read()
    except OSError:
        return None
    shim_dir = os.path.dirname(shim_path) + os.sep

    def resolve(p):
        p = p.replace("%~dp0", shim_dir).replace("%dp0%", shim_dir)
        p = p.replace("\\", os.sep).replace("/", os.sep)
        return os.path.normpath(p)

    # The npm cmd-shim pattern first: its body also names node.exe, and the
    # generic .exe scan below would mistake the interpreter for the target.
    script = None
    for match in re.finditer(r'"([^"]*%dp0%[^"]*)"', body):
        cand = resolve(match.group(1))
        if os.path.isfile(cand) and not cand.lower().endswith(".exe"):
            script = cand
            break
    if script:
        node = None
        prog = re.search(r'SET\s+"?_prog=(%dp0%\\[^"\r\n]*?\.exe)"?', body, re.I)
        if prog:
            cand = resolve(prog.group(1))
            if os.path.isfile(cand):
                node = cand
        if not node:
            node = _win_which("node") or "node"
        return [node, script]
    # A launcher shim — "<interpreter.exe>" "<script>" %* — the shape a
    # hand-written wrapper or a venv's console script uses (and the shape the
    # adapter contract tests generate on Windows). Both halves are quoted
    # paths, so the pair resolves to [interpreter, script] and the spawn skips
    # cmd.exe and its %* requoting. Without this branch the single-.exe scan
    # below would return the interpreter alone and drop the script, so the
    # resolved command line starts `node --prompt …` and dies in node's own
    # option parser ("bad option: --prompt") — which reads like an adapter
    # bug rather than a shim the harness failed to unwrap. Pairing is by kind
    # rather than by adjacency, so a flag between the two still resolves.
    quoted = [resolve(m.group(1)) for m in re.finditer(r'"([^"]+)"', body)]
    exes = [p for p in quoted
            if p.lower().endswith(".exe") and os.path.isfile(p)]
    scripts = [p for p in quoted
               if not p.lower().endswith((".exe", ".cmd", ".bat"))
               and os.path.isfile(p)]
    if exes and scripts:
        return [exes[0], scripts[0]]
    for match in re.finditer(r'"([^"]+\.exe)"', body):
        cand = resolve(match.group(1))
        if os.path.isfile(cand):
            return [cand]
    return None


def _parse_request(raw, default_timeout):
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        emit(False, "invalid request JSON", 2)
        sys.exit(0)
    if not isinstance(req, dict):
        req = {}
    return req


def read_request(default_timeout=DEFAULT_TIMEOUT):
    """Read and validate the stdin JSON contract.

    On invalid JSON the unified error result (exit_code 2) is emitted and the
    process exits. Returns a Request namespace: prompt, timeout_s, cwd (None
    when unset), resume ("" when unset) and tools_policy ("" when unset).
    """
    raw = sys.stdin.read()
    return _build_request(_parse_request(raw, default_timeout), default_timeout)


def read_request_lined(default_timeout=DEFAULT_TIMEOUT):
    """Line-framed variant of read_request for session-capable adapters.

    The wire request is one JSON object marshalled without embedded newlines,
    so the first stdin line IS the whole request — and in session mode the
    pipe stays open for the turn lines that follow. A whole-stdin read() here
    would deadlock, so session-aware adapters must use this entry point.
    Blank lines are skipped; on EOF or invalid JSON the unified error result
    is emitted and the process exits.
    """
    while True:
        line = sys.stdin.readline()
        if line == "":
            emit(False, "empty adapter request", 2)
            sys.exit(0)
        if line.strip():
            return _build_request(
                _parse_request(line, default_timeout), default_timeout)


def _build_request(req, default_timeout):
    prompt = req.get("prompt", "")
    try:
        timeout = int(req.get("timeout_s", default_timeout))
    except (TypeError, ValueError):
        timeout = default_timeout
    cwd = req.get("cwd") or None
    resume = req.get("resume") or ""
    tools_policy = req.get("tools_policy") or ""
    restricted = bool(req.get("restricted"))
    cmd = req.get("cmd") or ""
    try:
        max_turns = max(0, int(req.get("max_turns", 0) or 0))
    except (TypeError, ValueError):
        max_turns = 0
    try:
        max_budget = float(req.get("max_budget_usd", 0) or 0)
    except (TypeError, ValueError):
        max_budget = 0.0
    try:
        complexity = float(req.get("complexity", 0) or 0)
    except (TypeError, ValueError):
        complexity = 0.0
    return Request(
        prompt, timeout, cwd, resume, tools_policy, cmd, max_turns, restricted,
        system_prompt=req.get("system_prompt") or "",
        result_schema=req.get("result_schema") or "",
        mcp_config=req.get("mcp_config") or "",
        max_budget_usd=max_budget,
        effort=req.get("effort") or "",
        session=bool(req.get("session")),
        task_id=req.get("task_id") or "",
        complexity=complexity,
    )


class Request:
    """The parsed adapter request; iterates as (prompt, timeout, cwd) so
    prompt, timeout, cwd = read_request() keeps working, with
    resume/tools_policy/cmd/max_turns/restricted as extra attributes."""

    def __init__(self, prompt, timeout, cwd, resume, tools_policy, cmd="",
                 max_turns=0, restricted=False, system_prompt="",
                 result_schema="", mcp_config="", max_budget_usd=0.0,
                 effort="", session=False, task_id="", complexity=0.0):
        self.prompt = prompt
        self.timeout = timeout
        self.cwd = cwd
        self.resume = resume
        self.tools_policy = tools_policy
        # cmd is the argv template a generic adapter (generic.py) expands —
        # the card's agents.<name>.command field, verbatim.
        self.cmd = cmd
        # max_turns is a per-task turn cap from the task spec; adapters with
        # a turn-limit flag apply it, the rest ignore it.
        self.max_turns = max_turns
        # restricted marks an unconsented remote-origin run: the adapter must
        # expose a read-only tool face and it OUTRANKS tools_policy. The Go
        # scheduler only sends it to adapters that declare the mode, so a
        # request carrying restricted=true at an adapter that ignores it is
        # already a bug — never widen flags to work around it here.
        self.restricted = restricted
        # system_prompt rides --append-system-prompt on CLIs that have the
        # flag: the Go side puts the static protocol riders there so the user
        # prompt carries only the task, and the stable prefix caches better.
        self.system_prompt = system_prompt
        # result_schema is a JSON Schema string for CLIs with structured
        # output (claude --json-schema): the model's final reply validates
        # against it, so status/question/delegate_requests arrive parsed
        # instead of as text markers. Adapters without the flag ignore it.
        self.result_schema = result_schema
        # mcp_config is a ready-made {"mcpServers":{…}} JSON document for
        # CLIs that accept a config flag (claude --mcp-config) — it carries
        # the panda passthrough servers without writing .mcp.json into the
        # task's work directory.
        self.mcp_config = mcp_config
        # max_budget_usd maps to --max-budget-usd where the CLI has it: the
        # task's cost ceiling enforced inside the harness. 0 = unset.
        self.max_budget_usd = max_budget_usd
        # effort names the CLI's reasoning-effort level (claude --effort).
        self.effort = effort
        # session requests the interactive mode: the adapter keeps the CLI
        # alive and serves one turn per {"type":"user","text":…} stdin line,
        # emitting a result envelope per turn (see run_session).
        self.session = session
        # task_id is the OpenPanda task this run belongs to; it is already
        # injected as PANDA_TASK_ID env by the Go side — adapters that need
        # it in-band (e.g. a flag) read it here.
        self.task_id = task_id
        # complexity is the entry classifier's 0–1 difficulty score for the
        # task; adapters that pick among several served models (opencode)
        # read it to prefer their free tier on simple tasks. 0 = unset.
        self.complexity = complexity

    def __iter__(self):
        return iter((self.prompt, self.timeout, self.cwd))

    def __getitem__(self, i):
        return (self.prompt, self.timeout, self.cwd)[i]


def clamp_result(text, limit=MAX_RESULT_CHARS):
    """Bound a result string, keeping its head and its tail.

    A long run's useful parts are its start (what it set out to do) and its end
    (how it turned out — the accuracy line, the error). Dropping the tail would
    throw away the answer, so the middle goes instead.
    """
    if not isinstance(text, str) or len(text) <= limit:
        return text
    keep = limit - len(TRUNCATION_MARKER)
    if keep < 2:
        return text[:limit]
    head = keep // 2
    return text[:head] + TRUNCATION_MARKER + text[-(keep - head):]


def emit(ok, result, exit_code, tokens=None, cost=None, usage=None,
         session_id=None, extra=None, session_dead=False):
    """Write the unified adapter result JSON to stdout (one line, flushed).

    usage is the optional structured token breakdown (input_tokens /
    output_tokens / cache_* keys); session_id is the agent session a later
    run may resume. Both ride the wire only when set, so consumers that do
    not know them see the classic envelope unchanged. extra is an optional
    dict merged verbatim into the payload — the path for adapter-specific
    fields the harness passes through untouched (structured output, the
    CLI's subagent stats). Reserved envelope keys (ok/result/exit_code/
    tokens/cost/usage/session_id) cannot be overridden through it.

    session_dead marks a session-mode result whose adapter process is gone:
    the Go side's one-shot fallback keys on the session actually being
    dead, and the async process-exit marker races this envelope when the
    adapter kills the CLI on its way out — the flag is the deterministic
    witness.
    """
    payload = {
        "ok": bool(ok),
        "result": clamp_result(result),
        "exit_code": exit_code,
    }
    if tokens is not None:
        payload["tokens"] = tokens
    if cost is not None:
        payload["cost"] = cost
    if isinstance(usage, dict) and any(usage.values()):
        payload["usage"] = usage
    if session_id:
        payload["session_id"] = session_id
    if session_dead:
        payload["session_dead"] = True
    if isinstance(extra, dict):
        for k, v in extra.items():
            if k not in payload:
                payload[k] = v
    print(json.dumps(payload, ensure_ascii=False))
    sys.stdout.flush()


# Field-level bounds for typed events, in UTF-8 BYTES. An event rides one
# stderr line, and the Go harness spills any stderr line past its byte cap
# (maxProgressLine) to diagnostics — so the budgets here must bound wire
# size, not code points: 16000 CJK characters are ~48KB on the wire, and the
# event would split mid-line and be dropped instead of clamped. The CLI's
# own transcript on the node keeps the full content — these bounds are for
# the display feed.
EVENT_FIELD_LIMITS = {
    "text": 16000,
    "thinking": 16000,
    "content": 8000,
    "input": 4000,
    "note": 300,
}
EVENT_FIELD_LIMIT_DEFAULT = 8000
EVENT_TRUNC_MARK = "…[截断]"


def _clamp_str(s, limit):
    """Cut s at limit bytes of UTF-8 without splitting a code point."""
    raw = s.encode("utf-8")
    if len(raw) <= limit:
        return s
    return raw[:limit].decode("utf-8", errors="ignore") + EVENT_TRUNC_MARK


def _clamp_field(key, value):
    limit = EVENT_FIELD_LIMITS.get(key, EVENT_FIELD_LIMIT_DEFAULT)
    if isinstance(value, str):
        return _clamp_str(value, limit)
    if isinstance(value, (dict, list)):
        blob = json.dumps(value, ensure_ascii=False)
        if len(blob.encode("utf-8")) <= limit:
            return value
        # An oversized structure degrades to its truncated JSON text — the
        # display layer renders the string verbatim instead of a tree.
        return _clamp_str(blob, limit)
    return value


def emit_event(ev, phase=None, **fields):
    """Emit one typed agent-activity event as NDJSON on stderr.

    ev names the block kind — "text", "thinking", "tool_use", "tool_result"
    (plus adapter-specific kinds). Fields pass through after per-field
    bounds; "parent" carries the tool_use id a sub-agent block belongs to,
    which is what lets the display layer nest one agent's activity under the
    Task node that spawned it — the everything-is-a-node view.

    phase (optional) tags adapter bookkeeping that is not agent output. A
    "spawn" event says the task was handed to the CLI; the Go side keeps the
    startup-grace silence window open because no model has produced anything
    yet.
    """
    payload = {"type": "event", "ev": ev}
    if phase:
        payload["phase"] = phase
    for k, v in fields.items():
        payload[k] = _clamp_field(k, v)
    sys.stderr.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stderr.flush()


def progress(note):
    """Emit one progress event as NDJSON on stderr for the Go harness."""
    sys.stderr.write(json.dumps({"type": "progress", "note": note}, ensure_ascii=False) + "\n")
    sys.stderr.flush()


def progress_kind(kind, note):
    """Emit one typed progress event with a kind tag.

    The Go harness parses the optional "kind" field and records it as a
    distinct event category (e.g. "subagent" for Claude's Task tool), so
    the orchestration layer can see WHEN the agent spawns sub-agents
    instead of treating them as ordinary tool notes.
    """
    sys.stderr.write(json.dumps(
        {"type": "progress", "kind": kind, "note": note},
        ensure_ascii=False) + "\n")
    sys.stderr.flush()


def parse_json_line(line):
    """Parse one stdout line as a JSON object, or None (blank/invalid/non-dict)."""
    line = line.strip()
    if not line:
        return None
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        return None
    return obj if isinstance(obj, dict) else None


def kill_tree(proc):
    """Kill the CLI and everything it spawned (they hold the pipes open).

    proc.kill() alone kills one process. An agent CLI is a launcher: it spawns
    node, git, ripgrep, its own MCP servers. The survivors inherit the stdout
    pipe, so the read loop never sees EOF and a timed-out task hangs forever
    instead of being reported as a timeout — which is worse than the timeout,
    because the scheduler is still holding a slot for it.
    """
    if sys.platform == "win32":
        # No killpg on Windows; taskkill /T walks the child tree. Its console
        # window is suppressed so a headless node stays headless.
        try:
            subprocess.run(
                ["taskkill", "/T", "/F", "/PID", str(proc.pid)],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0),
                timeout=10,
            )
            return
        except (OSError, subprocess.SubprocessError):
            pass  # taskkill missing or the process already gone
    else:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
            return
        except OSError:
            pass  # group already gone — fall through to the direct kill
    proc.kill()


def run_stream(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, on_line=None, on_stderr=None,
               stop_when=None, idle_after=None, idle_timeout=6.0, input=None):
    """Stream-mode runtime shared by the JSONL / stream-json adapters.

    Spawns cmd with pipes, forwards every stdout line to on_line(line), drains
    stderr on a background thread (forwarding to on_stderr if provided), and enforces
    the timeout over the WHOLE run with a watchdog that kills the process tree.

    input (optional) is written to the child's stdin, which is then closed — the
    streaming twin of run_plain's input. A prompt that does not fit an argv
    element travels this way (see claude_code's WIN_CMDLINE_SAFE): the write
    happens on its own thread because the payload can exceed the pipe buffer
    while the child is still starting up, and a synchronous write would hold
    the stdout reader this function is here to run.

    stop_when is the escape hatch for a CLI that prints its answer and then
    never exits: the line it returns True for ends the run, and the tree is
    killed there instead of waiting out the deadline. grok 1.0.67 does exactly
    that under `-p` — the turn's `end` event is the last thing it writes, and
    the process stays alive afterwards — so without this every grok task sat
    until the 600s watchdog and was then reported as a timeout even though it
    had answered. A stopped run is a completed run: it returns normally, not
    as timed_out. Pick the CLI's true terminator: grok also emits a `usage`
    event per assistant message, and that one arrives before the message's
    tool calls, so stopping there cuts a tool-using turn at its first
    sentence.

    idle_after / idle_timeout cover the CLIs whose terminator is not
    guaranteed. grok writes `end` on most turns and then sometimes just
    doesn't: the answer, its `usage` line and the text are all out, the
    process sits at 0% CPU, and `end` never comes — measured repeatedly at
    00:00 on a text-only prompt that the same command finished in four
    seconds a few minutes earlier. Waiting for `end` there is a hang. So a
    line matching idle_after arms a silence timer: any further line disarms
    it (and re-arms if it matches too), and idle_timeout seconds of quiet end
    the run the same way stop_when does — killed tree, normal return. `usage`
    is the right arm: it is written after every assistant message, so a
    tool-using turn re-arms only after its tool calls have been read.

    Returns (returncode, stderr_text, timed_out). Raises FileNotFoundError
    when the CLI binary is missing; a timeout is reported via timed_out=True
    (the tree is already dead), not via an exception.
    """
    proc = _popen(
        resolve_argv(cmd),
        stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, env=os.environ.copy(), cwd=cwd,
        **GROUP_KW,
    )
    err_chunks = []
    drain_exc = []

    if input is not None:
        def feed():
            try:
                proc.stdin.write(input.encode("utf-8"))
                proc.stdin.flush()
            except (BrokenPipeError, OSError, ValueError):
                # The child exited or closed its end before reading the whole
                # payload. Its stderr already carries the real reason; a write
                # error here is a symptom, not the diagnosis.
                pass
            finally:
                try:
                    proc.stdin.close()
                except (BrokenPipeError, OSError, ValueError):
                    pass

        threading.Thread(target=feed, daemon=True).start()

    def drain():
        for raw in iter(proc.stderr.readline, b""):
            chunk = decode_cli(raw)
            err_chunks.append(chunk)
            if on_stderr is not None:
                try:
                    on_stderr(chunk)
                except Exception as ex:
                    drain_exc.append(ex)
                    kill_tree(proc)
                    break
        proc.stderr.close()

    t = threading.Thread(target=drain, daemon=True)
    t.start()

    finished = threading.Event()
    expired = threading.Event()

    def watchdog():
        if finished.wait(timeout):
            return  # completed within the deadline
        expired.set()
        kill_tree(proc)

    threading.Thread(target=watchdog, daemon=True).start()

    # Silence timer (see idle_after in the docstring). State is shared with
    # the reader loop under a lock; the watcher only ever kills the tree, so
    # the loop below finishes on stdout EOF and the run returns normally.
    idle_lock = threading.Lock()
    idle_state = {"armed": False, "last": time.monotonic(), "fired": False}

    def idle_watch():
        while not finished.is_set():
            if finished.wait(0.5):
                return
            with idle_lock:
                armed, quiet = idle_state["armed"], time.monotonic() - idle_state["last"]
            if armed and quiet >= idle_timeout:
                with idle_lock:
                    idle_state["fired"] = True
                kill_tree(proc)
                return

    if idle_after is not None:
        threading.Thread(target=idle_watch, daemon=True).start()

    timed_out = False
    try:
        for raw_line in proc.stdout:
            if drain_exc:
                raise drain_exc[0]
            line = decode_cli(raw_line)
            if on_line is not None:
                on_line(line)
            if stop_when is not None and stop_when(line):
                # The answer is in hand; the CLI simply does not exit (see the
                # docstring). Kill the tree and stop reading, so the caller
                # gets the run back now instead of at the watchdog.
                kill_tree(proc)
                break
            if idle_after is not None:
                with idle_lock:
                    idle_state["armed"] = bool(idle_after(line))
                    idle_state["last"] = time.monotonic()
        if drain_exc:
            raise drain_exc[0]
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        kill_tree(proc)
        proc.wait()
        timed_out = True
    except Exception:
        kill_tree(proc)
        proc.wait()
        raise
    finally:
        finished.set()
        t.join(timeout=2)
    if expired.is_set():
        proc.wait()
        timed_out = True
    return proc.returncode, "".join(err_chunks), timed_out


def run_plain(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, input=None, heartbeat_s=0):
    """One-shot runtime: capture stdout/stderr verbatim with a hard timeout.

    input (optional) is piped to the child's stdin — the generic adapter's
    {stdin} placeholder delivers the prompt this way, both for CLIs whose
    headless contract reads stdin and for prompts too large for an argv
    element. Unset keeps stdin at DEVNULL so a CLI that asks cannot block.

    heartbeat_s (optional) emits a progress line that often while the child
    runs. A CLI with no machine-readable stream is silent for the whole run,
    and the Go side's silence watchdog cannot tell "thinking for four minutes"
    from "hung" — a heartbeat turns that ambiguity into a visible wait.

    Returns (returncode, stdout, stderr). Raises subprocess.TimeoutExpired
    (with the whole tree already killed) when the deadline passes and
    FileNotFoundError when the binary is missing — the same contract the
    adapters had around subprocess.run, so existing except branches keep
    working.
    """
    proc = _popen(
        resolve_argv(cmd),
        stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, env=os.environ.copy(), cwd=cwd,
        **GROUP_KW,
    )
    stop_beat = threading.Event()
    if heartbeat_s and heartbeat_s > 0:
        label = cmd[0] if cmd else "cli"

        def _beat():
            started = time.monotonic()
            while not stop_beat.wait(heartbeat_s):
                progress("%s still running (%ds)" % (label, int(time.monotonic() - started)))

        threading.Thread(target=_beat, daemon=True).start()
    try:
        out, err = proc.communicate(
            input=input.encode("utf-8") if isinstance(input, str) else input,
            timeout=timeout)
    except subprocess.TimeoutExpired:
        kill_tree(proc)
        proc.communicate()  # reap + drain: the closed pipes return immediately
        raise
    finally:
        stop_beat.set()
    return proc.returncode, decode_cli(out or b""), decode_cli(err or b"")


def resolve_binary(candidates, default=None):
    """First candidate that resolves on PATH.

    Registry and adapter used to disagree about a CLI's name: the roster probe
    accepted any of the registry's binaries while the adapter spawned one
    hardcoded name, so a `claude-code`-only install showed as installed and
    then exited 127 at dispatch. Resolving here keeps both views on the same
    answer.
    """
    for name in candidates:
        if not name:
            continue
        hit = shutil.which(name)
        if hit:
            return name
    return default or (candidates[0] if candidates else "")


def prompt_argv(prompt):
    """The prompt as a trailing argv element, guarded against leading '-'.

    A prompt that begins with a dash ("- please fix …") was parsed as a flag
    by every CLI that takes the message positionally. `--` ends option parsing;
    it is only emitted when it is needed, so the common case keeps the argv the
    contract tests pin.
    """
    if prompt.startswith("-"):
        return ["--", prompt]
    return [prompt]


def run_simple(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, label=None, input=None,
               heartbeat_s=0):
    """Full plain-adapter main loop: run cmd and emit the unified result.

    stdout is the result (verbatim); on failure the stderr diagnosis is the
    fallback text. Timeout → exit_code 124, missing binary → 127, a binary
    that is present but not executable → 126 (the shell convention), and any
    other spawn failure — an argv element past the OS limit, ENOEXEC — → 1
    with the OS error as the diagnosis: an unhandled spawn exception would
    surface upstream as "adapter output not JSON" instead of the real cause.
    input is the optional stdin payload (see run_plain).

    The whole run also lands on the transcript as one tool_use/tool_result
    pair — a CLI without a native event stream still reads as a node: the
    invocation row carries argv shape (count, not contents — the prompt is
    inside argv and must not be re-logged), the result row the bounded
    output.
    """
    label = label or (cmd[0] if cmd else "cli")
    emit_event("tool_use", id=label, name=label,
               input={"argc": max(0, len(cmd) - 1)}, phase="spawn")
    try:
        returncode, out, err = run_plain(cmd, cwd=cwd, timeout=timeout, input=input,
                                         heartbeat_s=heartbeat_s)
    except (subprocess.TimeoutExpired, FileNotFoundError, PermissionError,
            OSError) as ex:
        if isinstance(ex, subprocess.TimeoutExpired):
            diag, code = label + " timed out", 124
        elif isinstance(ex, FileNotFoundError):
            diag, code = label + " binary not found", 127
        elif isinstance(ex, PermissionError):
            diag, code = label + " is not executable", 126
        else:
            diag, code = f"{label} spawn failed: {ex}", 1
        # The invocation node never ran — pair it with an error result so
        # the transcript does not show a tool call that hangs open.
        emit_event("tool_result", tool_use_id=label, is_error=True,
                   content=diag)
        emit(False, diag, code)
        return
    emit_event("tool_result", tool_use_id=label,
               is_error=returncode != 0,
               content=out.strip() or err.strip())
    emit(returncode == 0, out.strip() or err.strip() or "(no output)", returncode)


# ---------------------------------------------------------------------------
# Session mode (stream-json input): the adapter keeps the agent CLI alive and
# serves one turn per stdin line. Used by CLIs whose print mode accepts a
# streaming input channel (claude --input-format stream-json): a supervision
# "continue" verdict then costs a message write instead of a full process
# spawn + session reload.
# ---------------------------------------------------------------------------


def spawn(cmd, cwd=None):
    """Spawn cmd with stdin/stdout pipes and a background stderr drain.

    Unlike run_stream the stdin stays open for the session's whole life.
    Returns (proc, err_chunks): err_chunks is the live list the drain thread
    appends to (join it for diagnostics). Raises FileNotFoundError when the
    binary is missing.
    """
    proc = _popen(
        resolve_argv(cmd), stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace", env=os.environ.copy(), cwd=cwd,
        **GROUP_KW,
    )
    err_chunks = []

    def drain():
        for chunk in iter(proc.stderr.readline, ""):
            err_chunks.append(chunk)
        proc.stderr.close()

    threading.Thread(target=drain, daemon=True).start()
    return proc, err_chunks


def write_line(stream, obj):
    """Write one JSON object as a line to stream and flush.

    Returns False when the pipe is already closed (the CLI died) instead of
    raising BrokenPipeError into the session loop.
    """
    try:
        stream.write(json.dumps(obj, ensure_ascii=False) + "\n")
        stream.flush()
        return True
    except (BrokenPipeError, OSError, ValueError):
        return False


class TurnDeadline:
    """Per-turn watchdog for session mode.

    start() arms a timer that kills the process tree after `seconds`; the
    caller cancels it when the turn's result event lands. A dead process
    ends the session — the loop's next read sees EOF.
    """

    def __init__(self, proc):
        self._proc = proc
        self._timer = None

    def start(self, seconds):
        self.cancel()
        self._timer = threading.Timer(seconds, self._fire)
        self._timer.daemon = True
        self._timer.start()

    def cancel(self):
        if self._timer is not None:
            self._timer.cancel()
            self._timer = None

    def _fire(self):
        kill_tree(self._proc)


def read_turn_lines():
    """Yield the session's turn requests: parsed {"type":"user","text":…}
    objects, skipping blank/invalid lines until stdin closes."""
    for line in sys.stdin:
        msg = parse_json_line(line)
        if msg is None or msg.get("type") != "user":
            continue
        yield msg
