#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: OpenClaw CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `openclaw agent --local --json --message <prompt>` headlessly. stdout is
reduced to the answer payload and a non-zero exit becomes the diagnosis. The
session id is chosen here (or resumed from the request), so a supervision
continuation keeps the same openclaw session. tools_policy rides the request
but does not change the command line. This adapter never prints secrets.

Injected runs (OPENPANDA_INJECTED_MODEL=1) use openclaw's embedded,
config-pinned surface instead of the ambient gateway one:

  OPENCLAW_CONFIG_PATH=<cfg> OPENCLAW_STATE_DIR=<dir> \
    openclaw agent --local --json --session-id <id> --message <prompt> \
                   --model panda/<m>

The config pins a provider openclaw has never seen, so the operator's own
openclaw.json is not edited. `OPENCLAW_CONFIG_PATH` / `OPENCLAW_STATE_DIR` are
the only knobs 2026.6.x exposes for that: the older `agent exec --config
<file> --state-dir <dir>` surface is gone, and the flags are refused outright
("does not recognize option --config"). The key is NOT in the config file:
`apiKey` is a secret reference (`{source: env, provider: pandaenv, id:
OPENCLAW_API_KEY}`) resolved against a `secrets.providers.pandaenv` entry that
reads the injected environment, so the credential stays on the wire from panda
to the child process only.

Two paths must be redirected for this to be hermetic, and neither has a flag:

  * config + gateway state → OPENCLAW_CONFIG_PATH / OPENCLAW_STATE_DIR.
  * the auth-profile store → **no setting exists**. openclaw resolves it from
    the home directory (``~\\.openclaw\\agents\\<id>\\agent\\openclaw-agent.sqlite``)
    no matter what the state dir says, and a store written by an older release
    aborts the turn outright with "requires legacy credential migration" —
    which on Windows cannot even be repaired by `openclaw doctor --fix` when
    the profile directory fails its ACL check. So the injected path also points
    HOME/USERPROFILE at a temp dir. That is the whole reason a stale store
    stops mattering: an injected run never reads the operator's store, and
    never needs it to be migrated.

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
import uuid

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
    # 2026.6.x takes the pinned config and state from the environment. The
    # older `agent exec --config/--state-dir` flags are gone: the CLI answers
    # "does not recognize option --config" before the model is ever contacted.
    os.environ["OPENCLAW_CONFIG_PATH"] = os.path.join(root, "config.json")
    os.environ["OPENCLAW_STATE_DIR"] = state
    # HOME/USERPROFILE: see the module docstring — the auth-profile store has
    # no path flag and would otherwise be read from (and demanded from) the
    # operator's real home.
    os.environ["HOME"] = home
    os.environ["USERPROFILE"] = home
    harness.at_cleanup(shutil.rmtree, root, True)
    return root


def _reduce(raw):
    """Reduce the `agent --json` envelope to the unified result.

    Two shapes are in the wild:

      * 2026.6.x — {"payloads": [{"text": …}], "meta": {"agentMeta":
        {"sessionId", "provider", "model", "usage"}, "aborted",
        "finalAssistantVisibleText", "stopReason"}}. There is no top-level
        `ok`; the verdict is the absence of `aborted`/`error`.
      * the older documented one — {ok, status, final, payloads[], usage,
        model, provider, sessionId, error}.

    Anything that does not parse falls through to the caller's raw-text path —
    a CLI that printed a banner before its JSON should still deliver an answer.
    """
    start = raw.find("{")
    if start < 0:
        return None
    try:
        doc = json.loads(raw[start:])
    except ValueError:
        return None
    if not isinstance(doc, dict):
        return None
    if "ok" not in doc and "payloads" not in doc and "meta" not in doc:
        return None

    meta = doc.get("meta") if isinstance(doc.get("meta"), dict) else {}
    agent_meta = meta.get("agentMeta") if isinstance(meta.get("agentMeta"), dict) else {}

    text = doc.get("final") or meta.get("finalAssistantVisibleText") or ""
    if not text:
        for payload in doc.get("payloads") or []:
            if isinstance(payload, dict) and payload.get("text"):
                text = payload["text"]
                break
    if not text:
        text = meta.get("finalAssistantRawText") or ""

    # The usage breakdown moved from the top level to meta.agentMeta.usage in
    # 2026.6.x; the key names did not change.
    usage = doc.get("usage") if isinstance(doc.get("usage"), dict) else {}
    if not usage and isinstance(agent_meta.get("usage"), dict):
        usage = agent_meta["usage"]
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
    aborted = meta.get("aborted") is True
    if "ok" in doc:
        ok = doc.get("ok") is True
    else:
        ok = not aborted and not err
    return {
        "ok": ok,
        "text": text,
        "error": err or ("aborted" if aborted else ""),
        "usage": mapped,
        "tokens": total,
        "session_id": doc.get("sessionId") or agent_meta.get("sessionId") or "",
    }


# How often a silent run says it is still alive. `openclaw agent` prints
# nothing until the run finishes, so without this the Go silence watchdog sees
# a dead pipe for the whole run and kills a healthy long task at the default
# 4-minute silence limit. The heartbeat does not shorten a wedged run: the
# hard agent budget still bounds it.
HEARTBEAT_S = 20


def _command(session_id, prompt, model):
    """The headless invocation for openclaw 2026.6.x.

    `agent exec` is not a subcommand any more (`exec` is parsed as a stray
    argument, and the flags below then earn "Too many arguments for this
    command"), the prompt must ride --message instead of argv position, and
    the config/state come from OPENCLAW_CONFIG_PATH / OPENCLAW_STATE_DIR
    rather than flags. `--local` runs the embedded agent instead of dialling
    the gateway: a headless task must not depend on — or deliver into — the
    operator's channel sessions. `--session-id` is the one session selector
    that needs no configured agent id.
    """
    cmd = ["openclaw", "agent", "--local", "--json",
           "--session-id", session_id, "--message", prompt]
    if model:
        cmd += ["--model", model]
    return cmd


def main():
    req = harness.read_request()
    prompt, timeout, cwd = req
    session_id = req.resume or str(uuid.uuid4())

    if os.environ.get("OPENPANDA_INJECTED_MODEL") != "1":
        cmd = _command(session_id, prompt,
                       harness.selected_model(os.environ.get("OPENCLAW_MODEL", "")))
        harness.run_simple(cmd, cwd=cwd, timeout=timeout, heartbeat_s=HEARTBEAT_S)
        return

    model = os.environ.get("OPENCLAW_MODEL", "")
    root = _sandbox(model, os.environ.get("OPENAI_BASE_URL", ""),
                    os.environ.get("OPENPANDA_MODEL_API_TYPE", ""))
    if not root:
        # A half-configured injection must not take the agent down with it:
        # run the agent's own model exactly as the non-injected path does.
        harness.progress("injection carried no usable endpoint; using the agent's own model")
        cmd = _command(session_id, prompt,
                       harness.selected_model(os.environ.get("OPENCLAW_MODEL", "")))
        harness.run_simple(cmd, cwd=cwd, timeout=timeout, heartbeat_s=HEARTBEAT_S)
        return

    qualified = PROVIDER_ID + "/" + model
    cmd = _command(session_id, prompt, qualified)
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
