#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""PTY integration check for the side channel (/side).

The side channel is a second composer beside the main thread. Three facts about
it are only observable in a real terminal, because they are about frames rather
than values:

  1. /side replaces the frame with the channel, and Esc gives the main frame
     back — the channel must not linger over the thread it was opened from.
  2. Leaving and returning lands in the SAME channel: the question asked
     before Esc is still there, unanswered or answered. A channel that resets
     on return would be a one-shot prompt wearing a thread's name.
  3. /side close inside the channel discards it for good.

The model endpoint is deliberately unreachable (127.0.0.1:1, retries off), so a
side question fails fast — this check never depends on a real model, and the
question block it asserts on is committed before the ask starts.

All state (db, convo, history) goes into a throwaway temp dir via --config and
XDG_STATE_HOME; the developer's real CLI state is never touched.

Usage: PANDA_BIN=/path/to/panda scripts/tui-side-pty-check.py
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
BIN = os.path.abspath(os.environ.get("PANDA_BIN") or os.path.join(ROOT, "bin", "panda"))
ROWS, COLS = 30, 110
ANSI_RE = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]")

# Frame markers in English: the channel's title bar, and the main footer's
# mouse-mode hint — a string the channel's own footer does not carry.
SIDE_MARK = "SIDE CHANNEL"
MAIN_MARK = "ctrl+t scroll mode"


def terms_version_current():
    src = os.path.join(ROOT, "internal", "config", "config.go")
    try:
        with open(src, encoding="utf-8") as fh:
            text = fh.read()
    except OSError:
        return None
    m = re.search(r"TermsVersionCurrent\s*=\s*(\d+)", text)
    return int(m.group(1)) if m else None


def make_workspace():
    tmp = tempfile.mkdtemp(prefix="panda-tui-side-")
    atexit.register(shutil.rmtree, tmp, True)
    version = terms_version_current()
    if version is None:
        print("tui-side-pty-check: cannot read config.TermsVersionCurrent")
        return None, None
    cfg = os.path.join(tmp, "config.yaml")
    with open(cfg, "w", encoding="utf-8") as fh:
        fh.write(
            "node:\n"
            "  name: tui-side-check\n"
            "model:\n"
            "  provider: openai\n"
            "  api_type: openai\n"
            "  base_url: http://127.0.0.1:1/v1\n"
            "  model: tui-side-check\n"
            "  no_auth: true\n"
            # Negative disables retries: the unreachable endpoint fails a side
            # ask immediately instead of stretching the check over backoff.
            "  max_retries: -1\n"
            "storage:\n"
            "  db_path: %s\n"
            "  artifact_path: %s\n"
            "  context_path: %s\n"
            "  memory_path: %s\n"
            "ui:\n"
            "  onboarded: true\n"
            "  terms_accepted: true\n"
            "  terms_version: %s\n"
            "  locale: en\n"
            % tuple([os.path.join(tmp, n) for n in
                     ("panda.db", "artifacts", "context", "memory")] + [version])
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


def frame(sink, start):
    """The text written since `start`, with terminal control sequences gone.

    Bubble Tea repaints the alternate screen, so the bytes produced by one
    action are the frame that action caused. Searching the delta — rather than
    the whole capture — is what lets "the channel is gone" be asserted: the
    marker it was opened with is still in the earlier frames.
    """
    return ANSI_RE.sub(b"", b"".join(sink[start:])).decode("utf-8", "replace")


def run(cfg, tmp):
    """Drive one TUI session through open → return → reopen → close → quit."""
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
    steps = {}

    drain(master, 2.5, out)                       # splash
    send(master, b"\r", out, 1.5)                 # enter chat

    # 1. Open the channel.
    start = len(out)
    send(master, b"/side\r", out, 1.0)
    steps["open"] = frame(out, start)

    # 2. Ask one question, then Esc straight back. The question block commits
    #    before the ask starts, so it is there whether the dead model answers
    #    or not.
    send(master, b"what does this function do\r", out, 2.0)
    start = len(out)
    send(master, b"\x1b", out, 1.0)               # Esc: return, not close
    steps["return"] = frame(out, start)

    # 3. Reopen: the same channel, question intact.
    start = len(out)
    send(master, b"/side\r", out, 1.2)
    steps["reopen"] = frame(out, start)

    # 4. Close for good.
    start = len(out)
    send(master, b"/side close\r", out, 1.0)
    steps["close"] = frame(out, start)

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
    return steps, b"".join(out), rc


def report(title, checks, failures):
    print("== %s ==" % title)
    for name, ok in checks:
        print(("  PASS  " if ok else "  FAIL  ") + name)
        if not ok:
            failures.append(name)


def main():
    if not os.access(BIN, os.X_OK):
        print("tui-side-pty-check: %s is not executable (run `make build`)" % BIN)
        return 2
    tmp, cfg = make_workspace()
    if not cfg:
        return 2
    failures = []

    steps, blob, rc = run(cfg, tmp)
    open_, back, reopen, close = (steps.get(k, "") for k in ("open", "return", "reopen", "close"))

    report("side channel open / return / reopen / close", [
        ("/side opens the channel frame", SIDE_MARK in open_),
        ("Esc returns to the main frame", SIDE_MARK not in back and MAIN_MARK in back),
        ("reopening lands in the same channel", SIDE_MARK in reopen),
        ("the question asked before Esc is still there", "what does this function do" in reopen),
        ("/side close discards the channel", SIDE_MARK not in close and MAIN_MARK in close),
        ("TUI restored the normal screen on exit (1049l)", b"\x1b[?1049l" in blob),
        ("process exited cleanly (rc=%s)" % rc, rc == 0),
    ], failures)

    if failures:
        print("\nFAILED: " + "; ".join(failures))
        for name in ("open", "return", "reopen", "close"):
            print("\n--- %s frame ---\n%s" % (name, steps.get(name, "")[-1500:]))
        return 1
    print("\nall side-channel checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
