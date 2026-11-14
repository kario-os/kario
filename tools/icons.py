#!/usr/bin/env python3
"""Draw the system apps' icons (original artwork) into apps/<app>/icon.png.

Each icon is a rounded square with a vertical gradient and a white glyph,
drawn at 4x and downsampled for smooth edges. Re-run after changing a design
(naming icons draws only those):

    python3 tools/icons.py [snake ...]
"""
import math
import os
import sys

from PIL import Image, ImageDraw, ImageFont

S = 384          # drawing size
OUT = 96         # shipped size (the pixel renderer shows 44-48 px)
WHITE = (255, 255, 255, 255)
HERE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FONT = "/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf"


def hexc(h):
    h = h.lstrip("#")
    return tuple(int(h[i : i + 2], 16) for i in (0, 2, 4))


def base(top, bottom):
    grad = Image.new("RGBA", (S, S))
    t, b = hexc(top), hexc(bottom)
    px = grad.load()
    for y in range(S):
        f = y / (S - 1)
        c = tuple(round(t[i] + (b[i] - t[i]) * f) for i in range(3)) + (255,)
        for x in range(S):
            px[x, y] = c
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle((8, 8, S - 9, S - 9), radius=86, fill=255)
    out = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    out.paste(grad, (0, 0), mask)
    return out


def glyph():
    layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    return layer, ImageDraw.Draw(layer)


def line(d, pts, w, fill=WHITE):
    d.line(pts, fill=fill, width=w)
    for x, y in (pts[0], pts[-1]):
        d.ellipse((x - w / 2, y - w / 2, x + w / 2, y + w / 2), fill=fill)


def phone(d):
    # Drawn upright (pads on the right, handle on the left), then tilted.
    m = Image.new("L", (S, S), 0)
    md = ImageDraw.Draw(m)
    md.rounded_rectangle((148, 54, 262, 124), radius=30, fill=255)
    md.rounded_rectangle((148, 260, 262, 330), radius=30, fill=255)
    md.arc((112, 70, 352, 314), 110, 250, fill=255, width=62)
    d.bitmap((0, 0), m.rotate(45, center=(S / 2, S / 2), resample=Image.BICUBIC), fill=WHITE)


def messages(d, accent):
    d.rounded_rectangle((70, 88, 314, 262), radius=56, fill=WHITE)
    d.polygon([(118, 236), (96, 312), (184, 252)], fill=WHITE)
    for x in (134, 192, 250):
        d.ellipse((x - 18, 157, x + 18, 193), fill=accent)


def bezier(p0, p1, p2, n=24):
    return [((1 - t) ** 2 * p0[0] + 2 * (1 - t) * t * p1[0] + t * t * p2[0],
             (1 - t) ** 2 * p0[1] + 2 * (1 - t) * t * p1[1] + t * t * p2[1]) for t in (i / n for i in range(n + 1))]


def goose(d, accent):
    # Hongyan ("wild goose", the bird that carries letters in Chinese
    # poetry): a wild goose in flight over a letter.
    for side in (-1, 1):
        tip = (192 + side * 126, 78)
        upper = bezier((192, 150), (192 + side * 50, 84), tip)
        lower = bezier(tip, (192 + side * 70, 138), (192, 178))
        d.polygon(upper + lower, fill=WHITE)
    d.ellipse((174, 140, 210, 190), fill=WHITE)
    d.rounded_rectangle((96, 210, 288, 316), radius=20, fill=WHITE)
    d.line([(112, 226), (192, 278), (272, 226)], fill=accent, width=14, joint="curve")


def coin(d, accent):
    d.ellipse((76, 76, 308, 308), fill=WHITE)
    d.ellipse((98, 98, 286, 286), outline=accent, width=10)
    line(d, [(150, 124), (192, 186)], 22, accent)
    line(d, [(234, 124), (192, 186)], 22, accent)
    line(d, [(192, 186), (192, 272)], 22, accent)
    line(d, [(146, 206), (238, 206)], 18, accent)
    line(d, [(146, 240), (238, 240)], 18, accent)


def alarm(d, accent):
    for x in (104, 280):
        d.ellipse((x - 46, 66, x + 46, 158), fill=WHITE)
    line(d, [(128, 300), (106, 326)], 20)
    line(d, [(256, 300), (278, 326)], 20)
    d.ellipse((82, 100, 302, 320), fill=WHITE)
    line(d, [(192, 210), (192, 146)], 18, accent)
    line(d, [(192, 210), (238, 236)], 18, accent)
    d.ellipse((178, 196, 206, 224), fill=accent)


def note(d):
    def head(cx, cy):
        h = Image.new("L", (S, S), 0)
        ImageDraw.Draw(h).ellipse((cx - 46, cy - 32, cx + 46, cy + 32), fill=255)
        return h.rotate(20, center=(cx, cy), resample=Image.BICUBIC)

    layer = Image.new("L", (S, S), 0)
    for cx, cy in ((132, 276), (262, 246)):
        layer.paste(255, (0, 0), head(cx, cy))
    ld = ImageDraw.Draw(layer)
    ld.rectangle((160, 110, 182, 270), fill=255)
    ld.rectangle((290, 80, 312, 240), fill=255)
    ld.polygon([(160, 110), (312, 80), (312, 128), (160, 158)], fill=255)
    d.bitmap((0, 0), layer, fill=WHITE)


def calc(d):
    w = 24
    line(d, [(136, 106), (136, 166)], w)
    line(d, [(106, 136), (166, 136)], w)
    line(d, [(218, 136), (278, 136)], w)
    line(d, [(112, 226), (160, 274)], w)
    line(d, [(160, 226), (112, 274)], w)
    line(d, [(218, 232), (278, 232)], w)
    line(d, [(218, 268), (278, 268)], w)


def blocks(d):
    for x, y in ((92, 92), (208, 92), (92, 208)):
        d.rounded_rectangle((x, y, x + 84, y + 84), radius=20, fill=WHITE)
    cx, cy, r = 250, 250, 54
    d.polygon([(cx, cy - r), (cx + r, cy), (cx, cy + r), (cx - r, cy)], fill=WHITE)


def gear():
    m = Image.new("L", (S, S), 0)
    md = ImageDraw.Draw(m)
    cx = cy = S / 2
    md.ellipse((cx - 104, cy - 104, cx + 104, cy + 104), fill=255)
    for k in range(8):
        a = math.radians(k * 45)
        ux, uy = math.cos(a), math.sin(a)
        px, py = -uy, ux
        pts = []
        for along, side in ((80, -26), (136, -20), (136, 20), (80, 26)):
            pts.append((cx + ux * along + px * side, cy + uy * along + py * side))
        md.polygon(pts, fill=255)
    md.ellipse((cx - 44, cy - 44, cx + 44, cy + 44), fill=0)
    return m


def camera(d, accent):
    d.rounded_rectangle((126, 92, 236, 140), radius=16, fill=WHITE)
    d.rounded_rectangle((60, 118, 324, 304), radius=40, fill=WHITE)
    d.ellipse((126, 146, 258, 278), fill=accent)
    d.ellipse((156, 176, 228, 248), fill=WHITE)
    d.ellipse((270, 140, 296, 166), fill=accent)


def gallery(d, accent):
    d.rounded_rectangle((68, 84, 316, 300), radius=28, fill=WHITE)
    d.polygon([(92, 276), (166, 176), (212, 232), (246, 196), (292, 276)], fill=accent)
    d.ellipse((232, 116, 280, 164), fill=accent)


def weather(d, accent):
    d.ellipse((96, 84, 224, 212), fill=hexc("#ffd43b") + (255,))
    d.ellipse((120, 176, 236, 292), fill=WHITE)
    d.ellipse((190, 150, 310, 270), fill=WHITE)
    d.rounded_rectangle((96, 226, 320, 300), radius=37, fill=WHITE)


def store(d, accent):
    d.rounded_rectangle((104, 150, 312, 316), radius=22, fill=WHITE)
    d.arc((150, 92, 266, 208), 180, 360, fill=WHITE, width=18)
    d.ellipse((182, 214, 234, 266), fill=accent)


# The Nokia LCD: dark square pixels on yellow-green (apps/snake/lcd.go).
LCD_INK = (0x1E, 0x2C, 0x10, 255)


def snake(d, accent):
    for x, y in ((92, 252), (148, 252), (204, 252), (204, 196), (204, 140), (260, 140)):
        d.rectangle((x, y, x + 48, y + 48), fill=LCD_INK)
    d.rectangle((116, 128, 136, 148), fill=LCD_INK)
    d.rectangle((136, 108, 156, 128), fill=LCD_INK)
    d.rectangle((136, 148, 156, 168), fill=LCD_INK)
    d.rectangle((96, 108, 116, 128), fill=LCD_INK)
    d.rectangle((96, 148, 116, 168), fill=LCD_INK)


def notes(d, accent):
    d.rounded_rectangle((116, 92, 300, 324), radius=18, fill=WHITE)
    for y in (150, 196, 242):
        d.rounded_rectangle((146, y, 270, y + 14), radius=7, fill=accent)


def globe(d, accent):
    w = 18
    d.ellipse((76, 76, 308, 308), outline=WHITE, width=w)
    d.ellipse((140, 76, 244, 308), outline=WHITE, width=w)
    line(d, [(192, 80), (192, 304)], w)
    line(d, [(82, 192), (302, 192)], w)
    d.arc((96, 60, 288, 170), 25, 155, fill=WHITE, width=w)
    d.arc((96, 214, 288, 324), 205, 335, fill=WHITE, width=w)


def wrench(d, accent):
    line(d, [(118, 270), (224, 164)], 46)
    d.ellipse((96, 248, 140, 292), fill=WHITE)
    d.ellipse((190, 74, 310, 194), fill=WHITE)
    d.polygon([(250, 70), (318, 70), (318, 138)], fill=accent)
    d.ellipse((248, 102, 288, 142), fill=accent)


def four(d, accent):
    for x, y in ((92, 92), (208, 92), (92, 208), (208, 208)):
        d.rounded_rectangle((x, y, x + 84, y + 84), radius=22, fill=WHITE)
    d.ellipse((232, 232, 268, 268), fill=accent)


def cards(d, accent):
    # The card pack: a card behind, the front one with a stripe and the
    # contactless waves.
    d.rounded_rectangle((112, 80, 322, 212), radius=22, fill=(255, 255, 255, 140))
    d.rounded_rectangle((62, 146, 290, 304), radius=24, fill=WHITE)
    d.rectangle((62, 176, 290, 204), fill=accent)
    for r in (20, 38, 56):
        d.arc((104 - r, 256 - r, 104 + r, 256 + r), -45, 45, fill=accent, width=11)


def python_text(d):
    font = ImageFont.truetype(FONT, 168)
    x0, y0, x1, y1 = d.textbbox((0, 0), "Py", font=font)
    d.text(((S - (x1 - x0)) / 2 - x0, (S - (y1 - y0)) / 2 - y0 - 6), "Py", font=font, fill=WHITE)
    d.ellipse((268, 274, 300, 306), fill=hexc("#ffd43b") + (255,))


ICONS = {
    "dialer": ("#46d37a", "#1f9d4c", lambda d, a: phone(d)),
    "messages": ("#5cb2ff", "#1f6fd1", messages),
    "chat": ("#6f7dff", "#3445c8", goose),
    "wallet": ("#ffbe55", "#f08200", coin),
    "clock": ("#ff7b7b", "#e03131", alarm),
    "music": ("#ff7cc0", "#d6336c", lambda d, a: note(d)),
    "calc": ("#93a4b8", "#4a5a70", lambda d, a: calc(d)),
    "miniapp": ("#b387ff", "#7048e8", lambda d, a: blocks(d)),
    "settings": ("#b5bdc6", "#5f6b76", None),
    "hello-py": ("#5a9bd4", "#2f5f94", lambda d, a: python_text(d)),
    "camera": ("#6b7280", "#1f2937", camera),
    "gallery": ("#2dd4bf", "#0f766e", gallery),
    "weather": ("#60a5fa", "#2563eb", weather),
    "store": ("#fb7185", "#be123c", store),
    "browser": ("#38bdf8", "#0369a1", globe),
    "snake": ("#c5dd80", "#8fae3c", snake),
    "cards": ("#6b8cff", "#1e3a8a", cards),
    # Main menu folders (shell/system.go), installed as system icons.
    "icons/folder-tools": ("#94a3b8", "#475569", wrench),
    "icons/folder-apps": ("#a78bfa", "#6d28d9", four),
    # A sample third-party app (packaged, not in the system image).
    "examples/notes": ("#fcd34d", "#d97706", notes),
}


def main():
    only = set(sys.argv[1:])
    for app, (top, bottom, draw) in ICONS.items():
        if only and app not in only:
            continue
        img = base(top, bottom)
        if draw is None:
            img.paste(WHITE, (0, 0), gear())
        else:
            layer, d = glyph()
            draw(d, hexc(bottom) + (255,))
            img.alpha_composite(layer)
        if app.startswith("icons/"):
            path = os.path.join(HERE, app + ".png")
        elif "/" in app:
            path = os.path.join(HERE, *app.split("/"), "icon.png")
        else:
            path = os.path.join(HERE, "apps", app, "icon.png")
        os.makedirs(os.path.dirname(path), exist_ok=True)
        img.resize((OUT, OUT), Image.LANCZOS).save(path, optimize=True)
        print(path)


if __name__ == "__main__":
    main()
