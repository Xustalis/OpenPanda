#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: Pi coding agent (`pi`, @mariozechner/pi-coding-agent) → PANDA Commander.

Protocol (shared with claude_code.py / codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy, restricted, system_prompt,
          effort, task_id}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id}
  stderr: NDJSON {"type":"progress","note":str} status lines and
          {"type":"event",...} transcript frames; see _harness.py

Execution mode: `pi --mode json` writes the session header plus one JSONL
event per record on stdout (JsonAgentSessionEvent), then exits. The adapter
reduces the stream: assistant text on message_end is the answer, tool
executions become progress notes + tool_use/tool_result events, usage is
summed across assistant messages, and the session header's id is what a
follow-up round resumes with (--session <id>). CLIs too old for --mode json
degrade to print mode (`pi -p`).

Tool face: pi's --tools replaces the declared tool selection, so the minimal
policy runs a fixed built-in allowlist (MCP tools stay excluded via
-ex/--exclude-tools 'mcp__*'). A restricted run (unconsented remote task)
narrows further to the read-only tools AND switches extensions, MCP and
project trust off — a hostile work directory cannot reach the run through
.pi/ resources or extension hooks. Normal runs pass --approve so project
resources (AGENTS.md context, .pi/skills, .pi/mcp.json passthrough) load.

Model selection: pi is multi-provider. With its own credentials the run uses
pi's configured default model; PI_MODEL / PI_PROVIDER pin one explicitly.
When PANDA injects its model (OPENPANDA_INJECTED_MODEL=1, PI_MODEL +
PI_BASE_URL + PI_API_KEY + OPENPANDA_MODEL_API_TYPE set by the commander),
the adapter writes a temporary agent dir whose models.json declares a
"panda" custom provider — the api key rides a ${PI_API_KEY} env
interpolation, never the file — and selects it via --model panda/<id>.
Sessions keep the user's real session store (--session-dir), so a follow-up
round's --session id resolves across runs regardless of the temp dir.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the pi difference:
flags, the injection shim, and the event reduction. It never prints secrets.
"""
import json
import os
import shutil
import subprocess
import tempfile

import _harness as harness

# pi's built-in tool names (read/bash/edit/write are pi's own defaults;
# powershell is the Windows shell tool; grep/find/ls are the read helpers).
# --tools replaces the selection, so these names must be complete.
SAFE_TOOLS = "read,bash,powershell,edit,write,grep,find,ls"
READONLY_TOOLS = "read,grep,find,ls"

# pi --thinking levels; anything else (e.g. claude-style "max" is valid,
# "auto" is not) is dropped rather than rejected at the CLI.
THINKING_LEVELS = {"off", "minimal", "low", "medium", "high", "xhigh", "max"}

# models.json "api" dialect per provider protocol — the wire format PANDA's
# configured endpoint speaks, mapped to pi's adapter name for it.
API_DIALECTS = {
    "openai": "openai-completions",
    "anthropic": "anthropic-messages",
}


class ProviderFailure(Exception):
    """Provider or upstream API failure — failover to the next agent helps."""


class Unsupported(Exception):
    """The CLI rejected --mode json — degrade to print mode."""


def _injected_model():
    """Build the temporary agent dir for an injected run.

    Returns (model_arg, cleanup_dir): model_arg is "panda/<id>" or "" when
    the env does not carry a complete injection (missing model name).
    """
    model = os.environ.get("PI_MODEL", "")
    base_url = os.environ.get("PI_BASE_URL", "")
    if not model:
        return "", ""
    api = API_DIALECTS.get(os.environ.get("OPENPANDA_MODEL_API_TYPE", "").strip() or "openai",
                           "openai-completions")
    provider = {"api": api, "models": [{"id": model}]}
    if base_url:
        provider["baseUrl"] = base_url
    # The key rides an env interpolation — models.json never stores it.
    if os.environ.get("PI_API_KEY"):
        provider["apiKey"] = "${PI_API_KEY}"
    agent_dir = tempfile.mkdtemp(prefix="panda-pi-agent-")
    try:
        with open(os.path.join(agent_dir, "models.json"), "w", encoding="utf-8") as f:
            json.dump({"providers": {"panda": provider}}, f)
    except OSError:
        shutil.rmtree(agent_dir, ignore_errors=True)
        return "", ""
    os.environ["PI_CODING_AGENT_DIR"] = agent_dir
    return "panda/" + model, agent_dir


def _common_flags(req):
    """Tool-face and trust flags shared by the JSON and print-mode paths."""
    if req.restricted:
        # Read-only face for an unconsented remote task: no writes, no shell,
        # and no .pi/ project resources (extensions/MCP included) — the work
        # dir is where the attack surface lives.
        return ["--tools", READONLY_TOOLS, "--no-mcp", "--no-extensions",
                "--no-approve"]
    flags = ["--approve"]  # project resources load inside the task's work dir
    if req.tools_policy != "extended":
        flags += ["--tools", SAFE_TOOLS, "--exclude-tools", "mcp__*"]
    return flags


def _build_cmd(req, injected_model):
    cmd = ["pi", "--mode", "json"]
    cmd += _common_flags(req)
    if injected_model:
        cmd += ["--model", injected_model]
    else:
        if os.environ.get("PI_MODEL"):
            cmd += ["--model", os.environ["PI_MODEL"]]
        if os.environ.get("PI_PROVIDER"):
            cmd += ["--provider", os.environ["PI_PROVIDER"]]
    if injected_model:
        # Keep session state in the user's real store: the injected agent dir
        # is a per-run tempdir, so its sessions would not survive a resume.
        cmd += ["--session-dir",
                os.path.join(os.path.expanduser("~"), ".pi", "agent", "sessions")]
    if req.resume:
        cmd += ["--session", req.resume]
    if req.system_prompt:
        cmd += ["--append-system-prompt", req.system_prompt]
    effort = req.effort.strip().lower()
    if effort in THINKING_LEVELS:
        cmd += ["--thinking", effort]
    # "--" stops option parsing so a prompt beginning with "-" cannot be
    # misparsed as a flag.
    cmd += ["--", req.prompt]
    return cmd


def _text_of(message):
    """Join the text blocks of an assistant message (pi-ai content shape)."""
    if not isinstance(message, dict):
        return ""
    parts = []
    for block in message.get("content") or []:
        if isinstance(block, dict) and block.get("type") == "text" \
                and isinstance(block.get("text"), str):
            parts.append(block["text"])
    return "\n".join(p for p in parts if p)


def _thinking_of(message):
    parts = []
    for block in (message.get("content") if isinstance(message, dict) else []) or []:
        if isinstance(block, dict) and block.get("type") in ("thinking", "reasoning"):
            t = block.get("thinking") or block.get("text") or ""
            if t:
                parts.append(t)
    return "\n".join(parts)


def _usage_of(u):
    """Normalize a pi Usage object to the wire breakdown (0s when absent)."""
    if not isinstance(u, dict):
        return {}
    def _n(*names):
        for n in names:
            v = u.get(n)
            if isinstance(v, (int, float)) and v:
                return int(v)
        return 0
    return {
        "input_tokens": _n("input", "input_tokens"),
        "output_tokens": _n("output", "output_tokens"),
        "cache_read_tokens": _n("cacheRead", "cache_read_tokens"),
        "cache_write_tokens": _n("cacheWrite", "cache_write_tokens"),
    }


def _cost_of(u):
    if isinstance(u, dict) and isinstance(u.get("cost"), dict):
        c = u["cost"].get("total")
        if isinstance(c, (int, float)) and c:
            return float(c)
    return 0.0


def _first_arg(args):
    """Short human-readable arg for a progress note (path/command first)."""
    if not isinstance(args, dict) or not args:
        return ""
    for key in ("command", "cmd", "path", "file", "pattern", "query"):
        v = args.get(key)
        if isinstance(v, str) and v:
            return v.splitlines()[0][:80]
    try:
        v = next(iter(args.values()))
        if isinstance(v, str):
            return v[:80]
    except StopIteration:
        pass
    return ""


def _result_text(result):
    """Best-effort printable rendering of a tool_execution_end result."""
    if isinstance(result, str):
        return result
    if isinstance(result, dict):
        # {content:[{type:"text",text}...]} is the tool-result message shape.
        parts = [b.get("text", "") for b in result.get("content") or []
                 if isinstance(b, dict) and b.get("type") == "text"]
        if parts:
            return "\n".join(p for p in parts if p)
    try:
        return json.dumps(result, ensure_ascii=False)
    except (TypeError, ValueError):
        return str(result)


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    injected_model, agent_dir = "", ""
    if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1":
        injected_model, agent_dir = _injected_model()
    try:
        cmd = _build_cmd(req, injected_model)
        try:
            out = _run_events(cmd, cwd, timeout)
        except ProviderFailure as e:
            harness.emit(False, f"provider failure: {e}", 1)
            return
        except Unsupported:
            # Older pi without --mode json: one-shot print-mode run. The
            # command is rebuilt, never token-filtered (the prompt could
            # itself match a filtered token).
            plain = ["pi", "--print"] + _common_flags(req)
            if injected_model:
                plain += ["--model", injected_model]
            elif os.environ.get("PI_MODEL"):
                plain += ["--model", os.environ["PI_MODEL"]]
            if req.resume:
                if injected_model:
                    # Same session-store fix as the JSON path: the injected
                    # agent dir is a per-run tempdir — without --session-dir
                    # --session resolves against an empty store and silently
                    # loses the prior round. Gated on resume so a CLI too old
                    # for the flag does not lose the whole fallback run.
                    plain += ["--session-dir",
                              os.path.join(os.path.expanduser("~"), ".pi", "agent", "sessions")]
                plain += ["--session", req.resume]
            plain += ["--", prompt]
            try:
                rc, text, err = harness.run_plain(plain, cwd=cwd, timeout=timeout)
            except subprocess.TimeoutExpired:
                harness.emit(False, "pi timed out", 124)
                return
            except FileNotFoundError:
                harness.emit(False, "pi binary not found", 127)
                return
            harness.emit(rc == 0, text.strip() or err.strip(), rc)
            return
        except subprocess.TimeoutExpired:
            harness.emit(False, "pi timed out", 124)
            return
        except FileNotFoundError:
            harness.emit(False, "pi binary not found", 127)
            return
        harness.emit(out["ok"], out["result"], out["exit_code"],
                     tokens=out.get("tokens"), cost=out.get("cost"),
                     usage=out.get("usage"), session_id=out.get("session_id"))
    finally:
        if agent_dir:
            shutil.rmtree(agent_dir, ignore_errors=True)


def _run_events(cmd, cwd, timeout):
    """JSON mode: reduce pi's event stream to the unified result."""
    state = {
        "saw_event": False,
        "session_id": "",
        "last_text": "",
        "tool_outputs": [],
        "usage": {"input_tokens": 0, "output_tokens": 0,
                  "cache_read_tokens": 0, "cache_write_tokens": 0},
        "cost": 0.0,
        "cur_usage": None,
        "error": "",
    }

    def on_line(line):
        ev = harness.parse_json_line(line)
        if ev is None:
            return
        state["saw_event"] = True
        _fold(ev, state)

    def on_stderr(chunk):
        low = chunk.lower()
        if "rate limit" in low or "econnrefused" in low or "invalid api key" in low:
            raise ProviderFailure(chunk.strip())

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line, on_stderr=on_stderr)
    if timed_out:
        raise subprocess.TimeoutExpired(cmd, timeout)

    if not state["saw_event"]:
        low = err.lower()
        if returncode != 0 and ("unknown option" in low or "unrecognized" in low
                                or "invalid value" in low or "unknown flag" in low
                                or "unknown argument" in low):
            raise Unsupported()
        msg = err.strip() or f"pi exited {returncode} without events"
        return {"ok": False, "result": msg, "exit_code": returncode or 1}

    text = state["last_text"]
    if not text and state["tool_outputs"]:
        text = "\n\n".join(state["tool_outputs"])
    if not text:
        # Events streamed but no assistant text: report failure, not a green
        # "ok" — a silent success here makes the supervisor re-run the agent
        # until budget is spent (same failure class opencode hit).
        detail = err.strip().splitlines()
        tail = " / ".join(detail[-2:]) if detail else "no stderr"
        return {
            "ok": False,
            "result": ("pi streamed events but no assistant text was extracted "
                       f"(unrecognized event schema); stderr tail: {tail}"),
            "exit_code": returncode or 1,
            "session_id": state["session_id"],
        }
    usage = state["usage"]
    tokens = (usage["input_tokens"] + usage["output_tokens"]) or None
    return {
        "ok": returncode == 0 and not state["error"],
        "result": text,
        "exit_code": returncode,
        "tokens": tokens,
        "cost": state["cost"] or None,
        "usage": usage,
        "session_id": state["session_id"],
    }


def _fold(ev, state):
    """Fold one JsonAgentSessionEvent into the run state.

    Defensive by construction: the wire schema is versioned (session header
    carries "version"), so every access is shape-checked and unknown event
    types are ignored, never fatal.
    """
    et = ev.get("type") or ""

    if et == "session":
        sid = ev.get("id")
        if isinstance(sid, str) and sid:
            state["session_id"] = sid
        return

    if et == "message_update":
        u = _usage_of(ev.get("usage"))
        if any(u.values()):
            state["cur_usage"] = u
        cu = ev.get("cost")
        if isinstance(cu, (int, float)) and cu:
            state["cost"] += float(cu)
        a = ev.get("assistantMessageEvent")
        if isinstance(a, dict):
            at = a.get("type")
            if at == "error":
                err = a.get("error") or a.get("message") or "assistant error"
                raise ProviderFailure(str(err))
        return

    if et == "message_end":
        msg = ev.get("message")
        if not isinstance(msg, dict):
            return
        u = _usage_of(msg.get("usage"))
        if not any(u.values()):
            u = state.pop("cur_usage", None) or {}
        else:
            state["cur_usage"] = None
        for k, v in u.items():
            state["usage"][k] += v
        c = _cost_of(msg.get("usage"))
        if c:
            state["cost"] += c
        if msg.get("role") != "assistant":
            return
        think = _thinking_of(msg)
        if think:
            harness.emit_event("thinking", thinking=think)
        text = _text_of(msg)
        if text:
            state["last_text"] = text
            mid = msg.get("id") or msg.get("messageId") or "msg"
            harness.emit_event("text", text=text, id=str(mid))
        if isinstance(msg.get("errorMessage"), str) and msg["errorMessage"]:
            state["error"] = msg["errorMessage"]
        return

    if et == "tool_execution_start":
        tid = str(ev.get("toolCallId") or "")
        name = str(ev.get("toolName") or "tool")
        args = ev.get("args")
        harness.progress(f"{name}: {_first_arg(args)}" if _first_arg(args) else name)
        harness.emit_event("tool_use", id=tid, name=name,
                           input=args if isinstance(args, dict) else {"input": args})
        return

    if et == "tool_execution_end":
        tid = str(ev.get("toolCallId") or "")
        is_err = bool(ev.get("isError"))
        content = _result_text(ev.get("result"))
        harness.emit_event("tool_result", tool_use_id=tid,
                           is_error=is_err, content=content)
        if content.strip() and not is_err:
            state["tool_outputs"].append(content.strip())
        return

    if et == "auto_retry_end":
        if ev.get("success") is False:
            raise ProviderFailure(
                str(ev.get("finalError") or "retries exhausted"))
        return

    # turn_start/end, message_start, agent_start/end, agent_settled,
    # compaction_*, queue_update, tool_execution_update — no transcript
    # output needed; the events above carry everything the run loop reads.


if __name__ == "__main__":
    main()
