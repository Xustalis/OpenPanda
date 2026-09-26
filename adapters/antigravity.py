#!/usr/bin/env python3
"""Adapter: Antigravity CLI (agy) → PANDA Commander.

Protocol (shared with claude_code.py / codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost},
          plus the optional {usage, session_id}

Runs `agy -p <prompt> --output-format json` headlessly: the single JSON
envelope carries the response text, the usage breakdown, and the
conversation_id a follow-up round resumes with (--conversation <id>, so a
supervision continuation keeps agy's own conversation instead of
cold-starting). --dangerously-skip-permissions rides both tool policies: a
headless run cannot answer an interactive permission prompt, so tools run
with permission_mode=always-proceed either way — the same choice grok's
--always-approve and hermes' --yolo make. This adapter never prints secrets.

Auth is agy's own: a cached OS-keyring token profile or a Gemini API key
(GEMINI_API_KEY / GOOGLE_API_KEY). Unauthenticated headless runs exit with an
`authentication required` error, which lands in the result as the diagnosis.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the agy difference: the
command line and the JSON envelope reduction.
"""
import json
import subprocess

import _harness as harness


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req.prompt, req.timeout, req.cwd

    cmd = ["agy", "-p", prompt,
           "--output-format", "json",
           "--dangerously-skip-permissions"]
    if req.resume:
        cmd += ["--conversation", req.resume]

    try:
        returncode, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        harness.emit(False, "agy timed out", 124)
        return
    except FileNotFoundError:
        harness.emit(False, "agy binary not found", 127)
        return

    # The envelope is one JSON object on stdout; diagnostics stay on stderr.
    # Scan lines back-to-front so a stray non-JSON line before the envelope
    # (a warning, a banner) does not sink an otherwise good run.
    env = None
    for line in reversed(out.strip().splitlines()):
        candidate = harness.parse_json_line(line)
        # Only the completion envelope carries status/response — a stray
        # event object must not be mistaken for the run's result.
        if candidate is not None and ("status" in candidate or "response" in candidate):
            env = candidate
            break
    if env is None:
        harness.emit(False, (out.strip() or err.strip() or "agy returned no result"),
                     returncode or 1)
        return

    status = env.get("status") or ""
    response = env.get("response") or ""
    if isinstance(response, str) and not response.strip():
        response = err.strip()

    usage = None
    raw_usage = env.get("usage")
    if isinstance(raw_usage, dict):
        usage = {
            "input_tokens": raw_usage.get("input_tokens") or 0,
            "output_tokens": (raw_usage.get("output_tokens") or 0)
                + (raw_usage.get("thinking_tokens") or 0),
            "cache_read_tokens": raw_usage.get("cache_read_tokens") or 0,
        }

    tokens = 0
    if isinstance(raw_usage, dict):
        tokens = raw_usage.get("total_tokens") or (
            usage["input_tokens"] + usage["output_tokens"])

    ok = status.upper() == "SUCCESS" and returncode == 0
    if not ok and not response:
        response = "agy status " + (status or "?") + ": " + (err.strip() or "(no detail)")
    harness.emit(ok, response, returncode,
                 tokens=tokens or None, usage=usage,
                 session_id=env.get("conversation_id") or "")


if __name__ == "__main__":
    main()
