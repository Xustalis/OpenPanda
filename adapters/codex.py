#!/usr/bin/env python3
"""Adapter: Codex CLI → PANDA Commander.

Protocol (shared with claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional)
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `codex exec --json` and reduces its JSONL event stream to the final
agent message. Codex emits one JSON object per line; the event shape has
changed across versions, so both the current item envelopes
(item.completed → agent_message) and the legacy msg envelopes
(msg.type == agent_message / task_complete) are recognised. This adapter
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
    try:
        timeout = int(req.get("timeout_s", DEFAULT_TIMEOUT))
    except (TypeError, ValueError):
        timeout = DEFAULT_TIMEOUT
    cwd = req.get("cwd") or None

    cmd = ["codex", "exec", "--json", "--skip-git-repo-check"]
    # Optional model override: CODEX_MODEL (or the generic ANTHROPIC_MODEL
    # convention used by the other adapters) maps to codex's -m flag.
    model = os.environ.get("CODEX_MODEL") or os.environ.get("ANTHROPIC_MODEL", "")
    if model:
        cmd += ["--model", model]
    cmd.append(prompt)

    try:
        result = subprocess.run(
            cmd, stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=timeout, env=os.environ.copy(), cwd=cwd,
        )
    except subprocess.TimeoutExpired:
        _emit(False, "codex timed out", 124)
        return
    except FileNotFoundError:
        _emit(False, "codex binary not found", 127)
        return

    text, tokens = _reduce(result.stdout)
    if not text:
        # No parseable events (older CLI without --json, or a plain error):
        # fall back to whatever the CLI printed.
        text = result.stdout.strip() or result.stderr.strip()
    _emit(result.returncode == 0, text, result.returncode, tokens)


def _reduce(stdout):
    """Collapse codex's JSONL event stream to (final agent message, tokens)."""
    texts = []
    tokens = None
    for line in stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(obj, dict):
            continue
        # Current format: {"type":"item.completed","item":{"type":"agent_message","text":…}}
        item = obj.get("item")
        if isinstance(item, dict) and item.get("type") == "agent_message" and item.get("text"):
            texts.append(str(item["text"]))
        # Current format usage rides the turn envelope.
        if obj.get("type") == "turn.completed" and isinstance(obj.get("usage"), dict):
            usage = obj["usage"]
            try:
                tokens = int(usage.get("input_tokens", 0)) + int(usage.get("output_tokens", 0))
            except (TypeError, ValueError):
                tokens = None
        # Legacy format: {"msg":{"type":"agent_message","message":…}}
        msg = obj.get("msg")
        if isinstance(msg, dict):
            if msg.get("type") == "agent_message" and msg.get("message"):
                texts.append(str(msg["message"]))
            if msg.get("type") == "task_complete" and msg.get("last_agent_message"):
                texts.append(str(msg["last_agent_message"]))
    # The last agent message is the answer; earlier ones are interstitial.
    return (texts[-1] if texts else ""), tokens


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
