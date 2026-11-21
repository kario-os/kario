#!/usr/bin/env python3
"""What CI runs for a change (.github/workflows/ci.yml, docs/38-contributing.md).

The files changed between a base and a head map to areas, each area to a
job or a step of one. The Go packages tested are those changed and every
package whose tests depend on them (go list); a change to what make build
stages for the selftest runs the selftest, the compatibility run, the
pixel selftest and the Shell's end-to-end tests. A file no rule knows, or
one of the build's own (Makefile, go.mod, go.sum, scripts/, .github/), runs
everything; so do pushes to main, the nightly run and --all.

    scripts/ci-plan.py [--base REF] [--head REF] [--event NAME] [--all]
                       [--files-from FILE] [--github-output FILE] [--summary FILE]

--base defaults to origin/main. Without --head the working tree (commits,
uncommitted changes, untracked files) is compared with the merge base.
--files-from reads "<status><TAB><path>" lines (or bare paths) instead of
asking git. Without --github-output the outputs (key=value) are printed
before the summary.
"""
import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys
import uuid

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

AREAS = ["go", "selftest", "cts", "pixel", "telephonyd", "mm", "capi", "jsrt", "paint", "gui", "ts", "sku", "yocto",
         "docs", "workflows"]
# The job of ci.yml each area needs; docs and workflows are steps of checks.
JOB = {"go": "go", "selftest": "selftest", "cts": "selftest", "pixel": "pixel", "telephonyd": "telephonyd",
       "mm": "mm", "capi": "capi", "jsrt": "go", "paint": "paint", "gui": "gui", "ts": "ts", "sku": "sku",
       "yocto": "yocto", "docs": "checks", "workflows": "checks"}
FULL_EVENTS = {"push", "schedule", "workflow_dispatch", "merge_group"}
INFRA = ("Makefile", "go.mod", "go.sum", "scripts/", ".github/")
# What make build compiles (./cmd/... ./apps/... ./cts/... ./examples/...):
# a source change in a package these link is a change to the system.
PROGRAM_ROOTS = ("cmd/", "apps/", "cts/", "examples/")
# A change to the system runs the selftest, the compatibility run, the pixel
# selftest and the Shell's end-to-end tests (they build every program and
# stage them as make build does).
SYSTEM = "system"
SYSTEM_AREAS = ("selftest", "cts", "pixel")
SYSTEM_TESTS = ("shell",)
# What make builds for the Go tests that run it (make test builds all of it
# first), and the packages whose tests do: kario-jsrt's harnesses, and the
# Shell's end-to-end stack, which stages telephonyd, the C SDK's sample app
# and the native module sample (scripts/stage.sh).
GO_MAKE = {
    "jsrt": ("jsrt", "apps/miniapp", "cmd/kario", "shell"),
    "native-example": ("shell",),
    "telephonyd": ("shell",),
    "capi": ("shell",),
}

# (pattern, areas, seeds, tests). A pattern ending in "/" is a directory, one
# with "*" a glob ("*" crosses "/"), anything else one file. Every rule a file
# matches applies. seeds are Go packages to treat as changed (their
# importers' tests run too); tests are packages whose tests read the file.
# Rules apply to files other than Go source, and to a path in testdata/ only
# when the pattern names testdata; Go files map through the package graph.
RULES = [
    ("docs/", {"docs"}),
    ("*.md", {"docs"}),
    ("LICENSE", {"docs"}),
    ("docs/22-developer-api.md", {"docs"}, (), ("shell",)),
    ("docs/23-renderer-protocol.md", {"docs"}, (), ("shell",)),
    (".gitignore", set()),
    # What scripts/stage.sh installs for the selftest besides the Go programs.
    ("brand.json", {SYSTEM}),
    ("icons/", {SYSTEM}),
    ("profile.d/", {SYSTEM}),
    ("bench/", {SYSTEM}),
    ("web/", {SYSTEM}),
    ("apps/", {SYSTEM}),
    ("examples/", {SYSTEM}),
    ("audio/testdata/", {SYSTEM}, (), ("audio/cue", "audio/flac", "audio/hifi", "audio/musicfile", "audio/opusfile",
                                       "audio/vorbisfile")),
    ("examples/mp-counter/", set(), (), ("apps/miniapp",)),
    ("examples/notes/", set(), (), ("cmd/kario",)),
    ("examples/karioui-notes/", set(), (), ("cmd/kario",)),
    ("examples/imgproc/", set(), (), ("cmd/kario",)),
    ("examples/videos/", set(), (), ("programs/mediad",)),
    ("apps/calc/", set(), (), ("cmd/kario",)),
    # kario-jsrt, the runtime of TypeScript apps (C, on QuickJS-ng).
    ("jsrt/src/", {"jsrt", SYSTEM}, ("jsrt",)),
    ("jsrt/CMakeLists.txt", {"jsrt", SYSTEM}, ("jsrt",)),
    ("jsrt/src/languages.h", set(), (), ("tools/tsgen",)),
    ("jsrt/testdata/", set(), (), ("shell",)),
    # TypeScript: @kario/sdk, the runtime under Node.js, Kario Studio.
    ("sdk/ts/", {"ts"}),
    ("sdk/ts/node/", set(), (), ("jsrt",)),
    ("sdk/ts/ui.d.ts", set(), (), ("tools/tsgen",)),
    ("sdk/ts/node/languages.mjs", set(), (), ("tools/tsgen",)),
    ("ide/", {"ts"}),
    # The C SDK (libphoneui), its C++ sample app, the native module header.
    ("sdk/c/", {"capi", SYSTEM}),
    ("sdk/c/include/", set(), (), ("jsrt", "cmd/kario")),
    ("sdk/rust/kario-native/", set(), ("jsrt/nativetest",)),
    # telephonyd (C++23) and its ModemManager backend's D-Bus test.
    ("telephonyd/src/", {"telephonyd", SYSTEM}),
    ("telephonyd/CMakeLists.txt", {"telephonyd", "mm", SYSTEM}),
    ("telephonyd/sim-network.json", {"telephonyd", SYSTEM}),
    ("telephonyd/apns.json", {"telephonyd", "mm", SYSTEM}),
    ("telephonyd/test/", {"telephonyd"}),
    ("telephonyd/src/mm*", {"mm"}),
    ("telephonyd/src/apn*", {"mm"}),
    ("telephonyd/test/mm_test.py", {"mm"}),
    ("telephonyd/test/mock_mm.py", {"mm"}),
    ("telephonyd/test/Containerfile", {"mm"}),
    # The renderers; the recipes fetch the crates their lockfiles name.
    ("paint/", {"paint", "pixel"}),
    ("gui/", {"gui"}),
    ("paint/Cargo.lock", {"yocto"}),
    ("gui/Cargo.lock", {"yocto"}),
    ("sku/", {"sku"}),
    ("tools/sku.py", {"sku"}),
    ("tools/sku_test.py", {"sku"}),
    ("tools/slots_test.py", {"sku"}),
    ("tools/phone-chroot/", {"sku"}),
    ("tools/phone_chroot_test.py", {"sku"}),
    # The Yocto layer: static checks only (no image build in CI); the update
    # applier's tests run three of its scripts.
    ("yocto/", {"yocto"}),
    ("yocto/meta-phone/dynamic-layers/rauc/recipes-phone/phone-bundle/files/", set(), (), ("cmd/phone-ota-delta",)),
    ("yocto/meta-phone/recipes-phone/phone-ota/files/slot-choose", set(), (), ("cmd/phone-ota-delta",)),
    ("tools/gomodinc.py", {"yocto"}),
    ("tools/cratesinc.py", {"yocto"}),
    # Run by hand; what they write is committed and checked where it is used.
    ("tools/icons.py", set()),
    ("tools/samples.py", set()),
    ("tools/samples.json", set()),
    ("tools/strokes-from-cnchar.py", set()),
    ("tools/imedict/", set()),
    # A module of its own (documentation snapshots), outside ./...
    ("tools/snap2png/", set()),
    # The web view builds in a container (make webview-dev), not in CI.
    ("webview/", set()),
]
# Go source that other packages' tests read as files rather than import:
# tools/tsgen writes the TypeScript types from package ui's declarations;
# cmd/kario's tests build the calendar, a TypeScript app whose directory is
# also the Go package of its fuzz target.
SOURCE_READERS = [("ui/", ("tools/tsgen",)), ("apps/calendar/", ("cmd/kario",))]

SHEBANG = re.compile(rb"^#!\s*\S*(?:/|env\s+)(?:sh|bash|dash|ksh)\b")
JS_EXT = {".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".mts", ".cts", ".html", ".htm", ".vue"}
SAFE_PKG = re.compile(r"^[A-Za-z0-9_.+~/-]+$")


def matches(pattern, path):
    if pattern.endswith("/"):
        return path.startswith(pattern)
    if "*" in pattern:
        return fnmatch.fnmatchcase(path, pattern)
    return path == pattern


def rel_of(module, importpath):
    if importpath == module:
        return "."
    if importpath.startswith(module + "/"):
        return importpath[len(module) + 1:]
    return None


class GoGraph:
    """The root module's packages (by directory): what each imports, what
    its tests import, the files it embeds; from go list -json."""

    def __init__(self, packages):
        self.pkgs = packages
        self.embeds = {}
        for d, p in packages.items():
            for e in p["embeds"]:
                self.embeds.setdefault(os.path.normpath(os.path.join(d, e)), d)

    @classmethod
    def parse(cls, module, text):
        dec, i, pkgs = json.JSONDecoder(), 0, {}
        while True:
            while i < len(text) and text[i].isspace():
                i += 1
            if i >= len(text):
                break
            obj, i = dec.raw_decode(text, i)
            d = rel_of(module, obj.get("ImportPath", ""))
            if d is None:
                continue

            def local(key):
                return {r for r in (rel_of(module, x) for x in obj.get(key) or []) if r is not None}

            pkgs[d] = {"imports": local("Imports"),
                       "test_imports": local("TestImports") | local("XTestImports"),
                       "embeds": set(sum((obj.get(k) or [] for k in ("EmbedFiles", "TestEmbedFiles",
                                                                       "XTestEmbedFiles")), []))}
        return cls(pkgs)

    @classmethod
    def load(cls, root):
        vendored = os.path.isfile(os.path.join(root, "vendor", "modules.txt"))
        env = dict(os.environ, GOPROXY="off", GOFLAGS="-mod=vendor" if vendored else "-mod=readonly")
        out = subprocess.run(["go", "list", "-e", "-json=ImportPath,Imports,TestImports,XTestImports,EmbedFiles,"
                              "TestEmbedFiles,XTestEmbedFiles", "./..."],
                             cwd=root, env=env, capture_output=True, text=True, check=True).stdout
        return cls.parse(read_module(root), out)

    def tested_by(self, seeds):
        """The packages whose tests depend on a seed: its importers, theirs
        and so on, and the packages whose tests import one of those."""
        rev = {}
        for d, p in self.pkgs.items():
            for i in p["imports"]:
                rev.setdefault(i, set()).add(d)
        reach, stack = set(seeds), list(seeds)
        while stack:
            for d in rev.get(stack.pop(), ()):
                if d not in reach:
                    reach.add(d)
                    stack.append(d)
        return {d for d, p in self.pkgs.items() if d in reach or p["test_imports"] & reach}

    def programs(self):
        """The packages make build links."""
        seen, stack = set(), [d for d in self.pkgs if d.startswith(PROGRAM_ROOTS)]
        while stack:
            d = stack.pop()
            if d not in seen:
                seen.add(d)
                stack.extend(i for i in self.pkgs[d]["imports"] if i in self.pkgs)
        return seen

    def owner(self, path, ancestors):
        """The package a file other than Go source belongs to: the one that
        embeds it or has its sources beside it; with ancestors, the closest
        package above it."""
        if path in self.embeds:
            return self.embeds[path]
        d = os.path.dirname(path) or "."
        if d in self.pkgs:
            return d
        while ancestors and d != ".":
            d = os.path.dirname(d) or "."
            if d in self.pkgs:
                return d
        return None


def read_module(root):
    with open(os.path.join(root, "go.mod"), encoding="utf-8") as f:
        for line in f:
            if line.startswith("module "):
                return line.split()[1]
    raise SystemExit("ci-plan: no module line in go.mod")


class Repo:
    """The tree planned: the working tree (head None) or a commit."""

    def __init__(self, root, head=None):
        self.root, self.head = root, head
        self._files = None
        self._set = None
        self._batch = None

    def git(self, *args):
        return subprocess.run(["git", "-C", self.root, *args], capture_output=True, check=True).stdout

    def files(self):
        if self._files is None:
            if self.head:
                out = self.git("ls-tree", "-r", "-z", "--name-only", self.head)
            else:
                out = self.git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
            self._files = sorted({f for f in out.decode("utf-8", "surrogateescape").split("\0") if f})
            self._set = set(self._files)
        return self._files

    def exists(self, path):
        if self.head:
            self.files()
            return path in self._set
        return os.path.isfile(os.path.join(self.root, path))

    def head_bytes(self, path, n=128):
        if not self.head:
            try:
                with open(os.path.join(self.root, path), "rb") as f:
                    return f.read(n)
            except OSError:
                return b""
        if self._batch is None:
            self._batch = subprocess.Popen(["git", "-C", self.root, "cat-file", "--batch"],
                                           stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        self._batch.stdin.write(f"{self.head}:{path}\n".encode("utf-8", "surrogateescape"))
        self._batch.stdin.flush()
        header = self._batch.stdout.readline().split()
        if len(header) != 3:
            return b""
        data = self._batch.stdout.read(int(header[2]) + 1)
        return data[:n] if header[1] == b"blob" else b""


def is_shell(repo, path):
    return path.endswith(".sh") or bool(SHEBANG.match(repo.head_bytes(path)))


def tsproject(repo, path):
    """A file of a TypeScript project (below a directory with a tsconfig.json)."""
    d = os.path.dirname(path)
    while d:
        if repo.exists(d + "/tsconfig.json"):
            return True
        d = os.path.dirname(d)
    return False


def changed_files(root, base, head):
    """The merge base and (status, path) of every file changed since it;
    deleted and renamed files under their old path too."""

    def git(*args):
        return subprocess.run(["git", "-C", root, *args], capture_output=True, check=True).stdout

    mb = git("merge-base", base, head or "HEAD").decode().strip()
    parts = git("diff", "--name-status", "--no-renames", "-z", mb, *([head] if head else [])) \
        .decode("utf-8", "surrogateescape").split("\0")
    out = [(parts[i][0], parts[i + 1]) for i in range(0, len(parts) - 1, 2)]
    if not head:
        untracked = git("ls-files", "-z", "--others", "--exclude-standard").decode("utf-8", "surrogateescape")
        out += [("A", f) for f in untracked.split("\0") if f]
    return mb, out


class Plan:
    def __init__(self, files):
        self.files = files
        self.all = False
        self.reason = ""
        self.areas = {a: False for a in AREAS}
        self.why = {a: [] for a in AREAS}
        self.go_packages, self.go_make, self.go_files = [], [], []
        self.shell_scripts, self.shell_scripts_new = [], []
        self.codeql, self.infra, self.unknown, self.notes = [], [], [], []

    def mark(self, area, path):
        self.areas[area] = True
        if len(self.why[area]) < 3 and path not in self.why[area]:
            self.why[area].append(path)

    def jobs(self, event):
        jobs = {"checks"} | {JOB[a] for a, on in self.areas.items() if on}
        if event == "pull_request":
            jobs.add("dco")
        return sorted(jobs)

    def outputs(self, event):
        out = {"all": str(self.all).lower()}
        out.update({a: str(on).lower() for a, on in self.areas.items()})
        out["go_packages"] = " ".join(self.go_packages)
        out["go_make"] = " ".join(self.go_make)
        out["go_files"] = "\n".join(self.go_files)
        out["shell_scripts"] = "\n".join(self.shell_scripts)
        out["shell_scripts_new"] = "\n".join(self.shell_scripts_new)
        out["codeql"] = json.dumps(self.codeql)
        out["jobs"] = json.dumps(self.jobs(event))
        return out


def codeql_language(path):
    if path.startswith("vendor/"):
        return None
    if path.endswith(".go") or path in ("go.mod", "go.sum"):
        return "go"
    if os.path.splitext(path)[1] in JS_EXT:
        return "javascript-typescript"
    if re.fullmatch(r"\.github/workflows/[^/]+\.ya?ml", path) or os.path.basename(path) in ("action.yml", "action.yaml"):
        return "actions"
    return None


def plan(files, repo, graph, event="", full=False):
    """The plan for (status, path) changes. graph is a GoGraph, or None when
    go list is not available: then any Go change tests ./..."""
    p = Plan(files)
    nested = {os.path.dirname(f) for f in repo.files() if f.endswith("/go.mod")}
    seeds, tests, system = {}, set(), []
    go_everything = False
    for status, path in files:
        if "\n" in path or "\r" in path:
            p.unknown.append(path.replace("\n", "\\n").replace("\r", "\\r"))
            continue
        infra = any(matches(i, path) for i in INFRA)
        if infra:
            p.infra.append(path)
        lang = codeql_language(path)
        if lang and lang not in p.codeql:
            p.codeql.append(lang)
        if path.startswith(".github/workflows/"):
            p.mark("workflows", path)
        if status != "D" and not path.startswith("vendor/") and is_shell(repo, path):
            p.shell_scripts.append(path)
            if status == "A":
                p.shell_scripts_new.append(path)
        parts = path.split("/")
        testdata = parts.index("testdata") if "testdata" in parts[:-1] else -1
        for prefix, readers in SOURCE_READERS:
            if path.startswith(prefix):
                tests.update(readers)
        if path.endswith(".go"):
            if status != "D" and not path.startswith("vendor/"):
                p.go_files.append(path)
            d = os.path.dirname(path) or "."
            if path.startswith("vendor/") or any(d == m or d.startswith(m + "/") for m in nested):
                continue
            p.mark("go", path)
            if graph is None:
                go_everything = True
            elif testdata >= 0:
                tests.add("/".join(parts[:testdata]) or ".")
            elif path.endswith("_test.go"):
                tests.add(d)
            else:
                seeds.setdefault(d, path)
            continue
        known = infra
        for rule in RULES:
            pattern, areas = rule[0], rule[1]
            if (testdata >= 0 and "testdata" not in pattern) or not matches(pattern, path):
                continue
            known = True
            for a in areas:
                if a == SYSTEM:
                    system.append(path)
                else:
                    p.mark(a, path)
            rseeds, rtests = (rule[2] if len(rule) > 2 else ()), (rule[3] if len(rule) > 3 else ())
            for s in rseeds:
                seeds.setdefault(s, path)
            tests.update(rtests)
            if rseeds or rtests:
                p.mark("go", path)
        if not path.startswith("vendor/") and tsproject(repo, path):
            p.mark("ts", path)
            known = True
        if graph is not None and not path.startswith("vendor/"):
            owner = graph.owner(path, ancestors=not known)
            if testdata >= 0 and owner is None:
                owner = graph.owner("/".join(parts[:testdata]) + "/x", ancestors=True)
            if owner is not None:
                known = True
                p.mark("go", path)
                if testdata >= 0:
                    tests.add(owner)
                else:
                    seeds.setdefault(owner, path)
        if not known:
            p.unknown.append(path)
    if full or event in FULL_EVENTS:
        p.reason = "--all" if full and event not in FULL_EVENTS else f"a {event} event runs everything"
    elif p.infra:
        p.reason = "the build itself changed"
    elif p.unknown:
        p.reason = "a file no rule knows changed"
    if p.reason:
        return everything(p, repo)
    if go_everything:
        p.go_packages = ["./..."]
        p.notes.append("go list was not available: every Go package is tested")
    elif graph is not None:
        programs = graph.programs()
        system += [f for d, f in sorted(seeds.items()) if d in programs]
        if system:
            tests.update(SYSTEM_TESTS)
        pkgs = (graph.tested_by(set(seeds)) | tests) & set(graph.pkgs)
        if pkgs == set(graph.pkgs):
            p.go_packages = ["./..."]
        else:
            p.go_packages = ["./" + d if d != "." else "." for d in sorted(pkgs)]
        if any(not SAFE_PKG.match(x) for x in p.go_packages):
            p.go_packages = ["./..."]
            p.notes.append("a package path with unusual characters: every Go package is tested")
        p.go_make = [t for t, users in GO_MAKE.items() if pkgs & set(users) or p.go_packages == ["./..."]]
    for f in system:
        for a in SYSTEM_AREAS:
            p.mark(a, f)
    if p.areas["jsrt"] and "jsrt" not in p.go_make:
        p.go_make.insert(0, "jsrt")
    p.areas["go"] = bool(p.go_packages)
    if not p.areas["go"]:
        p.why["go"] = []
    p.codeql.sort()
    return p


def everything(p, repo):
    p.all = True
    for a in AREAS:
        p.areas[a] = True
    p.go_packages = ["./..."]
    p.go_make = list(GO_MAKE)
    p.codeql = ["actions", "go", "javascript-typescript"]
    code = [f for f in repo.files() if not f.startswith("vendor/")]
    p.go_files = [f for f in code if f.endswith(".go")]
    p.shell_scripts = [f for f in code if is_shell(repo, f)]
    scripts = set(p.shell_scripts)
    p.shell_scripts_new = [path for status, path in p.files if status == "A" and path in scripts]
    return p


def write_outputs(f, outputs):
    for k, v in outputs.items():
        if "\n" in v:
            delim = "EOF_" + uuid.uuid4().hex
            f.write(f"{k}<<{delim}\n{v}\n{delim}\n")
        else:
            f.write(f"{k}={v}\n")


def md(s):
    return s.replace("|", "\\|").replace("`", "'").replace("<", "&lt;")


COMMANDS = {
    "go": "make {go_make}; go vet -composites=false {pkgs}; go test {pkgs}",
    "selftest": "make selftest",
    "cts": "make cts",
    "pixel": "make pixel-selftest",
    "telephonyd": "make telephonyd",
    "mm": "make mm-selftest",
    "capi": "make capi",
    "jsrt": "make jsrt (the go job builds it, and its tests must not skip)",
    "paint": "cd paint && cargo clippy --locked --all-targets && cargo test --release --locked",
    "gui": "cd gui && cargo clippy --locked --all-targets && cargo test --release --locked",
    "ts": "cd ide/vscode && npm ci && npm test; then tsc -p for each tsconfig.json project",
    "sku": "make sku-check",
    "yocto": "python3 scripts/yocto-check.py && python3 yocto/abslot-test.py && python3 yocto/fajita-flash-test.py",
    "docs": "python3 scripts/docs-links.py docs && python3 scripts/docs-en-check.py",
    "workflows": "actionlint",
}


def summary(p, event, base, head):
    span = f" ({base[:12]}...{head[:12] if head else 'working tree'})" if base else ""
    n = len(p.files)
    lines = ["### CI plan", "", f"Event: {event or 'local'}; {n} file{'' if n == 1 else 's'} changed{span}."]
    if p.all:
        lines.append(f"**Everything runs**: {p.reason}.")
        for title, items in (("build files", p.infra), ("files no rule knows", p.unknown)):
            if items:
                more = f" and {len(items) - 10} more" if len(items) > 10 else ""
                lines.append(f"- {title}: " + ", ".join(f"`{md(x)}`" for x in items[:10]) + more)
    elif not any(p.areas.values()):
        lines.append("Only the repository checks run: nothing a build or test covers changed.")
    elif [a for a, on in p.areas.items() if on] == ["docs"]:
        lines.append("Only documents changed: the repository checks run, no build or test.")
    lines += ["", "| Area | Runs | Why (a few of the files) |", "|------|------|------|"]
    for a in AREAS:
        why = "" if p.all else ", ".join(f"`{md(x)}`" for x in p.why[a])
        lines.append(f"| {a} | {'yes' if p.areas[a] else 'no'} | {why} |")
    lines.append("")
    if p.areas["go"]:
        n = "all" if p.go_packages == ["./..."] else len(p.go_packages)
        shown = " ".join(p.go_packages[:40]) + (" ..." if len(p.go_packages) > 40 else "")
        lines.append(f"Go packages ({n}): `{md(shown)}`")
        if p.go_make:
            lines.append(f"Built first for their tests: `make {' '.join(p.go_make)}`")
    if p.codeql:
        lines.append(f"CodeQL languages: {', '.join(p.codeql)}")
    lines += [f"Note: {n}" for n in p.notes]
    lines += ["", "The same on your machine (after `make vendor`):", ""]
    for a in AREAS:
        if p.areas[a]:
            cmd = COMMANDS[a].format(go_make=" ".join(p.go_make), pkgs=" ".join(p.go_packages))
            lines.append(f"- {a}: `{md(cmd)}`")
    lines.append("- always: the repository checks make test runs (scripts/no-chinese.py, i18n-check.py, "
                 "const-check.py, license-check.py, docs-links.py, docs-en-check.py), gofmt, shellcheck")
    return "\n".join(lines) + "\n"


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--base", default="origin/main")
    ap.add_argument("--head")
    ap.add_argument("--event", default="")
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--files-from")
    ap.add_argument("--github-output")
    ap.add_argument("--summary")
    ap.add_argument("--repo", default=ROOT)
    args = ap.parse_args(argv)
    repo = Repo(args.repo, args.head)
    base, files = "", []
    full = args.all
    if args.files_from:
        with open(args.files_from, encoding="utf-8") as f:
            files = [tuple(line.rstrip("\n").split("\t", 1)) if "\t" in line else ("M", line.strip())
                     for line in f if line.strip()]
    elif args.event not in FULL_EVENTS and not full:
        try:
            base, files = changed_files(args.repo, args.base, args.head)
        except subprocess.CalledProcessError as e:
            print(f"ci-plan: git failed ({e.stderr.decode(errors='replace').strip()}): planning everything",
                  file=sys.stderr)
            full = True
    graph = None
    if files and not full:
        try:
            graph = GoGraph.load(args.repo)
        except (OSError, subprocess.CalledProcessError, ValueError) as e:
            print(f"ci-plan: go list failed ({e}): every Go package is tested", file=sys.stderr)
    p = plan(files, repo, graph, args.event, full)
    outputs = p.outputs(args.event)
    text = summary(p, args.event, base, args.head)
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as f:
            write_outputs(f, outputs)
    else:
        write_outputs(sys.stdout, outputs)
        print()
    if args.summary:
        with open(args.summary, "a", encoding="utf-8") as f:
            f.write(text)
    print(text, end="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
