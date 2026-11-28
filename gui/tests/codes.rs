// phone-gui draws a system-drawn code (an image with `code`,
// 23-renderer-protocol.md §4) at a whole number of panel pixels a module,
// quiet zones and all, centred and as large as the image's box allows (the
// row less ImageView's 6 px padding across, at most 72% of the body down),
// or the Shell's note and number where one pixel a module does not fit: run
// headless on the frames the Shell sends.

use std::io::{BufRead, BufReader, Write};
use std::process::{Command, Stdio};

use base64::Engine as _;
use serde_json::{Value, json};

const BLACK: [u8; 3] = [0, 0, 0];
const WHITE: [u8; 3] = [255, 255, 255];
// The dark theme's in phone.slint: cards, text, dim text, and the secure
// mask's ground.
const CARD: [u8; 3] = [27, 29, 39];
const FG: [u8; 3] = [222, 222, 228];
const DIM: [u8; 3] = [130, 132, 145];
const MASK: [u8; 3] = [42, 42, 51];
// How far a text's antialiased pixels may be from its colour and count.
const TEXT: u8 = 6;

const QR_QUIET: usize = 4;
const BAR_QUIET: usize = 10;

struct Panel {
    w: u32,
    h: u32,
    scale: f32,
}

const BAR_T9: Panel = Panel { w: 480, h: 640, scale: 2.0 };
const BAR_QWERTY: Panel = Panel { w: 640, h: 480, scale: 2.0 };
const COMMUNICATOR: Panel = Panel { w: 800, h: 352, scale: 1.6 };
const SMALL: Panel = Panel { w: 240, h: 320, scale: 1.0 };

struct Snap {
    w: usize,
    h: usize,
    rgb: Vec<u8>,
    // The body's height, logical, as the layout report says.
    area: f32,
}

impl Snap {
    fn at(&self, x: usize, y: usize) -> [u8; 3] {
        let i = (y * self.w + x) * 3;
        [self.rgb[i], self.rgb[i + 1], self.rgb[i + 2]]
    }

    /// The smallest box (x0, y0, x1, y1, inclusive) around the pixels
    /// inside within that are of colour c.
    fn bounds(&self, c: [u8; 3], within: (usize, usize, usize, usize)) -> Option<(usize, usize, usize, usize)> {
        let mut b: Option<(usize, usize, usize, usize)> = None;
        for y in within.1..=within.3 {
            for x in within.0..=within.2 {
                if self.at(x, y) == c {
                    b = Some(match b {
                        None => (x, y, x, y),
                        Some((x0, y0, x1, y1)) => (x0.min(x), y0.min(y), x1.max(x), y1.max(y)),
                    });
                }
            }
        }
        b
    }

    fn all(&self) -> (usize, usize, usize, usize) {
        (0, 0, self.w - 1, self.h - 1)
    }

    /// How many pixels inside within are within off levels of c in every
    /// channel.
    fn count(&self, c: [u8; 3], off: u8, within: (usize, usize, usize, usize)) -> usize {
        let near = |p: [u8; 3]| p.iter().zip(c).all(|(a, b)| a.abs_diff(b) <= off);
        (within.1..=within.3).flat_map(|y| (within.0..=within.2).map(move |x| (x, y))).filter(|&(x, y)| near(self.at(x, y))).count()
    }
}

/// One frame with body drawn by phone-gui headless on panel, snapped.
fn draw(name: &str, panel: &Panel, body: Value) -> Snap {
    let dir = std::env::temp_dir().join(format!("phone-gui-codes-{}-{name}", std::process::id()));
    let mut gui = Command::new(env!("CARGO_BIN_EXE_phone-gui"))
        .arg("--headless")
        .arg("--snap-dir")
        .arg(&dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .expect("phone-gui");
    let mut to = gui.stdin.take().unwrap();
    let mut from = BufReader::new(gui.stdout.take().unwrap()).lines();
    let device = json!({"id": "test", "keypad": "t9", "width": panel.w, "height": panel.h, "scale": panel.scale});
    for m in [
        json!({"t": "hello", "proto": "phone.frame/1", "device": device}),
        json!({"t": "frame", "screen": name, "title": "Code", "body": body, "soft": ["", "Back", ""]}),
        json!({"t": "snap", "name": name}),
    ] {
        writeln!(to, "{m}").unwrap();
    }
    to.flush().unwrap();
    let mut area = 0.0;
    loop {
        let line = from.next().expect("phone-gui ended before the snapshot").unwrap();
        let m: Value = serde_json::from_str(&line).unwrap();
        match m["t"].as_str() {
            Some("layout") => area = m["layout"]["area"].as_f64().unwrap() as f32,
            Some("snapped") => {
                assert_eq!(m["err"], "", "{name}: the snapshot");
                break;
            }
            _ => {}
        }
    }
    writeln!(to, r#"{{"t": "quit"}}"#).unwrap();
    drop(to);
    gui.wait().unwrap();
    let file = dir.join(format!("{name}.png"));
    let mut dec = png::Decoder::new(BufReader::new(std::fs::File::open(&file).unwrap()));
    dec.set_transformations(png::Transformations::normalize_to_color8());
    let mut reader = dec.read_info().unwrap();
    let mut rgb = vec![0; reader.output_buffer_size().unwrap()];
    let info = reader.next_frame(&mut rgb).unwrap();
    assert_eq!(info.color_type, png::ColorType::Rgb);
    rgb.truncate(info.buffer_size());
    let _ = std::fs::remove_dir_all(&dir);
    assert_eq!((info.width, info.height), (panel.w, panel.h), "{name}: the snapshot's size");
    Snap { w: info.width as usize, h: info.height as usize, rgb, area }
}

/// Modules made up from seed (true: dark), the first and last dark.
fn modules(n: usize, seed: u32) -> Vec<bool> {
    let mut x = seed;
    (0..n)
        .map(|i| {
            x = x.wrapping_mul(1_103_515_245).wrapping_add(12345);
            i == 0 || i == n - 1 || x >> 16 & 1 == 1
        })
        .collect()
}

/// A QR code's modules (n by n, each row's first and last dark), quiet
/// zones included.
fn qr(n: usize) -> Vec<Vec<bool>> {
    let side = n + 2 * QR_QUIET;
    (0..side)
        .map(|y| {
            let mut row = vec![false; side];
            if (QR_QUIET..QR_QUIET + n).contains(&y) {
                row[QR_QUIET..QR_QUIET + n].copy_from_slice(&modules(n, y as u32));
            }
            row
        })
        .collect()
}

/// A barcode's row of modules (n, the first and last bars), quiet zones
/// included.
fn bar(n: usize) -> Vec<bool> {
    let mut row = vec![false; BAR_QUIET];
    row.extend(modules(n, 7));
    row.extend(vec![false; BAR_QUIET]);
    row
}

/// The image node the Shell sends for a code: one pixel a module, a
/// barcode a quarter of its width high (16 rows at least).
fn code_node(kind: &str, rows: &[Vec<bool>], secure: bool, number: &str) -> Value {
    let (w, h) = (rows[0].len(), if kind == "bar" { (rows[0].len() / 4).max(16) } else { rows.len() });
    let mut rgb = Vec::with_capacity(w * h * 3);
    for y in 0..h {
        for &dark in &rows[y.min(rows.len() - 1)] {
            rgb.extend_from_slice(if dark { &BLACK } else { &WHITE });
        }
    }
    let note = if kind == "bar" { "The barcode does not fit:" } else { "The QR code does not fit the screen" };
    json!({"kind": "image", "image": {"w": w, "h": h, "rgb": base64::engine::general_purpose::STANDARD.encode(&rgb),
        "secure": secure, "pixelated": true, "code": kind, "code_note": note, "code_text": number}})
}

fn hint() -> Value {
    json!({"kind": "text", "text": "Refreshes every 30 seconds"})
}

/// Checks the code drawn in snap: rows (a barcode's one row for every
/// pixel row) at a whole number of pixels a module, every pixel of the
/// code, quiet zones included, the module's; centred across, the most
/// pixels a module that fit the image's box. Returns them.
fn check_code(what: &str, s: &Snap, panel: &Panel, rows: &[Vec<bool>], quiet: usize, barcode: bool) -> usize {
    let (x0, y0, x1, y1) = s.bounds(BLACK, s.all()).unwrap_or_else(|| panic!("{what}: no code drawn"));
    let (iw, dark_w) = (rows[0].len(), rows[0].len() - 2 * quiet);
    let k = (x1 - x0 + 1) / dark_w;
    assert!(k >= 1 && x1 - x0 + 1 == k * dark_w, "{what}: {} pixels across for {dark_w} modules", x1 - x0 + 1);
    let (ox, cw) = (x0 as isize - (quiet * k) as isize, iw * k);
    // A barcode's bars run its whole height.
    let (oy, ch) = if barcode { (y0 as isize, y1 - y0 + 1) } else { (y0 as isize - (quiet * k) as isize, rows.len() * k) };
    assert!(ox >= 0 && oy >= 0 && ox as usize + cw <= s.w && oy as usize + ch <= s.h, "{what}: its quiet zones are off the screen");
    let (ox, oy) = (ox as usize, oy as usize);
    for y in 0..ch {
        let row = &rows[if barcode { 0 } else { y / k }];
        for x in 0..cw {
            let want = if row[x / k] { BLACK } else { WHITE };
            let got = s.at(ox + x, oy + y);
            assert_eq!(got, want, "{what} at {k} pixels a module: pixel {x},{y} of the code (module {},{})", x / k, y / k);
        }
    }
    let (left, right) = (ox, s.w - ox - cw);
    assert!(left.abs_diff(right) <= 1, "{what}: {left} pixels left of it, {right} right");
    let room_w = ((panel.w as f32 / panel.scale - 12.0) * panel.scale).floor() as usize;
    let room_h = ((s.area * 0.72).max(60.0) * panel.scale).floor() as usize;
    assert!(cw <= room_w && ch <= room_h, "{what}: {cw}x{ch} pixels, over its box of {room_w}x{room_h}");
    if barcode {
        let ih = (iw / 4).max(16);
        assert!(ch >= ih && ch <= ih * k, "{what}: {ch} pixels high, for {ih} rows at {k} pixels");
        assert!((k + 1) * iw > room_w, "{what}: {k} pixels a module, where {} fit", k + 1);
    } else {
        assert!((k + 1) * iw > room_w || (k + 1) * rows.len() > room_h, "{what}: {k} pixels a module, where {} fit", k + 1);
    }
    k
}

#[test]
fn codes_are_drawn_at_whole_pixels_a_module() {
    // The payment barcode (18 digits: 134 modules and the quiet zones) and
    // a 100-character QR code (29 modules).
    let (pay, code) = (vec![bar(134)], qr(29));
    for (name, panel, k_bar) in [("t9", &BAR_T9, 2), ("qwerty", &BAR_QWERTY, 4), ("communicator", &COMMUNICATOR, 5), ("small", &SMALL, 1)] {
        let s = draw(&format!("bar-{name}"), panel, json!([code_node("bar", &pay, false, ""), hint()]));
        let k = check_code(&format!("the barcode on {name}"), &s, panel, &pay, BAR_QUIET, true);
        assert_eq!(k, k_bar, "the barcode on {name}");
        let s = draw(&format!("qr-{name}"), panel, json!([code_node("qr", &code, false, ""), hint()]));
        check_code(&format!("the QR code on {name}"), &s, panel, &code, QR_QUIET, false);
    }
}

#[test]
fn a_code_too_big_shows_the_note() {
    // 270 modules across, 228 pixels of room.
    let wide = vec![bar(250)];
    for secure in [false, true] {
        let what = if secure { "the secure barcode" } else { "the barcode" };
        let s = draw(&format!("too-wide-{secure}"), &SMALL, json!([code_node("bar", &wide, secure, "2812 3456 7890 1234 56"), hint()]));
        assert!(s.bounds(BLACK, s.all()).is_none() && s.bounds(WHITE, s.all()).is_none(), "{what}: bars drawn");
        let card = s.bounds(CARD, s.all()).unwrap_or_else(|| panic!("{what}: no note"));
        assert!(s.count(DIM, TEXT, card) > 20, "{what}: the note is not written");
        let (number, masked) = (s.count(FG, TEXT, card), s.count(MASK, 0, card));
        if secure {
            assert!(number == 0 && masked > 200, "{what}: the number not masked ({number} pixels of it, {masked} of the mask)");
        } else {
            assert!(number > 20 && masked == 0, "{what}: the number not written ({number} pixels)");
        }
    }
    // 209 modules down, about 180 pixels of room: the note alone.
    let tall = qr(201);
    let s = draw("too-tall", &SMALL, json!([code_node("qr", &tall, false, ""), hint()]));
    assert!(s.bounds(BLACK, s.all()).is_none(), "the QR code: modules drawn");
    let card = s.bounds(CARD, s.all()).expect("the QR code: no note");
    assert!(s.count(DIM, TEXT, card) > 20 && s.count(FG, TEXT, card) == 0, "the QR code's note");
}

#[test]
fn a_secure_code_is_masked_in_snapshots() {
    let code = qr(29);
    let s = draw("secure", &BAR_T9, json!([code_node("qr", &code, true, ""), hint()]));
    assert!(s.bounds(BLACK, s.all()).is_none(), "modules drawn");
    let (x0, y0, x1, y1) = s.bounds(MASK, s.all()).expect("no mask");
    assert!(x1 - x0 + 1 == y1 - y0 + 1 && (x1 - x0 + 1) % code.len() == 0, "the mask is not the code's box: {x0},{y0}-{x1},{y1}");
}
