#!/usr/bin/env python3
"""PTY integration check for the REPL task watcher.

Drives the real REPL in a pseudo-terminal, simulates the user typing a
half-finished line, flips a task to done behind its back (as the daemon
would), and asserts:
  1. the watcher's completion line appears while the prompt is up,
  2. the user's in-progress buffer survives the interleave.
Exit code 0 = both hold.
"""
import os
import pty
import select
import sqlite3
import subprocess
import sys
import time

DB = "testdata/live/mac/openpanda.db"
TASK = "watchtest-0001"

def db_exec(sql, args=()):
    con = sqlite3.connect(DB, timeout=10)
    con.execute(sql, args)
    con.commit()
    con.close()

def read_all(master, timeout=0.5):
    out = b""
    end = time.time() + timeout
    while time.time() < end:
        r, _, _ = select.select([master], [], [], 0.1)
        if r:
            try:
                chunk = os.read(master, 65536)
            except OSError:
                break
            if not chunk:
                break
            out += chunk
    return out.decode("utf-8", errors="replace")

def main():
    db_exec("DELETE FROM tasks WHERE task_id=?", (TASK,))
    db_exec("DELETE FROM task_events WHERE task_id=?", (TASK,))

    master, slave = pty.openpty()
    env = dict(os.environ)
    env.pop("OPENPANDA_MODEL_API_KEY", None)   # engine optional; watcher is the subject
    env["TERM"] = "xterm-256color"
    p = subprocess.Popen(
        ["./bin/panda", "repl", "--config", "testdata/live/mac-config.yaml"],
        stdin=slave, stdout=slave, stderr=slave, env=env, cwd=".",
    )
    os.close(slave)
    all_out = []
    try:
        all_out.append(read_all(master, 6))     # banner + first prompt + baseline

        os.write(master, b"half typed line")    # user mid-edit, no Enter yet
        time.sleep(1)
        all_out.append(read_all(master, 0.5))

        now = int(time.time())
        # Clone an existing well-formed row so every column the Go scanner
        # reads is present; then flip the probe fields.
        con = sqlite3.connect(DB, timeout=10)
        cols = [r[1] for r in con.execute("PRAGMA table_info(tasks)")]
        src = con.execute(
            "SELECT %s FROM tasks ORDER BY created_at DESC LIMIT 1" % ",".join(cols)).fetchone()
        if src is None:
            print("no source task row to clone")
            sys.exit(2)
        placeholders = []
        vals = []
        for c, v in zip(cols, src):
            if c == "task_id":
                placeholders.append("?"); vals.append(TASK)
            elif c == "title":
                placeholders.append("?"); vals.append("watcher probe task")
            elif c == "state":
                placeholders.append("?"); vals.append("running")
            elif c == "updated_at":
                placeholders.append("?"); vals.append(now)
            else:
                placeholders.append("?"); vals.append(v)
        con.execute("INSERT INTO tasks (%s) VALUES (%s)" % (",".join(cols), ",".join(placeholders)), vals)
        con.commit()
        con.close()
        time.sleep(3)                           # watcher sees running (no notify)
        all_out.append(read_all(master, 0.5))

        db_exec("UPDATE tasks SET state='done', updated_at=? WHERE task_id=?",
                (int(time.time()), TASK))
        time.sleep(4.5)                         # watcher sees done -> notify interleaves
        tail = read_all(master, 1.0)

        ok_notify = ("watcher probe task" in tail and "done" in tail and TASK[:9] in tail)
        ok_buffer = "half typed line" in tail   # redraw keeps the buffer
        print("notify_line:", ok_notify, "| buffer_kept:", ok_buffer)
        if not (ok_notify and ok_buffer):
            full = "".join(all_out)
            print("--- full session (last 2000) ---")
            print(repr(full[-2000:]))
        sys.exit(0 if (ok_notify and ok_buffer) else 1)
    finally:
        p.terminate()
        try:
            p.wait(timeout=3)
        except subprocess.TimeoutExpired:
            p.kill()
        db_exec("DELETE FROM tasks WHERE task_id=?", (TASK,))
        db_exec("DELETE FROM task_events WHERE task_id=?", (TASK,))

if __name__ == "__main__":
    main()
