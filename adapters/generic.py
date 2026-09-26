#!/usr/bin/env python3
"""Adapter: generic command template → PANDA Commander.

Protocol (shared with claude_code.py / codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy, cmd}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

This adapter exists so a capability card can wire ANY headless CLI without a
bespoke script. The card declares the invocation once:

    agents:
      zcode:
        adapter: "generic.py"
        install_check: "zcode --version"
        command: "zcode --prompt {prompt}"
        capabilities: ["coding", "shell", "file_edit"]

The command template rides the request's "cmd" field verbatim. It is split
with shlex (posix off on Windows) and every "{prompt}" placeholder is replaced
by the task prompt as ONE literal argv element — never string-interpolated
into a shell line, so prompt content cannot break out into shell syntax. When
the template has no placeholder, the prompt is appended as the last argument.

The generic contract is intentionally thin: stdout is the result, non-zero
exit (or empty output) is the diagnosis, and timeout/missing-binary map to
124/127 like the other plain adapters. Streaming progress, session resume and
structured usage are bespoke-adapter features — a CLI that needs them still
wants its own thin script on _harness.py.

tools_policy is noted but unenforced: the generic layer cannot know which
flags mean "safe tool whitelist" on an arbitrary CLI, so whatever the template
declares is what runs. Declare the narrowest flags the CLI offers (e.g. an
auto-approve flag is usually required — a headless run cannot answer an
interactive permission prompt). This adapter never prints secrets.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the template expansion.
"""
import os
import shlex

import _harness as harness

PROMPT_PLACEHOLDER = "{prompt}"


def expand(template, prompt):
    """argv-expand a card-declared command template.

    Returns the argv list, or None when the template is unusable (empty or
    unparseable). The prompt is substituted as a literal argv element —
    quoting in the template belongs to the operator's flags, not to the
    prompt, so "{prompt}" and '{prompt}' both match after shlex.
    """
    try:
        argv = shlex.split(template, posix=(os.name != "nt"))
    except ValueError:
        return None
    if not argv:
        return None
    seen = False
    out = []
    for arg in argv:
        if arg == PROMPT_PLACEHOLDER:
            out.append(prompt)
            seen = True
        elif PROMPT_PLACEHOLDER in arg:
            # Embedded form ("--prompt={prompt}"): the placeholder sits inside
            # one argv element, so the prompt still lands as literal text.
            out.append(arg.replace(PROMPT_PLACEHOLDER, prompt))
            seen = True
        else:
            out.append(arg)
    if not seen:
        out.append(prompt)
    return out


def main():
    req = harness.read_request()
    argv = expand(req.cmd, req.prompt)
    if argv is None:
        harness.emit(False,
                     "generic adapter: card agent.command template is empty or unparseable",
                     2)
        return
    harness.run_simple(argv, cwd=req.cwd, timeout=req.timeout, label=argv[0])


if __name__ == "__main__":
    main()
