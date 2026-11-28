#!/usr/bin/env python3
"""Code coverage of the tests (make coverage, docs/40-testing.md §7).

The parts, each skipped with the reason when a tool it needs is missing:

  go-unit  go test of every package, each test binary counting the whole
           module (-coverpkg), the shell package last as make test runs it
  go-e2e   the text selftest (phone-shell --selftest --keep-going) on the
           system built with -cover as make build stages it: the Shell,
           the services and the apps, each a process of its own, write
           their counters into GOCOVERDIR while they run (tools/covflush)
  rust     phone-paint's tests (cargo test --release) built with
           -C instrument-coverage, read with llvm-profdata and llvm-cov of
           rustc's LLVM: the toolchain's llvm-tools component, or
           LLVM_PROFDATA and LLVM_COV
  c        the tests of telephonyd (C++) and of the C SDK built with
           --coverage, read with the compiler's gcov
  ts       the Node.js runtime of TypeScript apps (sdk/ts/node) under the
           Go tests that run it (NODE_V8_COVERAGE, during go-unit), and
           Kario Studio's tests (node --test --experimental-test-coverage)
           when ide/vscode has its node_modules (npm ci)

    scripts/coverage.py [--out DIR] [--parts go-unit,go-e2e,rust,c,ts]
                        [--only REGEXP]

Go's coverage is of statements: the unit tests', the selftest's and both
merged. The others count lines. The report: DIR/summary.txt (printed at
the end), DIR/summary.md (CI's job summary), DIR/summary.json and
DIR/report/index.html, which links the merged Go profile as HTML
(go.html), phone-paint's (rust/) and the C, C++ and JavaScript sources
annotated (c/, ts/). --parts runs only those parts; the report takes the
others from DIR as an earlier run left them, with that run's date (a Go
file changed between the unit tests' run and the selftest's counts from
the newer one only, and the summary names it); --parts '' only writes the
report again. The exit status is 1 when the tests of a part that ran
failed (the report is written all the same), 2 on a usage error.
"""
import argparse
import bisect
import datetime
import glob
import html
import json
import os
import re
import shutil
import subprocess
import sys
import time
import xml.etree.ElementTree as ET

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PARTS = ("go-unit", "go-e2e", "rust", "c", "ts")
GO = os.environ.get("GO", "go")
TAGS = "kariocover"
# What make build compiles and stages.
PROGRAMS = ("./cmd/...", "./apps/...", "./cts/...", "./examples/...")
# The C and C++ projects: (name, source directory, test programs).
C_PROJECTS = (("telephonyd", "telephonyd", ("telephonyd_test",)), ("sdk/c", "sdk/c", ("phoneui_test",)))
# The areas the summary names first: (title, packages, files). A package
# ending in "/..." takes the ones below it too.
FOCUS = (
    ("Shell", ("shell",), ()),
    ("Capability broker", (), ("shell/broker.go", "shell/broker_phone.go")),
    ("Sandbox", ("sandbox", "programs/sandboxcmd"), ()),
    ("Keystore", ("keystore", "programs/keystored"), ()),
    ("Wire protocol", ("wire",), ()),
    ("Packages and signatures", ("pkg",), ()),
    ("System update", ("ota/...", "cmd/phone-ota-delta"), ()),
)
TOP = 25


def module():
    with open(os.path.join(ROOT, "go.mod"), encoding="utf-8") as f:
        for line in f:
            if line.startswith("module "):
                return line.split()[1]
    raise SystemExit("coverage: no module line in go.mod")


MODULE = module()


def log(msg):
    print(f"coverage: {msg}", flush=True)


def run(cmd, logfile, cwd=ROOT, env=None, quiet=False):
    """Runs cmd with its output on the console (unless quiet) and appended
    to logfile; its exit status and output."""
    log("$ " + " ".join(cmd))
    out = []
    with open(logfile, "a", encoding="utf-8") as lf:
        lf.write("$ " + " ".join(cmd) + "\n")
        p = subprocess.Popen(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                             errors="replace")
        for line in p.stdout:
            if not quiet:
                sys.stdout.write(line)
            lf.write(line)
            out.append(line)
        rc = p.wait()
    sys.stdout.flush()
    return rc, "".join(out)


def output(cmd, cwd=ROOT, env=None):
    return subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, check=True).stdout


def fresh(path):
    shutil.rmtree(path, ignore_errors=True)
    os.makedirs(path)
    return path


class Part:
    def __init__(self, name):
        self.name = name
        self.status = "skipped"
        self.note = ""
        self.seconds = 0.0
        self.when = ""
        self.failures = []

    def skip(self, why):
        self.status, self.note = "skipped", why
        log(f"{self.name}: skipped: {why}")
        return self

    def fail(self, why, failures=()):
        self.status, self.note = "failed", why
        self.failures = list(failures)
        log(f"{self.name}: FAILED: {why}")
        return self

    def ok(self, note=""):
        if self.status != "failed":
            self.status, self.note = "ok", note
        return self

    def as_json(self):
        return {"status": self.status, "note": self.note, "seconds": round(self.seconds), "when": self.when,
                "failures": self.failures[:50]}


# --- Go ---------------------------------------------------------------------

BLOCK = re.compile(r"^(.+):(\d+\.\d+,\d+\.\d+) (\d+) (\d+)$")


def read_profile(path, blocks):
    """Adds a text profile's blocks to blocks, {(file, range): [statements,
    count]}: the same block from several test binaries or processes adds up."""
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = BLOCK.match(line.rstrip("\n"))
            if not m:
                continue
            b = blocks.setdefault((m.group(1), m.group(2)), [int(m.group(3)), 0])
            b[1] += int(m.group(4))
    return blocks


def write_profile(path, blocks):
    with open(path, "w", encoding="utf-8") as f:
        f.write("mode: atomic\n")
        for (file, rng), (n, c) in sorted(blocks.items()):
            f.write(f"{file}:{rng} {n} {c}\n")


def tally(blocks, key):
    """Statements and covered statements by key(file)."""
    out = {}
    for (file, _), (n, c) in blocks.items():
        t = out.setdefault(key(file), [0, 0])
        t[0] += n
        if c:
            t[1] += n
    return out


def pkg_of(file):
    return os.path.dirname(file)


def rel(importpath):
    if importpath == MODULE:
        return "."
    return importpath[len(MODULE) + 1:] if importpath.startswith(MODULE + "/") else importpath


def go_test_failures(text):
    return sorted({m.group(1) for m in re.finditer(r"^(?:FAIL\t|--- FAIL: )(\S+)", text, re.M)})


def go_unit(part, out, env):
    d = fresh(os.path.join(out, "go", "unit"))
    logfile = os.path.join(out, "logs", "go-unit.log")
    pkgs = output([GO, "list", "./..."]).split()
    shell = MODULE + "/shell"
    common = [GO, "test", "-count=1", "-timeout", "30m", "-tags", TAGS, "-covermode=atomic",
              "-coverpkg=" + MODULE + "/..."]
    failed = []
    # The shell package's scenario runs its parts at once, each a stack of
    # processes: beside it the other packages' timed tests starve.
    for name, group in (("rest", [p for p in pkgs if p != shell]), ("shell", [p for p in pkgs if p == shell])):
        if not group:
            continue
        rc, text = run(common + ["-coverprofile=" + os.path.join(d, name + ".out")] + group, logfile, env=env)
        if rc:
            failed += go_test_failures(text) or [f"go test exited {rc}"]
    if failed:
        return part.fail(f"{len(failed)} failing test(s) or package(s)", failed)
    return part.ok(f"{len(pkgs)} packages")


def junit_counts(path):
    try:
        root = ET.parse(path).getroot()
    except (OSError, ET.ParseError):
        return None
    cases = root.findall("testcase")
    failed = [c.get("name", "") for c in cases if c.find("failure") is not None]
    skipped = sum(1 for c in cases if c.find("skipped") is not None)
    return {"cases": len(cases), "failed": failed, "skipped": skipped, "passed": len(cases) - len(failed) - skipped}


def go_e2e(part, out, only):
    base = os.path.join(out, "go")
    os.makedirs(base, exist_ok=True)
    profile = os.path.join(base, "e2e.out")
    if os.path.exists(profile):
        os.remove(profile)
    logfile = os.path.join(out, "logs", "go-e2e.log")
    bindir, prefix = fresh(os.path.join(base, "bin")), os.path.join(base, "usr")
    shutil.rmtree(prefix, ignore_errors=True)
    rc, _ = run([GO, "build", "-cover", "-covermode=atomic", "-coverpkg=" + MODULE + "/...", "-tags", TAGS,
                 "-o", bindir + "/", *PROGRAMS], logfile)
    if rc:
        return part.fail("the coverage build failed")
    covdir = fresh(os.path.join(base, "e2e"))
    env = dict(os.environ, GOCOVERDIR=covdir)
    rc, _ = run(["sh", "scripts/stage.sh", bindir, prefix], logfile, env=env)
    if rc:
        return part.fail("staging the coverage build failed (scripts/stage.sh)")
    junit = os.path.join(base, "selftest-junit.xml")
    if os.path.exists(junit):
        os.remove(junit)
    cmd = [os.path.join(prefix, "bin", "phone-shell"), "--selftest", "--keep-going",
           "--snap-dir", fresh(os.path.join(base, "snapshots")),
           "--baselines", "shell/testdata/snapshots", "--junit", junit]
    if only:
        cmd += ["--only", only]
    rc, _ = run(cmd, logfile, env=env)
    for leftover in glob.glob(os.path.join(covdir, ".flush-*")):
        shutil.rmtree(leftover, ignore_errors=True)
    crc, ctext = run([GO, "tool", "covdata", "textfmt", "-i=" + covdir, "-o", profile], logfile)
    if crc:
        return part.fail("go tool covdata could not read the selftest's counters: " + ctext.strip()[-200:])
    counts = junit_counts(junit)
    processes = len({f.split(".")[2] for f in os.listdir(covdir) if f.startswith("covcounters.")})
    note = f"{processes} processes"
    if counts:
        note = (f"selftest: {counts['passed']} passed, {len(counts['failed'])} failed, {counts['skipped']} skipped "
                f"of {counts['cases']} cases; " + note)
    part.note = note
    if rc:
        failed = counts["failed"] if counts else []
        return part.fail(note if counts else f"the selftest exited {rc}", failed)
    return part.ok(note)


def stale_files(unit, e2e, newer):
    """The files whose blocks differ between the two profiles: their source
    changed between the runs (PARTS= ran one of them again). Each is taken
    from the newer run only, its blocks dropped from the other."""
    def ranges(blocks):
        out = {}
        for file, rng in blocks:
            out.setdefault(file, set()).add(rng)
        return out
    u, e = ranges(unit), ranges(e2e)
    stale = sorted(f for f in u.keys() & e.keys() if u[f] != e[f])
    older = unit if newer == "e2e" else e2e
    for k in [k for k in older if k[0] in stale]:
        del older[k]
    return stale


def go_report(out, report, newer="e2e"):
    """Merges the unit tests' and the selftest's profiles; the numbers and
    the HTML of the merged one. newer is the part that ran last."""
    unit, e2e = {}, {}
    for p in glob.glob(os.path.join(out, "go", "unit", "*.out")):
        read_profile(p, unit)
    if os.path.exists(os.path.join(out, "go", "e2e.out")):
        read_profile(os.path.join(out, "go", "e2e.out"), e2e)
    if not unit and not e2e:
        return None
    stale = stale_files(unit, e2e, newer)
    merged = {}
    for blocks in (unit, e2e):
        for k, (n, c) in blocks.items():
            b = merged.setdefault(k, [n, 0])
            b[1] += c
    profile = os.path.join(out, "go", "merged.out")
    write_profile(profile, merged)
    res = {"packages": {}, "files": {}, "total": {}, "stale": [rel(f) for f in stale], "newer": newer}
    for name, blocks in (("unit", unit), ("e2e", e2e), ("merged", merged)):
        for p, (n, c) in tally(blocks, pkg_of).items():
            res["packages"].setdefault(rel(p), {})[name] = [n, c]
        for f, (n, c) in tally(blocks, lambda f: f).items():
            res["files"].setdefault(rel(f), {})[name] = [n, c]
        res["total"][name] = [sum(n for n, _ in blocks.values()), sum(n for n, c in blocks.values() if c)]
    # A package the selftest's programs do not link counts as not run there;
    # a side that did not run has no numbers.
    for k in ("packages", "files"):
        for v in res[k].values():
            for side, blocks in (("unit", unit), ("e2e", e2e)):
                v.setdefault(side, [v["merged"][0], 0] if blocks else [0, 0])
    res["total"]["e2e_linked"] = res["total"]["e2e"]
    if e2e:
        res["total"]["e2e"] = [res["total"]["merged"][0], res["total"]["e2e"][1]]
    for args, dst in ((["-html=" + profile, "-o", os.path.join(report, "go.html")], None),
                      (["-func=" + profile], os.path.join(out, "go", "func.txt"))):
        try:
            text = output([GO, "tool", "cover", *args])
            if dst:
                with open(dst, "w", encoding="utf-8") as f:
                    f.write(text)
        except subprocess.CalledProcessError as e:
            log("go tool cover " + args[0] + " failed: " + e.stderr.strip()[-300:])
    res["html"] = "go.html" if os.path.exists(os.path.join(report, "go.html")) else ""
    return res


# --- Rust -------------------------------------------------------------------

def llvm_tools():
    """llvm-profdata and llvm-cov for rustc's LLVM, or the reason there are
    none."""
    try:
        vv = output(["rustc", "-vV"])
    except (OSError, subprocess.CalledProcessError):
        return None, "no rustc"
    info = dict(line.split(": ", 1) for line in vv.splitlines() if ": " in line)
    major = info.get("LLVM version", "").split(".")[0]
    if os.environ.get("LLVM_PROFDATA") and os.environ.get("LLVM_COV"):
        return (os.environ["LLVM_PROFDATA"], os.environ["LLVM_COV"]), ""
    sysroot = output(["rustc", "--print", "sysroot"]).strip()
    tools = tuple(os.path.join(sysroot, "lib", "rustlib", info.get("host", ""), "bin", t)
                  for t in ("llvm-profdata", "llvm-cov"))
    if all(os.access(t, os.X_OK) for t in tools):
        return tools, ""
    found = tuple(shutil.which(t) for t in ("llvm-profdata", "llvm-cov"))
    if all(found):
        v = re.search(r"LLVM version (\d+)", output([found[0], "--version"]))
        if v and v.group(1) == major:
            return found, ""
    return None, (f"no llvm-profdata and llvm-cov of LLVM {major or '?'} (rustc's): "
                  "rustup component add llvm-tools, or LLVM_PROFDATA= and LLVM_COV=")


def rust(part, out, report):
    if not shutil.which("cargo"):
        return part.skip("no cargo")
    tools, why = llvm_tools()
    if not tools:
        return part.skip(why)
    profdata, llvmcov = tools
    base = os.path.join(out, "rust")
    os.makedirs(base, exist_ok=True)
    prof = fresh(os.path.join(base, "profraw"))
    logfile = os.path.join(out, "logs", "rust.log")
    env = dict(os.environ, RUSTFLAGS=(os.environ.get("RUSTFLAGS", "") + " -C instrument-coverage").strip(),
               CARGO_TARGET_DIR=os.path.join(base, "target"),
               # The crates' build scripts are instrumented too: what they
               # write while cargo builds is not the tests'.
               LLVM_PROFILE_FILE=os.path.join(fresh(os.path.join(base, "build-profraw")), "%p-%m.profraw"),
               # llvm-cov finds functions by their symbols.
               CARGO_PROFILE_RELEASE_STRIP="false", CARGO_PROFILE_RELEASE_LTO="false")
    paint = os.path.join(ROOT, "paint")
    rc, text = run(["cargo", "test", "--release", "--locked", "--no-run", "--message-format=json"], logfile,
                   cwd=paint, env=env, quiet=True)
    env["LLVM_PROFILE_FILE"] = os.path.join(prof, "paint-%p-%m.profraw")
    if rc:
        return part.fail("phone-paint's tests did not build")
    exes = []
    for line in text.splitlines():
        if line.startswith("{"):
            msg = json.loads(line)
            if msg.get("reason") == "compiler-artifact" and msg.get("profile", {}).get("test") and msg.get("executable"):
                exes.append(msg["executable"])
    trc, text = run(["cargo", "test", "--release", "--locked"], logfile, cwd=paint, env=env)
    raws = glob.glob(os.path.join(prof, "*.profraw"))
    if not raws or not exes:
        return part.fail("phone-paint's tests left no profile")
    data = os.path.join(base, "paint.profdata")
    rc, _ = run([profdata, "merge", "-sparse", *raws, "-o", data], logfile)
    if rc:
        return part.fail("llvm-profdata merge failed")
    objects = [exes[0]] + [a for e in exes[1:] for a in ("-object", e)]
    ignore = r"(/\.cargo/|/rustc/|/target/|^/usr/)"
    common = ["-instr-profile=" + data, "-ignore-filename-regex=" + ignore]
    try:
        summary = json.loads(output([llvmcov, "export", "-summary-only", *common, *objects]))["data"][0]
        output([llvmcov, "show", "-format=html", "-output-dir=" + os.path.join(report, "rust"),
                "-show-line-counts-or-regions", *common, *objects])
    except (subprocess.CalledProcessError, ValueError, KeyError, IndexError) as e:
        return part.fail(f"llvm-cov failed: {e}")
    res = {"files": {}, "html": "rust/index.html"}
    for f in summary["files"]:
        s = f["summary"]
        res["files"][os.path.relpath(f["filename"], ROOT)] = {k: [s[k]["count"], s[k]["covered"]]
                                                              for k in ("lines", "functions", "regions")}
    res["total"] = {k: [summary["totals"][k]["count"], summary["totals"][k]["covered"]]
                    for k in ("lines", "functions", "regions")}
    with open(os.path.join(base, "summary.json"), "w", encoding="utf-8") as f:
        json.dump(res, f, indent=1)
    if trc:
        return part.fail("phone-paint's tests failed", go_test_failures(text) or ["cargo test"])
    return part.ok(f"{len(exes)} test program(s)")


# --- C and C++ --------------------------------------------------------------

def sibling(compiler, tool):
    """tool of the GCC that compiler is: g++-14's gcov-14 or gcc-14, the
    toolset's beside its g++."""
    path = shutil.which(compiler) or compiler
    d, name = os.path.split(path)
    for cand in (os.path.join(d, re.sub(r"g\+\+|gcc|c\+\+", tool, name)), os.path.join(d, tool)):
        if os.access(cand, os.X_OK) and os.path.isfile(cand):
            return cand
    return None


def gcov_for(compiler):
    return sibling(compiler, "gcov")


def gcov_json(text):
    dec, i, docs = json.JSONDecoder(), 0, []
    while True:
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            return docs
        doc, i = dec.raw_decode(text, i)
        docs.append(doc)


def gcov_lines(docs, src_dir, lines):
    """Adds the line counts of gcov's JSON documents for the sources under
    src_dir (not its tests) to lines, {path: {line: count}}."""
    for doc in docs:
        cwd = doc.get("current_working_directory", "")
        for f in doc.get("files", []):
            path = os.path.normpath(os.path.join(cwd, f["file"]))
            r = os.path.relpath(path, ROOT)
            if not path.startswith(src_dir + os.sep) or "/test" in r[len(os.path.relpath(src_dir, ROOT)):]:
                continue
            per = lines.setdefault(r, {})
            for ln in f.get("lines", []):
                per[ln["line_number"]] = per.get(ln["line_number"], 0) + ln["count"]
    return lines


def c_part(part, out, report):
    cxx = os.environ.get("CXX23") or "g++"
    if not shutil.which("cmake") or not shutil.which("ninja"):
        return part.skip("no cmake or ninja")
    # One GCC for the C and the C++: its gcov reads the notes of its own
    # version only.
    gcov, cc = gcov_for(cxx), sibling(cxx, "gcc")
    if not gcov or not cc:
        return part.skip(f"no gcov and gcc beside {cxx}")
    logfile = os.path.join(out, "logs", "c.log")
    res, failed = {"projects": {}}, []
    for name, src, tests in C_PROJECTS:
        b = fresh(os.path.join(out, "c", name.replace("/", "-")))
        flags = "--coverage -O0"
        rc, _ = run(["cmake", "-S", src, "-B", b, "-G", "Ninja", "-DCMAKE_BUILD_TYPE=Debug",
                     "-DCMAKE_CXX_COMPILER=" + cxx, "-DCMAKE_C_COMPILER=" + cc, "-DCMAKE_C_FLAGS=" + flags,
                     "-DCMAKE_CXX_FLAGS=" + flags, "-DCMAKE_EXE_LINKER_FLAGS=--coverage",
                     "-DCMAKE_SHARED_LINKER_FLAGS=--coverage"], logfile)
        if rc == 0:
            rc, _ = run(["ninja", "-C", b], logfile)
        if rc:
            failed.append(f"{name}: build")
            continue
        for t in tests:
            rc, _ = run([os.path.join(b, t)], logfile, cwd=b)
            if rc:
                failed.append(f"{name}: {t}")
        lines = {}
        # Every object's notes: one no test ran has no data file, and gcov
        # counts its lines as not run.
        for gcno in glob.glob(os.path.join(b, "**", "*.gcno"), recursive=True):
            try:
                text = output([gcov, "--json-format", "--stdout", "-o", os.path.dirname(gcno), gcno], cwd=b)
            except subprocess.CalledProcessError as e:
                failed.append(f"{name}: gcov {os.path.basename(gcno)}: {e.stderr.strip()[-200:]}")
                continue
            gcov_lines(gcov_json(text), os.path.join(ROOT, src), lines)
        files = {f: [len(per), sum(1 for c in per.values() if c)] for f, per in sorted(lines.items())}
        res["projects"][name] = {"files": files, "total": [sum(v[0] for v in files.values()),
                                                           sum(v[1] for v in files.values())]}
        for f, per in lines.items():
            annotate(os.path.join(report, "c"), f, per)
    res["html"] = "c/index.html"
    annotate_index(os.path.join(report, "c"), {f: v for p in res["projects"].values() for f, v in p["files"].items()})
    with open(os.path.join(out, "c", "summary.json"), "w", encoding="utf-8") as f:
        json.dump(res, f, indent=1)
    if failed:
        return part.fail("; ".join(failed), failed)
    return part.ok(", ".join(n for n, _, _ in C_PROJECTS))


# --- TypeScript and JavaScript ----------------------------------------------

def code_line(text):
    s = text.strip()
    return bool(s) and not s.startswith(("//", "/*", "*", "*/"))


def v8_lines(source, functions):
    """Line counts of one script from V8's block coverage: each line takes
    the count of the innermost range that holds all of it."""
    starts, ends, pos = [], [], 0
    lines = source.split("\n")
    for ln in lines:
        n = len(ln.encode("utf-16-le")) // 2
        starts.append(pos)
        ends.append(pos + n)
        pos += n + 1
    counts = [None] * len(lines)
    ranges = sorted(((r["startOffset"], r["endOffset"], r["count"]) for f in functions for r in f["ranges"]),
                    key=lambda r: (r[0], -r[1]))
    for s, e, c in ranges:
        for i in range(bisect.bisect_left(starts, s), bisect.bisect_right(ends, e)):
            counts[i] = c
    return {i + 1: c for i, c in enumerate(counts) if c is not None and code_line(lines[i])}


def utf16_len(s):
    return len(s.encode("utf-16-le")) // 2


def v8_coverage(v8dir, srcdir, lines):
    """Adds the line counts of srcdir's scripts from NODE_V8_COVERAGE's files
    to lines, {path: {line: count}}. kario run, test and preview run the
    copy of them the kario command embeds (sdk/ts/embed.go), written out of
    the tree: a script elsewhere is one of them when its name is and V8's
    extent of it is that file's length."""
    sources = {}
    for p in glob.glob(os.path.join(srcdir, "*")):
        if p.endswith((".mjs", ".js", ".cjs")):
            with open(p, encoding="utf-8") as f:
                sources[p] = f.read()
    by_name = {os.path.basename(p): p for p in sources}
    for path in sorted(glob.glob(os.path.join(v8dir, "*.json"))):
        try:
            with open(path, encoding="utf-8") as f:
                doc = json.load(f)
        except (OSError, ValueError):
            continue
        for script in doc.get("result", []):
            url = script.get("url", "")
            if not url.startswith("file://"):
                continue
            file = os.path.normpath(url[len("file://"):].split("?")[0])
            functions = script.get("functions", [])
            if file not in sources:
                twin = by_name.get(os.path.basename(file))
                extent = max((r["endOffset"] for f in functions for r in f["ranges"]), default=-1)
                if not twin or extent != utf16_len(sources[twin]):
                    continue
                file = twin
            per = lines.setdefault(os.path.relpath(file, ROOT), {})
            for ln, c in v8_lines(sources[file], functions).items():
                per[ln] = per.get(ln, 0) + c
    return lines


def lcov_lines(path, lines, keep, base):
    """Adds an lcov file's line counts (DA records) of the files keep(path)
    accepts to lines; a relative path is base's."""
    cur = None
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line.startswith("SF:"):
                p = os.path.normpath(os.path.join(base, line[3:]))
                cur = lines.setdefault(os.path.relpath(p, ROOT), {}) if keep(p) else None
            elif line.startswith("DA:") and cur is not None:
                ln, c = line[3:].split(",")[:2]
                cur[int(ln)] = cur.get(int(ln), 0) + int(c)
            elif line == "end_of_record":
                cur = None
    return lines


def unloaded(lines, paths):
    """Adds the files no process loaded to lines, every line of code not run:
    Node.js reports only the scripts it loaded."""
    for p in paths:
        r = os.path.relpath(p, ROOT)
        if r in lines:
            continue
        with open(p, encoding="utf-8", errors="replace") as f:
            lines[r] = {i: 0 for i, text in enumerate(f.read().split("\n"), 1) if code_line(text)}
    return lines


def ts(part, out, report, ran_unit):
    node = shutil.which("node")
    if not node:
        return part.skip("no node")
    logfile = os.path.join(out, "logs", "ts.log")
    res, notes, failed = {"projects": {}}, [], []
    v8dir = os.path.join(out, "ts", "v8")
    sdk = {}
    node_dir = os.path.join(ROOT, "sdk", "ts", "node")
    if os.path.isdir(v8dir) and os.listdir(v8dir):
        v8_coverage(v8dir, node_dir, sdk)
    elif not ran_unit:
        notes.append("sdk/ts/node: needs go-unit")
    if sdk:
        res["projects"]["sdk/ts/node"] = unloaded(sdk, sorted(glob.glob(os.path.join(node_dir, "*.mjs")) +
                                                              glob.glob(os.path.join(node_dir, "*.js"))))
    vscode = os.path.join(ROOT, "ide", "vscode")
    if os.path.isdir(os.path.join(vscode, "node_modules")):
        lcov = os.path.join(out, "ts", "vscode.lcov")
        rc, _ = run([os.path.join(vscode, "node_modules", ".bin", "tsc"), "-p", "."], logfile, cwd=vscode)
        if rc == 0:
            tests = sorted(glob.glob(os.path.join(vscode, "out", "test", "*.test.js")) +
                           glob.glob(os.path.join(vscode, "scripts", "*.test.mjs")))
            rc, _ = run([node, "--enable-source-maps", "--test", "--experimental-test-coverage",
                         "--test-reporter=spec", "--test-reporter-destination=stdout",
                         "--test-reporter=lcov", "--test-reporter-destination=" + lcov, *tests], logfile, cwd=vscode)
        if rc:
            failed.append("ide/vscode: npm test")
        if os.path.exists(lcov):
            studio = {}
            lcov_lines(lcov, studio, lambda p: p.startswith(vscode + os.sep) and "node_modules" not in p
                       and not re.search(r"\.test\.[mc]?[jt]s$", p) and os.sep + "test" + os.sep not in p, vscode)
            # The extension's sources and the packaging script; e2e.mjs
            # drives VS Code (npm run e2e), a test.
            res["projects"]["ide/vscode"] = unloaded(studio, sorted(
                glob.glob(os.path.join(vscode, "src", "*.ts")) + [os.path.join(vscode, "scripts", "vsix.mjs")]))
    else:
        notes.append("ide/vscode: no node_modules (cd ide/vscode && npm ci)")
    if not res["projects"]:
        return part.skip("; ".join(notes) or "nothing ran under Node.js")
    out_projects = {}
    for name, lines in res["projects"].items():
        files = {f: [len(per), sum(1 for c in per.values() if c)] for f, per in sorted(lines.items())}
        out_projects[name] = {"files": files, "total": [sum(v[0] for v in files.values()),
                                                       sum(v[1] for v in files.values())]}
        for f, per in lines.items():
            annotate(os.path.join(report, "ts"), f, per)
    annotate_index(os.path.join(report, "ts"), {f: v for p in out_projects.values() for f, v in p["files"].items()})
    summary = {"projects": out_projects, "html": "ts/index.html"}
    with open(os.path.join(out, "ts", "summary.json"), "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=1)
    if failed:
        return part.fail("; ".join(failed), failed)
    return part.ok("; ".join(notes))


# --- Annotated sources ------------------------------------------------------

STYLE = ("body{font:13px sans-serif;margin:1em}table{border-collapse:collapse}td,th{padding:1px 8px;"
         "text-align:right}td:first-child,th:first-child{text-align:left}pre{margin:0;font:12px monospace}"
         ".hit{background:#dfd}.miss{background:#fdd}.n{color:#888;text-align:right;padding-right:6px}")


def page_name(path):
    return path.replace("/", "_") + ".html"


def annotate(d, path, per):
    """One source file with its lines marked run or not."""
    os.makedirs(d, exist_ok=True)
    try:
        with open(os.path.join(ROOT, path), encoding="utf-8", errors="replace") as f:
            src = f.read().split("\n")
    except OSError:
        return
    rows = []
    for i, text in enumerate(src, 1):
        c = per.get(i)
        cls = "" if c is None else ("hit" if c else "miss")
        rows.append(f'<tr class="{cls}"><td class="n">{i}</td><td class="n">{"" if c is None else c}</td>'
                    f"<td><pre>{html.escape(text)}</pre></td></tr>")
    with open(os.path.join(d, page_name(path)), "w", encoding="utf-8") as f:
        f.write(f"<!doctype html><meta charset=utf-8><title>{html.escape(path)}</title><style>{STYLE}</style>"
                f"<h1>{html.escape(path)}</h1><table>{''.join(rows)}</table>\n")


def annotate_index(d, files):
    os.makedirs(d, exist_ok=True)
    rows = "".join(f'<tr><td><a href="{page_name(p)}">{html.escape(p)}</a></td><td>{pct(v)}</td><td>{v[1]}/{v[0]}'
                   f"</td></tr>" for p, v in sorted(files.items()))
    with open(os.path.join(d, "index.html"), "w", encoding="utf-8") as f:
        f.write(f"<!doctype html><meta charset=utf-8><title>Coverage</title><style>{STYLE}</style>"
                f"<table><tr><th>File</th><th>Lines</th><th>Run/all</th></tr>{rows}</table>\n")


# --- The report -------------------------------------------------------------

def pct(v):
    return f"{100 * v[1] / v[0]:.1f}%" if v and v[0] else "-"


def focus_rows(go):
    rows = []
    for title, pkgs, files in FOCUS:
        t = {"unit": [0, 0], "e2e": [0, 0], "merged": [0, 0]}
        for name, v in (go["files"] if files else go["packages"]).items():
            if files:
                hit = name in files
            else:
                hit = any(name == p or (p.endswith("/...") and (name == p[:-4] or name.startswith(p[:-3])))
                          for p in pkgs)
            if hit:
                for k in t:
                    t[k] = [t[k][0] + v[k][0], t[k][1] + v[k][1]]
        what = ", ".join(files or pkgs)
        rows.append((title, what, t))
    return rows


def biggest_gaps(go, n=TOP):
    gaps = [(v["merged"][0] - v["merged"][1], f, v["merged"]) for f, v in go["files"].items()]
    return sorted(gaps, reverse=True)[:n]


def tree_label(root=ROOT):
    """The tree's commit, "with changes" when tools/phone-chroot/tree-note.sh
    finds any (a new file counts), or "" outside git."""
    try:
        first = output(["sh", os.path.join(root, "tools", "phone-chroot", "tree-note.sh")]).split("\n", 1)[0]
    except (OSError, subprocess.CalledProcessError):
        return ""
    m = re.fullmatch(r"([0-9a-f]+), (no changes|with changes:)", first)
    if not m:
        return ""
    return m.group(1)[:12] + ("" if m.group(2) == "no changes" else " with changes")


def summarize(out, parts, go, rust_res, c_res, ts_res):
    rev = tree_label()
    now = datetime.datetime.now().astimezone().strftime("%Y-%m-%d %H:%M %Z")
    txt = [f"Coverage of {rev or 'this tree'}, {now}", ""]
    md = ["### Coverage", "", f"Of {rev or 'this tree'}, {now}.", ""]
    txt.append("Parts:")
    md += ["| Part | Result | Minutes | Run | Note |", "|------|--------|---------|-----|------|"]
    for name in PARTS:
        p = parts[name]
        txt.append(f"  {name:8} {p['status']:8} {p['seconds'] / 60:5.1f} min  {p['when']:16}  {p['note']}")
        md.append(f"| {name} | {p['status']} | {p['seconds'] / 60:.1f} | {p['when']} | {mdq(p['note'])} |")
        for f in p.get("failures", [])[:10]:
            txt.append(f"           failed: {f}")
    txt.append("")
    md.append("")
    if go:
        tot = go["total"]
        txt += [f"{'Go, statements run':26} unit tests  selftest  merged  statements",
                f"  {'total':24}{pct(tot['unit']):>10}  {pct(tot['e2e']):>8}  {pct(tot['merged']):>6}"
                f"  {tot['merged'][0]:>10}"]
        md += ["#### Go (statements)", "", "| | Unit tests | Selftest | Merged | Statements |",
               "|---|---|---|---|---|",
               f"| **total** | {pct(tot['unit'])} | {pct(tot['e2e'])} | **{pct(tot['merged'])}** | {tot['merged'][0]} |"]
        for title, what, t in focus_rows(go):
            txt.append(f"  {title:24}{pct(t['unit']):>10}  {pct(t['e2e']):>8}  {pct(t['merged']):>6}"
                       f"  {t['merged'][0]:>10}  ({what})")
            md.append(f"| {title} ({mdq(what)}) | {pct(t['unit'])} | {pct(t['e2e'])} | {pct(t['merged'])} | "
                      f"{t['merged'][0]} |")
        if go.get("stale"):
            note = (f"{len(go['stale'])} file(s) changed between the unit tests' run and the selftest's, counted "
                    f"from the newer ({'selftest' if go['newer'] == 'e2e' else 'unit tests'}) only: "
                    + ", ".join(go["stale"][:10]) + (" ..." if len(go["stale"]) > 10 else ""))
            txt += ["", "Note: " + note]
            md += ["", "**Note**: " + mdq(note)]
        txt += ["", f"Go, the {TOP} files with the most statements no test runs (merged):"]
        md += ["", f"<details><summary>The {TOP} Go files with the most statements no test runs</summary>", "",
               "| File | Not run | Merged |", "|---|---|---|"]
        for miss, f, v in biggest_gaps(go):
            txt.append(f"  {miss:6}  {pct(v):>6}  {f}")
            md.append(f"| {mdq(f)} | {miss} | {pct(v)} |")
        md += ["", "</details>", ""]
        txt += ["", f"{'Go packages':39} unit tests  selftest  merged  statements"]
        md += ["<details><summary>Every Go package</summary>", "",
               "| Package | Unit tests | Selftest | Merged | Statements |", "|---|---|---|---|---|"]
        for p, v in sorted(go["packages"].items()):
            txt.append(f"  {p:38}{pct(v['unit']):>10}  {pct(v['e2e']):>8}  {pct(v['merged']):>6}  "
                       f"{v['merged'][0]:>10}")
            md.append(f"| {mdq(p)} | {pct(v['unit'])} | {pct(v['e2e'])} | {pct(v['merged'])} | {v['merged'][0]} |")
        md += ["", "</details>", ""]
        txt.append("")
    if rust_res:
        t = rust_res["total"]
        txt.append(f"Rust, phone-paint's tests: lines {pct(t['lines'])} of {t['lines'][0]}, functions "
                   f"{pct(t['functions'])}, regions {pct(t['regions'])}")
        md += ["#### Rust: phone-paint's tests", "", "| File | Lines | Functions | Regions |", "|---|---|---|---|",
               f"| **total** | **{pct(t['lines'])}** of {t['lines'][0]} | {pct(t['functions'])} | "
               f"{pct(t['regions'])} |"]
        for f, v in sorted(rust_res["files"].items(), key=lambda kv: kv[1]["lines"][1] - kv[1]["lines"][0]):
            txt.append(f"  {pct(v['lines']):>6} of {v['lines'][0]:>5} lines  {f}")
            md.append(f"| {mdq(f)} | {pct(v['lines'])} of {v['lines'][0]} | {pct(v['functions'])} | "
                      f"{pct(v['regions'])} |")
        txt.append("")
        md.append("")
    for title, res in (("C and C++, their tests", c_res), ("JavaScript and TypeScript, under Node.js", ts_res)):
        if not res:
            continue
        md += [f"#### {title} (lines)", "", "| File | Lines |", "|---|---|"]
        for name, proj in res["projects"].items():
            txt.append(f"{title}: {name}: lines {pct(proj['total'])} of {proj['total'][0]}")
            md.append(f"| **{mdq(name)}** | **{pct(proj['total'])}** of {proj['total'][0]} |")
            for f, v in sorted(proj["files"].items(), key=lambda kv: kv[1][1] - kv[1][0]):
                txt.append(f"  {pct(v):>6} of {v[0]:>5} lines  {f}")
                md.append(f"| {mdq(f)} | {pct(v)} of {v[0]} |")
        txt.append("")
        md.append("")
    report = os.path.join(out, "report")
    txt.append(f"The report: {os.path.relpath(os.path.join(report, 'index.html'), ROOT)}")
    with open(os.path.join(out, "summary.txt"), "w", encoding="utf-8") as f:
        f.write("\n".join(txt) + "\n")
    with open(os.path.join(out, "summary.md"), "w", encoding="utf-8") as f:
        f.write("\n".join(md) + "\n")
    with open(os.path.join(out, "summary.json"), "w", encoding="utf-8") as f:
        json.dump({"rev": rev, "date": now, "parts": parts, "go": go and {k: go[k] for k in ("total", "packages", "stale")},
                   "rust": rust_res, "c": c_res, "ts": ts_res}, f, indent=1)
    links = [(n, h) for n, h in (("Go, merged (go tool cover)", go and go.get("html")),
                                 ("Rust, phone-paint (llvm-cov)", rust_res and rust_res.get("html")),
                                 ("C and C++", c_res and c_res.get("html")),
                                 ("JavaScript and TypeScript", ts_res and ts_res.get("html"))) if h]
    with open(os.path.join(report, "index.html"), "w", encoding="utf-8") as f:
        f.write(f"<!doctype html><meta charset=utf-8><title>Kario coverage</title><style>{STYLE}</style><ul>"
                + "".join(f'<li><a href="{h}">{html.escape(n)}</a>' for n, h in links)
                + f"</ul><pre>{html.escape(chr(10).join(txt))}</pre>\n")
    return "\n".join(txt) + "\n"


def mdq(s):
    return str(s).replace("|", "\\|").replace("<", "&lt;")


def load(path):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--out", default=os.path.join(ROOT, "build", "coverage"))
    ap.add_argument("--parts", default=",".join(PARTS))
    ap.add_argument("--only", default="", help="the selftest's cases whose name matches (phone-shell --only)")
    args = ap.parse_args(argv)
    want = [p.strip() for p in args.parts.replace(" ", ",").split(",") if p.strip()]
    bad = [p for p in want if p not in PARTS]
    if bad:
        print(f"coverage: unknown part {', '.join(bad)} (parts: {', '.join(PARTS)})", file=sys.stderr)
        return 2
    out = os.path.abspath(args.out)
    os.makedirs(os.path.join(out, "logs"), exist_ok=True)
    report = os.path.join(out, "report")
    os.makedirs(report, exist_ok=True)
    state = os.path.join(out, "parts.json")
    parts = load(state) or {}
    for name in PARTS:
        parts.setdefault(name, {"status": "not run", "note": "", "seconds": 0, "when": "", "failures": []})
    if "go-unit" in want or "go-e2e" in want:
        if not shutil.which(GO):
            for name in ("go-unit", "go-e2e"):
                if name in want:
                    parts[name] = Part(name).skip(f"no {GO}").as_json()
            want = [p for p in want if p not in ("go-unit", "go-e2e")]
    failed = False
    for name in PARTS:
        if name not in want:
            continue
        for f in glob.glob(os.path.join(out, "logs", name + ".log")):
            os.remove(f)
        part, start = Part(name), time.time()
        part.when = datetime.datetime.now().strftime("%Y-%m-%d %H:%M")
        log(f"{name}: start")
        if name == "go-unit":
            env = dict(os.environ)
            if shutil.which("node") and "ts" in want:
                env["NODE_V8_COVERAGE"] = fresh(os.path.join(out, "ts", "v8"))
            go_unit(part, out, env)
        elif name == "go-e2e":
            go_e2e(part, out, args.only)
        elif name == "rust":
            shutil.rmtree(os.path.join(report, "rust"), ignore_errors=True)
            rust(part, out, report)
        elif name == "c":
            shutil.rmtree(os.path.join(report, "c"), ignore_errors=True)
            c_part(part, out, report)
        elif name == "ts":
            shutil.rmtree(os.path.join(report, "ts"), ignore_errors=True)
            ts(part, out, report, "go-unit" in want)
        part.seconds = time.time() - start
        log(f"{name}: {part.status} in {part.seconds / 60:.1f} min{': ' + part.note if part.note else ''}")
        failed |= part.status == "failed"
        parts[name] = part.as_json()
        with open(state, "w", encoding="utf-8") as f:
            json.dump(parts, f, indent=1)
    newer = "go-unit" if parts["go-unit"]["when"] > parts["go-e2e"]["when"] else "go-e2e"
    go = go_report(out, report, newer[3:])

    def kept(name, path):
        return load(path) if parts[name]["status"] != "skipped" else None

    text = summarize(out, parts, go, kept("rust", os.path.join(out, "rust", "summary.json")),
                     kept("c", os.path.join(out, "c", "summary.json")), kept("ts", os.path.join(out, "ts", "summary.json")))
    print(text, end="")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
