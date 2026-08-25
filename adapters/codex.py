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

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the codex difference:
flags, the JSONL event shapes and the reduction to the final message.
"""
import os

import _harness as harness


def main():
    prompt, timeout, cwd = harness.read_request()

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
    # timeline fills in while codex works.
    lines = []

    def on_line(line):
        lines.append(line)
        note = _note(line)
        if note:
            harness.progress(note)

    try:
        returncode, err, timed_out = harness.run_stream(
            cmd, cwd=cwd, timeout=timeout, on_line=on_line)
    except FileNotFoundError:
        harness.emit(False, "codex binary not found", 127)
        return
    if timed_out:
        harness.emit(False, "codex timed out", 124)
        return

    text, tokens = _reduce("".join(lines))
    if not text:
        # No parseable events (older CLI without --json, or a plain error):
        # fall back to whatever the CLI printed.
        text = "".join(lines).strip() or err.strip()
    harness.emit(returncode == 0, text, returncode, tokens)


def _note(line):
    """One progress note from a codex JSONL event line, or None.

    Completed items map to short notes: command executions (name + command),
    file changes (path), MCP tool calls. Agent messages are the answer, not
    progress, so they are skipped here.
    """
    obj = harness.parse_json_line(line)
    if obj is None or obj.get("type") != "item.completed":
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


def _reduce(stdout):
    """Collapse codex's JSONL event stream to (final agent message, tokens)."""
    texts = []
    tokens = None
    for line in stdout.splitlines():
        obj = harness.parse_json_line(line)
        if obj is None:
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


if __name__ == "__main__":
    main()
