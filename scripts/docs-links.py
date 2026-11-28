#!/usr/bin/env python3
"""Relative links in docs/ and docs/en/ resolve, and section numbers hold
(make test, CI).

A link is [text](target) outside code: fenced blocks and inline code are
left out, so a C++ lambda like [](kario::module &m) in an example is no link.

A heading that starts with a number ("## 9. Charger mode", "### 8.2 When it
does not come up") gives that section number, and one file gives each number
once. A link to a document followed by section numbers ("[39](39-oneplus-6t.md)
§8.2", more numbers joined by the Chinese enumeration comma, ", " or " and ")
names sections that document's headings give.
"""
import os
import re
import sys

FENCE = re.compile(r"^(```|~~~).*?^\1[^\n]*$", re.M | re.S)
INLINE = re.compile(r"(`+)(?:(?!\1).)+?\1", re.S)
LINK = re.compile(r"\]\(([^)#\s]+)")
NUMBER = r"\d+(?:\.\d+)*"
HEADING = re.compile(rf"^#{{1,6}}[ \t]+({NUMBER})\.?(?=\s)", re.M)
# "...md) §8.2", "...md)'s §8.2" or with the Chinese possessive particle.
SECTION_REF = re.compile(rf"\]\(([^)#\s]+\.md)\)(?:'s|\s*\u7684)?\s*§\s?({NUMBER})"
                         rf"((?:(?:\u3001|, | and )§\s?{NUMBER})*)")
MORE = re.compile(rf"§\s?({NUMBER})")


def prose(text):
    """text with fenced blocks and inline code blanked, keeping its lines."""
    def blank(m):
        return "\n" * m.group(0).count("\n")
    return INLINE.sub(blank, FENCE.sub(blank, text))


def line_of(text, pos):
    return text.count("\n", 0, pos) + 1


def links(text):
    return LINK.findall(prose(text))


def sections(text):
    """The section numbers the headings give, each with its line."""
    text = prose(text)
    return [(m.group(1), line_of(text, m.start())) for m in HEADING.finditer(text)]


def section_refs(text):
    """(target, section number, line) for each section a link names."""
    text = prose(text)
    out = []
    for m in SECTION_REF.finditer(text):
        line = line_of(text, m.start())
        for n in [m.group(2)] + MORE.findall(m.group(3)):
            out.append((m.group(1), n, line))
    return out


def check(root):
    """The broken links, the repeated section numbers and the links to
    sections that do not exist in root and root/en, one line each."""
    missing, repeated, unknown = [], [], []
    given = {}

    def numbers(path):
        if path not in given:
            with open(path, encoding="utf-8") as fh:
                given[path] = {n for n, _ in sections(fh.read())}
        return given[path]

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
                    missing.append(f"{d}/{f}: missing {target}")
            first = {}
            for n, line in sections(text):
                if n in first:
                    repeated.append(f"{d}/{f}:{line}: section {n} again (first at line {first[n]})")
                else:
                    first[n] = line
            for target, n, line in section_refs(text):
                path = os.path.normpath(os.path.join(d, target))
                if os.path.isfile(path) and n not in numbers(path):
                    unknown.append(f"{d}/{f}:{line}: {target} has no section {n}")
    return missing, repeated, unknown


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else "docs"
    missing, repeated, unknown = check(root)
    for line in missing + repeated + unknown:
        print(line)
    print(f"docs-links: {len(missing)} broken links, {len(repeated)} repeated section numbers, "
          f"{len(unknown)} links to sections that do not exist")
    return 1 if missing or repeated or unknown else 0


if __name__ == "__main__":
    sys.exit(main())
