"""Tests of tools/gomodinc.py, run as a program in a copy of it (with
scripts/license-check.py) beside a small go.mod and go.sum, and a stand-in
go command that logs its calls and fills the module cache as the real one
would: what go mod tidy rearranges in go.mod does not make the lists stale,
what changes the modules does; the lists are written with the local Go
only, which must be as new as go.mod's go line; --ensure writes stale lists
again, and says why when it cannot; --rev compares with a commit's files.

    python3 -m unittest tools/gomodinc_test.py
"""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
RECIPE = "recipe"

FAKE_GO = r'''#!/usr/bin/env python3
import os, sys
args = sys.argv[1:]
with open(os.environ["FAKE_GO_LOG"], "a") as f:
    f.write("%s GOTOOLCHAIN=%s\n" % (" ".join(args), os.environ.get("GOTOOLCHAIN", "")))
if args == ["env", "GOVERSION"]:
    print(os.environ.get("FAKE_GO_VERSION", "go1.25.0"))
elif args[:2] == ["list", "-deps"]:
    if os.environ.get("FAKE_GO_OFFLINE"):
        sys.exit('go: example.com/m@v1.0.0: Get "https://proxy.golang.org/example.com/m/@v/v1.0.0.zip": '
                 "dial tcp: lookup proxy.golang.org: no such host")
    dl = os.path.join(os.environ["GOMODCACHE"], "cache", "download")
    def put(path, data):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(data)
    put(os.path.join(dl, "example.com", "m", "@v", "v1.0.0.zip"), "zip")
    put(os.path.join(dl, "example.com", "b", "@v", "v1.2.0.mod"), "module example.com/b\n")
    # GOTOOLCHAIN=auto with a Go older than go.mod's go line: the go command
    # fetches the newer toolchain into the module cache, as a module.
    if os.environ.get("GOTOOLCHAIN") != "local" and os.environ.get("FAKE_GO_VERSION", "go1.25.0") < "go1.25.0":
        put(os.path.join(dl, "golang.org", "toolchain", "@v", "v0.0.1-go1.25.0.linux-amd64.zip"), "toolchain")
'''

GOMOD = """module example.com/phone

go 1.25.0

require (
	example.com/a v1.0.0
	example.com/m v1.0.0
)

require example.com/b v1.2.0 // indirect
"""

# What go mod tidy (and a newer go command) may make of the same file.
GOMOD_TIDY = """// The phone.
module example.com/phone

go 1.25.0

toolchain go1.25.4

godebug default=go1.25

require (
	example.com/m v1.0.0 // a comment
	example.com/b v1.2.0
)

require example.com/a v1.0.0 // indirect
"""

GOSUM = """example.com/a v1.0.0 h1:aaaa=
example.com/a v1.0.0/go.mod h1:bbbb=
example.com/b v1.2.0/go.mod h1:cccc=
example.com/m v1.0.0 h1:dddd=
example.com/m v1.0.0/go.mod h1:eeee=
"""


class Tree(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="gomodinc-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.tree = os.path.join(self.tmp, "tree")
        for d in ("tools", "scripts", RECIPE):
            os.makedirs(os.path.join(self.tree, d))
        shutil.copy(os.path.join(HERE, "gomodinc.py"), os.path.join(self.tree, "tools"))
        shutil.copy(os.path.join(HERE, "..", "scripts", "license-check.py"), os.path.join(self.tree, "scripts"))
        self.write("go.mod", GOMOD)
        self.write("go.sum", GOSUM)
        bindir = os.path.join(self.tmp, "bin")
        os.makedirs(bindir)
        go = os.path.join(bindir, "go")
        with open(go, "w") as f:
            f.write(FAKE_GO)
        os.chmod(go, 0o755)
        self.log = os.path.join(self.tmp, "go.log")
        self.env = {k: v for k, v in os.environ.items() if k not in ("GO", "GOTOOLCHAIN", "GOFLAGS")}
        self.env.update(PATH=bindir + os.pathsep + os.environ["PATH"], FAKE_GO_LOG=self.log)

    def write(self, name, text):
        with open(os.path.join(self.tree, name), "w") as f:
            f.write(text)

    def read(self, name):
        try:
            with open(os.path.join(self.tree, name)) as f:
                return f.read()
        except FileNotFoundError:
            return None

    def calls(self):
        return self.read(os.path.relpath(self.log, self.tree)) or ""

    def run_tool(self, *args, **env):
        return subprocess.run([sys.executable, os.path.join(self.tree, "tools", "gomodinc.py"), *args],
                              cwd=self.tree, env=dict(self.env, **env), capture_output=True, text=True)

    def generate(self, **env):
        return self.run_tool(RECIPE, "phone-fw", "./cmd/...", **env)

    def checks(self, *rev):
        return self.run_tool("--check", *rev, RECIPE, "phone-fw").returncode == 0

    def mods(self):
        return self.read(os.path.join(RECIPE, "phone-fw-go-mods.inc"))


class Digest(Tree):
    def test_what_go_mod_tidy_rearranges_keeps_the_lists(self):
        self.assertEqual(self.generate().returncode, 0)
        self.assertTrue(self.checks())
        self.write("go.mod", GOMOD_TIDY)
        self.assertTrue(self.checks(), "comments, // indirect, toolchain, godebug and the order are not the modules")

    def test_what_changes_the_modules_makes_them_stale(self):
        self.assertEqual(self.generate().returncode, 0)
        for name, text in [
            ("go.mod", GOMOD.replace("example.com/a v1.0.0", "example.com/a v1.0.1")),
            ("go.mod", GOMOD + "\nreplace example.com/m => example.com/n v1.0.0\n"),
            ("go.mod", GOMOD + "\nexclude example.com/b v1.1.0\n"),
            ("go.mod", GOMOD.replace("go 1.25.0", "go 1.26.0")),
            ("go.sum", GOSUM + "example.com/c v1.0.0/go.mod h1:ffff=\n"),
        ]:
            with self.subTest(name=name, text=text):
                self.write(name, text)
                r = self.run_tool("--check", RECIPE, "phone-fw")
                self.assertEqual(r.returncode, 1)
                self.assertIn("run make gomods", r.stderr)
                self.write("go.mod", GOMOD)
                self.write("go.sum", GOSUM)
                self.assertTrue(self.checks())

    def test_no_lists_yet(self):
        self.assertFalse(self.checks())


class Generate(Tree):
    def test_a_go_older_than_go_mod_is_refused(self):
        r = self.generate(FAKE_GO_VERSION="go1.24.3")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("go.mod needs Go 1.25.0 or newer", r.stderr)
        self.assertIn("go1.24.3", r.stderr)
        self.assertIsNone(self.mods())
        self.assertNotIn("list", self.calls())

    def test_only_the_local_toolchain(self):
        self.assertEqual(self.generate().returncode, 0)
        calls = self.calls().splitlines()
        self.assertTrue(calls)
        for c in calls:
            self.assertTrue(c.endswith(" GOTOOLCHAIN=local"), c)
        mods = self.mods()
        self.assertIn("gomod://example.com/m;version=v1.0.0;sha256sum=", mods)
        self.assertIn("gomod://example.com/b;version=v1.2.0;mod=1;sha256sum=", mods)
        self.assertNotIn("golang.org/toolchain", mods)

    def test_GO_names_the_go_command(self):
        os.rename(os.path.join(self.tmp, "bin", "go"), os.path.join(self.tmp, "go-1.25"))
        self.assertEqual(self.generate(GO=os.path.join(self.tmp, "go-1.25")).returncode, 0)
        self.assertTrue(self.checks())

    def test_without_the_network(self):
        r = self.generate(FAKE_GO_OFFLINE="1")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("failed", r.stderr)
        self.assertIn("https://proxy.golang.org", r.stderr)
        self.assertIsNone(self.mods())


class Ensure(Tree):
    def ensure(self, *rev, **env):
        return self.run_tool("--ensure", *rev, RECIPE, "phone-fw", "./cmd/...", **env)

    def test_current_lists_are_left_alone(self):
        self.assertEqual(self.generate().returncode, 0)
        before = self.calls()
        r = self.ensure()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(self.calls(), before)

    def test_stale_lists_are_written_again(self):
        self.assertEqual(self.generate().returncode, 0)
        self.write("go.mod", GOMOD.replace("example.com/a v1.0.0", "example.com/a v1.0.1"))
        r = self.ensure()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("writing it again", r.stderr)
        self.assertTrue(self.checks())

    def test_stale_lists_that_cannot_be_written(self):
        self.assertEqual(self.generate().returncode, 0)
        old = self.mods()
        self.write("go.mod", GOMOD.replace("example.com/a v1.0.0", "example.com/a v1.0.1"))
        for env, why in [({"FAKE_GO_OFFLINE": "1"}, "https://proxy.golang.org"), ({"FAKE_GO_VERSION": "go1.24.3"}, "Go 1.25.0")]:
            with self.subTest(why=why):
                r = self.ensure(**env)
                self.assertNotEqual(r.returncode, 0)
                self.assertIn(why, r.stderr)
                self.assertEqual(self.mods(), old)

    def commit(self):
        """go.mod and go.sum as a new commit on HEAD (plumbing: no hooks
        of the machine's git template run in this scratch repository)."""
        def git(*args):
            return subprocess.run(["git", "-C", self.tree, "-c", "user.name=t", "-c", "user.email=t@example.com", *args],
                                  check=True, capture_output=True, text=True).stdout.strip()
        if not os.path.isdir(os.path.join(self.tree, ".git")):
            git("init", "-q")
        git("add", "go.mod", "go.sum")
        parent = subprocess.run(["git", "-C", self.tree, "rev-parse", "-q", "--verify", "HEAD"],
                                capture_output=True, text=True).stdout.strip()
        c = git("commit-tree", git("write-tree"), "-m", "go.mod", *(["-p", parent] if parent else []))
        git("update-ref", "HEAD", c)

    def test_a_commit(self):
        self.commit()
        self.assertEqual(self.generate().returncode, 0)
        bumped = GOMOD.replace("example.com/a v1.0.0", "example.com/a v1.0.1")
        self.write("go.mod", bumped)
        self.assertFalse(self.checks())
        self.assertTrue(self.checks("--rev", "HEAD"), "the lists are HEAD's, which SRC=git builds")
        before = self.calls()
        self.assertEqual(self.ensure("--rev", "HEAD").returncode, 0)
        self.assertEqual(self.calls(), before)
        self.assertEqual(self.ensure().returncode, 0)
        r = self.ensure("--rev", "HEAD")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("SRC=git", r.stderr)
        self.commit()
        self.assertEqual(self.ensure("--rev", "HEAD").returncode, 0)
        self.assertFalse(self.checks("--rev", "HEAD~1"))
        r = self.run_tool("--check", "--rev", "nosuchrev", RECIPE, "phone-fw")
        self.assertIn("no go.mod and go.sum at nosuchrev", r.stderr)


if __name__ == "__main__":
    unittest.main()
