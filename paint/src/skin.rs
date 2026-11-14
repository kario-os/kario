//! The device skin for design reviews (R21): a snapshot inside a phone body
//! with a modern keypad under it, 9-key or QWERTY as the device profile
//! says. Written next to the plain snapshot with --skin; never on a panel.

use tiny_skia::Pixmap;

use crate::canvas::{Canvas, Face, Fonts, Rgb};
use crate::i18n::t;

const BODY: Rgb = Rgb(0x1c, 0x1e, 0x24);
const EDGE: Rgb = Rgb(0x2e, 0x31, 0x3a);
const KEY: Rgb = Rgb(0x2a, 0x2d, 0x36);
const KEY_TOP: Rgb = Rgb(0x34, 0x38, 0x43);
const LEGEND: Rgb = Rgb(0xee, 0xee, 0xf2);
const SUB: Rgb = Rgb(0x8e, 0x93, 0xa1);
const ACCENT: Rgb = Rgb(0x4c, 0xa3, 0xff);
const CALL: Rgb = Rgb(0x3f, 0xb6, 0x6b);
const END: Rgb = Rgb(0xe0, 0x4f, 0x4f);

/// The screen (in logical pixels w × h at scale s) set into a body.
pub fn skinned(screen: &Pixmap, keypad: &str, s: f32, fonts: &'static Fonts) -> Canvas {
    let (sw, sh) = (screen.width() as f32 / s, screen.height() as f32 / s);
    let m = 14.0; // body margin around the screen
    let pad = if keypad == "t9" { sw * 1.02 } else { sw * 0.78 };
    let (w, h) = (sw + 2.0 * m, sh + 2.0 * m + 12.0 + pad);
    let mut cv = Canvas::new((w * s).ceil() as u32, (h * s).ceil() as u32, s, fonts);
    cv.clear(Rgb(0xf4, 0xf5, 0xf7));
    cv.round(0.0, 0.0, w, h, 28.0, EDGE);
    cv.round(1.5, 1.5, w - 3.0, h - 3.0, 26.5, BODY);
    cv.round(m - 3.0, m - 3.0, sw + 6.0, sh + 6.0, 6.0, Rgb(0x05, 0x05, 0x07));
    cv.blit(screen, m, m, sw, sh, false);
    let top = m + sh + 12.0;
    nav(&mut cv, m, top, sw);
    if keypad == "t9" {
        nine_keys(&mut cv, m, top + sw * 0.36, sw, pad - sw * 0.36 - m);
    } else {
        qwerty(&mut cv, m, top + sw * 0.30, sw, pad - sw * 0.30 - m);
    }
    cv
}

fn key(cv: &mut Canvas, x: f32, y: f32, w: f32, h: f32, r: f32, c: Rgb) {
    cv.round(x, y + 1.0, w, h, r, Rgb(0x10, 0x11, 0x15));
    cv.round(x, y, w, h, r, c);
    cv.round(x + 1.0, y + 1.0, w - 2.0, h * 0.5, r, c.mix(KEY_TOP, 0.5));
}

fn centered(cv: &mut Canvas, face: Face, size: f32, cx: f32, y: f32, text: &str, c: Rgb) {
    let tw = cv.measure(face, size, text);
    cv.text(face, size, cx - tw / 2.0, y, text, c);
}

/// Soft keys, the call and end keys, and the D-pad with OK in the middle.
fn nav(cv: &mut Canvas, x: f32, y: f32, w: f32) {
    let r = w * 0.15;
    let (cx, cy) = (x + w / 2.0, y + r + 2.0);
    cv.circle(cx, cy, r + 1.5, Rgb(0x10, 0x11, 0x15));
    cv.circle(cx, cy, r, KEY);
    cv.circle(cx, cy, r * 0.48, KEY_TOP);
    cv.circle(cx, cy, r * 0.44, ACCENT);
    centered(cv, Face::Bold, r * 0.34, cx, cy - r * 0.24, "OK", Rgb(0xff, 0xff, 0xff));
    for (dx, dy) in [(0.0, -1.0), (0.0, 1.0), (-1.0, 0.0), (1.0, 0.0)] {
        cv.circle(cx + dx * r * 0.74, cy + dy * r * 0.74, 1.6, SUB);
    }
    let kw = (w / 2.0 - r - 10.0).max(20.0);
    for (i, label) in [t("Options"), t("Back")].iter().enumerate() {
        let kx = if i == 0 { x } else { x + w - kw };
        key(cv, kx, y + 2.0, kw, r * 0.62, 7.0, KEY);
        centered(cv, Face::Sans, 9.0, kx + kw / 2.0, y + 2.0 + r * 0.31 - 7.0, label, SUB);
    }
    for (i, c) in [CALL, END].iter().enumerate() {
        let kx = if i == 0 { x } else { x + w - kw };
        key(cv, kx, y + r * 1.1, kw, r * 0.62, 7.0, KEY);
        cv.round(kx + kw / 2.0 - 9.0, y + r * 1.1 + r * 0.31 - 2.5, 18.0, 5.0, 2.5, *c);
    }
}

fn nine_keys(cv: &mut Canvas, x: f32, y: f32, w: f32, h: f32) {
    let keys = [
        ("1", "∞"), ("2", "abc"), ("3", "def"),
        ("4", "ghi"), ("5", "jkl"), ("6", "mno"),
        ("7", "pqrs"), ("8", "tuv"), ("9", "wxyz"),
        ("*", t("Symbols")), ("0", t("Space")), ("#", t("Zh/En")),
    ];
    let (gap, cols, rows) = (6.0, 3.0, 4.0);
    let (kw, kh) = ((w - gap * (cols - 1.0)) / cols, (h - gap * (rows - 1.0)) / rows);
    for (i, (d, sub)) in keys.iter().enumerate() {
        let (c, r) = ((i % 3) as f32, (i / 3) as f32);
        let (kx, ky) = (x + c * (kw + gap), y + r * (kh + gap));
        key(cv, kx, ky, kw, kh, kh * 0.32, KEY);
        let size = (kh * 0.42).min(18.0);
        cv.text(Face::Bold, size, kx + kw * 0.16, ky + (kh - size * 1.45) / 2.0, d, LEGEND);
        let ss = (kh * 0.24).min(9.5);
        let sw = cv.measure(Face::Sans, ss, sub);
        cv.text(Face::Sans, ss, kx + kw - sw - kw * 0.14, ky + (kh - ss * 1.45) / 2.0, sub, SUB);
    }
}

fn qwerty(cv: &mut Canvas, x: f32, y: f32, w: f32, h: f32) {
    let rows: [&str; 3] = ["QWERTYUIOP", "ASDFGHJKL", "ZXCVBNM"];
    let gap = 3.0;
    let kh = (h - gap * 3.0) / 4.0;
    let kw = (w - gap * 9.0) / 10.0;
    for (r, row) in rows.iter().enumerate() {
        let n = row.chars().count() as f32;
        let x0 = x + (w - n * kw - (n - 1.0) * gap) / 2.0;
        for (i, ch) in row.chars().enumerate() {
            let kx = x0 + i as f32 * (kw + gap);
            let ky = y + r as f32 * (kh + gap);
            key(cv, kx, ky, kw, kh, 4.0, KEY);
            let size = (kh * 0.42).min(12.0);
            centered(cv, Face::Bold, size, kx + kw / 2.0, ky + (kh - size * 1.45) / 2.0, &ch.to_string(), LEGEND);
        }
    }
    let ky = y + 3.0 * (kh + gap);
    let wide = [(t("Symbols"), 1.6), (t("Zh/En"), 1.6), (t("Space"), 4.6), ("⌫", 1.6)];
    let total: f32 = wide.iter().map(|(_, u)| u).sum();
    let unit = (w - gap * (wide.len() as f32 - 1.0)) / total;
    let mut kx = x;
    for (label, u) in wide {
        let kw = unit * u;
        key(cv, kx, ky, kw, kh, 4.0, KEY);
        centered(cv, Face::Sans, (kh * 0.36).min(10.0), kx + kw / 2.0, ky + (kh - (kh * 0.36).min(10.0) * 1.45) / 2.0, label, SUB);
        kx += kw + gap;
    }
}
