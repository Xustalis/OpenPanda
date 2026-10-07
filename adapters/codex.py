#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: Codex CLI → PANDA Commander.

Protocol (shared with claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id}
  stderr: NDJSON {"type":"progress","note":str,"kind":str?} status lines,
          plus {"type":"event","ev":...} transcript frames — tool-shaped
          items land as tool_use (on item.started) / tool_result (on
          item.completed), agent_message items as text, reasoning items as
          thinking; see _harness.py

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


class ProviderFailure(Exception):
    """Provider or upstream API failure (e.g. 402, 5xx, quota exhausted, rate limit)."""


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    # PANDA's sandbox is a cwd/env boundary, not OS isolation. Keep Codex's
    # own workspace policy on top: restricted (unconsented remote task) pins
    # the run to read-only — no writes, and codex's sandbox is what stands
    # between the remote prompt and the filesystem, so restricted outranks
    # tools_policy. Otherwise minimal stays workspace-write and extended
    # lifts the filesystem scope (explicit operator choice, mirroring the
    # claude adapter's extended tool face).
    if req.restricted:
        sandbox = "read-only"
    else:
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

    # If PANDA model injection is active, override Codex's provider and model
    # dynamically on the CLI so fallback/injected keys take effect over ~/.codex/config.toml.
    if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1":
        base_url = os.environ.get("OPENAI_BASE_URL", "")
        api_key = os.environ.get("OPENAI_API_KEY", "")
        inj_model = os.environ.get("CODEX_MODEL") or os.environ.get("OPENAI_MODEL", "")
        if base_url:
            cmd += [
                "-c", 'model_provider="custom"',
                "-c", f'model_providers.custom.base_url="{base_url}"',
                "-c", f'model_providers.custom.experimental_bearer_token="{api_key}"',
            ]
        if inj_model:
            cmd += ["-c", f'model="{inj_model}"']
    else:
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
    state = {"session_id": "", "started": set()}

    def on_line(line):
        lines.append(line)
        sid = _session_id(line)
        if sid:
            state["session_id"] = sid
        note = _note(line)
        if note:
            harness.progress(note)
        _emit_item_events(line, state["started"])

        # Fast provider failure detection: if the turn failed with 402/quota/server error,
        # raise ProviderFailure so PANDA's dynamic model injection can rescue immediately.
        obj = harness.parse_json_line(line)
        if obj:
            if obj.get("type") == "turn.failed":
                err_dict = obj.get("error") if isinstance(obj.get("error"), dict) else {}
                err_msg = err_dict.get("message", "")
                raise ProviderFailure(err_msg or "turn failed")
            if obj.get("type") == "error":
                msg = str(obj.get("message") or "")
                low = msg.lower()
                if "402" in low or "quota" in low or "payment required" in low or "rate limit" in low:
                    raise ProviderFailure(msg)

    try:
        returncode, err, timed_out = harness.run_stream(
            cmd, cwd=cwd, timeout=timeout, on_line=on_line)
    except ProviderFailure as e:
        harness.emit(False, f"provider failure: {e}", 1)
        return
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


def _emit_item_events(line, started):
    """Map a codex JSONL item envelope to typed activity events.

    item.started on a tool-shaped item becomes the tool_use row (live: the
    call is underway); item.completed pairs it as tool_result on the same
    item id. A completed item without a seen start (older streams emit only
    completions) gets its tool_use emitted first so the transcript never
    shows an orphaned result. agent_message items become text blocks,
    reasoning items become thinking blocks. Codex has no sub-agent parent
    id, so sub-agent nesting simply never appears here — the transcript
    stays flat, which is the honest shape of what the harness reported.
    `started` is the caller's set of item ids already announced.
    """
    obj = harness.parse_json_line(line)
    if obj is None:
        return
    et = obj.get("type")
    item = obj.get("item")
    if not isinstance(item, dict):
        return
    it = item.get("type")
    iid = str(item.get("id") or "")

    if et == "item.started":
        name, inp = _tool_call(item)
        if name:
            started.add(iid)
            harness.emit_event("tool_use", id=iid, name=name, input=inp)
        return
    if et != "item.completed":
        return
    if it == "agent_message":
        text = str(item.get("text") or "")
        if text.strip():
            harness.emit_event("text", text=text, id=iid)
        return
    if it == "reasoning":
        text = str(item.get("text") or "")
        if text.strip():
            harness.emit_event("thinking", thinking=text, id=iid)
        return
    name, inp = _tool_call(item)
    if name:
        if iid not in started:
            # No item.started ever arrived — emit the call alongside its
            # result so the pair stays complete on the transcript.
            harness.emit_event("tool_use", id=iid, name=name, input=inp)
        out = item.get("aggregated_output") or item.get("output") or ""
        status = str(item.get("status") or "")
        harness.emit_event("tool_result", tool_use_id=iid,
                           is_error=status in ("failed", "error")
                           or bool(item.get("is_error")),
                           content=str(out))


def _tool_call(item):
    """(name, input) for a codex tool-shaped item, or (None, None)."""
    it = item.get("type")
    if it == "command_execution":
        return "shell", {"command": item.get("command") or ""}
    if it == "file_change":
        return "edit", {"path": item.get("path") or "",
                        "kind": item.get("kind") or ""}
    if it == "mcp_tool_call":
        server = item.get("server") or ""
        tool = item.get("tool") or ""
        return f"mcp:{server}.{tool}".rstrip("."), item.get("arguments") or {}
    if it == "web_search":
        return "web_search", {"query": item.get("query") or ""}
    return None, None


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
