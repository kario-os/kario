#!/usr/bin/env python3
"""Relative links in docs/ and docs/en/ resolve (make test, CI).

A link is [text](target) outside code: fenced blocks and inline code are
left out, so a C++ lambda like [](kario::module &m) in an example is no link.
"""
import os
import re
import sys

FENCE = re.compile(r"^(```|~~~).*?^\1[^\n]*$", re.M | re.S)
INLINE = re.compile(r"(`+)(?:(?!\1).)+?\1", re.S)
LINK = re.compile(r"\]\(([^)#\s]+)")


def links(text):
    text = FENCE.sub("", text)
    text = INLINE.sub("", text)
    return LINK.findall(text)


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else "docs"
    bad = 0
    for d in (root, os.path.join(root, "en")):
        for f in sorted(os.listdir(d)):
            if not f.endswith(".md"):
                continue
            with open(os.path.join(d, f), encoding="utf-8") as fh:
                text = fh.read()
            for target in links(text):
                if "://" in target or target.startswith("mailto:"):
                    continue
                if not os.path.exists(os.path.join(d, target)):
                    print(f"{d}/{f}: missing {target}")
                    bad += 1
    print(f"docs-links: {bad} broken links")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
