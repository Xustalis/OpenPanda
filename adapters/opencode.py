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


class Unsupported(Exception):
    """The CLI rejected --format json — degrade to plain text mode."""


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    # opencode resolves --model as provider/model; a bare model name fails. A
    # provider/model id from env wins; otherwise default to the built-in free
    # model, which needs no API key. stdin is /dev/null so opencode never waits
    # on an inherited pipe for the prompt.
    model = os.environ.get("OPENCODE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    cmd = ["opencode", "run", "--print-logs=false",
           "--format", "json", "--auto"]
    if model and "/" in model:
        cmd += ["--model", model]
    # A follow-up round resumes the agent's own session: its reasoning trail
    # survives instead of cold-starting on the bare follow-up instruction.
    if req.resume:
        cmd += ["--session", req.resume]
    cmd.append(prompt)

    try:
        out = _run_events(cmd, cwd, timeout)
    except Unsupported:
        # Older CLI without --format json: one-shot plain text run. The
        # plain command is rebuilt from scratch (never token-filtered out
        # of cmd — the prompt itself could match a filtered token). The
        # default model rides here too: the stream path may have omitted
        # --model in favor of --auto — which an old CLI just rejected — so
        # a bare "--model ''" would fail resolution outright.
        plain = ["opencode", "run", "--print-logs=false", "--model", model or DEFAULT_MODEL]
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
        # part id -> text; message.part.updated re-sends the part's full text
        # as it grows, so keying by id keeps each part exactly once.
        "texts": {},
        "order": [],
        "usage": {"input_tokens": 0, "output_tokens": 0},
        "cost": None,
    }

    def on_line(line):
        ev = harness.parse_json_line(line)
        if ev is None:
            return
        state["saw_event"] = True
        _fold(ev, state)

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line)
    if timed_out:
        raise subprocess.TimeoutExpired(cmd, timeout)

    if not state["saw_event"]:
        # Nothing parse came out: either the CLI rejects --format json
        # (older version — degrade) or it failed outright (surface stderr).
        low = err.lower()
        if returncode != 0 and ("unknown option" in low or "unrecognized" in low
                                or "invalid value" in low or "unknown flag" in low):
            raise Unsupported()
        msg = err.strip() or f"opencode exited {returncode} without events"
        return {"ok": False, "result": msg, "exit_code": returncode or 1}

    text = "\n\n".join(state["texts"][pid] for pid in state["order"] if state["texts"].get(pid))
    usage = state["usage"]
    tokens = usage["input_tokens"] + usage["output_tokens"] or None
    return {
        "ok": returncode == 0,
        "result": text or err.strip(),
        "exit_code": returncode,
        "tokens": tokens,
        "cost": state["cost"],
        "usage": usage,
        "session_id": state["session_id"],
    }


def _fold(ev, state):
    """Fold one opencode event into the running state (defensively: the
    event schema drifts between versions, so every access is shape-checked
    and unknown shapes are ignored, never fatal)."""
    et = ev.get("type") or ""
    props = ev.get("properties") if isinstance(ev.get("properties"), dict) else {}

    # Session id: most events carry it as properties.sessionID; a
    # session.created envelope may nest it under info/id instead.
    sid = props.get("sessionID") or props.get("session_id") or ""
    if not sid:
        info = props.get("info") if isinstance(props.get("info"), dict) else {}
        sid = info.get("id") or props.get("id") or ""
    if sid and not state["session_id"]:
        state["session_id"] = str(sid)

    part = props.get("part") if isinstance(props.get("part"), dict) else None
    if part is not None:
        pid = str(part.get("id") or len(state["order"]))
        ptype = part.get("type")
        if ptype == "text" and isinstance(part.get("text"), str) and part["text"]:
            if pid not in state["texts"]:
                state["order"].append(pid)
            state["texts"][pid] = part["text"]
        elif ptype == "tool" and et.endswith("updated"):
            tool = str(part.get("tool") or "tool")
            st = part.get("state") if isinstance(part.get("state"), dict) else {}
            arg = str(st.get("title") or st.get("input") or "")[:80]
            # A completed tool call is the useful timeline entry; started
            # calls churn too much to be worth a note each.
            if st.get("status") in ("completed", "error") or "completed" in str(st):
                harness.progress(f"{tool}: {arg}" if arg else tool)
        return

    # Usage/cost ride the message envelope on newer CLIs (cumulative per
    # message; keep the max so re-emits never double count).
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
