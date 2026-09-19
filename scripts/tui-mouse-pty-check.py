#!/usr/bin/env python3
"""PTY integration check for TUI mouse ownership.

The regression this guards is invisible to unit tests, because it is about the
bytes the binary writes to the tty: whether it asks the terminal for cell-motion
reporting (which is what kills drag-select / double-click / copy). Drives the
real binary in a pseudo-terminal and asserts:

  1. default (mouseSelect): DECSET 1007 is requested, cell-motion (1002/1003) is
     NOT, an arrow key still scrolls the transcript (that is how a wheel notch
     arrives under alternate scroll), and 1007 is reset on exit.
  2. PANDA_MOUSE=scroll: cell-motion IS requested and a wheel notch still
     scrolls, with alternate scroll left alone.
  3. ctrl+t mid-session: cell-motion IS requested and the switch is announced in
     the transcript.

Usage: PANDA_BIN=/path/to/panda scripts/tui-mouse-pty-check.py

The check brings its own throwaway config, because the assertions are about the
chat prompt: a clean checkout has no config.yaml, so the TUI would stop at the
first-run wizard and there would be no transcript to scroll. Set PANDA_CONFIG to
point at a prepared file instead.

Exit code 0 = every check holds.
"""
import atexit
import fcntl
import os
import pty
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
BIN = os.environ.get("PANDA_BIN") or os.path.join(ROOT, "bin", "panda")
# The window is deliberately short. Two of the checks below read the transcript's
# scroll indicator to prove a wheel notch reached the transcript, and the offset
# is correctly clamped to the content height — so at 40 rows, where the startup
# banner fits with room to spare, there is nothing to scroll and nothing to
# observe. At 12 the banner alone overflows (the ceiling is ~4 lines), so the
# arrow/wheel path has a visible effect. Keep this below the banner height.
ROWS, COLS = 12, 120
SETTLE = 0.5

# CFG is the config every run is started with; main() fills it in.
CFG = None


def make_config():
    """Write a minimal config that lands the TUI in idle chat.

    Two gates stand between the splash screen and the chat prompt: onboarding
    (ui.onboarded) and the model wizard (a config with no model at all). Both
    have to be satisfied or a wheel notch has no transcript to scroll and every
    assertion below would fail for the wrong reason. Everything the run writes —
    the database, the context and memory trees — goes into this private temp
    directory, so the check never touches the developer's real state.
    """
    tmp = tempfile.mkdtemp(prefix="panda-tui-mouse-")
    atexit.register(shutil.rmtree, tmp, True)
    path = os.path.join(tmp, "config.yaml")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(
            "node:\n"
            "  name: pty-mouse-check\n"
            "model:\n"
            "  provider: openai\n"
            "  api_type: openai\n"
            "  base_url: http://127.0.0.1:1/v1\n"
            "  model: pty-mouse-check\n"
            "  no_auth: true\n"
            "storage:\n"
            "  db_path: %s\n"
            "  artifact_path: %s\n"
            "  context_path: %s\n"
            "  memory_path: %s\n"
            "ui:\n"
            "  onboarded: true\n"
            "  terms_accepted: true\n"
            "  locale: en\n"
            % tuple(os.path.join(tmp, n) for n in
                    ("panda.db", "artifacts", "context", "memory"))
        )
    return path


def drain(fd, seconds, sink):
    """Read whatever the program writes for `seconds`, appending to sink."""
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


def run(env_overrides, keys):
    """Cold-start the TUI, send keys, quit, and return everything it printed."""
    master, slave = pty.openpty()
    # openpty hands out a 0x0 window; without a real size the TUI clips its view
    # to zero columns and there is nothing to read.
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    env = dict(os.environ, TERM="xterm-256color", OPENPANDA_LANG="en", NO_COLOR="1")
    env.pop("PANDA_MOUSE", None)
    env.update(env_overrides)

    proc = subprocess.Popen(
        [BIN, "--config", CFG],
        stdin=slave,
        stdout=slave,
        stderr=slave,
        env=env,
        cwd=ROOT,
        close_fds=True,
        start_new_session=True,
    )
    os.close(slave)

    out = []
    drain(master, 3.0, out)  # splash screen
    for key, settle in keys:
        try:
            os.write(master, key)
        except OSError:
            break  # the program already exited
        drain(master, settle, out)

    # Leave cleanly: two Ctrl-C inside the interrupt window quit. The first may
    # land after the program is already gone, which is fine.
    for _ in range(2):
        try:
            os.write(master, b"\x03")
        except OSError:
            break
        drain(master, SETTLE, out)

    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        proc.wait(timeout=5)
    try:
        os.close(master)
    except OSError:
        pass
    return b"".join(out)


def report(title, checks, failures):
    print("== %s ==" % title)
    for name, ok in checks:
        print(("  PASS  " if ok else "  FAIL  ") + name)
        if not ok:
            failures.append(name)


def main():
    global CFG
    if not os.access(BIN, os.X_OK):
        print("tui-mouse-pty-check: %s is not executable (run `make build`)" % BIN)
        return 2
    CFG = os.environ.get("PANDA_CONFIG") or make_config()
    failures = []
    enter = (b"\r", 2.0)  # leave the splash screen

    blob = run({}, [enter, (b"\x1b[A", 1.5)])  # Up == a wheel notch here
    report("default (ui.mouse unset -> select)", [
        ("requests alternate scroll (DECSET 1007h)", b"\x1b[?1007h" in blob),
        ("does NOT capture the mouse (no 1002h)", b"\x1b[?1002h" not in blob),
        ("does NOT capture the mouse (no 1003h)", b"\x1b[?1003h" not in blob),
        ("arrow up still scrolls the transcript", b"browsing history" in blob),
        ("resets alternate scroll on exit (DECRST 1007l)", b"\x1b[?1007l" in blob),
    ], failures)

    blob = run({"PANDA_MOUSE": "scroll"}, [enter, (b"\x1b[<64;10;10M", 1.0)])
    report("PANDA_MOUSE=scroll", [
        ("captures the mouse (1002h present)", b"\x1b[?1002h" in blob),
        ("leaves alternate scroll alone (no 1007h)", b"\x1b[?1007h" not in blob),
        ("a wheel notch still scrolls the transcript", b"browsing history" in blob),
    ], failures)

    blob = run({}, [enter, (b"\x14", 1.5)])  # ctrl+t
    report("ctrl+t", [
        ("captures the mouse (1002h present)", b"\x1b[?1002h" in blob),
        ("announces the switch in the transcript", b"mouse captured by panda" in blob),
    ], failures)

    # ctrl+t twice: out to scroll mode and back again. The flag has to follow
    # both ways. Re-arming is the half that matters, because the way back hands
    # the mouse over — if 1007 were not requested again the wheel would be dead
    # from that moment on, and only on the switch-back path.
    blob = run({}, [enter, (b"\x14", 1.2), (b"\x14", 1.2)])
    report("ctrl+t twice (scroll, then back to select)", [
        ("captures the mouse on the way out (1002h)", b"\x1b[?1002h" in blob),
        ("releases alternate scroll while captured (1007l)", b"\x1b[?1007l" in blob),
        ("hands the mouse back (1002l)", b"\x1b[?1002l" in blob),
        ("re-arms alternate scroll on the way back (a second 1007h)",
         blob.count(b"\x1b[?1007h") >= 2),
    ], failures)

    if failures:
        print("\nFAILED: " + "; ".join(failures))
        return 1
    print("\nall mouse-ownership checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
