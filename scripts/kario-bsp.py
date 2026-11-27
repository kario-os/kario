#!/usr/bin/env python3
"""kario-bsp, the public BSP layers (24-sku-framework.md §8.7.6), at the
commit yocto/kas/phone-fajita.yml pins, for what reads its files outside a
Yocto build: tools/sku.py's checks against its machines, the tests of
make test that read its recipes.

  kario-bsp.py pin             the URL, branch, commit and layers pinned
  kario-bsp.py fetch <dir>     fetch kario-bsp into <dir> (made the first
                               time) and check the pinned commit out; fails
                               when it cannot, or when the branch no longer
                               has the commit
  kario-bsp.py layers <dir>    the pinned layers in <dir>, ':'-separated
                               (KARIO_BSP_LAYERS)

The Makefile's KARIO_BSP=<checkout> takes a checkout of one's own as it
is instead of fetching (yocto/kas/local-bsp.yml for builds).
"""
import os
import re
import subprocess
import sys

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KAS = os.path.join(FW, "yocto", "kas", "phone-fajita.yml")
NAME = "kario-bsp"
PLACEHOLDER = "0" * 40


class Bad(Exception):
    pass


def pin(kas=KAS):
    """{url, branch, commit, layers} of the kas file's repos.kario-bsp."""
    with open(kas, encoding="utf-8") as f:
        text = f.read()
    m = re.search(rf"^  {NAME}:\n((?:    .*\n|\s*\n)+)", text, re.M)
    if not m:
        raise Bad(f"{os.path.relpath(kas, FW)}: no repos.{NAME}")
    block = m.group(1)
    out = {k: (re.search(rf'^    {k}: "?([^\s"]+)"?$', block, re.M) or [None, None])[1] for k in ("url", "branch", "commit")}
    out["layers"] = re.findall(r"^      ([\w.-]+):", block, re.M)
    if not out["url"] or not out["layers"]:
        raise Bad(f"{os.path.relpath(kas, FW)}: repos.{NAME} has no url or no layers")
    if not re.fullmatch(r"[0-9a-f]{40}", out["commit"] or ""):
        raise Bad(f"{os.path.relpath(kas, FW)}: repos.{NAME}.commit is {out['commit']!r}, not a full commit")
    return out


def git(d, *args, check=True):
    r = subprocess.run(["git", "-C", d, *args], capture_output=True, text=True)
    if check and r.returncode:
        raise Bad(f"git {' '.join(args)} in {d}: {r.stderr.strip() or r.stdout.strip()}")
    return r


def fetch(d, kas=KAS):
    """kario-bsp checked out in d at the pinned commit; what it did."""
    p = pin(kas)
    where = os.path.relpath(kas, FW)
    if p["commit"] == PLACEHOLDER:
        raise Bad(f"{where} pins no {NAME} commit yet (repos.{NAME}.commit is the placeholder): "
                  f"fill it in, or give KARIO_BSP=<a checkout of {NAME}>")
    if not os.path.isdir(os.path.join(d, ".git")):
        os.makedirs(d, exist_ok=True)
        git(d, "init", "-q")
        git(d, "remote", "add", "origin", p["url"])
    elif git(d, "remote", "get-url", "origin").stdout.strip() != p["url"]:
        git(d, "remote", "set-url", "origin", p["url"])
    ref = f"refs/remotes/origin/{p['branch']}" if p["branch"] else "FETCH_HEAD"
    git(d, "fetch", "-q", "origin", f"+{p['branch']}:{ref}" if p["branch"] else p["commit"])
    if p["branch"] and git(d, "merge-base", "--is-ancestor", p["commit"], ref, check=False).returncode:
        raise Bad(f"{p['url']}: {p['branch']} has no commit {p['commit']} (published again since?): "
                  f"pin its commit in {where}")
    git(d, "checkout", "-q", "-f", "--detach", p["commit"])
    git(d, "clean", "-q", "-fdx")
    return f"{NAME} fetched at {p['commit'][:12]} into {d}"


def layers(d, kas=KAS):
    """The pinned layers' directories in d."""
    out = [os.path.join(os.path.abspath(d), l) for l in pin(kas)["layers"]]
    for l in out:
        if not os.path.isfile(os.path.join(l, "conf", "layer.conf")):
            raise Bad(f"{l}: no Yocto layer (make bsp-fetch, or KARIO_BSP=<a checkout of {NAME}>)")
    return out


def main(argv):
    try:
        if argv == ["pin"]:
            p = pin()
            print(f"{p['url']} {p['branch']} {p['commit']} {' '.join(p['layers'])}")
        elif len(argv) == 2 and argv[0] == "fetch":
            print(fetch(argv[1]))
        elif len(argv) == 2 and argv[0] == "layers":
            print(":".join(layers(argv[1])))
        else:
            print(__doc__.strip(), file=sys.stderr)
            return 2
    except Bad as e:
        print(f"kario-bsp: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
