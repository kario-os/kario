"""Tests of scripts/ci-plan.py: synthetic changes against a synthetic package
graph (go list -json, mocked), and two checks of the rules against this
repository.

    python3 -m unittest scripts/ci_plan_test.py
"""
import importlib.util
import io
import json
import os
import re
import shutil
import subprocess
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
_spec = importlib.util.spec_from_file_location("ci_plan", os.path.join(HERE, "ci-plan.py"))
cp = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cp)

MODULE = "github.com/kario-os/kario"


def golist(*pkgs):
    """What go list -json=... prints for (dir, imports, test imports, embeds)."""
    out = []
    for d, imports, test_imports, embeds in pkgs:
        out.append(json.dumps({
            "ImportPath": MODULE if d == "." else f"{MODULE}/{d}",
            "Imports": ["fmt"] + [f"{MODULE}/{i}" for i in imports] + ["github.com/godbus/dbus/v5"],
            "TestImports": ["testing"] + [f"{MODULE}/{i}" for i in test_imports],
            "EmbedFiles": embeds,
        }, indent="\t"))
    return "\n".join(out) + "\n"


GOLIST = golist(
    ("ui", [], [], []),
    ("wire", ["ui"], [], []),
    ("svc", ["wire"], [], []),
    ("i18n", [], [], ["languages.json"]),
    ("obex", [], [], []),
    ("jsrt", ["ui", "wire"], ["jsrt/nativetest"], []),
    ("jsrt/nativetest", [], [], []),
    ("shell", ["ui", "wire", "svc", "jsrt", "i18n", "obex"], ["jsrt/nativetest"],
     ["selftest.script", "locale/zh-Hans.json", "mermaid/lovely-mermaid.iife.js"]),
    ("programs/shellcmd", ["shell"], [], ["preview.html"]),
    ("cmd/phone-shell", ["programs/shellcmd"], [], []),
    ("cmd/kario", ["jsrt", "sdk/ts", "sdk/c/include"], [], ["template/src/main.ts"]),
    ("sdk/ts", [], [], ["index.d.ts", "ui.d.ts", "node/run.mjs"]),
    ("sdk/c/include", [], [], ["kario_native.h"]),
    ("apps/chat", ["ui", "i18n"], [], ["manifest.json", "locale/zh-Hans.json"]),
    ("apps/miniapp", ["jsrt", "ui"], [], ["programs/notes/app.tsx"]),
    ("audio", [], [], []),
    ("audio/flac", ["audio"], [], []),
    ("cts", [], ["shell"], []),
    ("tools/tsgen", ["i18n"], [], []),
    ("tools/cfuzz", [], [], []),
    ("ota", [], [], []),
    ("cmd/phone-ota-delta", ["ota"], [], []),
    ("programs/mediad", ["audio"], [], []),
    ("cmd/kario-system", ["programs/mediad", "svc"], [], []),
)

TREE = {
    "go.mod": b"module github.com/kario-os/kario\n",
    "Makefile": b"all:\n",
    "chat": b"\x7fELF",
    "docs/05-implementation.md": b"# x\n",
    "docs/en/05-implementation.md": b"# x\n",
    "docs/22-developer-api.md": b"# x\n",
    "shell/core.go": b"package shell\n",
    "shell/core_test.go": b"package shell\n",
    "shell/selftest.script": b"",
    "shell/mermaid/LICENSE.lovely-mermaid": b"",
    "ui/names.go": b"package ui\n",
    "paint/src/canvas.rs": b"",
    "paint/Cargo.lock": b"",
    "gui/src/main.rs": b"",
    "telephonyd/src/mm.cpp": b"",
    "telephonyd/src/server.cpp": b"",
    "telephonyd/test/telephonyd_test.cpp": b"",
    "sdk/ts/ui.d.ts": b"",
    "sdk/ts/node/run.mjs": b"",
    "sdk/c/phoneui.c": b"",
    "ide/vscode/tsconfig.json": b"{}",
    "ide/vscode/src/extension.ts": b"",
    "apps/calc/tsconfig.json": b"{}",
    "apps/calc/src/main.ts": b"",
    "apps/chat/locale/zh-Hans.json": b"{}",
    "apps/miniapp/testdata/fuzz/FuzzProgram/82fb": b"",
    "audio/testdata/tone440.mp3": b"",
    "jsrt/src/kario.c": b"",
    "sku/skus/qemu-t9.toml": b"",
    "yocto/qemu-ota.sh": b"#!/bin/sh\n",
    "yocto/meta-phone/recipes-phone/phone-ota/files/slot-choose": b"# sourced\n",
    "yocto/meta-phone/recipes-phone/phone-ota/files/ota-test": b"#!/bin/sh\nset -eu\n",
    "tools/snap2png/go.mod": b"module x\n",
    "tools/snap2png/main.go": b"package main\n",
    "webview/nav.js": b"",
    "scripts/no-chinese.py": b"#!/usr/bin/env python3\n",
    "scripts/new-tool.sh": b"#!/usr/bin/env bash\n",
    ".github/workflows/ci.yml": b"name: platform\n",
}


class FakeRepo:
    def __init__(self, tree=TREE):
        self.tree = tree

    def files(self):
        return sorted(self.tree)

    def exists(self, path):
        return path in self.tree

    def head_bytes(self, path, n=128):
        return self.tree.get(path, b"")[:n]


def run(*paths, event="pull_request", graph="default", status="M", full=False):
    files = [p if isinstance(p, tuple) else (status, p) for p in paths]
    g = cp.GoGraph.parse(MODULE, GOLIST) if graph == "default" else graph
    return cp.plan(files, FakeRepo(), g, event, full)


def on(p):
    return {a for a, v in p.areas.items() if v}


class PlanTest(unittest.TestCase):
    def test_documents_only(self):
        p = run("docs/05-implementation.md", "docs/en/05-implementation.md")
        self.assertEqual(on(p), {"docs"})
        self.assertFalse(p.all)
        self.assertEqual(p.go_packages, [])
        self.assertEqual(json.loads(p.outputs("pull_request")["jobs"]), ["checks", "dco"])

    def test_a_document_the_shell_tests_read(self):
        p = run("docs/22-developer-api.md")
        self.assertEqual(on(p), {"docs", "go"})
        self.assertEqual(p.go_packages, ["./shell"])
        self.assertEqual(p.go_make, ["jsrt", "native-example", "telephonyd", "capi"])

    def test_renderer_only(self):
        p = run("paint/src/canvas.rs")
        self.assertEqual(on(p), {"paint", "pixel"})
        self.assertEqual(json.loads(p.outputs("pull_request")["jobs"]), ["checks", "dco", "paint", "pixel"])

    def test_lockfile_of_a_renderer_checks_the_recipe_crate_list(self):
        self.assertEqual(on(run("paint/Cargo.lock")), {"paint", "pixel", "yocto"})

    def test_shell_source_is_a_change_to_the_system(self):
        p = run("shell/core.go")
        self.assertEqual(on(p), {"go", "selftest", "cts", "pixel"})
        self.assertEqual(p.go_packages, ["./cmd/phone-shell", "./cts", "./programs/shellcmd", "./shell"])
        self.assertEqual(p.why["selftest"], ["shell/core.go"])
        self.assertEqual(p.go_make, ["jsrt", "native-example", "telephonyd", "capi"])

    def test_a_test_file_runs_its_package_only(self):
        p = run("shell/core_test.go")
        self.assertEqual(on(p), {"go"})
        self.assertEqual(p.go_packages, ["./shell"])

    def test_reverse_dependencies_and_test_imports(self):
        p = run("ui/names.go")
        # wire imports ui, svc wire, shell svc; cts only in its tests imports
        # shell; tools/tsgen reads ui's source.
        for pkg in ("./ui", "./wire", "./svc", "./jsrt", "./shell", "./cts", "./apps/chat", "./apps/miniapp",
                    "./cmd/kario", "./cmd/kario-system", "./tools/tsgen"):
            self.assertIn(pkg, p.go_packages)
        for pkg in ("./audio", "./ota", "./tools/cfuzz", "./sdk/ts"):
            self.assertNotIn(pkg, p.go_packages)
        self.assertTrue(p.areas["selftest"])

    def test_a_leaf_package(self):
        p = run("audio/flac/flac.go")
        self.assertEqual(p.go_packages, ["./audio/flac"])
        self.assertFalse(p.areas["selftest"])
        self.assertEqual(p.go_make, [])

    def test_a_deleted_package_tests_its_importers(self):
        g = cp.GoGraph.parse(MODULE, GOLIST)
        del g.pkgs["obex"]
        p = run(("D", "obex/obex.go"), graph=g)
        self.assertIn("./shell", p.go_packages)
        self.assertNotIn("./obex", p.go_packages)
        self.assertEqual(p.go_files, [])

    def test_embedded_and_nested_files(self):
        p = run("apps/chat/locale/zh-Hans.json")
        self.assertIn("./apps/chat", p.go_packages)
        self.assertTrue(p.areas["selftest"])
        p = run("shell/mermaid/LICENSE.lovely-mermaid")
        self.assertIn("./shell", p.go_packages)
        self.assertEqual(p.unknown, [])

    def test_testdata_runs_the_owner_tests_only(self):
        p = run("apps/miniapp/testdata/fuzz/FuzzProgram/82fb")
        self.assertEqual(on(p), {"go"})
        self.assertEqual(p.go_packages, ["./apps/miniapp"])

    def test_staged_testdata_is_a_change_to_the_system(self):
        p = run("audio/testdata/tone440.mp3")
        self.assertTrue(p.areas["selftest"])
        self.assertIn("./audio", p.go_packages)
        self.assertIn("./audio/flac", p.go_packages)
        self.assertIn("./shell", p.go_packages)

    def test_telephonyd(self):
        p = run("telephonyd/src/mm.cpp")
        self.assertEqual(on(p), {"telephonyd", "mm", "selftest", "cts", "pixel", "go"})
        self.assertEqual(p.go_packages, ["./shell"])
        self.assertIn("telephonyd", p.go_make)
        self.assertNotIn("mm", on(run("telephonyd/src/server.cpp")))
        self.assertEqual(on(run("telephonyd/test/telephonyd_test.cpp")), {"telephonyd"})

    def test_typescript(self):
        p = run("ide/vscode/src/extension.ts")
        self.assertEqual(on(p), {"ts"})
        self.assertEqual(p.codeql, ["javascript-typescript"])
        p = run("apps/calc/src/main.ts")
        self.assertTrue({"ts", "selftest"} <= on(p))
        self.assertIn("./cmd/kario", p.go_packages)
        p = run("sdk/ts/ui.d.ts")
        self.assertTrue({"ts", "go", "selftest"} <= on(p))
        self.assertIn("./tools/tsgen", p.go_packages)
        self.assertIn("./cmd/kario", p.go_packages)
        p = run("sdk/ts/node/run.mjs")
        self.assertIn("./jsrt", p.go_packages)

    def test_jsrt_and_capi(self):
        p = run("jsrt/src/kario.c")
        self.assertTrue({"jsrt", "go", "selftest"} <= on(p))
        self.assertEqual(p.go_make[0], "jsrt")
        self.assertIn("./apps/miniapp", p.go_packages)
        p = run("sdk/c/phoneui.c")
        self.assertTrue({"capi", "selftest"} <= on(p))
        self.assertEqual(p.go_packages, ["./shell"])

    def test_sku_and_yocto(self):
        self.assertEqual(on(run("sku/skus/qemu-t9.toml")), {"sku"})
        p = run("yocto/qemu-ota.sh")
        self.assertEqual(on(p), {"yocto"})
        self.assertEqual(p.shell_scripts, ["yocto/qemu-ota.sh"])
        p = run("yocto/meta-phone/recipes-phone/phone-ota/files/slot-choose")
        self.assertEqual(on(p), {"yocto", "go"})
        self.assertEqual(p.go_packages, ["./cmd/phone-ota-delta"])
        self.assertEqual(p.shell_scripts, [])

    def test_nested_module_and_unbuilt_directories(self):
        p = run("tools/snap2png/main.go", "webview/nav.js")
        self.assertEqual(on(p), set())
        self.assertEqual(p.go_files, ["tools/snap2png/main.go"])
        self.assertEqual(p.unknown, [])
        self.assertEqual(p.codeql, ["go", "javascript-typescript"])

    def test_build_files_run_everything(self):
        for path in ("go.mod", "go.sum", "Makefile", "scripts/no-chinese.py", ".github/workflows/ci.yml"):
            p = run(path)
            self.assertTrue(p.all, path)
            self.assertEqual(on(p), set(cp.AREAS), path)
            self.assertEqual(p.go_packages, ["./..."])
            self.assertEqual(p.infra, [path])
        p = run(".github/workflows/ci.yml")
        self.assertEqual(p.codeql, ["actions", "go", "javascript-typescript"])
        self.assertIn("yocto/qemu-ota.sh", p.shell_scripts)
        self.assertIn("shell/core.go", p.go_files)

    def test_unknown_files_run_everything(self):
        p = run("chat", "docs/05-implementation.md")
        self.assertTrue(p.all)
        self.assertEqual(p.unknown, ["chat"])
        p = run("docs/a\nb.md")
        self.assertTrue(p.all)

    def test_events_that_run_everything(self):
        for event in ("push", "schedule", "workflow_dispatch"):
            p = run(event=event)
            self.assertTrue(p.all)
            self.assertEqual(json.loads(p.outputs(event)["jobs"]),
                             ["capi", "checks", "go", "gui", "mm", "paint", "pixel", "selftest", "sku",
                              "telephonyd", "ts", "yocto"])
        self.assertTrue(run("docs/05-implementation.md", full=True).all)

    def test_without_go_list(self):
        p = run("shell/core_test.go", graph=None)
        self.assertEqual(p.go_packages, ["./..."])
        self.assertTrue(p.notes)
        self.assertTrue(run("shell/selftest.script", graph=None).all)

    def test_new_shell_scripts(self):
        p = run(("A", "scripts/new-tool.sh"), "yocto/meta-phone/recipes-phone/phone-ota/files/ota-test")
        self.assertIn("scripts/new-tool.sh", p.shell_scripts_new)
        self.assertNotIn("yocto/meta-phone/recipes-phone/phone-ota/files/ota-test", p.shell_scripts_new)
        self.assertIn("yocto/meta-phone/recipes-phone/phone-ota/files/ota-test", p.shell_scripts)

    def test_outputs(self):
        p = run("shell/core.go", "yocto/qemu-ota.sh")
        buf = io.StringIO()
        cp.write_outputs(buf, p.outputs("pull_request"))
        parsed, lines = {}, buf.getvalue().split("\n")
        i = 0
        while i < len(lines) and lines[i]:
            if "<<" in lines[i]:
                k, delim = lines[i].split("<<", 1)
                end = lines.index(delim, i + 1)
                parsed[k] = "\n".join(lines[i + 1:end])
                i = end + 1
            else:
                k, v = lines[i].split("=", 1)
                parsed[k] = v
                i += 1
        self.assertEqual(parsed["go"], "true")
        self.assertEqual(parsed["paint"], "false")
        self.assertEqual(parsed["go_packages"], "./cmd/phone-shell ./cts ./programs/shellcmd ./shell")
        self.assertEqual(parsed["go_files"], "shell/core.go")
        self.assertEqual(parsed["shell_scripts"], "yocto/qemu-ota.sh")
        self.assertEqual(json.loads(parsed["jobs"]), ["checks", "dco", "go", "pixel", "selftest", "yocto"])
        text = cp.summary(p, "pull_request", "a" * 40, "b" * 40)
        self.assertIn("| selftest | yes | `shell/core.go` |", text)
        self.assertIn("make selftest", text)

    def test_unsafe_package_paths_fall_back_to_all(self):
        g = cp.GoGraph.parse(MODULE, golist(("a b", [], [], []), ("ui", [], [], [])))
        p = run("a b/x.go", graph=g)
        self.assertEqual(p.go_packages, ["./..."])


def tracked():
    try:
        out = subprocess.run(["git", "-C", ROOT, "ls-files", "-z"], capture_output=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return None
    return [f for f in out.decode("utf-8", "surrogateescape").split("\0") if f]


def real_graph():
    if shutil.which("go") is None:
        return None
    try:
        return cp.GoGraph.load(ROOT)
    except (OSError, subprocess.CalledProcessError):
        return None


LIT = r'"((?:[^"\\\n]|\\.)*)"'


def relative_reads(src):
    """Paths a Go test names relative to its package: "../x" literals and
    filepath.Join("..", ...) of literals."""
    for m in re.finditer(r"filepath\.Join\(\s*(" + LIT + r"(?:\s*,\s*" + LIT + r")*)", src):
        parts = re.findall(LIT, m.group(1))
        if parts and parts[0] == "..":
            yield "/".join(parts)
    for m in re.finditer(LIT, src):
        if m.group(1).startswith("../"):
            yield m.group(1)


class RepositoryTest(unittest.TestCase):
    """The rules against this repository's files."""

    @classmethod
    def setUpClass(cls):
        cls.files = tracked()
        cls.graph = real_graph()
        if cls.files is None or cls.graph is None:
            raise unittest.SkipTest("needs git and go")
        cls.repo = cp.Repo(ROOT)

    def test_every_file_below_the_root_is_known(self):
        p = cp.plan([("M", f) for f in self.files], self.repo, self.graph, "pull_request")
        self.assertEqual([f for f in p.unknown if "/" in f], [])

    def test_files_a_go_test_reads_outside_its_package_are_mapped(self):
        missing = []
        for f in self.files:
            if not f.endswith("_test.go") or f.startswith("vendor/"):
                continue
            d = os.path.dirname(f)
            if d not in self.graph.pkgs:
                continue
            with open(os.path.join(ROOT, f), encoding="utf-8") as fh:
                src = fh.read()
            for ref in set(relative_reads(src)):
                target = os.path.normpath(os.path.join(d, ref))
                if target.startswith("..") or target.split("/")[0] == "build" or target == d \
                        or target.startswith(d + "/"):
                    continue
                hits = [t for t in self.files if t == target or t.startswith(target + "/")
                        or ("*" in target and re.fullmatch(re.escape(target).replace(r"\*", "[^/]*"), t))]
                for t in hits[:1]:
                    p = cp.plan([("M", t)], self.repo, self.graph, "pull_request")
                    if not p.all and f"./{d}" not in p.go_packages:
                        missing.append(f"{f} reads {ref} ({t}): plan for {t} lacks ./{d}")
        self.assertEqual(missing, [])


if __name__ == "__main__":
    unittest.main()
