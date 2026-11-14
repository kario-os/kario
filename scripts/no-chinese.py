#!/usr/bin/env python3
"""Fail when program code holds Chinese, comments included.

The text a user reads lives in each component's locale/<language>.json
(package i18n); code, comments, build files and unit files are English.
What is data may hold Chinese: the locale catalogs, app manifests and other
JSON, the selftest scripts (the scenario in the UI's language), the SKU
descriptions, the documents and test fixtures. Code that has to name a
Chinese character (a test of CJK handling) writes it as an escape.

    scripts/no-chinese.py [repo]
"""
import os
import re
import subprocess
import sys

CJK = re.compile("[\u3000-\u303f\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\ufe30-\ufe4f\uff00-\uffef]")
CODE_EXT = {".go", ".rs", ".c", ".h", ".cc", ".cpp", ".hpp", ".py", ".js", ".mjs", ".ts", ".tsx", ".sh", ".slint",
            ".html", ".css", ".bb", ".bbappend", ".bbclass", ".inc", ".conf", ".cfg", ".service", ".socket",
            ".timer", ".yml", ".yaml", ".cmake", ".toml", ".wks", ".dts", ".dtsi"}
CODE_NAMES = {"Makefile", "CMakeLists.txt", "Containerfile", "Dockerfile"}
DATA_TOP = ("sku/", "docs/")
DATA_DIRS = ("testdata", "locale")


def is_code(path):
    if path.startswith(DATA_TOP) or any(d in DATA_DIRS for d in path.split("/")[:-1]):
        return False
    base = os.path.basename(path)
    return base in CODE_NAMES or os.path.splitext(base)[1] in CODE_EXT


def files(repo):
    out = subprocess.run(["git", "-C", repo, "ls-files", "--cached", "--others", "--exclude-standard"],
                         capture_output=True, text=True, check=True).stdout
    return sorted(set(out.split()))


def main():
    repo = sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    bad = []
    for f in files(repo):
        p = os.path.join(repo, f)
        if not is_code(f) or not os.path.isfile(p):
            continue
        try:
            text = open(p, encoding="utf-8").read()
        except UnicodeDecodeError:
            continue
        for n, line in enumerate(text.splitlines(), 1):
            if CJK.search(line):
                bad.append(f"{f}:{n}: {line.strip()[:100]}")
    for b in bad:
        print("no-chinese:", b)
    print(f"no-chinese: {len(bad)} lines of code with Chinese")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
