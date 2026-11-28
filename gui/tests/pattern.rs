// phone-gui draws factory mode's test pattern (the frame's `pattern`,
// feature "pattern", 23-renderer-protocol.md §4) as phone-paint does: its
// colour on the whole panel, the status bar too, nothing else of the frame;
// a grid's lines one panel pixel wide on whole pixels, the cells done
// filled: run headless on frames as the Shell's factory screen sends them.

use std::io::{BufRead, BufReader, Write};
use std::process::{Command, Stdio};

use serde_json::{Value, json};

const BLACK: [u8; 3] = [0, 0, 0];
const WHITE: [u8; 3] = [255, 255, 255];
const RED: [u8; 3] = [255, 0, 0];
const GREEN: [u8; 3] = [0, 255, 0];

struct Panel {
    w: u32,
    h: u32,
    scale: f32,
}

// phone-paint's pattern_fills_the_panel's: one pixel a unit, a tall phone
// and the communicator.
const PANELS: [Panel; 3] = [Panel { w: 240, h: 320, scale: 1.0 }, Panel { w: 480, h: 960, scale: 2.0 }, Panel { w: 800, h: 352, scale: 1.6 }];

struct Snap {
    w: usize,
    h: usize,
    rgb: Vec<u8>,
}

impl Snap {
    fn at(&self, x: usize, y: usize) -> [u8; 3] {
        let i = (y * self.w + x) * 3;
        [self.rgb[i], self.rgb[i + 1], self.rgb[i + 2]]
    }

    fn count(&self, c: [u8; 3]) -> usize {
        self.rgb.chunks(3).filter(|p| *p == c).count()
    }
}

/// phone-gui headless on panel sent msgs, each snapped after it: what it
/// declared ready with, and the snapshots.
fn draw(name: &str, panel: &Panel, msgs: &[Value]) -> (Value, Vec<Snap>) {
    let dir = std::env::temp_dir().join(format!("phone-gui-pattern-{}-{name}", std::process::id()));
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
    writeln!(to, "{}", json!({"t": "hello", "proto": "phone.frame/1", "device": device})).unwrap();
    for (i, m) in msgs.iter().enumerate() {
        writeln!(to, "{m}").unwrap();
        writeln!(to, "{}", json!({"t": "snap", "name": format!("{name}-{i}")})).unwrap();
    }
    to.flush().unwrap();
    let (mut ready, mut snapped) = (Value::Null, 0);
    while snapped < msgs.len() {
        let line = from.next().expect("phone-gui ended before the snapshots").unwrap();
        let m: Value = serde_json::from_str(&line).unwrap();
        match m["t"].as_str() {
            Some("ready") => ready = m,
            Some("snapped") => {
                assert_eq!(m["err"], "", "{name}: snapshot {snapped}");
                snapped += 1;
            }
            _ => {}
        }
    }
    writeln!(to, r#"{{"t": "quit"}}"#).unwrap();
    drop(to);
    gui.wait().unwrap();
    let snaps = (0..msgs.len())
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
    (ready, snaps)
}

/// The factory screen's frame with pattern over it: the status bar, the
/// home screen's menu and a toast it hides.
fn factory(pattern: Value) -> Value {
    json!({"t": "frame", "screen": "sys/factory", "title": "Factory test", "body": [], "focus": -1, "soft": ["", "", ""],
        "status": {"time": "10:00", "mid": "■ Factory mode", "mid_kind": "factory", "net": "4G", "battery": 80},
        "overlay": {"kind": "menu", "title": "Factory test", "phase": -1, "choice": -1,
            "items": [{"header": true, "text": "SKU qemu-t9"}, {"text": "· Keys", "sel": true}, {"text": "· Display"}]},
        "toast": "Saved", "pattern": pattern})
}

#[test]
fn the_renderer_declares_pattern() {
    let (ready, _) = draw("ready", &PANELS[0], &[factory(json!({"color": "#ff0000"}))]);
    let features = ready["features"].as_array().expect("no features");
    assert!(features.iter().any(|f| f == "pattern"), "features {features:?}");
}

#[test]
fn a_pattern_fills_the_panel() {
    for p in &PANELS {
        let what = format!("{}x{}", p.w, p.h);
        let (_, s) = draw(&format!("solid-{what}"), p, &[factory(json!({"color": "#ff0000"})), json!({"t": "patch", "patch": {"pattern": null}})]);
        assert_eq!(s[0].count(RED), s[0].w * s[0].h, "{what}: not all of the panel red");
        assert_eq!(s[1].count(RED), 0, "{what}: the pattern stays once the patch takes it away");
    }
}

#[test]
fn a_grid_is_drawn_on_whole_pixels() {
    for p in &PANELS {
        let what = format!("{}x{}", p.w, p.h);
        let grid = json!({"color": "#000000", "grid": [4, 6], "line": "#ffffff", "done": [0, 23], "mark": "#00ff00"});
        let (_, s) = draw(&format!("grid-{what}"), p, &[factory(grid)]);
        let s = &s[0];
        let (pw, ph) = (s.w, s.h);
        let (cw, ch) = (pw / 4, ph / 6);
        assert_eq!(s.at(cw / 2, ch / 2), GREEN, "{what}: the first cell done");
        assert_eq!(s.at(pw - cw / 2, ph - ch / 2), GREEN, "{what}: the last cell done");
        assert_eq!(s.at(cw + cw / 2, ch / 2), BLACK, "{what}: a cell not done");
        assert_eq!(s.at(0, ch / 2), WHITE, "{what}: the left edge");
        assert_eq!(s.at(pw - 1, ch / 2), WHITE, "{what}: the right edge");
        assert_eq!(s.at(cw / 2, 0), WHITE, "{what}: the top edge");
        assert_eq!(s.at(cw / 2, ph - 1), WHITE, "{what}: the bottom edge");
        // Each line on the panel pixel below its place, the last one on
        // the panel's last.
        let across: Vec<usize> = (0..pw).filter(|&x| s.at(x, ch + ch / 2) == WHITE).collect();
        let want: Vec<usize> = (0..=4).map(|c| (c * pw / 4).min(pw - 1)).collect();
        assert_eq!(across, want, "{what}: the lines across, each a panel pixel");
        let down: Vec<usize> = (0..ph).filter(|&y| s.at(cw + cw / 2, y) == WHITE).collect();
        let want: Vec<usize> = (0..=6).map(|r| (r * ph / 6).min(ph - 1)).collect();
        assert_eq!(down, want, "{what}: the lines down, each a panel pixel");
        let other = s.rgb.chunks(3).filter(|c| ![BLACK, WHITE, GREEN].iter().any(|k| k == c)).count();
        assert_eq!(other, 0, "{what}: pixels of other colours (smoothed edges, the frame under it)");
    }
}
