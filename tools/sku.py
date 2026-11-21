#!/usr/bin/env python3
"""SKU generator (24-sku-framework.md).

A SKU names a board, a display, a keypad, a memory tier, a region and a
product (fw/sku/). This expands one into what an image needs:

  share/phone/sku.json            the variant, read by phone-shell
  profile.d/20-product.json       the product's composition layer
  overlay.dts                     the display and keypad in the board's
                                  device tree (real boards)
  yocto/kas/generated/sku.yml     the build: machine, QEMU's display, ...

  sku.py list                     the SKUs
  sku.py check                    expand every SKU (no files written)
  sku.py gen <sku>                write the files for one
  sku.py files <sku> <dir>        only its sku.json, profile.json and, for a
                                  whole phone, phone.env (tools/phone-chroot)
  sku.py package <sku> <images>   collect the firmware package from a build
  sku.py sims                     write the table kario run -sku simulates
                                  (cmd/kario/skus.json)
"""
import hashlib
import json
import os
import re
import shlex
import shutil
import sys
import tomllib

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKUS = os.path.join(FW, "sku")
LAYER = os.path.join(FW, "yocto", "meta-phone")
FILES = os.path.join(LAYER, "recipes-phone", "phone-sku", "files")
KAS = os.path.join(FW, "yocto", "kas", "generated", "sku.yml")
KCONFIG = os.path.join(LAYER, "recipes-kernel", "linux", "files", "phone-qcom.cfg")
OUT = os.path.join(FW, "build", "sku")
SIMS = os.path.join(FW, "cmd", "kario", "skus.json")

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
INPUT_KEYS = {"buttons"}
# How big one logical pixel may be on a whole phone's panel, in mm. The
# 2.8" 480x640 target at 2x has 42.7 mm / 240 = 0.178 mm; the least taken
# is a 2.4" 240x320 KaiOS phone at 1x (0.152 mm), the most twice the target.
LOGICAL_MM = (0.15, 0.36)


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


def dt_labels(src, dtb):
    """The labels a board's tree defines, following its #includes."""
    root = os.path.join(src, "arch", "arm64", "boot", "dts")
    found = [p for p in (os.path.join(root, v, dtb[:-4] + ".dts") for v in os.listdir(root)) if os.path.exists(p)]
    if not found:
        raise Bad(f"{dtb}: no such tree in {root}")
    labels, todo, seen = set(), [found[0]], set()
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


def wiring_labels(w):
    """The labels a board's wiring makes the overlay refer to."""
    out = {w["keypad_bus"], w["panel_dsi"], w["panel_dsi"] + "_out", w["panel_dsi"] + "_phy", "mdss",
           w["panel_vcc"], w["panel_iovcc"], w["dsi_vdda"]}
    for ref in (w["keypad_irq"], w["panel_reset"]):
        out.add(ref.split()[0])
    return out


def load(kind, ident):
    path = os.path.join(SKUS, kind, ident + ".toml")
    try:
        with open(path, "rb") as f:
            data = tomllib.load(f)
    except FileNotFoundError:
        raise Bad(f"no {kind[:-1]} {ident!r} ({os.path.relpath(path, FW)})")
    if data.get("id") != ident:
        raise Bad(f"{os.path.relpath(path, FW)}: id is {data.get('id')!r}")
    return data


def keys():
    with open(os.path.join(SKUS, "keys.toml"), "rb") as f:
        t = tomllib.load(f)
    return {**t["keys"], **t.get("unmapped", {})}


def names(kind):
    return sorted(f[:-5] for f in os.listdir(os.path.join(SKUS, kind)) if f.endswith(".toml"))


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
        for kind in ("display", "keypad"):
            if kind in sku:
                raise Bad(f"{ident}: board {sku['board']} is a whole phone: its {kind} is its own (leave {kind} out)")
        r["display"] = check_phone(ident, r["board"])
        r["keypad"] = dict(OWN_KEYPAD if "keymap" in r["board"] else PHONE_KEYPAD)
    elif "keymap" in r["board"]:
        raise Bad(f"{ident}: board {sku['board']}: a keymap is a whole phone's (class = \"phone\"); "
                  "a keypad module's codes come from its matrix")
    board, display, keypad = r["board"], r["display"], r["keypad"]
    if sku.get("memory") not in MEMORY:
        raise Bad(f"{ident}: memory must be one of {sorted(MEMORY)}")
    virtual = bool(board.get("virtual"))
    if virtual != bool(display.get("virtual")) or virtual != (keypad.get("chip") == "usb"):
        raise Bad(f"{ident}: QEMU's board takes QEMU's display and keyboard, a real board real ones")
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
        have = kconfig()
        for cfg in (CHIPS[keypad["chip"]][1], display.get("kconfig")):
            if cfg not in have:
                raise Bad(f"{ident}: {cfg} is not in {os.path.relpath(KCONFIG, FW)}")
        check_matrix(ident, keypad)
        src = kernel_src()
        if src:
            missing = wiring_labels(board["wiring"]) - dt_labels(src, board["dtb"])
            if missing:
                raise Bad(f"{ident}: {board['dtb']} has no {', '.join(sorted(missing))}")
    check_leds(ident, board)
    check_gnss(ident, board)
    check_unverified(ident, board)
    r["device"], r["rotate"], r["memory"] = device, rotate, MEMORY[sku["memory"]]
    return r


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
    if is_phone(board):
        # The panel as drawn (shell.SKU's width, height, scale): the Shell
        # lays pages out for it and tells the renderer.
        out["width"], out["height"] = phone_screen(sku["id"], display, r["rotate"])
        out["scale"] = display["scale"]
        if "keymap" in board:
            out["keymap"] = {code: board["keymap"][code] for code in sorted(board["keymap"], key=int)}
    if r["rotate"]:
        out["rotate"] = r["rotate"]
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
    return {"id": sku["id"], "form": form, "width": dw, "height": dh, "emulator": bool(r["board"].get("virtual")),
            "shell": shell}


def sims_json():
    """The products' SKUs and the emulator's; whole phones are test
    devices, not something an app is written for."""
    rs = [resolve(s) for s in names("skus")]
    return json.dumps([sim(r) for r in rs if not is_phone(r["board"])], indent=2) + "\n"


def keymap(keypad):
    """linux,keymap cells: MATRIX_KEY(row, col, code) = row << 24 | col << 16 | code."""
    table = keys()
    cells = []
    for pos, key in sorted(keypad["matrix"].items(), key=lambda kv: tuple(int(x) for x in kv[0].split(","))):
        row, col = (int(x) for x in pos.split(","))
        cells.append(f"0x{row << 24 | col << 16 | table[key][1]:08x}")
    return cells


def overlay(r):
    """The display and the keypad as a device tree overlay on the board's tree."""
    board, display, keypad, w = r["board"], r["display"], r["keypad"], r["board"]["wiring"]
    compat = ", ".join(f'"{c}"' for c in display["compatible"])
    cells = keymap(keypad)
    rows = "\n".join("\t\t\t\t" + " ".join(cells[i:i + 4]) for i in range(0, len(cells), 4))
    chip = CHIPS[keypad["chip"]][0]
    return f"""// SKU {r['sku']['id']}: {display['name']} and {keypad['name']} on {board['name']}.
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
\t\t\t\tremote-endpoint = <&{w['panel_dsi']}_out>;
\t\t\t}};
\t\t}};
\t}};
}};

&{w['panel_dsi']}_out {{
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
"""


def no_machine(r):
    if is_phone(r["board"]) and "machine" not in r["board"]:
        raise Bad(f"{r['sku']['id']}: board {r['board']['id']} is a whole phone without a Yocto machine yet: "
                  "tools/phone-chroot/kario-phone.sh runs the image's root file system on it (24-sku-framework.md §6.2)")


def kas(r):
    no_machine(r)
    sku, board, display = r["sku"], r["board"], r["display"]
    lines = [f'PHONE_SKU = "{sku["id"]}"']
    if board.get("virtual"):
        lines.append(f'QB_GRAPHICS:forcevariable = "-device virtio-gpu-pci,xres={display["width"]},yres={display["height"]}"')
        lines.append(f'QB_MEM:forcevariable = "-m {r["memory"]}"')
    elif not is_phone(board):
        lines.append(f'PHONE_SKU_DTB = "{board["dtb"]}"')
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


def phone_env(r):
    """What tools/phone-chroot reads of a whole phone's board, as shell
    assignments."""
    board = r["board"]
    env = {
        "KARIO_SKU": r["sku"]["id"], "KARIO_BOARD": board["id"], "KARIO_COMPATIBLE": board["compatible"],
        "KARIO_NURA": board.get("nura", ""), "KARIO_MEMORY_MB": str(r["memory"]),
        "KARIO_BUTTONS": "|".join(board.get("input", {}).get("buttons", [])),
        "KARIO_UNVERIFIED": " ".join(board.get("unverified", [])),
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


def gen(ident):
    r = resolve(ident)
    no_machine(r)
    out = os.path.join(OUT, ident)
    files = {
        "sku.json": json.dumps(sku_json(r), ensure_ascii=False, indent=2) + "\n",
        "profile.json": json.dumps(r["product"].get("profile", {}), ensure_ascii=False, indent=2) + "\n",
    }
    if not r["board"].get("virtual") and not is_phone(r["board"]):
        files["overlay.dts"] = overlay(r)
    shutil.rmtree(FILES, ignore_errors=True)
    shutil.rmtree(out, ignore_errors=True)
    for name, text in files.items():
        write(os.path.join(out, name), text)
        write(os.path.join(FILES, name), text)
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
        elif len(argv) == 3 and argv[0] == "files":
            print(f"SKU {argv[1]}: {', '.join(files(argv[1], argv[2]))} -> {argv[2]}")
        elif len(argv) == 3 and argv[0] == "package":
            package(argv[1], argv[2])
        elif argv == ["sims"]:
            write(SIMS, sims_json())
            print(f"{len(names('skus'))} SKUs -> {os.path.relpath(SIMS, FW)}")
        else:
            print(__doc__.strip(), file=sys.stderr)
            return 2
    except Bad as e:
        print("sku:", e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
