#!/usr/bin/env python3
"""Merge CMake's compile_commands.json files into one, for editors that
open the repository at its root (make lsp).

    scripts/compile-db.py BUILD-DIR... > compile_commands.json
"""
import json
import os
import sys


def main():
    out = []
    for d in sys.argv[1:]:
        p = os.path.join(d, "compile_commands.json")
        if os.path.exists(p):
            with open(p, encoding="utf-8") as f:
                out += json.load(f)
    json.dump(out, sys.stdout, indent=1)
    print()


if __name__ == "__main__":
    main()
