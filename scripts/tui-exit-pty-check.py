#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""PTY integration check for TUI exit visibility and failed-turn persistence.

Two regressions this guards, both invisible to unit tests:

  1. A failed bare-mode turn must persist its user+error pair into the convo
     file (tui_turn.go onDone captures pendingPrompt before resetLive wipes
     it — the order was once reversed and the pair silently vanished, leaving
     /history and the next launch with no trace of the attempt).
  2. Exiting the alternate screen must not leave the user guessing where the
     conversation went: a bare chat prints a "saved / reloads next time"
     note, a bound session prints its id with the /resume hint — and the
     terminal is left clean (1049l restore, kitty flags popped, 1007 off).

The model endpoint is deliberately unreachable (127.0.0.1:1, retries off), so
every submitted turn fails fast and lands in the error path — exactly the
turn whose persistence this checks. All state (db, convo, history) goes into
a throwaway temp dir via --config and XDG_STATE_HOME; the developer's real
CLI state is never touched.

Usage: PANDA_BIN=/path/to/panda scripts/tui-exit-pty-check.py
Exit code 0 = every check holds.
"""
import atexit
import fcntl
import os
import pty
import re
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
# Absolute: the child runs with cwd=tmp, so a relative PANDA_BIN would not resolve.
BIN = os.path.abspath(os.environ.get("PANDA_BIN") or os.path.join(ROOT, "bin", "panda"))
ROWS, COLS = 24, 100
ANSI_RE = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]")


def make_workspace():
    """A private config + XDG state dir; everything the run writes dies here."""
    tmp = tempfile.mkdtemp(prefix="panda-tui-exit-")
    atexit.register(shutil.rmtree, tmp, True)
    cfg = os.path.join(tmp, "config.yaml")
    with open(cfg, "w", encoding="utf-8") as fh:
        fh.write(
            "node:\n"
            "  name: tui-exit-check\n"
            "model:\n"
            "  provider: openai\n"
            "  api_type: openai\n"
            "  base_url: http://127.0.0.1:1/v1\n"
            "  model: tui-exit-check\n"
            "  no_auth: true\n"
            # Negative disables retries: the unreachable endpoint fails the
            # turn immediately instead of stretching the check over backoff.
            "  max_retries: -1\n"
            "storage:\n"
            "  db_path: %s\n"
            "  artifact_path: %s\n"
            "  context_path: %s\n"
            "  memory_path: %s\n"
            "ui:\n"
            "  onboarded: true\n"
            "  terms_accepted: true\n"
            # Without a version the fixture looks like a MIT-era install and
            # the reconsent card swallows the keys below. Keep in step with
            # internal/config.TermsVersionCurrent.
            "  terms_version: 2\n"
            "  locale: en\n"
            % tuple(os.path.join(tmp, n) for n in
                    ("panda.db", "artifacts", "context", "memory"))
        )
    return tmp, cfg


def drain(fd, seconds, sink):
    end = time.time() + seconds
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.2)
        if not r:
            continue
        try:
            chunk = os.read(fd, 65536)
        except OSError:
            return
        if not chunk:
            return
        sink.append(chunk)


def send(fd, data, sink, settle):
    try:
        os.write(fd, data)
    except OSError:
        return
    drain(fd, settle, sink)


def run(cfg, tmp):
    """Drive one TUI session: splash -> submit a failing turn -> quit."""
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
    env = dict(os.environ, TERM="xterm-256color", OPENPANDA_LANG="en",
               XDG_STATE_HOME=tmp)
    env.pop("PANDA_MOUSE", None)
    proc = subprocess.Popen(
        [BIN, "--config", cfg],
        stdin=slave, stdout=slave, stderr=slave,
        env=env, cwd=tmp, close_fds=True, start_new_session=True,
    )
    os.close(slave)
    out = []
    drain(master, 2.5, out)                       # splash
    send(master, b"\r", out, 1.5)                 # enter chat
    send(master, b"ping turn\r", out, 0.3)        # submit; endpoint is dead
    # Wait for the turn to finish for real — quitting mid-turn races doneMsg
    # and would test the interrupt path, not the completed-failure path.
    deadline = time.time() + 30
    while time.time() < deadline:
        drain(master, 0.5, out)
        probe = ANSI_RE.sub(b"", b"".join(out[-3:]))
        if b"Running" not in probe and len(out) > 3:
            break
    drain(master, 2.0, out)                       # let the final frame settle
    for _ in range(2):                            # double ctrl-c quits
        send(master, b"\x03", out, 0.8)
    try:
        rc = proc.wait(timeout=8)
    except subprocess.TimeoutExpired:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        rc = -1
    try:
        os.close(master)
    except OSError:
        pass
    return b"".join(out), rc


def convo_files(tmp):
    d = os.path.join(tmp, "openpanda", "conversations")
    if not os.path.isdir(d):
        return []
    return [os.path.join(d, n) for n in os.listdir(d) if n.endswith(".json")]


def report(title, checks, failures):
    print("== %s ==" % title)
    for name, ok in checks:
        print(("  PASS  " if ok else "  FAIL  ") + name)
        if not ok:
            failures.append(name)


def main():
    if not os.access(BIN, os.X_OK):
        print("tui-exit-pty-check: %s is not executable (run `make build`)" % BIN)
        return 2
    tmp, cfg = make_workspace()
    failures = []

    blob, rc = run(cfg, tmp)
    text = ANSI_RE.sub(b"", blob).decode("utf-8", "replace")

    files = convo_files(tmp)
    convo = ""
    for f in files:
        with open(f, encoding="utf-8") as fh:
            convo += fh.read()

    report("exit visibility and failed-turn persistence", [
        ("TUI entered the alternate screen (1049h)", b"\x1b[?1049h" in blob),
        ("alt screen restored on exit (1049l)", b"\x1b[?1049l" in blob),
        ("failed turn persisted the user prompt into convo", "ping turn" in convo),
        ("failed turn persisted the marked error side", "\\u26a0" in convo or "⚠" in convo),
        ("model error rendered in the configured locale (en)", "cannot reach the model service" in text),
        ("exit note printed on the normal screen ('chat saved')", "chat saved" in text),
        ("kitty keyboard flags popped on exit", b"\x1b[<u" in blob),
        ("process exited cleanly (rc=%s)" % rc, rc == 0),
    ], failures)

    if failures:
        print("\nFAILED: " + "; ".join(failures))
        return 1
    print("\nall exit-visibility checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
