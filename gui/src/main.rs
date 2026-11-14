// phone-gui is the reference pixel renderer. It speaks the renderer protocol
// (phone.frame/1, 23-renderer-protocol.md): the shell writes JSON lines to its
// stdin (hello, frame, snap, quit) and reads JSON lines from its stdout
// (ready, display, key, snapped). It runs either headless, rendering into
// memory and writing PNG snapshots for verification, or on the device,
// drawing to DRM/KMS and reading keys (cargo feature "kms").

slint::include_modules!();

mod i18n;
#[cfg(feature = "kms")]
mod sandbox;
#[cfg(feature = "kms")]
mod seccomp;
mod surfaces;

use std::cell::RefCell;
use std::io::{BufRead, Write};
use std::rc::Rc;

use base64::Engine as _;
use serde::{Deserialize, Deserializer};
use slint::{Color, Image, Model, ModelRc, Rgb8Pixel, SharedPixelBuffer, SharedString, VecModel};

/// Go encodes empty slices as null.

// phone-gui and Slint under the GPL-3.0 (gui/COPYING): a renderer to compare
// phone-paint with and the open image's renderer, never in a product image.
const LICENSE: &str = "GPL-3.0-only";
const TOOLKIT: &str = "Slint 1.18.1 (GPL-3.0)";

fn nullable<'de, D: Deserializer<'de>, T: Default + Deserialize<'de>>(d: D) -> Result<T, D::Error> {
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

#[derive(Deserialize, Clone)]
#[serde(default)]
struct Device {
    id: String,
    keypad: String,
    width: u32,
    height: u32,
    scale: f32,
}

impl Default for Device {
    fn default() -> Self {
        Device { id: "bar-t9".into(), keypad: "t9".into(), width: 480, height: 640, scale: 2.0 }
    }
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FStatus {
    time: String,
    mid: String,
    mid_kind: String,
    ime: String,
    net: String,
    signal: i32,
    battery: i32,
    locked: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FScroll {
    pos: f32,
    frac: f32,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FImage {
    w: u32,
    h: u32,
    rgb: String,
    surface: String,
    seq: u32,
    secure: bool,
    pixelated: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FRun {
    text: String,
    col: i32,
    width: i32,
    #[serde(rename = "box")]
    is_box: bool,
    fg: String,
    bg: String,
    bold: bool,
    italic: bool,
    under: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FNode {
    kind: String,
    style: String,
    text: String,
    title: String,
    subtitle: String,
    trailing: String,
    badge: String,
    icon: String,
    label: String,
    placeholder: String,
    value: String,
    preedit: String,
    secret: bool,
    out: bool,
    focused: bool,
    image: Option<FImage>,
    pip: Option<FImage>,
    waiting: String,
    #[serde(deserialize_with = "nullable")]
    doc: Vec<Vec<FRun>>,
    columns: i32,
    #[serde(deserialize_with = "nullable")]
    tiles: Vec<FTile>,
    sel: i32,
    fraction: f32,
    icon_image: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FTile {
    icon: String,
    image: String,
    pixels: Option<FImage>,
    title: String,
    badge: String,
    focused: bool,
    photo: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FIdle {
    time: String,
    date: String,
    operator: String,
    #[serde(deserialize_with = "nullable")]
    lines: Vec<String>,
    hint: String,
    wallpaper: Option<FImage>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FBoot {
    progress: f32,
    brand: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FIme {
    #[serde(deserialize_with = "nullable")]
    items: Vec<String>,
    sel: i32,
    active: bool,
    numbered: bool,
    more: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FMenuItem {
    header: bool,
    text: String,
    key: String,
    sel: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FOverlay {
    kind: String,
    title: String,
    #[serde(deserialize_with = "nullable")]
    lines: Vec<String>,
    warn: String,
    #[serde(deserialize_with = "nullable")]
    items: Vec<FMenuItem>,
    query: Option<String>,
    #[serde(deserialize_with = "nullable")]
    choices: Vec<String>,
    choice: i32,
    pin: [i32; 2],
    note: String,
    note_bad: bool,
    #[serde(deserialize_with = "nullable")]
    footer: Vec<String>,
    image: Option<FImage>,
    phase: f32,
    #[serde(deserialize_with = "nullable")]
    tiles: Vec<FTile>,
    columns: i32,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Frame {
    screen: String,
    status: FStatus,
    title: String,
    loading: String,
    #[serde(deserialize_with = "nullable")]
    body: Vec<FNode>,
    focus: i32,
    scroll: FScroll,
    #[serde(deserialize_with = "nullable")]
    soft: Vec<String>,
    #[serde(deserialize_with = "nullable")]
    ime: Vec<FIme>,
    toast: String,
    #[serde(deserialize_with = "nullable")]
    banner: Vec<String>,
    overlay: Option<FOverlay>,
    boot: Option<FBoot>,
    idle: Option<FIdle>,
    layout: String,
    theme: String,
}

#[derive(Deserialize)]
struct Msg {
    t: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    device: Option<Device>,
    #[serde(default)]
    proto: String,
    #[serde(default)]
    echo: bool,
    #[serde(default)]
    side_fd: Option<i32>,
}

const PROTO: &str = "phone.frame/1";

fn proto_major(p: &str) -> Option<u32> {
    p.strip_prefix("phone.frame/")?.split('.').next()?.parse().ok()
}

fn ready(features: &[&str], display: Option<(u32, u32, f32)>) -> serde_json::Value {
    let mut m = serde_json::json!({
        "t": "ready",
        "proto": PROTO,
        "renderer": {
            "name": env!("CARGO_PKG_NAME"),
            "version": env!("CARGO_PKG_VERSION"),
            "license": LICENSE,
            "toolkit": TOOLKIT,
        },
        "features": features,
    });
    if let Some((w, h, s)) = display {
        m["display"] = serde_json::json!({"width": w, "height": h, "scale": s});
    }
    m
}

fn color(hex: &str) -> Color {
    let v = u32::from_str_radix(hex.trim_start_matches('#'), 16).unwrap_or(0xdedee4);
    Color::from_rgb_u8((v >> 16) as u8, (v >> 8) as u8, v as u8)
}

thread_local! {
    static ICONS: RefCell<std::collections::HashMap<String, Option<Image>>> = RefCell::new(Default::default());
}

/// App icons are PNG files in the installed app directory. They come from
/// app packages, so the size is checked from the header before decoding.
fn icon(path: &str) -> Option<Image> {
    if path.is_empty() {
        return None;
    }
    ICONS.with(|c| c.borrow_mut().entry(path.to_string()).or_insert_with(|| load_icon(path)).clone())
}

fn load_icon(path: &str) -> Option<Image> {
    const MAX: u32 = 256;
    let raw = std::fs::read(path).ok().filter(|r| r.len() <= 512 << 10)?;
    let mut dec = png::Decoder::new(std::io::Cursor::new(raw));
    dec.set_transformations(png::Transformations::normalize_to_color8());
    let (w, h) = {
        let info = dec.read_header_info().ok()?;
        (info.width, info.height)
    };
    if w == 0 || h == 0 || w > MAX || h > MAX {
        eprintln!("phone-gui: icon {path}: {w}x{h} is over {MAX}x{MAX}");
        return None;
    }
    let mut reader = dec.read_info().ok()?;
    let mut buf = vec![0; reader.output_buffer_size()?];
    let out = reader.next_frame(&mut buf).ok()?;
    let px = &buf[..out.buffer_size()];
    let rgba: Vec<u8> = match out.color_type {
        png::ColorType::Rgba => px.to_vec(),
        png::ColorType::Rgb => px.chunks(3).flat_map(|p| [p[0], p[1], p[2], 255]).collect(),
        png::ColorType::GrayscaleAlpha => px.chunks(2).flat_map(|p| [p[0], p[0], p[0], p[1]]).collect(),
        png::ColorType::Grayscale => px.iter().flat_map(|&g| [g, g, g, 255]).collect(),
        _ => return None,
    };
    let buf = SharedPixelBuffer::<slint::Rgba8Pixel>::clone_from_slice(&rgba, out.width, out.height);
    Some(Image::from_rgba8(buf))
}

thread_local! {
    // The last picture taken from each surface, by frame number: a frame
    // that only moves the focus does not copy the wallpaper again.
    static SURFACES: RefCell<std::collections::HashMap<String, (u32, Image)>> = RefCell::new(Default::default());
}

fn picture(img: &Option<FImage>) -> Option<(Image, f32, bool, bool)> {
    let img = img.as_ref()?;
    if img.w == 0 || img.h == 0 {
        return None;
    }
    let shown = if img.surface.is_empty() {
        let raw = base64::engine::general_purpose::STANDARD.decode(&img.rgb).ok()?;
        if raw.len() != (img.w * img.h * 3) as usize {
            return None;
        }
        Image::from_rgb8(SharedPixelBuffer::<Rgb8Pixel>::clone_from_slice(&raw, img.w, img.h))
    } else {
        surface_image(img)?
    };
    Some((shown, img.w as f32 / img.h as f32, img.pixelated, img.secure))
}

fn surface_image(img: &FImage) -> Option<Image> {
    if let Some(i) = SURFACES.with(|c| c.borrow().get(&img.surface).filter(|(seq, _)| *seq == img.seq).map(|(_, i)| i.clone())) {
        return Some(i);
    }
    let (w, h, raw) = surfaces::rgb(&img.surface)?;
    if (w, h) != (img.w, img.h) {
        return None;
    }
    let i = Image::from_rgb8(SharedPixelBuffer::<Rgb8Pixel>::clone_from_slice(&raw, w, h));
    SURFACES.with(|c| {
        let mut c = c.borrow_mut();
        if c.len() >= 128 {
            c.clear();
        }
        c.insert(img.surface.clone(), (img.seq, i.clone()));
    });
    Some(i)
}

/// Decodes a box-drawing character (U+2500 block) or arrow into arm styles:
/// 1 light, 2 heavy, 3 double.
fn box_cell(ch: char) -> BoxCell {
    let (up, right, down, left, dashed, arrow) = match ch {
        '─' => (0, 1, 0, 1, false, 0),
        '│' => (1, 0, 1, 0, false, 0),
        '┌' | '╭' => (0, 1, 1, 0, false, 0),
        '┐' | '╮' => (0, 0, 1, 1, false, 0),
        '└' | '╰' => (1, 1, 0, 0, false, 0),
        '┘' | '╯' => (1, 0, 0, 1, false, 0),
        '├' => (1, 1, 1, 0, false, 0),
        '┤' => (1, 0, 1, 1, false, 0),
        '┬' => (0, 1, 1, 1, false, 0),
        '┴' => (1, 1, 0, 1, false, 0),
        '┼' => (1, 1, 1, 1, false, 0),
        '╴' => (0, 0, 0, 1, false, 0),
        '╵' => (1, 0, 0, 0, false, 0),
        '╶' => (0, 1, 0, 0, false, 0),
        '╷' => (0, 0, 1, 0, false, 0),
        '━' => (0, 2, 0, 2, false, 0),
        '┃' => (2, 0, 2, 0, false, 0),
        '┏' => (0, 2, 2, 0, false, 0),
        '┓' => (0, 0, 2, 2, false, 0),
        '┗' => (2, 2, 0, 0, false, 0),
        '┛' => (2, 0, 0, 2, false, 0),
        '┣' => (2, 2, 2, 0, false, 0),
        '┫' => (2, 0, 2, 2, false, 0),
        '┳' => (0, 2, 2, 2, false, 0),
        '┻' => (2, 2, 0, 2, false, 0),
        '╋' => (2, 2, 2, 2, false, 0),
        '═' => (0, 3, 0, 3, false, 0),
        '║' => (3, 0, 3, 0, false, 0),
        '╔' => (0, 3, 3, 0, false, 0),
        '╗' => (0, 0, 3, 3, false, 0),
        '╚' => (3, 3, 0, 0, false, 0),
        '╝' => (3, 0, 0, 3, false, 0),
        '╠' => (3, 3, 3, 0, false, 0),
        '╣' => (3, 0, 3, 3, false, 0),
        '╦' => (0, 3, 3, 3, false, 0),
        '╩' => (3, 3, 0, 3, false, 0),
        '╬' => (3, 3, 3, 3, false, 0),
        '╤' => (0, 3, 1, 3, false, 0),
        '╧' => (1, 3, 0, 3, false, 0),
        '╪' => (1, 3, 1, 3, false, 0),
        '╥' => (0, 1, 3, 1, false, 0),
        '╨' => (3, 1, 0, 1, false, 0),
        '╫' => (3, 1, 3, 1, false, 0),
        '╟' => (3, 1, 3, 0, false, 0),
        '╢' => (3, 0, 3, 1, false, 0),
        '╞' => (1, 3, 1, 0, false, 0),
        '╡' => (1, 0, 1, 3, false, 0),
        '┄' | '┈' | '╌' => (0, 1, 0, 1, true, 0),
        '┆' | '┊' | '╎' => (1, 0, 1, 0, true, 0),
        '┅' | '┉' | '╍' => (0, 2, 0, 2, true, 0),
        '┇' | '┋' | '╏' => (2, 0, 2, 0, true, 0),
        // Arrow heads keep a stem toward the edge they end.
        '▼' | '▽' => (1, 0, 0, 0, false, 1),
        '▲' | '△' => (0, 0, 1, 0, false, 2),
        '▶' | '▷' | '►' => (0, 0, 0, 1, false, 3),
        '◀' | '◁' | '◄' => (0, 1, 0, 0, false, 4),
        _ => (0, 1, 0, 1, false, 0),
    };
    BoxCell { up, right, down, left, dashed, arrow }
}

fn strings(v: &[String]) -> ModelRc<SharedString> {
    ModelRc::new(VecModel::from(v.iter().map(SharedString::from).collect::<Vec<_>>()))
}

fn node(n: &FNode) -> Node {
    let mut out = Node {
        kind: n.kind.as_str().into(),
        style: n.style.as_str().into(),
        text: n.text.as_str().into(),
        title: n.title.as_str().into(),
        subtitle: n.subtitle.as_str().into(),
        trailing: n.trailing.as_str().into(),
        badge: n.badge.as_str().into(),
        icon: n.icon.as_str().into(),
        label: n.label.as_str().into(),
        placeholder: n.placeholder.as_str().into(),
        value: n.value.as_str().into(),
        preedit: n.preedit.as_str().into(),
        secret: n.secret,
        out: n.out,
        focused: n.focused,
        waiting: n.waiting.as_str().into(),
        img_ratio: 1.0,
        columns: n.columns,
        sel: n.sel,
        fraction: n.fraction,
        tiles: ModelRc::new(VecModel::from(
            n.tiles
                .iter()
                .map(|t| {
                    let img = picture(&t.pixels).map(|p| p.0).or_else(|| icon(&t.image));
                    Tile {
                        icon: t.icon.as_str().into(),
                        title: t.title.as_str().into(),
                        badge: t.badge.as_str().into(),
                        focused: t.focused,
                        has_img: img.is_some(),
                        img: img.unwrap_or_default(),
                        photo: t.photo,
                    }
                })
                .collect::<Vec<_>>(),
        )),
        ..Default::default()
    };
    if let Some(img) = icon(&n.icon_image) {
        out.icon_img = img;
        out.has_icon_img = true;
    }
    if let Some((img, ratio, pixelated, secure)) = picture(&n.image) {
        out.img = img;
        out.has_img = true;
        out.img_ratio = ratio;
        out.pixelated = pixelated;
        out.secure = secure;
    }
    if let Some((img, _, _, _)) = picture(&n.pip) {
        out.pip = img;
        out.has_pip = true;
    }
    if !n.doc.is_empty() {
        let lines: Vec<DocLine> = n
            .doc
            .iter()
            .map(|runs| DocLine {
                runs: ModelRc::new(VecModel::from(
                    runs.iter()
                        .map(|r| Run {
                            text: r.text.as_str().into(),
                            col: r.col,
                            width: r.width,
                            cells: ModelRc::new(VecModel::from(if r.is_box {
                                r.text.chars().map(box_cell).collect::<Vec<_>>()
                            } else {
                                Vec::new()
                            })),
                            fg: color(&r.fg),
                            bg: if r.bg.is_empty() { Color::default() } else { color(&r.bg) },
                            has_bg: !r.bg.is_empty(),
                            bold: r.bold,
                            italic: r.italic,
                            under: r.under,
                        })
                        .collect::<Vec<_>>(),
                )),
            })
            .collect();
        out.doc = ModelRc::new(VecModel::from(lines));
    }
    out
}

fn overlay(o: &FOverlay) -> Overlay {
    let mut out = Overlay {
        kind: o.kind.as_str().into(),
        title: o.title.as_str().into(),
        lines: strings(&o.lines),
        warn: o.warn.as_str().into(),
        items: ModelRc::new(VecModel::from(
            o.items
                .iter()
                .map(|i| MenuItem { header: i.header, text: i.text.as_str().into(), key: i.key.as_str().into(), sel: i.sel })
                .collect::<Vec<_>>(),
        )),
        query: o.query.clone().unwrap_or_default().into(),
        has_query: o.query.is_some(),
        choices: strings(&o.choices),
        choice: o.choice,
        pin_filled: o.pin[0],
        pin_total: o.pin[1],
        note: o.note.as_str().into(),
        note_bad: o.note_bad,
        footer: strings(&o.footer),
        phase: o.phase,
        columns: o.columns,
        sel_top: menu_sel_top(o),
        tiles: ModelRc::new(VecModel::from(
            o.tiles
                .iter()
                .map(|t| {
                    let img = picture(&t.pixels).map(|p| p.0).or_else(|| icon(&t.image));
                    Tile {
                        icon: t.icon.as_str().into(),
                        title: t.title.as_str().into(),
                        badge: t.badge.as_str().into(),
                        focused: t.focused,
                        has_img: img.is_some(),
                        img: img.unwrap_or_default(),
                        photo: t.photo,
                    }
                })
                .collect::<Vec<_>>(),
        )),
        ..Default::default()
    };
    if let Some((img, _, _, _)) = picture(&o.image) {
        out.img = img;
        out.has_img = true;
    }
    out
}

/// Top of the selected menu row inside the sheet, from the fixed row heights
/// in phone.slint (title 24, query 26, header 20, item 26).
fn menu_sel_top(o: &FOverlay) -> f32 {
    let mut y = 24.0 + if o.query.is_some() { 26.0 } else { 0.0 };
    for i in &o.items {
        if i.sel {
            return y;
        }
        y += if i.header { 20.0 } else { 26.0 };
    }
    0.0
}

/// Scroll intent from the shell plus the geometry the layout reported.
#[derive(Default)]
struct View {
    pos: f32,
    frac: f32,
    focus: i32,
    geom: Vec<(f32, f32)>,
    // A focused grid: (row of the focused tile, number of rows).
    grid: Option<(usize, usize)>,
    screen: String,
    // The standby and boot screens have no body to report.
    bodyless: bool,
    reported: Option<serde_json::Value>,
}

thread_local! {
    static VIEW: RefCell<View> = RefCell::new(View::default());
    static HELD: RefCell<Option<(String, slint::Timer)>> = const { RefCell::new(None) };
    static NINE_KEY: RefCell<bool> = const { RefCell::new(false) };
}

/// How long a 9-key key must be held to count as a long press.
const LONG_PRESS: std::time::Duration = std::time::Duration::from_millis(600);

/// On a 9-key pad, digits, * and # are sent when released (a tap) or once
/// held for LONG_PRESS (a long press), the way Nokia phones behave.
fn text_key(pressed: bool, text: &str) {
    let holdable = NINE_KEY.with(|n| *n.borrow()) && text.chars().count() == 1 && "0123456789*#".contains(text);
    if !holdable {
        if pressed {
            send(&serde_json::json!({"t": "key", "key": "", "text": text}));
        }
        return;
    }
    if pressed {
        let timer = slint::Timer::default();
        let held = text.to_string();
        timer.start(slint::TimerMode::SingleShot, LONG_PRESS, move || {
            let fire = HELD.with(|h| matches!(&*h.borrow(), Some((t, _)) if *t == held));
            if fire {
                HELD.with(|h| *h.borrow_mut() = None);
                send(&serde_json::json!({"t": "key", "key": "", "text": held, "long": true}));
            }
        });
        HELD.with(|h| *h.borrow_mut() = Some((text.to_string(), timer)));
        return;
    }
    let tap = HELD.with(|h| {
        let mut h = h.borrow_mut();
        if matches!(&*h, Some((t, _)) if t == text) {
            *h = None;
            true
        } else {
            false
        }
    });
    if tap {
        send(&serde_json::json!({"t": "key", "key": "", "text": text}));
    }
}

/// The colours of a theme (frame.theme), the same as the text renderer's
/// (shell/render.go): background, text, dim, accent, focus, status bar,
/// title bar, soft keys, menu, menu selection.
/// One theme of Settings › Theme (Frame.theme). The first ten roles are the
/// text renderer's (shell/render.go textThemes); the rest only pixels need.
struct Palette {
    bg: (u8, u8, u8),
    fg: (u8, u8, u8),
    dim: (u8, u8, u8),
    accent: (u8, u8, u8),
    focus: (u8, u8, u8),
    status: (u8, u8, u8),
    title: (u8, u8, u8),
    soft: (u8, u8, u8),
    menu: (u8, u8, u8),
    menu_sel: (u8, u8, u8),
    card: (u8, u8, u8),
    line: (u8, u8, u8),
    bub_in: (u8, u8, u8),
    bub_out: (u8, u8, u8),
    ok: (u8, u8, u8),
    warn: (u8, u8, u8),
    danger: (u8, u8, u8),
    /// The standby screen without a wallpaper, top to bottom.
    idle: [(u8, u8, u8); 3],
}

const DARK: Palette = Palette {
    bg: (18, 18, 24), fg: (222, 222, 228), dim: (130, 132, 145), accent: (95, 170, 255), focus: (44, 62, 96),
    status: (8, 8, 12), title: (38, 42, 58), soft: (28, 29, 38), menu: (34, 37, 50), menu_sel: (66, 98, 160),
    card: (27, 29, 39), line: (48, 54, 74), bub_in: (52, 54, 64), bub_out: (38, 92, 58),
    ok: (100, 205, 125), warn: (240, 175, 70), danger: (240, 95, 95),
    idle: [(30, 45, 82), (15, 21, 38), (38, 23, 61)],
};

const LIGHT: Palette = Palette {
    bg: (244, 245, 247), fg: (29, 33, 41), dim: (107, 114, 128), accent: (31, 111, 235), focus: (214, 230, 255),
    status: (228, 230, 235), title: (255, 255, 255), soft: (233, 235, 239), menu: (255, 255, 255), menu_sel: (190, 214, 255),
    card: (255, 255, 255), line: (213, 217, 224), bub_in: (228, 230, 235), bub_out: (204, 236, 212),
    ok: (26, 143, 74), warn: (194, 106, 0), danger: (208, 58, 58),
    idle: [(220, 232, 250), (236, 240, 247), (246, 233, 242)],
};

const RETRO: Palette = Palette {
    bg: (180, 198, 156), fg: (28, 40, 20), dim: (76, 92, 60), accent: (36, 56, 24), focus: (146, 168, 118),
    status: (162, 182, 136), title: (168, 187, 142), soft: (162, 182, 136), menu: (169, 188, 144), menu_sel: (138, 158, 112),
    card: (169, 188, 144), line: (138, 158, 112), bub_in: (162, 182, 136), bub_out: (146, 168, 118),
    ok: (36, 56, 24), warn: (90, 64, 16), danger: (106, 32, 16),
    idle: [(184, 202, 160), (176, 195, 150), (166, 186, 140)],
};

const AMBER: Palette = Palette {
    bg: (21, 18, 12), fg: (243, 230, 200), dim: (168, 154, 124), accent: (255, 176, 0), focus: (62, 48, 18),
    status: (12, 10, 6), title: (36, 30, 19), soft: (28, 24, 16), menu: (34, 29, 20), menu_sel: (110, 80, 20),
    card: (34, 29, 20), line: (59, 51, 38), bub_in: (40, 34, 24), bub_out: (70, 52, 16),
    ok: (156, 200, 90), warn: (255, 138, 60), danger: (239, 90, 74),
    idle: [(42, 30, 12), (30, 22, 10), (22, 16, 8)],
};

fn palette(name: &str) -> &'static Palette {
    match name {
        "light" => &LIGHT,
        "retro" => &RETRO,
        "amber" => &AMBER,
        _ => &DARK,
    }
}

fn set_theme(ui: &Phone, name: &str) {
    let p = palette(name);
    let c = |(r, g, b): (u8, u8, u8)| slint::Color::from_rgb_u8(r, g, b);
    let t = ui.global::<Theme>();
    t.set_bg(c(p.bg));
    t.set_fg(c(p.fg));
    t.set_dim(c(p.dim));
    t.set_accent(c(p.accent));
    t.set_focus(c(p.focus));
    t.set_status(c(p.status));
    t.set_title(c(p.title));
    t.set_soft(c(p.soft));
    t.set_menu(c(p.menu));
    t.set_menu_sel(c(p.menu_sel));
    t.set_card(c(p.card));
    t.set_rule(c(p.line));
    t.set_bub_in(c(p.bub_in));
    t.set_bub_out(c(p.bub_out));
    t.set_ok(c(p.ok));
    t.set_warn(c(p.warn));
    t.set_danger(c(p.danger));
    t.set_idle_a(c(p.idle[0]));
    t.set_idle_mid(c(p.idle[1]));
    t.set_idle_b(c(p.idle[2]));
}

fn apply(ui: &Phone, f: &Frame) {
    set_theme(ui, &f.theme);
    ui.set_body_layout(f.layout.as_str().into());
    let s = &f.status;
    ui.set_time(s.time.as_str().into());
    ui.set_mid(s.mid.as_str().into());
    ui.set_mid_kind(s.mid_kind.as_str().into());
    ui.set_ime_mode(s.ime.as_str().into());
    ui.set_net(s.net.as_str().into());
    ui.set_signal(s.signal);
    ui.set_battery(s.battery);
    ui.set_locked(s.locked);
    ui.set_screen_title(f.title.as_str().into());
    ui.set_loading(f.loading.as_str().into());
    let mut soft = f.soft.clone();
    soft.resize(3, String::new());
    ui.set_soft(strings(&soft));
    ui.set_ime(ModelRc::new(VecModel::from(
        f.ime
            .iter()
            .map(|r| ImeRow { items: strings(&r.items), sel: r.sel, active: r.active, numbered: r.numbered, more: r.more })
            .collect::<Vec<_>>(),
    )));
    ui.set_toast(f.toast.as_str().into());
    ui.set_banner_title(f.banner.first().map(String::as_str).unwrap_or("").into());
    ui.set_banner_body(f.banner.get(1).map(String::as_str).unwrap_or("").into());
    ui.set_has_overlay(f.overlay.is_some());
    if let Some(o) = &f.overlay {
        ui.set_overlay(overlay(o));
    }
    let nodes: Vec<Node> = if f.idle.is_some() { Vec::new() } else { f.body.iter().map(node).collect() };
    ui.set_idle_on(f.idle.is_some());
    if let Some(i) = &f.idle {
        ui.set_idle_time(i.time.as_str().into());
        ui.set_idle_date(i.date.as_str().into());
        ui.set_idle_operator(i.operator.as_str().into());
        ui.set_idle_lines(strings(&i.lines));
        ui.set_idle_hint(i.hint.as_str().into());
        let wp = picture(&i.wallpaper).map(|p| p.0);
        ui.set_idle_has_wallpaper(wp.is_some());
        ui.set_idle_wallpaper(wp.unwrap_or_default());
    }
    let same_screen = ui.get_nodes().row_count() == nodes.len();
    ui.set_nodes(ModelRc::new(VecModel::from(nodes)));
    VIEW.with(|v| {
        let mut v = v.borrow_mut();
        v.pos = f.scroll.pos;
        v.frac = f.scroll.frac;
        v.focus = f.focus;
        v.geom.resize(f.body.len(), (0.0, 0.0));
        v.screen = f.screen.clone();
        v.bodyless = f.idle.is_some() || f.boot.is_some();
        v.grid = usize::try_from(f.focus).ok().and_then(|i| f.body.get(i)).and_then(|n| {
            let cols = n.columns.max(1) as usize;
            (n.kind == "grid" && n.sel >= 0).then(|| (n.sel as usize / cols, n.tiles.len().div_ceil(cols).max(1)))
        });
    });
    ui.set_boot_on(f.boot.is_some());
    if let Some(b) = &f.boot {
        ui.set_boot_progress(b.progress);
        ui.set_brand(b.brand.as_str().into());
    }
    if !same_screen {
        ui.set_scroll_y(0.0);
    }
}

/// Reports the geometry of the frame as laid out ("layout"), when it
/// changed: the body's height, the scroll offset and each node's place.
fn report_layout(ui: &Phone) {
    let area = ui.get_body_h();
    let offset = ui.get_scroll_y();
    let msg = VIEW.with(|v| {
        let mut v = v.borrow_mut();
        if v.bodyless {
            return None;
        }
        let nodes: Vec<_> = v.geom.iter().map(|&(y, h)| serde_json::json!({"y": y, "h": h})).collect();
        let l = serde_json::json!({"screen": v.screen, "area": area, "offset": offset, "nodes": nodes});
        if v.reported.as_ref() == Some(&l) {
            return None;
        }
        v.reported = Some(l.clone());
        Some(l)
    });
    if let Some(l) = msg {
        send(&serde_json::json!({"t": "layout", "layout": l}));
    }
}

/// Moves the viewport so that it follows the shell's intent; returns whether
/// it changed (another layout pass is then needed).
fn settle(ui: &Phone) -> bool {
    let body_h = ui.get_body_h();
    let max = (ui.get_content_h() - body_h).max(0.0);
    let target = VIEW.with(|v| {
        let v = v.borrow();
        let mut s = v.pos * max;
        if let Some(&(mut top, mut h)) = usize::try_from(v.focus).ok().and_then(|i| v.geom.get(i)) {
            if let Some((row, rows)) = v.grid {
                h /= rows as f32;
                top += row as f32 * h;
            }
            if h > body_h {
                s = top + v.frac.max(0.0) * (h - body_h);
            } else {
                if top + h > s + body_h {
                    s = top + h - body_h;
                }
                if top < s {
                    s = top;
                }
            }
        }
        s.clamp(0.0, max)
    });
    if (target - ui.get_scroll_y()).abs() > 0.5 {
        ui.set_scroll_y(target);
        return true;
    }
    false
}

fn set_words(ui: &Phone) {
    let w = ui.global::<Words>();
    w.set_secure_mask(i18n::t("Secure content · screenshot hidden").into());
    w.set_payment_password(i18n::t("Payment password").into());
    w.set_opening_camera(i18n::t("Opening the camera…").into());
    w.set_tagline(i18n::t("The keyboard-first phone system").into());
    w.set_alarm(i18n::t("Alarm ringing").into());
    w.set_options(i18n::t("Options").into());
}

fn wire_callbacks(ui: &Phone) {
    ui.on_geom(|i, y, h| {
        VIEW.with(|v| {
            let mut v = v.borrow_mut();
            let i = i as usize;
            if i >= v.geom.len() {
                v.geom.resize(i + 1, (0.0, 0.0));
            }
            v.geom[i] = (y, h);
        })
    });
    ui.on_key(|name, text| {
        let line = serde_json::json!({"t": "key", "key": name.as_str(), "text": text.as_str()});
        send(&line);
    });
    ui.on_text_key(|pressed, text| text_key(pressed, text.as_str()));
}

fn send(v: &serde_json::Value) {
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{v}");
    let _ = out.flush();
}

const DEVICE_FONTS: &str = "/usr/share/fonts/noto-cjk";

fn main() {
    if std::env::var_os("SLINT_FONT_PATH").is_none() && std::path::Path::new(DEVICE_FONTS).is_dir() {
        // SAFETY: no other thread exists yet.
        unsafe { std::env::set_var("SLINT_FONT_PATH", DEVICE_FONTS) };
    }
    let args: Vec<String> = std::env::args().collect();
    let headless = args.iter().any(|a| a == "--headless");
    let snap_dir = args
        .iter()
        .position(|a| a == "--snap-dir")
        .and_then(|i| args.get(i + 1).cloned())
        .unwrap_or_else(|| ".".into());
    // The first line is "hello". Read it without keeping stdin locked: on
    // the device another thread reads the frames.
    let mut first = String::new();
    let _ = std::io::stdin().read_line(&mut first);
    let hello = serde_json::from_str::<Msg>(&first).ok();
    let shell_proto = hello.as_ref().map(|m| m.proto.clone()).unwrap_or_default();
    if proto_major(&shell_proto) != proto_major(PROTO) {
        eprintln!("phone-gui: the shell speaks {shell_proto:?}, this renderer {PROTO}");
        std::process::exit(3);
    }
    // Surfaces come on the side channel; without one the shell sends the
    // pixels in the frames.
    let shm = hello.as_ref().and_then(|m| m.side_fd).map(surfaces::listen).is_some();
    let dev = hello.and_then(|m| m.device).unwrap_or_default();
    if headless {
        headless_main(dev, shm, std::io::stdin().lock().lines(), &snap_dir);
    } else {
        device_main(dev, shm);
    }
}

struct Headless {
    window: Rc<slint::platform::software_renderer::MinimalSoftwareWindow>,
    start: std::time::Instant,
}

impl slint::platform::Platform for Headless {
    fn create_window_adapter(&self) -> Result<Rc<dyn slint::platform::WindowAdapter>, slint::PlatformError> {
        Ok(self.window.clone())
    }
    fn duration_since_start(&self) -> core::time::Duration {
        self.start.elapsed()
    }
}

fn headless_main(dev: Device, shm: bool, lines: impl Iterator<Item = std::io::Result<String>>, snap_dir: &str) {
    use slint::platform::software_renderer::{MinimalSoftwareWindow, PremultipliedRgbaColor, RepaintBufferType};
    let window = MinimalSoftwareWindow::new(RepaintBufferType::NewBuffer);
    slint::platform::set_platform(Box::new(Headless { window: window.clone(), start: std::time::Instant::now() }))
        .expect("platform");
    let ui = Phone::new().expect("ui");
    set_words(&ui);
    wire_callbacks(&ui);
    window.dispatch_event(slint::platform::WindowEvent::ScaleFactorChanged { scale_factor: dev.scale });
    window.set_size(slint::PhysicalSize::new(dev.width, dev.height));
    ui.set_screen_w(dev.width as f32 / dev.scale);
    ui.set_screen_h(dev.height as f32 / dev.scale);
    ui.set_mask_secure(true);
    ui.show().expect("show");
    let (w, h) = (dev.width as usize, dev.height as usize);
    let mut buf = vec![PremultipliedRgbaColor::default(); w * h];
    let draw = |buf: &mut [PremultipliedRgbaColor]| {
        slint::platform::update_timers_and_animations();
        window.draw_if_needed(|r| {
            r.render(buf, w);
        });
    };
    let mut features = vec!["snap", "images", "anim", "patch", "layout"];
    if shm {
        features.push("shm");
    }
    send(&ready(&features, Some((dev.width, dev.height, dev.scale))));
    let mut doc = serde_json::Value::Null;
    for line in lines {
        let Ok(line) = line else { break };
        let Ok(m) = serde_json::from_str::<Msg>(&line) else { continue };
        match m.t.as_str() {
            "frame" | "patch" => match next_frame(&mut doc, &m.t, &line) {
                Ok(f) => apply(&ui, &f),
                Err(e) => eprintln!("phone-gui: bad frame: {e}"),
            },
            "snap" => {
                for i in 0..8 {
                    draw(&mut buf);
                    if !settle(&ui) && i > 0 {
                        break;
                    }
                }
                let t0 = std::time::Instant::now();
                while window.has_active_animations() && t0.elapsed() < std::time::Duration::from_millis(600) {
                    std::thread::sleep(std::time::Duration::from_millis(16));
                    window.request_redraw();
                    draw(&mut buf);
                }
                window.request_redraw();
                draw(&mut buf);
                report_layout(&ui);
                let path = std::path::Path::new(snap_dir).join(format!("{}.png", m.name));
                let res = write_png(&path, &buf, w as u32, h as u32);
                let mut reply = serde_json::json!({"t": "snapped", "name": m.name,
                    "err": res.err().map(|e| e.to_string()).unwrap_or_default()});
                if m.echo {
                    reply["frame"] = doc.clone();
                }
                send(&reply);
            }
            "quit" => break,
            _ => {}
        }
    }
}

/// The frame after a "frame" message (a whole frame) or a "patch" (a JSON
/// merge patch, RFC 7386, against the last one).
fn next_frame(doc: &mut serde_json::Value, t: &str, line: &str) -> serde_json::Result<Frame> {
    let v: serde_json::Value = serde_json::from_str(line)?;
    if t == "frame" {
        *doc = v;
    } else if let Some(p) = v.get("patch") {
        merge(doc, p);
    }
    serde_json::from_value(doc.clone())
}

fn merge(target: &mut serde_json::Value, patch: &serde_json::Value) {
    let serde_json::Value::Object(p) = patch else {
        *target = patch.clone();
        return;
    };
    // Our one extension: an array patched element by element.
    if let (Some(serde_json::Value::Object(set)), serde_json::Value::Array(arr)) = (p.get("$set"), &mut *target) {
        for (i, v) in set {
            if let Some(e) = i.parse::<usize>().ok().and_then(|i| arr.get_mut(i)) {
                merge(e, v);
            }
        }
        return;
    }
    if !target.is_object() {
        *target = serde_json::Value::Object(Default::default());
    }
    let t = target.as_object_mut().expect("object");
    for (k, v) in p {
        if v.is_null() {
            t.remove(k);
        } else {
            merge(t.entry(k.clone()).or_insert(serde_json::Value::Null), v);
        }
    }
}

fn write_png(
    path: &std::path::Path,
    buf: &[slint::platform::software_renderer::PremultipliedRgbaColor],
    w: u32,
    h: u32,
) -> Result<(), Box<dyn std::error::Error>> {
    if let Some(dir) = path.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let mut enc = png::Encoder::new(std::io::BufWriter::new(std::fs::File::create(path)?), w, h);
    enc.set_color(png::ColorType::Rgb);
    enc.set_depth(png::BitDepth::Eight);
    let rgb: Vec<u8> = buf.iter().flat_map(|p| [p.red, p.green, p.blue]).collect();
    enc.write_header()?.write_image_data(&rgb)?;
    Ok(())
}

#[cfg(feature = "kms")]
thread_local! {
    static REPORTED: std::cell::Cell<(u32, u32)> = const { std::cell::Cell::new((0, 0)) };
}

#[cfg(feature = "kms")]
fn device_main(dev: Device, shm: bool) {
    let sandboxed = match sandbox::enter() {
        Ok(f) => f,
        Err(e) => {
            eprintln!("phone-gui: sandbox: {e}");
            std::process::exit(6);
        }
    };
    NINE_KEY.with(|n| *n.borrow_mut() = dev.keypad == "t9");
    // SAFETY: set before any other thread exists.
    unsafe { std::env::set_var("SLINT_SCALE_FACTOR", dev.scale.to_string()) };
    let ui = Phone::new().expect("ui");
    set_words(&ui);
    wire_callbacks(&ui);
    let weak = ui.as_weak();
    std::thread::spawn(move || {
        let mut doc = serde_json::Value::Null;
        for line in std::io::stdin().lock().lines() {
            let Ok(line) = line else { break };
            let Ok(m) = serde_json::from_str::<Msg>(&line) else { continue };
            match m.t.as_str() {
                "frame" | "patch" => {
                    let Ok(f) = next_frame(&mut doc, &m.t, &line) else { continue };
                    let _ = weak.upgrade_in_event_loop(move |ui| {
                        // The panel's mode decides the size, not the device profile.
                        let size = ui.window().size();
                        let sf = ui.window().scale_factor();
                        if REPORTED.with(|r| r.replace((size.width, size.height))) != (size.width, size.height) {
                            send(&serde_json::json!({"t": "display",
                                "display": {"width": size.width, "height": size.height, "scale": sf}}));
                        }
                        ui.set_screen_w(size.width as f32 / sf);
                        ui.set_screen_h(size.height as f32 / sf);
                        apply(&ui, &f);
                        let w = ui.as_weak();
                        slint::Timer::single_shot(std::time::Duration::from_millis(1), move || {
                            if let Some(ui) = w.upgrade() {
                                while settle(&ui) {}
                                // The nodes report their places as they
                                // settle; send them once that is done.
                                let w = ui.as_weak();
                                slint::Timer::single_shot(std::time::Duration::from_millis(16), move || {
                                    if let Some(ui) = w.upgrade() {
                                        report_layout(&ui);
                                    }
                                });
                            }
                        });
                    });
                }
                "quit" => break,
                _ => {}
            }
        }
        let _ = slint::quit_event_loop();
    });
    let mut features = vec!["kms", "keys", "images", "anim", "patch", "layout"];
    if shm {
        features.push("shm");
    }
    features.extend(sandboxed);
    send(&ready(&features, None));
    ui.run().expect("run");
}

#[cfg(not(feature = "kms"))]
fn device_main(_: Device, _: bool) {
    eprintln!("phone-gui: built without the \"kms\" feature; use --headless");
    std::process::exit(2);
}
