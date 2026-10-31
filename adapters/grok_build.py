#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: Grok Build CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus the optional {session_id}

Runs `grok -s <uuid> --single <prompt> --output-format streaming-json
--always-approve` headless and reads the event stream: the answer arrives as
`text` deltas and the turn closes on its `end` line — this build does not exit
afterwards (see _harness.run_stream's stop_when), so the adapter ends the turn
itself instead of waiting out the watchdog. This adapter never prints secrets.

Session continuity: none, deliberately. Each round starts a new conversation
with its own UUID; grok 1.0.67 cannot resume one headless (see `_new_session`),
so a handle the next round could not use is not reported as if it could.
--always-approve rides both policies: a headless run cannot sit on an
interactive permission prompt, and grok exposes no narrower CLI tool
whitelist, so tools_policy does not change the command line here.

Text-only on an injected model: a tool-using turn never finishes there (the
tool runs, then the turn's next inference never returns — see _injected_model
and the registry's grok_build note), so such a run is cut at the first
tool_call and reported with that reason instead of hanging to the watchdog.
The registry also declares grok_build's capabilities as `text`, which keeps
command-shaped work from being routed here in the first place.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the grok difference:
the command line and the session naming.
"""
import json
import os
import subprocess
import uuid

import _harness as harness


def _new_session():
    """A fresh session id for this run.

    grok types `--session-id` as a UUID ("--session-id must be a valid UUID"),
    and a name-shaped handle from an earlier build is a hard error before any
    work starts.

    This run always starts a new conversation: grok 1.0.67 cannot resume one
    headless. `-r <id> --single` answers the prompt with nothing at all — two
    `available_commands` lines and then silence — and `-s <existing id>` is
    refused outright ("Session ID … is already in use"). Reporting a handle
    the next round cannot use is worse than reporting none, so the round
    cold-starts and the supervision prompt carries the accumulated context,
    which it already does.
    """
    return str(uuid.uuid4())


def _injected_model():
    """Whether this run rides PANDA's injected model instead of grok's own.

    PANDA exports OPENPANDA_INJECTED_MODEL=1 for every injected run (see
    modelEnvForAdapter). That is precisely the configuration in which the
    tool-turn hang below was measured: a foreign OpenAI-compatible endpoint
    with no xAI session behind it. A grok that holds its own credentials never
    sees the variable, so its tool turns keep the ordinary path.
    """
    return os.environ.get("OPENPANDA_INJECTED_MODEL") == "1"


def _is_turn_end(line):
    """Whether one stream line closes the turn.

    grok closes a turn with `end` — {"type":"end","stopReason":"end_turn"} —
    and then stays alive instead of exiting (see _harness.run_stream's
    stop_when).

    It is NOT `usage`. Usage is per-message accounting, and in a tool-using
    turn it lands *before* that message's `tool_call` lines:

        thought… → usage → tool_call(run_terminal_command) → tool_call_update

    Stopping on `usage` therefore ended the run at the model's first sentence.
    grok had said "I will run the command" and the command was never run, yet
    the adapter reported that sentence as the finished answer — which is how
    a task that asked grok to run three commands got three "I am about to do
    it" preambles and a supervisor that kept asking it to actually do the
    work. `end` is the only terminal marker in the stream.

    An `error` line is terminal too: bad key, empty balance, unreachable
    endpoint. grok prints it and then sits there like it does after `end`, so
    without this the run burned the whole watchdog to report a timeout
    instead of the error it already had.
    """
    try:
        return json.loads(line).get("type") in ("end", "error")
    except (ValueError, AttributeError):
        return False


def _is_usage(line):
    """Whether one stream line is the per-message usage report.

    grok writes it after every assistant message, so it doubles as "a message
    just finished" — the signal run_stream's silence timer arms on. `end`
    should follow, but it does not always (see run_stream's idle_after), and
    a turn that produced its answer must not sit out the watchdog waiting for
    a line that never comes.
    """
    try:
        return json.loads(line).get("type") == "usage"
    except (ValueError, AttributeError):
        return False


def _stream_turn(cmd, cwd, timeout, stop_on_tool=None):
    """Run one headless turn over streaming-json.

    Returns (text, returncode, stderr, timed_out, rejected, tool). `rejected`
    is True when the CLI refused the flags outright — an older build, whose
    plain mode does exit on its own — and the caller should fall back to a
    one-shot run. `tool` names the first tool the model asked for, when the
    run was cut short at that request (see stop_on_tool).
    """
    parts = []
    errors = []
    saw_event = [False]
    called = []

    def on_line(line):
        line = line.strip()
        if not line:
            return
        saw_event[0] = True
        try:
            ev = json.loads(line)
        except ValueError:
            return
        if ev.get("type") == "text":
            data = ev.get("data")
            if isinstance(data, str):
                parts.append(data)
        elif ev.get("type") == "error":
            # grok reports a dropped connection, a bad key or an unreachable
            # endpoint as a terminal `error` event. Collecting only `text` made
            # such a run look successful whenever a preamble had arrived, and
            # the orchestrator never failed over.
            msg = ev.get("message") or ev.get("error") or ev.get("data") or ""
            if isinstance(msg, dict):
                msg = msg.get("message") or msg.get("detail") or ""
            if msg:
                errors.append(str(msg))

    def stop_when(line):
        if _is_turn_end(line):
            return True
        if not stop_on_tool or called:
            return False
        try:
            ev = json.loads(line)
        except ValueError:
            return False
        if ev.get("type") != "tool_call":
            return False
        called.append(ev.get("title") or ev.get("toolName") or "a tool")
        return True

    try:
        returncode, err, timed_out = harness.run_stream(
            cmd, cwd=cwd, timeout=timeout, on_line=on_line,
            stop_when=stop_when, idle_after=_is_usage, idle_timeout=6.0)
    except FileNotFoundError:
        raise
    low = err.lower()
    rejected = (not saw_event[0] and returncode != 0 and (
        "unknown option" in low or "unrecognized" in low
        or "unknown flag" in low or "invalid option" in low
        or "invalid value" in low))
    return ("".join(parts).strip(), returncode, err, timed_out, rejected,
            called[0] if called else "", errors[0] if errors else "")


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    # Headless single-turn mode. A positional [PROMPT] starts the interactive
    # TUI, so the prompt must ride --single/-p ("print the response to stdout
    # and exit"), and --always-approve runs tool calls without an interactive
    # permission prompt. --output-format streaming-json, not plain, because
    # this build prints the answer and then keeps running: the event stream
    # carries the lines the adapter stops on, while plain output has no marker
    # at all and left every task to the 600s watchdog.
    #
    # --no-plan was tried and removed: it did not stop the tool-turn hang, and
    # the turn that finally ran the command under it still never returned.
    # Plan mode stays at its default, as upstream ships it.
    #
    # Each run starts a new conversation (see _new_session): this build cannot
    # resume one headless, and passing the handle to -s is refused because the
    # session already exists.
    session = _new_session()
    model = os.environ.get("GROK_MODEL", "")

    def argv(output, session_id):
        cmd = ["grok"]
        if session_id:
            cmd += ["-s", session_id]
        cmd += ["--single", prompt, "--output-format", output,
                "--always-approve"]
        if model:
            cmd += ["--model", model]
        return cmd

    # Transcript pair, same shape run_simple emits for plain adapters: the
    # invocation row records argv shape only (the prompt is inside argv and
    # must not be re-logged), the result row the bounded output.
    harness.emit_event("tool_use", id="grok", name="grok",
                       input={"argc": len(argv("streaming-json", session)) - 1})
    # Announce the session before the run: grok creates it lazily, and a run
    # killed before its result envelope (stall, hard limit) still leaves a
    # checkpoint the Go side latched.
    harness.emit_event("session", id=session)

    try:
        text, returncode, err, timed_out, rejected, tool, stream_error = _stream_turn(
            argv("streaming-json", session), cwd, timeout,
            stop_on_tool=_injected_model())
    except FileNotFoundError:
        harness.emit_event("tool_result", tool_use_id="grok", is_error=True,
                           content="grok binary not found")
        harness.emit(False, "grok binary not found", 127)
        return

    if tool:
        # Cut at the first tool request instead of waiting out the watchdog:
        # on an injected endpoint this build never finishes a tool-using turn
        # (the tool runs, then the turn's second inference never returns — see
        # _injected_model), so the only two outcomes are "hang until the 600 s
        # watchdog" and "say so now". Saying so now keeps the round honest and
        # cheap; the text already emitted is kept above the diagnosis because
        # it is what the model was about to do.
        note = ("grok asked to run %s, and stopped before it: on an injected "
                "model endpoint this CLI's tool turns never return (the tool "
                "executes, then the next inference hangs indefinitely). "
                "grok_build is declared text-only for that reason — give it "
                "a prompt it can answer from the text it already has, or run "
                "it behind its own xAI credentials." % tool)
        result = (text + "\n\n" + note) if text else note
        harness.emit_event("tool_result", tool_use_id="grok", is_error=True,
                           content=result)
        harness.emit(False, result, 1, session_id=session)
        return

    if rejected:
        # A build too old for streaming-json. Its plain mode exits by itself,
        # so the one-shot path is still correct there — and `-s`/`-r` may be
        # missing too, hence the nameless argv.
        try:
            returncode, out, err = harness.run_plain(
                argv("plain", ""), cwd=cwd, timeout=timeout)
        except subprocess.TimeoutExpired:
            harness.emit_event("tool_result", tool_use_id="grok", is_error=True,
                               content="grok timed out")
            harness.emit(False, "grok timed out", 124)
            return
        text = out.strip() or err.strip()
        harness.emit_event("tool_result", tool_use_id="grok",
                           is_error=returncode != 0, content=text)
        # No session_id: this run was nameless (see above), so there is no
        # checkpoint a later round could resume.
        harness.emit(returncode == 0, text, returncode)
        return

    if timed_out:
        harness.emit_event("tool_result", tool_use_id="grok", is_error=True,
                           content="grok timed out")
        # The named session exists grok-side even though this run died —
        # reporting it lets the retry resume rather than cold-start.
        harness.emit(False, "grok timed out", 124, session_id=session)
        return

    # An `error` event ends the turn as a failure even when a preamble arrived:
    # text alone would report ok, the orchestrator would not fail over, and the
    # operator would see "exec failed: " with the real reason discarded. The
    # message is the diagnosis, kept after whatever the model had said.
    if stream_error:
        result = (text + "\n\n" + stream_error) if text else stream_error
    else:
        result = text or err.strip()
    # A turn that reached its own terminal event is a completed turn whatever
    # the exit code says: the tree was killed on purpose once the answer was in
    # hand, so the status reflects the kill rather than the work.
    ok = bool(text) and not stream_error
    harness.emit_event("tool_result", tool_use_id="grok",
                       is_error=not ok, content=result)
    harness.emit(ok, result, 0 if ok else (returncode or 1), session_id=session)


if __name__ == "__main__":
    main()
