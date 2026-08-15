#!/usr/bin/env python3
"""Adapter: Claude Code CLI → PANDA Commander.

Protocol:
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

The Go core injects ANTHROPIC_API_KEY via the process environment; this
adapter never prints it.
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
    try:
        timeout = int(req.get("timeout_s", DEFAULT_TIMEOUT))
    except (TypeError, ValueError):
        timeout = DEFAULT_TIMEOUT
    cwd = req.get("cwd") or None

    cmd = [
        "claude", "-p", prompt,
        "--output-format", "json",
        "--allowedTools", "Read,Write,Edit,Bash,Grep,Glob",
        "--max-turns", "30",
        "--permission-mode", "acceptEdits",
    ]

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
        _emit(False, "claude timed out", 124)
        return
    except FileNotFoundError:
        _emit(False, "claude binary not found", 127)
        return

    out = {}
    try:
        out = json.loads(result.stdout or "{}")
    except json.JSONDecodeError:
        pass

    _emit(
        result.returncode == 0,
        out.get("result", result.stdout or result.stderr),
        result.returncode,
        tokens=out.get("total_tokens"),
        cost=out.get("total_cost_usd"),
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
