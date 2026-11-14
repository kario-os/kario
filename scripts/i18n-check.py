#!/usr/bin/env python3
"""Check the message catalogs against the code (package i18n).

For every directory with a locale/zh-Hans.json, the code beside it (and,
for a Rust crate, under its src/) is searched for translation calls with a
literal message: tr.T("...") in Go, t("...") in Rust, T("...") in Python,
tr("...") in C, C++ and TypeScript apps, phone.t("...") in mini-programs.
Each must have a zh-Hans translation. Every catalog of the directory
(locale/<lang>.json, English being the source has none) must keep each message's format verbs
in order and hold only messages still used: their English appears as a
string literal in that code (in a call, or in a table whose entries reach
the call later).

The other languages (zh-Hant, es) are translated catalog by catalog: what
they lack falls back at run time (zh-Hant to zh-Hans, everything to
English), so a missing message is reported, with each catalog's
completeness, but fails only for a language named with --strict.

    scripts/i18n-check.py [-v] [--strict LANG]... [repo]

-v lists the missing messages.
"""
import argparse
import json
import os
import re
import sys

DQ = r'"(?:[^"\\\n]|\\.)*"'
SQ = r"'(?:[^'\\\n]|\\.)*'"
BQ = r"`[^`]*`"
LANGS = {
    ".go": (r"\btr\.T\(", [DQ, BQ]),
    ".rs": (r"(?<![\w.])(?:i18n::)?t\(", [DQ]),
    ".py": (r"(?<![\w.])T\(", [DQ, SQ]),
    ".js": (r"\bphone\.t\(", [DQ, SQ, BQ]),
    ".mjs": (r"\bphone\.t\(", [DQ, SQ, BQ]),
    # Mini-programs call phone.t, TypeScript apps tr from "kario".
    ".ts": (r"(?:\bphone\.t|(?<![\w.])tr)\(", [DQ, SQ, BQ]),
    ".tsx": (r"(?:\bphone\.t|(?<![\w.])tr)\(", [DQ, SQ, BQ]),
    ".c": (r"(?<![\w.])tr\(", [DQ]),
    ".cpp": (r"(?<![\w.])tr\(", [DQ]),
    ".html": (None, [DQ, SQ]),
}
VERB = re.compile(r"%[-+# 0]*(?:\[[0-9]+\])?[0-9*]*(?:\.[0-9*]+)?[vTtbcdoOqxXUeEfFgGsp%]")
BASE = "zh-Hans"
OTHERS = ["zh-Hant", "es"]


def unquote(lit):
    body = lit[1:-1]
    if lit[0] == "`":
        return body
    if lit[0] == "'":
        body = body.replace("\\'", "'").replace('"', '\\"')
    body = re.sub(r"\\u\{([0-9a-fA-F]+)\}", lambda m: chr(int(m.group(1), 16)), body)
    try:
        return json.loads('"' + body + '"')
    except ValueError:
        return body


def sources(d):
    for name in sorted(os.listdir(d)):
        if os.path.splitext(name)[1] in LANGS:
            yield os.path.join(d, name)
    if os.path.isfile(os.path.join(d, "Cargo.toml")):
        for root, _, names in os.walk(os.path.join(d, "src")):
            for name in sorted(names):
                if name.endswith(".rs"):
                    yield os.path.join(root, name)


def verbs(s):
    return [v for v in VERB.findall(s) if v != "%%"]


def check_dir(d):
    """Returns the problems and, for each other language, the messages it lacks."""
    problems = []
    catalogs = {}
    for name in sorted(os.listdir(os.path.join(d, "locale"))):
        lang, ext = os.path.splitext(name)
        if ext == ".json":
            with open(os.path.join(d, "locale", name), encoding="utf-8") as f:
                catalogs[lang] = json.load(f)
    base = catalogs[BASE]
    used = set()
    for path in sources(d):
        call, kinds = LANGS[os.path.splitext(path)[1]]
        literal = "|".join(kinds)
        src = open(path, encoding="utf-8").read()
        for m in re.finditer(literal, src):
            used.add(unquote(m.group(0)))
        if call is None:
            continue
        for m in re.finditer(call + r"\s*(" + literal + ")", src):
            msg = unquote(m.group(1))
            if msg not in base:
                problems.append(f"{path}: no translation for {msg!r}")
    for lang, catalog in catalogs.items():
        where = f"{d}/locale/{lang}.json"
        if lang not in [BASE] + OTHERS:
            problems.append(f"{where}: {lang} is not a language the system offers ({', '.join([BASE] + OTHERS)})")
        for msg, tr in catalog.items():
            if msg not in used:
                problems.append(f"{where}: {msg!r} is not used")
            if verbs(msg) != verbs(tr):
                problems.append(f"{where}: {msg!r} and its translation have different verbs")
    missing = {lang: sorted(m for m in base if m not in catalogs.get(lang, {})) for lang in OTHERS}
    return problems, len(base), missing


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("-v", action="store_true", help="list the missing messages")
    ap.add_argument("--strict", action="append", default=[], metavar="LANG", help="fail on messages LANG lacks")
    ap.add_argument("repo", nargs="?", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    args = ap.parse_args()
    repo = args.repo.rstrip("/")
    rel = lambda p: p[len(repo) + 1:] if p.startswith(repo + "/") else p
    problems, dirs, msgs = [], 0, 0
    complete = {lang: 0 for lang in OTHERS}
    lacking = {lang: 0 for lang in OTHERS}
    report = []
    for root, subdirs, names in os.walk(repo):
        subdirs[:] = sorted(s for s in subdirs if s not in (".git", "build", "vendor", "target", "node_modules"))
        if not os.path.isfile(os.path.join(root, "locale", BASE + ".json")):
            continue
        p, n, missing = check_dir(root)
        problems += p
        dirs, msgs = dirs + 1, msgs + n
        parts = []
        for lang in OTHERS:
            lack = missing[lang]
            lacking[lang] += len(lack)
            if not lack:
                complete[lang] += 1
            parts.append(f"{lang} {n - len(lack)}/{n}")
            if lang in args.strict:
                problems += [f"{root}/locale/{lang}.json: no translation for {m!r}" for m in lack]
        report.append(f"{rel(root)}: " + ", ".join(parts))
        if args.v:
            for lang in OTHERS:
                report += [f"  {lang} lacks {m!r}" for m in missing[lang]]
    for line in report:
        print("i18n-check:", line)
    for p in problems:
        print("i18n-check:", rel(p))
    for lang in OTHERS:
        print(f"i18n-check: {lang}: {complete[lang]} of {dirs} catalogs complete, {lacking[lang]} messages missing (they fall back)")
    print(f"i18n-check: {dirs} catalogs, {msgs} messages, {len(problems)} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
