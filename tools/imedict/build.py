#!/usr/bin/env python3
"""Build the embedded IME dictionaries from AOSP sources (Apache-2.0):

  PinyinIME  jni/data/rawdict_utf16_65105_freq.txt  -> ime/data/zh.tsv.gz  (word, freq, pinyin)
  LatinIME   dictionaries/en_wordlist.combined.gz   -> ime/data/en.tsv.gz  (word, freq)

Usage: build.py <rawdict_utf16_65105_freq.txt> <en_wordlist.combined.gz> <out dir>
Sources: https://android.googlesource.com/platform/packages/inputmethods/PinyinIME
         https://android.googlesource.com/platform/packages/inputmethods/LatinIME
"""
import gzip
import re
import sys

zh_src, en_src, out = sys.argv[1:4]

rows = []
with open(zh_src, encoding="utf-16") as f:
    for line in f:
        parts = line.strip().lstrip("\ufeff").split()
        if len(parts) < 4:
            continue
        word, freq, pinyin = parts[0], float(parts[1]), parts[3:]
        if len(pinyin) != len(word):
            continue
        rows.append((word, freq, " ".join(pinyin)))
rows.sort(key=lambda r: -r[1])
with gzip.open(f"{out}/zh.tsv.gz", "wt", encoding="utf-8", compresslevel=9) as f:
    for word, freq, pinyin in rows:
        f.write(f"{word}\t{freq:.4g}\t{pinyin}\n")

words = {}
entry = re.compile(r"^ word=([^,]+),f=(\d+),flags=([^,]*)")
with gzip.open(en_src, "rt", encoding="utf-8") as f:
    for line in f:
        m = entry.match(line)
        if not m:
            continue
        word, freq, flags = m.group(1), int(m.group(2)), m.group(3)
        if "offensive" in flags or not re.fullmatch(r"[A-Za-z]+(?:'[A-Za-z]+)?", word):
            continue
        words[word] = max(freq, words.get(word, 0))
top = sorted(words.items(), key=lambda kv: -kv[1])[:60000]
with gzip.open(f"{out}/en.tsv.gz", "wt", encoding="utf-8", compresslevel=9) as f:
    for word, freq in top:
        f.write(f"{word}\t{freq}\n")

print(f"zh {len(rows)} entries, en {len(top)} words")
