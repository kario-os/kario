#!/usr/bin/env python3
"""Static checks of the Yocto layer, for CI, which builds no image
(docs/38-contributing.md): what bitbake would refuse, or silently skip.

- every recipe and append lies where layer.conf's BBFILES finds it;
- every recipe has a LICENSE, and a LIC_FILES_CHKSUM unless it is an image
  or CLOSED;
- the classes inherited from this layer (phone-*) and the files a recipe
  requires beside it exist;
- the crate lists of phone-paint and phone-gui are those of their
  Cargo.lock (tools/cratesinc.py, as make yocto-src writes them);
- every Go package a recipe's GO_INSTALL names has a file besides its
  tests: go_list_packages hands go install each package by name, and go
  install refuses one of tests only (a fuzz target beside TypeScript);
- every patch in the layer says its Upstream-Status (bitbake's patch QA
  stops the build at do_patch without one);
- the kas files have a header with a version;
- the layer's Python compiles, and shellcheck finds no error in its shell
  scripts (warnings are printed; a script without a shebang is sourced by
  sh).

    scripts/yocto-check.py [repo]
"""
import os
import re
import shutil
import subprocess
import sys

SHEBANG = re.compile(rb"^#!\s*\S*(?:/|env\s+)(?:sh|bash|dash|ksh)\b")


def bitbake_glob(pattern):
    return re.compile("".join("[^/]*" if c == "*" else re.escape(c) for c in pattern) + "$")


def value(text, var):
    """The value of var's assignments, continuation lines joined."""
    text = re.sub(r"\\\n", " ", text)
    return [m.group(1) for m in re.finditer(rf'^{var}\s*(?:\+|\?\?|\?|:)?=\s*"([^"]*)"', text, re.M)]


def test_only_packages(go_install, module, files):
    """The directories of the module's packages that GO_INSTALL's patterns
    match, as go list does, and that have no Go file but tests."""
    prefixes = []
    for p in go_install.split():
        p = p.replace("${GO_IMPORT}", module)
        if p == module or p.startswith(module + "/"):
            p = p[len(module):].lstrip("/")
            prefixes.append((p.removesuffix("...").rstrip("/"), p.endswith("...")))
    modules = {os.path.dirname(f) for f in files if os.path.basename(f) == "go.mod"} - {""}
    dirs = {}
    for f in files:
        d, name = os.path.split(f)
        if not name.endswith(".go"):
            continue
        parts = d.split("/") if d else []
        if any(c in ("testdata", "vendor") or c.startswith((".", "_")) for c in parts):
            continue
        if any(d == m or d.startswith(m + "/") for m in modules):
            continue
        if not any(d == p or (tree and (p == "" or d.startswith(p + "/"))) for p, tree in prefixes):
            continue
        dirs.setdefault(d, []).append(name)
    return sorted(d for d, names in dirs.items() if all(n.endswith("_test.go") for n in names))


def upstream_status(text):
    """Whether a patch's header says its Upstream-Status, as the patch QA wants."""
    return re.search(r"^Upstream-Status: *(Pending|Submitted|Accepted|Backport|Denied|Inactive-Upstream|Inappropriate)\b", text, re.M) is not None


def main():
    repo = sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    layer = os.path.join(repo, "yocto", "meta-phone")
    # What a commit of the tree would hold: files not added yet are checked too.
    files = [f for f in subprocess.run(["git", "-C", repo, "ls-files", "--cached", "--others", "--exclude-standard", "yocto"],
                                       capture_output=True, text=True, check=True).stdout.split()
             if os.path.exists(os.path.join(repo, f))]
    bad = []
    conf = open(os.path.join(layer, "conf", "layer.conf"), encoding="utf-8").read()
    globs = []
    for v in value(conf, "BBFILES") + value(conf, "BBFILES_DYNAMIC"):
        for item in v.split():
            item = item.split(":", 1)[1] if ":" in item.split("/", 1)[0] else item
            globs.append(bitbake_glob(item.replace("${LAYERDIR}/", "")))
    classes = {os.path.splitext(os.path.basename(f))[0] for f in files if f.endswith(".bbclass")}
    module = re.search(r"^module\s+(\S+)", open(os.path.join(repo, "go.mod"), encoding="utf-8").read(), re.M).group(1)
    tree = None
    recipes = 0
    for f in files:
        rel = os.path.relpath(os.path.join(repo, f), layer)
        name = os.path.basename(f)
        if f.endswith((".bb", ".bbappend")):
            recipes += 1
            if not any(g.match(rel) for g in globs):
                bad.append(f"{f}: not where layer.conf's BBFILES looks, so bitbake never parses it")
        if not f.endswith((".bb", ".bbappend", ".bbclass", ".inc")):
            continue
        text = open(os.path.join(repo, f), encoding="utf-8").read()
        bpn = name.split("_")[0].removesuffix(".bb")
        for req in re.findall(r"^require\s+(\S+)", text, re.M):
            req = req.replace("${BPN}", bpn).replace("${PN}", bpn)
            if "/" in req or "$" in req:
                continue
            path = os.path.join(repo, os.path.dirname(f), req)
            if os.path.exists(path):
                text += "\n" + open(path, encoding="utf-8").read()
            else:
                bad.append(f"{f}: requires {req}, which is not beside it")
        inherits = set(" ".join(re.findall(r"^inherit\s+(.+)$", text, re.M)).split())
        for c in sorted(inherits):
            if c.startswith("phone-") and c not in classes:
                bad.append(f"{f}: inherits {c}, which has no {c}.bbclass in the layer")
        if f.endswith(".bb"):
            lic = value(text, "LICENSE")
            if not lic:
                bad.append(f"{f}: no LICENSE")
            elif lic[0] != "CLOSED" and not inherits & {"image", "core-image"} and not value(text, "LIC_FILES_CHKSUM"):
                bad.append(f"{f}: LICENSE {lic[0]} without LIC_FILES_CHKSUM")
            if value(text, "GO_IMPORT") == [module]:
                if tree is None:
                    tree = [p for p in subprocess.run(
                        ["git", "-C", repo, "ls-files", "--cached", "--others", "--exclude-standard"],
                        capture_output=True, text=True, check=True).stdout.split()
                        if os.path.exists(os.path.join(repo, p))]
                for d in test_only_packages(" ".join(value(text, "GO_INSTALL")), module, tree):
                    bad.append(f"{f}: GO_INSTALL names {d}, which has only tests, and go install "
                               "refuses it (give the package a file that is not a test)")
    for crate in ("paint", "gui"):
        inc = f"yocto/meta-phone/recipes-phone/phone-{crate}/phone-{crate}-crates.inc"
        want = subprocess.run([sys.executable, "tools/cratesinc.py", f"{crate}/Cargo.lock"], cwd=repo,
                              capture_output=True, text=True, check=True).stdout
        if open(os.path.join(repo, inc), encoding="utf-8").read() != want:
            bad.append(f"{inc}: not the crates of {crate}/Cargo.lock (python3 tools/cratesinc.py "
                       f"{crate}/Cargo.lock > {inc}, or make yocto-src)")
    for f in files:
        if f.startswith("yocto/kas/") and f.endswith(".yml"):
            if not re.search(r"^header:\s*\n\s+version:\s*\d+", open(os.path.join(repo, f), encoding="utf-8").read(), re.M):
                bad.append(f"{f}: no header with a version")
        if f.endswith(".patch") and not upstream_status(open(os.path.join(repo, f), encoding="utf-8", errors="replace").read()):
            bad.append(f"{f}: no Upstream-Status line (do_patch's QA stops the build)")
        if f.endswith(".py"):
            try:
                compile(open(os.path.join(repo, f), encoding="utf-8").read(), f, "exec")
            except SyntaxError as e:
                bad.append(f"{f}:{e.lineno}: {e.msg}")
    scripts = []
    for f in files:
        with open(os.path.join(repo, f), "rb") as fh:
            head = fh.read(128)
        if f.endswith(".sh") or SHEBANG.match(head):
            scripts.append((f, head.startswith(b"#!")))
    if shutil.which("shellcheck") is None:
        print(f"yocto-check: no shellcheck here: {len(scripts)} shell scripts not checked")
    else:
        for f, shebang in scripts:
            args = ["shellcheck", "--format=gcc"] + ([] if shebang else ["--shell=sh"]) + [f]
            warn = subprocess.run(args + ["--severity=warning"], cwd=repo, capture_output=True, text=True)
            for line in warn.stdout.splitlines():
                print("yocto-check: shellcheck:", line)
            if subprocess.run(args + ["--severity=error"], cwd=repo, capture_output=True).returncode != 0:
                bad.append(f"{f}: shellcheck finds errors")
    for b in bad:
        print("yocto-check:", b)
    print(f"yocto-check: {recipes} recipes and appends, {len(bad)} problems")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
