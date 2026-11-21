"""Tests for tools/sku.py: the SKUs expand, and the tables the generator
keeps agree with the code that reads what it writes."""
import json
import os
import re
import subprocess
import sys
import tempfile
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
            if sku.is_phone(r["board"]):
                continue
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

    # A keypad without a Ctrl key says what stands in for it; the Shell and
    # the simulated SKU carry it to the renderer.
    def test_ctrl(self):
        r = sku.resolve("kario-qwerty-sd662-4g")
        self.assertEqual(sku.sku_json(r)["ctrl"], "fn+sym")
        self.assertEqual(sku.sim(r)["shell"]["ctrl"], "fn+sym")
        self.assertNotIn("ctrl", sku.sku_json(sku.resolve("kario-t9-sd460-2g")))
        for ctrl, why in (("sym", "is not"), ("shift+sym", "is not"), ("fn+fn", "is not"), ("fn+ctrl", "no ctrl key"), (1, "is not")):
            with self.assertRaisesRegex(sku.Bad, why):
                sku.check_matrix("x", dict(r["keypad"], ctrl=ctrl))
        with self.assertRaisesRegex(sku.Bad, "has a ctrl key"):
            sku.check_matrix("x", dict(r["keypad"], matrix=dict(r["keypad"]["matrix"], **{"6,1": "ctrl"})))
        self.assertIn('json:"ctrl,omitempty"', read(sku.FW, "shell", "sku.go"))

    # kario carries the table it simulates; it is regenerated with the SKUs.
    def test_sims_are_current(self):
        self.assertEqual(read(sku.SIMS), sku.sims_json(), "cmd/kario/skus.json is stale: python3 tools/sku.py sims")
        for s in sku.json.loads(sku.sims_json()):
            self.assertNotIn("id", s["shell"])
            self.assertFalse({"leds", "backlight", "keyboard_backlight", "light_sensor", "modem", "nfc", "fingerprint", "suspend", "gnss"} & set(s["shell"]))

    # The emulator reads NMEA from a pipe in the Shell's run directory; the
    # boards read the modem's GPS; a malformed receiver is refused.
    def test_gnss(self):
        self.assertEqual(sku.sku_json(sku.resolve("qemu-t9"))["gnss"], "fifo:gnss.nmea")
        self.assertEqual(sku.sku_json(sku.resolve("kario-t9-sd460-2g"))["gnss"], "modemmanager")
        board = sku.load("boards", "qemuarm64")
        for good in ("sim", "modemmanager", "nmea:/dev/gnss0", "nmea:/dev/ttyHS1@115200", "fifo:gnss.nmea", "fifo:/run/phone/gnss.nmea"):
            sku.check_gnss("x", dict(board, gnss=good))
        for bad in ("gps", "sim:x", "nmea:gnss0", "nmea:/dev/../etc/x", "nmea:/dev/ttyS0@1234", "fifo:a/b", "fifo:..", "fifo:", 1):
            with self.assertRaisesRegex(sku.Bad, "gnss"):
                sku.check_gnss("x", dict(board, gnss=bad))

    # What the Shell reads (shell/sku.go) has the field the generator writes.
    def test_gnss_field_in_the_shell(self):
        self.assertIn('json:"gnss,omitempty"', read(sku.FW, "shell", "sku.go"))

    def test_leds(self):
        j = sku.sku_json(sku.resolve("kario-qwerty-sd662-4g"))
        self.assertEqual(j["leds"]["notify"], {"name": "rgb:status", "color": [0, 255, 0]})
        self.assertNotIn("leds", sku.sku_json(sku.resolve("kario-t9-sd460-2g")))
        board = sku.load("boards", "qemuarm64")
        for leds, why in (({"notify": "a/b"}, "not an LED class name"), ({"notify_color": [1]}, "per channel"),
                          ({"notify": "x", "notify_color": [256]}, "per channel"), ({"blink": 1}, "unknown")):
            with self.assertRaisesRegex(sku.Bad, why):
                sku.check_leds("x", dict(board, leds=leds))

    # A board may name its keyboard backlight LED; the Shell reads the field.
    def test_keyboard_backlight(self):
        board = sku.load("boards", "qemuarm64")
        sku.check_leds("x", dict(board, keyboard_backlight="pm8xxx::kbd_backlight"))
        with self.assertRaisesRegex(sku.Bad, "keyboard_backlight"):
            sku.check_leds("x", dict(board, keyboard_backlight="a/b"))
        r = sku.resolve("kario-qwerty-sd662-4g")
        self.assertNotIn("keyboard_backlight", sku.sku_json(r))
        r = dict(r, board=dict(r["board"], keyboard_backlight="white::kbd_backlight"))
        self.assertEqual(sku.sku_json(r)["keyboard_backlight"], "white::kbd_backlight")
        self.assertIn('json:"keyboard_backlight,omitempty"', read(sku.FW, "shell", "sku.go"))

    # A board may name its light sensor (an IIO device's name).
    def test_light_sensor(self):
        board = sku.load("boards", "qemuarm64")
        sku.check_leds("x", dict(board, light_sensor="stk3310"))
        with self.assertRaisesRegex(sku.Bad, "light_sensor"):
            sku.check_leds("x", dict(board, light_sensor="../x"))
        r = sku.resolve("kario-qwerty-sd662-4g")
        r = dict(r, board=dict(r["board"], light_sensor="stk3310"))
        self.assertEqual(sku.sku_json(r)["light_sensor"], "stk3310")
        self.assertIn('json:"light_sensor,omitempty"', read(sku.FW, "shell", "sku.go"))


class PhoneTest(unittest.TestCase):
    """Whole phones (class = "phone"): the board brings its panel and
    buttons, the SKU only the form factor."""

    def resolve(self, ident="oneplus-fajita-t9", sku_over=None, board_over=None, display_over=None):
        """resolve with the SKU's, its board's or its panel's fields replaced
        (None drops one)."""
        real = sku.load

        def patched(kind, name):
            data = real(kind, name)
            for over, k in ((sku_over, "skus"), (board_over, "boards")):
                if over is not None and kind == k:
                    data = {**data, **over}
            if display_over is not None and kind == "boards":
                data = {**data, "display": {**data["display"], **display_over}}
            return {k: v for k, v in data.items() if v is not None}

        sku.load = patched
        try:
            return sku.resolve(ident)
        finally:
            sku.load = real

    def test_the_panel_as_drawn(self):
        j = sku.sku_json(sku.resolve("oneplus-fajita-t9"))
        self.assertEqual((j["device"], j["width"], j["height"], j["scale"]), ("bar-t9", 1080, 2340 - 160, 4.5))
        self.assertEqual((j["keypad"], j["display"], j["modem"]), ("hid", "s6e3fc2x01", "modemmanager"))
        self.assertTrue(j["suspend"] and j["power_key"])
        self.assertNotIn("fn", j)
        j = sku.sku_json(sku.resolve("xiaomi-mido-t9"))
        self.assertEqual((j["width"], j["height"], j["leds"]["notify"]["name"]), (1080, 1920, "green:indicator"))
        # 1080 / 4.5: the 9-key bar phone's own logical width.
        self.assertEqual(j["width"] / j["scale"], sku.DEVICES["bar-t9"][0] / 2)

    # The 6T has a Yocto machine (39-oneplus-6t.md): the kernel's own device
    # tree, no keypad overlay, and the build's images flashed as they are.
    def test_a_machine(self):
        r = sku.resolve("oneplus-fajita-t9")
        k = sku.kas(r)
        self.assertIn("machine: oneplus-fajita\n", k)
        self.assertIn('PHONE_SKU = "oneplus-fajita-t9"', k)
        self.assertNotIn("PHONE_SKU_DTB", k)
        conf = read(sku.FW, "yocto", "meta-phone", "conf", "machine", r["board"]["machine"] + ".conf")
        self.assertIn(f'KERNEL_DEVICETREE = "qcom/{r["board"]["dtb"]}"', conf)
        self.assertTrue(os.path.exists(os.path.join(sku.FW, r["board"]["flash"])))
        with self.assertRaisesRegex(sku.Bad, "no firmware package.*yocto/fajita-flash.sh"):
            sku.package("oneplus-fajita-t9", "/nonexistent")

    # Nothing to build yet: the image runs in a chroot on Nura.
    def test_no_machine(self):
        r = sku.resolve("oneplus-enchilada-t9")
        for f in (sku.kas, lambda r: sku.gen("oneplus-enchilada-t9")):
            with self.assertRaisesRegex(sku.Bad, "whole phone without a Yocto machine"):
                f(r)

    def test_files_for_the_chroot(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(sku.files("oneplus-fajita-t9", d), ["phone.env", "profile.json", "sku.json"])
            r = sku.resolve("oneplus-fajita-t9")
            self.assertEqual(json.loads(read(d, "sku.json")), sku.sku_json(r))
            env = read(d, "phone.env")
            self.assertIn("KARIO_COMPATIBLE=oneplus,fajita\n", env)
            self.assertIn("KARIO_BUTTONS='pm8941_pwrkey|Volume keys'\n", env)
            out = subprocess.run(["sh", "-c", f". {d}/phone.env && echo \"$KARIO_BUTTONS\""], capture_output=True, text=True)
            self.assertEqual(out.stdout, "pm8941_pwrkey|Volume keys\n")
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(sku.files("qemu-t9", d), ["profile.json", "sku.json"])

    def test_not_in_the_simulated_table(self):
        ids = {s["id"] for s in json.loads(sku.sims_json())}
        self.assertIn("qemu-t9", ids)
        self.assertFalse({"oneplus-fajita-t9", "oneplus-enchilada-t9", "xiaomi-mido-t9"} & ids)

    def test_the_shell_reads_the_scale(self):
        self.assertIn('json:"scale,omitempty"', read(sku.FW, "shell", "sku.go"))

    def test_mismatches_are_refused(self):
        for kw, why in (
            ({"sku_over": {"display": "dmt028"}}, "its display is its own"),
            ({"sku_over": {"keypad": "t9-v1"}}, "its keypad is its own"),
            ({"sku_over": {"device": None}}, "no form factor"),
            ({"sku_over": {"device": "bar-qwerty"}}, "rotate = 90 turns it"),
            ({"sku_over": {"rotate": 90}}, "inset rows cannot be turned"),
            ({"display_over": {"scale": 2}}, "well smaller than on the 2.8"),
            ({"display_over": {"scale": 8}}, "over twice as big"),
            ({"display_over": {"width_mm": 148, "height_mm": 68}}, "not the shape"),
            ({"display_over": {"inset": 1200}}, "rows left dark"),
            ({"display_over": {"dpi": 400}}, "unknown 'dpi'"),
            ({"display_over": {"width": None}}, "width is the panel's pixels"),
            ({"board_over": {"display": None}}, "without a \\[display\\]"),
            ({"board_over": {"compatible": None}}, "root compatible"),
            ({"board_over": {"input": {"buttons": "Volume keys"}}}, "input device names"),
            ({"board_over": {"unverified": ["display.dpi"]}}, "not one of its values"),
        ):
            with self.subTest(kw=kw), self.assertRaisesRegex(sku.Bad, why):
                self.resolve(**kw)

    # A KaiOS phone's own keypad: its device tree's codes, each to one of
    # our keys (KEY_MENU, KEY_BACK, KEY_SEND, KEY_POWER as the red key,
    # KEY_OK, the number row as the 9-key pad).
    KAIOS = {"139": "soft-left", "158": "soft-right", "231": "call", "116": "end", "352": "ok", "14": "clear", "1": "back",
             "103": "up", "108": "down", "105": "left", "106": "right", "522": "star", "523": "pound",
             **{str(2 + i): str((i + 1) % 10) for i in range(10)}}

    def test_keymap(self):
        r = self.resolve("xiaomi-mido-t9", board_over={"keymap": dict(self.KAIOS)})
        j = sku.sku_json(r)
        self.assertEqual(j["keypad"], "builtin")
        self.assertEqual(list(j["keymap"])[:3], ["1", "2", "3"], "keymap in code order")
        self.assertEqual((j["keymap"]["139"], j["keymap"]["11"], j["keymap"]["116"]), ("soft-left", "0", "end"))
        self.assertNotIn("keymap", sku.sku_json(sku.resolve("xiaomi-mido-t9")))
        self.assertEqual(sku.sku_json(sku.resolve("xiaomi-mido-t9"))["keypad"], "hid")
        # A value to confirm on the phone may be one of its codes.
        self.resolve("xiaomi-mido-t9", board_over={"keymap": dict(self.KAIOS), "unverified": ["keymap.139"]})

    def test_keymap_refused(self):
        less = {k: v for k, v in self.KAIOS.items() if v != "pound"}
        for km, why in (
            ({}, "table of input codes"),
            ("139=soft-left", "table of input codes"),
            (dict(self.KAIOS, **{"0": "ok"}), "'0' is not an input code"),
            (dict(self.KAIOS, **{"768": "ok"}), "'768' is not an input code"),
            (dict(self.KAIOS, **{"KEY_MENU": "ok"}), "'KEY_MENU' is not an input code"),
            (dict(self.KAIOS, **{"0139": "ok"}), "'0139' is not an input code"),
            (dict(self.KAIOS, **{"139": "menu"}), "139 = 'menu' is no key"),
            (less, "lacks pound"),
        ):
            with self.subTest(km=km), self.assertRaisesRegex(sku.Bad, why):
                self.resolve("xiaomi-mido-t9", board_over={"keymap": km})
        # The qwerty form factor needs the letters a 9-key keypad lacks.
        with self.assertRaisesRegex(sku.Bad, "lacks a, b"):
            self.resolve("xiaomi-mido-t9", sku_over={"device": "bar-qwerty", "rotate": 90}, board_over={"keymap": dict(self.KAIOS)})
        # A module board's keypad has its matrix.
        real = sku.load

        def patched(kind, name):
            data = real(kind, name)
            return dict(data, keymap=dict(self.KAIOS)) if kind == "boards" else data

        sku.load = patched
        try:
            with self.assertRaisesRegex(sku.Bad, "a keymap is a whole phone's"):
                sku.resolve("kario-t9-sd662-2g")
        finally:
            sku.load = real

    # The Shell reads the keymap and maps every key the generator accepts to
    # the code phone-paint reads it as (shell/sku.go's keyCodes).
    def test_keymap_in_the_shell(self):
        src = read(sku.FW, "shell", "sku.go")
        self.assertIn('json:"keymap,omitempty"', src)
        block = src[src.index("var keyCodes = map[string]int{"):]
        block = block[:block.index("\n}\n")]
        shell = {k: int(v) for k, v in re.findall(r'"([a-z0-9-]+)": (\d+)', block)}
        self.assertEqual(shell, {k: v[1] for k, v in sku.keys_mapped().items()})

    # A panel without inset rows may show a landscape form factor turned, as
    # the module boards' sideways panel.
    def test_turned(self):
        r = self.resolve("xiaomi-mido-t9", sku_over={"device": "bar-qwerty", "rotate": 90})
        j = sku.sku_json(r)
        self.assertEqual((j["width"], j["height"], j["rotate"]), (1920, 1080, 90))
        with self.assertRaisesRegex(sku.Bad, "rotate = 90 turns it"):
            self.resolve("xiaomi-mido-t9", sku_over={"device": "bar-qwerty"})


if __name__ == "__main__":
    unittest.main()
