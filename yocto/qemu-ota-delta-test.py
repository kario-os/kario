#!/usr/bin/env python3
"""Test the label yocto/qemu-ota-delta.sh gives this tree in what it
reports (its function tree_rev, taken out of the script and run on its own,
no QEMU) on a temporary repository with tree-note.sh where the function
finds it, git's global and system configuration off.

Checked: the commit as git describe --always names it; "-dirty" after it
when the tree has changes, a new file too; a file .gitignore names is no
change; "unknown" outside git; version() labels a build with tree_rev.

    qemu-ota-delta-test.py
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "qemu-ota-delta.sh")
NOTE = os.path.join(HERE, "..", "tools", "phone-chroot", "tree-note.sh")


def function(name):
    """The script's definition of the shell function name."""
    with open(SCRIPT, encoding="utf-8") as f:
        m = re.search(rf"^{name}\(\) {{\n.*?^}}\n", f.read(), re.M | re.S)
    if not m:
        raise AssertionError(f"no function {name} in {SCRIPT}")
    return m.group(0)


class TreeRev(unittest.TestCase):
    def setUp(self):
        self.repo = tempfile.mkdtemp(prefix="qemu-ota-delta-test-")
        self.addCleanup(shutil.rmtree, self.repo)
        self.env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
                        GIT_CEILING_DIRECTORIES=os.path.dirname(self.repo))
        os.makedirs(os.path.join(self.repo, "yocto"))
        os.makedirs(os.path.join(self.repo, "tools", "phone-chroot"))
        shutil.copy(NOTE, os.path.join(self.repo, "tools", "phone-chroot"))
        self.write(".gitignore", "/build/\n")
        self.git("init", "-q")
        self.git("add", ".")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "start")
        self.rev = self.git("describe", "--always").strip()

    def git(self, *args):
        return subprocess.run(["git", "-C", self.repo, *args], env=self.env, capture_output=True, text=True,
                              check=True).stdout

    def write(self, path, text="x\n"):
        p = os.path.join(self.repo, path)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "w", encoding="utf-8") as f:
            f.write(text)

    def tree_rev(self):
        """tree_rev's output, its $here the repository's yocto/ as the
        script's is."""
        r = subprocess.run(["sh", "-c", "set -u\n" + function("tree_rev") + "tree_rev\n"],
                           env=dict(self.env, here=os.path.join(self.repo, "yocto")),
                           capture_output=True, text=True, timeout=30)
        self.assertEqual((r.returncode, r.stderr), (0, ""))
        return r.stdout

    def test_a_tree_without_changes(self):
        self.write("build/out.txt")
        self.assertEqual(self.tree_rev(), self.rev + "\n")

    def test_a_new_file_is_a_change(self):
        self.write("notes.txt")
        self.assertEqual(self.tree_rev(), self.rev + "-dirty\n")

    def test_a_changed_file_is_a_change(self):
        self.write(".gitignore", "/build/\n/out/\n")
        self.assertEqual(self.tree_rev(), self.rev + "-dirty\n")

    def test_outside_git(self):
        shutil.rmtree(os.path.join(self.repo, ".git"))
        self.assertEqual(self.tree_rev(), "unknown\n")

    def test_a_build_is_labelled_with_it(self):
        self.assertIn("$(tree_rev)", function("version"))


if __name__ == "__main__":
    sys.exit(unittest.main())
