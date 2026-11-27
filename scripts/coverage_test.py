"""Tests of scripts/coverage.py's readers and the tools it picks, on small
inputs written here: Go profiles merged by block, V8's block coverage as
lines, gcov's JSON and lcov restricted to a project's sources, the LLVM
tools matching rustc's LLVM and the gcov matching the compiler; the
report's label of the tree, on a temporary repository.

    python3 -m unittest scripts/coverage_test.py
"""
import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("coverage", os.path.join(HERE, "coverage.py"))
cov = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cov)
M = cov.MODULE


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="coverage-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)

    def write(self, name, text):
        path = os.path.join(self.tmp, name)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)
        return path


class GoProfiles(Base):
    def test_blocks_add_up_and_a_block_counts_once(self):
        a = self.write("a.out", f"mode: atomic\n{M}/wire/wire.go:10.2,12.3 2 0\n{M}/wire/wire.go:13.2,14.3 3 1\n"
                                f"{M}/ui/ui.go:5.1,6.2 4 0\n")
        b = self.write("b.out", f"mode: atomic\n{M}/wire/wire.go:10.2,12.3 2 5\n{M}/wire/wire.go:13.2,14.3 3 0\n")
        blocks = cov.read_profile(b, cov.read_profile(a, {}))
        self.assertEqual(blocks[(f"{M}/wire/wire.go", "10.2,12.3")], [2, 5])
        t = cov.tally(blocks, cov.pkg_of)
        self.assertEqual(t[f"{M}/wire"], [5, 5])
        self.assertEqual(t[f"{M}/ui"], [4, 0])
        out = os.path.join(self.tmp, "m.out")
        cov.write_profile(out, blocks)
        self.assertEqual(cov.read_profile(out, {}), blocks)

    def test_report_merges_unit_and_selftest(self):
        out = os.path.join(self.tmp, "cov")
        self.write("cov/go/unit/rest.out", f"mode: atomic\n{M}/shell/broker.go:1.1,2.1 10 0\n"
                                           f"{M}/shell/core.go:1.1,2.1 30 1\n{M}/sandbox/apply_linux.go:1.1,2.1 6 0\n")
        self.write("cov/go/e2e.out", f"mode: atomic\n{M}/shell/broker.go:1.1,2.1 10 4\n")
        os.makedirs(os.path.join(out, "report"))
        with mock.patch.object(cov, "output", return_value=""):
            go = cov.go_report(out, os.path.join(out, "report"))
        self.assertEqual(go["total"]["unit"], [46, 30])
        self.assertEqual(go["total"]["e2e"], [46, 10])
        self.assertEqual(go["total"]["merged"], [46, 40])
        self.assertEqual(go["packages"]["sandbox"], {"unit": [6, 0], "e2e": [6, 0], "merged": [6, 0]})
        focus = {title: t for title, _, t in cov.focus_rows(go)}
        self.assertEqual(focus["Capability broker"]["merged"], [10, 10])
        self.assertEqual(focus["Shell"]["unit"], [40, 30])
        self.assertEqual(cov.biggest_gaps(go)[0][:2], (6, "sandbox/apply_linux.go"))

    def test_a_file_changed_between_the_runs_counts_from_the_newer(self):
        out = os.path.join(self.tmp, "cov")
        # The unit tests ran on an older usbhelper.go: its blocks sit elsewhere.
        self.write("cov/go/unit/rest.out", f"mode: atomic\n{M}/shell/usbhelper.go:10.1,12.1 5 1\n"
                                           f"{M}/shell/core.go:1.1,2.1 3 1\n")
        self.write("cov/go/e2e.out", f"mode: atomic\n{M}/shell/usbhelper.go:10.1,11.1 2 0\n"
                                     f"{M}/shell/usbhelper.go:20.1,22.1 4 1\n{M}/shell/core.go:1.1,2.1 3 0\n")
        os.makedirs(os.path.join(out, "report"))
        with mock.patch.object(cov, "output", return_value=""):
            go = cov.go_report(out, os.path.join(out, "report"), "e2e")
        self.assertEqual(go["stale"], ["shell/usbhelper.go"])
        self.assertEqual(go["files"]["shell/usbhelper.go"]["merged"], [6, 4])
        self.assertEqual(go["files"]["shell/core.go"]["merged"], [3, 3])
        with mock.patch.object(cov, "output", return_value=""):
            go = cov.go_report(out, os.path.join(out, "report"), "unit")
        self.assertEqual(go["files"]["shell/usbhelper.go"]["merged"], [5, 5])

    def test_a_side_that_did_not_run_has_no_numbers(self):
        out = os.path.join(self.tmp, "cov")
        self.write("cov/go/e2e.out", f"mode: atomic\n{M}/wire/wire.go:1.1,2.1 10 4\n")
        os.makedirs(os.path.join(out, "report"))
        with mock.patch.object(cov, "output", return_value=""):
            go = cov.go_report(out, os.path.join(out, "report"))
        self.assertEqual(cov.pct(go["packages"]["wire"]["unit"]), "-")
        self.assertEqual(cov.pct(go["total"]["unit"]), "-")
        self.assertEqual(cov.pct(go["packages"]["wire"]["e2e"]), "100.0%")

    def test_failures_named(self):
        text = "ok  \tx/a\t1s\n--- FAIL: TestB (0.1s)\nFAIL\tgithub.com/x/b\t2s\nFAIL\n"
        self.assertEqual(cov.go_test_failures(text), ["TestB", "github.com/x/b"])


class V8(unittest.TestCase):
    def test_a_line_takes_the_innermost_range_that_holds_it(self):
        src = "function f(x) {\n  if (x) {\n    return 1;\n  }\n  // done\n\n  return 2;\n}\nf(0);\n"
        block = src.index("{\n    return")
        functions = [
            {"ranges": [{"startOffset": 0, "endOffset": len(src), "count": 1}]},
            {"ranges": [{"startOffset": 0, "endOffset": src.index("}\nf(0)") + 1, "count": 1},
                        {"startOffset": block, "endOffset": src.index("  // done") - 1, "count": 0}]},
        ]
        got = cov.v8_lines(src, functions)
        self.assertEqual(got[1], 1)
        self.assertEqual(got[2], 1, "the line holds more than the branch")
        self.assertEqual(got[3], 0, "the branch not taken")
        self.assertEqual(got[4], 0)
        self.assertEqual(got[7], 1)
        self.assertEqual(got[9], 1)
        self.assertNotIn(5, got, "a comment is not code")
        self.assertNotIn(6, got, "nor a blank line")

    def test_offsets_count_utf16_units(self):
        src = 'const s = "\U0001F600";\nnever();\n'
        start = len('const s = "\U0001F600";\n'.encode("utf-16-le")) // 2
        functions = [{"ranges": [{"startOffset": 0, "endOffset": start + 8, "count": 1},
                                 {"startOffset": start, "endOffset": start + 8, "count": 0}]}]
        self.assertEqual(cov.v8_lines(src, functions), {1: 1, 2: 0})


class Readers(Base):
    def test_v8_coverage_keeps_the_sources_and_their_copies(self):
        sdk = os.path.join(self.tmp, "sdk")
        js = self.write("sdk/a.mjs", "export const a = 1;\n")
        other = self.write("app/b.mjs", "x();\n")

        def script(url, end, count=1):
            return {"url": url, "functions": [{"ranges": [{"startOffset": 0, "endOffset": end, "count": count}]}]}
        doc = {"result": [script("file://" + js, 20), script("file://" + other, 4), script("node:fs", 3),
                          # kario test's copy of the runtime, and an app's own a.mjs.
                          script("file:///tmp/kario-test-1/node/a.mjs?kario=1", 20),
                          script("file:///tmp/app/a.mjs", 7)]}
        for i in range(2):
            self.write(f"v8/coverage-{i}.json", json.dumps(doc))
        lines = {}
        with mock.patch.object(cov, "ROOT", self.tmp):
            cov.v8_coverage(os.path.join(self.tmp, "v8"), sdk, lines)
        self.assertEqual(lines, {"sdk/a.mjs": {1: 4}})

    def test_gcov_lines_leave_out_tests_and_system_headers(self):
        src = os.path.join(self.tmp, "telephonyd")
        doc = {"current_working_directory": os.path.join(self.tmp, "build"), "files": [
            {"file": os.path.join(src, "src", "json.cpp"), "lines": [{"line_number": 3, "count": 2},
                                                                     {"line_number": 4, "count": 0}]},
            {"file": os.path.join(src, "test", "telephonyd_test.cpp"), "lines": [{"line_number": 1, "count": 1}]},
            {"file": "/usr/include/c++/14/bits/vector.h", "lines": [{"line_number": 1, "count": 1}]}]}
        text = json.dumps(doc) + "\n" + json.dumps(doc) + "\n"
        lines = {}
        with mock.patch.object(cov, "ROOT", self.tmp):
            cov.gcov_lines(cov.gcov_json(text), src, lines)
        self.assertEqual(lines, {"telephonyd/src/json.cpp": {3: 4, 4: 0}})

    def test_lcov_lines(self):
        keep = os.path.join(self.tmp, "ide", "src", "a.ts")
        path = self.write("x.lcov", f"TN:\nSF:{keep}\nDA:1,3\nDA:2,0\nend_of_record\n"
                                    f"SF:{self.tmp}/ide/node_modules/m.js\nDA:1,1\nend_of_record\n"
                                    "SF:scripts/b.mjs\nDA:4,1\nend_of_record\n")
        lines = {}
        with mock.patch.object(cov, "ROOT", self.tmp):
            cov.lcov_lines(path, lines, lambda p: "node_modules" not in p, os.path.join(self.tmp, "ide"))
        self.assertEqual(lines, {"ide/src/a.ts": {1: 3, 2: 0}, "ide/scripts/b.mjs": {4: 1}})

    def test_a_file_never_loaded_counts_as_not_run(self):
        loaded = self.write("sdk/a.mjs", "a();\n")
        never = self.write("sdk/b.mjs", "// b\nb();\n\nc();\n")
        lines = {"sdk/a.mjs": {1: 1}}
        with mock.patch.object(cov, "ROOT", self.tmp):
            cov.unloaded(lines, [loaded, never])
        self.assertEqual(lines, {"sdk/a.mjs": {1: 1}, "sdk/b.mjs": {2: 0, 4: 0}})

    def test_junit_counts(self):
        path = self.write("j.xml", '<?xml version="1.0"?><testsuite tests="3"><testcase name="a"/>'
                                   '<testcase name="b"><failure message="x">x</failure></testcase>'
                                   '<testcase name="c"><skipped message="not selected"/></testcase></testsuite>')
        self.assertEqual(cov.junit_counts(path), {"cases": 3, "failed": ["b"], "skipped": 1, "passed": 1})
        self.assertIsNone(cov.junit_counts(os.path.join(self.tmp, "none.xml")))


class Tools(Base):
    def exe(self, path, text="#!/bin/sh\n"):
        p = self.write(path, text)
        os.chmod(p, 0o755)
        return p

    def rustc(self, llvm="22.1.2"):
        sysroot = os.path.join(self.tmp, "sysroot")

        def fake(cmd, **_):
            if cmd[1] == "-vV":
                return f"rustc 1.95.0\nhost: x86_64-unknown-linux-gnu\nLLVM version: {llvm}\n"
            if cmd[1:] == ["--print", "sysroot"]:
                return sysroot + "\n"
            return "LLVM version 19.1.7\n"
        return mock.patch.object(cov, "output", side_effect=fake), sysroot

    def test_the_toolchains_llvm_tools(self):
        patch, sysroot = self.rustc()
        bindir = os.path.join(sysroot, "lib", "rustlib", "x86_64-unknown-linux-gnu", "bin")
        tools = (self.exe(os.path.join(bindir, "llvm-profdata")), self.exe(os.path.join(bindir, "llvm-cov")))
        with patch, mock.patch.dict(os.environ, {"LLVM_PROFDATA": "", "LLVM_COV": ""}):
            self.assertEqual(cov.llvm_tools(), (tools, ""))

    def test_another_llvm_on_path_is_not_taken(self):
        patch, _ = self.rustc()
        path = os.path.join(self.tmp, "bin")
        self.exe(os.path.join(path, "llvm-profdata"))
        self.exe(os.path.join(path, "llvm-cov"))
        with patch, mock.patch.dict(os.environ, {"PATH": path, "LLVM_PROFDATA": "", "LLVM_COV": ""}):
            tools, why = cov.llvm_tools()
        self.assertIsNone(tools)
        self.assertIn("LLVM 22", why)
        self.assertIn("rustup component add llvm-tools", why)

    def test_the_variables_win(self):
        patch, _ = self.rustc()
        with patch, mock.patch.dict(os.environ, {"LLVM_PROFDATA": "/x/profdata", "LLVM_COV": "/x/cov"}):
            self.assertEqual(cov.llvm_tools(), (("/x/profdata", "/x/cov"), ""))

    def test_gcov_of_the_compiler(self):
        gxx = self.exe("tc/bin/g++-14")
        gcov, gcc = self.exe("tc/bin/gcov-14"), self.exe("tc/bin/gcc-14")
        self.exe("tc/bin/gcov")
        self.assertEqual(cov.gcov_for(gxx), gcov)
        self.assertEqual(cov.sibling(gxx, "gcc"), gcc)
        toolset, toolset_gcov = self.exe("ts/bin/g++"), self.exe("ts/bin/gcov")
        self.assertEqual(cov.gcov_for(toolset), toolset_gcov)
        self.assertIsNone(cov.gcov_for(self.exe("none/bin/g++")))


class TreeLabel(Base):
    """The report's label on a temporary repository with tree-note.sh where
    coverage.py looks for it, git's global and system configuration off."""

    def setUp(self):
        super().setUp()
        env = mock.patch.dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
                              GIT_CEILING_DIRECTORIES=os.path.dirname(self.tmp))
        env.start()
        self.addCleanup(env.stop)
        with open(os.path.join(cov.ROOT, "tools", "phone-chroot", "tree-note.sh"), encoding="utf-8") as f:
            self.write("tools/phone-chroot/tree-note.sh", f.read())
        self.write(".gitignore", "/build/\n")
        self.git("init", "-q")
        self.git("add", ".")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "start")
        self.head = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.run(["git", "-C", self.tmp, *args], capture_output=True, text=True, check=True).stdout

    def test_a_tree_without_changes(self):
        self.write("build/out.txt", "x\n")
        self.assertEqual(cov.tree_label(self.tmp), self.head[:12])

    def test_a_new_file_is_a_change(self):
        self.write("notes.txt", "x\n")
        self.assertEqual(cov.tree_label(self.tmp), self.head[:12] + " with changes")

    def test_outside_git(self):
        shutil.rmtree(os.path.join(self.tmp, ".git"))
        self.assertEqual(cov.tree_label(self.tmp), "")


if __name__ == "__main__":
    unittest.main()
