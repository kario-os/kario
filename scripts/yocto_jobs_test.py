"""Tests of scripts/yocto-jobs.py: the image build's parallelism from the
processors and the memory, a control group's limit counted, on made-up
/proc and /sys trees (cgroup v1 and v2).

    python3 -m unittest scripts/yocto_jobs_test.py
"""
import importlib.util
import os
import shutil
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("yocto_jobs", os.path.join(HERE, "yocto-jobs.py"))
yj = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(yj)

GIB = 1 << 30
UNLIMITED_V1 = "9223372036854771712"


class Jobs(unittest.TestCase):
    def test_the_rule(self):
        for cpus, mem, want in [
            (48, 48 * GIB, 24),          # the developer machine: its user slice's 48 GiB
            (8, int(15.4 * GIB), 7),     # 8 cores, 15 GB
            (4, 64 * GIB, 4),
            (64, 1 * GIB, 1),
            (1, 0, 1),
        ]:
            with self.subTest(cpus=cpus, mem=mem):
                self.assertEqual(yj.jobs(cpus, mem), want)


class Machine(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="yocto-jobs-test-")
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.put("proc/meminfo", "MemTotal:       263463456 kB\nMemFree:        1000 kB\n")
        real = yj.processors
        yj.processors = lambda: 48
        self.addCleanup(setattr, yj, "processors", real)

    def put(self, path, text):
        path = os.path.join(self.root, path)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(text)

    def test_cgroup_v1_a_limit_above(self):
        self.put("proc/self/cgroup", "11:pids:/user.slice/user-1070.slice/session-1.scope\n"
                 "3:memory:/user.slice/user-1070.slice/session-1.scope\n1:name=systemd:/user.slice\n")
        m = "sys/fs/cgroup/memory/"
        self.put(m + "memory.limit_in_bytes", UNLIMITED_V1)
        self.put(m + "user.slice/memory.limit_in_bytes", str(224 * GIB))
        self.put(m + "user.slice/user-1070.slice/memory.limit_in_bytes", str(48 * GIB))
        self.put(m + "user.slice/user-1070.slice/session-1.scope/memory.limit_in_bytes", UNLIMITED_V1)
        self.assertEqual(yj.cgroup_limit(self.root), 48 * GIB)
        n, why = yj.explain(self.root)
        self.assertEqual(n, 24)
        self.assertEqual(why, "24 from 48 processors and 48 GiB of memory (the control group's limit; the machine "
                              "has 251.3 GiB): one job per processor, at most one per 2 GiB")

    def test_cgroup_v2(self):
        self.put("proc/self/cgroup", "0::/user.slice/user-1000.slice/session-2.scope\n")
        c = "sys/fs/cgroup/"
        self.put(c + "user.slice/memory.max", "max\n")
        self.put(c + "user.slice/user-1000.slice/memory.max", str(15 * GIB) + "\n")
        self.put(c + "user.slice/user-1000.slice/session-2.scope/memory.max", "max\n")
        self.assertEqual(yj.cgroup_limit(self.root), 15 * GIB)
        self.assertEqual(yj.explain(self.root)[0], 7)

    def test_no_limit(self):
        self.put("proc/self/cgroup", "0::/\n")
        self.put("sys/fs/cgroup/memory.max", "max\n")
        self.assertIsNone(yj.cgroup_limit(self.root))
        n, why = yj.explain(self.root)
        self.assertEqual(n, 48)
        self.assertIn("(the machine's)", why)

    def test_no_cgroup_files(self):
        self.assertIsNone(yj.cgroup_limit(self.root))
        self.assertEqual(yj.explain(self.root)[0], 48)

    def test_a_limit_above_the_machine(self):
        self.put("proc/meminfo", "MemTotal:       16000000 kB\n")
        self.put("proc/self/cgroup", "0::/a\n")
        self.put("sys/fs/cgroup/a/memory.max", str(64 * GIB))
        n, why = yj.explain(self.root)
        self.assertEqual(n, 7)
        self.assertIn("(the machine's)", why)


if __name__ == "__main__":
    unittest.main()
