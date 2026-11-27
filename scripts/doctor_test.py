"""Tests of scripts/doctor.sh with stand-ins for the tools it looks for, on a
PATH of nothing else: the device image's requirements are MISS (each
taken away in turn fails it), what only some checks want is a warning,
docker does instead of podman, and each part has its own verdict.

    python3 -m unittest scripts/doctor_test.py
"""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
DOCTOR = os.path.join(HERE, "doctor.sh")
REAL = ["sh", "awk", "grep", "sort", "head", "id", "cat", "dirname", "readlink", "sed", "ls", "mkdir"]

FAKES = {
    "go": 'echo "go version go${FAKE_GO:-1.25.0} linux/amd64"',
    "cargo": 'echo "cargo 1.92.0 (a1b2c3d4e 2025-11-01)"',
    "cmake": 'echo "cmake version 3.28.3"',
    "ninja": 'echo "1.11.1"',
    "openssl": 'echo "OpenSSL 3.0.13 30 Jan 2024"',
    "podman": 'echo "podman version 4.9.4"',
    "docker": '[ "$1" = info ] && exit "${FAKE_DOCKER_INFO:-0}"; echo "Docker version 27.0.1, build 1"',
    "newuidmap": "exit 0",
    "newgidmap": "exit 0",
    "getsubids": '[ "$1" = -g ] && shift; [ "${FAKE_SUBIDS:-65536}" = 0 ] && exit 1; echo "0: $1 100000 ${FAKE_SUBIDS:-65536}"',
    "pkg-config": '[ "$1" = --modversion ] && echo 8.5.0; exit 0',
    "fc-list": 'echo "/usr/share/fonts/NotoSansCJK.ttc: Noto Sans CJK SC:style=Regular"',
    "nproc": 'echo "${FAKE_NPROC:-16}"',
    "df": 'echo "Filesystem 1024-blocks Used Available Capacity Mounted on"; '
          'echo "/dev/sda1 2000000000 1 ${FAKE_DF_KB:-200000000} 1% /"',
}


class Doctor(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="doctor-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.bin = os.path.join(self.tmp, "bin")
        os.makedirs(self.bin)
        for name in REAL:
            os.symlink(shutil.which(name), os.path.join(self.bin, name))
        os.symlink(sys.executable, os.path.join(self.bin, "python3"))
        for name, body in FAKES.items():
            self.fake(name, body)
        self.gxx = self.fake("g++-fake", 'echo "${FAKE_GCC:-14}"', where=self.tmp)
        self.kas = self.fake("kas-container", 'KAS_CONTAINER_SCRIPT_VERSION="5.5"', where=os.path.join(self.tmp, "kas"))
        self.work = os.path.join(self.tmp, "yocto", "kario-work")
        self.env = {"PATH": self.bin, "HOME": os.path.join(self.tmp, "home"), "CXX23": self.gxx, "KAS": self.kas,
                    "WORK": self.work}

    def fake(self, name, body, where=None):
        where = where or self.bin
        os.makedirs(where, exist_ok=True)
        path = os.path.join(where, name)
        with open(path, "w") as f:
            f.write("#!/bin/sh\n" + body + "\n")
        os.chmod(path, 0o755)
        return path

    def drop(self, name):
        os.remove(os.path.join(self.bin, name))

    def doctor(self, *args, **env):
        r = subprocess.run(["sh", DOCTOR, *args, os.path.join(self.tmp, "yocto")], env=dict(self.env, **env),
                           capture_output=True, text=True, cwd=self.tmp)
        return r.returncode, r.stdout

    def assertMisses(self, line, *args, **env):
        code, out = self.doctor("--image", *args, **env)
        self.assertEqual(code, 1, out)
        self.assertIn("  MISS  " + line, out)
        self.assertIn("DOCTOR: the device image cannot be built here", out)

    def test_all_there(self):
        code, out = self.doctor("--image")
        self.assertEqual(code, 0, out)
        self.assertNotIn("MISS", out)
        self.assertIn("ok    podman 4.9.4 (kas-container's engine)", out)
        self.assertIn("ok    kas-container 5.5 " + self.kas, out)
        self.assertIn("DOCTOR: the device image can be built", out)
        self.assertNotIn("host build", out)

    def test_each_requirement(self):
        self.assertMisses("kas-container at /nowhere/kas-container", KAS="/nowhere/kas-container")
        self.assertMisses("go 1.24.3, need 1.25.0 or newer", FAKE_GO="1.24.3")
        self.assertMisses("GCC 14 or newer", FAKE_GCC="13")
        self.assertMisses("9 GB free", FAKE_DF_KB=str(10 * 1000 * 1000))
        for name in ("cargo", "openssl", "go"):
            with self.subTest(name=name):
                os.rename(os.path.join(self.bin, name), os.path.join(self.tmp, name))
                self.assertMisses(name + " (")
                os.rename(os.path.join(self.tmp, name), os.path.join(self.bin, name))

    def test_no_container_engine(self):
        self.drop("podman")
        self.drop("docker")
        self.assertMisses("podman or docker")

    @unittest.skipIf(os.getuid() == 0, "rootless podman only")
    def test_rootless_podman(self):
        self.assertMisses("0 subordinate uids and 0 gids", FAKE_SUBIDS="0")
        self.assertMisses("4096 subordinate uids and 4096 gids", FAKE_SUBIDS="4096")
        self.drop("newgidmap")
        self.assertMisses("newuidmap and newgidmap")

    def test_docker_instead(self):
        self.drop("podman")
        self.drop("getsubids")
        code, out = self.doctor("--image")
        self.assertEqual(code, 0, out)
        self.assertIn("ok    docker 27.0.1 (kas-container's engine)", out)
        self.assertNotIn("subordinate", out)
        self.assertMisses("docker cannot reach its daemon", FAKE_DOCKER_INFO="1")

    def test_disk_for_a_first_build_and_after(self):
        self.assertMisses("47 GB free for " + self.work + ": a first build", FAKE_DF_KB=str(50 * 1000 * 1000))
        os.makedirs(os.path.join(self.work, "build"))
        code, out = self.doctor("--image", FAKE_DF_KB=str(50 * 1000 * 1000))
        self.assertEqual(code, 0, out)
        self.assertIn("ok    47 GB free for " + self.work + " (at least 20)", out)
        self.assertMisses("9 GB free for " + self.work + ": a build in it", FAKE_DF_KB=str(10 * 1000 * 1000))

    def test_only_warnings(self):
        code, out = self.doctor("--image", FAKE_NPROC="4")
        self.assertEqual(code, 0, out)
        self.assertIn("warn  4 CPUs", out)

    def test_parallelism(self):
        code, out = self.doctor("--image", BB_NUMBER_THREADS="5", PARALLEL_MAKE="-j 3")
        self.assertIn("ok    bitbake runs 5 tasks at once, make -j 3 in each", out)
        code, out = self.doctor("--image")
        self.assertRegex(out, r"ok    bitbake runs (\d+) tasks at once, make -j \1 in each \(YOCTO_JOBS= sets both; here: \1 from ")

    def test_the_parts_verdicts(self):
        code, out = self.doctor(KAS="/nowhere/kas-container")
        self.assertEqual(code, 0, "the host targets have all they need: " + out)
        self.assertIn("DOCTOR: the host targets can run", out)
        self.assertIn("DOCTOR: the device image cannot be built here", out)
        self.drop("cmake")
        code, out = self.doctor()
        self.assertEqual(code, 1, out)
        self.assertIn("  MISS  cmake (", out)
        self.assertIn("DOCTOR: the host targets miss what is marked MISS under them", out)
        self.assertIn("DOCTOR: the device image can be built", out)


if __name__ == "__main__":
    unittest.main()
