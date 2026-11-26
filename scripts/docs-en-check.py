#!/usr/bin/env python3
"""The English documents (docs/en/) follow the Chinese ones (docs/): every
document has its translation, which points back to the original and has the
same structure (headings, table rows, code fences), so a section or a table
row added in Chinese and not in English shows up here.

    scripts/docs-en-check.py [docs]
"""
import os
import re
import sys


def shape(path):
    heads = rows = fences = 0
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            s = line.lstrip()
            if s.startswith("```"):
                fences += 1
            elif re.match(r"#{1,6} ", line):
                heads += 1
            elif s.startswith("|"):
                rows += 1
    return heads, rows, fences


def main():
    docs = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "docs")
    en = os.path.join(docs, "en")
    names = sorted(n for n in os.listdir(docs) if n.endswith(".md") and (re.match(r"\d\d-", n) or n == "README.md"))
    bad = []
    for n in names:
        tr = os.path.join(en, n)
        if not os.path.exists(tr):
            bad.append(f"{n}: no English translation in docs/en/")
            continue
        with open(tr, encoding="utf-8") as fh:
            first = fh.readline()
        if f"(../{n})" not in first:
            bad.append(f"en/{n}: the first line does not point to ../{n}")
        zh, e = shape(os.path.join(docs, n)), shape(tr)
        if n != "README.md" and zh != e:
            what = ", ".join(f"{k} {a} vs {b}" for k, a, b in zip(("headings", "table rows", "code fences"), zh, e) if a != b)
            bad.append(f"{n}: the English file differs: {what}")
    for n in sorted(os.listdir(en)):
        if n.endswith(".md") and n not in names:
            bad.append(f"en/{n}: no Chinese original")
    for b in bad:
        print("docs-en-check:", b)
    print(f"docs-en-check: {len(names)} documents, {len(bad)} problems")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
