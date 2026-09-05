#!/usr/bin/env python3
"""Adapter: Codex CLI → PANDA Commander.

Protocol (shared with claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id}

Runs `codex exec --json` and reduces its JSONL event stream to the final
agent message. Codex emits one JSON object per line; the event shape has
changed across versions, so both the current item envelopes
(item.completed → agent_message) and the legacy msg envelopes
(msg.type == agent_message / task_complete) are recognised. The session id
rides the session envelope (session_meta / session.created → payload.id);
a follow-up round feeds it back through `codex exec resume <SESSION_ID>`,
so a supervision continuation keeps codex's own conversation instead of
cold-starting. This adapter never prints secrets.

Tool policy: tools_policy=minimal (the default) runs under codex's
workspace-write sandbox; tools_policy=extended escalates to
danger-full-access so the agent can work beyond the work dir under an
explicit operator choice. The approval policy stays non-interactive either
way — an unattended run can never sit on a prompt.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the codex difference:
flags, the JSONL event shapes and the reduction to the final message.
"""
import os

import _harness as harness


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    # PANDA's sandbox is a cwd/env boundary, not OS isolation. Keep Codex's
    # own workspace policy on top: minimal stays workspace-write, extended
    # lifts the filesystem scope (explicit operator choice, mirroring the
    # claude adapter's extended tool face).
    sandbox = "danger-full-access" if req.tools_policy == "extended" else "workspace-write"

    # Non-interactive headless exec; a follow-up round resumes the previous
    # run's session (its plan history and approvals survive) instead of
    # cold-starting on the bare follow-up instruction.
    if req.resume:
        cmd = ["codex", "exec", "resume", req.resume]
    else:
        cmd = ["codex", "exec"]
    cmd += ["--json", "--skip-git-repo-check",
            "--sandbox", sandbox, "-c", "approval_policy=\"never\""]
    if not req.resume:
        # A resumed run continues an existing session; --ephemeral would
        # discard exactly what the resume is meant to keep.
        cmd.append("--ephemeral")
    # Model selection is Codex-specific; Anthropic variables must not leak
    # into this provider contract.
    model = os.environ.get("CODEX_MODEL") or os.environ.get("OPENAI_MODEL", "")
    if model:
        cmd += ["--model", model]
    cmd.append(prompt)

    # Stream the JSONL output live: tool/command items become progress
    # notes on stderr (see the Go harness progressWriter), so the task
    # timeline fills in while codex works.
    lines = []
    state = {"session_id": ""}

    def on_line(line):
        lines.append(line)
        sid = _session_id(line)
        if sid:
            state["session_id"] = sid
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

    text, usage = _reduce("".join(lines))
    if not text:
        # No parseable events (older CLI without --json, or a plain error):
        # fall back to whatever the CLI printed.
        text = "".join(lines).strip() or err.strip()
    tokens = usage["input_tokens"] + usage["output_tokens"] or None
    harness.emit(returncode == 0, text, returncode, tokens,
                 usage=usage, session_id=state["session_id"])


def _session_id(line):
    """The session id from a codex session envelope, or "".

    Current CLIs open with {"type":"session.created","payload":{"id":…}};
    older ones print {"type":"session_meta","payload":{"id":…}}. Both carry
    the id `codex exec resume` accepts.
    """
    obj = harness.parse_json_line(line)
    if obj is None or obj.get("type") not in ("session.created", "session_meta"):
        return ""
    payload = obj.get("payload")
    if isinstance(payload, dict) and payload.get("id"):
        return str(payload["id"])
    return ""


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
    """Collapse codex's JSONL event stream to (final agent message, usage).

    usage is the wire breakdown dict; codex reports input/output on the
    turn envelope (and nothing cost-shaped, so cost stays unset).
    """
    texts = []
    usage = {"input_tokens": 0, "output_tokens": 0}
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
            u = obj["usage"]
            try:
                usage["input_tokens"] = int(u.get("input_tokens", 0))
                usage["output_tokens"] = int(u.get("output_tokens", 0))
            except (TypeError, ValueError):
                pass
        # Legacy format: {"msg":{"type":"agent_message","message":…}}
        msg = obj.get("msg")
        if isinstance(msg, dict):
            if msg.get("type") == "agent_message" and msg.get("message"):
                texts.append(str(msg["message"]))
            if msg.get("type") == "task_complete" and msg.get("last_agent_message"):
                texts.append(str(msg["last_agent_message"]))
    # The last agent message is the answer; earlier ones are interstitial.
    return (texts[-1] if texts else ""), usage


if __name__ == "__main__":
    main()
