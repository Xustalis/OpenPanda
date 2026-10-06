#!/usr/bin/env python3
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
import json
import os
import signal
import subprocess
import sys
import threading

DEFAULT_TIMEOUT = 600

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
    return Request(
        prompt, timeout, cwd, resume, tools_policy, cmd, max_turns, restricted,
        system_prompt=req.get("system_prompt") or "",
        result_schema=req.get("result_schema") or "",
        mcp_config=req.get("mcp_config") or "",
        max_budget_usd=max_budget,
        effort=req.get("effort") or "",
        session=bool(req.get("session")),
        task_id=req.get("task_id") or "",
    )


class Request:
    """The parsed adapter request; iterates as (prompt, timeout, cwd) so
    prompt, timeout, cwd = read_request() keeps working, with
    resume/tools_policy/cmd/max_turns/restricted as extra attributes."""

    def __init__(self, prompt, timeout, cwd, resume, tools_policy, cmd="",
                 max_turns=0, restricted=False, system_prompt="",
                 result_schema="", mcp_config="", max_budget_usd=0.0,
                 effort="", session=False, task_id=""):
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


def emit_event(ev, **fields):
    """Emit one typed agent-activity event as NDJSON on stderr.

    ev names the block kind — "text", "thinking", "tool_use", "tool_result"
    (plus adapter-specific kinds). Fields pass through after per-field
    bounds; "parent" carries the tool_use id a sub-agent block belongs to,
    which is what lets the display layer nest one agent's activity under the
    Task node that spawned it — the everything-is-a-node view.
    """
    payload = {"type": "event", "ev": ev}
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


def run_stream(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, on_line=None, on_stderr=None):
    """Stream-mode runtime shared by the JSONL / stream-json adapters.

    Spawns cmd with pipes, forwards every stdout line to on_line(line), drains
    stderr on a background thread (forwarding to on_stderr if provided), and enforces
    the timeout over the WHOLE run with a watchdog that kills the process tree.

    Returns (returncode, stderr_text, timed_out). Raises FileNotFoundError
    when the CLI binary is missing; a timeout is reported via timed_out=True
    (the tree is already dead), not via an exception.
    """
    proc = subprocess.Popen(
        cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, env=os.environ.copy(), cwd=cwd,
        **GROUP_KW,
    )
    err_chunks = []
    drain_exc = []

    def drain():
        for chunk in iter(proc.stderr.readline, ""):
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

    timed_out = False
    try:
        for line in proc.stdout:
            if drain_exc:
                raise drain_exc[0]
            if on_line is not None:
                on_line(line)
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


def run_plain(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, input=None):
    """One-shot runtime: capture stdout/stderr verbatim with a hard timeout.

    input (optional) is piped to the child's stdin — the generic adapter's
    {stdin} placeholder delivers the prompt this way, both for CLIs whose
    headless contract reads stdin and for prompts too large for an argv
    element. Unset keeps stdin at DEVNULL so a CLI that asks cannot block.

    Returns (returncode, stdout, stderr). Raises subprocess.TimeoutExpired
    (with the whole tree already killed) when the deadline passes and
    FileNotFoundError when the binary is missing — the same contract the
    adapters had around subprocess.run, so existing except branches keep
    working.
    """
    proc = subprocess.Popen(
        cmd, stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, env=os.environ.copy(), cwd=cwd,
        **GROUP_KW,
    )
    try:
        out, err = proc.communicate(input=input, timeout=timeout)
    except subprocess.TimeoutExpired:
        kill_tree(proc)
        proc.communicate()  # reap + drain: the closed pipes return immediately
        raise
    return proc.returncode, out, err


def run_simple(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, label=None, input=None):
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
               input={"argc": max(0, len(cmd) - 1)})
    try:
        returncode, out, err = run_plain(cmd, cwd=cwd, timeout=timeout, input=input)
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
    proc = subprocess.Popen(
        cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, env=os.environ.copy(), cwd=cwd,
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
