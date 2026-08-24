#!/usr/bin/env python3
"""Adapter: Codex CLI → PANDA Commander.

Protocol (shared with claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `codex exec --json` and reduces its JSONL event stream to the final
agent message. Codex emits one JSON object per line; the event shape has
changed across versions, so both the current item envelopes
(item.completed → agent_message) and the legacy msg envelopes
(msg.type == agent_message / task_complete) are recognised. This adapter
never prints secrets.
"""
import json
import os
import signal
import subprocess
import sys

DEFAULT_TIMEOUT = 600

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

    # PANDA's sandbox is a cwd/env boundary, not OS isolation. Keep Codex's
    # own workspace policy enabled and make the run non-interactive/ephemeral.
    cmd = ["codex", "exec", "--json", "--skip-git-repo-check",
           "--sandbox", "workspace-write", "-c", "approval_policy=\"never\"",
           "--ephemeral"]
    # Model selection is Codex-specific; Anthropic variables must not leak
    # into this provider contract.
    model = os.environ.get("CODEX_MODEL", "")
    if model:
        cmd += ["--model", model]
    cmd.append(prompt)

    # Stream the JSONL output live: tool/command items become progress
    # notes on stderr (see the Go harness progressWriter), so the task
    # timeline fills in while codex works. stderr drains on a thread so a
    # chatty CLI cannot fill the pipe and deadlock the stdout loop.
    import threading
    try:
        proc = subprocess.Popen(
            cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True, env=os.environ.copy(), cwd=cwd,
            **_GROUP_KW,
        )
    except FileNotFoundError:
        _emit(False, "codex binary not found", 127)
        return
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

    lines = []
    try:
        for line in proc.stdout:
            lines.append(line)
            note = _note(line)
            if note:
                _progress(note)
        proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        _kill_tree(proc)
        proc.wait()
        _emit(False, "codex timed out", 124)
        return
    finally:
        finished.set()
        t.join(timeout=2)
    if expired.is_set():
        proc.wait()
        _emit(False, "codex timed out", 124)
        return

    text, tokens = _reduce("".join(lines))
    if not text:
        # No parseable events (older CLI without --json, or a plain error):
        # fall back to whatever the CLI printed.
        text = "".join(lines).strip() or "".join(err_chunks).strip()
    _emit(proc.returncode == 0, text, proc.returncode, tokens)


def _note(line):
    """One progress note from a codex JSONL event line, or None.

    Completed items map to short notes: command executions (name + command),
    file changes (path), MCP tool calls. Agent messages are the answer, not
    progress, so they are skipped here.
    """
    line = line.strip()
    if not line:
        return None
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        return None
    if not isinstance(obj, dict) or obj.get("type") != "item.completed":
        return None
    item = obj.get("item")
    if not isinstance(item, dict):
        return None
    it = item.get("type")
    if it == "command_execution":
        arg = str(item.get("command") or "")[:80]
        return f"shell: {arg}" if arg else "shell command"
    if it == "file_change":
        return f"edit: {item.get('path') or ''}"
    if it == "mcp_tool_call":
        server = item.get("server") or ""
        tool = item.get("tool") or ""
        return f"mcp: {server}.{tool}".strip(".")
    if it == "web_search":
        return f"search: {str(item.get('query') or '')[:60]}"
    return None


def _progress(note):
    sys.stderr.write(json.dumps({"type": "progress", "note": note}, ensure_ascii=False) + "\n")
    sys.stderr.flush()


def _reduce(stdout):
    """Collapse codex's JSONL event stream to (final agent message, tokens)."""
    texts = []
    tokens = None
    for line in stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(obj, dict):
            continue
        # Current format: {"type":"item.completed","item":{"type":"agent_message","text":…}}
        item = obj.get("item")
        if isinstance(item, dict) and item.get("type") == "agent_message" and item.get("text"):
            texts.append(str(item["text"]))
        # Current format usage rides the turn envelope.
        if obj.get("type") == "turn.completed" and isinstance(obj.get("usage"), dict):
            usage = obj["usage"]
            try:
                tokens = int(usage.get("input_tokens", 0)) + int(usage.get("output_tokens", 0))
            except (TypeError, ValueError):
                tokens = None
        # Legacy format: {"msg":{"type":"agent_message","message":…}}
        msg = obj.get("msg")
        if isinstance(msg, dict):
            if msg.get("type") == "agent_message" and msg.get("message"):
                texts.append(str(msg["message"]))
            if msg.get("type") == "task_complete" and msg.get("last_agent_message"):
                texts.append(str(msg["last_agent_message"]))
    # The last agent message is the answer; earlier ones are interstitial.
    return (texts[-1] if texts else ""), tokens


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


if __name__ == "__main__":
    main()
