// phone-gui keeps the chosen tile of the photo picker and the share sheet
// (an overlay of kind "picker", its `tiles`, `columns` and `choice`,
// 23-renderer-protocol.md §4) on screen as phone-paint does: whole rows from
// the first that keeps the chosen tile's row on screen, as a menu keeps its
// entry, with the body's scroll bar at the right edge; a grid that fits is
// drawn from its top as before, without the bar: run headless on frames as
// the Shell's picker sends them, each tile's picture a colour of its own.

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::process::{Command, Stdio};

use base64::Engine as _;
use serde_json::{Value, json};

// The dark theme's dim text, which the scroll bar is drawn in.
const DIM: [u8; 3] = [130, 132, 145];
// How far a tile's picture may be from where it belongs, in panel pixels:
// its edges are smoothed into the tile.
const OFF: f32 = 2.0;

struct Panel {
    name: &'static str,
    w: u32,
    h: u32,
    scale: f32,
    // The Shell's columns for the panel (shell/system.go menuColumns).
    cols: usize,
}

const PANELS: [Panel; 3] = [
    Panel { name: "bar-t9", w: 480, h: 640, scale: 2.0, cols: 3 },
    Panel { name: "bar-qwerty", w: 640, h: 480, scale: 2.0, cols: 4 },
    Panel { name: "communicator", w: 800, h: 352, scale: 1.6, cols: 5 },
];

impl Panel {
    /// Where tile i's picture belongs with the grid's row first at its
    /// top, in panel pixels (x0, y0, x1, y1, its edges): phone.slint's
    /// PickerOverlay below the status bar (18) and its title (24), tiles
    /// (width - 12) / cols apart across and 16 more down, 6 in from the
    /// grid's edge, the picture 4 in from the tile's and 4 short of its
    /// width.
    fn place(&self, i: usize, first: usize) -> (f32, f32, f32, f32) {
        let cw = (self.w as f32 / self.scale - 12.0) / self.cols as f32;
        let x = 6.0 + (i % self.cols) as f32 * cw + 4.0;
        let y = 18.0 + 24.0 + 6.0 + (i / self.cols - first) as f32 * (cw + 16.0) + 4.0;
        let side = cw - 12.0;
        let s = self.scale;
        (x * s, y * s, (x + side) * s, (y + side) * s)
    }
}

struct Snap {
    w: usize,
    h: usize,
    rgb: Vec<u8>,
}

/// Where a tile is drawn: the box (x0, y0, x1, y1, inclusive) around the
/// pixels of its colour and how many there are; none if there are none.
type Drawn = Option<((usize, usize, usize, usize), usize)>;

impl Snap {
    fn at(&self, x: usize, y: usize) -> [u8; 3] {
        let i = (y * self.w + x) * 3;
        [self.rgb[i], self.rgb[i + 1], self.rgb[i + 2]]
    }

    /// Where each of n tiles is drawn.
    fn tiles(&self, n: usize) -> Vec<Drawn> {
        let of: HashMap<[u8; 3], usize> = (0..n).map(|i| (colour(i), i)).collect();
        let mut out: Vec<Drawn> = vec![None; n];
        for y in 0..self.h {
            for x in 0..self.w {
                let Some(&i) = of.get(&self.at(x, y)) else { continue };
                out[i] = Some(match out[i] {
                    None => ((x, y, x, y), 1),
                    Some(((x0, y0, x1, y1), c)) => ((x0.min(x), y0.min(y), x1.max(x), y1.max(y)), c + 1),
                });
            }
        }
        out
    }

    /// The scroll bar's run of pixels (top, bottom, inclusive) down the
    /// middle of the panel's last logical pixels but one, below the
    /// picker's title.
    fn bar(&self, p: &Panel) -> Option<(usize, usize)> {
        let x = ((p.w as f32 / p.scale - 2.0) * p.scale) as usize;
        let from = ((18.0 + 24.0) * p.scale) as usize;
        let ys: Vec<usize> = (from..self.h).filter(|&y| self.at(x, y) == DIM).collect();
        Some((*ys.first()?, *ys.last()?))
    }
}

/// Tile i's colour: no blue, which none of the theme's colours lacks, so
/// no other pixel, smoothed or not, has it.
fn colour(i: usize) -> [u8; 3] {
    [255 - 3 * i as u8, 40 + 5 * i as u8, 0]
}

/// The picker the Shell sends for photos: n tiles, each picture a colour
/// of its own, choice the chosen one.
fn picker(n: usize, cols: usize, choice: usize) -> Value {
    let tiles: Vec<Value> = (0..n)
        .map(|i| {
            let rgb = colour(i).repeat(4);
            json!({"icon": "P", "title": format!("Photo {i}"), "focused": i == choice,
                "pixels": {"w": 2, "h": 2, "rgb": base64::engine::general_purpose::STANDARD.encode(rgb)}})
        })
        .collect();
    json!({"t": "frame", "screen": "chat/thread", "title": "Chat", "body": [{"kind": "input", "placeholder": "Message"}],
        "focus": 0, "soft": ["", "", ""], "status": {"time": "10:00", "net": "4G", "battery": 80},
        "overlay": {"kind": "picker", "title": "Choose a photo · System album", "phase": -1, "choice": choice, "columns": cols,
            "lines": ["Only the chosen one goes to “Chat”"], "footer": ["OK choose · 1–9 pick · Back cancel"], "tiles": tiles}})
}

/// phone-gui headless on panel sent frames, each snapped after it.
fn draw(name: &str, panel: &Panel, frames: &[Value]) -> Vec<Snap> {
    let dir = std::env::temp_dir().join(format!("phone-gui-picker-{}-{name}", std::process::id()));
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
    let device = json!({"id": panel.name, "keypad": "t9", "width": panel.w, "height": panel.h, "scale": panel.scale});
    writeln!(to, "{}", json!({"t": "hello", "proto": "phone.frame/1", "device": device})).unwrap();
    for (i, f) in frames.iter().enumerate() {
        writeln!(to, "{f}").unwrap();
        writeln!(to, "{}", json!({"t": "snap", "name": format!("{name}-{i}")})).unwrap();
    }
    to.flush().unwrap();
    let mut snapped = 0;
    while snapped < frames.len() {
        let line = from.next().expect("phone-gui ended before the snapshots").unwrap();
        let m: Value = serde_json::from_str(&line).unwrap();
        if m["t"] == "snapped" {
            assert_eq!(m["err"], "", "{name}: snapshot {snapped}");
            snapped += 1;
        }
    }
    writeln!(to, r#"{{"t": "quit"}}"#).unwrap();
    drop(to);
    gui.wait().unwrap();
    let snaps = (0..frames.len())
        .map(|i| {
            let file = dir.join(format!("{name}-{i}.png"));
            let mut dec = png::Decoder::new(BufReader::new(std::fs::File::open(&file).unwrap()));
            dec.set_transformations(png::Transformations::normalize_to_color8());
            let mut reader = dec.read_info().unwrap();
            let mut rgb = vec![0; reader.output_buffer_size().unwrap()];
            let info = reader.next_frame(&mut rgb).unwrap();
            assert_eq!(info.color_type, png::ColorType::Rgb);
            assert_eq!((info.width, info.height), (panel.w, panel.h), "{name}: the snapshot's size");
            rgb.truncate(info.buffer_size());
            Snap { w: info.width as usize, h: info.height as usize, rgb }
        })
        .collect();
    let _ = std::fs::remove_dir_all(&dir);
    snaps
}

/// Checks that tile i is drawn whole where it belongs with row first at
/// the grid's top.
fn check_tile(what: &str, panel: &Panel, drawn: Drawn, i: usize, first: usize) {
    let ((x0, y0, x1, y1), n) = drawn.unwrap_or_else(|| panic!("{what}: tile {i} is not drawn"));
    let (px0, py0, px1, py1) = panel.place(i, first);
    let near = |got: usize, want: f32| (got as f32 - want).abs() <= OFF;
    assert!(
        near(x0, px0) && near(y0, py0) && near(x1 + 1, px1) && near(y1 + 1, py1),
        "{what}: tile {i} drawn at {x0},{y0}-{},{}, not whole at {px0:.1},{py0:.1}-{px1:.1},{py1:.1} (row {first} first)",
        x1 + 1,
        y1 + 1
    );
    assert_eq!(n, (x1 - x0 + 1) * (y1 - y0 + 1), "{what}: tile {i} drawn with a hole in it");
}

#[test]
fn a_picker_keeps_its_choice_on_screen() {
    const N: usize = 40;
    for p in &PANELS {
        let frames: Vec<Value> = (0..N).map(|k| picker(N, p.cols, k)).collect();
        let snaps = draw(&format!("{}-40", p.name), p, &frames);
        let drawn: Vec<_> = snaps.iter().map(|s| s.tiles(N)).collect();
        for (k, d) in drawn.iter().enumerate() {
            let what = format!("{}, tile {k} chosen", p.name);
            let ((_, y0, _, y1), _) = d[k].unwrap_or_else(|| panic!("{what}: it is not drawn"));
            let top = ((18.0 + 24.0) * p.scale) as usize;
            assert!(y0 >= top && y1 < p.h as usize, "{what}: drawn at {y0}..{y1}, off the grid");
        }
        // Rows from the first that keeps the chosen one's on screen: as
        // many as are drawn with the first tile chosen.
        let rows = N.div_ceil(p.cols);
        let fit = drawn[0].iter().position(Option::is_none).unwrap_or(N).div_ceil(p.cols);
        assert!(fit >= 1 && fit < rows, "{}: {fit} rows of {rows} drawn", p.name);
        let mut bars = Vec::new();
        for (k, d) in drawn.iter().enumerate() {
            let what = format!("{}, tile {k} chosen", p.name);
            let first = (k / p.cols + 1).saturating_sub(fit);
            let shown = first * p.cols..(first + fit) * p.cols;
            for (i, t) in d.iter().enumerate() {
                if shown.contains(&i) {
                    check_tile(&what, p, *t, i, first);
                } else {
                    assert!(t.is_none(), "{what}: tile {i} drawn, outside rows {first}..{}", first + fit);
                }
            }
            let bar = snaps[k].bar(p).unwrap_or_else(|| panic!("{what}: no scroll bar for {rows} rows"));
            bars.push((first, bar));
        }
        // The bar at the top, moving down a step a row, as long all the
        // way.
        let (_, (t0, b0)) = bars[0];
        assert!(((t0 as f32) - (18.0 + 24.0) * p.scale).abs() <= OFF + p.scale, "{}: the bar starts at {t0}", p.name);
        for w in bars.windows(2) {
            let ((f0, (ta, _)), (f1, (tb, bb))) = (w[0], w[1]);
            assert!((bb - tb).abs_diff(b0 - t0) <= 1, "{}: the bar {} pixels long for row {f1}, {} for row 0", p.name, bb - tb, b0 - t0);
            assert!(if f1 > f0 { tb > ta } else { tb == ta }, "{}: the bar at {tb} for row {f1}, at {ta} for row {f0}", p.name);
        }

        // A grid that fits is drawn from its top, the tiles where they
        // were, without the bar.
        let all = p.cols * fit;
        let frames: Vec<Value> = (0..all).map(|k| picker(all, p.cols, k)).collect();
        for (k, s) in draw(&format!("{}-fits", p.name), p, &frames).iter().enumerate() {
            let what = format!("{}, {all} tiles, tile {k} chosen", p.name);
            for (i, t) in s.tiles(all).into_iter().enumerate() {
                check_tile(&what, p, t, i, 0);
            }
            assert_eq!(s.bar(p), None, "{what}: a scroll bar on a grid that fits");
        }
    }
}
