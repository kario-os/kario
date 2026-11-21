"""Tests of scripts/yocto-check.py's look for packages of tests only, which
go install refuses by name.

    python3 -m unittest scripts/yocto_check_test.py
"""
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("yocto_check", os.path.join(HERE, "yocto-check.py"))
yc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(yc)

MODULE = "github.com/kario-os/kario"
INSTALL = "${GO_IMPORT}/cmd/... ${GO_IMPORT}/apps/... ${GO_IMPORT}/cts/probe"


class TestOnlyPackages(unittest.TestCase):
    def check(self, *files):
        return yc.test_only_packages(INSTALL, MODULE, list(files))

    def test_a_typescript_app_with_a_fuzz_target(self):
        self.assertEqual(self.check("apps/calendar/fuzz_test.go", "apps/calendar/src/ics.ts",
                                    "apps/clock/main.go", "apps/clock/main_test.go"), ["apps/calendar"])

    def test_a_file_beside_the_tests(self):
        self.assertEqual(self.check("apps/calendar/fuzz_test.go", "apps/calendar/doc.go"), [])

    def test_outside_the_patterns(self):
        self.assertEqual(self.check("tools/cfuzz/cfuzz_test.go", "appstore/x_test.go",
                                    "cts/other/x_test.go", "cmdx/x_test.go"), [])
        self.assertEqual(self.check("cts/probe/x_test.go"), ["cts/probe"])

    def test_what_go_list_skips(self):
        self.assertEqual(self.check("apps/x/testdata/a_test.go", "apps/.x/a_test.go", "apps/_x/a_test.go",
                                    "apps/m/go.mod", "apps/m/a_test.go", "apps/m/sub/b_test.go"), [])


class UpstreamStatus(unittest.TestCase):
    def test_a_header_with_it(self):
        self.assertTrue(yc.upstream_status("A fix.\n\nUpstream-Status: Pending\n---\n"))
        self.assertTrue(yc.upstream_status("Upstream-Status: Inappropriate [Kario only]\n"))

    def test_a_header_without_it(self):
        self.assertFalse(yc.upstream_status("Kario's patch: debugger hooks.\n\n- JS_SetDebugHandler\n"))
        self.assertFalse(yc.upstream_status("Upstream-Status: maybe\n"))


if __name__ == "__main__":
    unittest.main()
