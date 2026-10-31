#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
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
import os
import subprocess
import tempfile

import _harness as harness

# Where agy keeps its CLI settings. The Gemini-API-key branch is only taken
# when this file says modelProvider=gemini — with an empty/missing file agy
# goes straight to Google OAuth even when GEMINI_API_KEY is set, which reads
# as "injection did nothing" from the outside.
SETTINGS_PATH = os.path.join(os.path.expanduser("~"), ".gemini",
                             "antigravity-cli", "settings.json")


def _restore_settings(original):
    """Put the operator's settings.json back exactly as it was.

    `original` is the file's bytes before the injected run, or None when there
    was no file. Leaving the rewrite in place would pin the operator's own
    interactive agy to the API-key branch: it then answers "authentication
    required" instead of using its OAuth session, with no way back except
    hand-editing the file.
    """
    try:
        if original is None:
            os.unlink(SETTINGS_PATH)
        else:
            with open(SETTINGS_PATH, "wb") as fh:
                fh.write(original)
    except OSError:
        pass


def _select_gemini_provider():
    """Make agy use its API-key branch (env-supplied key + base URL).

    Merge, never overwrite: the file also carries the operator's own CLI
    preferences (theme, telemetry, …), and an injected run must not reset them.
    The original bytes are registered for restoration on both exit paths — the
    atexit handler and the signal handler that calls os._exit — so a cancelled
    run does not leave the rewrite behind.

    Failure is non-fatal — agy then falls back to OAuth and reports
    "authentication required", which is a truer diagnosis than a crash here.

    Two injected runs still race on this one file (each restores its own copy;
    the later exit wins). agy has no env override for the settings path, so the
    alternative is refusing the second run outright, which is worse than a
    narrow window that only affects concurrent injected agy runs.
    """
    try:
        original = None
        if os.path.exists(SETTINGS_PATH):
            with open(SETTINGS_PATH, "rb") as fh:
                original = fh.read()
        doc = {}
        if original:
            doc = json.loads(original.decode("utf-8")) or {}
        if doc.get("modelProvider") == "gemini":
            return
        doc["modelProvider"] = "gemini"
        os.makedirs(os.path.dirname(SETTINGS_PATH), exist_ok=True)
        fd, tmp = tempfile.mkstemp(prefix=".panda-agy-",
                                   dir=os.path.dirname(SETTINGS_PATH))
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                json.dump(doc, fh, indent=2, ensure_ascii=False)
            os.replace(tmp, SETTINGS_PATH)
        except OSError:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise
        harness.at_cleanup(_restore_settings, original)
    except (OSError, ValueError):
        pass


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req.prompt, req.timeout, req.cwd

    if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1":
        _select_gemini_provider()

    cmd = ["agy", "-p", prompt,
           "--output-format", "json",
           "--dangerously-skip-permissions"]
    if req.resume:
        cmd += ["--conversation", req.resume]

    # Transcript pair: a CLI without a native event stream still reads as a
    # node — the invocation row carries argv shape (the prompt is inside argv
    # and must not be re-logged), the result row the bounded output.
    harness.emit_event("tool_use", id="agy", name="agy",
                       input={"argc": len(cmd) - 1})
    if req.resume:
        # A resumed run's conversation id is known up front; announce it so
        # a mid-run kill still reports the checkpoint the Go side latched.
        # (First runs only learn conversation_id from the final envelope.)
        harness.emit_event("session", id=req.resume)
    try:
        returncode, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        harness.emit_event("tool_result", tool_use_id="agy", is_error=True,
                           content="agy timed out")
        # A resumed run already knows its conversation id: report it so the
        # retry re-enters the same conversation instead of cold-starting.
        harness.emit(False, "agy timed out", 124,
                     session_id=req.resume or None)
        return
    except FileNotFoundError:
        harness.emit_event("tool_result", tool_use_id="agy", is_error=True,
                           content="agy binary not found")
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
    harness.emit_event("tool_result", tool_use_id="agy", is_error=not ok,
                       content=response)
    harness.emit(ok, response, returncode,
                 tokens=tokens or None, usage=usage,
                 session_id=env.get("conversation_id") or "")


if __name__ == "__main__":
    main()
