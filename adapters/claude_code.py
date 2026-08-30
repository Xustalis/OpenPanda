#!/usr/bin/env python3
"""Adapter: Claude Code CLI → PANDA Commander.

Protocol (shared with codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id}

Progress channel: while the agent runs, NDJSON objects
  {"type": "progress", "note": "Bash: du -ah | sort -rh"}
are written to stderr; the Go harness parses them and records live task
events (see internal/commander/adapter.go progressWriter). Anything else on
stderr is retained for failure diagnosis.

Execution mode: `claude -p --output-format stream-json --verbose` streams
one JSON event per line. Tool-use events become progress notes (the user
sees WHAT the agent is doing as it happens); the final `result` event
carries the answer, the structured token usage, the cost and the session id
a follow-up round can resume with (--resume, so a supervision continuation
keeps the agent's own conversation instead of cold-starting). Older CLIs
without stream-json fall back to the single-shot `--output-format json`
mode. This adapter never prints secrets.

Tool policy: tools_policy=minimal (the default) runs under a safe file-and-
shell whitelist; tools_policy=extended drops the whitelist so the agent's
own Skills, sub-agent Task tool and any MCP servers configured for the work
directory (.mcp.json, written by the Go harness) are reachable. The default
stays minimal: unattended runs only widen their tool face under an explicit
operator choice.

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
    req = harness.read_request()
    prompt, timeout, cwd = req

    model = os.environ.get("CLAUDE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    base = ["claude", "-p", prompt,
            "--max-turns", "30",
            "--permission-mode", "acceptEdits"]
    # minimal (or unset) keeps the safe file-and-shell whitelist; extended
    # leaves the tool face unrestricted so Skills / the Task (sub-agent) tool
    # / project MCP servers work under an explicit operator choice.
    if req.tools_policy != "extended":
        base += ["--allowedTools", ALLOWED_TOOLS]
    # A follow-up round resumes the agent's own session: its reasoning trail
    # survives instead of cold-starting on the bare follow-up instruction.
    if req.resume:
        base += ["--resume", req.resume]

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
                 tokens=out.get("tokens"), cost=out.get("cost"),
                 usage=out.get("usage"), session_id=out.get("session_id"))


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
            _emit_tool_events(ev)
        elif et == "result":
            state["final"] = ev

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line)
    if timed_out:
        raise subprocess.TimeoutExpired(cmd, timeout)

    final = state["final"]
    if final is not None:
        usage = _usage(final.get("usage"))
        tokens = usage["input_tokens"] + usage["output_tokens"]
        return {
            "ok": not final.get("is_error") and returncode == 0,
            "result": final.get("result") or "",
            "exit_code": returncode,
            "tokens": tokens or None,
            "cost": final.get("total_cost_usd"),
            "usage": usage,
            "session_id": final.get("session_id") or "",
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


def _usage(raw):
    """Normalize the result event's usage block to the wire breakdown."""
    raw = raw if isinstance(raw, dict) else {}

    def num(key):
        v = raw.get(key)
        return int(v) if isinstance(v, (int, float)) and v else 0

    return {
        "input_tokens": num("input_tokens"),
        "output_tokens": num("output_tokens"),
        "cache_read_tokens": num("cache_read_input_tokens"),
        "cache_write_tokens": num("cache_creation_input_tokens"),
    }


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
    # Tokens come from the usage block, never from num_turns (a turn is not
    # a token); CLIs too old to report usage leave the field unset.
    usage = _usage(parsed.get("usage"))
    tokens = usage["input_tokens"] + usage["output_tokens"]
    return {
        "ok": returncode == 0 and not parsed.get("is_error"),
        "result": parsed.get("result") or "",
        "exit_code": returncode,
        "tokens": tokens or None,
        "cost": parsed.get("total_cost_usd"),
        "usage": usage,
        "session_id": parsed.get("session_id") or "",
    }


def _emit_tool_events(ev):
    """Emit progress events for one assistant event's tool uses.

    Sub-agent Task tool calls get their own typed progress event
    (kind="subagent") so the orchestration layer can see when the agent
    delegates work to its own sub-agents; everything else becomes a
    regular tool progress note.
    """
    msg = ev.get("message")
    if not isinstance(msg, dict):
        return
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
            arg = arg[:79] + "\u2026"
        note = f"{name}: {arg}" if arg else name
        # Claude's built-in Task tool spawns a sub-agent: surface it as a
        # typed event so the Go harness records it distinctly from an
        # ordinary tool call (the operator sees the delegation chain).
        if name == "Task":
            harness.progress_kind("subagent", note)
        else:
            harness.progress(note)


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
            arg = arg[:79] + "\u2026"
        parts.append(f"{name}: {arg}" if arg else name)
    return " | ".join(parts)


if __name__ == "__main__":
    main()
