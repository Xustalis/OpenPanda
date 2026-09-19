#!/usr/bin/env python3
"""Interactive probe: what does THIS terminal actually send for the mouse?

Whether a wheel gesture arrives as an arrow key, as a mouse report, or not at
all is a property of the terminal, not of panda — and it cannot be answered from
a pseudo-terminal, because a PTY has no wheel. Run this in the real terminal you
use for panda and watch the counters.

    python3 scripts/tui-mouse-probe.py

What to do:

  1. two-finger swipe (or roll the wheel)          -> watch wheel / up / down
  2. drag with the left button                     -> watch click, and whether
                                                      the terminal highlights
  3. press m, then repeat 1 and 2                  -> same, with capture ON
  4. press q to quit

How to read it, for the panda default (no capture):

  * up/down rising on a swipe  = the terminal translates the wheel into arrow
    keys inside the alternate screen (DECSET 1007, or an equivalent built-in).
    This is what panda's default mode relies on; scrolling needs no modifier
    and dragging selects text.
  * nothing at all on a swipe  = the terminal leaves the wheel unhandled here.
    panda would look like "scrolling is gone" in its default mode, and the
    capture mode (ctrl+t, wheel arrives as a mouse report) is the way out.
  * wheel rising on a swipe    = the terminal reports the wheel directly even
    without capture, so both modes scroll.

With capture ON (press m) the wheel should always show up as "wheel" — that is
the second thing this probe confirms.
"""
import os
import re
import select
import sys
import termios
import tty

ALT_ON, ALT_OFF = b"\x1b[?1049h", b"\x1b[?1049l"
SCROLL_ON, SCROLL_OFF = b"\x1b[?1007h", b"\x1b[?1007l"
CELL_ON, CELL_OFF = b"\x1b[?1002h\x1b[?1006h", b"\x1b[?1002l\x1b[?1006l"

SGR = re.compile(rb"\x1b\[<(\d+);(\d+);(\d+)([Mm])")
CSI_ARROW = re.compile(rb"\x1b\[([ABCD])")
ARROWS = {b"A": "up", b"B": "down", b"C": "right", b"D": "left"}
WHEEL_BUTTONS = {64, 65}          # SGR codes 64/65 are wheel up/down
KINDS = ("up", "down", "left", "right", "wheel", "click", "other")


def classify(chunk, counts, log):
    """Count the recognisable input sequences in one read, in order."""
    i = 0
    while i < len(chunk):
        if chunk[i : i + 1] == b"\x1b":
            m = SGR.match(chunk, i)
            if m:
                code = int(m.group(1))
                kind = "wheel" if code in WHEEL_BUTTONS else "click"
                counts[kind] += 1
                log.append("%-5s  %s" % (kind, m.group(0).decode("latin1")))
                i = m.end()
                continue
            m = CSI_ARROW.match(chunk, i)
            if m:
                kind = ARROWS[m.group(1)]
                counts[kind] += 1
                log.append("%-5s  %s  (arrow key)" % (kind, m.group(0).decode("latin1")))
                i = m.end()
                continue
            counts["other"] += 1
            log.append("other  %s" % chunk[i : i + 3].hex(" "))
            i += 3
            continue
        ch = chunk[i : i + 1]
        i += 1
        if ch == b"q":
            return True
        if ch == b"m":
            return None  # toggle capture, handled by the caller
        if ch in (b"\r", b"\n"):
            continue
        counts["other"] += 1
        log.append("other  %s" % ch.decode("latin1", "replace"))
    return False


def draw(captured, counts, log):
    lines = [
        "panda mouse probe    capture: %-3s   alternate scroll (1007): ON"
        % ("ON" if captured else "OFF"),
        "  swipe/wheel  up=%(up)d down=%(down)d left=%(left)d right=%(right)d"
        "   wheel-report=%(wheel)d" % counts,
        "  left button  click/drag-report=%(click)d    unparsed=%(other)d" % counts,
        "  m: toggle capture (1002)    q: quit",
        "",
        "recent input from the terminal:",
    ]
    lines += ["    " + e for e in log[-8:]]
    body = "\x1b[H" + "".join(line + "\x1b[K\n" for line in lines) + "\x1b[J"
    os.write(sys.stdout.fileno(), body.encode())


def main():
    if not sys.stdin.isatty():
        print("tui-mouse-probe: needs a real terminal (run it in iTerm2/Terminal.app)")
        return 2

    fd = sys.stdin.fileno()
    saved = termios.tcgetattr(fd)
    captured = False
    counts = dict.fromkeys(KINDS, 0)
    log = []
    try:
        tty.setraw(fd)
        os.write(sys.stdout.fileno(), ALT_ON + SCROLL_ON)
        draw(captured, counts, log)
        while True:
            r, _, _ = select.select([fd], [], [], 1.0)
            if not r:
                continue
            try:
                chunk = os.read(fd, 4096)
            except OSError:
                break
            if not chunk:
                break
            verdict = classify(chunk, counts, log)
            if verdict is True:
                break
            if verdict is None:
                captured = not captured
                seq = CELL_ON if captured else CELL_OFF
                os.write(sys.stdout.fileno(), seq)
                log.append("--- capture %s ---" % ("ON" if captured else "OFF"))
            draw(captured, counts, log)
    finally:
        os.write(sys.stdout.fileno(), CELL_OFF + SCROLL_OFF + ALT_OFF)
        termios.tcsetattr(fd, termios.TCSADRAIN, saved)
    return 0


if __name__ == "__main__":
    sys.exit(main())
