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
  sku.py package <sku> <images>   collect the firmware package from a build
"""
import hashlib
import json
import os
import re
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

# The form factors of shell/device.go (sku_test.py compares).
DEVICES = {"bar-t9": (480, 640, "t9"), "bar-qwerty": (640, 480, "qwerty"), "communicator": (800, 352, "full")}
MEMORY = {"1g": 1024, "2g": 2048, "3g": 3072, "4g": 4096}
CHIPS = {"tca8418": ("ti,tca8418", "CONFIG_KEYBOARD_TCA8418")}
# The keys every keypad needs, and those of each kind (10 §1).
NEEDED = {"soft-left", "soft-right", "ok", "back", "clear", "call", "end", "up", "down", "left", "right"}
FORM_KEYS = {"t9": set("0123456789") | {"star", "pound"}, "qwerty": set("abcdefghijklmnopqrstuvwxyz") | {"space", "shift"}}


class Bad(Exception):
    pass


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
    for kind in ("board", "display", "keypad", "product"):
        if kind not in sku:
            raise Bad(f"{ident}: no {kind}")
        r[kind] = load(kind + "s", sku[kind])
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
    pw, ph = (display["height"], display["width"]) if rotate else (display["width"], display["height"])
    if (pw, ph) != (dw, dh):
        raise Bad(f"{ident}: the display shows {pw}x{ph}, a {device} is {dw}x{dh}")
    if not virtual:
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
    r["device"], r["rotate"], r["memory"] = device, rotate, MEMORY[sku["memory"]]
    return r


def class_name(s):
    return isinstance(s, str) and 0 < len(s) < 64 and "/" not in s and s.strip() == s


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
    if "backlight" in board and not class_name(board["backlight"]):
        raise Bad(f"{ident}: board {board['id']}: backlight {board['backlight']!r} is not a class name")
    if board.get("nfc", "sim") != "sim" and not str(board.get("nfc")).startswith("nci:/dev/"):
        raise Bad(f"{ident}: board {board['id']}: nfc {board['nfc']!r} is not sim or nci:/dev/<node>")
    if board.get("fingerprint", "none") not in ("sim", "none"):
        raise Bad(f"{ident}: board {board['id']}: fingerprint {board['fingerprint']!r} is not sim or none")


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
    if r["rotate"]:
        out["rotate"] = r["rotate"]
    if r["keypad"].get("fn"):
        out["fn"] = {FN_KEYS[k]: ch for k, ch in sorted(r["keypad"]["fn"].items())}
    leds = board.get("leds", {})
    if "notify" in leds:
        out["leds"] = {"notify": {"name": leds["notify"], **({"color": leds["notify_color"]} if leds.get("notify_color") else {})}}
    if "backlight" in board:
        out["backlight"] = board["backlight"]
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
    return out


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


def kas(r):
    sku, board, display = r["sku"], r["board"], r["display"]
    lines = [f'PHONE_SKU = "{sku["id"]}"']
    if board.get("virtual"):
        lines.append(f'QB_GRAPHICS:forcevariable = "-device virtio-gpu-pci,xres={display["width"]},yres={display["height"]}"')
        lines.append(f'QB_MEM:forcevariable = "-m {r["memory"]}"')
    else:
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


def gen(ident):
    r = resolve(ident)
    out = os.path.join(OUT, ident)
    files = {
        "sku.json": json.dumps(sku_json(r), ensure_ascii=False, indent=2) + "\n",
        "profile.json": json.dumps(r["product"].get("profile", {}), ensure_ascii=False, indent=2) + "\n",
    }
    if not r["board"].get("virtual"):
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
    board, machine = r["board"], r["board"]["machine"]
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
                resolve(s)
                if not load("boards", load("skus", s)["board"]).get("virtual"):
                    overlay(resolve(s))
            print(f"{len(names('skus'))} SKUs check out")
        elif len(argv) == 2 and argv[0] == "gen":
            gen(argv[1])
        elif len(argv) == 3 and argv[0] == "package":
            package(argv[1], argv[2])
        else:
            print(__doc__.strip(), file=sys.stderr)
            return 2
    except Bad as e:
        print("sku:", e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
