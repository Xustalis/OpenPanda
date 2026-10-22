#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: DeepSeek Harness (dsh) CLI → PANDA Commander.

Protocol (shared with codex.py / claude_code.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

Runs the harness headlessly (`dsh --profile headless <prompt>`). The prompt
is passed as the positional task description; stdout is returned verbatim and
a non-zero exit becomes the diagnosis. dsh exposes no documented session or
tool-whitelist surface on this command line, so resume/tools_policy ride the
request but do not change the command line, and tokens/cost stay unset. This
adapter never prints secrets.

Model injection is the one place this adapter is more than an argv bridge:

dsh reads DEEPSEEK_API_KEY and DEEPSEEK_BASE_URL from the environment, but it
has no DEEPSEEK_MODEL equivalent — the model comes from the profile's
`agent-default-model` entry. When PANDA injects a model (the commander sets
DEEPSEEK_MODEL beside the key and endpoint), this adapter writes a one-entry
profile patch pinning that model and passes it as `--patch`. Without it the
run would quietly keep dsh's own default model while panda's announcement
claimed otherwise.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the dsh difference: the
command line.
"""
import os
import tempfile

import _harness as harness

# The profile entry that chooses the model. Its shipped config is
# {provider: deepseek-official, model: deepseek-flash}; a patch entry's config
# REPLACES the whole block, so the provider has to ride along.
MODEL_ENTRY_ID = "agent-default-model"
MODEL_PROVIDER = "deepseek-official"
PROVIDER_ENTRY_ID = "llm-deepseek"
MAX_TOKENS = 131072


def model_patch(model):
    """Write the profile patch pinning dsh's default model; return its path.

    The model name comes from panda's config, so it is emitted as a
    double-quoted YAML scalar with backslashes and quotes escaped: a name
    carrying ':' or '#' must not become a mapping or a comment.
    """
    escaped = model.replace("\\", "\\\\").replace('"', '\\"')
    body = (
        "- id: %s\n"
        "  config:\n"
        "    provider: %s\n"
        '    model: "%s"\n'
        "- id: %s\n"
        "  config:\n"
        "    maxTokens: %d\n"
        % (MODEL_ENTRY_ID, MODEL_PROVIDER, escaped, PROVIDER_ENTRY_ID, MAX_TOKENS)
    )
    fd, path = tempfile.mkstemp(prefix="panda-dsh-model-", suffix=".yml")
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(body)
    return path


def main():
    prompt, timeout, cwd = harness.read_request()
    argv = ["dsh", "--profile", "headless"]
    patch = ""
    model = (os.environ.get("DEEPSEEK_MODEL") or "").strip()
    if model:
        try:
            patch = model_patch(model)
            argv += ["--patch", patch]
        except OSError as ex:
            # An unwritable temp dir must not fail the task: run on dsh's own
            # default model and say so, instead of reporting a model that was
            # never applied.
            harness.progress("dsh: model patch skipped (%s)" % ex)
            patch = ""
    argv.append(prompt)
    try:
        harness.run_simple(argv, cwd=cwd, timeout=timeout)
    finally:
        if patch:
            try:
                os.unlink(patch)
            except OSError:
                pass


if __name__ == "__main__":
    main()
