#!/usr/bin/env python3
"""Shared adapter runtime for PANDA Commander (pi-style harness layering).

Every adapter under adapters/ is a thin "how do I call this CLI" layer on top
of this module; the shared runtime lives here, once:

  * wire contract  — stdin JSON request {prompt, timeout_s, cwd} and stdout
    JSON result {ok, result, exit_code, tokens, cost} (read_request / emit)
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
GROUP_KW = {"start_new_session": True} if sys.platform != "win32" else {}


def read_request(default_timeout=DEFAULT_TIMEOUT):
    """Read and validate the stdin JSON contract {prompt, timeout_s, cwd}.

    On invalid JSON the unified error result (exit_code 2) is emitted and the
    process exits. Returns (prompt, timeout_s, cwd); cwd is None when unset.
    """
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        emit(False, "invalid request JSON", 2)
        sys.exit(0)
    prompt = req.get("prompt", "")
    try:
        timeout = int(req.get("timeout_s", default_timeout))
    except (TypeError, ValueError):
        timeout = default_timeout
    cwd = req.get("cwd") or None
    return prompt, timeout, cwd


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


def emit(ok, result, exit_code, tokens=None, cost=None):
    """Write the unified adapter result JSON to stdout (one line, flushed)."""
    payload = {
        "ok": bool(ok),
        "result": clamp_result(result),
        "exit_code": exit_code,
    }
    if tokens is not None:
        payload["tokens"] = tokens
    if cost is not None:
        payload["cost"] = cost
    print(json.dumps(payload, ensure_ascii=False))
    sys.stdout.flush()


def progress(note):
    """Emit one progress event as NDJSON on stderr for the Go harness."""
    sys.stderr.write(json.dumps({"type": "progress", "note": note}, ensure_ascii=False) + "\n")
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
    """Kill the CLI and everything it spawned (they hold the pipes open)."""
    if sys.platform != "win32":
        try:
            os.killpg(proc.pid, signal.SIGKILL)
            return
        except OSError:
            pass  # group already gone — fall through to the direct kill
    proc.kill()


def run_stream(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, on_line=None):
    """Stream-mode runtime shared by the JSONL / stream-json adapters.

    Spawns cmd with pipes, forwards every stdout line to on_line(line), drains
    stderr on a background thread, and enforces the timeout over the WHOLE run
    with a watchdog that kills the process tree.

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

    def drain():
        for chunk in iter(proc.stderr.readline, ""):
            err_chunks.append(chunk)
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
            if on_line is not None:
                on_line(line)
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        kill_tree(proc)
        proc.wait()
        timed_out = True
    finally:
        finished.set()
        t.join(timeout=2)
    if expired.is_set():
        proc.wait()
        timed_out = True
    return proc.returncode, "".join(err_chunks), timed_out


def run_plain(cmd, cwd=None, timeout=DEFAULT_TIMEOUT):
    """One-shot runtime: capture stdout/stderr verbatim with a hard timeout.

    Returns (returncode, stdout, stderr). Raises subprocess.TimeoutExpired
    (with the whole tree already killed) when the deadline passes and
    FileNotFoundError when the binary is missing — the same contract the
    adapters had around subprocess.run, so existing except branches keep
    working.
    """
    proc = subprocess.Popen(
        cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, env=os.environ.copy(), cwd=cwd,
        **GROUP_KW,
    )
    try:
        out, err = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        kill_tree(proc)
        proc.communicate()  # reap + drain: the closed pipes return immediately
        raise
    return proc.returncode, out, err


def run_simple(cmd, cwd=None, timeout=DEFAULT_TIMEOUT, label=None):
    """Full plain-adapter main loop: run cmd and emit the unified result.

    stdout is the result (verbatim); on failure the stderr diagnosis is the
    fallback text. Timeout → exit_code 124, missing binary → 127. This is the
    whole adapter for CLIs without a streaming contract.
    """
    label = label or (cmd[0] if cmd else "cli")
    try:
        returncode, out, err = run_plain(cmd, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        emit(False, label + " timed out", 124)
        return
    except FileNotFoundError:
        emit(False, label + " binary not found", 127)
        return
    emit(returncode == 0, out.strip() or err.strip(), returncode)
