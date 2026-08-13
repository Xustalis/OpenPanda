#!/usr/bin/env python3
"""Adapter: OpenCode CLI → PANDA Commander.

Protocol (shared with claude_code.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

OpenCode is model-agnostic; the Go core injects ANTHROPIC_BASE_URL /
ANTHROPIC_API_KEY / ANTHROPIC_MODEL so `opencode run` routes through the same
configured provider (DeepSeek by default) as the entry model. This adapter
never prints secrets.
"""
import json
import os
import subprocess
import sys

DEFAULT_TIMEOUT = 600


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

    # OpenCode picks up the Anthropic-compatible endpoint from the env the Go
    # core sets; --model selects the provider model.
    model = os.environ.get("ANTHROPIC_MODEL", "")
    cmd = ["opencode", "run", "--print-logs=false"]
    if model:
        cmd += ["--model", model]
    cmd += [prompt]

    env = os.environ.copy()
    # Go core must have set this; surface a clear error if absent.
    if not env.get("ANTHROPIC_API_KEY"):
        _emit(False, "ANTHROPIC_API_KEY not set", 3)
        return

    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout, env=env, cwd=cwd
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
