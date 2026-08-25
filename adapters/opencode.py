#!/usr/bin/env python3
"""Adapter: OpenCode CLI → PANDA Commander.

Protocol (shared with claude_code.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

OpenCode is model-agnostic. By default it runs opencode's built-in free model,
which needs no API key or provider configuration; set OPENCODE_MODEL (or
ANTHROPIC_MODEL) to a provider/model id (e.g. deepseek/deepseek-chat) to use a
custom provider. This adapter never prints secrets.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the opencode difference:
model resolution and the command line.
"""
import os

import _harness as harness

# opencode resolves --model as provider/model; a bare model name is not a valid
# provider and fails resolution. The built-in free model needs no key or config,
# so it is the default when no provider/model id is given.
DEFAULT_MODEL = "opencode/deepseek-v4-flash-free"


def main():
    prompt, timeout, cwd = harness.read_request()

    # opencode resolves --model as provider/model; a bare model name fails. A
    # provider/model id from env wins; otherwise default to the built-in free
    # model, which needs no API key. stdin is /dev/null so opencode never waits
    # on an inherited pipe for the prompt.
    model = os.environ.get("OPENCODE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    if "/" not in model:
        model = DEFAULT_MODEL
    harness.run_simple(
        ["opencode", "run", "--print-logs=false", "--model", model, prompt],
        cwd=cwd, timeout=timeout)


if __name__ == "__main__":
    main()
