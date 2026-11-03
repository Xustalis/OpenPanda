#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: OpenClaw CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `openclaw agent exec <prompt>` headlessly. stdout is returned verbatim
and a non-zero exit becomes the diagnosis. openclaw exposes no documented
session or tool-whitelist surface, so resume/tools_policy ride the request but
do not change the command line. This adapter never prints secrets.

Injected runs (OPENPANDA_INJECTED_MODEL=1) use openclaw's reproducible-exec
surface instead of the ambient one:

  openclaw agent exec --config <cfg> --state-dir <dir> --model panda/<m> --json

`--config` pins a config file we generate, so the model entry is a provider
openclaw has never seen and the operator's own openclaw.json is not edited.
The key is NOT in that file: `apiKey` is a secret reference
(`{source: env, provider: pandaenv, id: OPENCLAW_API_KEY}`) resolved against a
`secrets.providers.pandaenv` entry that reads the injected environment, so
the credential stays on the wire from panda to the child process only.

Two paths must be redirected for this to be hermetic, and only one of them
has a flag:

  * config + gateway state → `--config` / `--state-dir` (temp dir).
  * the auth-profile store → **no flag exists**. openclaw resolves it from
    the home directory (``~\\.openclaw\\agents\\<id>\\agent\\openclaw-agent.sqlite``)
    no matter what OPENCLAW_STATE_DIR or --state-dir say, and a store written
    by an older release aborts the turn outright with "requires legacy
    credential migration" — which on Windows cannot even be repaired by
    `openclaw doctor --fix` when the profile directory fails its ACL check.
    So the injected path also points HOME/USERPROFILE at a temp dir. That is
    the whole reason a stale store stops mattering: an injected run never
    reads the operator's store, and never needs it to be migrated.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the openclaw difference:
the command line, the heartbeat cadence of its silent run, and that sandbox.
"""
import json
import os
import re
import shutil
import subprocess
import tempfile

import _harness as harness

# Provider id the injected model rides under, and the name of the secret
# provider entry that resolves its key out of the environment.
PROVIDER_ID = "panda"
SECRET_PROVIDER = "pandaenv"

# openclaw's provider API adapter per PANDA dialect. "openai-completions" is
# the chat/completions wire (not the Responses one): panda's injected endpoint
# is an OpenAI-compatible /chat/completions server.
API_TYPES = {
    "openai": "openai-completions",
    "anthropic": "anthropic-messages",
}

_SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/-]*$")


def _injected_config(model, base_url, api_type):
    """Write the pinned config: one provider, one model, no secret.

    The key rides `{source: env, provider: pandaenv, id: <var>}` — openclaw
    resolves that through `secrets.providers.pandaenv`, an env reader. Nothing
    in this document is sensitive, so the temp file may live for the run.
    """
    provider = {
        "baseUrl": base_url,
        "api": API_TYPES.get(api_type or "openai", "openai-completions"),
        "models": [{"id": model, "name": model}],
    }
    if os.environ.get("OPENCLAW_API_KEY"):
        provider["auth"] = "api-key"
        provider["apiKey"] = {
            "source": "env",
            "provider": SECRET_PROVIDER,
            "id": "OPENCLAW_API_KEY",
        }
    return {
        "secrets": {"providers": {SECRET_PROVIDER: {"source": "env"}}},
        "models": {"providers": {PROVIDER_ID: provider}},
    }


def _sandbox(model, base_url, api_type):
    """Lay out a throwaway config/state/home triple; return its root or ""."""
    model = (model or "").strip()
    base_url = (base_url or "").strip()
    if not model or not base_url or not _SAFE_ID.match(model):
        return ""
    try:
        root = tempfile.mkdtemp(prefix="panda-openclaw-")
    except OSError:
        return ""
    try:
        state = os.path.join(root, "state")
        home = os.path.join(root, "home")
        os.makedirs(state, exist_ok=True)
        os.makedirs(home, exist_ok=True)
        with open(os.path.join(root, "config.json"), "w", encoding="utf-8") as fh:
            json.dump(_injected_config(model, base_url, api_type), fh, ensure_ascii=False)
    except OSError:
        shutil.rmtree(root, ignore_errors=True)
        return ""
    # HOME/USERPROFILE: see the module docstring — the auth-profile store has
    # no path flag and would otherwise be read from (and demanded from) the
    # operator's real home.
    os.environ["HOME"] = home
    os.environ["USERPROFILE"] = home
    harness.at_cleanup(shutil.rmtree, root, True)
    return root


def _reduce(raw):
    """Reduce the `agent exec --json` envelope to the unified result.

    The envelope is documented as stable: {ok, status, final, payloads[],
    usage, model, provider, sessionId, error}. Anything that does not parse
    falls through to the caller's raw-text path — a CLI that printed a banner
    before its JSON should still deliver an answer.
    """
    start = raw.find("{")
    if start < 0:
        return None
    try:
        doc = json.loads(raw[start:])
    except ValueError:
        return None
    if not isinstance(doc, dict) or "ok" not in doc:
        return None
    text = doc.get("final") or ""
    if not text:
        for payload in doc.get("payloads") or []:
            if isinstance(payload, dict) and payload.get("text"):
                text = payload["text"]
                break
    usage = doc.get("usage") if isinstance(doc.get("usage"), dict) else {}
    mapped = {}
    for src, dst in (("input", "input_tokens"), ("output", "output_tokens"),
                     ("cacheRead", "cache_read_tokens"),
                     ("cacheWrite", "cache_write_tokens")):
        if isinstance(usage.get(src), int):
            mapped[dst] = usage[src]
    total = usage.get("total") if isinstance(usage.get("total"), int) else None
    err = doc.get("error")
    if isinstance(err, dict):
        err = err.get("message") or json.dumps(err, ensure_ascii=False)
    return {
        "ok": doc.get("ok") is True,
        "text": text,
        "error": err or "",
        "usage": mapped,
        "tokens": total,
        "session_id": doc.get("sessionId") or "",
    }


# How often a silent run says it is still alive. `openclaw agent exec` prints
# nothing until the run finishes, so without this the Go silence watchdog sees
# a dead pipe for the whole run and kills a healthy long task at the default
# 4-minute silence limit. The heartbeat does not shorten a wedged run: the
# hard agent budget still bounds it.
HEARTBEAT_S = 20


def main():
    prompt, timeout, cwd = harness.read_request()

    if os.environ.get("OPENPANDA_INJECTED_MODEL") != "1":
        harness.run_simple(["openclaw", "agent", "exec", prompt],
                           cwd=cwd, timeout=timeout, heartbeat_s=HEARTBEAT_S)
        return

    model = os.environ.get("OPENCLAW_MODEL", "")
    root = _sandbox(model, os.environ.get("OPENAI_BASE_URL", ""),
                    os.environ.get("OPENPANDA_MODEL_API_TYPE", ""))
    if not root:
        # A half-configured injection must not take the agent down with it:
        # run the agent's own model exactly as the non-injected path does.
        harness.progress("injection carried no usable endpoint; using the agent's own model")
        harness.run_simple(["openclaw", "agent", "exec", prompt],
                           cwd=cwd, timeout=timeout, heartbeat_s=HEARTBEAT_S)
        return

    qualified = PROVIDER_ID + "/" + model
    cmd = ["openclaw", "agent", "exec",
           "--config", os.path.join(root, "config.json"),
           "--state-dir", os.path.join(root, "state"),
           "--model", qualified,
           "--json", prompt]
    harness.progress("using injected model %s via a pinned openclaw config" % qualified)
    label = "openclaw"
    harness.emit_event("tool_use", id=label, name=label,
                       input={"argc": max(0, len(cmd) - 1)})
    try:
        rc, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout,
                                         heartbeat_s=HEARTBEAT_S)
    except subprocess.TimeoutExpired:
        harness.emit_event("tool_result", tool_use_id=label, is_error=True,
                           content="openclaw timed out")
        harness.emit(False, "openclaw timed out", 124)
        return
    except FileNotFoundError:
        harness.emit_event("tool_result", tool_use_id=label, is_error=True,
                           content="openclaw binary not found")
        harness.emit(False, "openclaw binary not found", 127)
        return

    doc = _reduce(out) if out else None
    if doc is None:
        # No envelope: report what the CLI actually said, exactly like the
        # plain path — a malformed envelope must not look like a silent pass.
        harness.emit_event("tool_result", tool_use_id=label,
                           is_error=rc != 0, content=out.strip() or err.strip())
        harness.emit(rc == 0, out.strip() or err.strip() or "(no output)", rc)
        return
    text = doc["text"] or doc["error"] or err.strip() or "(no output)"
    harness.emit_event("tool_result", tool_use_id=label,
                       is_error=not doc["ok"], content=text)
    harness.emit(doc["ok"], text, rc if rc != 0 else (0 if doc["ok"] else 1),
                 tokens=doc["tokens"], usage=doc["usage"] or None,
                 session_id=doc["session_id"] or None)


if __name__ == "__main__":
    main()
