#!/usr/bin/env python3
"""Adapter: Grok Build CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `grok --single <prompt> --output-format plain --always-approve` headless.
Plain output is human-readable text, so the result is used verbatim; a
non-zero exit (or empty output on failure) becomes the diagnosis. This
adapter never prints secrets.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the grok difference: the
command line.
"""
import _harness as harness


def main():
    prompt, timeout, cwd = harness.read_request()

    # Headless single-turn mode. A positional [PROMPT] starts the interactive
    # TUI, so the prompt must ride --single/-p ("print the response to stdout
    # and exit"); --always-approve runs tool calls without an interactive
    # permission prompt, and --output-format plain keeps the result as text.
    cmd = ["grok", "--single", prompt, "--output-format", "plain", "--always-approve"]
    harness.run_simple(cmd, cwd=cwd, timeout=timeout)


if __name__ == "__main__":
    main()
