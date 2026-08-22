#!/usr/bin/env python3
"""Adapter: OpenClaw CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `openclaw agent exec <prompt>` headlessly. stdout is returned verbatim
and a non-zero exit becomes the diagnosis. This adapter never prints secrets.
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

    cmd = ["openclaw", "agent", "exec", prompt]

    try:
        result = subprocess.run(
            cmd, stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=timeout, env=os.environ.copy(), cwd=cwd,
        )
    except subprocess.TimeoutExpired:
        _emit(False, "openclaw timed out", 124)
        return
    except FileNotFoundError:
        _emit(False, "openclaw binary not found", 127)
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