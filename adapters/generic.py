#!/usr/bin/env python3
"""Adapter: generic command template → PANDA Commander.

Protocol (shared with claude_code.py / codex.py / opencode.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy, max_turns, cmd}
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
with shlex (posix off on Windows) and every placeholder is replaced by literal
argv text — never string-interpolated into a shell line, so substituted text
cannot break out into shell syntax:

  {prompt}     the task prompt as ONE argv element (the embedded form
               "--prompt={prompt}" keeps it inside one element); when the
               template has no {prompt} the prompt is appended as the last
               argument — unless {stdin} claimed it
  {stdin}      drops this element and pipes the prompt to the child's stdin
               instead — for CLIs whose headless contract reads stdin, and
               for prompts too large for the OS argv limit (Linux bounds one
               element at 128 KiB, Windows the whole line at ~32 KiB; injected
               memory/skill context can push a prompt past either)
  {cwd}        the task's working directory
  {resume}     the session id a previous run returned, for CLIs with a resume
               flag; with nothing to resume the element drops out — and a
               bare "{resume}" takes a preceding "--flag" with it, so both
               "--session={resume}" and "--session {resume}" stay valid
  {max_turns}  the task spec's turn cap; drops the same way when unset

The generic contract is intentionally thin: stdout is the result, non-zero
exit (or empty output) is the diagnosis, and timeout/missing-binary/not-
executable map to 124/127/126 like the shell conventions. Streaming progress,
structured usage and a session id returned over the wire are bespoke-adapter
features — a CLI that needs them still wants its own thin script on
_harness.py.

tools_policy is noted but unenforced: the generic layer cannot know which
flags mean "safe tool whitelist" on an arbitrary CLI, so whatever the template
declares is what runs. Declare the narrowest flags the CLI offers (an
auto-approve flag is usually required — a headless run cannot answer an
interactive permission prompt). For the same reason the adapter has no
restricted read-only mode: the scheduler refuses unconsented remote tasks on
generic agents rather than silently running them full-power. This adapter
never prints secrets.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the template expansion.
"""
import os
import shlex

import _harness as harness

PROMPT_PLACEHOLDER = "{prompt}"
STDIN_PLACEHOLDER = "{stdin}"

# Per-element argv ceiling below the OS hard limits: Linux bounds one element
# at MAX_ARG_STRLEN (128 KiB) and Windows the whole command line near 32 KiB.
# A prompt over the line fails at execve — the warning below points at {stdin}
# before the spawn error surfaces.
_ARGV_ARG_LIMIT = 32_000 if os.name == "nt" else 128 * 1024


def expand(template, prompt, cwd="", resume="", max_turns=0):
    """argv-expand a card-declared command template.

    Returns (argv, prompt_via_stdin): argv is the expanded list — None when
    the template is empty, unparseable, or expands to nothing — and
    prompt_via_stdin marks a {stdin} element claiming the prompt for the
    child's stdin instead of argv.
    """
    try:
        argv = shlex.split(template, posix=(os.name != "nt"))
    except ValueError:
        return None, False
    optional = {
        # {cwd} always resolves (the adapter's own cwd when the request left
        # it unset), so it never drops its element; the other two drop.
        "{cwd}": cwd or os.getcwd(),
        "{resume}": resume,
        "{max_turns}": str(max_turns) if max_turns > 0 else "",
    }
    out = []
    seen_prompt = use_stdin = False
    for arg in argv:
        if arg == STDIN_PLACEHOLDER:
            use_stdin = True
            continue
        unset = next((ph for ph, v in optional.items() if not v and ph in arg), None)
        if unset is not None:
            # An optional placeholder with no value drops its whole element.
            # The bare two-token form ("--session {resume}") also drops the
            # flag that only introduced it — anything but that and a dangling
            # flag would eat the next element as its value.
            if arg == unset and out and out[-1].startswith("-") and "=" not in out[-1]:
                out.pop()
            continue
        if arg == PROMPT_PLACEHOLDER:
            out.append(prompt)
            seen_prompt = True
            continue
        if PROMPT_PLACEHOLDER in arg:
            arg = arg.replace(PROMPT_PLACEHOLDER, prompt)
            seen_prompt = True
        for ph, v in optional.items():
            if v and ph in arg:
                arg = arg.replace(ph, v)
        out.append(arg)
    if not out:
        return None, use_stdin
    if not seen_prompt and not use_stdin:
        out.append(prompt)
    return out, use_stdin


def main():
    req = harness.read_request()
    argv, use_stdin = expand(req.cmd, req.prompt, cwd=req.cwd or "",
                             resume=req.resume, max_turns=req.max_turns)
    if argv is None:
        harness.emit(False,
                     "generic adapter: card agent.command template is empty, unparseable, or expands to nothing",
                     2)
        return
    label = argv[0]
    if not use_stdin:
        # Warn early when the argv-bound prompt nears the OS limit: past it
        # the spawn fails at execve, which reads as an opaque spawn error
        # instead of the sizing problem it is.
        size = len(req.prompt.encode("utf-8", "replace"))
        if size >= _ARGV_ARG_LIMIT:
            harness.progress(
                f"{label}: prompt is {size} bytes — past the OS argv limit; "
                "a {stdin} element in the card's command template pipes it instead")
    harness.run_simple(argv, cwd=req.cwd, timeout=req.timeout,
                       label=label, input=req.prompt if use_stdin else None)


if __name__ == "__main__":
    main()
