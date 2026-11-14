#!/usr/bin/env python3
"""License boundary check (D7, 58-name-ip-open-source.md §3.2).

The platform (Go module: shell, services, SDK, apps) is Apache-2.0 and must
stay free of copyleft code so that anyone, including us, can build closed
products on it. The device's renderer (paint/: phone-paint, Apache-2.0 like
the platform) must be shippable without GPLv3 (D9); phone-gui (gui/) is
GPL-3.0-only, a renderer to compare with, never in a device image.

Exit status 1 when a platform dependency is copyleft or unrecognised.
"""
import glob
import os
import re
import sys

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

PERMISSIVE = {"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC", "Zlib", "Unlicense", "CC0-1.0"}
WEAK = {"MPL-2.0"}  # file-level copyleft: fine unmodified, changes to those files stay MPL

# Order matters: the GNU texts mention other licences, so they go first.
PATTERNS = [
    ("AGPL-3.0", r"GNU AFFERO GENERAL PUBLIC LICENSE"),
    ("LGPL", r"GNU LESSER GENERAL PUBLIC LICENSE|GNU LIBRARY GENERAL PUBLIC LICENSE"),
    ("GPL", r"GNU GENERAL PUBLIC LICENSE"),
    ("MPL-2.0", r"Mozilla Public License,? (version|Version) 2\.0"),
    ("Apache-2.0", r"Apache License[\s\S]{0,200}Version 2\.0"),
    ("MIT", r"Permission is hereby granted, free of charge, to any person"),
    ("BSD-3-Clause", r"Redistribution and use in source and binary forms[\s\S]*(Neither the name|names of its\s+contributors)"),
    ("BSD-2-Clause", r"Redistribution and use in source and binary forms"),
    ("ISC", r"Permission to use, copy, modify, and(/or)? distribute this software for any"),
    ("Zlib", r"This software is provided 'as-is', without any express or implied"),
    ("Unlicense", r"This is free and unencumbered software released into the public domain"),
    ("CC0-1.0", r"CC0 1\.0 Universal"),
]
# Modules without a licence file, checked by hand: what their README says.
REVIEWED = {
    "github.com/mattn/go-localereader": ("MIT", "README.md, section License"),
}
LICENSE_FILES = re.compile(r"^(LICEN[CS]E|COPYING)(\.md|\.txt)?$", re.I)


def classify(text):
    for name, pat in PATTERNS:
        if re.search(pat, text):
            return name
    return "unknown"


def module_license(path):
    """The licence of a vendored module: its own file, else the nearest parent's."""
    d = os.path.join(FW, "vendor", path)
    while True:
        if os.path.isdir(d):
            for f in sorted(os.listdir(d)):
                if LICENSE_FILES.match(f):
                    with open(os.path.join(d, f), errors="replace") as fh:
                        return classify(fh.read()), os.path.relpath(os.path.join(d, f), FW)
        parent = os.path.dirname(d)
        if parent == d or not parent.startswith(os.path.join(FW, "vendor")):
            return "unknown", None
        d = parent


def go_modules():
    mods = []
    with open(os.path.join(FW, "vendor", "modules.txt")) as fh:
        for line in fh:
            m = re.match(r"^# (\S+) (\S+)", line)
            if m:
                mods.append((m.group(1), m.group(2)))
    return mods


SPDX_PERMISSIVE = PERMISSIVE | {"BSD-2-Clause", "BSL-1.0", "Unicode-3.0", "Unicode-DFS-2016", "0BSD", "Apache-2.0 WITH LLVM-exception", "MIT-0"}


def permissive_option(expr):
    """Some OR-alternative of an SPDX expression is all permissive."""
    if not expr or expr == "(licence file)":
        return False
    for alt in re.split(r"\s+OR\s+|/", expr.replace("(", "").replace(")", "")):
        parts = [p.strip() for p in re.split(r"\s+AND\s+", alt)]
        if all(p in SPDX_PERMISSIVE for p in parts):
            return True
    return False


GPL3 = "GPL-3.0"


def renderer_dir(slot):
    return os.path.join(FW, slot)


def renderer_crates(slot="gui"):
    """name-version -> licence expression, from Cargo.lock and the local registry."""
    lock = os.path.join(renderer_dir(slot), "Cargo.lock")
    if not os.path.exists(lock):
        return {}
    with open(lock) as fh:
        pkgs = re.findall(r'\[\[package\]\]\nname = "([^"]+)"\nversion = "([^"]+)"', fh.read())
    home = os.environ.get("CARGO_HOME", os.path.expanduser("~/.cargo"))
    out = {}
    for name, ver in pkgs:
        lic = ""
        if name in ("phone-gui", "phone-paint"):
            src = [os.path.join(renderer_dir(slot), "Cargo.toml")]
        else:
            src = glob.glob(os.path.join(home, "registry", "src", "*", f"{name}-{ver}", "Cargo.toml"))
        for toml in src[:1]:
            with open(toml, errors="replace") as fh:
                m = re.search(r'^license\s*=\s*"([^"]+)"', fh.read(), re.M)
            lic = m.group(1) if m else "(licence file)"
        out[f"{name}-{ver}"] = lic
    return out


def main():
    bad = False
    with open(os.path.join(FW, "LICENSE")) as fh:
        own = classify(fh.read())
    print(f"platform (fw/LICENSE): {own}")
    if own != "Apache-2.0":
        print("  FAIL: the platform must be Apache-2.0")
        bad = True

    counts = {}
    for mod, ver in go_modules():
        lic, where = module_license(mod)
        if lic == "unknown" and mod in REVIEWED:
            lic, why = REVIEWED[mod]
            print(f"  reviewed: {mod} {ver} is {lic} ({why})")
        counts[lic] = counts.get(lic, 0) + 1
        if lic in PERMISSIVE:
            continue
        if lic in WEAK:
            print(f"  note: {mod} {ver} is {lic} (file-level copyleft; keep it unmodified)")
            continue
        print(f"  FAIL: {mod} {ver} is {lic}" + (f" ({where})" if where else " (no licence file found)"))
        bad = True
    print("platform Go dependencies: " + ", ".join(f"{n} {k}" for k, n in sorted(counts.items())))

    # phone-gui is GPL-3.0-only (D10): each of its crates needs an option
    # the GPL-3.0 can take, a permissive licence or the GPL-3.0 itself.
    if not os.path.isdir(os.path.join(FW, "gui")):
        print("phone-gui: not in this tree (the platform repository alone), skipped")
        print("LICENSE-CHECK: " + ("FAIL" if bad else "PASS"))
        return 1 if bad else 0
    with open(os.path.join(FW, "gui", "Cargo.toml")) as fh:
        expr = re.search(r'^license\s*=\s*"([^"]+)"', fh.read(), re.M)
    expr = expr.group(1) if expr else ""
    print(f"phone-gui (gui/Cargo.toml): {expr}")
    if expr != "GPL-3.0-only":
        print("  FAIL: phone-gui is GPL-3.0-only")
        bad = True
    crates = renderer_crates()
    if crates:
        known = {n: l for n, l in crates.items() if l}
        gpl = sorted(n for n, l in known.items() if not permissive_option(l) and GPL3 in l)
        blocked = sorted(n for n, l in known.items()
                         if not n.startswith("phone-gui-") and not permissive_option(l) and GPL3 not in l)
        print(f"phone-gui crates: {len(crates)} in Cargo.lock, licence read for {len(known)} "
              f"(the rest are for other platforms and not fetched here); "
              f"GPL-3.0 (Slint): {len(gpl)}; neither permissive nor GPL-3.0: {len(blocked)}")
        for n in blocked:
            print(f"  FAIL: {n} is {known[n]}")
        bad = bad or bool(blocked)

    # phone-paint is Apache-2.0 and in every image: every dependency needs a
    # permissive option, read from the sources `cargo fetch` brought for all
    # platforms (make test), so none is skipped.
    paint = renderer_crates("paint")
    if paint:
        blocked = sorted(n for n, l in paint.items() if not n.startswith("phone-paint-") and not permissive_option(l))
        print(f"own renderer (paint/): {len(paint)} crates, licence read for {sum(1 for l in paint.values() if l)}; "
              f"without a permissive option: {len(blocked)}")
        for n in blocked:
            print(f"  FAIL: {n} is {paint[n] or 'unknown'}")
        bad = bad or bool(blocked)

    print("LICENSE-CHECK: " + ("FAIL" if bad else "PASS"))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
