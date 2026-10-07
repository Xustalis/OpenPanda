#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Adapter: generic command template → PANDA Commander.

Protocol (shared with claude_code.py / codex.py / opencode.py / pi.py):
  stdin:  a JSON object with keys {prompt, timeout_s, cwd} (cwd optional),
          plus optional {resume, tools_policy, max_turns, cmd, task_id,
          effort, system_prompt}
  stdout: a JSON object with keys {ok, result, exit_code, tokens, cost}

This adapter exists so a capability card can wire ANY headless CLI — and any
driver or platform tool a node can exec — without a bespoke script. The card
declares the invocation once:

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

  {prompt}        the task prompt as ONE argv element (the embedded form
                  "--prompt={prompt}" keeps it inside one element); when the
                  template has no {prompt} the prompt is appended as the last
                  argument — unless {stdin} claimed it
  {stdin}         drops this element and pipes the prompt to the child's stdin
                  instead — for CLIs whose headless contract reads stdin, and
                  for prompts too large for the OS argv limit (Linux bounds one
                  element at 128 KiB, Windows the whole line at ~32 KiB; injected
                  memory/skill context can push a prompt past either)
  {cwd}           the task's working directory
  {resume}        the session id a previous run returned, for CLIs with a resume
                  flag; with nothing to resume the element drops out — and a
                  bare "{resume}" takes a preceding "--flag" with it, so both
                  "--session={resume}" and "--session {resume}" stay valid
  {max_turns}     the task spec's turn cap; drops the same way when unset
  {task_id}       the OpenPanda task id the run belongs to (correlation for
                  CLIs/drivers that tag their own output); drops when unset
  {timeout_s}     the advertised run budget in seconds, for CLIs with their
                  own --timeout/--deadline flag; drops when unset
  {effort}        the task's reasoning-effort hint verbatim, for CLIs that
                  take --effort/--reasoning; drops when unset
  {system_prompt} the adapter-level system rider (the static protocol text
                  the Go side appends); drops when unset — CLIs with an
                  append-system-prompt flag can take it as "--flag {system_prompt}"
  {env:NAME}      the value of environment variable NAME (e.g.
                  "--api-key {env:MYTOOL_API_KEY}"), resolved at expansion
                  time. The commander forwards the names the template
                  references into the adapter's otherwise-sandboxed
                  environment — and only those names — so a card-declared
                  credential actually reaches the expansion (the native
                  "generic" adapter resolves the same names straight from
                  the daemon env). An unset variable drops the whole
                  element — and a bare "{env:NAME}" takes a preceding
                  "--flag" with it — so a credential flag never goes out
                  half-populated. The value is substituted literally into
                  argv: it can never smuggle extra arguments.

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
never prints secrets; {env:NAME} values reach the child process verbatim and
are never echoed.

The same template contract is implemented natively by the Go runtime when a
card declares adapter: "generic" (no .py): identical placeholder expansion and
exit-code mapping, no Python interpreter required — the portable path for
nodes where adapters/ scripts or a Python runtime cannot be assumed.

The wire contract, watchdog timeout, process-tree cleanup and stderr
diagnostics live in _harness.py; this file is only the template expansion.
"""
import os
import re
import shlex

import _harness as harness

PROMPT_PLACEHOLDER = "{prompt}"
STDIN_PLACEHOLDER = "{stdin}"

# {env:NAME} — a placeholder the card wires to a process env var (credentials,
# endpoints, serial ports the card cannot hardcode). NAME is a bare env var
# name; anything else is not a placeholder and passes through literally.
ENV_PLACEHOLDER = re.compile(r"\{env:([A-Za-z_][A-Za-z0-9_]*)\}")

# Per-element argv ceiling below the OS hard limits: Linux bounds one element
# at MAX_ARG_STRLEN (128 KiB) and Windows the whole command line near 32 KiB.
# A prompt over the line fails at execve — the warning below points at {stdin}
# before the spawn error surfaces.
_ARGV_ARG_LIMIT = 32_000 if os.name == "nt" else 128 * 1024


def expand(template, prompt, cwd="", resume="", max_turns=0, task_id="",
           effort="", system_prompt="", timeout_s=0):
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
        # it unset), so it never drops its element; the others drop.
        "{cwd}": cwd or os.getcwd(),
        "{resume}": resume,
        "{max_turns}": str(max_turns) if max_turns > 0 else "",
        "{task_id}": task_id,
        "{effort}": effort,
        "{system_prompt}": system_prompt,
        "{timeout_s}": str(timeout_s) if timeout_s > 0 else "",
    }
    out = []
    seen_prompt = use_stdin = False
    for arg in argv:
        if arg == STDIN_PLACEHOLDER:
            use_stdin = True
            continue
        expanded_arg, missing_env, unset, saw_prompt = _substitute_arg(
            arg, optional, prompt)
        if missing_env or unset is not None:
            # An optional placeholder with no value drops its whole element.
            # The bare two-token form ("--session {resume}", "--key {env:X}")
            # also drops the flag that only introduced it — anything but that
            # and a dangling flag would eat the next element as its value.
            bare = arg == unset or ENV_PLACEHOLDER.fullmatch(arg)
            if bare and out and out[-1].startswith("-") and "=" not in out[-1]:
                out.pop()
            continue
        seen_prompt = seen_prompt or saw_prompt
        out.append(expanded_arg)
    if not out:
        return None, use_stdin
    if not seen_prompt and not use_stdin:
        out.append(prompt)
    return out, use_stdin


def _substitute_arg(arg, optional, prompt):
    """Expand one template element in a single left-to-right pass.

    Every "{...}" span is resolved at most once, so substituted values —
    above all the prompt text — are never rescanned for further
    placeholders. (The old form expanded optional placeholders after
    {prompt}, so a prompt containing a literal "{cwd}" got silently
    rewritten.) Returns (expanded, missing_env, unset, saw_prompt):
    missing_env marks an {env:NAME} reference that resolved to "", unset
    holds the first empty-valued optional placeholder encountered, and
    saw_prompt reports {prompt} was consumed. Unresolvable spans pass
    through literally. Mirrors substituteGenericArg in generic.go.
    """
    out = []
    missing_env = False
    unset = None
    saw_prompt = False
    i, n = 0, len(arg)
    while i < n:
        j = arg.find("{", i)
        if j < 0:
            out.append(arg[i:])
            break
        out.append(arg[i:j])
        k = arg.find("}", j)
        if k < 0:
            out.append(arg[j:])
            break
        tok = arg[j:k + 1]
        i = k + 1
        if tok == PROMPT_PLACEHOLDER:
            out.append(prompt)
            saw_prompt = True
        elif tok == STDIN_PLACEHOLDER:
            # Only the whole-element form pipes the prompt; embedded inside
            # a larger element it is literal text.
            out.append(tok)
        else:
            m = ENV_PLACEHOLDER.fullmatch(tok)
            if m:
                v = os.environ.get(m.group(1), "")
                if v:
                    out.append(v)
                else:
                    missing_env = True
                    out.append(tok)
            elif tok in optional:
                if optional[tok]:
                    out.append(optional[tok])
                else:
                    if unset is None:
                        unset = tok
                    out.append(tok)
            else:
                out.append(tok)
    return "".join(out), missing_env, unset, saw_prompt


def main():
    req = harness.read_request()
    argv, use_stdin = expand(
        req.cmd, req.prompt, cwd=req.cwd or "", resume=req.resume,
        max_turns=req.max_turns, task_id=req.task_id, effort=req.effort,
        system_prompt=req.system_prompt, timeout_s=req.timeout)
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
