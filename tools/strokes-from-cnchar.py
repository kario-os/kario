#!/usr/bin/env python3
"""Writes ime/data/strokes.tsv.gz (character, five-class stroke code) from
the stroke-order data of cnchar-order (MIT, github.com/theajack/cnchar).

The 25 stroke shapes fold into GB 13000.1's five classes, the digits of the
9-key stroke input: 1 heng, horizontal (incl. ti, rising), 2 shu, vertical
(incl. shu gou, vertical hook), 3 pie, left-falling, 4 dian, dot (incl. na,
right-falling), 5 zhe, every stroke with a turn. cnchar names the shapes in
Chinese, hence the escapes.

    python3 tools/strokes-from-cnchar.py cnchar.order.min.js ime/data/strokes.tsv.gz
"""
import gzip, json, re, sys

src, dst = sys.argv[1], sys.argv[2]
js = open(src, encoding="utf-8").read()
blobs = [json.loads(b) for b in re.findall(r"JSON\.parse\('(\{.*?\})'\)", js, re.S)]
chars = next(b for b in blobs if "\u4e00" in b)
shapes = next(b for b in blobs if "j" in b and isinstance(b["j"], dict))
cls = {}
for letter, s in shapes.items():
    name = s["name"]
    if name in ("\u6a2a", "\u63d0"):
        cls[letter] = "1"
    elif name in ("\u7ad6", "\u7ad6\u94a9"):
        cls[letter] = "2"
    elif name == "\u6487":
        cls[letter] = "3"
    elif name in ("\u70b9", "\u70b92", "\u637a"):
        cls[letter] = "4"
    elif s["type"] == "\u6298\u7b14":
        cls[letter] = "5"
    else:
        sys.exit(f"unclassified stroke {letter} {name}")
with gzip.open(dst, "wt", encoding="utf-8", compresslevel=9) as out:
    for ch, letters in sorted(chars.items(), key=lambda kv: (len(kv[1]), kv[0])):
        out.write(f"{ch}\t{''.join(cls[l] for l in letters)}\n")
print(f"{len(chars)} characters")
