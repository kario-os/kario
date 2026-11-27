#!/usr/bin/env python3
"""What a QEMU machine hands the host over its serial console: files whose
every line carries its number and Adler-32 (console-file in the image,
phone-shell --print-png),

    PHONE-FILE <name> <n> <Adler-32 of the line> <line>
    PHONE-FILE-END <name> <lines> <Adler-32 of them all> [base64]

each line in one write, straight to the console or through the journal
(which puts "[<seconds>] <unit's name>[<pid>]: " before it). The kernel
prints on that console past the tty, a line at a time ("[<seconds>] ..."),
and such a line can land anywhere inside one of the machine's, whose rest
comes after it (after any more of the kernel's). A file's line is mended,
the kernel's lines taken out and the rest put back, when its sum then
holds; one that cannot be, a line missing, a file without its end are named
with the console's line, and the exit status is 1. A kernel line that only
quotes a file's line (selftest-heartbeat.sh's "last:") holds none. A file
whose name ends in .ask is a machine's requests to the host as it runs,
line n request n, said again until answered, without an end
(qemu-buttons.py reads them): no file here, its lines not judged.

files <console-log> <dir>: every file whole into <dir> (decoded where its
end says base64); what was mended and what is wrong on stdout.
text <console-log>: the console without terminal controls and carriage
returns, each file's line as its text alone, the kernel's lines taken out
of it after it; those that cannot be mended left out and named on stderr.
A file without its end is no error here (a log written as the machine went
on, which may have gone down before its end).
report <console-log> <NAME>: a run's report (soak-boot.sh's, stress-boot.sh's,
bench-boot.sh's) out of the console's text as text gives it, from its last
<NAME>-BEGIN line to the <NAME>-RESULT line after it, or to <NAME>-END in a
report without one (an image whose report was no console file printed it as
plain lines, which come out as they are); exit status 0 when nothing is
wrong and the report says <NAME>-RESULT: PASS, or, without a result line,
has its <NAME>-END.

Usage: qemu-console.py files <console-log> <dir> | text <console-log> | report <console-log> <NAME>
"""
import base64
import binascii
import os
import re
import sys
import zlib

MARK = b"PHONE-FILE"
NAME = rb"([A-Za-z0-9_][A-Za-z0-9._-]*)"
LINE = re.compile(rb"PHONE-FILE " + NAME + rb" ([1-9][0-9]*) ([0-9a-f]{8})(?: (.*))?\Z", re.S)
END = re.compile(rb"PHONE-FILE-END " + NAME + rb" ([0-9]+) ([0-9a-f]{8})( base64)?\Z")
KERNEL = re.compile(rb"\[ *[0-9]+\.[0-9]{6}\] ")
JOURNAL = re.compile(rb"\[ *[0-9]+\.[0-9]{6}\] [^\s\[\]:]+\[[0-9]+\]: \Z")
# CSI, OSC/DCS/APC/PM/SOS strings, the two-byte ones; getty and systemd
# leave them before a line (the journal's lines too), colours inside one.
TERMINAL = re.compile(rb"\x1b(?:\[[0-?]*[ -/]*[@-~]|[]P^_X][^\x07\x1b]*(?:\x07|\x1b\\)?|[ -~]?)")
# How deep a line can be cut (a kernel line inside the rest of one already
# cut, ...), and the most kernel lines in a row inside one.
DEPTH = 3
BURST = 2000


def console_lines(raw):
    return [TERMINAL.sub(b"", line) for line in raw.replace(b"\r", b"").split(b"\n")]


def own(pre):
    """A file's line after pre is one of the machine's: at the start of the
    console's line, after what a terminal left there, or after the
    journal's prefix; not one a kernel or journal line quotes."""
    return JOURNAL.search(pre) is not None or KERNEL.search(pre) is None


def parse(text):
    """The file's line in text, from its first own mark: (kind, name, n,
    sum, rest) with kind "line", "end" or None (a mark and no line), or
    None when text has no mark."""
    k = text.find(MARK)
    while k >= 0:
        if own(text[:k]):
            m = LINE.match(text, k)
            if m:
                return "line", m[1].decode(), int(m[2]), int(m[3], 16), m[4] or b""
            m = END.match(text, k)
            if m:
                return "end", m[1].decode(), int(m[2]), int(m[3], 16), m[4] is not None
            return None, None, None, None, text[k:]
        k = text.find(MARK, k + 1)
    return None


def sound(p):
    return p is not None and (p[0] == "end" or (p[0] == "line" and zlib.adler32(p[4]) == p[3]))


def asks(name):
    """The file of that name holds requests (qemu-buttons.py)."""
    return name.endswith(".ask")


def starts(head):
    """head, cut by a kernel line, has the start of a file's line in it."""
    if parse(head) is not None:
        return True
    return any(head.endswith(MARK[:n]) and own(head[:-n]) for n in range(1, len(MARK)))


def readings(lines, i, depth, top=True):
    """Line i as it is, then with a kernel line from each place on taken out
    and the rest put back from a line after it (past more kernel lines),
    itself read so: (text, the line after the last read, what was taken
    out). At the top only where what comes before has a file's line."""
    yield lines[i], i + 1, []
    if depth == 0:
        return
    for k in KERNEL.finditer(lines[i], 1):
        head = lines[i][:k.start()]
        if top and not starts(head):
            continue
        for j in range(i + 1, min(len(lines), i + 1 + BURST)):
            for text, end, taken in readings(lines, j, depth - 1, False):
                yield head + text, end, [lines[i][k.start():]] + lines[i + 1:j] + taken
            if not KERNEL.match(lines[j]):
                break


def read(lines):
    """The console's lines: ("other", at, line), ("damaged", at, parse of
    the line as it is) or (parse, at, end, taken) for a file's line."""
    i = 0
    while i < len(lines):
        line = lines[i]
        if MARK not in line and KERNEL.search(line, 1) is None:
            yield "other", i, line
            i += 1
            continue
        seen = None
        for text, end, taken in readings(lines, i, DEPTH):
            p = parse(text)
            if sound(p):
                yield p, i, end, taken
                i = end
                break
            seen = seen or p
        else:
            yield ("damaged", i, seen) if seen else ("other", i, line)
            i += 1


def show(b, most=160):
    s = b.decode("utf-8", "replace")
    return s if len(s) <= most else s[:most] + "..."


def ranges(ns):
    out, ns = [], sorted(ns)
    while ns:
        a = b = ns.pop(0)
        while ns and ns[0] == b + 1:
            b = ns.pop(0)
        out.append(str(a) if a == b else f"{a}-{b}")
    return ", ".join(out)


class Console:
    """The files of one console log as their lines come; files: they are
    kept, each must have its end."""

    def __init__(self, log, say, files):
        self.log, self.say, self.files = log, say, files
        self.lost = "not kept, " if files else ""
        self.bad = 0
        # name: (its lines so far, a line's text or None (not had) or False
        # (damaged); the console line of its first)
        self.open = {}
        self.kept = {}

    def wrong(self, msg):
        self.bad += 1
        self.say(f"{self.log}: {msg}")

    def lines(self, name, n, at):
        """The lines of the file line n of name belongs to, at least n
        (line 1 again starts the file anew)."""
        f = self.open.get(name)
        if f is None or (n == 1 and f[0][0] is not None):
            if f is not None:
                self.unended(name)
            f = self.open[name] = ([], at)
        if n > len(f[0]):
            f[0].extend([None] * (n - len(f[0])))
        return f[0]

    def unended(self, name):
        have, at = self.open.pop(name)
        missing = [n + 1 for n, t in enumerate(have) if t is None]
        if missing:
            self.wrong(f"{name}: line {ranges(missing)} missing (the file from console line {at + 1})")
        if self.files:
            self.wrong(f"{name}: no end after its line {len(have)} (the file from console line {at + 1}): cut short")

    def line(self, name, n, text, at):
        have = self.lines(name, n, at)
        if isinstance(have[n - 1], bytes) and have[n - 1] != text:
            self.wrong(f"{name}: line {n} again at console line {at + 1}, not as before")
        have[n - 1] = text

    def damaged(self, p, at, line):
        what = f"{p[1]} line {p[2]}" if p[0] == "line" else "a console file's line"
        self.wrong(f"line {at + 1}: {what} damaged, its sum holds neither as it is nor with a kernel line taken out: {show(line)}")
        if p[0] == "line":
            have = self.lines(p[1], p[2], at)
            if not isinstance(have[p[2] - 1], bytes):
                have[p[2] - 1] = False

    def end(self, name, count, total, b64, at):
        have = self.open.pop(name, ([], at))[0]
        where = f"its end at console line {at + 1}"
        damaged = [n + 1 for n, t in enumerate(have) if t is False]
        missing = [n + 1 for n, t in enumerate(have) if t is None] + list(range(len(have) + 1, count + 1))
        if damaged:
            self.wrong(f"{name}: {self.lost}line {ranges(damaged)} damaged")
            return
        if missing:
            self.wrong(f"{name}: {self.lost}line {ranges(missing)} of {count} missing before {where}")
            return
        if len(have) != count:
            self.wrong(f"{name}: {self.lost}{len(have)} lines where {where} says {count}")
            return
        data = b"".join(t + b"\n" for t in have)
        if zlib.adler32(data) != total:
            self.wrong(f"{name}: {self.lost}its lines do not have the sum {where} says")
            return
        if b64:
            try:
                data = base64.b64decode(data.replace(b"\n", b""), validate=True)
            except binascii.Error as e:
                self.wrong(f"{name}: {self.lost}not base64 ({e})")
                return
        self.kept[name] = data

    def close(self):
        for name in list(self.open):
            self.unended(name)


def walk(log, raw, say, text=None, mended=None):
    """Read the console log raw, telling say what is wrong and mended what
    was mended (say, without text); without text the files are kept, text
    gets the console's lines."""
    if mended is None and text is None:
        mended = say
    c = Console(log, say, text is None)
    lines = console_lines(raw)
    for ev in read(lines):
        if ev[0] == "other":
            if text is not None:
                text.append(ev[2])
        elif ev[0] == "damaged":
            if ev[2][0] == "line" and asks(ev[2][1]):
                if text is not None:
                    text.append(lines[ev[1]])
                continue
            c.damaged(ev[2], ev[1], lines[ev[1]])
        else:
            p, at, end, taken = ev
            if asks(p[1]):
                if text is not None:
                    text.append(p[4] if p[0] == "line" else lines[at])
                    text.extend(taken)
                continue
            if taken and mended:
                mended(f"{log}: line {at + 1}: {p[1]} {'line ' + str(p[2]) if p[0] == 'line' else 'end'} mended, "
                       f"{len(taken)} kernel line{'s' if len(taken) > 1 else ''} taken out of it: {show(taken[0])}")
            if p[0] == "line":
                c.line(p[1], p[2], p[4], at)
                if text is not None:
                    text.append(p[4])
            else:
                c.end(p[1], p[2], p[3], p[4], at)
            if text is not None:
                text.extend(taken)
    c.close()
    return c


def report(text, name):
    """The report called name in the console's text: (its lines, whether
    it passed)."""
    begin = max((i for i, t in enumerate(text) if name + b"-BEGIN" in t), default=None)
    if begin is None:
        return [], False
    rest = text[begin:]
    last = next((i for i, t in enumerate(rest) if name + b"-RESULT" in t), None)
    if last is not None:
        return rest[:last + 1], name + b"-RESULT: PASS" in rest[last]
    last = next((i for i, t in enumerate(rest) if name + b"-END" in t), None)
    return (rest, False) if last is None else (rest[:last + 1], True)


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "files":
        log, out = sys.argv[2:]
        with open(log, "rb") as f:
            raw = f.read()
        c = walk(log, raw, print)
        os.makedirs(out, exist_ok=True)
        for name, data in c.kept.items():
            with open(os.path.join(out, name), "wb") as f:
                f.write(data)
        print(f"{log}: {len(c.kept)} files kept in {out}{': ' + ' '.join(sorted(c.kept)) if c.kept else ''}"
              f"{f'; {c.bad} wrong' if c.bad else ''}")
        return 1 if c.bad else 0
    if len(sys.argv) == 3 and sys.argv[1] == "text":
        with open(sys.argv[2], "rb") as f:
            raw = f.read()
        text = []
        c = walk(sys.argv[2], raw, lambda m: print(m, file=sys.stderr), text)
        sys.stdout.buffer.write(b"".join(t + b"\n" for t in text))
        return 1 if c.bad else 0
    if len(sys.argv) == 4 and sys.argv[1] == "report":
        with open(sys.argv[2], "rb") as f:
            raw = f.read()
        text, said = [], []
        c = walk(sys.argv[2], raw, said.append, text, said.append)
        lines, passed = report(text, sys.argv[3].encode())
        sys.stdout.buffer.write(b"".join(t + b"\n" for t in lines))
        sys.stdout.flush()
        for m in said + ([] if lines else [f"{sys.argv[2]}: no {sys.argv[3]}-BEGIN"]):
            print(m)
        return 0 if passed and not c.bad else 1
    print(__doc__.strip().splitlines()[-1], file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
