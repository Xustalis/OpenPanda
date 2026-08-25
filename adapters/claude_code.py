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

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the Claude Code
difference: flags, event shapes and the plain-mode fallback.
"""
import json
import os
import subprocess

import _harness as harness

ALLOWED_TOOLS = "Read,Write,Edit,Bash,Grep,Glob"


class Unsupported(Exception):
    """The CLI rejected a streaming flag — degrade to plain JSON mode."""


def main():
    prompt, timeout, cwd = harness.read_request()

    model = os.environ.get("CLAUDE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    base = ["claude", "-p", prompt,
            "--allowedTools", ALLOWED_TOOLS,
            "--max-turns", "30",
            "--permission-mode", "acceptEdits"]

    try:
        out = _run_stream(base, model, cwd, timeout)
    except Unsupported:
        # Older CLI: stream-json not available — degrade to one-shot JSON.
        try:
            out = _run_plain(base, model, cwd, timeout)
        except subprocess.TimeoutExpired:
            harness.emit(False, "claude timed out", 124)
            return
        except FileNotFoundError:
            harness.emit(False, "claude binary not found", 127)
            return
    except subprocess.TimeoutExpired:
        harness.emit(False, "claude timed out", 124)
        return
    except FileNotFoundError:
        harness.emit(False, "claude binary not found", 127)
        return

    harness.emit(out["ok"], out["result"], out["exit_code"],
                 tokens=out.get("tokens"), cost=out.get("cost"))


def _run_stream(base, model, cwd, timeout):
    """Stream mode: parse event lines, emit progress, return the result event."""
    cmd = base + ["--output-format", "stream-json", "--verbose"]
    if model:
        cmd += ["--model", model]

    state = {"final": None, "saw_event": False}

    def on_line(line):
        ev = harness.parse_json_line(line)
        if ev is None:
            return
        state["saw_event"] = True
        et = ev.get("type")
        if et == "assistant":
            note = _tool_note(ev)
            if note:
                harness.progress(note)
        elif et == "result":
            state["final"] = ev

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line)
    if timed_out:
        raise subprocess.TimeoutExpired(cmd, timeout)

    final = state["final"]
    if final is not None:
        usage = final.get("usage") or {}
        tokens = (usage.get("input_tokens") or 0) + (usage.get("output_tokens") or 0)
        return {
            "ok": not final.get("is_error") and returncode == 0,
            "result": final.get("result") or "",
            "exit_code": returncode,
            "tokens": tokens or None,
            "cost": final.get("total_cost_usd"),
        }

    # No result event: either a flag the CLI rejected (older version) or a
    # hard failure. The flag error degrades to plain mode; anything else
    # surfaces the CLI's stderr.
    low = err.lower()
    if not state["saw_event"] and returncode != 0 and (
            "unknown option" in low or "unrecognized" in low
            or "invalid value" in low):
        raise Unsupported()
    msg = err.strip() or f"claude exited {returncode} without a result event"
    return {"ok": False, "result": msg, "exit_code": returncode or 1}


def _run_plain(base, model, cwd, timeout):
    """One-shot mode for older CLIs: single JSON object on stdout."""
    cmd = base + ["--output-format", "json"]
    if model:
        cmd += ["--model", model]
    returncode, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout)
    try:
        parsed = json.loads(out or "{}")
    except json.JSONDecodeError:
        return {"ok": False, "result": (err or "").strip() or "claude output not JSON",
                "exit_code": returncode}
    return {
        "ok": returncode == 0 and not parsed.get("is_error"),
        "result": parsed.get("result") or "",
        "exit_code": returncode,
        "tokens": parsed.get("num_turns"),
        "cost": None,
    }


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


if __name__ == "__main__":
    main()
