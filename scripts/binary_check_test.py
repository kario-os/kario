"""Tests of scripts/binary-check.py, which keeps programs and big files out
of what make publish pushes, on temporary git repositories; and of
scripts/publish.sh refusing to push while it fails.

    python3 -m unittest scripts/binary_check_test.py
"""
import importlib.util
import os
import shutil
import struct
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("binary_check", os.path.join(HERE, "binary-check.py"))
bc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(bc)

ELF = b"\x7fELF\x02\x01\x01\x00" + bytes(56)
# A PE program's DOS header, its e_lfanew pointing at the PE signature.
PE = b"MZ" + bytes(0x3a) + struct.pack("<I", 0x80) + bytes(0x80 - 0x40) + b"PE\0\0" + bytes(32)
GIT_ENV = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1", GIT_AUTHOR_NAME="t",
               GIT_AUTHOR_EMAIL="t@example.com", GIT_COMMITTER_NAME="t", GIT_COMMITTER_EMAIL="t@example.com")


class Repo(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp(prefix="binary-check-")
        self.addCleanup(shutil.rmtree, self.dir)
        self.repo = os.path.join(self.dir, "repo")
        os.mkdir(self.repo)
        self.git("init", "-q", "-b", "main")

    def git(self, *args):
        return subprocess.run(["git", "-C", self.repo, *args], env=GIT_ENV, capture_output=True, text=True,
                              check=True).stdout

    def write(self, path, data):
        p = os.path.join(self.repo, path)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "wb") as f:
            f.write(data)

    def check(self, **kw):
        kw.setdefault("limit", 1000)
        kw.setdefault("big", {})
        kw.setdefault("programs", {})
        return bc.check(self.repo, **kw)

    def flagged(self, **kw):
        return sorted(line.split(":")[0] for line in self.check(**kw)[0])


class Check(Repo):
    def test_a_program_built_in_an_app_s_directory(self):
        self.write("apps/settings/main.go", b"package main\n")
        self.write("apps/settings/settings", ELF)
        bad, n = self.check()
        self.assertEqual(n, 2)
        self.assertEqual(len(bad), 1)
        self.assertIn("apps/settings/settings: a program (ELF, 64 B)", bad[0])

    def test_a_tracked_program_too(self):
        self.write("bin/tool", ELF)
        self.git("add", "bin/tool")
        self.assertEqual(self.flagged(), ["bin/tool"])

    def test_a_big_file(self):
        self.write("docs/img/shot.png", b"\x89PNG" + bytes(997))
        self.write("docs/img/small.png", b"\x89PNG" + bytes(996))
        bad, _ = self.check()
        self.assertEqual([b.split(":")[0] for b in bad], ["docs/img/shot.png"])
        self.assertIn("1001 B, over the 1000 B limit", bad[0])

    def test_an_allowed_big_file_up_to_its_own_limit(self):
        self.write("ime/data/zh.tsv.gz", bytes(5000))
        self.assertEqual(self.flagged(big={"ime/data/zh.tsv.gz": (5000, "the dictionary")}), [])
        bad, _ = self.check(big={"ime/data/zh.tsv.gz": (4999, "the dictionary")})
        self.assertEqual(len(bad), 1)
        self.assertIn("ime/data/zh.tsv.gz: 5 KiB, over the 5 KiB BIG allows it", bad[0])

    def test_an_allowed_program(self):
        self.write("hal/testdata/fw.elf", ELF)
        self.assertEqual(self.flagged(programs={"hal/testdata/fw.elf": "a fixture"}), [])

    def test_what_git_ignores_is_not_published(self):
        self.write(".gitignore", b"/apps/settings/settings\n/build/\n")
        self.write("apps/settings/settings", ELF)
        self.write("build/usr/bin/kario-system", ELF + bytes(5000))
        self.write("build/big.tar.gz", bytes(5000))
        self.assertEqual(self.check(), ([], 1))

    def test_a_tracked_file_git_would_ignore_is_still_published(self):
        self.write("phone-shell", ELF)
        self.git("add", "phone-shell")
        self.write(".gitignore", b"/phone-*\n")
        self.assertEqual(self.flagged(), ["phone-shell"])

    def test_a_deleted_file_and_a_link(self):
        self.write("gone", ELF)
        self.git("add", "gone")
        os.remove(os.path.join(self.repo, "gone"))
        self.write("data/big", bytes(5000))
        self.write(".gitignore", b"/data/\n")
        os.symlink("data/big", os.path.join(self.repo, "link"))
        self.assertEqual(self.check(), ([], 1))

    def test_the_formats(self):
        cases = {
            "elf": ELF,
            "macho64": bytes.fromhex("cffaedfe") + bytes(60),
            "macho32be": bytes.fromhex("feedface") + bytes(60),
            "fat": bytes.fromhex("cafebabe") + bytes(60),
            "pe": PE,
            "mz-text": b"MZ is how a DOS program starts, and this sentence too." + bytes(10),
            "short-mz": b"MZ",
            "text": b"#!/bin/sh\necho hi\n",
            "empty": b"",
        }
        for name, data in cases.items():
            self.write(name, data)
        got = {name: bc.program(os.path.join(self.repo, name)) for name in cases}
        self.assertEqual(got, {"elf": "ELF", "macho64": "Mach-O", "macho32be": "Mach-O", "fat": "Mach-O",
                               "pe": "PE", "mz-text": None, "short-mz": None, "text": None, "empty": None})

    def test_the_command(self):
        self.write("ok.txt", b"text\n")
        script = os.path.join(HERE, "binary-check.py")
        ok = subprocess.run(["python3", script, self.repo], capture_output=True, text=True)
        self.assertEqual(ok.returncode, 0, ok.stdout)
        self.assertIn("1 files publish would push, 0 problems", ok.stdout)
        self.write("a.out", ELF)
        bad = subprocess.run(["python3", script, self.repo], capture_output=True, text=True)
        self.assertEqual(bad.returncode, 1)
        self.assertIn("binary-check: a.out: a program (ELF", bad.stdout)

    def test_this_repository_s_allowances_are_files_it_has(self):
        for f, (most, why) in bc.BIG.items():
            self.assertTrue(os.path.isfile(os.path.join(bc.ROOT, f)), f)
            self.assertGreater(most, bc.LIMIT, f)
            self.assertTrue(why, f)
        for f, why in bc.PROGRAMS.items():
            self.assertTrue(bc.program(os.path.join(bc.ROOT, f)), f)
            self.assertTrue(why, f)


class Publish(Repo):
    """publish.sh checks before git add -A: a refused publish stages nothing
    and pushes nothing."""

    def setUp(self):
        super().setUp()
        self.remote = os.path.join(self.dir, "remote.git")
        subprocess.run(["git", "init", "-q", "--bare", self.remote], env=GIT_ENV, check=True)
        self.git("remote", "add", "origin", self.remote)
        self.write("README", b"Kario\n")
        self.git("add", "README")
        self.git("commit", "-q", "-m", "start")

    def publish(self):
        return subprocess.run(["sh", os.path.join(HERE, "publish.sh"), "test"], cwd=self.repo, env=GIT_ENV,
                              capture_output=True, text=True)

    def remote_main(self):
        return subprocess.run(["git", "-C", self.remote, "rev-parse", "-q", "--verify", "refs/heads/main"],
                              env=GIT_ENV, capture_output=True, text=True).stdout.strip()

    def test_a_program_stops_it(self):
        self.write("apps/settings/settings", ELF)
        r = self.publish()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("apps/settings/settings: a program (ELF", r.stdout)
        self.assertIn("publish: not published", r.stderr)
        self.assertEqual(self.remote_main(), "")
        self.assertEqual(self.git("diff", "--cached", "--name-only"), "")

    def test_without_one_it_publishes(self):
        self.write(".gitignore", b"/apps/settings/settings\n")
        self.write("apps/settings/settings", ELF)
        r = self.publish()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(self.remote_main(), self.git("rev-parse", "HEAD").strip())
        files = subprocess.run(["git", "-C", self.remote, "ls-tree", "-r", "--name-only", "main"], env=GIT_ENV,
                               capture_output=True, text=True, check=True).stdout.split()
        self.assertEqual(files, [".gitignore", "README"])


if __name__ == "__main__":
    unittest.main()
