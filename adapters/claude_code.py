#!/usr/bin/env python3
"""Adapter: Claude Code CLI → PANDA Commander.

Protocol (shared with codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Progress channel: while the agent runs, NDJSON objects
  {"type": "progress", "note": "Bash: du -ah | sort -rh"}
are written to stderr; the Go harness parses them and records live task
events (see internal/commander/adapter.go progressWriter). Anything else on
stderr is retained for failure diagnosis.

Execution mode: `claude -p --output-format stream-json --verbose` streams
one JSON event per line. Tool-use events become progress notes (the user
sees WHAT the agent is doing as it happens); the final `result` event
carries the answer, token usage, and cost. Older CLIs without stream-json
fall back to the single-shot `--output-format json` mode. This adapter
never prints secrets.
"""
import json
import os
import signal
import subprocess
import sys

DEFAULT_TIMEOUT = 600
ALLOWED_TOOLS = "Read,Write,Edit,Bash,Grep,Glob"

# POSIX: run the CLI in its own process group so a timeout kills the whole
# tree — the CLI's children inherit the stdout pipe and keep it open after
# the parent dies, which would leave the read loop blocked.
_GROUP_KW = {"start_new_session": True} if sys.platform != "win32" else {}


def _kill_tree(proc):
    """Kill the CLI and everything it spawned (they hold the pipes open)."""
    if sys.platform != "win32":
        try:
            os.killpg(proc.pid, signal.SIGKILL)
            return
        except OSError:
            pass  # group already gone — fall through to the direct kill
    proc.kill()


def main():
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        _emit(False, "invalid request JSON", 2)
        return

    prompt = req.get("prompt", "")
    try:
        timeout = int(req.get("timeout_s", DEFAULT_TIMEOUT))
    except (TypeError, ValueError):
        timeout = DEFAULT_TIMEOUT
    cwd = req.get("cwd") or None

    model = os.environ.get("CLAUDE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    base = ["claude", "-p", prompt,
            "--allowedTools", ALLOWED_TOOLS,
            "--max-turns", "30",
            "--permission-mode", "acceptEdits"]

    try:
        out = _run_stream(base, model, cwd, timeout)
    except _Unsupported:
        # Older CLI: stream-json not available — degrade to one-shot JSON.
        try:
            out = _run_plain(base, model, cwd, timeout)
        except subprocess.TimeoutExpired:
            _emit(False, "claude timed out", 124)
            return
        except FileNotFoundError:
            _emit(False, "claude binary not found", 127)
            return
    except subprocess.TimeoutExpired:
        _emit(False, "claude timed out", 124)
        return
    except FileNotFoundError:
        _emit(False, "claude binary not found", 127)
        return

    _emit(out["ok"], out["result"], out["exit_code"],
          tokens=out.get("tokens"), cost=out.get("cost"))


def _run_stream(base, model, cwd, timeout):
    """Stream mode: parse event lines, emit progress, return the result event.

    stderr drains on a background thread so a chatty CLI can never fill the
    pipe buffer and deadlock the stdout loop.
    """
    cmd = base + ["--output-format", "stream-json", "--verbose"]
    if model:
        cmd += ["--model", model]
    proc = subprocess.Popen(
        cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, text=True, cwd=cwd,
        **_GROUP_KW,
    )
    import threading
    err_chunks = []
    def drain():
        for chunk in iter(proc.stderr.readline, ""):
            err_chunks.append(chunk)
        proc.stderr.close()
    t = threading.Thread(target=drain, daemon=True)
    t.start()

    # Watchdog: the stdout loop below blocks on the CLI's pipe, so
    # proc.wait(timeout) can never fire while the CLI is hung mid-stream
    # (pipe still open, no output). A timer thread kills the whole process
    # tree at the deadline instead — the closed pipes unblock both readers
    # and the loop exits, so the timeout is enforced over the WHOLE run,
    # not just the tail after stdout EOF.
    finished = threading.Event()
    expired = threading.Event()
    def watchdog():
        if finished.wait(timeout):
            return  # completed within the deadline
        expired.set()
        _kill_tree(proc)
    threading.Thread(target=watchdog, daemon=True).start()

    final = None
    saw_event = False
    try:
        for line in proc.stdout:
            ev = _parse(line)
            if ev is None:
                continue
            saw_event = True
            et = ev.get("type")
            if et == "assistant":
                note = _tool_note(ev)
                if note:
                    _progress(note)
            elif et == "result":
                final = ev
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        _kill_tree(proc)
        proc.wait()
        raise
    finally:
        finished.set()
        t.join(timeout=2)
    if expired.is_set():
        proc.wait()
        raise subprocess.TimeoutExpired(cmd, timeout)
    err = "".join(err_chunks)

    if final is not None:
        usage = final.get("usage") or {}
        tokens = (usage.get("input_tokens") or 0) + (usage.get("output_tokens") or 0)
        return {
            "ok": not final.get("is_error") and proc.returncode == 0,
            "result": final.get("result") or "",
            "exit_code": proc.returncode,
            "tokens": tokens or None,
            "cost": final.get("total_cost_usd"),
        }

    # No result event: either a flag the CLI rejected (older version) or a
    # hard failure. The flag error degrades to plain mode; anything else
    # surfaces the CLI's stderr.
    low = err.lower()
    if not saw_event and proc.returncode != 0 and (
            "unknown option" in low or "unrecognized" in low
            or "invalid value" in low):
        raise _Unsupported()
    msg = err.strip() or f"claude exited {proc.returncode} without a result event"
    return {"ok": False, "result": msg, "exit_code": proc.returncode or 1}


def _run_plain(base, model, cwd, timeout):
    """One-shot mode for older CLIs: single JSON object on stdout."""
    cmd = base + ["--output-format", "json"]
    if model:
        cmd += ["--model", model]
    result = subprocess.run(
        cmd, capture_output=True, text=True, timeout=timeout, cwd=cwd,
    )
    try:
        out = json.loads(result.stdout or "{}")
    except json.JSONDecodeError:
        return {"ok": False, "result": (result.stderr or "").strip() or "claude output not JSON",
                "exit_code": result.returncode}
    return {
        "ok": result.returncode == 0 and not out.get("is_error"),
        "result": out.get("result") or "",
        "exit_code": result.returncode,
        "tokens": out.get("num_turns"),
        "cost": None,
    }


def _parse(line):
    line = line.strip()
    if not line:
        return None
    try:
        ev = json.loads(line)
    except json.JSONDecodeError:
        return None
    return ev if isinstance(ev, dict) else None


def _tool_note(ev):
    """Summarize one assistant event's tool uses as a progress note."""
    msg = ev.get("message")
    if not isinstance(msg, dict):
        return ""
    parts = []
    for block in msg.get("content") or []:
        if not isinstance(block, dict) or block.get("type") != "tool_use":
            continue
        name = block.get("name", "tool")
        inp = block.get("input") or {}
        arg = ""
        if isinstance(inp, dict):
            for k in ("command", "file_path", "pattern", "path", "url", "query"):
                if inp.get(k):
                    arg = str(inp[k])
                    break
        if len(arg) > 80:
            arg = arg[:79] + "…"
        parts.append(f"{name}: {arg}" if arg else name)
    return " | ".join(parts)


def _progress(note):
    sys.stderr.write(json.dumps({"type": "progress", "note": note}, ensure_ascii=False) + "\n")
    sys.stderr.flush()


def _emit(ok, result, exit_code, tokens=None, cost=None):
    payload = {
        "ok": bool(ok),
        "result": result,
        "exit_code": exit_code,
    }
    if tokens is not None:
        payload["tokens"] = tokens
    if cost is not None:
        payload["cost"] = cost
    print(json.dumps(payload, ensure_ascii=False))
    sys.stdout.flush()


class _Unsupported(Exception):
    pass


if __name__ == "__main__":
    main()
