#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: DeepSeek Harness (dsh) CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Projects headless JSON events onto the task transcript and resumes by session ID.

Model injection is the one place this adapter is more than an argv bridge:

dsh reads DEEPSEEK_API_KEY and DEEPSEEK_BASE_URL from the environment, but it
has no DEEPSEEK_MODEL equivalent — the model comes from the profile's
`agent-default-model` entry. When PANDA injects a model (the commander sets
DEEPSEEK_MODEL beside the key and endpoint), this adapter writes a one-entry
profile patch pinning that model and passes it as `--patch`. Without it the
run would quietly keep dsh's own default model while panda's announcement
claimed otherwise.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the dsh difference: the
command line.
"""
import json
import os
import re
import tempfile

import _harness as harness

# What the CLI may be called on PATH. The registry probes the same list
# (internal/agents/registry.go), so the roster and the adapter agree about
# whether `dsh` is installed instead of the roster saying yes and the spawn
# exiting 127.
BINARIES = ["dsh", "deepseek-harness"]

# Older plain-output builds can remain silent until the answer arrives.
HEARTBEAT_S = 20

# The profile entry that chooses the model. Its shipped config is
# {provider: deepseek-official, model: deepseek-flash}; a patch entry's config
# REPLACES the whole block, so the provider has to ride along.
MODEL_ENTRY_ID = "agent-default-model"
MODEL_PROVIDER = "deepseek-official"
PROVIDER_ENTRY_ID = "llm-deepseek"
MAX_TOKENS = 131072


def model_patch(model):
    """Write the profile patch pinning dsh's default model; return its path.

    The model name comes from panda's config, so it is emitted as a quoted
    YAML scalar. json.dumps is the escaper: a JSON string is a valid
    double-quoted YAML scalar, and it handles newlines, tabs and control
    characters that a hand-rolled backslash/quote escape let through — a model
    name carrying a line break could open a new mapping entry.
    """
    quoted = json.dumps(str(model))
    body = (
        "- id: %s\n"
        "  config:\n"
        "    provider: %s\n"
        '    model: %s\n'
        "- id: %s\n"
        "  config:\n"
        "    maxTokens: %d\n"
        % (MODEL_ENTRY_ID, MODEL_PROVIDER, quoted, PROVIDER_ENTRY_ID, MAX_TOKENS)
    )
    fd, path = tempfile.mkstemp(prefix="panda-dsh-model-", suffix=".yml")
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(body)
    return path


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req
    binary = harness.resolve_binary(BINARIES, default="dsh")
    argv = [binary, "--profile", "headless", "--json"]
    patch = ""
    model = os.environ.get("DEEPSEEK_MODEL", "").strip() if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1" else ""
    if model:
        try:
            patch = model_patch(model)
            argv += ["--patch", patch]
        except OSError as ex:
            harness.emit(False, "dsh: could not apply the injected model: %s" % ex, 1)
            return
    if req.restricted:
        argv += ["--permission-mode", "read-only", "--approval-policy", "never"]
    if req.resume:
        argv += ["--session-id", req.resume]
    try:
        state = {"text": "", "final": "", "session_id": "", "error": "", "seen": False}

        def on_line(line):
            event = harness.parse_json_line(line)
            if event is None:
                return
            state["seen"] = True
            kind = event.get("type")
            if kind == "session":
                state["session_id"] = str(event.get("sessionId") or "")
                if state["session_id"]:
                    harness.emit_event("session", id=state["session_id"])
            elif kind == "text":
                text = event.get("text")
                if isinstance(text, str):
                    state["text"] += text
                    harness.emit_event("text", text=text)
            elif kind == "final":
                state["final"] = event["text"] if isinstance(event.get("text"), str) else ""
                state["session_id"] = str(event.get("sessionId") or state["session_id"])
                if state["final"] and not state["text"]:
                    harness.emit_event("text", text=state["final"])
            elif kind == "thinking":
                harness.emit_event("thinking", thinking=str(event.get("text") or ""))
            elif kind == "tool_call":
                call_id = str(event.get("callId") or "")
                name = str(event.get("tool") or "tool")
                args = event.get("input")
                if not isinstance(args, dict):
                    args = {"input": args} if args is not None else {}
                harness.emit_event("tool_use", id=call_id, name=name, input=args)
            elif kind == "tool_result":
                content = event.get("result") or event.get("error") or ""
                harness.emit_event("tool_result", tool_use_id=str(event.get("callId") or ""),
                                   is_error=event.get("status") == "error", content=str(content))
            elif kind == "error":
                state["error"] = str(event.get("message") or "dsh failed")

        try:
            code, stderr, timed_out = harness.run_stream(
                argv + harness.prompt_argv(prompt), cwd=cwd, timeout=timeout,
                on_line=on_line)
        except (FileNotFoundError, PermissionError, OSError, ValueError) as ex:
            harness.emit(False, "dsh spawn failed: %s" % ex, 1)
            return
        if timed_out:
            result, code = state["text"] or "dsh timed out", 124
        elif code and not state["seen"] and re.search(
                r"(?im)^.*(?:unknown|unrecognized|unexpected) (?:option|argument|flag)[: ]+\W*--json(?:\W|$)", stderr):
            legacy = argv.copy()
            legacy.remove("--json")
            harness.run_simple(legacy + harness.prompt_argv(prompt), cwd=cwd, timeout=timeout,
                               label="dsh", heartbeat_s=HEARTBEAT_S)
            return
        elif code != 0 or state["error"]:
            result = state["error"] or stderr.strip() or state["text"] or "dsh failed"
            code = code or 1
        elif not state["final"].strip():
            result, code = "dsh returned no final text", 1
        else:
            result = state["final"].strip()
        harness.emit(code == 0, result, code, session_id=state["session_id"] or None)
    finally:
        if patch:
            try:
                os.unlink(patch)
            except OSError:
                pass


if __name__ == "__main__":
    main()
