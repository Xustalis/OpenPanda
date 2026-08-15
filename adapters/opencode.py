#!/usr/bin/env python3
"""Adapter: OpenCode CLI → PANDA Commander.

Protocol (shared with claude_code.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

OpenCode is model-agnostic. By default it runs opencode's built-in free model,
which needs no API key or provider configuration; set OPENCODE_MODEL (or
ANTHROPIC_MODEL) to a provider/model id (e.g. deepseek/deepseek-chat) to use a
custom provider. This adapter never prints secrets.
"""
import json
import os
import subprocess
import sys

DEFAULT_TIMEOUT = 600
# opencode resolves --model as provider/model; a bare model name is not a valid
# provider and fails resolution. The built-in free model needs no key or config,
# so it is the default when no provider/model id is given.
DEFAULT_MODEL = "opencode/deepseek-v4-flash-free"


def main():
    raw = sys.stdin.read()
    try:
        req = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        _emit(False, "invalid request JSON", 2)
        return

    prompt = req.get("prompt", "")
    timeout = int(req.get("timeout_s", DEFAULT_TIMEOUT))
    cwd = req.get("cwd") or None

    # opencode resolves --model as provider/model; a bare model name fails. A
    # provider/model id from env wins; otherwise default to the built-in free
    # model, which needs no API key.
    model = os.environ.get("OPENCODE_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    if "/" not in model:
        model = DEFAULT_MODEL
    cmd = ["opencode", "run", "--print-logs=false", "--model", model, prompt]

    env = os.environ.copy()
    # No API-key requirement: the free provider needs none; a custom provider
    # supplies its own credentials. stdin is /dev/null so opencode never waits
    # on an inherited pipe for the prompt.
    try:
        result = subprocess.run(
            cmd, stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=timeout, env=env, cwd=cwd,
        )
    except subprocess.TimeoutExpired:
        _emit(False, "opencode timed out", 124)
        return
    except FileNotFoundError:
        _emit(False, "opencode binary not found", 127)
        return

    _emit(
        result.returncode == 0,
        result.stdout.strip() or result.stderr.strip(),
        result.returncode,
    )


def _emit(ok, result, exit_code, tokens=None, cost=None):
    payload = {
        "ok": bool(ok),
        "result": result,
        "exit_code": exit_code,
    }
    if tokens is not None:
        payload["tokens"] = tokens
    if cost is not None:
        payload["cost"] = cost
    print(json.dumps(payload, ensure_ascii=False))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
