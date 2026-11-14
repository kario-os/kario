"""Tests for tools/sku.py: the SKUs expand, and the tables the generator
keeps agree with the code that reads what it writes."""
import os
import re
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sku  # noqa: E402


def read(*path):
    with open(os.path.join(*path)) as f:
        return f.read()


class SKUTest(unittest.TestCase):
    def test_every_sku_expands(self):
        for s in sku.names("skus"):
            r = sku.resolve(s)
            j = sku.sku_json(r)
            self.assertEqual(j["id"], s)
            self.assertIn(j["device"], sku.DEVICES)
            self.assertIn(f'PHONE_SKU = "{s}"', sku.kas(r))
            if not r["board"].get("virtual"):
                o = sku.overlay(r)
                self.assertIn('compatible = "ti,tca8418"', o)
                self.assertIn(r["display"]["compatible"][0], o)

    # The generator's form factors are the shell's (shell/device.go).
    def test_devices_match_the_shell(self):
        src = read(sku.FW, "shell", "device.go")
        found = {m[0]: (int(m[1]), int(m[2]), m[3]) for m in re.findall(
            r'ID: "([a-z0-9-]+)".*?Keypad: "([a-z0-9]+)",\s*Width: (\d+), Height: (\d+)', src, re.S)
            for m in [(m[0], m[2], m[3], m[1])]}
        self.assertEqual(found, sku.DEVICES)

    # Every key a keypad may report is one phone-paint turns into a key.
    def test_paint_maps_every_key(self):
        kms = read(sku.FW, "paint", "src", "kms.rs")
        mapped = set(re.findall(r"KeyCode::(KEY_\w+)", kms))
        with open(os.path.join(sku.SKUS, "keys.toml"), "rb") as f:
            table = sku.tomllib.load(f)["keys"]
        for name, (code, _) in table.items():
            self.assertIn(code, mapped, f"{name}: phone-paint does not map {code}")

    # The codes' numbers are the kernel's.
    def test_codes_are_the_kernels(self):
        header = "/usr/include/linux/input-event-codes.h"
        if not os.path.exists(header):
            self.skipTest("no kernel headers")
        defs = {}
        for name, value in re.findall(r"#define\s+(KEY_\w+)\s+(0x[0-9a-fA-F]+|\d+)", read(header)):
            defs[name] = int(value, 0)
        for name, (code, number) in sku.keys().items():
            if code in defs:
                self.assertEqual(defs[code], number, f"{name}: {code}")

    def test_keymap_cells(self):
        pad = sku.load("keypads", "t9-v1")
        cells = sku.keymap(pad)
        self.assertEqual(cells[0], "0x0000003b")  # 0,0 soft-left: KEY_F1 (59)
        self.assertIn("0x0403020b", cells)        # 4,3 pound: KEY_NUMERIC_POUND (0x20b)

    # The wiring's labels exist in the boards' trees (where a kernel tree is
    # at hand), and a wrong one is caught before the build.
    def test_wiring_labels(self):
        src = sku.kernel_src()
        if not src:
            self.skipTest("no kernel tree")
        for b in ("sm6115-fxtec-pro1x", "sm4250-oneplus-billie2"):
            board = sku.load("boards", b)
            self.assertFalse(sku.wiring_labels(board["wiring"]) - sku.dt_labels(src, board["dtb"]), b)
        billie = sku.load("boards", "sm4250-oneplus-billie2")
        self.assertNotIn("pm6125_l9a", sku.dt_labels(src, billie["dtb"]))

    def test_mismatches_are_refused(self):
        r = sku.resolve("kario-t9-sd662-2g")
        bad = dict(r["keypad"], matrix={k: v for k, v in r["keypad"]["matrix"].items() if v != "pound"})
        with self.assertRaisesRegex(sku.Bad, "lacks pound"):
            sku.check_matrix("x", bad)

    def test_fn_layer(self):
        r = sku.resolve("kario-qwerty-sd662-4g")
        self.assertEqual(sku.sku_json(r)["fn"]["w"], "1")
        self.assertEqual(sku.sku_json(r)["fn"]["."], "0")
        for layer, why in (({"ok": "1"}, "not a character key"), ({"w": "12"}, "not one character")):
            with self.assertRaisesRegex(sku.Bad, why):
                sku.check_matrix("x", dict(r["keypad"], fn=layer))

    def test_leds(self):
        j = sku.sku_json(sku.resolve("kario-qwerty-sd662-4g"))
        self.assertEqual(j["leds"]["notify"], {"name": "rgb:status", "color": [0, 255, 0]})
        self.assertNotIn("leds", sku.sku_json(sku.resolve("kario-t9-sd460-2g")))
        board = sku.load("boards", "qemuarm64")
        for leds, why in (({"notify": "a/b"}, "not an LED class name"), ({"notify_color": [1]}, "per channel"),
                          ({"notify": "x", "notify_color": [256]}, "per channel"), ({"blink": 1}, "unknown")):
            with self.assertRaisesRegex(sku.Bad, why):
                sku.check_leds("x", dict(board, leds=leds))


if __name__ == "__main__":
    unittest.main()
