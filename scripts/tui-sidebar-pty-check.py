#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""PTY integration check for the task rail (Ctrl+B).

The rail is a layout feature: it takes columns from the transcript and paints a
second column beside it. Whether that splicing lands correctly — and whether
toggling it back actually gives the columns back — is only observable in a real
terminal, on real frames.

The fixture is a database with two unfinished tasks; the check opens the rail,
asserts the rows are there, closes it, and asserts the column is gone.

Usage: PANDA_BIN=/path/to/panda scripts/tui-sidebar-pty-check.py
Exit code 0 = every check holds.
"""
import atexit
import os
import pty
import re
import select
import shutil
import signal
import sqlite3
import struct
import subprocess
import sys
import tempfile
import termios
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.abspath(os.environ.get("PANDA_BIN") or os.path.join(ROOT, "bin", "panda"))
ROWS, COLS = 30, 120
ANSI_RE = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]")

RAIL_MARK = "Tasks"
TASK_MARK = "生成构建报告页面"
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
    tmp = tempfile.mkdtemp(prefix="panda-tui-sidebar-")
    atexit.register(shutil.rmtree, tmp, True)
    version = terms_version_current()
    if version is None:
        print("tui-sidebar-pty-check: cannot read config.TermsVersionCurrent")
        return None, None, None
    db = os.path.join(tmp, "panda.db")
    cfg = os.path.join(tmp, "config.yaml")
    with open(cfg, "w", encoding="utf-8") as fh:
        fh.write(
            "node:\n"
            "  name: tui-sidebar-check\n"
            "model:\n"
            "  provider: openai\n"
            "  api_type: openai\n"
            "  base_url: http://127.0.0.1:1/v1\n"
            "  model: tui-sidebar-check\n"
            "  no_auth: true\n"
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
            % (
                db,
                os.path.join(tmp, "artifacts"),
                os.path.join(tmp, "context"),
                os.path.join(tmp, "memory"),
                version,
            )
        )
    return tmp, cfg, db


def seed_tasks(cfg, tmp):
    """Open the store once so the schema exists, then add two unfinished tasks.

    The daemon creates the schema on first run; doing it by hand would duplicate
    a migration list this check has no business knowing. So the binary is asked
    to initialise by running `panda status` (read-only, and it opens the store).
    """
    env = dict(os.environ, XDG_STATE_HOME=tmp, OPENPANDA_LANG="en")
    subprocess.run(
        [BIN, "--config", cfg, "status"],
        env=env, cwd=tmp, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    db = os.path.join(tmp, "panda.db")
    if not os.path.exists(db):
        return False
    now = int(time.time())
    con = sqlite3.connect(db)
    try:
        for task_id, title, state in [
            ("tui-sidebar-running", "生成构建报告页面", "running"),
            ("tui-sidebar-queued", "跑一遍集成测试", "queued"),
        ]:
            con.execute(
                "INSERT OR REPLACE INTO tasks (task_id, title, state, owner_node,"
                " attempt_id, created_at, updated_at, context_type, complexity, risk)"
                " VALUES (?,?,'%s','fileview-check','att',?,?,'file',0.2,'low')"
                % state,
                (task_id, title, now, now),
            )
        con.commit()
    finally:
        con.close()
    return True


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
    return ANSI_RE.sub(b"", b"".join(sink[start:])).decode("utf-8", "replace")


def run(cfg, tmp):
    master, slave = pty.openpty()
    import fcntl

    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
    env = dict(os.environ, TERM="xterm-256color", OPENPANDA_LANG="en", XDG_STATE_HOME=tmp)
    env.pop("PANDA_MOUSE", None)
    proc = subprocess.Popen(
        [BIN, "--config", cfg],
        stdin=slave, stdout=slave, stderr=slave,
        env=env, cwd=tmp, close_fds=True, start_new_session=True,
    )
    os.close(slave)
    out = []
    steps = {}

    drain(master, 2.5, out)          # splash
    send(master, b"\r", out, 1.5)    # enter chat

    start = len(out)
    send(master, b"\x02", out, 1.2)  # Ctrl+B
    steps["open"] = frame(out, start)

    start = len(out)
    send(master, b"\x02", out, 1.2)  # Ctrl+B again
    steps["close"] = frame(out, start)

    for _ in range(2):
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
        print("tui-sidebar-pty-check: %s is not executable (run `make build`)" % BIN)
        return 2
    tmp, cfg, _ = make_workspace()
    if not cfg:
        return 2
    if not seed_tasks(cfg, tmp):
        print("tui-sidebar-pty-check: could not seed the task store")
        return 2
    failures = []

    steps, blob, rc = run(cfg, tmp)
    opened, closed = steps.get("open", ""), steps.get("close", "")

    report("task rail open / close", [
        ("Ctrl+B opens the rail", RAIL_MARK in opened),
        ("the rail lists the thread's work", TASK_MARK in opened),
        ("Ctrl+B closes it again", RAIL_MARK not in closed),
        ("the transcript gets its columns back", MAIN_MARK in closed),
        ("TUI restored the normal screen on exit (1049l)", b"\x1b[?1049l" in blob),
        ("process exited cleanly (rc=%s)" % rc, rc == 0),
    ], failures)

    if failures:
        print("\nFAILED: " + "; ".join(failures))
        for name in ("open", "close"):
            print("\n--- %s frame ---\n%s" % (name, steps.get(name, "")[-1800:]))
        return 1
    print("\nall task-rail checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
