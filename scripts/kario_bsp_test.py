"""Tests of scripts/kario-bsp.py: the commit phone-fajita.yml pins, and
kario-bsp fetched at it, from a repository made up here.

    python3 -m unittest scripts/kario_bsp_test.py
"""
import importlib.util
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("kario_bsp", os.path.join(HERE, "kario-bsp.py"))
kb = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(kb)

KAS = """header:
  version: 14

repos:
  meta-qcom:
    url: https://github.com/qualcomm-linux/meta-qcom
    branch: wrynose
    commit: c791f2b7d050745ad83077e40a362c9b6c15fade

  kario-bsp:
    url: {url}
    branch: main
    commit: {commit}
    layers:
      meta-kario-qcom:
      meta-kario-sdm845:

local_conf_header:
  x: ""
"""


def git(d, *args):
    return subprocess.run(["git", "-C", d, "-c", "user.name=t", "-c", "user.email=t@example.org", *args],
                          capture_output=True, text=True, check=True).stdout.strip()


def commit(d, msg, parent=None):
    """The tree as main's commit (as scripts/publish.sh makes one)."""
    git(d, "add", "-A")
    c = git(d, "commit-tree", git(d, "write-tree"), "-m", msg, *(["-p", parent] if parent else []))
    git(d, "update-ref", "refs/heads/main", c)
    return c


class Pin(unittest.TestCase):
    def test_phone_fajita_yml(self):
        p = kb.pin()
        self.assertEqual((p["url"], p["branch"], p["layers"]),
                         ("https://github.com/kario-os/kario-bsp", "main", ["meta-kario-qcom", "meta-kario-sdm845"]))
        self.assertRegex(p["commit"], r"^[0-9a-f]{40}$")


class Fetch(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.remote = os.path.join(self.tmp, "remote")
        os.makedirs(self.remote)
        git(self.remote, "init", "-q", "-b", "main")
        for layer in ("meta-kario-qcom", "meta-kario-sdm845"):
            os.makedirs(os.path.join(self.remote, layer, "conf"))
            with open(os.path.join(self.remote, layer, "conf", "layer.conf"), "w") as f:
                f.write(f'BBFILE_COLLECTIONS += "{layer}"\n')
        self.commit = commit(self.remote, "one")
        self.out = os.path.join(self.tmp, "build", "kario-bsp")

    def kas(self, commit=None, url=None):
        path = os.path.join(self.tmp, "phone-fajita.yml")
        with open(path, "w") as f:
            f.write(KAS.format(url=url or self.remote, commit=commit or self.commit))
        return path

    def test_fetched_at_the_pin(self):
        kas = self.kas()
        self.assertIn("fetched at", kb.fetch(self.out, kas))
        self.assertEqual(git(self.out, "rev-parse", "HEAD"), self.commit)
        self.assertEqual(kb.layers(self.out, kas), [os.path.join(self.out, "meta-kario-qcom"), os.path.join(self.out, "meta-kario-sdm845")])
        # A change in the copy is undone.
        with open(os.path.join(self.out, "meta-kario-qcom", "conf", "layer.conf"), "a") as f:
            f.write("# changed\n")
        self.assertIn("fetched at", kb.fetch(self.out, kas))
        self.assertEqual(git(self.out, "status", "--porcelain"), "")
        self.assertEqual(kb.pin(self.kas(f'"{self.commit}"'))["commit"], self.commit)

    def test_a_new_pin(self):
        kb.fetch(self.out, self.kas())
        with open(os.path.join(self.remote, "README.md"), "w") as f:
            f.write("two\n")
        two = commit(self.remote, "two", self.commit)
        kb.fetch(self.out, self.kas(two))
        self.assertEqual(git(self.out, "rev-parse", "HEAD"), two)

    # kario-bsp is published as one commit, replaced each time: a pin of
    # the commit before is refused, not checked out from what is left.
    def test_a_commit_main_no_longer_has(self):
        kb.fetch(self.out, self.kas())
        commit(self.remote, "again")
        with self.assertRaisesRegex(kb.Bad, f"main has no commit {self.commit}"):
            kb.fetch(self.out, self.kas())

    def test_refused(self):
        with self.assertRaisesRegex(kb.Bad, "pins no kario-bsp commit yet"):
            kb.fetch(self.out, self.kas(f'"{kb.PLACEHOLDER}"'))
        with self.assertRaisesRegex(kb.Bad, "git fetch"):
            kb.fetch(self.out, self.kas(url=os.path.join(self.tmp, "none")))
        with self.assertRaisesRegex(kb.Bad, "not a full commit"):
            kb.pin(self.kas(self.commit[:12]))
        with self.assertRaisesRegex(kb.Bad, "no Yocto layer"):
            kb.layers(os.path.join(self.tmp, "empty"), self.kas())


if __name__ == "__main__":
    unittest.main()
