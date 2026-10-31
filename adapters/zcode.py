#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: ZCode CLI (Z.AI) → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `zcode --prompt <prompt>` headlessly (one-shot prompt; zcode defaults to
its yolo permission mode for prompts, so no approval prompt can block a
headless turn). stdout is returned verbatim and a non-zero exit becomes the
diagnosis. zcode exposes no tool whitelist or resume flag, so resume/
tools_policy ride the request but do not change the command line.

Injected runs (OPENPANDA_INJECTED_MODEL=1) have to answer a question zcode
asks before it will talk to anyone. With no model chosen it refuses with
"Select a model before sending a message", and there is NO `--model` flag: the
selection is read from zcode's personal provider config
(`~/.zcode/v2/provider_config.json`), the same file the TUI's `/model` writes.
The ZCODE_* env trio the registry declares is not read by 0.17 at all —
verified: the run still dies on "Select a model" with all three set.

So an injected run generates that personal config in a temp file and points
ZCODE_PERSONAL_PROVIDER_CONFIG_FILE at it (0.17 honours the override; the
operator's own file is never touched). The document has two halves that must
agree, and the shape below is the one the CLI itself emits when it imports a
provider (see `parseLegacyCliModelConfig` → `Duc`):

  config.providerConfigRules.providerRules[]  one `panda` provider:
      group "standard-personal" (the group the CLI uses for a user-supplied
      endpoint), access {type: api-key}, api {type, baseUrl}, and the model in
      personalModelIds/modelOrder.
  config.defaultModelSelection               {providerId, modelId} — the
      answer to "which model?", which must live INSIDE config (a top-level
      copy is silently ignored and the run still asks for a model).

Unlike hermes/openclaw, zcode has no env-reference for the key: the provider
entry carries the API key as a literal. The file therefore goes into a temp
directory and is deleted when the run ends — the key is on disk for the
duration of one task, not in the operator's zcode state.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the zcode difference: the
command line plus that config shim.
"""
import json
import os
import re
import shutil
import subprocess
import tempfile

import _harness as harness

# Provider id the injected endpoint rides under. Cosmetic — it only has to
# avoid colliding with a provider the operator configured.
PROVIDER_ID = "panda"

# Where zcode looks for the personal provider config; overriding it keeps the
# generated document out of the operator's ~/.zcode.
CONFIG_ENV = "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"

# zcode's provider API adapter per PANDA dialect. zcode's own default provider
# is Anthropic-wire (api.z.ai/api/anthropic), so an injected OpenAI endpoint
# has to say so explicitly or the request goes out in the wrong shape.
API_TYPES = {
    "openai": "openai-chat-completions",
    "anthropic": "anthropic-messages",
}

_SAFE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/+-]*$")


def _personal_config(model, base_url, api_type, api_key):
    """The personal provider config for one injected run.

    `defaultModelSelection` sits inside `config` on purpose: the CLI reads
    that key from the personal *config* object, and a copy at the document
    root is dropped without a warning.
    """
    return {
        "schemaVersion": 1,
        "config": {
            "providerConfigRules": {
                "providerRules": [{
                    "providerId": PROVIDER_ID,
                    "providerName": "Panda",
                    "config": {
                        "group": "standard-personal",
                        "access": {"type": "api-key", "apiKey": api_key},
                        "api": {
                            "type": API_TYPES.get(api_type or "anthropic",
                                                  "anthropic-messages"),
                            "baseUrl": base_url,
                        },
                        "personalModelIds": [model],
                        "modelOrder": [model],
                    },
                }],
            },
            "modelConfigRules": {
                "providerModelRules": [],
                "manualProviderModelRules": [],
            },
            "defaultModelSelection": {"providerId": PROVIDER_ID, "modelId": model},
        },
    }


def _injected_config_path(model, base_url, api_type, api_key):
    """Write the personal config to a temp file; return its path or ""."""
    model = (model or "").strip()
    base_url = (base_url or "").strip()
    if not model or not base_url or not _SAFE_ID.match(model):
        return ""
    try:
        root = tempfile.mkdtemp(prefix="panda-zcode-")
    except OSError:
        return ""
    path = os.path.join(root, "provider_config.json")
    try:
        # 0600 on POSIX: the file carries the API key in cleartext. Windows has
        # no mode bits; the directory is user-private by default.
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump(_personal_config(model, base_url, api_type, api_key), fh,
                      ensure_ascii=False)
    except OSError:
        shutil.rmtree(root, ignore_errors=True)
        return ""
    harness.at_cleanup(shutil.rmtree, root, True)
    return path


def _reduce(raw):
    """Reduce zcode's `--json` envelope; None when it is not one."""
    start = raw.find("{")
    if start < 0:
        return None
    try:
        doc = json.loads(raw[start:])
    except ValueError:
        return None
    if not isinstance(doc, dict) or "response" not in doc:
        return None
    usage = doc.get("usage") if isinstance(doc.get("usage"), dict) else {}
    mapped = {}
    for src, dst in (("inputTokens", "input_tokens"),
                     ("outputTokens", "output_tokens"),
                     ("cacheReadTokens", "cache_read_tokens"),
                     ("cacheWriteTokens", "cache_write_tokens")):
        if isinstance(usage.get(src), int):
            mapped[dst] = usage[src]
    total = usage.get("totalTokens") if isinstance(usage.get("totalTokens"), int) else None
    return {
        "text": doc.get("response") or "",
        "usage": mapped,
        "tokens": total,
        "session_id": doc.get("sessionId") or "",
    }


def main():
    prompt, timeout, cwd = harness.read_request()

    if os.environ.get("OPENPANDA_INJECTED_MODEL") != "1":
        harness.run_simple(["zcode", "--prompt", prompt],
                           cwd=cwd, timeout=timeout)
        return

    model = os.environ.get("ZCODE_MODEL", "")
    cfg = _injected_config_path(model, os.environ.get("ZCODE_BASE_URL", ""),
                                os.environ.get("OPENPANDA_MODEL_API_TYPE", ""),
                                os.environ.get("ZCODE_API_KEY", ""))
    if not cfg:
        # A half-configured injection must not take the agent down with it:
        # fall back to whatever model the operator already selected.
        harness.progress("injection carried no usable endpoint; using the agent's own model")
        harness.run_simple(["zcode", "--prompt", prompt],
                           cwd=cwd, timeout=timeout)
        return

    os.environ[CONFIG_ENV] = cfg
    cmd = ["zcode", "--prompt", prompt, "--json", "--no-color"]
    harness.progress("using injected model %s via %s" % (model, CONFIG_ENV))
    label = "zcode"
    harness.emit_event("tool_use", id=label, name=label,
                       input={"argc": max(0, len(cmd) - 1)})
    try:
        rc, out, err = harness.run_plain(cmd, cwd=cwd, timeout=timeout)
    except subprocess.TimeoutExpired:
        harness.emit_event("tool_result", tool_use_id=label, is_error=True,
                           content="zcode timed out")
        harness.emit(False, "zcode timed out", 124)
        return
    except FileNotFoundError:
        harness.emit_event("tool_result", tool_use_id=label, is_error=True,
                           content="zcode binary not found")
        harness.emit(False, "zcode binary not found", 127)
        return

    doc = _reduce(out) if out else None
    if doc is None:
        # No envelope: report what the CLI actually said, like the plain path.
        text = out.strip() or err.strip() or "(no output)"
        harness.emit_event("tool_result", tool_use_id=label,
                           is_error=rc != 0, content=text)
        harness.emit(rc == 0, text, rc)
        return
    text = doc["text"] or err.strip() or "(no output)"
    harness.emit_event("tool_result", tool_use_id=label,
                       is_error=rc != 0, content=text)
    harness.emit(rc == 0, text, rc,
                 tokens=doc["tokens"], usage=doc["usage"] or None,
                 session_id=doc["session_id"] or None)


if __name__ == "__main__":
    main()
