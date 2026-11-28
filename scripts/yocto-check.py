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
- what a recipe fetches over the network is pinned: a git repository to a
  commit (SRCREV, or SRCREV_<name>; AUTOREV is none), a download to its
  sha256sum: without it bitbake builds whatever the server has that day
  (git), or refuses at do_fetch (a download), neither of which CI sees;
- a recipe's S is where its sources unpack: a recipe of local files that
  leaves S at bitbake.conf's ${UNPACKDIR}/${BP}, which none of them makes,
  is warned of at every build, and S under WORKDIR stops it (do_qa_unpack);
- the kas files and the layer's configuration take a package's runtime
  dependencies (RDEPENDS:<package> and the like) out with :remove only:
  any other assignment sets the key a recipe's RDEPENDS:${PN}-x expands
  to, and bitbake warns at every parse that one replaces the other;
- kernels that sign their modules name the key in their configuration
  fragments, and phone-kernel.inc puts it there: the key a kernel
  generates for itself differs per build, and lockdown refused every
  module of an image whose kernel and modules the shared sstate took from
  two builds;
- the kas files have a header with a version;
- a unit that runs phone-shell kills what is left in its group when it
  stops (KillMode not process or none): the Shell's renderer left over
  keeps the display's DRM master, and the next Shell's cannot start;
- the layer's Python compiles, and shellcheck finds no error in its shell
  scripts (warnings are printed; a script without a shebang is sourced by
  sh).

    scripts/yocto-check.py [repo]
    scripts/yocto-check.py --layer <layer> [kario]

--layer checks a BSP layer of another repository (kario-bsp's
meta-kario-*, 24-sku-framework.md §8.7.3) as it builds on kario's
meta-phone: the classes and files it requires from there, the module key
its kernel's fragments name; the checks of kario's own tree (its Go
packages, crate lists, kas files) are left out.
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


FETCHED = re.compile(r"(git|gitsm|https?|ftp)://")
SRC_URI = re.compile(r'^SRC_URI(?::[\w-]+)*\s*(?:\+|\?\?|\?|:|\.)?=\s*"([^"]*)"', re.M)


def expander(text, pv=""):
    """Expands ${VAR} by the recipe's own assignments (continuation lines
    joined); what it does not assign is left as it is."""
    assigned = {"PV": pv, **dict(re.findall(r'^([A-Za-z_]\w*)\s*(?:\?\?|\?|:)?=\s*"([^"]*)"', text, re.M))}

    def expand(s):
        for _ in range(5):
            s = re.sub(r"\$\{(\w+)\}", lambda m: assigned.get(m.group(1), m.group(0)), s)
        return s
    return expand


def unpinned_sources(text, pv=""):
    """What a recipe's text (its requires joined) fetches over the network
    without pinning it: a git repository without a commit in SRCREV (or
    SRCREV_<name> for one with name=), a download without its sha256sum.
    Entries inline Python makes (phone_src) are left out, and so are the
    crate and Go module lists, which tools/cratesinc.py and gomodinc.py
    write with their checksums."""
    text = re.sub(r"\\\n", " ", text)
    assigned = {"PV": pv, **dict(re.findall(r'^([A-Za-z_]\w*)\s*(?:\?\?|\?|:)?=\s*"([^"]*)"', text, re.M))}
    expand = expander(text, pv)

    out = []
    for v in SRC_URI.findall(text):
        for entry in re.sub(r"\$\{@[^}]*\}", " ", v).split():
            url = expand(entry)
            scheme = FETCHED.match(url)
            if not scheme:
                continue
            name = dict(p.split("=", 1) for p in url.split(";")[1:] if "=" in p).get("name")
            where = url.split(";")[0]
            if scheme.group(1).startswith("git"):
                var = f"SRCREV_{name}" if name else "SRCREV"
                rev = assigned.get(var, "")
                if not re.fullmatch(r"[0-9a-f]{40}", rev):
                    out.append(f"{where}: no commit in {var}" + (f" (it is {rev})" if rev else ""))
            else:
                flag = f"{name}.sha256sum" if name else "sha256sum"
                if not re.search(rf'^SRC_URI\[{re.escape(flag)}\]\s*=\s*"[0-9a-f]{{64}}"', text, re.M):
                    out.append(f"{where}: no SRC_URI[{flag}]")
    return out


# Classes of other layers that set S themselves: meta-rauc's bundle.
SETS_S = {"bundle"}


def source_dir_problem(text, inherits=()):
    """What bitbake's do_qa_unpack says of a recipe's S, given its text (its
    requires joined) and the classes it inherits; None when nothing. S
    under WORKDIR, or ${UNPACKDIR}/git, stops the build; a recipe of local
    files (file://, inline Python's too) without S has bitbake.conf's
    ${UNPACKDIR}/${BP}, which none of them makes unless unpacked there, and
    is warned of at every build. What a download or a checkout unpacks to
    cannot be told here."""
    text = re.sub(r"\\\n", " ", text)
    s = re.findall(r'^S\s*(?:\?\?|\?|:)?=\s*"([^"]*)"', text, re.M)
    if s:
        if "${WORKDIR}" in s[-1]:
            return f'S = "{s[-1]}": S lies under UNPACKDIR now, and do_qa_unpack stops the build'
        if s[-1] == "${UNPACKDIR}/git":
            return 'S = "${UNPACKDIR}/git": bitbake.conf\'s S is the checkout now, and do_qa_unpack stops the build'
        return None
    if set(inherits) & SETS_S:
        return None
    uris = expander(text)(" ".join(SRC_URI.findall(text)))
    if "file://" not in uris or FETCHED.search(uris) or re.search(r"(?:subdir|destsuffix)=\$\{BP\}", uris):
        return None
    return ("local files and no S: S is bitbake.conf's ${UNPACKDIR}/${BP}, which none of them makes, "
            'and do_qa_unpack warns at every build (S = "${UNPACKDIR}", or where they unpack)')


RUNTIME_KEYS = ("RDEPENDS", "RRECOMMENDS", "RSUGGESTS", "RPROVIDES", "RREPLACES", "RCONFLICTS")


def package_key_assignments(text):
    """The keys of a package's runtime dependencies (RDEPENDS:<package> and
    the like) that a configuration (a kas file, the layer's conf) sets other
    than with :remove: a recipe's RDEPENDS:${PN}-x becomes that key at
    parse time, and bitbake warns at every parse that one replaces the
    other, keeping one of the two."""
    out = []
    for m in re.finditer(rf'^\s*((?:{"|".join(RUNTIME_KEYS)}):([\w.+-]+)((?::[\w-]+)*))\s*(?:\+|\?\?|\?|:|\.)?=', text, re.M):
        if m.group(2) not in ("append", "prepend", "remove") and m.group(3).split(":")[-1] != "remove":
            out.append(m.group(1))
    return out


def shell_unit_kill_mode(text):
    """A unit's KillMode when it runs phone-shell and leaves processes of
    its group running after it stops; None otherwise (systemd's default
    is control-group)."""
    if not re.search(r"^ExecStart=.*\bphone-shell\b", text, re.M):
        return None
    modes = re.findall(r"^KillMode=(\S+)", text, re.M)
    mode = modes[-1] if modes else "control-group"
    return mode if mode in ("process", "none") else None


AUTOGENERATED_KEY = "certs/signing_key.pem"


def module_key_problems(fragments, inc):
    """What is wrong with the key kernels sign their modules with, given the
    kernel configuration fragments ({name: text}) and phone-kernel.inc: the
    key the kernel generates for itself differs from build to build, and the
    shared sstate then pairs one build's kernel with another's modules,
    which lockdown refuses; a key the include does not put in place fails
    the build later."""
    signing = sorted(n for n, t in fragments.items() if re.search(r"^CONFIG_MODULE_SIG=y$", t, re.M))
    if not signing:
        return []
    keys = {}
    for n, t in fragments.items():
        for k in re.findall(r'^CONFIG_MODULE_SIG_KEY="([^"]*)"$', t, re.M):
            keys.setdefault(k, []).append(n)
    if not keys:
        return [f"{', '.join(signing)} sign modules with the key the kernel generates in each build "
                f"(no CONFIG_MODULE_SIG_KEY): the shared sstate can pair one build's kernel with another's modules"]
    out = []
    for k, names in sorted(keys.items()):
        if k == AUTOGENERATED_KEY:
            out.append(f"{', '.join(names)}: CONFIG_MODULE_SIG_KEY is {k}, the key the kernel generates in each build")
        elif k not in inc:
            out.append(f"{', '.join(names)}: CONFIG_MODULE_SIG_KEY is {k}, which phone-kernel.inc does not put in place")
    return out


KARIO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def tracked(repo, sub):
    """What a commit of the tree would hold under sub: files not added yet
    are checked too."""
    return [f for f in subprocess.run(["git", "-C", repo, "ls-files", "--cached", "--others", "--exclude-standard", sub],
                                      capture_output=True, text=True, check=True).stdout.split()
            if os.path.exists(os.path.join(repo, f))]


def main(argv):
    if argv[:1] == ["--layer"] and len(argv) in (2, 3):
        layer = os.path.abspath(argv[1])
        kario = os.path.abspath(argv[2]) if len(argv) == 3 else KARIO
        if not os.path.isfile(os.path.join(layer, "conf", "layer.conf")):
            print(f"yocto-check: {layer} is no Yocto layer (no conf/layer.conf)", file=sys.stderr)
            return 2
        repo = subprocess.run(["git", "-C", layer, "rev-parse", "--show-toplevel"],
                              capture_output=True, text=True, check=True).stdout.strip()
        files = tracked(repo, os.path.relpath(layer, repo))
    elif len(argv) <= 1 and argv[:1] != ["--layer"]:
        repo = argv[0] if argv else KARIO
        kario = None
        layer = os.path.join(repo, "yocto", "meta-phone")
        files = tracked(repo, "yocto")
    else:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    # meta-phone, under a BSP layer: BBPATH finds what it requires there.
    phone = os.path.join(kario, "yocto", "meta-phone") if kario else layer
    bad = []
    conf = open(os.path.join(layer, "conf", "layer.conf"), encoding="utf-8").read()
    globs = []
    for v in value(conf, "BBFILES") + value(conf, "BBFILES_DYNAMIC"):
        for item in v.split():
            item = item.split(":", 1)[1] if ":" in item.split("/", 1)[0] else item
            globs.append(bitbake_glob(item.replace("${LAYERDIR}/", "")))
    classes = {os.path.splitext(os.path.basename(f))[0] for f in files if f.endswith(".bbclass")}
    if kario:
        classes |= {os.path.splitext(c)[0] for c in os.listdir(os.path.join(phone, "classes-recipe")) if c.endswith(".bbclass")}
        module = None
    else:
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
            if "$" in req or ("/" in req and not kario):
                continue
            if "/" in req:
                paths = [p for p in (os.path.join(layer, req), os.path.join(phone, req)) if os.path.exists(p)]
                if paths:
                    text += "\n" + open(paths[0], encoding="utf-8").read()
                else:
                    bad.append(f"{f}: requires {req}, which neither the layer nor meta-phone has")
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
        if f.endswith((".bb", ".bbappend")):
            pv = name.removesuffix(".bb").split("_")[1] if "_" in name and f.endswith(".bb") else ""
            for p in unpinned_sources(text, pv):
                bad.append(f"{f}: {p}")
        if f.endswith(".bb"):
            lic = value(text, "LICENSE")
            if not lic:
                bad.append(f"{f}: no LICENSE")
            elif lic[0] != "CLOSED" and not inherits & {"image", "core-image"} and not value(text, "LIC_FILES_CHKSUM"):
                bad.append(f"{f}: LICENSE {lic[0]} without LIC_FILES_CHKSUM")
            problem = source_dir_problem(text, inherits)
            if problem:
                bad.append(f"{f}: {problem}")
            if module and value(text, "GO_IMPORT") == [module]:
                if tree is None:
                    tree = [p for p in subprocess.run(
                        ["git", "-C", repo, "ls-files", "--cached", "--others", "--exclude-standard"],
                        capture_output=True, text=True, check=True).stdout.split()
                        if os.path.exists(os.path.join(repo, p))]
                for d in test_only_packages(" ".join(value(text, "GO_INSTALL")), module, tree):
                    bad.append(f"{f}: GO_INSTALL names {d}, which has only tests, and go install "
                               "refuses it (give the package a file that is not a test)")
    kernel = os.path.relpath(os.path.join(layer, "recipes-kernel", "linux"), repo) + "/"
    fragments = {os.path.basename(f): open(os.path.join(repo, f), encoding="utf-8").read()
                 for f in files if f.startswith(kernel + "files/") and f.endswith(".cfg")}
    phone_kernel = os.path.join(phone, "recipes-kernel", "linux")
    if kario:
        for c in os.listdir(os.path.join(phone_kernel, "files")):
            if c.endswith(".cfg"):
                fragments.setdefault(c, open(os.path.join(phone_kernel, "files", c), encoding="utf-8").read())
    inc = open(os.path.join(phone_kernel, "phone-kernel.inc"), encoding="utf-8").read()
    for p in module_key_problems(fragments, inc):
        bad.append(f"{kernel}files/{p}")
    for crate in () if kario else ("paint", "gui"):
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
        conf_dir = os.path.relpath(os.path.join(layer, "conf"), repo) + "/"
        if (f.startswith("yocto/kas/") and f.endswith(".yml")) or (f.startswith(conf_dir) and f.endswith(".conf")):
            for key in package_key_assignments(open(os.path.join(repo, f), encoding="utf-8").read()):
                bad.append(f"{f}: {key}: a recipe's {key.split(':')[0]}:${{PN}}... expands to this key, and bitbake warns at "
                           "every parse that one replaces the other (take values out with :remove, add them in a bbappend)")
        if f.endswith(".patch") and not upstream_status(open(os.path.join(repo, f), encoding="utf-8", errors="replace").read()):
            bad.append(f"{f}: no Upstream-Status line (do_patch's QA stops the build)")
        if f.endswith(".service"):
            mode = shell_unit_kill_mode(open(os.path.join(repo, f), encoding="utf-8").read())
            if mode:
                bad.append(f"{f}: KillMode={mode} leaves the Shell's renderer running after the Shell, holding the display "
                           "(KillMode=mixed: the Shell alone is signalled and ends its apps, what is left is killed)")
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
    sys.exit(main(sys.argv[1:]))
