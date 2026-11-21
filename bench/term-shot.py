#!/usr/bin/env python3
"""Run a terminal program in a pty of a given size, replay its output through
a terminal emulator (pyte) and write the final screen as an .ans file that
tools/snap2png can render. Used to see what a browser-in-terminal (Carbonyl)
shows at the phone's 40x24 cells.

  term-shot.py --cols 40 --rows 24 --wait 8 --keys 'down,enter' --out x.ans -- carbonyl URL
"""
import argparse
import fcntl
import os
import pty
import select
import signal
import struct
import termios
import time

import pyte

KEYS = {"down": b"\x1b[B", "up": b"\x1b[A", "enter": b"\r", "tab": b"\t", "esc": b"\x1b"}
BASIC = {"black": (0, 0, 0), "red": (205, 49, 49), "green": (13, 188, 121), "brown": (229, 229, 16),
         "yellow": (229, 229, 16), "blue": (36, 114, 200), "magenta": (188, 63, 188), "cyan": (17, 168, 205),
         "white": (229, 229, 229)}


def rgb(c, default):
    if c == "default":
        return default
    if c in BASIC:
        return BASIC[c]
    if len(c) == 6:
        return tuple(int(c[i:i + 2], 16) for i in (0, 2, 4))
    return default


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cols", type=int, default=40)
    ap.add_argument("--rows", type=int, default=24)
    ap.add_argument("--wait", type=float, default=8)
    ap.add_argument("--keys", default="")
    ap.add_argument("--out", required=True)
    ap.add_argument("cmd", nargs="+")
    a = ap.parse_args()
    screen = pyte.Screen(a.cols, a.rows)
    stream = pyte.ByteStream(screen)
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["COLORTERM"] = "truecolor"
        os.execvp(a.cmd[0], a.cmd)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", a.rows, a.cols, 0, 0))

    def pump(seconds):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            r, _, _ = select.select([fd], [], [], 0.05)
            if r:
                try:
                    data = os.read(fd, 1 << 16)
                except OSError:
                    return
                stream.feed(data)
                # Answer cursor-position and device-attribute probes.
                if b"\x1b[6n" in data:
                    os.write(fd, b"\x1b[1;1R")
                if b"\x1b[c" in data or b"\x1b[0c" in data:
                    os.write(fd, b"\x1b[?62;22c")

    pump(a.wait)
    for k in filter(None, a.keys.split(",")):
        os.write(fd, KEYS.get(k, k.encode()))
        pump(1.5)
    pump(1.0)
    lines = []
    for y in range(a.rows):
        row = screen.buffer[y]
        out = []
        for x in range(a.cols):
            ch = row[x]
            fg = rgb(ch.fg, (220, 220, 228))
            bg = rgb(ch.bg, (0, 0, 0))
            if ch.reverse:
                fg, bg = bg, fg
            data = ch.data or " "
            if data == "":
                continue
            out.append("\x1b[0;38;2;%d;%d;%d;48;2;%d;%d;%dm%s" % (fg + bg + (data,)))
        lines.append("".join(out) + "\x1b[0m")
    with open(a.out, "w") as f:
        f.write("\n".join(lines))
    os.kill(pid, signal.SIGKILL)


if __name__ == "__main__":
    main()
