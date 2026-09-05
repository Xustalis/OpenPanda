#!/usr/bin/env python3
"""Adapter: Grok Build CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus the optional {session_id}

Runs `grok --single <prompt> --output-format plain --always-approve`
headless. Plain output is human-readable text, so the result is used
verbatim; a non-zero exit (or empty output on failure) becomes the
diagnosis. This adapter never prints secrets.

Session continuity: grok addresses conversations by a caller-chosen name
(`-s <name>`), so the adapter names the first run itself (panda-<uuid>),
returns that name as session_id, and a follow-up round passes it back via
-s — the supervision loop keeps grok's own conversation instead of
cold-starting. CLIs too old for -s degrade to a nameless one-shot run.
--always-approve rides both policies: a headless run cannot sit on an
interactive permission prompt, and grok exposes no narrower CLI tool
whitelist, so tools_policy does not change the command line here.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the grok difference:
the command line and the session naming.
"""
import subprocess
import uuid

import _harness as harness


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req

    # Headless single-turn mode. A positional [PROMPT] starts the interactive
    # TUI, so the prompt must ride --single/-p ("print the response to stdout
    # and exit"); --always-approve runs tool calls without an interactive
    # permission prompt, and --output-format plain keeps the result as text.
    # First runs self-name the session with the panda- prefix (the documented
    # and contract-tested marker that the session belongs to PANDA).
    session = req.resume or "panda-" + uuid.uuid4().hex[:12]
    cmd = ["grok", "-s", session, "--single", prompt,
           "--output-format", "plain", "--always-approve"]

    try:
        returncode, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        harness.emit(False, "grok timed out", 124)
        return
    except FileNotFoundError:
        harness.emit(False, "grok binary not found", 127)
        return

    # An old CLI that rejects -s fails before doing any work; retry the run
    # nameless instead of reporting a flag error as a task failure.
    low = err.lower()
    if returncode != 0 and not out.strip() and (
            "unknown option" in low or "unrecognized" in low
            or "unknown flag" in low or "invalid option" in low):
        cmd = [c for c in cmd if c not in ("-s", session)]
        try:
            returncode, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout)
        except subprocess.TimeoutExpired:
            harness.emit(False, "grok timed out", 124)
            return
        session = ""  # nameless run: nothing a later round could resume

    harness.emit(returncode == 0, out.strip() or err.strip(), returncode,
                 session_id=session)


if __name__ == "__main__":
    main()
