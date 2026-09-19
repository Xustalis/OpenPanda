#!/usr/bin/env python3
"""Adapter: OpenCode CLI → PANDA Commander.

Protocol (shared with claude_code.py / codex.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus optional {usage, session_id}

Execution mode: `opencode run --format json` streams the session's raw JSON
events (one per line). Text parts accumulate into the answer, tool parts
become progress notes, and the message/session envelopes carry the token
usage, the cost and the session id a follow-up round resumes with
(--session <id>, so a supervision continuation keeps opencode's own
conversation instead of cold-starting). CLIs too old for --format json
degrade to the plain text mode. This adapter never prints secrets.

OpenCode is model-agnostic. The stream path runs `--auto` (opencode picks its
own model, which needs no API key or provider configuration); the plain-mode
degrade path passes the built-in free model instead. Set OPENCODE_MODEL (or
ANTHROPIC_MODEL) to a provider/model id (e.g. deepseek/deepseek-chat) to pin
a custom provider on both paths. tools_policy is noted but not enforced:
opencode gates tools through its own permission config, not a CLI whitelist,
so both policies run with the agent's configured tool face.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the opencode difference:
model resolution, the command line and the event reduction.
"""
import os
import subprocess

import _harness as harness

# opencode resolves --model as provider/model; a bare model name is not a valid
# provider and fails resolution. The built-in free model needs no key or config,
# so it is the default when no provider/model id is given.
DEFAULT_MODEL = "opencode/deepseek-v4-flash-free"


class ProviderFailure(Exception):
    """Provider or upstream API failure (e.g. rate limit, auth, server_error)."""


class Unsupported(Exception):
    """The CLI rejected --format json — degrade to plain text mode."""


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    # Model resolution:
    # Always pass --auto so non-interactive runs auto-approve tool permissions.
    # Keep print-logs=false to avoid polluting stdout with internal runtime diagnostics.
    cmd = ["opencode", "run", "--print-logs=true",
           "--format", "json", "--auto"]
    if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1":
        model = os.environ.get("OPENCODE_MODEL") or os.environ.get("OPENAI_MODEL", "")
        if model:
            if "/" in model:
                cmd += ["--model", model]
            else:
                cmd += ["--model", f"openai/{model}"]
    else:
        model = os.environ.get("OPENCODE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
        if model and "/" in model:
            cmd += ["--model", model]

    # A follow-up round resumes the agent's own session: its reasoning trail
    # survives instead of cold-starting on the bare follow-up instruction.
    if req.resume:
        cmd += ["--session", req.resume]
    cmd.append(prompt)

    try:
        out = _run_events(cmd, cwd, timeout)
    except ProviderFailure as e:
        harness.emit(False, f"provider failure: {e}", 1)
        return
    except Unsupported:
        # Older CLI without --format json: one-shot plain text run. The
        # plain command is rebuilt from scratch (never token-filtered out
        # of cmd — the prompt itself could match a filtered token). The
        # default model rides here too: the stream path may have omitted
        # --model in favor of --auto — which an old CLI just rejected — so
        # a bare "--model ''" would fail resolution outright.
        plain = ["opencode", "run", "--print-logs=false", "--auto", "--model", model or DEFAULT_MODEL]
        if req.resume:
            plain += ["--session", req.resume]
        plain.append(prompt)
        try:
            returncode, text, err = harness.run_plain(plain, cwd=cwd, timeout=timeout)
        except subprocess.TimeoutExpired:
            harness.emit(False, "opencode timed out", 124)
            return
        except FileNotFoundError:
            harness.emit(False, "opencode binary not found", 127)
            return
        harness.emit(returncode == 0, text.strip() or err.strip(), returncode)
        return
    except subprocess.TimeoutExpired:
        harness.emit(False, "opencode timed out", 124)
        return
    except FileNotFoundError:
        harness.emit(False, "opencode binary not found", 127)
        return

    harness.emit(out["ok"], out["result"], out["exit_code"],
                 tokens=out.get("tokens"), cost=out.get("cost"),
                 usage=out.get("usage"), session_id=out.get("session_id"))


def _run_events(cmd, cwd, timeout):
    """Event mode: reduce opencode's raw JSON event stream to the result."""
    state = {
        "saw_event": False,
        "session_id": "",
        "texts": {},
        "order": [],
        "tool_outputs": [],
        "counted": set(),
        "usage": {"input_tokens": 0, "output_tokens": 0},
        "cost": None,
        "error": "",
    }

    def on_line(line):
        ev = harness.parse_json_line(line)
        if ev is None:
            return
        state["saw_event"] = True
        et = ev.get("type") or ""
        if et == "error":
            err_obj = ev.get("error") if isinstance(ev.get("error"), dict) else {}
            err_msg = ""
            if isinstance(err_obj.get("data"), dict):
                err_msg = err_obj["data"].get("message", "")
            if not err_msg:
                err_msg = str(err_obj.get("message") or "")
            state["error"] = err_msg or "opencode error"
            raise ProviderFailure(state["error"])
        _fold(ev, state)

    def on_stderr(chunk):
        low = chunk.lower()
        if "rate limit exceeded" in low or "ai_retryerror" in low or "ai_apicallerror" in low or "stream error" in low:
            raise ProviderFailure(chunk.strip())

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line, on_stderr=on_stderr)
    if timed_out:
        raise subprocess.TimeoutExpired(cmd, timeout)

    if not state["saw_event"]:
        # Nothing parsed came out: either the CLI rejects --format json
        # (older version — degrade) or it failed outright (surface stderr).
        low = err.lower()
        if returncode != 0 and ("unknown option" in low or "unrecognized" in low
                                or "invalid value" in low or "unknown flag" in low):
            raise Unsupported()
        msg = err.strip() or f"opencode exited {returncode} without events"
        return {"ok": False, "result": msg, "exit_code": returncode or 1}

    text = "\n\n".join(state["texts"][pid] for pid in state["order"] if state["texts"].get(pid))
    if not text and state["tool_outputs"]:
        text = "\n\n".join(state["tool_outputs"])
    if not text:
        # Events streamed but no assistant text came out of them. Reporting this
        # as success (which is what returning `err` here used to do) is worse than
        # failing: the caller records a "✓ ok" task whose result is a wall of
        # stderr logs, the supervisor judge never sees an answer and re-runs the
        # agent until its budget is spent, and the task parks in review forever.
        # A failure instead hands the work to the next adapter in the chain.
        detail = err.strip().splitlines()
        tail = " / ".join(detail[-2:]) if detail else "no stderr"
        return {
            "ok": False,
            "result": ("opencode streamed events but no assistant text was extracted "
                       f"(unrecognized event schema); stderr tail: {tail}"),
            "exit_code": returncode or 1,
            "session_id": state["session_id"],
        }
    usage = state["usage"]
    tokens = usage["input_tokens"] + usage["output_tokens"] or None
    return {
        "ok": returncode == 0 and not state["error"],
        "result": text,
        "exit_code": returncode,
        "tokens": tokens,
        "cost": state["cost"],
        "usage": usage,
        "session_id": state["session_id"],
    }


def _fold(ev, state):
    """Fold one opencode event into the running state (defensively: the
    event schema drifts between versions, so every access is shape-checked
    and unknown shapes are ignored, never fatal).

    Two envelope shapes are in the wild and both must work. Older builds nest
    the payload under "properties"; current builds (1.18.x, verified against a
    real stream) put `part`, `sessionID` and the rest directly on the event.
    Reading only "properties" is what silently produced no text at all: the
    parts were there, one level up, and the result fell through to stderr."""
    props = ev.get("properties") if isinstance(ev.get("properties"), dict) else ev
    et = ev.get("type") or ""

    # Session id: carried as sessionID on the envelope (current builds) or on
    # the part; a session.created envelope may nest it under info/id instead.
    part = props.get("part") if isinstance(props.get("part"), dict) else None
    sid = props.get("sessionID") or props.get("session_id") or ""
    if not sid and part is not None:
        sid = part.get("sessionID") or part.get("session_id") or ""
    if not sid:
        info = props.get("info") if isinstance(props.get("info"), dict) else {}
        sid = info.get("id") or props.get("id") or ""
    if sid and not state["session_id"]:
        state["session_id"] = str(sid)

    if part is not None:
        pid = str(part.get("id") or len(state["order"]))
        ptype = part.get("type")
        if ptype == "text" and isinstance(part.get("text"), str) and part["text"]:
            if pid not in state["texts"]:
                state["order"].append(pid)
            state["texts"][pid] = part["text"]
        elif ptype == "tool":
            # The event that carries a tool part is "tool_use" on current builds
            # and "message.part.updated" (or similar …updated) on older ones.
            if et == "tool_use" or et.endswith("updated"):
                tool = str(part.get("tool") or "tool")
                st = part.get("state") if isinstance(part.get("state"), dict) else {}
                arg = str(st.get("title") or st.get("input") or "")[:80]
                # A completed tool call is the useful timeline entry; started
                # calls churn too much to be worth a note each.
                if st.get("status") in ("completed", "error") or "completed" in str(st):
                    harness.progress(f"{tool}: {arg}" if arg else tool)
                    out = st.get("output")
                    if isinstance(out, str) and out.strip():
                        state["tool_outputs"].append(out.strip())
        # Usage rides the step_finish part on current builds: each step reports
        # the tokens that call spent, so they accumulate (keyed by part id, so a
        # re-emitted part never double counts).
        self_tokens = part.get("tokens") if isinstance(part.get("tokens"), dict) else None
        if self_tokens and pid not in state["counted"]:
            state["counted"].add(pid)
            try:
                state["usage"]["input_tokens"] += int(self_tokens.get("input", 0) or 0)
                state["usage"]["output_tokens"] += int(self_tokens.get("output", 0) or 0)
            except (TypeError, ValueError):
                pass
        part_cost = part.get("cost")
        if isinstance(part_cost, (int, float)) and part_cost:
            state["cost"] = max(state["cost"] or 0, float(part_cost))
        return

    # Older builds hang usage/cost off the message envelope instead (cumulative
    # per message; keep the max so re-emits never double count).
    info = props.get("info") if isinstance(props.get("info"), dict) else props
    toks = info.get("tokens") if isinstance(info.get("tokens"), dict) else None
    if toks:
        try:
            state["usage"]["input_tokens"] = max(
                state["usage"]["input_tokens"], int(toks.get("input", 0)))
            state["usage"]["output_tokens"] = max(
                state["usage"]["output_tokens"], int(toks.get("output", 0)))
        except (TypeError, ValueError):
            pass
    cost = info.get("cost")
    if isinstance(cost, (int, float)) and cost:
        state["cost"] = max(state["cost"] or 0, float(cost))


if __name__ == "__main__":
    main()
