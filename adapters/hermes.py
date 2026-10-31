#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: Hermes Agent CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs `hermes --cli --yolo -z <prompt>` headlessly (CLI output mode, one-shot
prompt, auto-approved tools). stdout is returned verbatim and a non-zero exit
becomes the diagnosis. --yolo rides both tool policies: a headless run cannot
sit on an interactive permission prompt, and hermes exposes no narrower CLI
tool whitelist, so resume/tools_policy ride the request but do not change the
command line. Hooks are NOT auto-approved by default: a hook is shell the
workspace or the agent itself can register, so --accept-hooks (which lifts
that approval) rides only under OPENPANDA_ACCEPT_HOOKS=1 — an explicit
operator choice, not a default. This adapter never prints secrets.

Injected runs (OPENPANDA_INJECTED_MODEL=1) need one step no env var can do.
hermes 0.19 resolves its model from `providers.<name>` + `model.*` in
$HERMES_HOME/config.yaml and ignores OPENAI_BASE_URL/OPENAI_API_KEY outright
(verified: the run dies with "HTTP 401: Missing Authentication header" no
matter which provider env is set). So the adapter materialises a throwaway
HERMES_HOME whose `panda` provider points at the injected endpoint through
`${ENV}` references — the values come from the child's environment, so the
secret never lands on disk, and the operator's own hermes home (skills,
sessions, hooks) is never touched.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the hermes difference: the
command line, the heartbeat cadence of its silent --cli mode, and that config
shim.
"""
import os
import re
import shutil
import tempfile

import _harness as harness

# The provider id the injected endpoint rides under. Cosmetic — it only has to
# avoid colliding with a provider the operator configured.
PROVIDER_ID = "panda"

# Config the shim writes. `${OPENPANDA_INJECT_BASE_URL}` is a hermes config
# env-ref, expanded when the config is read; `key_env` names a variable the
# CLI resolves itself. Neither is a secret, so this file is safe to leave in a
# temp dir for the life of the run.
CONFIG_TEMPLATE = """\
# Written by PANDA for one injected run; the directory is removed afterwards.
model:
  provider: {provider}
  model: {model}
providers:
  {provider}:
    api: ${{OPENPANDA_INJECT_BASE_URL}}
    key_env: OPENPANDA_INJECT_API_KEY
    default_model: {model}
"""

# Model and provider ids go into YAML unquoted; anything outside this shape is
# refused rather than quoted-and-hoped (a stray colon would turn the value
# into a mapping, and hermes would then report a config error instead of a
# model error).
_SAFE_SCALAR = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/+@-]*$")


def _injected_home(model):
    """Materialise a throwaway HERMES_HOME for an injected run.

    Returns the directory, or "" when the injection carried no endpoint or no
    usable model name. An empty return means the caller runs hermes exactly as
    before, so a half-configured injection degrades to the agent's own model
    instead of failing on a config we could not write.
    """
    base_url = (os.environ.get("OPENAI_BASE_URL") or "").strip()
    model = (model or "").strip()
    if not base_url or not model:
        return ""
    if not _SAFE_SCALAR.match(model) or not _SAFE_SCALAR.match(PROVIDER_ID):
        return ""
    # Endpoint and key ride the child's environment; the config only
    # references them, so an injected run leaves no credential behind.
    os.environ["OPENPANDA_INJECT_BASE_URL"] = base_url
    os.environ["OPENPANDA_INJECT_API_KEY"] = os.environ.get("OPENAI_API_KEY", "")
    try:
        home = tempfile.mkdtemp(prefix="panda-hermes-home-")
    except OSError:
        return ""
    try:
        with open(os.path.join(home, "config.yaml"), "w", encoding="utf-8") as fh:
            fh.write(CONFIG_TEMPLATE.format(provider=PROVIDER_ID, model=model))
    except OSError:
        shutil.rmtree(home, ignore_errors=True)
        return ""
    os.environ["HERMES_HOME"] = home
    harness.at_cleanup(shutil.rmtree, home, True)
    return home


# How often a silent run says it is still alive. --cli prints nothing until
# the run finishes, so without this the Go silence watchdog sees a dead pipe
# for the whole run and kills a healthy long task at the default 4-minute
# silence limit. The heartbeat does not shorten a wedged run: the hard
# agent budget still bounds it.
HEARTBEAT_S = 20


def main():
    prompt, timeout, cwd = harness.read_request()

    if os.environ.get("OPENPANDA_INJECTED_MODEL") == "1":
        model = os.environ.get("OPENAI_MODEL", "")
        if _injected_home(model):
            harness.progress("using injected model %s via a temporary HERMES_HOME" % model)

    # --cli: text output mode; --yolo: auto-approve tool calls; -z: one-shot
    # prompt so the agent runs to completion without an interactive session.
    cmd = ["hermes", "--cli", "--yolo", "-z", prompt]
    # Opt-in hook auto-approval, kept ahead of the prompt so the flag can
    # never be parsed as part of it.
    if os.environ.get("OPENPANDA_ACCEPT_HOOKS"):
        cmd = ["hermes", "--cli", "--yolo", "--accept-hooks", "-z", prompt]
    harness.run_simple(cmd, cwd=cwd, timeout=timeout, heartbeat_s=HEARTBEAT_S)


if __name__ == "__main__":
    main()
