#!/usr/bin/env python3
"""Adapter: Hermes Agent CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `hermes --cli --yolo -z <prompt>` headlessly (CLI output mode, one-shot
prompt, auto-approved tools). stdout is returned verbatim and a non-zero exit
becomes the diagnosis. --yolo rides both tool policies: a headless run cannot
sit on an interactive permission prompt, and hermes exposes no narrower CLI
tool whitelist, so resume/tools_policy ride the request but do not change the
command line. This adapter never prints secrets.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the hermes difference: the
command line.
"""
import _harness as harness


def main():
    prompt, timeout, cwd = harness.read_request()

    # --cli: text output mode; --yolo: auto-approve tool calls; -z: one-shot
    # prompt so the agent runs to completion without an interactive session.
    cmd = ["hermes", "--cli", "--yolo", "-z", prompt]
    harness.run_simple(cmd, cwd=cwd, timeout=timeout)


if __name__ == "__main__":
    main()
