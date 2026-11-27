"""Tests of scripts/docs-links.py, which checks the links in docs/ and
docs/en/ and the section numbers they name, on temporary docs trees.

    python3 -m unittest scripts/docs_links_test.py
"""
import importlib.util
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("docs_links", os.path.join(HERE, "docs-links.py"))
dl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(dl)

TARGET = "# The phone\n\n## 8. What to check first\n\n### 8.2 When it does not come up\n\n## 9. Returning\n"


class Check(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="docs-links-")
        self.addCleanup(shutil.rmtree, self.root)
        os.mkdir(os.path.join(self.root, "en"))
        self.write("39-phone.md", TARGET)

    def write(self, name, text):
        with open(os.path.join(self.root, name), "w", encoding="utf-8") as f:
            f.write(text)

    def problems(self):
        missing, repeated, unknown = dl.check(self.root)
        strip = lambda lines: [line[len(self.root) + 1:] for line in lines]
        return strip(missing), strip(repeated), strip(unknown)

    def test_a_link_to_a_file_that_is_not_there(self):
        self.write("01-a.md", "See [2](02-gone.md) and [39](39-phone.md), [web](https://example.com/x.md).\n")
        self.assertEqual(self.problems(), (["01-a.md: missing 02-gone.md"], [], []))

    def test_a_section_number_given_twice(self):
        self.write("28-battery.md", "# Battery\n\n## 9. Not done\n\n### 9.1 One\n\n## 9. Charger mode\n\n### 9.1 Two\n"
                                    "\n## 10. Next\n")
        self.assertEqual(self.problems(), ([], ["28-battery.md:7: section 9 again (first at line 3)",
                                                "28-battery.md:9: section 9.1 again (first at line 5)"], []))

    def test_a_link_to_a_section_that_is_not_there(self):
        self.write("40-a.md", "Repack it as [39](39-phone.md) §8.2 does.\n\nOr as [39](39-phone.md) §9.2 does.\n")
        self.assertEqual(self.problems(), ([], [], ["40-a.md:3: 39-phone.md has no section 9.2"]))

    def test_the_numbers_after_the_link(self):
        self.write("40-a.md", "[39](39-phone.md) §8\u3001§8.2\u3001§7; [39](39-phone.md)'s §9, §6 and §5; "
                              "[39](39-phone.md) \u7684 §4.\n\n[39](39-phone.md) §8; §1.3 here is this file's.\n")
        self.assertEqual(self.problems()[2], ["40-a.md:1: 39-phone.md has no section 7",
                                              "40-a.md:1: 39-phone.md has no section 6",
                                              "40-a.md:1: 39-phone.md has no section 5",
                                              "40-a.md:1: 39-phone.md has no section 4"])

    def test_code_is_left_out_and_lines_kept(self):
        self.write("13-a.md", "## 1. Build\n\n```sh\n## 1. not a heading\necho '[39](39-phone.md) §3'\n```\n\n"
                              "`[39](39-phone.md) §3` and ``[](kario::module &m)``\n\n[39](39-phone.md) §3\n")
        self.assertEqual(self.problems(), ([], [], ["13-a.md:10: 39-phone.md has no section 3"]))

    def test_each_language_against_its_own_files(self):
        self.write("en/39-phone.md", TARGET.replace("### 8.2 When it does not come up\n", ""))
        self.write("40-a.md", "[39](39-phone.md) §8.2\n")
        self.write("en/40-a.md", "[39](39-phone.md) §8.2\n")
        self.assertEqual(self.problems(), ([], [], ["en/40-a.md:1: 39-phone.md has no section 8.2"]))

    def test_the_command(self):
        script = os.path.join(HERE, "docs-links.py")
        self.write("40-a.md", "[39](39-phone.md) §8.2\n")
        ok = subprocess.run(["python3", script, self.root], capture_output=True, text=True)
        self.assertEqual(ok.returncode, 0, ok.stdout)
        self.assertIn("docs-links: 0 broken links, 0 repeated section numbers, "
                      "0 links to sections that do not exist", ok.stdout)
        self.write("40-a.md", "[39](39-phone.md) §8.3\n")
        bad = subprocess.run(["python3", script, self.root], capture_output=True, text=True)
        self.assertEqual(bad.returncode, 1)
        self.assertIn("40-a.md:1: 39-phone.md has no section 8.3", bad.stdout)
        self.assertIn("1 links to sections that do not exist", bad.stdout)


if __name__ == "__main__":
    unittest.main()
