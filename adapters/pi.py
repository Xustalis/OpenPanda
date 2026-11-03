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
import re
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


class UnknownFlags(Exception):
    """The CLI refused specific flags as unknown — rebuild without them.

    Carries the names the CLI itself listed, so the caller drops exactly what
    this build does not implement and retries, rather than guessing a version
    surface or paying for a `pi --help` probe (7–10 s on a cold node).
    """

    def __init__(self, flags):
        super().__init__(", ".join(sorted(flags)) or "unknown flags")
        self.flags = set(flags)


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
    # models.json never stores the secret: pi's `apiKey` field resolves a bare
    # name as an environment variable (docs/models.md "Value Resolution"), so
    # writing the VARIABLE NAME makes pi read PI_API_KEY at request time.
    #
    # It must be the bare name — "${PI_API_KEY}" is not a template pi
    # understands, so it is sent as the literal string and the provider
    # answers "401 token invalid", which reads like a bad key rather than a
    # bad manifest.
    if os.environ.get("PI_API_KEY"):
        provider["apiKey"] = "PI_API_KEY"
    agent_dir = tempfile.mkdtemp(prefix="panda-pi-agent-")
    try:
        _seed_settings(agent_dir)
        with open(os.path.join(agent_dir, "models.json"), "w", encoding="utf-8") as f:
            json.dump({"providers": {"panda": provider}}, f)
    except OSError:
        shutil.rmtree(agent_dir, ignore_errors=True)
        return "", ""
    # The finally in main() covers a normal exit; a signal path calls os._exit
    # and skips it, so the directory also rides the harness cleanup list.
    harness.at_cleanup(shutil.rmtree, agent_dir, True)
    os.environ["PI_CODING_AGENT_DIR"] = agent_dir
    return "panda/" + model, agent_dir


# Settings keys that pick a model. They are dropped from the seeded copy: the
# run is pinned with --model panda/<id>, and a leftover defaultProvider naming
# a provider whose auth.json was NOT copied (that file is a secret store and
# stays out of the temp dir) is a startup failure waiting to happen.
MODEL_SETTING_KEYS = ("defaultProvider", "defaultModel")


def _seed_settings(agent_dir):
    """Copy the user's settings.json into the temporary agent dir.

    PI_CODING_AGENT_DIR is the whole agent home — models.json, settings.json,
    auth.json, bin/. Pointing it at a fresh temp dir therefore does not just
    swap the provider, it drops every setting the user ever made. The one that
    bites on Windows is `shellPath`: without it pi's bash tool probes only
    `C:\\Program Files (x86)\\Git\\bin\\bash.exe`, fails with "No bash shell
    found", and the agent — which cannot run a single command — spends its
    whole turn reading pi's own source to work out why. A smoke test that
    should take one command ran for ten minutes and ended in a directory
    listing instead of a result.

    Copying the file keeps shellPath, theme and the rest of the user's setup.
    Anything unreadable is skipped: a missing settings.json is a working
    default, and refusing to run because a preference file is corrupt would
    trade one broken run for another.
    """
    src = os.path.join(_default_agent_dir(), "settings.json")
    if not os.path.isfile(src):
        return
    try:
        with open(src, "r", encoding="utf-8") as f:
            settings = json.load(f)
    except (OSError, ValueError):
        return
    if not isinstance(settings, dict):
        return
    for key in MODEL_SETTING_KEYS:
        settings.pop(key, None)
    with open(os.path.join(agent_dir, "settings.json"), "w", encoding="utf-8") as f:
        json.dump(settings, f, indent=2)


def _default_agent_dir():
    """The agent dir pi would use with no injection: PI_CODING_AGENT_DIR when
    the caller set one, else ~/.pi/agent (pi's own default, config.js
    getAgentDir)."""
    env = os.environ.get("PI_CODING_AGENT_DIR", "").strip()
    if env:
        return os.path.expanduser(env)
    return os.path.join(os.path.expanduser("~"), ".pi", "agent")


def _common_flags(req, dropped=frozenset()):
    """Tool-face and trust flags shared by the JSON and print-mode paths.

    `dropped` carries the flags this build already rejected as unknown (see
    _strip_flags): pi's CLI surface moves between minor versions — 0.73
    dropped --approve, --no-approve, --exclude-tools and the bare `--`
    separator outright, so a fixed command line dies on "Unknown options"
    before any model call. Rebuilding without the named flags keeps one
    adapter working across versions without a help probe (pi --help costs
    7–10 s on a cold node, which is a whole supervision round).
    """
    if req.restricted:
        # Read-only face for an unconsented remote task: no writes, no shell,
        # and no .pi/ project resources (extensions/MCP included) — the work
        # dir is where the attack surface lives.
        flags = ["--tools", READONLY_TOOLS, "--no-mcp", "--no-extensions",
                 "--no-approve"]
    else:
        flags = ["--approve"]  # project resources load inside the task's work dir
        if req.tools_policy != "extended":
            flags += ["--tools", SAFE_TOOLS, "--exclude-tools", "mcp__*"]
    return _strip_flags(flags, dropped)


# Flags in _common_flags that consume the following token as their value.
_VALUE_FLAGS = {"--tools", "--exclude-tools"}


def _strip_flags(flags, dropped):
    """Remove the flags in `dropped` (and their values) from `flags`.

    The `--` separator is special: it owns no value, so the prompt that
    follows it must survive. Getting that wrong would silently delete the
    task's own text.
    """
    if not dropped:
        return list(flags)
    out = []
    i = 0
    while i < len(flags):
        tok = flags[i]
        if tok in dropped:
            if tok in _VALUE_FLAGS and i + 1 < len(flags):
                i += 2
            else:
                i += 1
            continue
        out.append(tok)
        i += 1
    return out


_UNKNOWN_OPTION_RE = re.compile(r"unknown options?:\s*(.+)", re.IGNORECASE)


def _rejected_flags(stderr):
    """The flags a CLI named in its "Unknown options: a, b" refusal."""
    m = _UNKNOWN_OPTION_RE.search(stderr or "")
    if not m:
        return set()
    names = set()
    for part in m.group(1).split(","):
        tok = part.strip().strip("'\"")
        if tok.startswith("-"):
            names.add(tok)
    return names


def _build_cmd(req, injected_model, dropped=frozenset()):
    cmd = ["pi", "--mode", "json"]
    cmd += _common_flags(req, dropped)
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
    # misparsed as a flag. Builds that dropped the separator say so in their
    # "Unknown options" refusal, and it lands in `dropped`.
    if "--" not in dropped:
        cmd += ["--"]
    cmd.append(req.prompt)
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
    # dropped: flags this build has already refused as unknown. pi's surface
    # drifts (0.73 lost --approve/--exclude-tools/`--`), and the CLI names the
    # offending flags in its refusal — so the adapter learns the surface from
    # one cheap failure instead of paying 7–10 s for `pi --help` every round.
    dropped = set()
    out = None
    plain_fallback = False
    try:
        try:
            while True:
                cmd = _build_cmd(req, injected_model, dropped)
                try:
                    out = _run_events(cmd, cwd, timeout)
                    break
                except UnknownFlags as e:
                    new = {f for f in e.flags if f not in dropped}
                    if not new:
                        # Nothing left to drop: the rejection was not about our
                        # optional flags (e.g. --mode json itself) — degrade to
                        # print mode exactly as before.
                        plain_fallback = True
                        break
                    dropped |= new
                except Unsupported:
                    plain_fallback = True
                    break
        except ProviderFailure as e:
            st = getattr(e, "state", None) or {}
            usage = st.get("usage") if isinstance(st.get("usage"), dict) else {}
            harness.emit(False, f"provider failure: {e}", 1,
                         tokens=(usage.get("input_tokens", 0) + usage.get("output_tokens", 0)) or None,
                         cost=st.get("cost") or None, usage=usage or None,
                         session_id=st.get("session_id") or None)
            return
        except subprocess.TimeoutExpired:
            harness.emit(False, "pi timed out", 124)
            return
        except FileNotFoundError:
            harness.emit(False, "pi binary not found", 127)
            return
        if plain_fallback:
            # Older pi without --mode json: one-shot print-mode run. The command
            # is rebuilt, never token-filtered (the prompt could itself match a
            # filtered token), and it inherits the flags this build already
            # refused so the fallback does not die on the same refusal.
            plain = ["pi", "--print"] + _common_flags(req, dropped)
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
            if "--" not in dropped:
                plain += ["--"]
            plain.append(prompt)
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
            exc = ProviderFailure(chunk.strip())
            exc.state = state
            raise exc

    returncode, err, timed_out = harness.run_stream(
        cmd, cwd=cwd, timeout=timeout, on_line=on_line, on_stderr=on_stderr)
    if timed_out:
        # Checkpoint, not corpse: the minted session lets the retry resume
        # in place, and the accumulated usage still bills the task budget.
        usage = state["usage"]
        tokens = (usage["input_tokens"] + usage["output_tokens"]) or None
        return {
            "ok": False,
            "result": "pi timed out",
            "exit_code": 124,
            "tokens": tokens,
            "cost": state["cost"] or None,
            "usage": usage,
            "session_id": state["session_id"],
        }

    if not state["saw_event"]:
        low = err.lower()
        if returncode != 0 and ("unknown option" in low or "unrecognized" in low
                                or "invalid value" in low or "unknown flag" in low
                                or "unknown argument" in low):
            # "Unknown options: --a, --b" names what this build does not
            # implement; anything vaguer is treated as "this CLI has no
            # --mode json" and degrades to print mode.
            named = _rejected_flags(err)
            if named:
                raise UnknownFlags(named)
            raise Unsupported()
        msg = err.strip() or f"pi exited {returncode} without events"
        return {"ok": False, "result": msg, "exit_code": returncode or 1}

    text = state["last_text"]
    if not text and state["tool_outputs"]:
        # The agent ran tools and stopped without ever writing an answer.
        # Handing the tool log back as the result is how a failed run came out
        # looking finished: a smoke test that could not start a shell at all
        # reported "bash.exe\ngit.exe\nsh.exe\n\n.claude/\n.inject-test/…" —
        # a directory listing — as its conclusion, the supervisor read that as
        # work, and the thread printed it as the employee's closing paragraph.
        # A run with no answer is a failure that needs a reason, not a green
        # ok carrying a log; the tail is kept as evidence, one line, bounded.
        tail = " / ".join(line.strip() for line in state["tool_outputs"][-3:] if line.strip())
        if len(tail) > 300:
            tail = tail[:300] + "…"
        return {
            "ok": False,
            "result": ("pi ran tools but never produced a final answer"
                       + (f"; last tool output: {tail}" if tail else "")),
            "exit_code": returncode or 1,
            "session_id": state["session_id"],
        }
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
        if isinstance(sid, str) and sid and sid != state["session_id"]:
            state["session_id"] = sid
            # Announce the handle early: a run killed before its result
            # envelope still leaves a resumable checkpoint the Go side
            # latched.
            harness.emit_event("session", id=sid)
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
