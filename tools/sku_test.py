"""Tests for tools/sku.py: the SKUs expand, and the tables the generator
keeps agree with the code that reads what it writes."""
import contextlib
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import sku  # noqa: E402

# This tree's SKUs only, but where SKUPathTest sets them.
os.environ.pop(sku.SKU_PATH, None)


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
        for b in ("sm6115-fxtec-pro1x", "sm4250-oneplus-billie2", "qcm6490-ref", "qcm6490-flip"):
            board = sku.load("boards", b)
            self.assertFalse(sku.wiring_labels(board["wiring"]) - sku.dt_labels(src, board["dtb"]), b)
        billie = sku.load("boards", "sm4250-oneplus-billie2")
        self.assertNotIn("pm6125_l9a", sku.dt_labels(src, billie["dtb"]))
        ref = sku.load("boards", "qcm6490-ref")
        self.assertNotIn("mdss_dsi_out", sku.dt_labels(src, ref["dtb"]))

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

    # A keypad with the fingerprint sensor in its OK key says so; sku.json and
    # the simulated SKU carry it to keystored and the Shell.
    def test_fingerprint_in_the_ok_key(self):
        r = sku.resolve("kario-t9-sd460-2g")
        self.assertNotIn("fingerprint_at", sku.sku_json(r))
        r["keypad"] = dict(r["keypad"], fingerprint="ok-key")
        sku.check_fingerprint_place("x", r["keypad"])
        self.assertEqual(sku.sku_json(r)["fingerprint_at"], "ok-key")
        self.assertEqual(sku.sim(r)["shell"]["fingerprint_at"], "ok-key")
        with self.assertRaisesRegex(sku.Bad, "is not one of"):
            sku.check_fingerprint_place("x", dict(r["keypad"], fingerprint="back"))
        no_ok = {k: v for k, v in r["keypad"]["matrix"].items() if v != "ok"}
        with self.assertRaisesRegex(sku.Bad, "no ok key"):
            sku.check_fingerprint_place("x", dict(r["keypad"], matrix=no_ok))
        self.assertIn('json:"fingerprint_at,omitempty"', read(sku.FW, "shell", "sku.go"))
        self.assertIn('json:"fingerprint_at"', read(sku.FW, "programs", "keystored", "main.go"))

    # kario carries the table it simulates; it is regenerated with the SKUs.
    def test_sims_are_current(self):
        self.assertEqual(read(sku.SIMS), sku.sims_json(), "cmd/kario/skus.json is stale: python3 tools/sku.py sims")
        for s in sku.json.loads(sku.sims_json()):
            self.assertNotIn("id", s["shell"])
            self.assertFalse({"leds", "backlight", "keyboard_backlight", "light_sensor", "modem", "nfc", "fingerprint", "suspend", "gnss",
                              "audio", "rtc"} & set(s["shell"]))

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

    # The real-time clock: the 6T's is read-only and keeps the time as an
    # offset, QEMU's pl031 is set; both wake with "+N" alarms. A board
    # without [rtc] writes none (the Shell's zero value: settable).
    def test_rtc(self):
        self.assertEqual(sku.sku_json(sku.resolve("oneplus-fajita-t9"))["rtc"], {"read_only": True, "offset": True, "alarm": "relative"})
        self.assertEqual(sku.sku_json(sku.resolve("qemu-t9"))["rtc"], {"read_only": False, "offset": False, "alarm": "relative"})
        self.assertNotIn("rtc", sku.sku_json(sku.resolve("kario-t9-sd460-2g")))
        board = sku.load("boards", "qemuarm64")
        for good in ({}, {"alarm": "absolute"}, {"read_only": True}, {"read_only": True, "offset": True, "alarm": "relative"}):
            sku.check_rtc("x", dict(board, rtc=good))
        for bad, why in (
            ({"offset": True}, "from a read-only RTC only"),
            ({"read_only": True, "alarm": "absolute"}, "its alarm is relative"),
            ({"alarm": "+N"}, "relative or absolute"),
            ({"read_only": "yes"}, "true or false"),
            ({"settable": False}, "unknown 'settable'"),
            ("read_only", "rtc is a table"),
        ):
            with self.subTest(rtc=bad), self.assertRaisesRegex(sku.Bad, why):
                sku.check_rtc("x", dict(board, rtc=bad))
        self.assertIn("rtc.offset", sku.load("boards", "oneplus-fajita")["unverified"])

    # What the Shell reads (shell/sku.go) has every [rtc] field, with the
    # same rules (its LoadSKUFile refuses what check_rtc refuses).
    def test_rtc_in_the_shell(self):
        src = read(sku.FW, "shell", "sku.go")
        self.assertIn('json:"rtc,omitempty"', src)
        for k in sku.RTC:
            self.assertRegex(src, rf'json:"{k}(,omitempty)?"')
        for a in sku.ALARMS:
            self.assertIn(f'"{a}"', src)

    # phone-paint's GPU path by default: off unless the board (or the SKU
    # over it) says compose or raster, only on a board with a GPU; a board
    # whose display cannot render names its GPU's render node by driver.
    # The 6T stays off until it is checked on the phone.
    def test_paint_gpu(self):
        for s in sku.names("skus"):
            self.assertNotIn("paint_gpu", sku.sku_json(sku.resolve(s)), f"{s}: on by default before a phone checked it")
        fajita = sku.load("boards", "oneplus-fajita")
        self.assertEqual(fajita["paint_gpu"], "off")
        r = sku.resolve("kario-t9-sd662-2g")
        board = dict(r["board"], paint_gpu="compose", gpu_render="panfrost")
        self.assertEqual(sku.check_gpu("x", r["sku"], board), ("compose", "panfrost"))
        self.assertEqual(sku.check_gpu("x", dict(r["sku"], paint_gpu="raster"), board), ("raster", "panfrost"))
        self.assertEqual(sku.check_gpu("x", dict(r["sku"], paint_gpu="off"), board), ("off", "panfrost"))
        j = sku.sku_json(dict(r, paint_gpu="raster", gpu_render="powervr"))
        self.assertEqual((j["paint_gpu"], j["gpu_render"]), ("raster", "powervr"))
        qemu = sku.load("boards", "qemuarm64")
        for sk, b, why in (
            ({}, dict(board, paint_gpu="on"), "paint_gpu 'on' is not one of off, compose, raster"),
            ({"paint_gpu": True}, board, "paint_gpu True is not"),
            ({}, dict(board, gpu_render="../renderD128"), "not a kernel driver's name"),
            ({}, dict(board, gpu_render="Panfrost"), "not a kernel driver's name"),
            ({}, dict(qemu, paint_gpu="compose"), "has no GPU"),
            ({}, dict(qemu, gpu_render="virtio_gpu"), "has no GPU"),
        ):
            with self.subTest(why=why), self.assertRaisesRegex(sku.Bad, why):
                sku.check_gpu("x", sk, b)
        src = read(sku.FW, "shell", "sku.go")
        self.assertIn('json:"paint_gpu,omitempty"', src)
        self.assertIn('json:"gpu_render,omitempty"', src)
        self.assertNotIn("paint_gpu", json.dumps(sku.json.loads(sku.sims_json())))

    # An image on the GPU by default without changing the board
    # (yocto/kas/paint-gpu.yml): phone-sku's PHONE_PAINT_GPU_DEFAULT sets
    # the image's paint_gpu, only where the SKU's kas overlay says the board
    # has a GPU; unset, the image's sku.json is tools/sku.py's byte for byte.
    # The recipe's own Python runs here, with a stand-in for bitbake's d.
    def test_paint_gpu_default(self):
        recipe = read(sku.LAYER, "recipes-phone", "phone-sku", "phone-sku_0.1.bb")
        self.assertIn('PHONE_PAINT_GPU_DEFAULT ??= ""', recipe)
        self.assertIn("install -m 0644 ${B}/sku.json ${D}${datadir}/phone/sku.json", recipe)
        body = {name: textwrap.dedent(m) for name, m in re.findall(r"^python (\w*) ?\(\) \{\n(.*?)^\}", recipe, re.M | re.S)}

        class Fatal(Exception):
            pass

        class D:
            def __init__(self, **v):
                self.v = v

            def getVar(self, name):
                return self.v.get(name, "")

            def appendVar(self, *a):
                pass

            def appendVarFlag(self, *a):
                pass

        def fatal(msg):
            raise Fatal(msg)

        bb = type("bb", (), {"fatal": staticmethod(fatal)})

        def run(name, **v):
            exec(body[name], {"d": D(**v), "bb": bb, "os": os})

        fajita, qemu = sku.resolve("oneplus-fajita-t9"), sku.resolve("qemu-t9")
        self.assertIn('PHONE_SKU_GPU = "1"\n', sku.kas(fajita))
        self.assertNotIn("PHONE_SKU_GPU", sku.kas(qemu))
        j = sku.sku_json(fajita)
        with tempfile.TemporaryDirectory() as src, tempfile.TemporaryDirectory() as b:
            def image(paint, given=j):
                text = json.dumps(given, ensure_ascii=False, indent=2) + "\n"
                with open(os.path.join(src, "sku.json"), "w") as f:
                    f.write(text)
                run("", PHONE_PAINT_GPU_DEFAULT=paint, PHONE_SKU_GPU="1", PHONE_SKU=j["id"])
                run("do_compile", PHONE_PAINT_GPU_DEFAULT=paint, S=src, B=b)
                return text, read(b, "sku.json")
            text, out = image("")
            self.assertEqual(out, text, "unset: the SKU's sku.json as it is")
            self.assertEqual(json.loads(image("compose")[1]), {**j, "paint_gpu": "compose"})
            self.assertEqual(json.loads(image("raster")[1]), {**j, "paint_gpu": "raster"})
            self.assertEqual(json.loads(image("off", {**j, "paint_gpu": "raster"})[1]), j, "off over a board's raster")
            self.assertEqual(json.loads(image("", {**j, "paint_gpu": "raster"})[1])["paint_gpu"], "raster")
        run("", PHONE_PAINT_GPU_DEFAULT="off")
        for v, why in (
            ({"PHONE_PAINT_GPU_DEFAULT": "1", "PHONE_SKU_GPU": "1"}, "'1' is not off, compose or raster"),
            ({"PHONE_PAINT_GPU_DEFAULT": "compose", "PHONE_SKU": "qemu-t9"}, "SKU qemu-t9 has no GPU"),
        ):
            with self.subTest(why=why), self.assertRaisesRegex(Fatal, why):
                run("", **v)
        overlay = read(sku.FW, "yocto", "kas", "paint-gpu.yml")
        self.assertIn('    PHONE_PAINT_GPU_DEFAULT = "compose"\n', overlay)
        self.assertNotIn("PHONE_PAINT_GPU_DEFAULT", read(sku.FW, "yocto", "kas", "phone-fajita.yml"))

    # A display with a touchscreen: the Shell is told (sku.json's touch) and
    # QEMU's gets the virtio multitouch device, another device on a virtual
    # display is refused; the 6T's driver is built into its machine's
    # kernel, and a fragment that drops it or makes it a module is refused.
    def test_touch(self):
        self.assertTrue(sku.sku_json(sku.resolve("qemu-t9"))["touch"])
        self.assertIn('-device virtio-multitouch-pci"', sku.kas(sku.resolve("qemu-t9")))
        self.assertNotIn("touch", sku.sku_json(sku.resolve("qemu-qwerty")))
        self.assertNotIn("multitouch", sku.kas(sku.resolve("qemu-qwerty")))
        self.assertNotIn("touch", sku.sku_json(sku.resolve("kario-t9-sd460-2g")))
        self.assertTrue(sku.sku_json(sku.resolve("oneplus-fajita-t9"))["touch"])
        r = sku.resolve("qemu-t9")
        with self.assertRaisesRegex(sku.Bad, "not QEMU's"):
            sku.check_touch("x", r["board"], dict(r["display"], touchscreen="usb-tablet"))
        r = sku.resolve("oneplus-fajita-t9")
        have = sku.machine_kconfig(r["board"])
        for opt in sku.TOUCHSCREENS["syna,rmi4-i2c"]:
            self.assertEqual(have[opt], "y", opt)
            for value in ("m", "n", None):
                with self.subTest(opt=opt, value=value), self.assertRaisesRegex(sku.Bad, opt):
                    sku.check_touch("x", r["board"], r["display"], {k: v for k, v in dict(have, **{opt: value}).items() if v})
        with self.assertRaisesRegex(sku.Bad, "no kernel options known"):
            sku.check_touch("x", r["board"], dict(r["display"], touchscreen="goodix,gt9271"), have)
        # No machine of this layer (Nura's kernel, in the chroot): nothing to hold it to.
        sku.check_touch("x", sku.load("boards", "oneplus-enchilada"), {"touchscreen": "goodix,gt9271"})
        self.assertIn('json:"touch,omitempty"', read(sku.FW, "shell", "sku.go"))

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

    # Nothing to build yet: the image runs in a chroot on Nura, a generic one.
    def test_no_machine(self):
        r = sku.resolve("oneplus-enchilada-t9")
        for f in (sku.kas, lambda r: sku.gen("oneplus-enchilada-t9")):
            with self.assertRaisesRegex(sku.Bad, "whole phone without a Yocto machine"):
                f(r)
        self.assertIn("KARIO_MACHINE=''\n", sku.phone_env(r))

    # The image records what it is built from, for a phone test's kit.txt:
    # the tree's commit and changes, or the commit SRC=git builds.
    def test_the_image_names_its_source(self):
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=sku.FW, capture_output=True, text=True).stdout.strip()
        self.assertRegex(sku.source_note(), rf"^{head}, (no changes|with changes:\n  )")
        self.assertEqual(sku.source_note("HEAD"), f"{head}, no changes\n")
        saved = sku.FILES, sku.OUT, sku.KAS
        with tempfile.TemporaryDirectory() as d:
            sku.FILES, sku.OUT, sku.KAS = os.path.join(d, "files"), os.path.join(d, "out"), os.path.join(d, "sku.yml")
            try:
                with contextlib.redirect_stdout(io.StringIO()):
                    sku.gen("oneplus-fajita-t9", "HEAD")
            finally:
                sku.FILES, sku.OUT, sku.KAS = saved
            self.assertEqual(sorted(os.listdir(os.path.join(d, "files"))), ["partitions.conf", "profile.json", "sku.json", "source.txt", "watchdog.conf"])
            self.assertEqual(read(d, "files", "source.txt"), f"{head}, no changes\n")
        recipe = read(sku.LAYER, "recipes-phone", "phone-sku", "phone-sku_0.1.bb")
        self.assertIn("file://source.txt", recipe)
        self.assertIn("${datadir}/phone/source.txt", recipe)

    def test_files_for_the_chroot(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(sku.files("oneplus-fajita-t9", d), ["phone.env", "profile.json", "sku.json"])
            r = sku.resolve("oneplus-fajita-t9")
            self.assertEqual(json.loads(read(d, "sku.json")), sku.sku_json(r))
            env = read(d, "phone.env")
            self.assertIn("KARIO_COMPATIBLE=oneplus,fajita\n", env)
            self.assertIn("KARIO_BUTTONS='pm8941_pwrkey|Volume keys'\n", env)
            # The board's own image first; the generic machines' run too.
            self.assertIn("KARIO_MACHINE=oneplus-fajita\n", env)
            self.assertIn("KARIO_GENERIC_MACHINES='kario-qcom qemuarm64'\n", env)
            out = subprocess.run(["sh", "-c", f". {d}/phone.env && echo \"$KARIO_BUTTONS\""], capture_output=True, text=True)
            self.assertEqual(out.stdout, "pm8941_pwrkey|Volume keys\n")
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(sku.files("qemu-t9", d), ["profile.json", "sku.json"])

    # The 6T's sound card: the board file's [audio] goes into sku.json (the
    # Shell's own audiod plays to its pcm) and, for the chroot kit's setup,
    # into phone.env; boards without one have neither.
    def test_audio(self):
        r = sku.resolve("oneplus-fajita-t9")
        a = sku.sku_json(r)["audio"]
        self.assertEqual(a, {k: v for k, v in r["board"]["audio"].items() if k in sku.AUDIO})
        self.assertEqual(list(a), [k for k in sku.AUDIO if k in a])
        self.assertEqual((a["pcm"], a["card"], a["verb"], a["device"]), ("plughw:0,2", "hw:0", "HiFi", "Speaker"))
        self.assertNotIn("audio", sku.sku_json(sku.resolve("qemu-t9")))
        self.assertNotIn("audio", sku.sku_json(sku.resolve("xiaomi-mido-t9")))
        with tempfile.TemporaryDirectory() as d:
            sku.files("oneplus-fajita-t9", d)
            out = subprocess.run(["sh", "-c", f'. {d}/phone.env && echo "$KARIO_AUDIO_PCM|$KARIO_AUDIO_CARD|$KARIO_AUDIO_VERB|$KARIO_AUDIO_DEVICE"'],
                                 capture_output=True, text=True)
            j = json.loads(read(d, "sku.json"))["audio"]
            self.assertEqual(out.stdout, f"{j['pcm']}|{j['card']}|{j['verb']}|{j['device']}\n")
        with tempfile.TemporaryDirectory() as d:
            sku.files("xiaomi-mido-t9", d)
            self.assertIn("KARIO_AUDIO_VERB=''\n", read(d, "phone.env"))

    # What a test on the phone turns down before its media cases play (the
    # board's quiet_controls and quiet_level): the kit's alone, in
    # phone.env; not sku.json's, not the machine's.
    def test_quiet(self):
        r = sku.resolve("oneplus-fajita-t9")
        self.assertNotIn("quiet_controls", sku.sku_json(r)["audio"])
        with tempfile.TemporaryDirectory() as d:
            sku.files("oneplus-fajita-t9", d)
            out = subprocess.run(["sh", "-c", f'. {d}/phone.env && echo "$KARIO_AUDIO_QUIET|$KARIO_AUDIO_QUIET_LEVEL"'],
                                 capture_output=True, text=True)
            self.assertEqual(out.stdout, "RX0 Digital|RX1 Digital|RX2 Digital|RX3 Digital|RX4 Digital|15%\n")
        with tempfile.TemporaryDirectory() as d:
            sku.files("xiaomi-mido-t9", d)
            self.assertIn("KARIO_AUDIO_QUIET=''\n", read(d, "phone.env"))
        a = sku.load("boards", "oneplus-fajita")["audio"]
        board = sku.load("boards", "xiaomi-mido")
        for audio, why in (
            ({**a, "quiet_controls": "RX0 Digital"}, "quiet_controls are the card's mixer controls"),
            ({**a, "quiet_controls": ["RX0 'Digital'"]}, "quiet_controls are the card's mixer controls"),
            ({**a, "quiet_controls": []}, "quiet_controls are the card's mixer controls"),
            ({**a, "quiet_level": "15"}, "quiet_level = '15' is not a level"),
            ({**a, "quiet_level": "150%"}, "quiet_level = '150%' is not a level"),
            ({k: v for k, v in a.items() if k != "quiet_level"}, "quiet_controls needs quiet_level"),
            ({k: v for k, v in a.items() if k != "quiet_controls"}, "quiet_level needs quiet_controls"),
            ({"pcm": "plughw:0,0", "quiet_controls": ["Master"], "quiet_level": "10%"}, "quiet_controls needs card"),
        ):
            with self.subTest(audio=audio), self.assertRaisesRegex(sku.Bad, why):
                sku.check_audio("x", dict(board, audio=audio), ("m.conf", ""))
        self.assertIn("audio.quiet_controls", sku.load("boards", "oneplus-fajita")["unverified"])

    # The hardware watchdog as systemd arms it: the 6T's takes 32 s at most,
    # so its reboot watchdog is 30 s (systemd's 10 min is refused by the
    # driver); QEMU's board leaves systemd's own. Both go into the image as
    # a system.conf drop-in, which phone-sku installs.
    def test_watchdog(self):
        fajita, qemu = sku.load("boards", "oneplus-fajita"), sku.load("boards", "qemuarm64")
        self.assertEqual(sku.watchdog_conf(fajita).splitlines()[1:], ["[Manager]", "RebootWatchdogSec=30s"])
        self.assertEqual(sku.watchdog_conf(qemu).splitlines()[1:], ["# None given: systemd's own."])
        self.assertIn("watchdog.reboot", fajita["unverified"])
        for good in ({}, {"reboot": "off"}, {"runtime": "20s", "reboot": "default", "kexec": "1min"},
                     {"max_timeout": 32, "reboot": "32s"}, {"max_timeout": 600}, {"max_timeout": 32, "reboot": "default"}):
            sku.check_watchdog("x", dict(qemu, watchdog=good))
        for bad, why in (
            ({"max_timeout": 32}, "reboot 10min \\(systemd default\\) is more than the watchdog takes \\(32 s\\)"),
            ({"max_timeout": 32, "reboot": "1min"}, "reboot 1min is more than the watchdog takes"),
            ({"max_timeout": 32, "reboot": "off", "runtime": "33s"}, "runtime 33s is more"),
            ({"reboot": "10"}, "reboot = '10' is not off, default or a time"),
            ({"reboot": 30}, "reboot = 30 is not off"),
            ({"max_timeout": "32"}, "max_timeout is the longest timeout"),
            ({"shutdown": "off"}, "unknown 'shutdown'"),
            ("off", "watchdog is a table"),
        ):
            with self.subTest(watchdog=bad), self.assertRaisesRegex(sku.Bad, why):
                sku.check_watchdog("x", dict(qemu, watchdog=bad))
        recipe = read(sku.LAYER, "recipes-phone", "phone-sku", "phone-sku_0.1.bb")
        self.assertIn("file://watchdog.conf", recipe)
        self.assertIn("${systemd_unitdir}/system.conf.d/50-phone-watchdog.conf", recipe)

    # What the Shell reads (shell/sku.go) has every field the generator writes.
    def test_audio_in_the_shell(self):
        src = read(sku.FW, "shell", "sku.go")
        self.assertIn('json:"audio,omitempty"', src)
        for k in sku.AUDIO:
            self.assertRegex(src, rf'json:"{k}(,omitempty)?"')

    # One source: the Yocto machine's PHONE_AUDIO_* are the board's [audio],
    # and a difference either way is refused.
    def test_audio_is_the_machines(self):
        board = sku.load("boards", "oneplus-fajita")
        path, text = sku.machine_conf(board)
        sku.check_audio("x", board, (path, text))
        for k, var in sku.AUDIO.items():
            self.assertIn(f'{var} = "{board["audio"][k]}"\n', text)
        for conf, why in (
            (text.replace('PHONE_AUDIO_PCM = "plughw:0,2"', 'PHONE_AUDIO_PCM = "plughw:0,0"'), "pcm is 'plughw:0,2'.*PHONE_AUDIO_PCM = 'plughw:0,0'"),
            (text.replace('PHONE_AUDIO_UCM_CALL_MIC = "Mic"\n', ""), "call_mic is 'Mic'.*PHONE_AUDIO_UCM_CALL_MIC = None"),
            (text + 'PHONE_AUDIO_HEADSET = "Headset"\n', "PHONE_AUDIO_HEADSET is none of the board's"),
        ):
            with self.subTest(why=why), self.assertRaisesRegex(sku.Bad, why):
                sku.check_audio("x", board, (path, conf))
        for over, why in (
            ({**board["audio"], "device": "Speaker1"}, "device is 'Speaker1'.*PHONE_AUDIO_UCM_DEVICE = 'Speaker'"),
            (None, "has no \\[audio\\], .*oneplus-fajita.conf sets PHONE_AUDIO_CALL_SPEAKER_PCM, PHONE_AUDIO_EARPIECE_PCM"),
        ):
            with self.subTest(why=why), self.assertRaisesRegex(sku.Bad, why):
                self.resolve(board_over={"audio": over})

    def test_audio_refused(self):
        a = sku.load("boards", "oneplus-fajita")["audio"]
        board = sku.load("boards", "xiaomi-mido")
        for audio, why in (
            ({"pcm": "default"}, "pcm = 'default' is not an ALSA PCM"),
            ({"pcm": "plughw:0,2", "card": "0", "verb": "HiFi"}, "card = '0' is not a sound card"),
            ({"pcm": "plughw:0,2", "card": "hw:0", "verb": 'Hi"Fi'}, "is not a UCM verb or device"),
            ({"pcm": "plughw:0,2", "card": "hw:0"}, "card needs verb"),
            ({"pcm": "plughw:0,2", "verb": "HiFi"}, "verb needs card"),
            ({"pcm": "plughw:1,2", "card": "hw:0", "verb": "HiFi"}, "pcm plughw:1,2 is not on card hw:0"),
            ({k: v for k, v in a.items() if k != "earpiece"}, "call_verb needs earpiece"),
            ({k: v for k, v in a.items() if k not in ("call_verb", "earpiece", "earpiece_pcm", "call_speaker", "call_speaker_pcm")}, "call_mic needs call_verb"),
            ({k: v for k, v in a.items() if k != "pcm"}, "no pcm"),
            ({**a, "headset": "Headset"}, "unknown 'headset'"),
            ("plughw:0,2", "audio is a table"),
        ):
            with self.subTest(audio=audio), self.assertRaisesRegex(sku.Bad, why):
                sku.check_audio("x", dict(board, audio=audio))
        sku.check_audio("x", dict(board, audio={"pcm": "plughw:0,1"}))

    # The 6T's modem asks the system for its IMS (VoLTE) bearers: the
    # board's ims goes into the SKU's kas overlay as PHONE_IMS, from which
    # the machine installs 81voltd and the carrier configuration's oneshot;
    # without ims nothing sets PHONE_IMS and the machine installs neither.
    def test_ims(self):
        r = sku.resolve("oneplus-fajita-t9")
        board = r["board"]
        self.assertIn('PHONE_IMS = "81voltd"\n', sku.kas(r))
        self.assertIn("ims", board["unverified"])
        path, text = sku.machine_conf(board)
        sku.check_ims("x", board, (path, text))
        self.assertIn("PHONE_IMS_PACKAGES = \"${@'81voltd phone-qcom-pdc' if d.getVar('PHONE_IMS') == '81voltd' else ''}\"", text)
        self.assertIn("${PHONE_IMS_PACKAGES}", text)
        self.assertIn('PHONE_IMS ??= ""', text)
        self.assertNotIn("PHONE_IMS", sku.kas(sku.resolve("qemu-t9")))
        self.assertNotIn("ims", sku.load("boards", "oneplus-enchilada"))
        without = {k: v for k, v in board.items() if k != "ims"}
        sku.check_ims("x", without, (path, text))
        mido = sku.load("boards", "xiaomi-mido")
        for b, conf, why in (
            ({**mido, "ims": "imsd"}, None, "ims 'imsd' is not one of 81voltd"),
            ({**mido, "ims": "81voltd", "modem": "sim"}, None, "opens the bearers through the modemmanager backend"),
            (board, (path, text.replace("PHONE_IMS", "PHONE_VOLTE")), "has ims = '81voltd', .*oneplus-fajita.conf does not install it from PHONE_IMS"),
            (board, (path, text.replace("    ${PHONE_IMS_PACKAGES} \\\n", "    81voltd \\\n")), "installs 81voltd whatever the SKU"),
            (without, (path, text + 'IMAGE_INSTALL:append = " 81voltd"\n'), "installs 81voltd whatever the SKU"),
        ):
            with self.subTest(why=why), self.assertRaisesRegex(sku.Bad, why):
                sku.check_ims("x", b, conf)
        with self.assertRaisesRegex(sku.Bad, "ims 'yes' is not one of"):
            self.resolve(board_over={"ims": "yes"})

    # A board's modem is one of the backends telephonyd is built with (the
    # names in telephonyd/src/backend.cpp); anything else would leave the
    # phone without telephony at boot.
    def test_modem(self):
        built = re.findall(r'BackendKind\{"([\w-]+)"', read(sku.FW, "telephonyd", "src", "backend.cpp"))
        self.assertEqual(tuple(built), sku.MODEMS)
        self.assertEqual(sku.sku_json(sku.resolve("qemu-t9"))["modem"], "sim")
        self.assertEqual(sku.sku_json(sku.resolve("oneplus-fajita-t9"))["modem"], "modemmanager")
        mido = sku.load("boards", "xiaomi-mido")
        sku.check_modem("x", {k: v for k, v in mido.items() if k != "modem"})
        for good in sku.MODEMS:
            sku.check_modem("x", dict(mido, modem=good))
        for bad in ("qmi", "android-radio", "", 1):
            with self.subTest(modem=bad), self.assertRaisesRegex(sku.Bad, "modem .* is not one of sim, modemmanager"):
                sku.check_modem("x", dict(mido, modem=bad))
        with self.assertRaisesRegex(sku.Bad, "modem 'vendor' is not one of"):
            self.resolve(board_over={"modem": "vendor"})

    # The 6T's alert slider: the board names its input device ([input]
    # sound_switch), sku.json carries it for the Shell (which hands it to
    # the renderer) and phone.env for the chroot kit's facts; boards
    # without one have neither, and a name that is no device name, or a
    # button's, is refused.
    def test_sound_switch(self):
        r = sku.resolve("oneplus-fajita-t9")
        self.assertEqual(sku.sku_json(r)["sound_switch"], "Alert slider")
        self.assertIn("input.sound_switch", r["board"]["unverified"])
        self.assertIn("KARIO_SOUND_SWITCH='Alert slider'\n", sku.phone_env(r))
        for s in sku.names("skus"):
            if s != "oneplus-fajita-t9":
                self.assertNotIn("sound_switch", sku.sku_json(sku.resolve(s)), s)
        self.assertIn("KARIO_SOUND_SWITCH=''\n", sku.phone_env(sku.resolve("xiaomi-mido-t9")))
        self.assertNotIn("sound_switch", json.dumps(json.loads(sku.sims_json())))
        board = sku.load("boards", "oneplus-fajita")
        for s, why in (
            ("", "is an input device's name"),
            (" Alert slider", "is an input device's name"),
            ("Alert|slider", "is an input device's name"),
            ("Alert\nslider", "is an input device's name"),
            ("x" * 80, "is an input device's name"),
            (["Alert slider"], "is an input device's name"),
            ("Volume keys", "is a button and the sound switch"),
        ):
            with self.subTest(s=s), self.assertRaisesRegex(sku.Bad, why):
                sku.check_input_devices("x", dict(board, input={**board["input"], "sound_switch": s}))
        with self.assertRaisesRegex(sku.Bad, "unknown 'slider'"):
            self.resolve(board_over={"input": {"buttons": [], "slider": "Alert slider"}})
        # A board that is not a whole phone may have one too.
        r = sku.resolve("kario-t9-sd662-2g")
        r = dict(r, board=dict(r["board"], input={"sound_switch": "gpio-keys-slider"}))
        self.assertEqual(sku.sku_json(r)["sound_switch"], "gpio-keys-slider")
        src = read(sku.FW, "shell", "sku.go")
        self.assertIn('json:"sound_switch,omitempty"', src)

    # A whole phone's own outer display ([outer_display] of its board), as
    # a display file has it; the SKU cannot name one.
    def test_outer_display(self):
        outer = {"id": "cover", "name": "Cover", "panel": "sharp,ls013b7dh05", "width": 144, "height": 168,
                 "kind": "memory-lcd", "color": "mono", "drm_driver": "sharp_memory_display"}
        j = sku.sku_json(self.resolve(board_over={"outer_display": outer}))
        self.assertEqual(j["outer"], {"display": "cover", "width": 144, "height": 168, "kind": "memory-lcd",
                                      "color": "mono", "drm_driver": "sharp_memory_display"})
        for bad, why in (
            ({"spi_max_hz": 1}, "unknown 'spi_max_hz'"),
            ({"name": ""}, "has no name"),
            ({"drm_driver": None}, "names its DRM driver"),
            ({"kind": "crt"}, "kind 'crt'"),
            ({"panel": "Sharp"}, "panel is a device tree compatible"),
        ):
            o = {k: v for k, v in {**outer, **bad}.items() if v is not None}
            with self.subTest(bad=bad), self.assertRaisesRegex(sku.Bad, why):
                self.resolve(board_over={"outer_display": o})

    # The 6T's kernel has what the slider's driver needs, built in.
    def test_sound_switch_kernel(self):
        have = sku.machine_kconfig(sku.load("boards", "oneplus-fajita"))
        for opt in ("CONFIG_KEYBOARD_GPIO", "CONFIG_INPUT_EVDEV"):
            self.assertEqual(have.get(opt), "y", opt)

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


class ReferenceTest(unittest.TestCase):
    """The open reference phones (24-sku-framework.md §9): a board whose
    machine is not this layer's, its DSI endpoint's own label, and the
    [[part]] table."""

    PART = {"kind": "light", "name": "VCNL4040", "bus": "i2c", "compatible": "vishay,vcnl4040",
            "driver": "drivers/iio/light/vcnl4000.c"}

    def resolve(self, ident, **overs):
        """resolve with the fields of the SKU's board, display, ... (by
        kind: boards=, displays=; or by name: dmt028=) replaced, a field
        set to None left out."""
        real = sku.load

        def patched(kind, name):
            data = real(kind, name)
            for key in (kind, name):
                if key in overs:
                    data = {k: v for k, v in {**data, **overs[key]}.items() if v is not None}
            return data

        sku.load = patched
        try:
            return sku.resolve(ident)
        finally:
            sku.load = real

    def parts(self, *parts, src=""):
        board = sku.load("boards", "qemuarm64")
        sku.check_parts("x", dict(board, part=list(parts)), src)

    def test_the_reference_phones(self):
        for ident, device, rotate in (("ref-qwerty-qcm6490-4g", "bar-qwerty", 90), ("ref-t9-qcm6490-4g", "bar-t9", 0)):
            r = sku.resolve(ident)
            j = sku.sku_json(r)
            self.assertEqual((j["board"], j["device"], j.get("rotate", 0), j["memory_mb"]), ("qcm6490-ref", device, rotate, 4096))
            self.assertEqual((j["region"], j["product"], j["modem"]), ("", "ref", "modemmanager"))
            self.assertEqual(j["leds"], {"notify": {"name": "white:status"}})
            self.assertEqual((j["backlight"], j["keyboard_backlight"], j["light_sensor"]),
                             ("backlight", "white:kbd_backlight", "vcnl4000"))
            self.assertNotIn("part", j)
            self.assertIsNone(sku.machine_conf(r["board"]))
            self.assertNotIn("kario-qcm6490", sku.generic_machines())
            self.assertIn("machine: kario-qcm6490", sku.kas(r))
            o = sku.overlay(r)
            self.assertIn("remote-endpoint = <&mdss_dsi0_out>;", o)
            self.assertIn("&mdss_dsi0_out {", o)
            self.assertNotIn("mdss_dsi_out", o)

    # The DSI host's endpoint is <panel_dsi>_out unless the board names it.
    def test_panel_dsi_out(self):
        pro1x = sku.load("boards", "sm6115-fxtec-pro1x")["wiring"]
        self.assertEqual(sku.dsi_out(pro1x), "mdss_dsi0_out")
        self.assertIn("&mdss_dsi0_out {", sku.overlay(sku.resolve("kario-t9-sd662-2g")))
        ref = sku.load("boards", "qcm6490-ref")["wiring"]
        self.assertEqual(sku.dsi_out(ref), "mdss_dsi0_out")
        self.assertIn("mdss_dsi0_out", sku.wiring_labels(ref))
        self.assertNotIn("mdss_dsi_out", sku.wiring_labels(ref))

    # phone-qcom.cfg is the kario-qcom machine's kernel: a board of another
    # layer's machine is not held to it, one of this layer's is.
    def test_kconfig_of_this_layers_machine(self):
        absent = {"kconfig": "CONFIG_NOT_IN_PHONE_QCOM"}
        self.resolve("ref-t9-qcm6490-4g", displays=absent)
        with self.assertRaisesRegex(sku.Bad, "CONFIG_NOT_IN_PHONE_QCOM is not in"):
            self.resolve("kario-t9-sd662-2g", displays=absent)
        self.resolve("kario-t9-sd662-2g", displays=absent, boards={"machine": "kario-elsewhere"})

    def test_parts(self):
        self.parts()
        self.parts(self.PART, dict(self.PART, kind=["light", "proximity"]),
                   dict(self.PART, kind="camera", facing="back", bus="csi"),
                   dict(self.PART, driver=["drivers/iio/light/vcnl4000.c", "sound/soc/codecs/wcd-mbhc-v2.c"]),
                   {"kind": "isp", "name": "Spectra ISP", "bus": "soc", "driver": "none"})
        p = self.PART
        for bad, why in (
            ({"name": "x"}, "a list of tables"),
            ([dict(p, name="")], "has no name"),
            ([dict(p, name=" x")], "has no name"),
            ([{k: v for k, v in p.items() if k != "name"}], "has no name"),
            ([dict(p, address=0x60)], "unknown 'address'"),
            ([dict(p, kind="toaster")], "kind"),
            ([dict(p, kind=[])], "kind"),
            ([dict(p, kind=["light", "light"])], "kind"),
            ([dict(p, bus="can")], "bus"),
            ([{k: v for k, v in p.items() if k != "bus"}], "bus"),
            ([dict(p, kind="camera")], "faces front or back"),
            ([dict(p, facing="front")], "faces front or back"),
            ([dict(p, kind="camera", facing="up")], "faces front or back"),
            ([dict(p, driver="none")], "binds a compatible"),
            ([{k: v for k, v in p.items() if k != "driver"}], "is not \"none\""),
            ([dict(p, driver=[])], "is not \"none\""),
            ([dict(p, driver=["none"])], "is not \"none\""),
            ([dict(p, driver="drivers/iio/light/vcnl4000.h")], "is not \"none\""),
            ([dict(p, driver="/drivers/iio/light/vcnl4000.c")], "is not \"none\""),
            ([dict(p, driver="drivers/../etc/x.c")], "is not \"none\""),
            ([dict(p, driver="fs/x.c")], "is not \"none\""),
            ([{k: v for k, v in p.items() if k != "compatible"}], "not a device tree compatible"),
            ([dict(p, compatible="Vishay,VCNL4040")], "not a device tree compatible"),
            ([dict(p, compatible="vishay,vcnl4040 ")], "not a device tree compatible"),
        ):
            with self.subTest(bad=bad), self.assertRaisesRegex(sku.Bad, why):
                sku.check_parts("x", dict(sku.load("boards", "qemuarm64"), part=bad), "")

    # With a kernel tree, each driver file is in it and one matches the
    # compatible.
    def test_parts_against_a_kernel_tree(self):
        with tempfile.TemporaryDirectory() as src:
            os.makedirs(os.path.join(src, "drivers", "iio", "light"))
            with open(os.path.join(src, "drivers", "iio", "light", "vcnl4000.c"), "w") as f:
                f.write('{ .compatible = "vishay,vcnl4040", },\n')
            self.parts(self.PART, {"kind": "isp", "name": "ISP", "bus": "soc", "driver": "none"}, src=src)
            with self.assertRaisesRegex(sku.Bad, "is not in the kernel tree"):
                self.parts(dict(self.PART, driver=["drivers/iio/light/vcnl4000.c", "drivers/iio/light/ltr501.c"]), src=src)
            with self.assertRaisesRegex(sku.Bad, "does not match 'vishay,vcnl4000'"):
                self.parts(dict(self.PART, compatible="vishay,vcnl4000"), src=src)
            with self.assertRaisesRegex(sku.Bad, "does not match 'vishay,vcnl404'"):
                self.parts(dict(self.PART, compatible="vishay,vcnl404"), src=src)

    def test_parts_command(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(sku.main(["parts", "qcm6490-ref"]), 0)
        lines = out.getvalue().splitlines()
        self.assertEqual(len(lines), 1 + len(sku.load("boards", "qcm6490-ref")["part"]))
        self.assertTrue(any(l.startswith("camera(back) ") and "sony,imx258" in l for l in lines))
        self.assertTrue(any(re.search(r"^isp +soc +- +none  QCM6490 Spectra ISP$", l) for l in lines))
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(sku.main(["parts", "qemuarm64"]), 0)

    # Section 9 of 24-sku-framework.md is the reference board's hardware:
    # it names every part's compatible, in both languages.
    def test_docs_list_the_parts(self):
        for doc in (("docs", "24-sku-framework.md"), ("docs", "en", "24-sku-framework.md")):
            text = read(sku.FW, *doc)
            for b in sku.names("boards"):
                for p in sku.load("boards", b).get("part", []):
                    if "compatible" in p:
                        self.assertIn(f"`{p['compatible']}`", text, f"{os.path.join(*doc)}: {b} {p['name']}")
            for d in sku.names("displays"):
                for c in sku.load("displays", d).get("compatible", []):
                    self.assertIn(f"`{c}`", text, f"{os.path.join(*doc)}: display {d}")

    # The flip (§9.10): the outer display, the lid and the OK key's
    # fingerprint in sku.json, the outer panel on its SPI bus and the lid's
    # gpio-keys in the overlay.
    def test_the_flip(self):
        r = sku.resolve("ref-flip-qcm6490-4g")
        j = sku.sku_json(r)
        self.assertEqual((j["board"], j["device"], j["display"], j["keypad"], j["memory_mb"]),
                         ("qcm6490-flip", "bar-t9", "dmt028", "t9-fp-v1", 4096))
        self.assertEqual(j["outer"], {"display": "ls013b7dh05", "width": 144, "height": 168, "kind": "memory-lcd",
                                      "color": "mono", "drm_driver": "sharp_memory_display", "width_mm": 20, "height_mm": 24})
        self.assertEqual((j["lid"], j["fingerprint_at"]), ("Lid", "ok-key"))
        self.assertNotIn("fingerprint", j)
        self.assertNotIn("leds", j)
        o = sku.overlay(r)
        for want in ("&spi10 {", 'compatible = "sharp,ls013b7dh05";', "spi-max-frequency = <1000000>;", "spi-cs-high;",
                     "enable-gpios = <&tlmm 82 0>;", 'sharp,vcom-mode = "pwm";', "pwms = <&pm8350c_pwm 0 1000000000>;",
                     '&pm8350c_pwm {\n\tstatus = "okay";', '&{/} {', 'compatible = "gpio-keys";\n\t\tlabel = "Lid";',
                     "gpios = <&tlmm 78 1>;", "linux,input-type = <5>;", "linux,code = <0>;",
                     "interrupts-extended = <&tlmm 77 8>;"):
            self.assertIn(want, o)
        lines = sku.parts_table(r["board"])
        self.assertTrue(any(re.match(r"lid +gpio +gpio-keys ", l) for l in lines))
        self.assertTrue(any(re.match(r"fingerprint +usb +- +none ", l) for l in lines))
        for ident in ("ref-t9-qcm6490-4g", "ref-qwerty-qcm6490-4g", "kario-t9-sd662-2g", "qemu-t9"):
            r = sku.resolve(ident)
            self.assertFalse({"outer", "lid", "fingerprint_at"} & set(sku.sku_json(r)), ident)
            if not r["board"].get("virtual"):
                self.assertNotIn("sku_outer", sku.overlay(r))
                self.assertNotIn("sku_lid", sku.overlay(r))
        src = read(sku.FW, "shell", "sku.go")
        for tag in ("lid,omitempty", "outer,omitempty", "drm_driver"):
            self.assertIn(f'json:"{tag}', src)
        self.assertNotIn("outer", json.dumps(json.loads(sku.sims_json())))

    def test_outer_display_refused(self):
        flip = "ref-flip-qcm6490-4g"
        w = sku.load("boards", "qcm6490-flip")["wiring"]
        less = lambda *keys: {"wiring": {k: v for k, v in w.items() if k not in keys}}  # noqa: E731
        for overs, why in (
            ({"ls013b7dh05": {"bus": "dsi"}}, "wires an outer panel on SPI, not dsi"),
            ({"ls013b7dh05": {"bus": "i2c"}}, "bus 'i2c' is dsi or spi"),
            ({"ls013b7dh05": {"kind": "crt"}}, "kind 'crt'"),
            ({"ls013b7dh05": {"color": "sepia"}}, "color 'sepia'"),
            ({"ls013b7dh05": {"drm_driver": "Sharp memory"}}, "is a kernel DRM driver's name"),
            ({"ls013b7dh05": {"drm_driver": None}}, "names its DRM driver"),
            ({"ls013b7dh05": {"kconfig": "CONFIG_DRM_PANEL_MIPI_DBI"}}, "the overlay wires CONFIG_TINYDRM_SHARP_MEMORY"),
            ({"ls013b7dh05": {"compatible": "sharp,ls013b7dh05"}}, "compatible is the panel's"),
            ({"ls013b7dh05": {"spi_max_hz": None}}, "spi_max_hz is"),
            ({"ls013b7dh05": {"spi_max_hz": True}}, "spi_max_hz is"),
            ({"ls013b7dh05": {"spi_cs_high": "yes"}}, "spi_cs_high is"),
            ({"ls013b7dh05": {"lanes": 1}}, "lanes on an SPI panel"),
            ({"ls013b7dh05": {"width_mm": None}}, "both or neither"),
            ({"ls013b7dh05": {"width": 0}}, "width is the panel's pixels"),
            ({"ls013b7dh05": {"touchscreen": "x"}}, "nothing reads it"),
            ({"ls013b7dh05": {"refresh": 1}}, "unknown 'refresh'"),
            ({"ls013b7dh05": {"virtual": True}}, "QEMU's displays"),
            ({"dmt028": {"bus": "spi"}}, "wires the main panel on DSI, not spi"),
            ({"dmt028": {"spi_max_hz": 1}}, "spi_max_hz on a DSI panel"),
            ({"boards": less("outer_vcom")}, "has no outer_vcom for display ls013b7dh05"),
            ({"boards": less("outer_spi")}, "has no outer_spi"),
            ({"boards": {"wiring": dict(w, outer_spi="spi 10")}}, "the SPI controller's label"),
            ({"boards": {"wiring": dict(w, outer_enable="tlmm")}}, "a phandle's label and its cells"),
            ({"boards": {"wiring": dict(w, lid_gpio="&tlmm 78 1")}}, "a phandle's label and its cells"),
            ({"boards": {"input": {}}}, "a lid_gpio without \\[input\\] lid"),
            ({"boards": {"outer_display": {"id": "x"}}}, "an \\[outer_display\\] is a whole phone's"),
        ):
            with self.subTest(overs=overs), self.assertRaisesRegex(sku.Bad, why):
                self.resolve(flip, **overs)
        with self.assertRaisesRegex(sku.Bad, "its outer_display is its own"):
            self.resolve("oneplus-fajita-t9", skus={"outer_display": "ls013b7dh05"})
        with self.assertRaisesRegex(sku.Bad, "QEMU's displays"):
            self.resolve("qemu-t9", skus={"outer_display": "ls013b7dh05"})
        if sku.kernel_src():
            with self.assertRaisesRegex(sku.Bad, "has no spi99"):
                self.resolve(flip, boards={"wiring": dict(w, outer_spi="spi99")})
        # The board's outer wiring alone is no outer display, and a board
        # without it takes none.
        self.assertNotIn("outer", sku.sku_json(self.resolve(flip, skus={"outer_display": None})))
        with self.assertRaisesRegex(sku.Bad, "has no outer_spi"):
            self.resolve("ref-t9-qcm6490-4g", skus={"outer_display": "ls013b7dh05"})

    # QEMU's outer display: a second virtio-gpu device of its size.
    def test_virtual_outer_display(self):
        virtual = {"virtual": True, "compatible": None, "kconfig": None, "bus": None, "spi_max_hz": None,
                   "spi_cs_high": None, "drm_driver": "virtio_gpu"}
        r = self.resolve("qemu-t9", skus={"outer_display": "ls013b7dh05"}, ls013b7dh05=virtual)
        self.assertIn("-device virtio-gpu-pci,xres=480,yres=640 -device virtio-multitouch-pci "
                      "-device virtio-gpu-pci,xres=144,yres=168\"", sku.kas(r))
        self.assertEqual(sku.sku_json(r)["outer"]["drm_driver"], "virtio_gpu")
        self.assertNotIn("xres=144", sku.kas(sku.resolve("qemu-t9")))

    # [input] lid: the Hall switch's input device, on any board, refused as
    # the sound switch's name is.
    def test_lid(self):
        board = sku.load("boards", "qcm6490-flip")
        self.assertIn("input.lid", board["unverified"])
        for inp, why in (
            ({"lid": ""}, "lid '' is an input device's name"),
            ({"lid": "Lid|x"}, "is an input device's name"),
            ({"lid": "x" * 80}, "is an input device's name"),
            ({"lid": 1}, "is an input device's name"),
            ({"lid": "gpio-keys", "buttons": ["gpio-keys"]}, "is a button and the lid switch"),
            ({"lid": "Hall", "sound_switch": "Hall"}, "is the sound switch and the lid switch"),
            ({"hall": "Lid"}, "unknown 'hall'"),
        ):
            with self.subTest(inp=inp), self.assertRaisesRegex(sku.Bad, why):
                sku.check_input_devices("x", dict(board, input=inp))
        with self.assertRaisesRegex(sku.Bad, "unknown 'hall'"):
            self.resolve("ref-flip-qcm6490-4g", boards={"input": {"lid": "Lid", "hall": "x"}})
        r = self.resolve("kario-t9-sd662-2g", boards={"input": {"lid": "Lid"}})
        self.assertEqual(sku.sku_json(r)["lid"], "Lid")

    # A keypad's fingerprint sensor: where it is in sku.json (keystored's
    # fingerprint_at); a real board lists the sensor.
    def test_key_fingerprint(self):
        parts = [p for p in sku.load("boards", "qcm6490-flip")["part"] if p["kind"] != "fingerprint"]
        with self.assertRaisesRegex(sku.Bad, "keypad t9-fp-v1 has a fingerprint sensor, board qcm6490-flip no fingerprint part"):
            self.resolve("ref-flip-qcm6490-4g", boards={"part": parts})
        for at in ("power-key", "ok", "", 1):
            with self.subTest(at=at), self.assertRaisesRegex(sku.Bad, "is not one of ok-key"):
                self.resolve("ref-flip-qcm6490-4g", keypads={"fingerprint": at})
        r = self.resolve("qemu-t9", keypads={"fingerprint": "ok-key"})
        self.assertEqual(sku.sku_json(r)["fingerprint_at"], "ok-key")
        with self.assertRaisesRegex(sku.Bad, "no fingerprint part"):
            self.resolve("ref-t9-qcm6490-4g", keypads={"fingerprint": "ok-key"})


class SKUPathTest(unittest.TestCase):
    """KARIO_SKU_PATH (24-sku-framework.md §8.7.1): a private repository's
    SKU tree, which builds use and this tree's git-tracked files never name."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = os.path.join(self.tmp.name, "sku")
        for kind in ("boards", "skus", "products"):
            os.makedirs(os.path.join(self.dir, kind))
        board = read(sku.SKUS, "boards", "qcm6490-ref.toml")
        board = board.replace('id = "qcm6490-ref"', 'id = "secret-board"')
        board = board.replace('machine = "kario-qcm6490"', 'machine = "secret-machine"')
        board = board.replace('dtb = "qcm6490-fairphone-fp5.dtb"', 'dtb = "qcm6490-secret.dtb"')
        self.put("boards", "secret-board", board)
        self.put("products", "secret", 'id = "secret"\nname = "Secret product"\n')
        s = read(sku.SKUS, "skus", "ref-qwerty-qcm6490-4g.toml")
        s = s.replace('id = "ref-qwerty-qcm6490-4g"', 'id = "secret-qwerty"')
        s = s.replace('board = "qcm6490-ref"', 'board = "secret-board"').replace('product = "ref"', 'product = "secret"')
        self.put("skus", "secret-qwerty", s)
        os.environ[sku.SKU_PATH] = self.dir

    def tearDown(self):
        os.environ.pop(sku.SKU_PATH, None)
        self.tmp.cleanup()

    def put(self, kind, ident, text):
        with open(os.path.join(self.dir, kind, ident + ".toml"), "w") as f:
            f.write(text)

    def test_a_private_sku_resolves(self):
        self.assertIn("secret-qwerty", sku.names("skus"))
        self.assertNotIn("secret-qwerty", sku.names("skus", own=True))
        self.assertIn("ref-qwerty-qcm6490-4g", sku.names("skus"))
        r = sku.resolve("secret-qwerty")
        self.assertEqual((r["board"]["id"], r["product"]["id"], r["display"]["id"]), ("secret-board", "secret", "dmt028"))
        self.assertIn("machine: secret-machine", sku.kas(r))
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(sku.main(["check"]), 0)
            self.assertEqual(sku.main(["list"]), 0)
        self.assertIn("secret-qwerty", out.getvalue())

    # The table git tracks is this tree's alone; the files gen writes are
    # all ignored by git.
    def test_never_in_this_trees_tracked_files(self):
        self.assertEqual(read(sku.SIMS), sku.sims_json())
        self.assertNotIn("secret", sku.sims_json())
        saved = sku.SIMS, sku.FILES, sku.OUT, sku.KAS
        with tempfile.TemporaryDirectory() as d:
            sku.SIMS = os.path.join(d, "skus.json")
            sku.FILES, sku.OUT, sku.KAS = os.path.join(d, "files"), os.path.join(d, "out"), os.path.join(d, "sku.yml")
            try:
                with contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(sku.main(["sims"]), 0)
                    self.assertEqual(sku.main(["gen", "secret-qwerty"]), 0)
            finally:
                sku.SIMS, sku.FILES, sku.OUT, sku.KAS = saved
            self.assertEqual(read(d, "skus.json"), read(sku.SIMS))
            self.assertIn('"id": "secret-qwerty"', read(d, "files", "sku.json"))
            self.assertIn("machine: secret-machine", read(d, "sku.yml"))
        ignored = [os.path.join(sku.FILES, "sku.json"), os.path.join(sku.OUT, "secret-qwerty", "sku.json"), sku.KAS]
        res = subprocess.run(["git", "check-ignore", "--no-index", *ignored], cwd=sku.FW, capture_output=True, text=True)
        self.assertEqual(sorted(res.stdout.split()), sorted(ignored))
        res = subprocess.run(["git", "check-ignore", "--no-index", "-q", sku.SIMS], cwd=sku.FW)
        self.assertEqual(res.returncode, 1, "skus.json is tracked")

    def test_refused(self):
        self.put("boards", "qcm6490-ref", 'id = "qcm6490-ref"\n')
        with self.assertRaisesRegex(sku.Bad, "board 'qcm6490-ref' is in sku/boards/qcm6490-ref.toml and "):
            sku.load("boards", "qcm6490-ref")
        os.remove(os.path.join(self.dir, "boards", "qcm6490-ref.toml"))
        with open(os.path.join(self.dir, "keys.toml"), "w") as f:
            f.write("[keys]\n")
        with self.assertRaisesRegex(sku.Bad, "keys.toml: the key names are this tree's"):
            sku.names("skus")
        os.remove(os.path.join(self.dir, "keys.toml"))
        for path, why in ((os.path.join(self.tmp.name, "none"), "is no directory"),
                          (sku.SKUS, "is there twice"), (f"{self.dir}:{self.dir}", "is there twice")):
            os.environ[sku.SKU_PATH] = path
            with self.subTest(path=path), self.assertRaisesRegex(sku.Bad, why):
                sku.names("skus")
        os.environ[sku.SKU_PATH] = f":{self.dir}:"
        self.assertIn("secret-qwerty", sku.names("skus"))
        os.environ.pop(sku.SKU_PATH)
        with self.assertRaisesRegex(sku.Bad, r"no sku 'secret-qwerty' \(sku/skus/secret-qwerty.toml\)"):
            sku.resolve("secret-qwerty")

    # A board's tree the kernel source lacks: an error for this layer's
    # machine, left to the build for another layer's (which adds the tree).
    def test_a_tree_another_layer_adds(self):
        with tempfile.TemporaryDirectory() as src:
            os.makedirs(os.path.join(src, "arch", "arm64", "boot", "dts", "qcom"))
            self.assertIsNone(sku.dt_labels(src, "qcm6490-secret.dtb", missing_ok=True))
            with self.assertRaisesRegex(sku.Bad, "no such tree"):
                sku.dt_labels(src, "qcm6490-secret.dtb")
            saved = os.environ.get("PHONE_KERNEL_SRC")
            os.environ["PHONE_KERNEL_SRC"] = src
            real, machine = sku.load, {}

            def patched(kind, name):
                data = real(kind, name)
                if kind != "boards":
                    return data
                return {**{k: v for k, v in data.items() if k != "part"}, **machine}
            sku.load = patched
            try:
                sku.resolve("secret-qwerty")
                machine["machine"] = "kario-qcom"
                with self.assertRaisesRegex(sku.Bad, "qcm6490-secret.dtb: no such tree"):
                    sku.resolve("secret-qwerty")
            finally:
                sku.load = real
                if saved is None:
                    os.environ.pop("PHONE_KERNEL_SRC")
                else:
                    os.environ["PHONE_KERNEL_SRC"] = saved

    # A board that is not a whole phone has only its switches in [input].
    def test_input_of_a_board(self):
        board = sku.load("boards", "secret-board")
        sku.check_input_devices("x", dict(board, input={"sound_switch": "Mute switch", "lid": "Lid"}))
        with self.assertRaisesRegex(sku.Bad, "'buttons': a board that is not a whole phone"):
            sku.check_input_devices("x", dict(board, input={"buttons": ["Volume keys"]}))


class PartitionsTest(unittest.TestCase):
    """A board's partitions by role ([partitions], 43-phone-partitions.md):
    what a flash writes in each layout, what it never writes, and the one
    table the flash script, the initramfs and RAUC take their names from."""

    def board(self, **over):
        b = sku.load("boards", "oneplus-fajita")
        return dict(b, partitions={**b["partitions"], **over})

    def test_the_6t(self):
        b = sku.load("boards", "oneplus-fajita")
        rows = {n: (role, action, image) for n, role, action, image in sku.plan(b, "userdata")}
        self.assertEqual({n for n, r in rows.items() if r[1] == "write"}, {"boot_a", "boot_b", "userdata"})
        self.assertEqual(rows["userdata"], ("data", "write", "rootfs"))
        self.assertEqual(rows["system_a"], ("root", "keep", "-"))
        rows = {n: (role, action, image) for n, role, action, image in sku.plan(b, "ab")}
        self.assertEqual({n for n, r in rows.items() if r[1] == "write"}, {"boot_a", "boot_b", "system_a", "system_b"})
        self.assertEqual((rows["boot_b"][2], rows["system_b"][2]), ("abboot", "slot"))
        self.assertEqual({n for n, r in rows.items() if r[1] == "erase"}, {"dtbo_a", "dtbo_b", "userdata"})
        self.assertEqual([n for n, r in rows.items() if r[1] == "efs"], ["persist", "modemst1", "modemst2", "fsg", "fsc"])
        # The default layout is the one Kario has been flashed with.
        self.assertEqual(sku.plan(b), sku.plan(b, "userdata"))
        for n, r in rows.items():
            if sku.never(n):
                self.assertIn(r[1], ("keep", "efs"), n)

    # What the build takes from the table: RAUC's slots and backend, the
    # initramfs' partitions.conf, the chroot kit's map.
    def test_what_the_build_gets(self):
        r = sku.resolve("oneplus-fajita-t9")
        k = sku.kas(r)
        for line in ('PHONE_SLOT_KEEPER = "abl"', 'PHONE_ROOT_SLOTS = "system_a system_b"', 'PHONE_BOOT_SLOTS = "boot_a boot_b"'):
            self.assertIn(line, k)
        self.assertTrue(sku.partitions_conf(r["board"]).endswith("slots=abl\nroot=system\ndata=userdata\nstate=\n"))
        self.assertIn("KARIO_PARTITIONS='boot_a:boot boot_b:boot dtbo_a:dtbo", sku.phone_env(r))
        q = sku.resolve("qemu-t9")
        self.assertIn('PHONE_ROOT_SLOTS = "rootfs_a rootfs_b"', sku.kas(q))
        self.assertTrue(sku.partitions_conf(q["board"]).endswith("slots=state\nroot=rootfs\ndata=data\nstate=state\n"))
        mido = sku.load("boards", "xiaomi-mido")
        self.assertEqual(sku.partitions_conf(mido), "")
        self.assertNotIn("PHONE_SLOT_KEEPER", sku.kas(sku.resolve("kario-t9-sd662-2g")))

    def test_refused(self):
        part = sku.load("boards", "oneplus-fajita")["partitions"]["part"]

        def with_part(which, **over):
            return [dict(q, **over) if q["name"] == which else q for q in part]

        for over, why in (
            ({"layout": "flat"}, "layout 'flat' is not one"),
            ({"slots": "misc"}, "slots 'misc' is not one of abl, state"),
            ({"part": with_part("frp", name="misc")}, "misc twice"),
            ({"part": with_part("persist", role="data")}, "persist and userdata are both the data"),
            ({"part": [dict(q, name="fsg") if q["name"] == "userdata" else q for q in part if q["name"] != "fsg"]},
             "fsg holds what no flash may write"),
            ({"part": [dict(q, name="modem") if q["name"] == "system" else q for q in part if q["name"] != "modem"]},
             "modem holds what no flash may write"),
            ({"part": with_part("dtbo", name="persistbak")}, "persistbak holds what no flash may write"),
            ({"part": with_part("frp", role="efs")}, "frp: a phone's own partition \\(efs\\) is one no flash writes"),
            ({"part": with_part("boot", name="boot_a")}, "boot_a: an A/B pair is named without its slot suffix"),
            ({"part": with_part("boot", ab=False)}, "no boot pair"),
            ({"part": with_part("system", ab=False)}, "the root is an A/B pair"),
            ({"part": with_part("misc", role="cache")}, "role 'cache' is not one of"),
            ({"part": with_part("misc", name="my-misc")}, "'my-misc' is not a partition name"),
            ({"part": with_part("misc", size=1000)}, "size is the partition's bytes"),
            ({"part": with_part("misc", start=0)}, "unknown 'start'"),
            ({"part": [q for q in part if q["role"] != "data"]}, "layout 'userdata' is not one this board's partitions allow \\(none\\)"),
            ({"part": "boot"}, "part is a list"),
            ({"slots": "state"}, "slots = \"state\" keeps the slot in the state partition: none"),
            ({"bootloader": "abl"}, "unknown 'bootloader'"),
        ):
            with self.subTest(why=why), self.assertRaisesRegex(sku.Bad, why):
                sku.check_partitions("x", self.board(**over))
        sku.check_partitions("x", self.board(part=with_part("misc", size=1 << 20)))
        with self.assertRaisesRegex(sku.Bad, "layout 'flat' is not one its partitions allow"):
            sku.plan(sku.load("boards", "oneplus-fajita"), "flat")
        with self.assertRaisesRegex(sku.Bad, "has no \\[partitions\\]"):
            sku.plan(sku.load("boards", "xiaomi-mido"))

    # One list of what no flash writes, in tools/sku.py and in the flash
    # script, which does not trust the table for it.
    def test_never_is_the_flash_scripts(self):
        text = read(sku.FW, "yocto", "fajita-flash.sh")
        m = re.search(r'^NEVER="([^"]*)"', text, re.M)
        self.assertEqual(set(m.group(1).replace("\\\n", " ").split()), set(sku.NEVER))

    # The emulator's board is its A/B disk's partitions and RAUC's slots;
    # a difference is refused.
    def test_the_emulators_disk(self):
        b = sku.load("boards", "qemuarm64")
        sku.check_partition_files("x", b)
        part = [dict(q, name="system") if q["role"] == "root" else q for q in b["partitions"]["part"]]
        with self.assertRaisesRegex(sku.Bad, "phone-ab.wks.in data, rootfs_a, rootfs_b, state"):
            sku.check_partition_files("x", dict(b, partitions={**b["partitions"], "part": part}))

    # RAUC's configuration where the bootloader keeps the slot: the board's
    # root and boot pairs, filled in as rauc-conf.bbappend does.
    def test_rauc_where_the_bootloader_keeps_the_slot(self):
        r = sku.resolve("oneplus-fajita-t9")
        template = read(sku.RAUC_DIR, "system-abl.conf.in")
        append = read(os.path.dirname(sku.RAUC_DIR), "rauc-conf.bbappend")
        names = sku.part_names(r["board"], "root") + sku.part_names(r["board"], "boot")
        conf = template
        for key, name in zip(("@ROOT_A@", "@ROOT_B@", "@BOOT_A@", "@BOOT_B@"), names):
            self.assertIn(f'"s/{key}/$', append)
            conf = conf.replace(key, name)
        self.assertEqual(re.findall(r"^device=/dev/disk/by-partlabel/(\S+)$", conf, re.M), names)
        self.assertIn("bootloader-custom-backend=/usr/lib/phone/rauc-abl\n", conf)
        self.assertEqual(re.findall(r"^parent=(\S+)$", conf, re.M), ["rootfs.0", "rootfs.1"])
        self.assertNotIn("@", conf.replace("@COMPATIBLE@", ""))

    def test_the_command(self):
        out = subprocess.run([sys.executable, os.path.join(sku.FW, "tools", "sku.py"), "partitions", "oneplus-fajita", "ab"],
                             capture_output=True, text=True)
        self.assertEqual(out.returncode, 0, out.stderr)
        lines = out.stdout.splitlines()
        self.assertEqual(lines[0], "# oneplus-fajita: layout ab, slots abl")
        self.assertIn("system_a root write slot", lines)
        self.assertIn("persist efs efs -", lines)
        out = subprocess.run([sys.executable, os.path.join(sku.FW, "tools", "sku.py"), "partitions", "oneplus-fajita", "flat"],
                             capture_output=True, text=True)
        self.assertEqual(out.returncode, 1)
        self.assertIn("layout 'flat' is not one", out.stderr)


if __name__ == "__main__":
    unittest.main()
