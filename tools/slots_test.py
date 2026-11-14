"""The A/B slot logic (R37): RAUC's backend (rauc-boot) and the boot-time
choice of phone-initramfs (slot-choose), run on one state directory the way
an update, boots and the health mark use it."""
import os
import subprocess
import tempfile
import unittest

FILES = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                     "yocto", "meta-phone", "recipes-phone", "phone-ota", "files")


class Slots(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()

    def rauc(self, *args):
        out = subprocess.run(["sh", os.path.join(FILES, "rauc-boot"), *args], env={"PHONE_SLOTS": self.dir, "PHONE_SLOT_CHOOSE": os.path.join(FILES, "slot-choose"), "PATH": os.environ["PATH"]},
                             capture_output=True, text=True, check=True)
        return out.stdout.strip()

    def boot(self):
        """A boot: phone-initramfs picks the slot."""
        out = subprocess.run(["sh", "-c", f'. "{FILES}/slot-choose"; choose "$0"', self.dir],
                             capture_output=True, text=True, check=True)
        self.assertEqual(self.rauc("get-current"), out.stdout.strip())
        return out.stdout.strip()

    def install(self, slot):
        """What RAUC does around writing a slot."""
        self.rauc("set-state", slot, "bad")
        self.rauc("set-primary", slot)

    def test_update_then_healthy(self):
        self.assertEqual(self.boot(), "a")
        self.install("b")
        self.assertEqual(self.rauc("get-primary"), "b")
        self.assertEqual(self.rauc("get-state", "b"), "bad")
        self.assertEqual(self.boot(), "b")
        self.rauc("set-state", self.rauc("get-current"), "good")  # rauc status mark-good
        self.assertEqual(self.rauc("get-state", "b"), "good")
        for _ in range(5):
            self.assertEqual(self.boot(), "b")

    def test_update_that_never_comes_up_rolls_back(self):
        self.boot()
        self.install("b")
        self.assertEqual([self.boot() for _ in range(3)], ["b", "b", "b"])  # never marked good
        self.assertEqual(self.boot(), "a")
        self.assertEqual(self.rauc("get-primary"), "a")
        self.assertEqual(self.boot(), "a")

    def test_the_good_slot_marked_bad_is_left(self):
        self.boot()
        self.install("b")
        self.boot()
        self.rauc("set-state", "b", "bad")
        self.assertEqual(self.rauc("get-primary"), "a")
        self.assertEqual(self.boot(), "a")

    def test_a_slot_that_fails_verification_is_refused_at_once(self):
        self.boot()
        self.install("b")
        self.assertEqual(self.boot(), "b")
        out = subprocess.run(["sh", "-c", f'. "{FILES}/slot-choose"; refuse "$0"', self.dir],
                             capture_output=True, text=True, check=True)
        self.assertEqual(out.stdout.strip(), "a")
        self.assertIn("slot b failed verification", out.stderr)
        self.assertEqual(self.rauc("get-current"), "a")
        self.assertEqual(self.rauc("get-primary"), "a")
        self.assertEqual(self.boot(), "a")

    def test_a_broken_state_file_boots_slot_a(self):
        with open(os.path.join(self.dir, "boot"), "w") as f:
            f.write("slot=x\ngood=../etc\ntries=lots\n")
        self.assertEqual(self.boot(), "a")


if __name__ == "__main__":
    unittest.main()
