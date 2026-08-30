#!/usr/bin/env python3
"""Adapter: DeepSeek Harness (dsh) CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs the harness headlessly (`dsh --profile headless <prompt>`). The prompt
is passed as the positional task description; stdout is returned verbatim and
a non-zero exit becomes the diagnosis. dsh exposes no documented session,
usage or tool-whitelist surface, so resume/tools_policy ride the request but
do not change the command line, and tokens/cost stay unset. This adapter
never prints secrets.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the dsh difference: the
command line.
"""
import _harness as harness


def main():
    prompt, timeout, cwd = harness.read_request()
    harness.run_simple(["dsh", "--profile", "headless", prompt],
                       cwd=cwd, timeout=timeout)


if __name__ == "__main__":
    main()
