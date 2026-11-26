#!/usr/bin/env python3
"""SKU generator (24-sku-framework.md).

A SKU names a board, a display, a keypad, a memory tier, a region and a
product (fw/sku/). This expands one into what an image needs:

  share/phone/sku.json            the variant, read by phone-shell
  share/phone/source.txt          the commit the image is built from (a phone
                                  test's kit.txt names it)
  profile.d/20-product.json       the product's composition layer
  overlay.dts                     the displays and keypad in the board's
                                  device tree (real boards)
  partitions.conf                 the board's partitions by role, for
                                  phone-initramfs (43-phone-partitions.md)
  watchdog.conf                   the board's hardware watchdog, a systemd
                                  system.conf drop-in
  yocto/kas/generated/sku.yml     the build: machine, QEMU's display, the
                                  slots RAUC writes, ...

  sku.py list                     the SKUs
  sku.py check                    expand every SKU (no files written)
  sku.py gen <sku> [--rev REV]    write the files for one (REV: what SRC=git builds)
  sku.py files <sku> <dir>        only its sku.json, profile.json and, for a
                                  whole phone, phone.env (tools/phone-chroot)
  sku.py package <sku> <images>   collect the firmware package from a build
  sku.py sims                     write the table kario run -sku simulates
                                  (cmd/kario/skus.json)
  sku.py partitions <board> [layout]
                                  the board's partitions, one a line: name,
                                  role, what a flash does to it, the image
                                  it writes (yocto/fajita-flash.sh reads it)
  sku.py parts <board>            the board's parts ([[part]]), one a line:
                                  kind, bus, compatible, mainline driver, name

KARIO_SKU_PATH=<dir>[:<dir>...] adds SKU trees laid out as sku/ is (a
private repository's products): every command sees their SKUs and modules
but sims, which writes only this tree's.
"""
import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tomllib

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKUS = os.path.join(FW, "sku")
LAYER = os.path.join(FW, "yocto", "meta-phone")
FILES = os.path.join(LAYER, "recipes-phone", "phone-sku", "files")
KAS = os.path.join(FW, "yocto", "kas", "generated", "sku.yml")
KCONFIG = os.path.join(LAYER, "recipes-kernel", "linux", "files", "phone-qcom.cfg")
WKS = os.path.join(LAYER, "files", "wic", "phone-ab.wks.in")
RAUC_DIR = os.path.join(LAYER, "dynamic-layers", "rauc", "recipes-core", "rauc", "files")
OUT = os.path.join(FW, "build", "sku")
SIMS = os.path.join(FW, "cmd", "kario", "skus.json")
# More SKU trees, searched after this one (24-sku-framework.md §8.7.1):
# directories laid out as sku/ is (boards/, displays/, keypads/, products/,
# skus/), separated by ':'. A private repository's products (kario-internal's
# sku/) come in so; their ids are not this tree's, they bring no keys.toml,
# and they never go into this tree's git-tracked files (sims).
SKU_PATH = "KARIO_SKU_PATH"

# The form factors of shell/device.go (sku_test.py compares).
DEVICES = {"bar-t9": (480, 640, "t9"), "bar-qwerty": (640, 480, "qwerty"), "communicator": (800, 352, "full")}
MEMORY = {"1g": 1024, "2g": 2048, "3g": 3072, "4g": 4096}
CHIPS = {"tca8418": ("ti,tca8418", "CONFIG_KEYBOARD_TCA8418")}
# The keys every keypad needs, and those of each kind (10 §1).
NEEDED = {"soft-left", "soft-right", "ok", "back", "clear", "call", "end", "up", "down", "left", "right"}
FORM_KEYS = {"t9": set("0123456789") | {"star", "pound"}, "qwerty": set("abcdefghijklmnopqrstuvwxyz") | {"space", "shift"}}

# A whole phone (class = "phone", 24-sku-framework.md §7.4) brings its own
# panel ([display]) and buttons ([input]); the keypad is a USB or Bluetooth
# keyboard, which phone-paint maps as on the developer machine.
PHONE_KEYPAD = {"id": "hid", "name": "USB / Bluetooth keyboard and the phone's buttons", "chip": "hid"}
# A whole phone with a keypad of its own (a KaiOS phone's matrix in its
# device tree) names it in a [keymap]: the code the kernel reports for each
# key -> the key's name in keys.toml. phone-paint reads the key as that
# name's code; codes not in the table are read as they are.
OWN_KEYPAD = {"id": "builtin", "name": "the phone's own keypad (board keymap)", "chip": "builtin"}
KEY_MAX = 0x2ff
PANEL_KEYS = {"id", "name", "panel", "width", "height", "width_mm", "height_mm", "scale", "inset", "touchscreen"}
# [input]: the phone's own buttons, its hardware sound-profile switch and
# its lid switch, each by its input device's name. The switch
# (sound_switch) is either a three-position slider whose device reports
# ABS_SND_PROFILE (silent, vibrate, ring) or a two-position mute switch
# whose device reports SW_MUTE_DEVICE (set: silent, else ring); the
# renderer reads it and the Shell sets the sound mode by it. The lid switch
# (lid) is a flip's Hall sensor, whose device reports SW_LID. A board that
# is not a whole phone has only the switches here (its keys are the
# keypad's).
INPUT_KEYS = {"buttons", "sound_switch", "lid"}
INPUT_DEVICES = {"sound_switch": "sound switch", "lid": "lid switch"}
# A display file. bus: how the SoC drives the panel (the overlay wires a
# main panel on DSI and an outer one on SPI); kind: what lights it and what
# holds its picture, which the Shell's use of it follows (an LCD's
# backlight and an OLED's pixels draw power while it shows anything; a
# memory LCD is reflective and holds its picture at microwatts; e-paper
# holds it unpowered); color: what a pixel shows (a mono panel is drawn
# without anti-aliasing); drm_driver: the kernel DRM driver of its device,
# by which the renderer tells two panels apart.
DISPLAY_KEYS = {"id", "name", "compatible", "kconfig", "width", "height", "width_mm", "height_mm", "lanes", "virtual",
                "touchscreen", "bus", "kind", "color", "drm_driver", "spi_max_hz", "spi_cs_high"}
DISPLAY_KINDS = ("lcd", "oled", "memory-lcd", "e-paper")
DISPLAY_COLORS = ("rgb", "gray", "mono")
# A whole phone's own outer display ([outer_display] of its board).
OUTER_PANEL_KEYS = {"id", "name", "panel", "width", "height", "width_mm", "height_mm", "kind", "color", "drm_driver"}
# A board's sound card ([audio], 20-audio.md §10): the PCM phone-audiod
# plays media to, the card's ALSA UCM set-up for media and, with a call
# verb, the call's earpiece, speaker (each with the PCM it plays) and
# microphone. A Yocto machine passes the same values to the image's
# phone-audiod as these variables (phone-qcom-audio); check_audio holds
# them equal.
AUDIO = {
    "pcm": "PHONE_AUDIO_PCM", "card": "PHONE_AUDIO_UCM_CARD", "verb": "PHONE_AUDIO_UCM_VERB",
    "device": "PHONE_AUDIO_UCM_DEVICE", "call_verb": "PHONE_AUDIO_UCM_CALL_VERB",
    "earpiece": "PHONE_AUDIO_UCM_EARPIECE", "earpiece_pcm": "PHONE_AUDIO_EARPIECE_PCM",
    "call_speaker": "PHONE_AUDIO_UCM_CALL_SPEAKER", "call_speaker_pcm": "PHONE_AUDIO_CALL_SPEAKER_PCM",
    "call_mic": "PHONE_AUDIO_UCM_CALL_MIC",
}
# What each value needs beside it, as phone-audiod's flags do.
AUDIO_NEEDS = {"card": ("verb",), "verb": ("card",), "device": ("verb",), "call_verb": ("verb", "earpiece"),
               "earpiece": ("call_verb",), "earpiece_pcm": ("earpiece",), "call_speaker": ("call_verb",),
               "call_speaker_pcm": ("call_speaker",), "call_mic": ("call_verb",)}
# [audio]'s quiet_controls and quiet_level: what a test on the phone turns
# down before its media cases play through the speaker, and to what
# (tools/phone-chroot's selftest and pixel steps, which set the mixer back
# after the run): simple mixer controls of the card (named as UCM names
# are, phone.env joins them with |) and a level amixer takes. Not the
# machine's: only the kit reads them.
QUIET_LEVEL = re.compile(r"(100|[1-9]?[0-9])%")
PCM = re.compile(r"(plug)?hw:(\d+),\d+")
CARD = re.compile(r"hw:(\d+)")
# The drop-in quotes UCM names in a systemd unit and the recipe puts them in
# with sed: no quote, no |.
UCM_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9 _.+-]*")
# phone-paint's GPU path by default (42-gpu-rendering.md §3; PHONE_PAINT_GPU
# overrides it): off, composing (L1) or rasterizing too (L2).
PAINT_GPU = ("off", "compose", "raster")
# A kernel DRM driver's name, as a render node reports it (gpu_render).
DRIVER = re.compile(r"[a-z][a-z0-9_-]{0,31}")
# How big one logical pixel may be on a whole phone's panel, in mm. The
# 2.8" 480x640 target at 2x has 42.7 mm / 240 = 0.178 mm; the least taken
# is a 2.4" 240x320 KaiOS phone at 1x (0.152 mm), the most twice the target.
LOGICAL_MM = (0.15, 0.36)

# A board's partitions ([partitions], 43-phone-partitions.md): each by its
# name (without the slot suffix where ab = true) and its role, which says
# what Kario does with it in either layout. "userdata": the root file
# system is all of the data partition (the 6T until now); "ab": it is in
# the root slots, the data partition holds the data. What a flash does:
# write an image, erase, keep (Android's: never written), efs (this phone's
# own calibration and modem data: never written; backed up, and written
# back only from that backup).
LAYOUTS = ("userdata", "ab")
# Who keeps the A/B slot: the bootloader in the GPT attributes of boot_a
# and boot_b (abl: Qualcomm's ABL, phone-abslot), or phone-initramfs in the
# state partition (state: the emulator's disk, 25-system-update.md).
SLOT_KEEPERS = ("abl", "state")
PART_ROLES = ("boot", "dtbo", "root", "data", "state", "vbmeta", "misc", "efs", "firmware", "android")
PART_SINGLE = ("boot", "dtbo", "root", "data", "state", "vbmeta", "misc")
PART_KEYS = {"name", "ab", "role", "size"}
PART_NAME = re.compile(r"[A-Za-z][A-Za-z0-9_]{0,33}")
# Names no layout writes or erases, whatever a board file says: where
# Qualcomm, Unisoc and MediaTek phones keep one phone's own IMEI, modem
# data and calibration, the bootloader's own state and the board's data,
# and the firmware the SoC, the modem and the DSPs load. yocto/fajita-flash.sh
# keeps the same list (sku_test.py compares).
NEVER = frozenset((
    "persist persistbak modemst1 modemst2 fsg fsc oem_dycnvbk oem_stanvbk devinfo cdt ddr "
    "xbl xbl_config abl tz hyp aop devcfg keymaster cmnlib cmnlib64 qupfw storsec modem dsp bluetooth "
    "prodnv miscdata l_fixnv1 l_fixnv2 l_runtimenv1 l_runtimenv2 l_modem l_ldsp l_gdsp pm_sys "
    "uboot sml trustos teecfg wcnmodem "
    "nvram nvdata nvcfg protect1 protect2 proinfo seccfg preloader lk md1img").split())


class Bad(Exception):
    pass


def is_phone(board):
    return board.get("class") == "phone"


def kernel_src():
    """The kernel tree to check boards' device tree labels against, if any."""
    for d in (os.environ.get("PHONE_KERNEL_SRC", ""),
              os.path.expanduser("~/yocto/phone-work/build/tmp/work-shared/qemuarm64/kernel-source")):
        if d and os.path.isdir(os.path.join(d, "arch", "arm64", "boot", "dts")):
            return d
    return None


def dt_labels(src, dtb, missing_ok=False):
    """The labels a board's tree defines, following its #includes; None
    where the kernel tree has no such tree and missing_ok."""
    root = os.path.join(src, "arch", "arm64", "boot", "dts")
    trees = [p for p in (os.path.join(root, v, dtb[:-4] + ".dts") for v in os.listdir(root)) if os.path.exists(p)]
    if not trees:
        if missing_ok:
            return None
        raise Bad(f"{dtb}: no such tree in {root}")
    labels, todo, seen = set(), [trees[0]], set()
    while todo:
        path = todo.pop()
        if path in seen or not os.path.exists(path):
            continue
        seen.add(path)
        with open(path) as f:
            text = f.read()
        labels |= set(re.findall(r"^\s*([A-Za-z_][\w]*)\s*:\s*[\w@,.-]+\s*\{", text, re.M))
        for inc in re.findall(r'^#include\s+"([^"]+)"', text, re.M):
            todo.append(os.path.join(os.path.dirname(path), inc))
    return labels


def dsi_out(w):
    """The DSI host's output endpoint: <panel_dsi>_out, unless the SoC's
    tree names it otherwise (SC7280's mdss_dsi has mdss_dsi0_out)."""
    return w.get("panel_dsi_out", w["panel_dsi"] + "_out")


def wiring_labels(w):
    """The labels a board's wiring makes the overlay refer to."""
    out = {w["keypad_bus"], w["panel_dsi"], dsi_out(w), w["panel_dsi"] + "_phy", "mdss",
           w["panel_vcc"], w["panel_iovcc"], w["dsi_vdda"]}
    for ref in (w["keypad_irq"], w["panel_reset"]):
        out.add(ref.split()[0])
    if "outer_spi" in w:
        out.add(w["outer_spi"])
    for k in ("outer_enable", "outer_vcom", "lid_gpio"):
        if k in w:
            out.add(w[k].split()[0])
    return out


def sku_dirs():
    """This tree's sku/, then the trees KARIO_SKU_PATH names."""
    dirs = [SKUS]
    for d in os.environ.get(SKU_PATH, "").split(":"):
        if not d:
            continue
        d = os.path.abspath(os.path.expanduser(d))
        if not os.path.isdir(d):
            raise Bad(f"{SKU_PATH}: {d} is no directory")
        if os.path.realpath(d) in {os.path.realpath(x) for x in dirs}:
            raise Bad(f"{SKU_PATH}: {d} is there twice (or is this tree's sku/)")
        if os.path.exists(os.path.join(d, "keys.toml")):
            raise Bad(f"{SKU_PATH}: {d}/keys.toml: the key names are this tree's (sku/keys.toml) alone")
        dirs.append(d)
    return dirs


def shown(path):
    """A path as messages give it: from this tree, or whole."""
    rel = os.path.relpath(path, FW)
    return path if rel.startswith("..") else rel


def found(kind, dirs=None):
    """{id: path} of one kind's files in the SKU trees; an id in two is refused."""
    out = {}
    for d in dirs or sku_dirs():
        sub = os.path.join(d, kind)
        if not os.path.isdir(sub):
            continue
        for f in sorted(os.listdir(sub)):
            if f.endswith(".toml"):
                path = os.path.join(sub, f)
                if f[:-5] in out:
                    raise Bad(f"{kind[:-1]} {f[:-5]!r} is in {shown(out[f[:-5]])} and {shown(path)}")
                out[f[:-5]] = path
    return out


def load(kind, ident):
    path = found(kind).get(ident)
    if path is None:
        raise Bad(f"no {kind[:-1]} {ident!r} ({os.path.relpath(os.path.join(SKUS, kind, ident + '.toml'), FW)}"
                  f"{', ' + SKU_PATH if len(sku_dirs()) > 1 else ''})")
    with open(path, "rb") as f:
        data = tomllib.load(f)
    if data.get("id") != ident:
        raise Bad(f"{shown(path)}: id is {data.get('id')!r}")
    return data


def keys():
    with open(os.path.join(SKUS, "keys.toml"), "rb") as f:
        t = tomllib.load(f)
    return {**t["keys"], **t.get("unmapped", {})}


def names(kind, own=False):
    """The ids of one kind: of every SKU tree, or (own) of this one only."""
    return sorted(found(kind, [SKUS] if own else None))


def kconfig():
    try:
        with open(KCONFIG) as f:
            return {l.split("=")[0] for l in f if l.startswith("CONFIG_") and "=" in l}
    except FileNotFoundError:
        return set()


def resolve(ident):
    """The SKU with its modules, checked against each other."""
    sku = load("skus", ident)
    r = {"sku": sku}
    if "board" not in sku:
        raise Bad(f"{ident}: no board")
    r["board"] = load("boards", sku["board"])
    phone = is_phone(r["board"])
    for kind in ("product",) if phone else ("display", "keypad", "product"):
        if kind not in sku:
            raise Bad(f"{ident}: no {kind}")
        r[kind] = load(kind + "s", sku[kind])
    if phone:
        for kind in ("display", "keypad", "outer_display"):
            if kind in sku:
                raise Bad(f"{ident}: board {sku['board']} is a whole phone: its {kind} is its own (leave {kind} out)")
        r["display"] = check_phone(ident, r["board"])
        r["keypad"] = dict(OWN_KEYPAD if "keymap" in r["board"] else PHONE_KEYPAD)
        if "outer_display" in r["board"]:
            r["outer"] = check_phone_outer(ident, r["board"])
    else:
        if "keymap" in r["board"]:
            raise Bad(f"{ident}: board {sku['board']}: a keymap is a whole phone's (class = \"phone\"); "
                      "a keypad module's codes come from its matrix")
        if "outer_display" in r["board"]:
            raise Bad(f"{ident}: board {sku['board']}: an [outer_display] is a whole phone's; "
                      "a module board's SKU names its outer display (outer_display)")
        if "outer_display" in sku:
            r["outer"] = load("displays", sku["outer_display"])
    board, display, keypad = r["board"], r["display"], r["keypad"]
    if sku.get("memory") not in MEMORY:
        raise Bad(f"{ident}: memory must be one of {sorted(MEMORY)}")
    virtual = bool(board.get("virtual"))
    if virtual != bool(display.get("virtual")) or virtual != (keypad.get("chip") == "usb") or \
            ("outer" in r and virtual != bool(r["outer"].get("virtual"))):
        raise Bad(f"{ident}: QEMU's board takes QEMU's displays and keyboard, a real board real ones")
    if not phone:
        check_display(ident, display)
        if "outer" in r:
            check_display(ident, r["outer"], outer=True)
    device = sku.get("device") or {"t9": "bar-t9", "qwerty": "bar-qwerty"}.get(keypad.get("form", ""))
    if device not in DEVICES:
        raise Bad(f"{ident}: no form factor (device) for keypad {keypad['id']}")
    dw, dh, form = DEVICES[device]
    if keypad.get("form") and keypad["form"] != form:
        raise Bad(f"{ident}: a {keypad['form']} keypad in a {device}")
    rotate = sku.get("rotate", 0)
    if rotate not in (0, 90):
        raise Bad(f"{ident}: rotate is 0 or 90 (a quarter clockwise; the renderer turns frames)")
    if phone:
        check_keymap(ident, board, form)
        pw, ph = phone_screen(ident, display, rotate)
        # A whole phone's panel is not the form factor's size: the form
        # factor only has to face the same way (rotate turns it).
        if (pw > ph) != (dw > dh):
            raise Bad(f"{ident}: the panel shows {pw}x{ph}, a {device} is {dw}x{dh} (rotate = 90 turns it)")
    else:
        pw, ph = (display["height"], display["width"]) if rotate else (display["width"], display["height"])
        if (pw, ph) != (dw, dh):
            raise Bad(f"{ident}: the display shows {pw}x{ph}, a {device} is {dw}x{dh}")
    if not virtual and not phone:
        for need in ("dtb", "wiring"):
            if need not in board:
                raise Bad(f"{ident}: board {board['id']} has no {need}")
        if keypad.get("chip") not in CHIPS:
            raise Bad(f"{ident}: keypad chip {keypad.get('chip')!r} unknown")
        check_wiring_refs(ident, board)
        if "outer" in r:
            check_outer_wiring(ident, board, r["outer"])
        # The kario-qcom machine's kernel; a machine of another layer (a
        # BSP layer's, §8.2) configures its own.
        if machine_conf(board) is not None:
            have = kconfig()
            for cfg in (CHIPS[keypad["chip"]][1], display.get("kconfig"), r.get("outer", {}).get("kconfig")):
                if cfg and cfg not in have:
                    raise Bad(f"{ident}: {cfg} is not in {os.path.relpath(KCONFIG, FW)}")
        check_matrix(ident, keypad)
        src = kernel_src()
        # A machine of another layer may add the board's tree to its kernel
        # (a product's tree in a BSP layer, §8.7): that build checks it.
        labels = dt_labels(src, board["dtb"], missing_ok=machine_conf(board) is None) if src else None
        if labels is not None:
            missing = wiring_labels(board["wiring"]) - labels
            if missing:
                raise Bad(f"{ident}: {board['dtb']} has no {', '.join(sorted(missing))}")
    check_fingerprint_place(ident, keypad)
    check_leds(ident, board)
    check_gnss(ident, board)
    check_modem(ident, board)
    check_ims(ident, board)
    check_audio(ident, board)
    check_partitions(ident, board)
    check_partition_files(ident, board)
    check_rtc(ident, board)
    check_watchdog(ident, board)
    check_touch(ident, board, display)
    check_input_devices(ident, board)
    check_fingerprint_part(ident, board, keypad)
    check_unverified(ident, board)
    check_parts(ident, board)
    r["paint_gpu"], r["gpu_render"] = check_gpu(ident, sku, board)
    r["device"], r["rotate"], r["memory"] = device, rotate, MEMORY[sku["memory"]]
    return r


def check_gpu(ident, sku, board):
    """phone-paint's GPU path by default (paint_gpu, the SKU's over the
    board's) and the kernel driver of the GPU's render node where the
    display's device cannot render (gpu_render): both need a GPU."""
    b = board["id"]
    paint = sku.get("paint_gpu", board.get("paint_gpu", "off"))
    if paint not in PAINT_GPU:
        raise Bad(f"{ident}: paint_gpu {paint!r} is not one of {', '.join(PAINT_GPU)}")
    render = board.get("gpu_render", "")
    if render and (not isinstance(render, str) or not DRIVER.fullmatch(render)):
        raise Bad(f"{ident}: board {b}: gpu_render {render!r} is not a kernel driver's name (panfrost, powervr, ...)")
    if not board.get("gpu") and (paint != "off" or render):
        raise Bad(f"{ident}: board {b} has no GPU (gpu = false): paint_gpu must be off and gpu_render empty")
    return paint, render


def class_name(s):
    return isinstance(s, str) and 0 < len(s) < 64 and "/" not in s and s.strip() == s


def positive(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool) and v > 0


def check_phone(ident, board):
    """A whole phone's own panel ([display]) and buttons ([input]); the
    panel as the SKU's display."""
    b, d = board["id"], board.get("display")
    if not isinstance(d, dict):
        raise Bad(f"{ident}: board {b} is a whole phone without a [display]")
    for k in d:
        if k not in PANEL_KEYS:
            raise Bad(f"{ident}: board {b} [display]: unknown {k!r}")
    for k in ("id", "name"):
        if not isinstance(d.get(k), str) or not d[k]:
            raise Bad(f"{ident}: board {b} [display] has no {k}")
    for k in ("width", "height"):
        if not isinstance(d.get(k), int) or isinstance(d[k], bool) or d[k] <= 0:
            raise Bad(f"{ident}: board {b} [display]: {k} is the panel's pixels")
    for k in ("width_mm", "height_mm", "scale"):
        if not positive(d.get(k)):
            raise Bad(f"{ident}: board {b} [display]: {k} must be a positive number")
    if not 1 <= d["scale"] <= 8:
        raise Bad(f"{ident}: board {b} [display]: scale {d['scale']} is not 1..8 physical pixels per logical one")
    # The physical size is the pixels' shape: swapped or wrong millimetres
    # would pass the readability check below by accident.
    if abs(d["width_mm"] / d["height_mm"] / (d["width"] / d["height"]) - 1) > 0.05:
        raise Bad(f"{ident}: board {b} [display]: {d['width_mm']}x{d['height_mm']} mm is not the shape of {d['width']}x{d['height']}")
    mm = d["width_mm"] / d["width"] * d["scale"]
    if not LOGICAL_MM[0] <= mm <= LOGICAL_MM[1]:
        raise Bad(f"{ident}: board {b} [display]: at scale {d['scale']} a logical pixel is {mm:.3f} mm, "
                  f"not {LOGICAL_MM[0]}..{LOGICAL_MM[1]} mm (text {'well smaller than' if mm < LOGICAL_MM[0] else 'over twice as big as'} on the 2.8\" target)")
    inset = d.get("inset", 0)
    if not isinstance(inset, int) or isinstance(inset, bool) or inset < 0 or 2 * inset >= d["height"]:
        raise Bad(f"{ident}: board {b} [display]: inset {inset!r} is the rows left dark at the top and the bottom")
    for k in ("panel", "touchscreen"):
        if k in d and (not isinstance(d[k], str) or not d[k]):
            raise Bad(f"{ident}: board {b} [display]: {k} is a device tree compatible")
    if not isinstance(board.get("compatible"), str) or not board["compatible"]:
        raise Bad(f"{ident}: board {b}: compatible is the device tree's root compatible (tools/phone-chroot checks it)")
    inp = board.get("input", {})
    for k in inp:
        if k not in INPUT_KEYS:
            raise Bad(f"{ident}: board {b} [input]: unknown {k!r}")
    buttons = inp.get("buttons", [])
    if not isinstance(buttons, list) or not all(isinstance(n, str) and n for n in buttons):
        raise Bad(f"{ident}: board {b} [input]: buttons are input device names")
    return dict(d, virtual=False)


def check_display(ident, d, outer=False):
    """A display file: the main panel is on DSI (QEMU's is virtual), an
    outer one on SPI; an outer display names its DRM driver, which the
    renderer opens it by."""
    where = f"{ident}: display {d.get('id')}"
    for k in d:
        if k not in DISPLAY_KEYS:
            raise Bad(f"{where}: unknown {k!r}")
    for k in ("width", "height"):
        if not isinstance(d.get(k), int) or isinstance(d[k], bool) or d[k] <= 0:
            raise Bad(f"{where}: {k} is the panel's pixels")
    if ("width_mm" in d) != ("height_mm" in d) or not all(positive(d[k]) for k in ("width_mm", "height_mm") if k in d):
        raise Bad(f"{where}: width_mm and height_mm are the active area's millimetres, both or neither")
    if d.get("kind", "lcd") not in DISPLAY_KINDS:
        raise Bad(f"{where}: kind {d['kind']!r} is not one of {', '.join(DISPLAY_KINDS)}")
    if d.get("color", "rgb") not in DISPLAY_COLORS:
        raise Bad(f"{where}: color {d['color']!r} is not one of {', '.join(DISPLAY_COLORS)}")
    if "drm_driver" in d and (not isinstance(d["drm_driver"], str) or not DRIVER.fullmatch(d["drm_driver"])):
        raise Bad(f"{where}: drm_driver {d['drm_driver']!r} is a kernel DRM driver's name")
    if outer and "drm_driver" not in d:
        raise Bad(f"{where}: an outer display names its DRM driver (drm_driver), which the renderer opens it by")
    if outer and "touchscreen" in d:
        raise Bad(f"{where}: an outer display's touchscreen: nothing reads it")
    bus = d.get("bus", "dsi")
    if bus not in ("dsi", "spi"):
        raise Bad(f"{where}: bus {bus!r} is dsi or spi")
    if d.get("virtual"):
        return
    if bus != ("spi" if outer else "dsi"):
        raise Bad(f"{where}: the overlay wires {'an outer' if outer else 'the main'} panel on {'SPI' if outer else 'DSI'}, not {bus}")
    if not isinstance(d.get("compatible"), list) or not d["compatible"] or not all(isinstance(c, str) and COMPATIBLE.fullmatch(c) for c in d["compatible"]):
        raise Bad(f"{where}: compatible is the panel's device tree compatibles")
    if outer and d.get("kconfig") not in SPI_PANELS:
        raise Bad(f"{where}: kconfig {d.get('kconfig')!r}: the overlay wires {', '.join(sorted(SPI_PANELS))}")
    if bus == "dsi":
        for k in ("spi_max_hz", "spi_cs_high"):
            if k in d:
                raise Bad(f"{where}: {k} on a DSI panel")
    else:
        if "lanes" in d:
            raise Bad(f"{where}: lanes on an SPI panel")
        if not isinstance(d.get("spi_max_hz"), int) or isinstance(d["spi_max_hz"], bool) or not 0 < d["spi_max_hz"] <= 100_000_000:
            raise Bad(f"{where}: spi_max_hz is the panel's SPI clock (Hz)")
        if not isinstance(d.get("spi_cs_high", False), bool):
            raise Bad(f"{where}: spi_cs_high is true or false")


def check_phone_outer(ident, board):
    """A whole phone's own outer display ([outer_display]): its pixels,
    kind and DRM driver, as a display file has them."""
    b, d = board["id"], board["outer_display"]
    if not isinstance(d, dict):
        raise Bad(f"{ident}: board {b}: [outer_display] is a table")
    for k in d:
        if k not in OUTER_PANEL_KEYS:
            raise Bad(f"{ident}: board {b} [outer_display]: unknown {k!r}")
    for k in ("id", "name"):
        if not isinstance(d.get(k), str) or not d[k]:
            raise Bad(f"{ident}: board {b} [outer_display] has no {k}")
    check_display(ident, {k: v for k, v in d.items() if k != "panel"} | {"virtual": True}, outer=True)
    if "panel" in d and (not isinstance(d["panel"], str) or not COMPATIBLE.fullmatch(d["panel"])):
        raise Bad(f"{ident}: board {b} [outer_display]: panel is a device tree compatible")
    return dict(d, virtual=False)


def check_outer_wiring(ident, board, outer):
    """A module board's outer display: the SPI bus it is on and the
    [wiring] its driver needs (SPI_PANELS)."""
    w, b = board["wiring"], board["id"]
    for k in ("outer_spi",) + SPI_PANELS[outer["kconfig"]][0]:
        if k not in w:
            raise Bad(f"{ident}: board {b} [wiring] has no {k} for display {outer['id']}")


def check_wiring_refs(ident, board):
    """The optional [wiring]: outer_spi a controller's label, outer_enable,
    outer_vcom and lid_gpio a label and its cells; the lid's GPIO is the
    input device [input] lid names (the overlay's gpio-keys node is
    labelled so)."""
    w, b = board["wiring"], board["id"]
    if "outer_spi" in w and not (isinstance(w["outer_spi"], str) and re.fullmatch(r"[a-z_][a-z0-9_]*", w["outer_spi"])):
        raise Bad(f"{ident}: board {b} [wiring]: outer_spi {w['outer_spi']!r} is the SPI controller's label")
    for k in ("outer_enable", "outer_vcom", "lid_gpio"):
        if k in w and not (isinstance(w[k], str) and re.fullmatch(r"[a-z_][a-z0-9_]*( (0x[0-9a-f]+|[0-9]+))+", w[k])):
            raise Bad(f"{ident}: board {b} [wiring]: {k} {w[k]!r} is a phandle's label and its cells")
    if "lid_gpio" in w and "lid" not in board.get("input", {}):
        raise Bad(f"{ident}: board {b}: a lid_gpio without [input] lid, the name of its input device")


def check_fingerprint_part(ident, board, keypad):
    """A keypad with a fingerprint sensor (check_fingerprint_place) on a
    real board: the board lists the sensor's part."""
    if keypad.get("fingerprint") and not board.get("virtual") and \
            not any("fingerprint" in part_kinds(p) for p in board.get("part", [])):
        raise Bad(f"{ident}: keypad {keypad['id']} has a fingerprint sensor, board {board['id']} no fingerprint part")


def sound_switch(board):
    """The board's hardware sound-profile switch ([input] sound_switch), or ""."""
    return board.get("input", {}).get("sound_switch", "")


def check_input_devices(ident, board):
    """The sound switch and the lid switch are input devices' names as the
    kernel gives them (EVIOCGNAME: at most 79 bytes here), not one of the
    buttons nor each other."""
    inp, b = board.get("input", {}), board["id"]
    for k in inp:
        if k not in INPUT_KEYS:
            raise Bad(f"{ident}: board {b} [input]: unknown {k!r}")
    for k, what in INPUT_DEVICES.items():
        if k not in inp:
            continue
        s = inp[k]
        if not isinstance(s, str) or not s or s.strip() != s or len(s.encode()) > 79 or not s.isprintable() or "|" in s:
            raise Bad(f"{ident}: board {b} [input]: {k} {s!r} is an input device's name")
        if s in inp.get("buttons", []):
            raise Bad(f"{ident}: board {b} [input]: {s!r} is a button and the {what}")
    if "lid" in inp and inp["lid"] == inp.get("sound_switch"):
        raise Bad(f"{ident}: board {b} [input]: {inp['lid']!r} is the sound switch and the lid switch")
    if not is_phone(board):
        for k in inp:
            if k not in INPUT_DEVICES:
                raise Bad(f"{ident}: board {b} [input]: {k!r}: a board that is not a whole phone has only its sound_switch and lid here")


def check_keymap(ident, board, form):
    """A whole phone's [keymap]: input codes (decimal, 1..KEY_MAX) to keys
    of keys.toml. It lists every key of the phone's keypad, those whose
    code is already the key's too, so the keys the form factor needs must
    all be in it."""
    if "keymap" not in board:
        return
    b, km = board["id"], board["keymap"]
    if not isinstance(km, dict) or not km:
        raise Bad(f"{ident}: board {b}: keymap is a table of input codes to key names")
    table = keys_mapped()
    for code, key in km.items():
        if not re.fullmatch(r"[1-9][0-9]*", code) or int(code) > KEY_MAX:
            raise Bad(f"{ident}: board {b} [keymap]: {code!r} is not an input code (1..{KEY_MAX})")
        if key not in table:
            raise Bad(f"{ident}: board {b} [keymap]: {code} = {key!r} is no key in keys.toml")
    missing = (NEEDED | FORM_KEYS.get(form, set())) - set(km.values())
    if missing:
        raise Bad(f"{ident}: board {b} [keymap] lacks {', '.join(sorted(missing))}")


def keys_mapped():
    """The keys phone-paint maps (a keymap may name no other)."""
    with open(os.path.join(SKUS, "keys.toml"), "rb") as f:
        return tomllib.load(f)["keys"]


def phone_screen(ident, display, rotate):
    """What a whole phone's renderer draws, as the form factor sees it: the
    panel less its inset rows, which phone-paint leaves dark by centring the
    frame on the panel (kms::letterbox)."""
    inset = display.get("inset", 0)
    if rotate and inset:
        raise Bad(f"{ident}: a panel with inset rows cannot be turned (the renderer turns whole panels only)")
    w, h = display["width"], display["height"] - 2 * inset
    return (h, w) if rotate else (w, h)


def check_unverified(ident, board):
    """unverified lists the board's values still to confirm on the device,
    as field or table.field."""
    marks = board.get("unverified", [])
    if not isinstance(marks, list):
        raise Bad(f"{ident}: board {board['id']}: unverified is a list of fields")
    for m in marks:
        table, _, key = m.rpartition(".") if isinstance(m, str) else ("", "", "")
        where = board.get(table) if table else board
        if not key or not isinstance(where, dict) or key not in where:
            raise Bad(f"{ident}: board {board['id']}: unverified {m!r} is not one of its values")


# A board's hardware beyond what its other fields say ([[part]],
# 24-sku-framework.md §9): each part by what it is (kind, one or a list),
# its name, the bus it is on and the mainline driver that binds it: the
# device tree compatible and the kernel source file(s) that match it, or
# driver = "none" where mainline has none. Nothing at run time reads the
# table: it is the board's bill of materials for a builder (sku.py parts).
PART_KINDS = (
    "gpu", "display", "video-codec", "modem", "mobile-data", "wifi", "bluetooth", "gnss",
    "sound-card", "codec", "amplifier", "headphone-jack", "fm-radio",
    "camera", "camera-receiver", "isp", "focus", "flash",
    "light", "proximity", "accelerometer", "gyroscope", "magnetometer", "haptics", "fingerprint", "nfc",
    "charger", "fuel-gauge", "typec", "rtc", "watchdog", "led", "keyboard-backlight", "backlight", "buttons",
    "lid", "usb-host", "power-switch",
)
PART_BUSES = ("soc", "i2c", "spi", "uart", "spmi", "soundwire", "i2s", "csi", "dsi", "usb", "pcie", "sdio", "gpio", "pwm")
PART_FIELDS = {"kind", "name", "bus", "compatible", "driver", "facing"}
FACINGS = ("front", "back")
COMPATIBLE = re.compile(r"[a-z0-9][a-z0-9-]*(,[a-z0-9][a-z0-9.+_-]*)?")
DRIVER_SOURCE = re.compile(r"(drivers|sound)(/[A-Za-z0-9_+-][A-Za-z0-9_.+-]*)+\.c")


def part_kinds(p):
    k = p.get("kind")
    return [k] if isinstance(k, str) else k


def part_drivers(p):
    """A part's driver source files, [] for driver = "none"."""
    d = p.get("driver")
    if d == "none":
        return []
    return [d] if isinstance(d, str) else d


def check_parts(ident, board, src=None):
    """The board's [[part]] table; with a kernel tree (src: kernel_src()'s
    when None) each driver file exists and one of them matches the
    compatible."""
    b, parts = board["id"], board.get("part", [])
    where = f"{ident}: board {b} [[part]]"
    if not isinstance(parts, list) or not all(isinstance(p, dict) for p in parts):
        raise Bad(f"{where}: is a list of tables")
    for p in parts:
        name = p.get("name")
        if not isinstance(name, str) or not name or name.strip() != name:
            raise Bad(f"{where}: {p} has no name")
        for k in p:
            if k not in PART_FIELDS:
                raise Bad(f"{where} {name}: unknown {k!r}")
        kinds = part_kinds(p)
        if not isinstance(kinds, list) or not kinds or len(set(kinds)) != len(kinds) or \
                not all(isinstance(k, str) and k in PART_KINDS for k in kinds):
            raise Bad(f"{where} {name}: kind {p.get('kind')!r} is not one or more of {', '.join(PART_KINDS)}")
        if p.get("bus") not in PART_BUSES:
            raise Bad(f"{where} {name}: bus {p.get('bus')!r} is not one of {', '.join(PART_BUSES)}")
        if ("camera" in kinds) != ("facing" in p) or p.get("facing", "front") not in FACINGS:
            raise Bad(f"{where} {name}: a camera, and only a camera, faces front or back (facing)")
        drivers = part_drivers(p)
        if p.get("driver") == "none":
            if "compatible" in p:
                raise Bad(f"{where} {name}: no mainline driver (driver = \"none\") binds a compatible")
            continue
        if not isinstance(drivers, list) or not drivers or \
                not all(isinstance(d, str) and DRIVER_SOURCE.fullmatch(d) for d in drivers):
            raise Bad(f"{where} {name}: driver {p.get('driver')!r} is not \"none\" or kernel source files (drivers/....c, sound/....c)")
        c = p.get("compatible")
        if not isinstance(c, str) or not COMPATIBLE.fullmatch(c):
            raise Bad(f"{where} {name}: compatible {c!r} is not a device tree compatible")
    src = kernel_src() if src is None else src
    if not src:
        return
    for p in parts:
        texts = []
        for d in part_drivers(p):
            path = os.path.join(src, d)
            if not os.path.isfile(path):
                raise Bad(f"{where} {p['name']}: {d} is not in the kernel tree {src}")
            with open(path, errors="replace") as f:
                texts.append(f.read())
        if texts and not any(f'"{p["compatible"]}"' in t for t in texts):
            raise Bad(f"{where} {p['name']}: {', '.join(part_drivers(p))} does not match {p['compatible']!r} in {src}")


def parts_table(board):
    """The board's parts, one a line: kinds (a camera's facing), bus,
    compatible, driver files, name."""
    lines = []
    for p in board.get("part", []):
        kinds = ",".join(k + (f"({p['facing']})" if k == "camera" else "") for k in part_kinds(p))
        lines.append(f"{kinds:28} {p['bus']:9} {p.get('compatible', '-'):32} {' '.join(part_drivers(p)) or 'none'}  {p['name']}")
    return lines


def check_leds(ident, board):
    """The board's notification LED and backlight: Linux class device names
    (hal/led, hal/backlight), a channel intensity each for a multicolor LED."""
    leds = board.get("leds", {})
    for k in leds:
        if k not in ("notify", "notify_color"):
            raise Bad(f"{ident}: board {board['id']} [leds]: unknown {k!r}")
    if "notify" in leds and not class_name(leds["notify"]):
        raise Bad(f"{ident}: board {board['id']}: notify LED {leds['notify']!r} is not an LED class name")
    color = leds.get("notify_color", [])
    if color and ("notify" not in leds or not all(isinstance(c, int) and 0 <= c <= 255 for c in color)):
        raise Bad(f"{ident}: board {board['id']}: notify_color is 0..255 per channel of the notify LED")
    for k in ("backlight", "keyboard_backlight", "light_sensor"):
        if k in board and not class_name(board[k]):
            raise Bad(f"{ident}: board {board['id']}: {k} {board[k]!r} is not a class name")
    if board.get("nfc", "sim") != "sim" and not str(board.get("nfc")).startswith("nci:/dev/"):
        raise Bad(f"{ident}: board {board['id']}: nfc {board['nfc']!r} is not sim or nci:/dev/<node>")
    if board.get("fingerprint", "none") not in ("sim", "none"):
        raise Bad(f"{ident}: board {board['id']}: fingerprint {board['fingerprint']!r} is not sim or none")


# Where a keypad carries the fingerprint sensor (its fingerprint): "ok-key",
# in its OK key, which keystored powers only for the system's payment and
# security prompts (31-lock-and-biometrics.md §4.5). Left out, the board's
# sensor, if it has one, is a sensor of its own.
FINGERPRINT_PLACES = ("ok-key",)


def check_fingerprint_place(ident, keypad):
    place = keypad.get("fingerprint")
    if place is None:
        return
    if place not in FINGERPRINT_PLACES:
        raise Bad(f"{ident}: keypad {keypad['id']}: fingerprint {place!r} is not one of {', '.join(FINGERPRINT_PLACES)}")
    if "matrix" in keypad and "ok" not in keypad["matrix"].values():
        raise Bad(f"{ident}: keypad {keypad['id']}: a fingerprint sensor in the OK key, but no ok key")


def check_gnss(ident, board):
    """The board's location receiver, as location.ParseSpec reads it: sim,
    modemmanager, nmea:/dev/<node>[@<baud>], or fifo:<name> (in the Shell's
    run directory) or fifo:/<path>."""
    if "gnss" not in board:
        return
    g = board["gnss"]
    kind, _, rest = g.partition(":") if isinstance(g, str) else ("", "", "")
    path, _, baud = rest.partition("@")
    ok = {
        "sim": rest == "",
        "modemmanager": rest == "",
        "nmea": path.startswith("/dev/") and ".." not in path and (baud == "" or baud in BAUDS),
        "fifo": "@" not in rest and ".." not in path and (re.fullmatch(r"[\w.-]+", path) is not None or path.startswith("/")),
    }.get(kind, False)
    if not ok:
        raise Bad(f"{ident}: board {board['id']}: gnss {g!r} is not sim, modemmanager, nmea:/dev/<node>[@<baud>] or fifo:<name>")


# The serial speeds a GNSS receiver may be read at (location/stream.go).
BAUDS = {"4800", "9600", "19200", "38400", "57600", "115200", "230400", "460800", "921600"}

# telephonyd's built-in backends (telephonyd/src/backend.cpp), one of which
# a board's modem names; sku.json's default is sim. What is particular to
# the SoC beside it (IMS, the carrier's configuration, the voice path, the
# SIM slot) is the board's glue (24-sku-framework.md §8.8).
MODEMS = ("sim", "modemmanager")


def check_modem(ident, board):
    m = board.get("modem", "sim")
    if m not in MODEMS:
        raise Bad(f"{ident}: board {board['id']}: modem {m!r} is not one of {', '.join(MODEMS)}")


# What serves a board's modem when it asks the system for its IMS (VoLTE)
# bearers (ims), and the backend it opens them through: 81voltd answers
# QMI's IMS data service on QRTR and opens them through ModemManager. The
# SKU's kas overlay sets PHONE_IMS, from which a Yocto machine of this
# layer installs it.
IMS = {"81voltd": "modemmanager"}


def check_ims(ident, board, conf=None):
    """The board's ims: a daemon of IMS, beside the modem backend it opens
    the bearers through; the board's machine of this layer (conf: (path,
    text), the machine's file when None) installs it from PHONE_IMS and
    never by itself."""
    b, v = board["id"], board.get("ims")
    if v is not None:
        if v not in IMS:
            raise Bad(f"{ident}: board {b}: ims {v!r} is not one of {', '.join(IMS)}")
        if board.get("modem") != IMS[v]:
            raise Bad(f"{ident}: board {b}: ims {v} opens the bearers through the {IMS[v]} backend: modem is {IMS[v]}")
    conf = conf or machine_conf(board)
    if conf is None:
        return
    path, text = conf
    where = os.path.relpath(path, FW)
    code = [l for l in re.sub(r"\\\n", " ", text).splitlines() if not l.lstrip().startswith("#")]
    if v is not None and not any(re.search(r"\bPHONE_IMS\b", l) for l in code):
        raise Bad(f"{ident}: board {b} has ims = {v!r}, {where} does not install it from PHONE_IMS")
    for d in IMS:
        if any(re.search(rf"(?<![\w-]){d}(?![\w-])", l) and "PHONE_IMS" not in l for l in code):
            raise Bad(f"{ident}: {where} installs {d} whatever the SKU: install it from PHONE_IMS (the board's ims)")

# A board's real-time clock ([rtc], hal/rtc): read_only (setting it fails,
# so setting the time does not write it), offset (the system time is kept
# as an offset from the read-only RTC and set from both at boot), alarm
# (the wake alarm: "relative", "+N" seconds from the RTC's own time, right
# on any RTC; "absolute", the wake time itself, right only on an RTC that
# runs on the system's clock). Without [rtc]: settable, relative alarms.
RTC = {"read_only": False, "offset": False, "alarm": "relative"}
ALARMS = ("relative", "absolute")


def rtc(board):
    """The board's [rtc] with the defaults filled in."""
    return {**RTC, **board.get("rtc", {})}


def check_rtc(ident, board):
    b, r = board["id"], board.get("rtc", {})
    if not isinstance(r, dict):
        raise Bad(f"{ident}: board {b}: rtc is a table")
    for k, v in r.items():
        if k not in RTC:
            raise Bad(f"{ident}: board {b} [rtc]: unknown {k!r}")
        if k in ("read_only", "offset") and not isinstance(v, bool):
            raise Bad(f"{ident}: board {b} [rtc]: {k} is true or false")
    r = rtc(board)
    if r["alarm"] not in ALARMS:
        raise Bad(f"{ident}: board {b} [rtc]: alarm {r['alarm']!r} is relative or absolute")
    if r["offset"] and not r["read_only"]:
        raise Bad(f"{ident}: board {b} [rtc]: an offset is kept from a read-only RTC only (a settable one is set)")
    if r["read_only"] and r["alarm"] == "absolute":
        raise Bad(f"{ident}: board {b} [rtc]: a read-only RTC's time is not the system's: its alarm is relative")


# A board's hardware watchdog as systemd arms it ([watchdog]): runtime
# (RuntimeWatchdogSec: PID 1 pings it while the system runs), reboot
# (RebootWatchdogSec: armed when PID 1 hands a reboot over to
# systemd-shutdown, which pings it and leaves it armed through the final
# reboot()), kexec (KExecWatchdogSec), each "off", "default" (the timeout
# the hardware has) or a time in s, min or h; max_timeout: the longest
# timeout the board's driver takes, in seconds (above it WDIOC_SETTIMEOUT
# fails with EINVAL, and systemd disarms the watchdog). What a board leaves
# out is systemd's own. phone-sku installs it as a system.conf drop-in
# (watchdog.conf).
WATCHDOG = {"runtime": "RuntimeWatchdogSec", "reboot": "RebootWatchdogSec", "kexec": "KExecWatchdogSec"}
SYSTEMD_WATCHDOG = {"runtime": "off", "reboot": "10min", "kexec": "off"}
WATCHDOG_TIME = re.compile(r"off|default|([1-9][0-9]*)(s|min|h)")
WATCHDOG_UNITS = {"s": 1, "min": 60, "h": 3600}


def check_watchdog(ident, board):
    b, w = board["id"], board.get("watchdog", {})
    where = f"{ident}: board {b} [watchdog]"
    if not isinstance(w, dict):
        raise Bad(f"{ident}: board {b}: watchdog is a table")
    for k, v in w.items():
        if k == "max_timeout":
            if not isinstance(v, int) or isinstance(v, bool) or v <= 0:
                raise Bad(f"{where}: max_timeout is the longest timeout the driver takes, in whole seconds")
        elif k not in WATCHDOG:
            raise Bad(f"{where}: unknown {k!r}")
        elif not isinstance(v, str) or not WATCHDOG_TIME.fullmatch(v):
            raise Bad(f"{where}: {k} = {v!r} is not off, default or a time (30s, 2min, 1h)")
    if "max_timeout" not in w:
        return
    for k in WATCHDOG:
        v = w.get(k, SYSTEMD_WATCHDOG[k])
        m = WATCHDOG_TIME.fullmatch(v)
        if m.group(1) and int(m.group(1)) * WATCHDOG_UNITS[m.group(2)] > w["max_timeout"]:
            raise Bad(f"{where}: {k} {v}{'' if k in w else ' (systemd default)'} is more than the watchdog takes "
                      f"({w['max_timeout']} s): its driver refuses it and systemd disarms the watchdog")


def watchdog_conf(board):
    """The board's [watchdog] as a systemd system.conf drop-in (only its
    comment where the board leaves systemd's own)."""
    w = board.get("watchdog", {})
    head = f"# The board {board['id']}'s hardware watchdog for systemd (tools/sku.py, sku/boards/{board['id']}.toml [watchdog]).\n"
    keys = [k for k in WATCHDOG if k in w]
    if not keys:
        return head + "# None given: systemd's own.\n"
    return head + "[Manager]\n" + "".join(f"{WATCHDOG[k]}={w[k]}\n" for k in keys)


def machine_conf(board):
    """The board's Yocto machine configuration in this layer (path, text),
    or None (no machine, or one from another layer: QEMU's)."""
    path = os.path.join(LAYER, "conf", "machine", f"{board.get('machine', '')}.conf")
    if "machine" not in board or not os.path.exists(path):
        return None
    with open(path) as f:
        return path, f.read()


# A display's touchscreen ([display] touchscreen, its device tree
# compatible) and what its driver needs in the kernel: built in, the
# touches are there from the start, and under lockdown no module of it has
# to be signed and loaded (phone-lockdown.cfg). A board with a Yocto
# machine of this layer has them =y in its kernel's fragments. QEMU's
# virtual displays name QEMU's device, which the emulator's kernel drives
# with its virtio_input module (phone-qemuarm64.yml installs it).
TOUCHSCREENS = {"syna,rmi4-i2c": ("CONFIG_RMI4_CORE", "CONFIG_RMI4_I2C", "CONFIG_RMI4_F12")}
QEMU_TOUCHSCREENS = ("virtio-multitouch-pci",)


def machine_kconfig(board):
    """What the configuration fragments of the board's machine's kernel set
    ({CONFIG_X: value, "n" for not set}), in the order its recipe lists
    them; None without a machine and kernel recipe of this layer."""
    conf = machine_conf(board)
    m = conf and re.search(r'^PREFERRED_PROVIDER_virtual/kernel\s*=\s*"([^"]+)"', conf[1], re.M)
    if not m:
        return None
    kdir = os.path.join(LAYER, "recipes-kernel", "linux")
    recipes = sorted(f for f in os.listdir(kdir) if f.startswith(m.group(1) + "_") and f.endswith(".bb"))
    if not recipes:
        return None
    with open(os.path.join(kdir, recipes[0])) as f:
        frags = re.findall(r"file://([\w.-]+\.cfg)", f.read())
    have = {}
    for frag in frags:
        with open(os.path.join(kdir, "files", frag)) as f:
            for line in f:
                if o := re.match(r"(CONFIG_\w+)=(.*)$", line.strip()):
                    have[o.group(1)] = o.group(2)
                elif o := re.match(r"# (CONFIG_\w+) is not set$", line.strip()):
                    have[o.group(1)] = "n"
    return have


def check_touch(ident, board, display, kconfig=None):
    """The display's touchscreen: QEMU's device on a virtual display; on a
    board with a machine, its driver built into the machine's kernel
    (kconfig: the options set, the machine's fragments' when None)."""
    t = display.get("touchscreen")
    if t is None:
        return
    if display.get("virtual"):
        if t not in QEMU_TOUCHSCREENS:
            raise Bad(f"{ident}: display {display['id']}: touchscreen {t!r} is not QEMU's ({', '.join(QEMU_TOUCHSCREENS)})")
        return
    have = machine_kconfig(board) if kconfig is None else kconfig
    if have is None:
        return
    if t not in TOUCHSCREENS:
        raise Bad(f"{ident}: board {board['id']}: no kernel options known for touchscreen {t!r} (tools/sku.py TOUCHSCREENS)")
    missing = [o for o in TOUCHSCREENS[t] if have.get(o) != "y"]
    if missing:
        raise Bad(f"{ident}: board {board['id']}: its machine's kernel does not build {', '.join(missing)} in "
                  f"(touchscreen {t}; the kernel recipe's fragments)")


def check_audio(ident, board, conf=None):
    """The board's [audio], and the machine's PHONE_AUDIO_* (conf: (path,
    text), the machine's file when None) equal to it."""
    b, a = board["id"], board.get("audio", {})
    if not isinstance(a, dict):
        raise Bad(f"{ident}: board {b}: audio is a table")
    for k, v in a.items():
        if k == "quiet_controls":
            if not isinstance(v, list) or not v or not all(isinstance(c, str) and UCM_NAME.fullmatch(c) for c in v):
                raise Bad(f"{ident}: board {b} [audio]: quiet_controls are the card's mixer controls (names)")
            for need in ("card", "quiet_level"):
                if need not in a:
                    raise Bad(f"{ident}: board {b} [audio]: quiet_controls needs {need}")
            continue
        if k == "quiet_level":
            if not isinstance(v, str) or not QUIET_LEVEL.fullmatch(v):
                raise Bad(f"{ident}: board {b} [audio]: quiet_level = {v!r} is not a level (0% to 100%)")
            if "quiet_controls" not in a:
                raise Bad(f"{ident}: board {b} [audio]: quiet_level needs quiet_controls")
            continue
        if k not in AUDIO:
            raise Bad(f"{ident}: board {b} [audio]: unknown {k!r}")
        if k.endswith("pcm"):
            pattern, what = PCM, "an ALSA PCM (plughw:<card>,<device>)"
        elif k == "card":
            pattern, what = CARD, "a sound card (hw:<n>)"
        else:
            pattern, what = UCM_NAME, "a UCM verb or device"
        if not isinstance(v, str) or not pattern.fullmatch(v):
            raise Bad(f"{ident}: board {b} [audio]: {k} = {v!r} is not {what}")
        for need in AUDIO_NEEDS.get(k, ()):
            if need not in a:
                raise Bad(f"{ident}: board {b} [audio]: {k} needs {need}")
    if a and "pcm" not in a:
        raise Bad(f"{ident}: board {b} [audio]: no pcm (where phone-audiod plays media)")
    if "card" in a:
        for k in ("pcm", "earpiece_pcm", "call_speaker_pcm"):
            if k in a and PCM.fullmatch(a[k]).group(2) != CARD.fullmatch(a["card"]).group(1):
                raise Bad(f"{ident}: board {b} [audio]: {k} {a[k]} is not on card {a['card']}")
    conf = conf or machine_conf(board)
    if conf is None:
        return
    path, text = conf
    have = dict(re.findall(r'^(PHONE_AUDIO_\w+)\s*\??\??=\s*"([^"]*)"', re.sub(r"\\\n", " ", text), re.M))
    want = {AUDIO[k]: v for k, v in a.items() if k in AUDIO}
    key = {var: k for k, var in AUDIO.items()}
    where = os.path.relpath(path, FW)
    if have and not a:
        raise Bad(f"{ident}: board {b} has no [audio], {where} sets {', '.join(sorted(have))}: "
                  "the board file is the source, give it those values")
    for var in sorted(set(have) | set(want)):
        if var not in key:
            raise Bad(f"{ident}: {where}: {var} is none of the board's [audio] values ({', '.join(AUDIO.values())})")
        if have.get(var) != want.get(var):
            raise Bad(f"{ident}: board {b} [audio] {key[var]} is {want.get(var)!r}, {where} has {var} = {have.get(var)!r}: "
                      "the board file is the source, make the machine's the same")


def never(name):
    """Whether a partition's name (with or without its slot suffix) is one
    no flash writes."""
    n = name.lower()
    return n in NEVER or (n[-2:] in ("_a", "_b") and n[:-2] in NEVER)


def part_action(role, layout, keeper):
    """What a flash in the layout does to a partition of the role: write
    (and the image), erase, keep or efs."""
    if role == "boot":
        return "write", "boot" if layout == "userdata" else "abboot"
    if role == "dtbo":
        return "erase", ""
    if role == "root":
        return ("write", "slot") if layout == "ab" else ("keep", "")
    if role == "data":
        # The initramfs makes the data file system on a blank partition
        # (the emulator's disk image brings it).
        if layout == "userdata":
            return "write", "rootfs"
        return ("erase", "") if keeper == "abl" else ("write", "data")
    if role == "state":
        if layout == "userdata":
            return "keep", ""
        return ("erase", "") if keeper == "abl" else ("write", "state")
    if role == "efs":
        return "efs", ""
    return "keep", ""


def layouts(p):
    """The layouts a board's partitions allow."""
    roles = {q["role"]: q for q in p["part"]}
    out = []
    if "boot" in roles and "data" in roles and p["slots"] == "abl":
        out.append("userdata")
    if roles.get("root", {}).get("ab") and "data" in roles:
        out.append("ab")
    return out


def check_partitions(ident, board):
    """The board's [partitions]: the slot keeper, the default layout and
    one entry per partition, each a name and a role."""
    if "partitions" not in board:
        return
    b, p = board["id"], board["partitions"]
    where = f"{ident}: board {b} [partitions]"
    if not isinstance(p, dict):
        raise Bad(f"{where}: is a table")
    for k in p:
        if k not in ("layout", "slots", "part"):
            raise Bad(f"{where}: unknown {k!r}")
    if p.get("slots") not in SLOT_KEEPERS:
        raise Bad(f"{where}: slots {p.get('slots')!r} is not one of {', '.join(SLOT_KEEPERS)}")
    parts = p.get("part")
    if not isinstance(parts, list) or not parts or not all(isinstance(q, dict) for q in parts):
        raise Bad(f"{where}: part is a list of {{ name, role }} tables")
    seen, roles = set(), {}
    for q in parts:
        for k in q:
            if k not in PART_KEYS:
                raise Bad(f"{where}: unknown {k!r} in {q}")
        name, role, ab = q.get("name"), q.get("role"), q.get("ab", False)
        if not isinstance(name, str) or not PART_NAME.fullmatch(name):
            raise Bad(f"{where}: {name!r} is not a partition name")
        if role not in PART_ROLES:
            raise Bad(f"{where}: {name}: role {role!r} is not one of {', '.join(PART_ROLES)}")
        if not isinstance(ab, bool):
            raise Bad(f"{where}: {name}: ab is true or false")
        if ab and name[-2:] in ("_a", "_b"):
            raise Bad(f"{where}: {name}: an A/B pair is named without its slot suffix")
        if "size" in q and (not isinstance(q["size"], int) or isinstance(q["size"], bool) or q["size"] <= 0 or q["size"] % 4096):
            raise Bad(f"{where}: {name}: size is the partition's bytes, a multiple of 4096")
        for full in ([name + "_a", name + "_b"] if ab else [name]):
            if full in seen:
                raise Bad(f"{where}: {full} twice")
            seen.add(full)
        if role in PART_SINGLE and role in roles:
            raise Bad(f"{where}: {name} and {roles[role]['name']} are both the {role}")
        roles.setdefault(role, q)
        if role == "efs" and not never(name):
            raise Bad(f"{where}: {name}: a phone's own partition (efs) is one no flash writes: add it to NEVER "
                      "(tools/sku.py, yocto/fajita-flash.sh)")
        for layout in LAYOUTS:
            if never(name) and part_action(role, layout, p["slots"])[0] in ("write", "erase"):
                raise Bad(f"{where}: {name} holds what no flash may write (a phone's own data, the bootloader's or "
                          f"the firmware's): it cannot be the {role}")
    if "root" in roles and not roles["root"].get("ab"):
        raise Bad(f"{where}: the root is an A/B pair (ab = true)")
    if p["slots"] == "abl" and not roles.get("boot", {}).get("ab"):
        raise Bad(f"{where}: slots = \"abl\" keeps the slot in boot_a and boot_b: no boot pair (ab = true)")
    if p["slots"] == "state" and "state" not in roles:
        raise Bad(f"{where}: slots = \"state\" keeps the slot in the state partition: none")
    have = layouts(p)
    if p.get("layout") not in have:
        raise Bad(f"{where}: layout {p.get('layout')!r} is not one this board's partitions allow ({', '.join(have) or 'none'})")


def plan(board, layout=None):
    """The board's partitions in its order, slot suffixes added: (name,
    role, action, image) each, for the layout (the board's default when
    None)."""
    p = board.get("partitions")
    if not p:
        raise Bad(f"board {board['id']} has no [partitions]")
    layout = layout or p["layout"]
    if layout not in layouts(p):
        raise Bad(f"board {board['id']}: layout {layout!r} is not one its partitions allow ({', '.join(layouts(p))})")
    out = []
    for q in p["part"]:
        action, image = part_action(q["role"], layout, p["slots"])
        for name in ([q["name"] + "_a", q["name"] + "_b"] if q.get("ab") else [q["name"]]):
            out.append((name, q["role"], action, image or "-"))
    return out


def part_names(board, role):
    """The partitions of a role, slot suffixes added."""
    return [n for n, r, _, _ in plan(board, board["partitions"]["layout"]) if r == role]


def partitions_conf(board):
    """What phone-initramfs reads of the board's partitions
    (/etc/phone/partitions.conf): who keeps the slot, the names of the root
    pair, the data and the state partition. Empty without [partitions]:
    the initramfs then takes the emulator's disk's names."""
    p = board.get("partitions")
    if not p:
        return ""
    roles = {q["role"]: q["name"] for q in p["part"]}
    return (f"# The board {board['id']}'s partitions by role (tools/sku.py, sku/boards/{board['id']}.toml).\n"
            f"slots={p['slots']}\nroot={roles.get('root', '')}\ndata={roles.get('data', '')}\nstate={roles.get('state', '')}\n")


def check_partition_files(ident, board):
    """A board whose slots phone-initramfs keeps is the emulator's A/B disk:
    its partitions are the disk's (phone-ab.wks.in) and RAUC's slots
    (system.conf)."""
    p = board.get("partitions")
    if not p or p["slots"] != "state":
        return
    with open(WKS) as f:
        wks = set(re.findall(r"--part-name (\S+)", f.read()))
    with open(os.path.join(RAUC_DIR, "system.conf")) as f:
        rauc = re.findall(r"^device=/dev/disk/by-partlabel/(\S+)$", f.read(), re.M)
    names = [n for n, *_ in plan(board, "ab")]
    if set(names) != wks:
        raise Bad(f"{ident}: board {board['id']} [partitions] has {', '.join(sorted(names))}, "
                  f"{os.path.relpath(WKS, FW)} {', '.join(sorted(wks))}")
    if rauc != part_names(board, "root"):
        raise Bad(f"{ident}: RAUC's slots are {', '.join(rauc)}, the board's root pair {', '.join(part_names(board, 'root'))}")


def check_matrix(ident, keypad):
    table, seen, used = keys(), {}, set()
    for pos, key in keypad.get("matrix", {}).items():
        row, col = (int(x) for x in pos.split(","))
        if not (0 <= row < keypad["rows"] and 0 <= col < keypad["cols"]):
            raise Bad(f"{ident}: {keypad['id']} {pos} is outside its {keypad['rows']}x{keypad['cols']} matrix")
        if key not in table:
            raise Bad(f"{ident}: {keypad['id']} {pos}: no key {key!r} in keys.toml")
        if key in seen:
            raise Bad(f"{ident}: {keypad['id']}: {key} at {seen[key]} and {pos}")
        seen[key] = pos
        used.add(key)
    missing = (NEEDED | FORM_KEYS.get(keypad["form"], set())) - used
    if missing:
        raise Bad(f"{ident}: {keypad['id']} lacks {', '.join(sorted(missing))}")
    layer = keypad.get("fn", {})
    if layer and "fn" not in used:
        raise Bad(f"{ident}: {keypad['id']} has an Fn layer but no fn key")
    for key, ch in layer.items():
        if key not in used or key not in FN_KEYS:
            raise Bad(f"{ident}: {keypad['id']} Fn layer: {key!r} is not a character key of its matrix")
        if not isinstance(ch, str) or len(ch) != 1 or not ch.isprintable():
            raise Bad(f"{ident}: {keypad['id']} Fn layer: {key} types {ch!r}, not one character")
    check_ctrl(ident, keypad, used)


def check_ctrl(ident, keypad, used):
    """A keypad's ctrl: what stands in for a Ctrl key it lacks, a modifier
    then a key (fn+sym: Fn, then Sym, as on the E71), both on the matrix."""
    ctrl = keypad.get("ctrl")
    if ctrl is None:
        return
    first, _, second = ctrl.partition("+") if isinstance(ctrl, str) else ("", "", "")
    if first not in CTRL_MODIFIERS or not second or second in CTRL_MODIFIERS:
        raise Bad(f"{ident}: {keypad['id']} ctrl {ctrl!r} is not <{'|'.join(sorted(CTRL_MODIFIERS))}>+<key>")
    for key in (first, second):
        if key not in used:
            raise Bad(f"{ident}: {keypad['id']} ctrl {ctrl!r}: no {key} key on its matrix")
    if "ctrl" in used:
        raise Bad(f"{ident}: {keypad['id']} has a ctrl key and a ctrl {ctrl!r} for it")


# The modifiers phone-paint lets stand first in a keypad's ctrl.
CTRL_MODIFIERS = {"fn"}


# The character keys an Fn layer may give a second character, by the
# character the renderer reads from them.
FN_KEYS = {**{c: c for c in "abcdefghijklmnopqrstuvwxyz"}, "comma": ",", "dot": "."}


def sku_json(r):
    sku, board, display = r["sku"], r["board"], r["display"]
    out = {
        "id": sku["id"], "board": board["id"], "device": r["device"],
        "display": display["id"], "keypad": r["keypad"]["id"], "memory_mb": r["memory"],
        "web_gpu": bool(sku.get("web_gpu", board.get("gpu", False))),
        "modem": board.get("modem", "sim"), "region": sku.get("region", ""), "product": r["product"]["id"],
    }
    if r["paint_gpu"] != "off":
        out["paint_gpu"] = r["paint_gpu"]
    if r["gpu_render"]:
        out["gpu_render"] = r["gpu_render"]
    if is_phone(board):
        # The panel as drawn (shell.SKU's width, height, scale): the Shell
        # lays pages out for it and tells the renderer.
        out["width"], out["height"] = phone_screen(sku["id"], display, r["rotate"])
        out["scale"] = display["scale"]
        if "keymap" in board:
            out["keymap"] = {code: board["keymap"][code] for code in sorted(board["keymap"], key=int)}
    if r["rotate"]:
        out["rotate"] = r["rotate"]
    # The renderer reads a touchscreen only where the display has one.
    if display.get("touchscreen"):
        out["touch"] = True
    if sound_switch(board):
        out["sound_switch"] = sound_switch(board)
    if board.get("input", {}).get("lid"):
        out["lid"] = board["input"]["lid"]
    if "outer" in r:
        o = r["outer"]
        out["outer"] = {"display": o["id"], "width": o["width"], "height": o["height"], "kind": o.get("kind", "lcd"),
                        "color": o.get("color", "rgb"), "drm_driver": o["drm_driver"]}
        if "width_mm" in o:
            out["outer"]["width_mm"], out["outer"]["height_mm"] = o["width_mm"], o["height_mm"]
    if r["keypad"].get("fn"):
        out["fn"] = {FN_KEYS[k]: ch for k, ch in sorted(r["keypad"]["fn"].items())}
    if r["keypad"].get("ctrl"):
        out["ctrl"] = r["keypad"]["ctrl"]
    leds = board.get("leds", {})
    if "notify" in leds:
        out["leds"] = {"notify": {"name": leds["notify"], **({"color": leds["notify_color"]} if leds.get("notify_color") else {})}}
    for k in ("backlight", "keyboard_backlight", "light_sensor"):
        if k in board:
            out[k] = board[k]
    if board.get("fingerprint", "none") != "none":
        out["fingerprint"] = board["fingerprint"]
    if r["keypad"].get("fingerprint"):
        out["fingerprint_at"] = r["keypad"]["fingerprint"]
    # Virtual boards (the emulator) cannot be woken from suspend: neither
    # their keys nor their RTC wake them, so they never suspend.
    if board.get("suspend", not board.get("virtual")):
        out["suspend"] = True
    if board.get("power_key"):
        out["power_key"] = True
    if board.get("nfc"):
        out["nfc"] = board["nfc"]
    if board.get("gnss"):
        out["gnss"] = board["gnss"]
    if board.get("audio"):
        out["audio"] = {k: board["audio"][k] for k in AUDIO if k in board["audio"]}
    if "rtc" in board:
        out["rtc"] = rtc(board)
    return out


def sim(r):
    """What the developer machine's Shell simulates of a SKU (kario run
    -sku): the form factor, the keypad and its Fn layer, the memory tier,
    the region. No id: an SKU id is what tells the Shell it runs on a phone
    (it would power the machine off), and no LEDs, backlight, modem, NFC,
    fingerprint or location backends, which name the board's hardware."""
    sku = r["sku"]
    dw, dh, form = DEVICES[r["device"]]
    shell = {"board": r["board"]["id"], "device": r["device"], "display": r["display"]["id"],
             "keypad": r["keypad"]["id"], "memory_mb": r["memory"], "region": sku.get("region", ""),
             "product": r["product"]["id"]}
    if r["keypad"].get("fn"):
        shell["fn"] = {FN_KEYS[k]: ch for k, ch in sorted(r["keypad"]["fn"].items())}
    if r["keypad"].get("ctrl"):
        shell["ctrl"] = r["keypad"]["ctrl"]
    if r["keypad"].get("fingerprint"):
        shell["fingerprint_at"] = r["keypad"]["fingerprint"]
    return {"id": sku["id"], "form": form, "width": dw, "height": dh, "emulator": bool(r["board"].get("virtual")),
            "shell": shell}


def sims_json():
    """The products' SKUs and the emulator's, of this tree only (git
    tracks the table; KARIO_SKU_PATH's are private); whole phones are test
    devices, not something an app is written for."""
    rs = [resolve(s) for s in names("skus", own=True)]
    return json.dumps([sim(r) for r in rs if not is_phone(r["board"])], indent=2) + "\n"


def keymap(keypad):
    """linux,keymap cells: MATRIX_KEY(row, col, code) = row << 24 | col << 16 | code."""
    table = keys()
    cells = []
    for pos, key in sorted(keypad["matrix"].items(), key=lambda kv: tuple(int(x) for x in kv[0].split(","))):
        row, col = (int(x) for x in pos.split(","))
        cells.append(f"0x{row << 24 | col << 16 | table[key][1]:08x}")
    return cells


def sharp_memory(w):
    """A Sharp memory LCD's own properties and nodes: VCOM (the inversion
    its liquid crystal needs about once a second) from a PWM, which runs
    while the SoC sleeps; the driver's software VCOM would wake it."""
    ctl = w["outer_vcom"].split()[0]
    return ['sharp,vcom-mode = "pwm";', f"pwms = <&{w['outer_vcom']}>;"], f'\n&{ctl} {{\n\tstatus = "okay";\n}};\n'


# The SPI panels the overlay wires, by kernel option: the [wiring] they
# need besides outer_spi, and their properties (sharp_memory).
SPI_PANELS = {"CONFIG_TINYDRM_SHARP_MEMORY": (("outer_vcom",), sharp_memory)}


def outer_overlay(r):
    """The outer display's node on its SPI bus, "" without one."""
    if "outer" not in r:
        return ""
    o, w = r["outer"], r["board"]["wiring"]
    props, nodes = SPI_PANELS[o["kconfig"]][1](w)
    compat = ", ".join(f'"{c}"' for c in o["compatible"])
    props = [f"compatible = {compat};", "reg = <0>;", f"spi-max-frequency = <{o['spi_max_hz']}>;"] + \
        (["spi-cs-high;"] if o.get("spi_cs_high") else []) + \
        ([f"enable-gpios = <&{w['outer_enable']}>;"] if "outer_enable" in w else []) + props
    body = "".join(f"\t\t{p}\n" for p in props)
    return f"""
&{w['outer_spi']} {{
\tstatus = "okay";
\t#address-cells = <1>;
\t#size-cells = <0>;

\tsku_outer: display@0 {{
{body}\t}};
}};
{nodes}"""


def lid_overlay(r):
    """The lid's Hall switch as gpio-keys (lid_gpio, active while the lid
    is shut): an SW_LID switch that wakes the phone, its input device named
    [input] lid; "" without one."""
    w = r["board"]["wiring"]
    if "lid_gpio" not in w:
        return ""
    return f"""
&{{/}} {{
\tsku_lid: lid {{
\t\tcompatible = "gpio-keys";
\t\tlabel = "{r['board']['input']['lid']}";

\t\tevent-lid {{
\t\t\tlabel = "Lid";
\t\t\tgpios = <&{w['lid_gpio']}>;
\t\t\tlinux,input-type = <5>;\t/* EV_SW */
\t\t\tlinux,code = <0>;\t\t/* SW_LID */
\t\t\tdebounce-interval = <15>;
\t\t\twakeup-source;
\t\t}};
\t}};
}};
"""


def overlay(r):
    """The displays and the keypad as a device tree overlay on the board's tree."""
    board, display, keypad, w = r["board"], r["display"], r["keypad"], r["board"]["wiring"]
    compat = ", ".join(f'"{c}"' for c in display["compatible"])
    cells = keymap(keypad)
    rows = "\n".join("\t\t\t\t" + " ".join(cells[i:i + 4]) for i in range(0, len(cells), 4))
    chip = CHIPS[keypad["chip"]][0]
    outer = f", {r['outer']['name']}" if "outer" in r else ""
    return f"""// SKU {r['sku']['id']}: {display['name']}{outer} and {keypad['name']} on {board['name']}.
// Generated by tools/sku.py from fw/sku/; do not edit.
/dts-v1/;
/plugin/;

&{w['panel_dsi']} {{
\tvdda-supply = <&{w['dsi_vdda']}>;
\tstatus = "okay";
\t#address-cells = <1>;
\t#size-cells = <0>;

\tpanel@0 {{
\t\tcompatible = {compat};
\t\treg = <0>;
\t\treset-gpios = <&{w['panel_reset']}>;
\t\tVCC-supply = <&{w['panel_vcc']}>;
\t\tIOVCC-supply = <&{w['panel_iovcc']}>;
{f'\t\trotation = <{r["rotate"]}>;\n' if r["rotate"] else ''}
\t\tport {{
\t\t\tsku_panel_in: endpoint {{
\t\t\t\tremote-endpoint = <&{dsi_out(w)}>;
\t\t\t}};
\t\t}};
\t}};
}};

&{dsi_out(w)} {{
\tdata-lanes = <{' '.join(str(i) for i in range(display.get('lanes', 4)))}>;
\tremote-endpoint = <&sku_panel_in>;
}};

&{w['panel_dsi']}_phy {{
\tstatus = "okay";
}};

&mdss {{
\tstatus = "okay";
}};

&{w['keypad_bus']} {{
\tstatus = "okay";
\t#address-cells = <1>;
\t#size-cells = <0>;

\tkeypad@{keypad['address']:x} {{
\t\tcompatible = "{chip}";
\t\treg = <0x{keypad['address']:x}>;
\t\tinterrupts-extended = <&{w['keypad_irq']}>;
\t\tkeypad,num-rows = <{keypad['rows']}>;
\t\tkeypad,num-columns = <{keypad['cols']}>;
\t\tlinux,keymap = <
{rows}
\t\t\t>;
\t\twakeup-source;
\t}};
}};
{outer_overlay(r)}{lid_overlay(r)}"""


def no_machine(r):
    if is_phone(r["board"]) and "machine" not in r["board"]:
        raise Bad(f"{r['sku']['id']}: board {r['board']['id']} is a whole phone without a Yocto machine yet: "
                  "tools/phone-chroot/kario-phone.sh runs the image's root file system on it (24-sku-framework.md §6.2)")


def kas(r):
    no_machine(r)
    sku, board, display = r["sku"], r["board"], r["display"]
    lines = [f'PHONE_SKU = "{sku["id"]}"']
    if board.get("virtual"):
        touch = f" -device {display['touchscreen']}" if display.get("touchscreen") else ""
        # The outer display as a second virtio-gpu device of its own size,
        # a DRM device of its own as on the phone.
        outer = f" -device virtio-gpu-pci,xres={r['outer']['width']},yres={r['outer']['height']}" if "outer" in r else ""
        lines.append(f'QB_GRAPHICS:forcevariable = "-device virtio-gpu-pci,xres={display["width"]},yres={display["height"]}{touch}{outer}"')
        lines.append(f'QB_MEM:forcevariable = "-m {r["memory"]}"')
    elif not is_phone(board):
        lines.append(f'PHONE_SKU_DTB = "{board["dtb"]}"')
    if board.get("ims"):
        lines.append(f'PHONE_IMS = "{board["ims"]}"')
    if board.get("gpu"):
        # What lets phone-sku's PHONE_PAINT_GPU_DEFAULT (yocto/kas/paint-gpu.yml)
        # put phone-paint on the GPU, as check_gpu lets paint_gpu.
        lines.append('PHONE_SKU_GPU = "1"')
    if board.get("partitions"):
        # RAUC's slots and its boot loader backend (rauc-conf.bbappend), the
        # update bundle's slots (phone-bundle.bb).
        lines.append(f'PHONE_SLOT_KEEPER = "{board["partitions"]["slots"]}"')
        lines.append(f'PHONE_ROOT_SLOTS = "{" ".join(part_names(board, "root"))}"')
        lines.append(f'PHONE_BOOT_SLOTS = "{" ".join(part_names(board, "boot"))}"')
    conf = "\n".join("    " + l for l in lines)
    return f"""# Generated by tools/sku.py for SKU {sku['id']}; do not edit.
header:
  version: 14

machine: {board['machine']}

local_conf_header:
  zzz-sku: |
{conf}
"""


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def generic_machines():
    """The Yocto machines whose images are not one phone's (the boards that
    are not whole phones), of those this build has (this layer's, QEMU's):
    their root file system runs on any of them. Another layer's machine
    may tune for a newer core."""
    boards = [load("boards", b) for b in names("boards")]
    phones = {b.get("machine") for b in boards if is_phone(b)}
    return sorted({b["machine"] for b in boards if "machine" in b and (b.get("virtual") or machine_conf(b))} - phones)


def phone_env(r):
    """What tools/phone-chroot reads of a whole phone's board, as shell
    assignments: the sound card's values are sku.json's audio; the board's
    own Yocto machine (none yet for some) and the generic ones, whose
    images the kit also takes."""
    board, audio = r["board"], sku_json(r).get("audio", {})
    env = {
        "KARIO_SKU": r["sku"]["id"], "KARIO_BOARD": board["id"], "KARIO_COMPATIBLE": board["compatible"],
        "KARIO_MACHINE": board.get("machine", ""), "KARIO_GENERIC_MACHINES": " ".join(generic_machines()),
        "KARIO_NURA": board.get("nura", ""), "KARIO_MEMORY_MB": str(r["memory"]),
        "KARIO_BUTTONS": "|".join(board.get("input", {}).get("buttons", [])),
        "KARIO_SOUND_SWITCH": sound_switch(board),
        "KARIO_UNVERIFIED": " ".join(board.get("unverified", [])),
        "KARIO_AUDIO_PCM": audio.get("pcm", ""), "KARIO_AUDIO_CARD": audio.get("card", ""),
        "KARIO_AUDIO_VERB": audio.get("verb", ""), "KARIO_AUDIO_DEVICE": audio.get("device", ""),
        "KARIO_AUDIO_QUIET": "|".join(board.get("audio", {}).get("quiet_controls", [])),
        "KARIO_AUDIO_QUIET_LEVEL": board.get("audio", {}).get("quiet_level", ""),
        "KARIO_PARTITIONS": " ".join(f"{n}:{role}" for n, role, _, _ in plan(board)) if board.get("partitions") else "",
    }
    return "".join(f"{k}={shlex.quote(v)}\n" for k, v in env.items())


def files(ident, out):
    """Only the SKU's own files, into out: what tools/phone-chroot puts into
    an image built for another SKU (the binaries are the same for all)."""
    r = resolve(ident)
    texts = {
        "sku.json": json.dumps(sku_json(r), ensure_ascii=False, indent=2) + "\n",
        "profile.json": json.dumps(r["product"].get("profile", {}), ensure_ascii=False, indent=2) + "\n",
    }
    if is_phone(r["board"]):
        texts["phone.env"] = phone_env(r)
    for name, text in texts.items():
        write(os.path.join(out, name), text)
    return sorted(texts)


def source_note(rev=None):
    """What the image is built from, its share/phone/source.txt: this tree's
    commit and changes, or the commit rev names (SRC=git)."""
    note = os.path.join(FW, "tools", "phone-chroot", "tree-note.sh")
    return subprocess.run(["sh", note, *([rev] if rev else [])], capture_output=True, text=True, check=True).stdout


def gen(ident, rev=None):
    r = resolve(ident)
    no_machine(r)
    out = os.path.join(OUT, ident)
    files = {
        "sku.json": json.dumps(sku_json(r), ensure_ascii=False, indent=2) + "\n",
        "profile.json": json.dumps(r["product"].get("profile", {}), ensure_ascii=False, indent=2) + "\n",
        "partitions.conf": partitions_conf(r["board"]),
        "watchdog.conf": watchdog_conf(r["board"]),
    }
    if not r["board"].get("virtual") and not is_phone(r["board"]):
        files["overlay.dts"] = overlay(r)
    shutil.rmtree(FILES, ignore_errors=True)
    shutil.rmtree(out, ignore_errors=True)
    for name, text in files.items():
        write(os.path.join(out, name), text)
        write(os.path.join(FILES, name), text)
    write(os.path.join(FILES, "source.txt"), source_note(rev))
    write(os.path.join(out, "sku.yml"), kas(r))
    write(KAS, kas(r))
    print(f"SKU {ident}: {r['board']['name']}, {r['display']['name']}, {r['keypad']['name']}, "
          f"{r['device']}, {r['memory']} MB -> {os.path.relpath(out, FW)}")


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def package(ident, images):
    """The firmware package: the kernel, the device tree, the root file
    system and the SKU, with a manifest of their hashes (images: the
    build's deploy/images)."""
    r = resolve(ident)
    no_machine(r)
    board, machine = r["board"], r["board"]["machine"]
    if is_phone(board):
        raise Bad(f"{ident}: a whole phone has no firmware package: its build's boot and userdata images are "
                  f"flashed as they are ({board.get('flash', 'no flash script yet')})")
    deploy = os.path.join(images, machine)
    # The kernel carries phone-initramfs, which mounts the root file system.
    kernel = "Image" if board.get("virtual") else "Image.gz"
    want = {"rootfs.ext4": f"phone-image-{machine}.rootfs.ext4", kernel: f"{kernel}-initramfs-{machine}.bin"}
    if not board.get("virtual"):
        want["sku.dtb"] = f"phone-sku-{ident}.dtb"
    paths = {name: os.path.realpath(os.path.join(deploy, src)) for name, src in want.items()}
    for name, path in paths.items():
        if not os.path.exists(path):
            raise Bad(f"{ident}: {want[name]} is not in {deploy} (built for this SKU?)")
    out = os.path.join(OUT, ident, "firmware")
    shutil.rmtree(out, ignore_errors=True)
    os.makedirs(out)
    manifest = {"sku": sku_json(r), "board": board["id"], "machine": machine, "files": {}}
    for name, path in paths.items():
        dst = os.path.join(out, name)
        try:
            os.link(path, dst)
        except OSError:
            shutil.copy2(path, dst)
        manifest["files"][name] = {"sha256": sha256(dst), "size": os.path.getsize(dst)}
    shutil.copy2(os.path.join(OUT, ident, "sku.json"), os.path.join(out, "sku.json"))
    write(os.path.join(out, "manifest.json"), json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    print(f"firmware for {ident}: {os.path.relpath(out, FW)} ({', '.join(manifest['files'])})")


def main(argv):
    try:
        if argv[:1] == ["list"]:
            for s in names("skus"):
                r = resolve(s)
                print(f"{s:24} {r['board']['name']} · {r['display']['name']} · {r['keypad']['name']} · {r['sku']['memory']}")
        elif argv[:1] == ["check"]:
            for s in names("skus"):
                r = resolve(s)
                if not r["board"].get("virtual") and not is_phone(r["board"]):
                    overlay(r)
            print(f"{len(names('skus'))} SKUs check out")
        elif len(argv) == 2 and argv[0] == "gen":
            gen(argv[1])
        elif len(argv) == 4 and argv[0] == "gen" and argv[2] == "--rev":
            gen(argv[1], argv[3])
        elif len(argv) == 3 and argv[0] == "files":
            print(f"SKU {argv[1]}: {', '.join(files(argv[1], argv[2]))} -> {argv[2]}")
        elif len(argv) == 3 and argv[0] == "package":
            package(argv[1], argv[2])
        elif argv == ["sims"]:
            write(SIMS, sims_json())
            print(f"{len(names('skus', own=True))} SKUs -> {os.path.relpath(SIMS, FW)}")
        elif argv[:1] == ["partitions"] and len(argv) in (2, 3):
            board = load("boards", argv[1])
            check_partitions(argv[1], board)
            layout = argv[2] if len(argv) == 3 else board.get("partitions", {}).get("layout")
            rows = plan(board, layout)
            print(f"# {argv[1]}: layout {layout}, slots {board['partitions']['slots']}")
            for row in rows:
                print(" ".join(row))
        elif len(argv) == 2 and argv[0] == "parts":
            board = load("boards", argv[1])
            check_parts(argv[1], board)
            print(f"# {argv[1]}: kind, bus, compatible, mainline driver, part")
            for line in parts_table(board):
                print(line)
        else:
            print(__doc__.strip(), file=sys.stderr)
            return 2
    except Bad as e:
        print("sku:", e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
