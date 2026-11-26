#!/usr/bin/env python3
"""Fail when a file make publish would push is a program or too big.

scripts/publish.sh commits every file git does not ignore (git add -A):
the tracked files and the untracked ones .gitignore does not name. A
program built in the tree goes out with them unless .gitignore names it,
as apps/settings/settings did (6.4 MB, a go build in the app's
directory). Each of those files fails when it starts as an ELF, Mach-O or
PE program does, or when it is larger than LIMIT, unless BIG or PROGRAMS
allows it. publish.sh refuses to push while this fails.

    scripts/binary-check.py [repo]
"""
import os
import struct
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Above the largest file of each kind that belongs here (the sample video,
# 300 KiB; shell/selftest.script, 230 KiB; a sample photo, 210 KiB; a
# picture in docs/img, 193 KiB), below a Go program (1.5 MiB when empty)
# and phone-paint (2.2 MiB).
LIMIT = 512 << 10
# Files larger than LIMIT that belong in git: the most bytes each may have
# and why it is here.
BIG = {
    "ime/data/zh.tsv.gz": (1 << 20, "the pinyin input method's dictionary (AOSP PinyinIME's word list, written by "
                                    "tools/imedict/build.py with gzip -9), embedded in package ime"),
}
# Programs that belong in git (a test's fixture of a program reader) and why.
PROGRAMS = {}

# Thin Mach-O files of either byte order, 32 and 64 bits, and universal
# ones; a Java class starts as a universal file does, a build product too.
MACHO = {bytes.fromhex(m) for m in ("feedface", "cefaedfe", "feedfacf", "cffaedfe", "cafebabe", "cafebabf")}


def program(path):
    """The format of the program the file is ("ELF", "Mach-O", "PE"), or None."""
    with open(path, "rb") as f:
        head = f.read(64)
        if head.startswith(b"\x7fELF"):
            return "ELF"
        if head[:4] in MACHO:
            return "Mach-O"
        # MZ alone could start a text; a PE program's DOS header points at "PE\0\0".
        if head.startswith(b"MZ") and len(head) == 64:
            f.seek(struct.unpack_from("<I", head, 0x3c)[0])
            if f.read(4) == b"PE\0\0":
                return "PE"
    return None


def published(repo):
    """(path, file) of what publish.sh's git add -A commits: the tracked
    files still there and the untracked ones git does not ignore. A
    symbolic link is committed as the path it points to."""
    try:
        out = subprocess.run(["git", "-C", repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                             capture_output=True, check=True).stdout
    except subprocess.CalledProcessError as e:
        raise SystemExit(f"binary-check: git ls-files in {repo} failed: {e.stderr.decode(errors='replace').strip()}")
    except OSError as e:
        raise SystemExit(f"binary-check: git: {e}")
    for f in sorted({f for f in out.decode("utf-8", "surrogateescape").split("\0") if f}):
        p = os.path.join(repo, f)
        if os.path.isfile(p) and not os.path.islink(p):
            yield f, p


def size(n):
    if n >= 1 << 20:
        return f"{n / (1 << 20):.1f} MiB"
    return f"{n / 1024:.0f} KiB" if n >= 1 << 10 else f"{n} B"


def check(repo, limit=LIMIT, big=None, programs=None):
    """The problems, one line each, and how many files were checked."""
    big = BIG if big is None else big
    programs = PROGRAMS if programs is None else programs
    bad, checked = [], 0
    for f, p in published(repo):
        checked += 1
        n = os.path.getsize(p)
        kind = program(p)
        if kind and f not in programs:
            bad.append(f"{f}: a program ({kind}, {size(n)}): delete it or name it in .gitignore")
        elif f in big and n > big[f][0]:
            bad.append(f"{f}: {size(n)}, over the {size(big[f][0])} BIG allows it")
        elif f not in big and n > limit:
            bad.append(f"{f}: {size(n)}, over the {size(limit)} limit: make it smaller, keep it out of git, "
                       "or add it to BIG in scripts/binary-check.py with why")
    return bad, checked


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    bad, n = check(argv[0] if argv else ROOT)
    for b in bad:
        print("binary-check:", b)
    print(f"binary-check: {n} files publish would push, {len(bad)} problems")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
