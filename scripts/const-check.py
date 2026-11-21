#!/usr/bin/env python3
"""The shared vocabulary is spelled in one place: service sockets and
methods (svc/svc.go), what apps and the system share (ui/names.go), the
capabilities (ui/caps.go). A Go file elsewhere that writes one of those
values as a literal fails here; it has to use the constant. Only distinctive
values are checked (socket names, dotted or underscored names, environment
variables, a few state words); short common words like "open" would match
too much else. A line where a value only coincides says so:
// const-check: ok (<why>).
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFS = ["svc/svc.go", "ui/names.go", "ui/caps.go"]
WORDS = {"cancelled", "foreground", "background", "playing", "paused"}
SKIP_DIRS = {"vendor", "build", ".git", "gui", "webview", "telephonyd", "docs", "node_modules"}


def distinctive(v):
    return v in WORDS or re.fullmatch(r"[a-z][a-z0-9]*([._-][a-z0-9]+)+|[A-Z][A-Z0-9_]+_[A-Z0-9_]+", v) is not None


def main():
    values = {}
    for d in DEFS:
        for name, v in re.findall(r'^\s*(\w+)\s*=\s*(?:\w+\s*\+\s*)?"([^"]+)"', open(os.path.join(ROOT, d), encoding="utf-8").read(), re.M):
            if distinctive(v):
                values.setdefault(v, f"{d}: {name}")
    bad = 0
    for root, dirs, files in os.walk(ROOT):
        dirs[:] = [x for x in dirs if x not in SKIP_DIRS]
        for f in files:
            rel = os.path.relpath(os.path.join(root, f), ROOT)
            if not f.endswith(".go") or f.endswith("_test.go") or rel in DEFS:
                continue
            for n, line in enumerate(open(os.path.join(root, f), encoding="utf-8"), 1):
                # A value that only coincides (another service's field)
                # says so on its line.
                if line.lstrip().startswith("//") or "const-check: ok" in line:
                    continue
                code = re.sub(r"`[^`]*`", "", line)
                for v in re.findall(r'"([^"\\]+)"', code):
                    if v in values:
                        print(f"{rel}:{n}: \"{v}\" is {values[v]}")
                        bad += 1
    print(f"const-check: {len(values)} shared values, {bad} written as literals")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
