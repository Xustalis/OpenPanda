#!/usr/bin/env python3
"""Adapter: Claude Code CLI → PANDA Commander.

Protocol (shared with codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy, restricted, max_turns,
          system_prompt, result_schema, mcp_config, max_budget_usd,
          effort, session}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id, structured, subagent_stats}

Progress channel: while the agent runs, NDJSON objects are written to
stderr — {"type":"progress","note":…} for the compact status line and
{"type":"event","ev":…,…} for the structured activity stream (assistant
text, thinking, tool_use/tool_result pairs, and sub-agent blocks carrying
the parent tool_use id). The Go harness records both; the display layer
renders the event stream as a nested transcript. Anything else on stderr
is retained for failure diagnosis.

Execution mode: `claude -p --output-format stream-json --verbose` streams
one JSON event per line. With --forward-subagent-text, a Task tool's
sub-agent emits its own assistant/user events tagged with the parent's
tool_use id, so the transcript shows the delegation tree instead of a flat
log. The final `result` event carries the answer, the structured token
usage, the cost, the session id a follow-up round resumes with (--resume),
and — when the request carried result_schema — the validated
structured_output. Older CLIs without stream-json fall back to the
single-shot `--output-format json` mode; older CLIs that lack the newer
flags fall back to the v1 flag set first, so the event stream survives.

Session mode (request "session": true): the adapter reads line-framed
stdin — the request, then one {"type":"user","text":…} per turn — and runs
`claude --input-format stream-json`, forwarding each turn as a user
message and emitting one result envelope per turn. A supervision
"continue" then costs a message write instead of a process spawn and
session reload.

Tool policy: tools_policy=minimal (the default) runs under a safe file-and-
shell whitelist; tools_policy=extended drops the whitelist so the agent's
own Skills, sub-agent Task tool and any MCP servers are reachable.
restricted (unconsented remote task) narrows to a read-only face and
outranks tools_policy: on CLIs that have it this uses the native
--restricted mode (command tools removed, file tools confined to the work
dir); the flag degrade path uses the read-only --allowedTools whitelist.
The default stays minimal: unattended runs only widen their tool face
under an explicit operator choice. --permission-prompts none keeps the
run non-interactive: anything that would prompt is denied rather than
silently waited on.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the Claude Code
difference: flags, event shapes and the plain-mode fallback.
This adapter never prints secrets.
"""
import json
import os
import subprocess

import _harness as harness

ALLOWED_TOOLS = "Read,Write,Edit,Bash,Grep,Glob"

# Read-only face for unconsented remote tasks: no Bash (arbitrary shell), no
# Write/Edit (filesystem mutation), no WebFetch/WebSearch (egress). An agent
# restricted to reads can still answer "what does this code do" — the class of
# work a delegation is legitimately for without consent.
RESTRICTED_TOOLS = "Read,Grep,Glob"


class Unsupported(Exception):
    """The CLI rejected a streaming flag — degrade to an older flag set."""


class ProviderFailure(Exception):
    """The agent CLI's model provider failed with a server or auth error."""


def main():
    # read_request_lined keeps stdin open for session mode's turn lines; in
    # one-shot mode the first (only) line is the whole request.
    req = harness.read_request_lined()

    model = os.environ.get("CLAUDE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    # Turn cap order: the task's own spec.max_turns wins, then the operator's
    # env, then the default. A task that needs a long build-test loop asks for
    # it in-band instead of inheriting a ceiling sized for a quick edit.
    max_turns = str(req.max_turns or os.environ.get("CLAUDE_MAX_TURNS", "30"))

    if req.session:
        _main_session(req, model, max_turns)
        return

    try:
        out = _run_stream(_base_args(req, max_turns, v2=True), req, model,
                          req.cwd, req.timeout,
                          disable_settings=_injected(), v2=True)
    except ProviderFailure as e:
        harness.emit(False, str(e), 1)
        return
    except Unsupported:
        # Middle tier: the CLI streams fine but lacks the v2 flags
        # (--forward-subagent-text, --permission-prompts, …). Retry the
        # stream with the v1 flag set so the event feed survives; only a
        # CLI too old for stream-json at all degrades to plain JSON.
        try:
            out = _run_stream(_base_args(req, max_turns, v2=False), req,
                              model, req.cwd, req.timeout,
                              disable_settings=_injected(), v2=False)
        except ProviderFailure as e:
            harness.emit(False, str(e), 1)
            return
        except Unsupported:
            try:
                out = _run_plain(_base_args(req, max_turns, v2=False),
                                 model, req.cwd, req.timeout)
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
    except subprocess.TimeoutExpired:
        harness.emit(False, "claude timed out", 124)
        return
    except FileNotFoundError:
        harness.emit(False, "claude binary not found", 127)
        return

    harness.emit(out["ok"], out["result"], out["exit_code"],
                 tokens=out.get("tokens"), cost=out.get("cost"),
                 usage=out.get("usage"), session_id=out.get("session_id"),
                 extra=out.get("extra"))


def _injected():
    """True when PANDA injected the model endpoint (credential rescue)."""
    return bool(os.environ.get("OPENPANDA_INJECTED_MODEL") or os.environ.get("ANTHROPIC_BASE_URL"))


def _base_args(req, max_turns, v2):
    """The shared argv head: prompt, turn cap, permission posture, tool face.

    v2 adds the flags only newer CLIs accept (--permission-prompts, the
    native --restricted mode); the v1 tier uses the flag set older CLIs
    already understand so a flag rejection never leaves a run unprotected.
    """
    base = ["claude", "-p", req.prompt,
            "--max-turns", max_turns,
            "--permission-mode", "acceptEdits"]
    if v2:
        # Headless runs must never park on a permission prompt: "none"
        # auto-denies anything the mode does not already allow.
        base += ["--permission-prompts", "none"]
    # restricted (unconsented remote task) narrows to a read-only face and
    # outranks tools_policy — an operator's extended choice widens THEIR
    # tasks, not a peer's unconsented one. Otherwise minimal (or unset) keeps
    # the safe file-and-shell whitelist; extended leaves the tool face
    # unrestricted so Skills / the Task (sub-agent) tool / project MCP
    # servers work under an explicit operator choice.
    if req.restricted:
        if v2:
            # --restricted is the native read-only face (command tools
            # removed, file tools confined to the work dir); --tools pins
            # the whitelist it relaxes.
            base += ["--restricted", "--tools", RESTRICTED_TOOLS]
        else:
            base += ["--allowedTools", RESTRICTED_TOOLS]
    elif req.tools_policy != "extended":
        base += ["--allowedTools", ALLOWED_TOOLS]
    # A follow-up round resumes the agent's own session: its reasoning trail
    # survives instead of cold-starting on the bare follow-up instruction.
    if req.resume:
        base += ["--resume", req.resume]
    return base


def _v2_args(req):
    """Request-driven flags that only newer CLIs understand.

    Everything here degrades through the v2→v1→plain ladder: an old CLI
    rejects the argv at startup, the adapter retries without these, and the
    run keeps the streaming event feed on the v1 set.
    """
    args = ["--forward-subagent-text"]
    if req.system_prompt:
        args += ["--append-system-prompt", req.system_prompt]
    if req.result_schema:
        args += ["--json-schema", req.result_schema]
    if req.mcp_config:
        # The panda passthrough servers ride the flag instead of a .mcp.json
        # written into the task's work dir — no repo pollution, works on
        # read-only trees.
        args += ["--mcp-config", req.mcp_config]
    if req.max_budget_usd > 0:
        args += ["--max-budget-usd", str(req.max_budget_usd)]
    if req.effort:
        args += ["--effort", req.effort]
    if fb := os.environ.get("OPENPANDA_FALLBACK_MODELS", ""):
        args += ["--fallback-model", fb]
    return args


def _stream_argv(base, req, model, disable_settings, v2):
    cmd = base + ["--output-format", "stream-json", "--verbose"]
    if v2:
        cmd += _v2_args(req)
    if disable_settings:
        # Injected-model runs strip every settings source so the user's own
        # settings.json cannot override the injected base URL or model. This
        # flag rides both stream tiers: an ancient CLI that rejects it lands
        # on plain mode, which never resends it.
        cmd += ["--setting-sources", ""]
    # Claude Code CLI only accepts Anthropic model names. When a third-party
    # provider (such as DeepSeek Anthropic API) is injected, foreign model names
    # like 'deepseek-v4-flash' cause the CLI to exit with unrecognized_model.
    # Leaving it to default lets Claude Code negotiate with the provider endpoint.
    if model and not any(model.lower().startswith(p) for p in ("deepseek", "openai", "gpt")):
        cmd += ["--model", model]
    return cmd


def _run_stream(base, req, model, cwd, timeout, disable_settings=False, v2=True):
    """Stream mode: parse event lines, emit progress + typed events, return
    the result event."""
    cmd = _stream_argv(base, req, model, disable_settings, v2)

    state = {"final": None, "saw_event": False}

    def on_line(line):
        ev = harness.parse_json_line(line)
        if ev is None:
            return
        state["saw_event"] = True
        et = ev.get("type")
        if et == "assistant":
            _emit_assistant_events(ev)
        elif et == "user":
            _emit_user_events(ev)
        elif et == "result":
            state["final"] = ev
        elif et == "system":
            subtype = ev.get("subtype")
            if subtype == "thinking_tokens":
                delta = ev.get("estimated_tokens_delta", 0)
                if delta and delta > 0:
                    harness.progress("Claude: thinking…")
            elif subtype == "api_retry":
                attempt = ev.get("attempt", 1)
                status = ev.get("error_status", "")
                harness.progress(f"Claude: API retry {attempt} ({status})…")
                # Fast failover: if provider returns 5xx or auth error on retries,
                # abort quickly so PANDA dynamic injection or fallback chain can rescue the task.
                if attempt >= 2 and status in (401, 403, 500, 502, 503, 504):
                    raise ProviderFailure(f"api_retry error {status}: server_error")

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line)
    if timed_out:
        raise subprocess.TimeoutExpired(cmd, timeout)

    final = state["final"]
    if final is not None:
        return _result_out(final, returncode)

    # No result event: either a flag the CLI rejected (older version) or a
    # hard failure. The flag error degrades a tier; anything else surfaces
    # the CLI's stderr.
    low = err.lower()
    if not state["saw_event"] and returncode != 0 and (
            "unknown option" in low or "unrecognized" in low
            or "invalid value" in low):
        raise Unsupported()
    msg = err.strip() or f"claude exited {returncode} without a result event"
    return {"ok": False, "result": msg, "exit_code": returncode or 1}


def _result_out(final, returncode):
    """Reduce the result event to the wire shape.

    structured_output (when the request carried result_schema) rides through
    verbatim under "structured"; the answer text prefers its `answer` field
    over the rendered result string. subagent_stats passes through so the
    task record reports the delegation fan-out the run actually spawned.
    """
    usage = _usage(final.get("usage"))
    tokens = usage["input_tokens"] + usage["output_tokens"]
    res_text = final.get("result") or ""
    structured = final.get("structured_output")
    if isinstance(structured, dict):
        ans = structured.get("answer")
        if isinstance(ans, str) and ans.strip():
            res_text = ans
    is_err = bool(final.get("is_error"))
    # If the turn limit was reached, or if is_error is set but the model
    # completed substantive text output (e.g. analysis report), do not treat
    # as a fatal agent crash that discards the work.
    if is_err and (final.get("subtype") == "error_max_turns" or final.get("terminal_reason") == "max_turns"):
        if len(res_text.strip()) > 50:
            is_err = False
    extra = {}
    if structured is not None:
        extra["structured"] = structured
    stats = final.get("subagent_stats")
    if isinstance(stats, dict) and any(stats.values()):
        extra["subagent_stats"] = stats
    return {
        "ok": not is_err and (returncode == 0 or (not is_err and len(res_text.strip()) > 50)),
        "result": res_text,
        "exit_code": 0 if not is_err else returncode,
        "tokens": tokens or None,
        "cost": final.get("total_cost_usd"),
        "usage": usage,
        "session_id": final.get("session_id") or "",
        "extra": extra or None,
    }


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


def _parent_id(ev):
    """The tool_use id this event's sub-agent ran under, or "".

    --forward-subagent-text marks a sub-agent's messages with the parent
    Task call's id; both the snake and camel spellings have shipped, so
    read either rather than keying on a version.
    """
    return ev.get("parent_tool_use_id") or ev.get("parentToolUseID") or ""


def _emit_assistant_events(ev):
    """Emit typed events + progress notes for one assistant event's blocks.

    Every content block becomes a typed event for the transcript (text /
    thinking / tool_use), carrying the sub-agent parent id when the block
    ran inside a Task. Tool uses also emit the compact progress note the
    status line consumes — and a Task call gets the "subagent" kind so the
    orchestration layer records the delegation chain distinctly.
    """
    msg = ev.get("message")
    if not isinstance(msg, dict):
        return
    parent = _parent_id(ev)
    for block in msg.get("content") or []:
        if not isinstance(block, dict):
            continue
        bt = block.get("type")
        if bt == "text":
            text = block.get("text") or ""
            if text.strip():
                harness.emit_event("text", text=text, parent=parent)
        elif bt == "thinking":
            text = block.get("thinking") or ""
            if text.strip():
                harness.emit_event("thinking", thinking=text, parent=parent)
        elif bt == "tool_use":
            name = block.get("name", "tool")
            inp = block.get("input") or {}
            harness.emit_event("tool_use", id=block.get("id") or "",
                               name=name, input=inp, parent=parent)
            arg = ""
            if isinstance(inp, dict):
                for k in ("command", "file_path", "pattern", "path", "url", "query", "description"):
                    if inp.get(k):
                        arg = str(inp[k])
                        break
            if len(arg) > 80:
                arg = arg[:79] + "…"
            note = f"{name}: {arg}" if arg else name
            # Claude's built-in Task tool spawns a sub-agent: surface it as a
            # typed event so the Go harness records it distinctly from an
            # ordinary tool call (the operator sees the delegation chain).
            if name == "Task":
                harness.progress_kind("subagent", note)
            else:
                harness.progress(note)


def _emit_user_events(ev):
    """Emit typed events for one user event's tool_result blocks."""
    msg = ev.get("message")
    if not isinstance(msg, dict):
        return
    parent = _parent_id(ev)
    for block in msg.get("content") or []:
        if not isinstance(block, dict) or block.get("type") != "tool_result":
            continue
        content = block.get("content")
        if isinstance(content, list):
            # [{type:"text",text:…}] → joined text
            content = "\n".join(
                str(b.get("text") or "") for b in content
                if isinstance(b, dict) and b.get("type") == "text")
        if not isinstance(content, str):
            content = json.dumps(content, ensure_ascii=False) if content else ""
        harness.emit_event("tool_result",
                           tool_use_id=block.get("tool_use_id") or "",
                           is_error=bool(block.get("is_error")),
                           content=content, parent=parent)


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
    extra = {}
    if isinstance(parsed.get("structured_output"), dict):
        extra["structured"] = parsed["structured_output"]
    return {
        "ok": returncode == 0 and not parsed.get("is_error"),
        "result": parsed.get("result") or "",
        "exit_code": returncode,
        "tokens": tokens or None,
        "cost": parsed.get("total_cost_usd"),
        "usage": usage,
        "session_id": parsed.get("session_id") or "",
        "extra": extra or None,
    }


def _main_session(req, model, max_turns):
    """Session mode: one CLI process serves every turn over stream-json I/O.

    stdin contract after the request line: {"type":"user","text":…} per
    turn; each turn's text is forwarded as a user message and its result
    event becomes one envelope on our stdout. The first turn reuses
    req.prompt so a session request still reads as one task.
    """
    cmd = ["claude", "-p",
           "--input-format", "stream-json",
           "--output-format", "stream-json", "--verbose",
           "--max-turns", max_turns,
           "--permission-mode", "acceptEdits",
           "--permission-prompts", "none",
           "--forward-subagent-text"]
    if req.restricted:
        cmd += ["--restricted", "--tools", RESTRICTED_TOOLS]
    elif req.tools_policy != "extended":
        cmd += ["--allowedTools", ALLOWED_TOOLS]
    if req.system_prompt:
        cmd += ["--append-system-prompt", req.system_prompt]
    if req.result_schema:
        cmd += ["--json-schema", req.result_schema]
    if req.mcp_config:
        cmd += ["--mcp-config", req.mcp_config]
    if req.max_budget_usd > 0:
        cmd += ["--max-budget-usd", str(req.max_budget_usd)]
    if req.effort:
        cmd += ["--effort", req.effort]
    if fb := os.environ.get("OPENPANDA_FALLBACK_MODELS", ""):
        cmd += ["--fallback-model", fb]
    if _injected():
        cmd += ["--setting-sources", ""]
    # A task resuming on a fresh process (crash/restart with a persisted
    # session id) continues that conversation instead of starting cold.
    if req.resume:
        cmd += ["--resume", req.resume]
    if model and not any(model.lower().startswith(p) for p in ("deepseek", "openai", "gpt")):
        cmd += ["--model", model]

    try:
        proc, err_chunks = harness.spawn(cmd, cwd=req.cwd)
    except FileNotFoundError:
        harness.emit(False, "claude binary not found", 127)
        return
    deadline = harness.TurnDeadline(proc)

    def serve_turn(text):
        """One user message → events until this turn's result event."""
        if not harness.write_line(proc.stdin, {
                "type": "user",
                "message": {"role": "user",
                            "content": [{"type": "text", "text": text}]}}):
            return {"ok": False, "result": "claude session closed",
                    "exit_code": 1, "_dead": True}
        deadline.start(req.timeout)
        final = None
        try:
            for line in proc.stdout:
                ev = harness.parse_json_line(line)
                if ev is None:
                    continue
                et = ev.get("type")
                if et == "assistant":
                    _emit_assistant_events(ev)
                elif et == "user":
                    _emit_user_events(ev)
                elif et == "result":
                    final = ev
                    break
                elif et == "system":
                    sub = ev.get("subtype")
                    if sub == "api_retry":
                        attempt = ev.get("attempt", 1)
                        status = ev.get("error_status", "")
                        harness.progress(f"Claude: API retry {attempt} ({status})…")
                        if attempt >= 2 and status in (401, 403, 500, 502, 503, 504):
                            raise ProviderFailure(f"api_retry error {status}: server_error")
        finally:
            deadline.cancel()
        if final is None:
            dead = proc.poll() is not None
            diag = "".join(err_chunks).strip()
            return {"ok": False,
                    "result": diag or "claude session ended without a result",
                    "exit_code": proc.returncode or 1, "_dead": dead}
        return _result_out(final, 0)

    # Turn 0 is the request's own prompt; further turns arrive as lines.
    # A ProviderFailure ends the session with a clean error envelope — the
    # Go side falls back to a one-shot run, and an uncaught traceback here
    # would surface as "adapter session ended" instead of the real cause.
    try:
        out = serve_turn(req.prompt)
        _emit_turn_result(out)
        if not out.get("_dead"):
            for msg in harness.read_turn_lines():
                text = msg.get("text") or ""
                if not text.strip():
                    continue
                out = serve_turn(text)
                _emit_turn_result(out)
                if out.get("_dead"):
                    break
    except ProviderFailure as e:
        # The CLI may still be mid-retry — don't let it outlive the session.
        harness.kill_tree(proc)
        harness.emit(False, str(e), 1)
    try:
        proc.stdin.close()
    except OSError:
        pass
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        harness.kill_tree(proc)


def _emit_turn_result(out):
    """One session turn's envelope on stdout; the "_dead" marker is internal."""
    dead = out.pop("_dead", False)
    harness.emit(out["ok"], out["result"], out["exit_code"],
                 tokens=out.get("tokens"), cost=out.get("cost"),
                 usage=out.get("usage"), session_id=out.get("session_id"),
                 extra=out.get("extra"))
    out["_dead"] = dead


if __name__ == "__main__":
    main()
