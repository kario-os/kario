//! Lays out and draws one frame: our own design (flat, high contrast, text
//! on a grid), independent of phone-gui. The shell owns all semantics; this
//! only decides how things look.

use std::time::Instant;

use crate::canvas::{Canvas, Face, Rgb};
use crate::design::*;
use crate::frame::{Boot, Frame, Idle, ImeRow, Node, Overlay, Status, Tile};
use crate::i18n::t;

/// A value easing (ease-out cubic) from what is on screen to its target; a
/// new target mid-way starts from where it is, so motion never jumps.
#[derive(Default, Clone, Copy)]
pub struct Ease {
    from: f32,
    to: f32,
    start: Option<Instant>,
    ms: f32,
}

impl Ease {
    fn progress(&self, now: Option<Instant>) -> f32 {
        match (self.start, now) {
            (Some(s), Some(n)) if self.ms > 0.0 => (n.saturating_duration_since(s).as_secs_f32() * 1000.0 / self.ms).clamp(0.0, 1.0),
            _ => 1.0,
        }
    }

    pub fn value(&self, now: Option<Instant>) -> f32 {
        let e = 1.0 - (1.0 - self.progress(now)).powi(3);
        self.from + (self.to - self.from) * e
    }

    /// Without now (no live display) the value is the target at once.
    pub fn retarget(&mut self, to: f32, now: Option<Instant>, ms: f32) {
        if (to - self.to).abs() < 0.01 {
            return;
        }
        self.from = if now.is_some() { self.value(now) } else { to };
        self.start = now;
        self.to = to;
        self.ms = ms;
    }

    pub fn jump(&mut self, to: f32) {
        *self = Ease { from: to, to, start: None, ms: 0.0 };
    }

    pub fn running(&self, now: Option<Instant>) -> bool {
        now.is_some() && self.progress(now) < 1.0
    }
}

/// What survives between frames: the scroll offset of the current screen
/// (where it should be and where it is shown), and the focus moving.
#[derive(Default)]
pub struct State {
    screen: String,
    offset: f32,
    shown: Ease,
    focus: i32,
    old_focus: i32,
    fade: Ease,
    /// The time on a live display, where scrolling eases (140 ms) and the
    /// focus highlight fades over (120 ms); None (snapshots) draws the end.
    pub now: Option<Instant>,
    /// The body's geometry as last laid out, and whether the shell has it.
    layout: Option<serde_json::Value>,
    layout_sent: bool,
    /// Where the last frame drew its body, for the pointer.
    drawn: Drawn,
}

/// Where the body's nodes were last drawn, in logical pixels.
#[derive(Default)]
struct Drawn {
    screen: String,
    width: f32,
    /// The body's top and bottom, and the soft keys' top.
    top: f32,
    bottom: f32,
    soft: f32,
    x: f32,
    w: f32,
    /// Each node's top and height.
    nodes: Vec<(f32, f32)>,
    /// A grid's columns, tile height and tile count.
    grids: Vec<Option<(usize, f32, usize)>>,
    /// Something over the body (a dialog, the idle or boot screen).
    covered: bool,
}

/// What a click of the pointer means.
#[derive(Debug, PartialEq)]
pub enum Click {
    /// A body node (and in a grid, its tile): sent as "tap".
    Node(String, usize, usize),
    /// A key: a soft key, or OK where no node can be pointed at.
    Key(&'static str),
    Nothing,
}

/// What a click at (x, y) (logical pixels) does on the frame last drawn:
/// the soft keys are Options, OK and the page's own key, left to right; a
/// body node is tapped; over a dialog it is OK, as the left button was
/// before there was a pointer.
pub fn click(st: &State, x: f32, y: f32) -> Click {
    let d = &st.drawn;
    if y >= d.soft {
        return Click::Key(match x {
            x if x < d.width / 3.0 => "tab",
            x if x < d.width * 2.0 / 3.0 => "enter",
            _ => "f2",
        });
    }
    if d.covered {
        return Click::Key("enter");
    }
    if y < d.top || y >= d.bottom {
        return Click::Nothing;
    }
    for (i, &(ny, nh)) in d.nodes.iter().enumerate() {
        if y < ny || y >= ny + nh {
            continue;
        }
        let Some(Some((cols, th, count))) = d.grids.get(i).copied() else {
            return Click::Node(d.screen.clone(), i, 0);
        };
        let col = (((x - d.x) / (d.w / cols as f32)).max(0.0) as usize).min(cols - 1);
        let k = ((y - ny) / th) as usize * cols + col;
        return if k < count { Click::Node(d.screen.clone(), i, k) } else { Click::Nothing };
    }
    Click::Nothing
}

/// The pointer: an arrow with its tip at (x, y).
pub fn pointer(cv: &mut Canvas, x: f32, y: f32) {
    const ARROW: [(f32, f32); 7] = [(0.0, 0.0), (0.0, 16.0), (4.0, 12.4), (6.8, 18.6), (9.2, 17.6), (6.5, 11.6), (11.5, 11.6)];
    let pts: Vec<(f32, f32)> = ARROW.iter().map(|(dx, dy)| (x + dx, y + dy)).collect();
    cv.polygon(&pts, Rgb(0xff, 0xff, 0xff), Rgb(0x12, 0x12, 0x12), 1.1);
}

/// The geometry of the frame just laid out ("layout" message, R21), once
/// per change: the body area's height, each node's top and height, and the
/// scroll offset (logical pixels).
pub fn take_layout(st: &mut State) -> Option<serde_json::Value> {
    if st.layout_sent {
        return None;
    }
    st.layout_sent = true;
    st.layout.clone()
}

/// Lays the frame out without painting (headless: the shell still learns
/// the geometry of every frame).
pub fn layout(cv: &mut Canvas, f: &Frame, st: &mut State) {
    set_theme(&f.theme);
    if f.boot.is_some() || f.idle.is_some() {
        return;
    }
    let bottom = cv.h - SOFT_H - IME_H * f.ime.len() as f32;
    let top = if f.title.is_empty() { STATUS_H } else { STATUS_H + TITLE_H };
    arrange(cv, f, st, top, bottom);
}

/// More frames are due before the motion settles.
pub fn animating(st: &State) -> bool {
    st.shown.running(st.now) || st.fade.running(st.now)
}

pub fn draw(cv: &mut Canvas, f: &Frame, st: &mut State) {
    set_theme(&f.theme);
    st.drawn = Drawn {
        width: cv.w,
        soft: cv.h - SOFT_H,
        covered: f.overlay.is_some() || f.boot.is_some() || f.idle.is_some(),
        ..Drawn::default()
    };
    cv.clear(pal().bg);
    if let Some(b) = &f.boot {
        boot(cv, b);
        return;
    }
    let mut bottom = cv.h - SOFT_H - IME_H * f.ime.len() as f32;
    if let Some(idle) = &f.idle {
        idle_screen(cv, idle, STATUS_H, bottom);
    } else {
        let top = if f.title.is_empty() { STATUS_H } else { STATUS_H + TITLE_H };
        body(cv, f, st, top, bottom);
        if !f.title.is_empty() {
            cv.rect(0.0, STATUS_H, cv.w, TITLE_H, pal().title);
            let t = cv.fit(Face::Bold, TITLE, &f.title, cv.w - 2.0 * PAD);
            cv.text(Face::Bold, TITLE, PAD, STATUS_H + 3.5, &t, pal().fg);
        }
    }
    for row in &f.ime {
        ime_row(cv, row, bottom);
        bottom += IME_H;
    }
    soft(cv, &f.soft);
    status(cv, &f.status);
    if !f.banner.is_empty() {
        banner(cv, &f.banner);
    }
    if let Some(o) = &f.overlay {
        overlay(cv, o);
    }
    if !f.toast.is_empty() {
        toast(cv, &f.toast, cv.h - SOFT_H - IME_H * f.ime.len() as f32 - 6.0);
    }
}

fn centered(cv: &mut Canvas, face: Face, size: f32, y: f32, text: &str, c: Rgb) {
    let t = cv.fit(face, size, text, cv.w - 2.0 * PAD);
    let w = cv.measure(face, size, &t);
    cv.text(face, size, (cv.w - w) / 2.0, y, &t, c);
}

fn status(cv: &mut Canvas, s: &Status) {
    cv.rect(0.0, 0.0, cv.w, STATUS_H, pal().bar);
    cv.text(Face::Sans, CAPTION, PAD, 2.0, &s.time, pal().fg);
    if !s.mid.is_empty() {
        let c = match s.mid_kind.as_str() {
            "call" => pal().ok,
            "secure" => pal().trust,
            "mic" => pal().warn,
            "location" => pal().accent,
            _ => pal().fg,
        };
        centered(cv, Face::Bold, CAPTION, 2.0, &s.mid, c);
    }
    // Right to left: battery, signal bars, network, input method, padlock.
    let mut x = cv.w - PAD - 16.0;
    cv.frame(x, 4.0, 14.0, 8.0, 1.5, 1.0, pal().fg, pal().bar);
    cv.rect(x + 14.0, 6.5, 1.5, 3.0, pal().fg);
    let level = (s.battery.clamp(0, 100) as f32) / 100.0;
    cv.rect(x + 2.0, 6.0, 10.0 * level, 4.0, if s.battery <= 15 { pal().danger } else { pal().fg });
    x -= 16.0;
    for i in 0..4 {
        let h = 3.0 + i as f32 * 2.2;
        let c = if i < s.signal { pal().fg } else { pal().line };
        cv.rect(x + i as f32 * 3.2, 12.0 - h, 2.2, h, c);
    }
    for label in [s.net.as_str(), s.ime.as_str()] {
        if !label.is_empty() {
            let w = cv.measure(Face::Bold, MICRO, label);
            x -= w + 4.0;
            cv.text(Face::Bold, MICRO, x, 3.0, label, pal().fg);
        }
    }
    if s.locked {
        x -= 11.0;
        cv.frame(x + 1.5, 2.5, 6.0, 7.0, 3.0, 1.2, pal().fg, pal().bar);
        cv.rect(x, 6.0, 9.0, 7.0, pal().fg);
    }
}

fn soft(cv: &mut Canvas, labels: &[String; 3]) {
    let y = cv.h - SOFT_H;
    cv.rect(0.0, y, cv.w, SOFT_H, pal().bar);
    cv.rect(0.0, y, cv.w, 0.5, pal().line);
    let third = cv.w / 3.0;
    if !labels[0].is_empty() {
        let t = cv.fit(Face::Sans, BODY, &labels[0], third);
        cv.text(Face::Sans, BODY, PAD, y + 3.5, &t, pal().fg);
    }
    if !labels[1].is_empty() {
        let t = cv.fit(Face::Bold, BODY, &labels[1], third);
        let w = cv.measure(Face::Bold, BODY, &t);
        cv.text(Face::Bold, BODY, (cv.w - w) / 2.0, y + 3.5, &t, pal().fg);
    }
    if !labels[2].is_empty() {
        let t = cv.fit(Face::Sans, BODY, &labels[2], third);
        let w = cv.measure(Face::Sans, BODY, &t);
        cv.text(Face::Sans, BODY, cv.w - PAD - w, y + 3.5, &t, pal().fg);
    }
}

fn ime_row(cv: &mut Canvas, row: &ImeRow, y: f32) {
    cv.rect(0.0, y, cv.w, IME_H, pal().card);
    let mut x = PAD;
    for (i, item) in row.items.iter().enumerate() {
        let label = if row.numbered { format!("{} {}", i + 1, item) } else { item.clone() };
        let w = cv.measure(Face::Sans, BODY, &label) + 8.0;
        if x + w > cv.w - 12.0 {
            break;
        }
        let sel = row.active && i as i32 == row.sel;
        if sel {
            cv.round(x, y + 2.0, w, IME_H - 4.0, R_S, pal().accent);
        }
        cv.text(Face::Sans, BODY, x + 4.0, y + 2.5, &label, if sel { pal().bg } else if row.active { pal().fg } else { pal().dim });
        x += w + 2.0;
    }
    if row.more {
        cv.text(Face::Bold, BODY, cv.w - 10.0, y + 2.5, "›", pal().dim);
    }
}

fn banner(cv: &mut Canvas, lines: &[String]) {
    let h = 8.0 + 14.0 * lines.len() as f32;
    cv.frame(PAD, STATUS_H + 4.0, cv.w - 2.0 * PAD, h, R_M, 1.0, pal().line, pal().sheet);
    cv.rect(PAD, STATUS_H + 7.0, 2.5, h - 6.0, pal().accent);
    for (i, l) in lines.iter().enumerate() {
        let face = if i == 0 { Face::Bold } else { Face::Sans };
        let t = cv.fit(face, SMALL, l, cv.w - 4.0 * PAD);
        cv.text(face, SMALL, PAD * 2.0, STATUS_H + 7.0 + 14.0 * i as f32, &t, if i == 0 { pal().fg } else { pal().dim });
    }
}

fn toast(cv: &mut Canvas, text: &str, bottom: f32) {
    let t = cv.fit(Face::Sans, SMALL, text, cv.w - 6.0 * PAD);
    let w = cv.measure(Face::Sans, SMALL, &t) + 20.0;
    // Inverted, so it stands out on any page.
    cv.round((cv.w - w) / 2.0, bottom - 22.0, w, 20.0, 10.0, pal().fg);
    cv.text(Face::Sans, SMALL, (cv.w - w) / 2.0 + 10.0, bottom - 19.0, &t, pal().bg);
}

fn boot(cv: &mut Canvas, b: &Boot) {
    let y = cv.h * 0.38;
    centered(cv, Face::Bold, BRAND, y, &b.brand, pal().fg);
    let w = cv.w * 0.5;
    let x = (cv.w - w) / 2.0;
    cv.round(x, y + 46.0, w, 4.0, 2.0, pal().line);
    cv.round(x, y + 46.0, w * b.progress.clamp(0.0, 1.0), 4.0, 2.0, pal().accent);
}

fn idle_screen(cv: &mut Canvas, idle: &Idle, top: f32, bottom: f32) {
    let steps = 24;
    let band = (bottom - top) / steps as f32;
    let wallpaper = idle.wallpaper.as_ref().filter(|w| w.w > 0);
    if let Some(w) = wallpaper {
        // The picture covers the screen; a scrim from the top keeps the
        // clock and the lines readable on any photo.
        cv.image(w, 0.0, 0.0, cv.w, cv.h);
        cv.fade(0.0, top, cv.w, bottom - top, Rgb(0, 0, 0), 0.57, 0.12);
    } else {
        let (a, b) = pal().idle;
        for i in 0..steps {
            cv.rect(0.0, top + band * i as f32, cv.w, band + 0.5, a.mix(b, i as f32 / steps as f32));
        }
    }
    // Light text on a photo whatever the theme.
    let fg = if wallpaper.is_some() { Rgb(0xff, 0xff, 0xff) } else { pal().fg };
    let wide = cv.w > cv.h * 1.6;
    let col_w = if wide { cv.w / 2.0 } else { cv.w };
    let mut y = top + 10.0;
    let mid = |cv: &mut Canvas, face: Face, size: f32, y: f32, t: &str, c: Rgb| {
        let t = cv.fit(face, size, t, col_w - 2.0 * PAD);
        let w = cv.measure(face, size, &t);
        cv.text(face, size, (col_w - w) / 2.0, y, &t, c);
    };
    mid(cv, Face::Bold, TITLE, y, &idle.operator, fg);
    y += 22.0;
    mid(cv, Face::Sans, DISPLAY, y, &idle.time, fg);
    y += 54.0;
    mid(cv, Face::Sans, TITLE, y, &idle.date, fg);
    y += 26.0;
    let (lx, mut ly, lw) = if wide { (cv.w / 2.0 + PAD, top + 14.0, cv.w / 2.0 - 2.0 * PAD) } else { (PAD * 2.0, y, cv.w - 4.0 * PAD) };
    for l in &idle.lines {
        if ly + 22.0 > bottom - 16.0 {
            break;
        }
        cv.round_a(lx, ly, lw, 20.0, R_M, if wallpaper.is_some() { Rgb(0, 0, 0) } else { fg }, if wallpaper.is_some() { 0.35 } else { 0.08 });
        let t = cv.fit(Face::Sans, SMALL, l, lw - 12.0);
        cv.text(Face::Sans, SMALL, lx + 6.0, ly + 3.5, &t, fg);
        ly += 24.0;
    }
    if !idle.hint.is_empty() {
        centered(cv, Face::Sans, CAPTION, bottom - 14.0, &idle.hint, if wallpaper.is_some() { fg } else { pal().dim });
    }
}

struct Arranged {
    heights: Vec<f32>,
    pos: Vec<f32>,
    total: f32,
    max_off: f32,
}

/// Measures the body, moves the scroll target to keep the focus in view and
/// records the geometry for the shell.
fn arrange(cv: &mut Canvas, f: &Frame, st: &mut State, top: f32, bottom: f32) -> Arranged {
    let (x, w) = (PAD, cv.w - 2.0 * PAD);
    let area = bottom - top - 2.0 * GAP;
    let heights: Vec<f32> = f.body.iter().map(|n| node(cv, n, x, 0.0, w, area, false)).collect();
    let mut pos = Vec::with_capacity(heights.len());
    let mut total = 0.0;
    for h in &heights {
        pos.push(total);
        total += h + GAP;
    }
    if st.screen != f.screen {
        st.screen = f.screen.clone();
        st.offset = 0.0;
        st.shown.jump(0.0);
        (st.focus, st.old_focus) = (f.focus, -1);
        st.fade.jump(1.0);
    }
    if f.focus != st.focus {
        (st.old_focus, st.focus) = (st.focus, f.focus);
        st.fade.jump(0.0);
        st.fade.retarget(1.0, st.now, FOCUS_MS);
    }
    let max_off = (total - area).max(0.0);
    let focus = f.focus;
    if focus >= 0 && (focus as usize) < heights.len() {
        let (mut fy, mut fh) = (pos[focus as usize], heights[focus as usize]);
        // In a grid only the selected row has to be in view.
        let n = &f.body[focus as usize];
        if n.kind == "grid" && n.sel >= 0 {
            let (row_y, row_h) = grid_row(n, w);
            fy += row_y;
            fh = row_h;
        }
        if fh > area {
            st.offset = fy + (fh - area) * f.scroll.frac.max(0.0);
        } else {
            st.offset = st.offset.min(fy).max(fy + fh - area);
        }
    } else if max_off > 0.0 {
        st.offset = f.scroll.pos.clamp(0.0, 1.0) * max_off;
    }
    st.offset = st.offset.clamp(0.0, max_off);
    let nodes: Vec<serde_json::Value> = pos.iter().zip(&heights).map(|(y, h)| serde_json::json!({"y": y, "h": h})).collect();
    let geometry = serde_json::json!({"screen": f.screen, "area": area, "offset": st.offset, "nodes": nodes});
    if st.layout.as_ref() != Some(&geometry) {
        (st.layout, st.layout_sent) = (Some(geometry), false);
    }
    Arranged { heights, pos, total, max_off }
}

fn body(cv: &mut Canvas, f: &Frame, st: &mut State, top: f32, bottom: f32) {
    let (x, w) = (PAD, cv.w - 2.0 * PAD);
    let area = bottom - top - 2.0 * GAP;
    let Arranged { heights, pos, total, max_off } = arrange(cv, f, st, top, bottom);
    st.shown.retarget(st.offset, st.now, SCROLL_MS);
    let offset = st.shown.value(st.now).clamp(0.0, max_off);
    let fade = st.fade.value(st.now);
    // Content that fits sits where the screen asks: around the optical
    // centre (a little below the middle) or against the soft keys.
    let spare = (area - total).max(0.0);
    let lead = match f.layout.as_str() {
        "center" => spare * 0.55,
        "bottom" => spare,
        _ => 0.0,
    };
    st.drawn.screen = f.screen.clone();
    (st.drawn.top, st.drawn.bottom, st.drawn.x, st.drawn.w) = (top, bottom, x, w);
    for (i, n) in f.body.iter().enumerate() {
        let y = top + GAP + lead + pos[i] - offset;
        st.drawn.nodes.push((y, heights[i]));
        st.drawn.grids.push((n.kind == "grid").then(|| {
            let cols = n.columns.max(1) as usize;
            (cols, tile_height(&n.tiles, w / cols as f32), n.tiles.len())
        }));
        if y + heights[i] < top || y > bottom {
            continue;
        }
        if i as i32 == st.old_focus && fade < 1.0 && n.kind == "item" {
            cv.round(x - 2.0, y, w + 4.0, heights[i], R_M, pal().bg.mix(pal().focus, 1.0 - fade));
        }
        cv.focus_mix = if i as i32 == st.focus { fade } else { 1.0 };
        node(cv, n, x, y, w, area, true);
    }
    cv.focus_mix = 1.0;
    // Content scrolled under the bars is covered by them; under the soft
    // keys and candidate rows too.
    cv.rect(0.0, bottom, cv.w, cv.h - bottom, pal().bar);
    if max_off > 0.0 {
        let track = bottom - top - 4.0;
        let len = (area / total * track).max(12.0);
        let y = top + 2.0 + (track - len) * (offset / max_off);
        cv.round(cv.w - 3.0, y, 2.0, len, 1.0, pal().line);
    }
    if !f.loading.is_empty() {
        centered(cv, Face::Sans, BODY, (top + bottom) / 2.0 - 8.0, &f.loading, pal().dim);
    }
}

fn style_color(style: &str) -> Rgb {
    match style {
        "dim" => pal().dim,
        "accent" => pal().accent,
        "ok" => pal().ok,
        "warn" => pal().warn,
        "title" => pal().fg,
        _ => pal().fg,
    }
}

fn icon_color(icon: &str) -> Rgb {
    let palette = [pal().accent, pal().ok, pal().warn, Rgb(0xb0, 0x7c, 0xff), Rgb(0x3c, 0xc8, 0xc8), pal().danger];
    let h = icon.chars().fold(0u32, |a, c| a.wrapping_mul(31).wrapping_add(c as u32));
    palette[(h as usize) % palette.len()]
}

/// The app icon: its PNG when it has one, else its glyph on a colour tile.
fn glyph_icon(cv: &mut Canvas, image: &str, icon: &str, x: f32, y: f32, size: f32, draw: bool) {
    if !draw || (image.is_empty() && icon.is_empty()) {
        return;
    }
    if cv.icon(image, x, y, size) {
        return;
    }
    cv.round(x, y, size, size, size * 0.24, icon_color(icon));
    let g: String = icon.chars().take(1).collect();
    let fs = size * 0.55;
    let w = cv.measure(Face::Bold, fs, &g);
    cv.text(Face::Bold, fs, x + (size - w) / 2.0, y + (size - fs * 1.45) / 2.0, &g, Rgb(0xff, 0xff, 0xff));
}

/// Lays out (and with draw, paints) one node; returns its height.
fn node(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, area: f32, draw: bool) -> f32 {
    match n.kind.as_str() {
        "section" => {
            if draw {
                let tw = cv.text(Face::Bold, CAPTION, x, y + 3.0, &n.text, pal().dim);
                cv.rect(x + tw + 6.0, y + 9.0, (w - tw - 6.0).max(0.0), 0.5, pal().line);
            }
            17.0
        }
        "item" => item(cv, n, x, y, w, draw),
        "input" => input(cv, n, x, y, w, draw),
        "button" => {
            if draw {
                let (bg, fg) = if n.focused { (pal().accent, pal().bg) } else { (pal().card, pal().fg) };
                cv.round(x, y, w, 26.0, R_M, bg);
                let label = [&n.title, &n.label, &n.text].into_iter().find(|s| !s.is_empty()).cloned().unwrap_or_default();
                let t = cv.fit(Face::Bold, BODY, &label, w - 12.0);
                let tw = cv.measure(Face::Bold, BODY, &t);
                cv.text(Face::Bold, BODY, x + (w - tw) / 2.0, y + 5.0, &t, fg);
            }
            26.0
        }
        "progress" => {
            let label = if n.label.is_empty() { &n.text } else { &n.label };
            let lh = if label.is_empty() { 0.0 } else { 15.0 };
            if draw {
                if !label.is_empty() {
                    let t = cv.fit(Face::Sans, SMALL, label, w);
                    cv.text(Face::Sans, SMALL, x, y, &t, pal().dim);
                }
                cv.round(x, y + lh + 2.0, w, 5.0, 2.5, pal().line);
                cv.round(x, y + lh + 2.0, w * n.fraction.clamp(0.0, 1.0), 5.0, 2.5, pal().accent);
            }
            lh + 9.0
        }
        "image" | "media" => picture(cv, n, x, y, w, area, draw),
        "doc" => doc(cv, n, x, y, w, draw),
        "grid" => grid(cv, &n.tiles, n.columns, n.sel, x, y, w, draw),
        "bubble" => bubble(cv, n, x, y, w, area, draw),
        _ => {
            // text, and any kind a newer shell sends: its words, never nothing.
            let text = if n.text.is_empty() { &n.title } else { &n.text };
            if n.style == "display" {
                // One line, as large as the width allows (up to 40), right
                // aligned like a calculator's display.
                let size = (w / cv.measure(Face::Sans, 1.0, text).max(0.1) * 0.95).clamp(DISPLAY_MIN, DISPLAY);
                let t = cv.fit(Face::Sans, size, text, w);
                if draw {
                    let tw = cv.measure(Face::Sans, size, &t);
                    cv.text(Face::Sans, size, x + w - tw, y + 2.0, &t, pal().fg);
                }
                return size * 1.45 + 6.0;
            }
            let (face, size) = match n.style.as_str() {
                "big" => (Face::Sans, LARGE),
                "title" => (Face::Bold, TITLE),
                "dim" => (Face::Sans, SMALL),
                _ => (Face::Sans, BODY),
            };
            let lh = size * 1.5;
            let lines = cv.wrap(face, size, text, w);
            if draw {
                for (i, l) in lines.iter().enumerate() {
                    let ly = y + lh * i as f32 + (lh - size * 1.45) / 2.0;
                    if n.style == "big" {
                        let lw = cv.measure(face, size, l);
                        cv.text(face, size, x + (w - lw) / 2.0, ly, l, pal().fg);
                    } else {
                        cv.text(face, size, x, ly, l, style_color(&n.style));
                    }
                }
            }
            lh * lines.len() as f32
        }
    }
}

fn item(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, draw: bool) -> f32 {
    let title = if n.title.is_empty() { &n.text } else { &n.title };
    let has_icon = !n.icon.is_empty() || !n.icon_image.is_empty();
    let h = if n.subtitle.is_empty() { 26.0 } else { 36.0 };
    if !draw {
        return h;
    }
    if n.focused {
        cv.round(x - 2.0, y, w + 4.0, h, R_M, pal().bg.mix(pal().focus, cv.focus_mix));
        cv.round(x - 2.0, y + 4.0, 2.5, h - 8.0, 1.25, pal().bg.mix(pal().accent, cv.focus_mix));
    }
    let mut tx = x + 6.0;
    if has_icon {
        glyph_icon(cv, &n.icon_image, &n.icon, x + 5.0, y + (h - 20.0) / 2.0, 20.0, true);
        tx = x + 31.0;
    }
    let mut right = x + w - 4.0;
    if !n.badge.is_empty() {
        let bw = cv.measure(Face::Bold, MICRO, &n.badge) + 8.0;
        right -= bw;
        cv.round(right, y + (h - 13.0) / 2.0, bw, 13.0, 6.5, pal().accent);
        cv.text(Face::Bold, MICRO, right + 4.0, y + (h - 13.0) / 2.0 + 1.0, &n.badge, pal().bg);
        right -= 4.0;
    }
    if !n.trailing.is_empty() {
        let t = cv.fit(Face::Sans, CAPTION, &n.trailing, w * 0.4);
        let tw = cv.measure(Face::Sans, CAPTION, &t);
        right -= tw;
        cv.text(Face::Sans, CAPTION, right, y + if n.subtitle.is_empty() { 6.5 } else { 4.0 }, &t, pal().dim);
        right -= 6.0;
    }
    let face = if n.focused { Face::Bold } else { Face::Sans };
    let t = cv.fit(face, TITLE, title, right - tx);
    cv.text(face, TITLE, tx, y + if n.subtitle.is_empty() { 5.0 } else { 3.0 }, &t, pal().fg);
    if !n.subtitle.is_empty() {
        let s = cv.fit(Face::Sans, CAPTION, &n.subtitle, x + w - 4.0 - tx);
        cv.text(Face::Sans, CAPTION, tx, y + 20.0, &s, pal().dim);
    }
    h
}

fn input(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, draw: bool) -> f32 {
    let lh = if n.label.is_empty() { 0.0 } else { 14.0 };
    if !draw {
        return lh + 26.0;
    }
    if !n.label.is_empty() {
        cv.text(Face::Sans, CAPTION, x, y, &n.label, pal().dim);
    }
    let by = y + lh;
    cv.frame(x, by, w, 26.0, 5.0, if n.focused { 1.5 } else { 1.0 }, if n.focused { pal().accent } else { pal().line }, pal().card);
    let mut tx = x + 7.0;
    let value: String = if n.secret { "•".repeat(n.value.chars().count()) } else { n.value.clone() };
    if value.is_empty() && n.preedit.is_empty() {
        let p = cv.fit(Face::Sans, BODY, &n.placeholder, w - 14.0);
        cv.text(Face::Sans, BODY, tx, by + 5.5, &p, pal().dim);
    } else {
        // Keep the end of long text in view: it is where typing happens.
        let mut shown = value.clone();
        while cv.measure(Face::Sans, BODY, &shown) + cv.measure(Face::Sans, BODY, &n.preedit) > w - 18.0 && !shown.is_empty() {
            shown.remove(0);
        }
        tx += cv.text(Face::Sans, BODY, tx, by + 5.5, &shown, pal().fg);
        if !n.preedit.is_empty() {
            let pw = cv.text(Face::Sans, BODY, tx, by + 5.5, &n.preedit, pal().accent);
            cv.rect(tx, by + 21.0, pw, 1.0, pal().accent);
            tx += pw;
        }
    }
    if n.focused {
        cv.rect(tx + 1.0, by + 6.0, 1.2, 14.0, pal().accent);
    }
    lh + 26.0
}

fn picture(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, area: f32, draw: bool) -> f32 {
    let max_h = if n.fill { area } else { (area * 0.72).max(60.0) };
    let label_h = if n.label.is_empty() { 0.0 } else { 15.0 };
    let img_h = match &n.image {
        Some(img) if img.w > 0 => (w * img.h as f32 / img.w as f32).min(max_h),
        _ if n.fill => area,
        _ => 60.0f32.min(max_h),
    };
    if draw {
        match &n.image {
            Some(img) if img.w > 0 => {
                cv.rect(x, y, w, img_h, pal().bar);
                cv.image(img, x, y, w, img_h);
            }
            _ => {
                cv.round(x, y, w, img_h, R_M, pal().card);
                let t = if n.waiting.is_empty() { "…" } else { &n.waiting };
                let tw = cv.measure(Face::Sans, SMALL, t);
                cv.text(Face::Sans, SMALL, x + (w - tw) / 2.0, y + img_h / 2.0 - 8.0, t, pal().dim);
            }
        }
        if let Some(pip) = &n.pip {
            let pw = w * 0.3;
            cv.round(x + w - pw - 6.0, y + 6.0, pw + 2.0, pw * 0.75 + 2.0, R_S, pal().fg);
            cv.image(pip, x + w - pw - 5.0, y + 7.0, pw, pw * 0.75);
        }
        if n.focused {
            cv.rect(x, y + img_h - 2.0, w, 2.0, pal().accent);
        }
        if !n.label.is_empty() {
            let t = cv.fit(Face::Sans, CAPTION, &n.label, w);
            cv.text(Face::Sans, CAPTION, x, y + img_h + 2.0, &t, pal().dim);
        }
    }
    img_h + label_h + 2.0
}

fn doc(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, draw: bool) -> f32 {
    let cols = n.doc.iter().flat_map(|l| l.iter().map(|r| r.col + r.width)).max().unwrap_or(1).max(20) as f32;
    let cell = (w / cols).min(7.0);
    let size = cell * 1.85;
    let lh = size * 1.5;
    if draw {
        if n.focused {
            cv.rect(x - 3.0, y, 2.0, lh * n.doc.len() as f32, pal().accent);
        }
        for (i, line) in n.doc.iter().enumerate() {
            let ly = y + lh * i as f32;
            for r in line {
                let rx = x + r.col as f32 * cell;
                if let Some(bg) = Rgb::hex(&r.bg) {
                    cv.rect(rx, ly, r.width as f32 * cell, lh, bg);
                }
                let fg = Rgb::hex(&r.fg).unwrap_or(pal().fg);
                let face = if r.bold && !r.boxed { Face::Bold } else { Face::Mono };
                // Column positions come from the shell; draw each character in its cell.
                let mut cx = rx;
                for ch in r.text.chars() {
                    let wide = !ch.is_ascii() && !matches!(ch as u32, 0x2500..=0x257f | 0x2580..=0x259f);
                    cv.text(face, size, cx, ly + (lh - size * 1.45) / 2.0, &ch.to_string(), fg);
                    cx += cell * if wide { 2.0 } else { 1.0 };
                }
                if r.under {
                    cv.rect(rx, ly + lh - 1.5, r.width as f32 * cell, 0.8, fg);
                }
            }
        }
    }
    lh * n.doc.len() as f32 + 2.0
}

/// Keys (tiles without an icon, like the calculator's) are short; app tiles square.
fn tile_height(tiles: &[Tile], tw: f32) -> f32 {
    if tiles.iter().all(|t| t.icon.is_empty() && t.image.is_empty() && t.pixels.is_none()) {
        tw * 0.62
    } else {
        tw * 0.98
    }
}

/// Where the selected tile's row is inside a grid node.
fn grid_row(n: &Node, w: f32) -> (f32, f32) {
    let cols = n.columns.max(1) as usize;
    let th = tile_height(&n.tiles, w / cols as f32);
    ((n.sel.max(0) as usize / cols) as f32 * th, th)
}

#[allow(clippy::too_many_arguments)]
fn grid(cv: &mut Canvas, tiles: &[Tile], columns: i32, sel: i32, x: f32, y: f32, w: f32, draw: bool) -> f32 {
    let cols = columns.max(1) as usize;
    let tw = w / cols as f32;
    let th = tile_height(tiles, tw);
    let rows = tiles.len().div_ceil(cols);
    if draw {
        for (i, t) in tiles.iter().enumerate() {
            let (tx, ty) = (x + (i % cols) as f32 * tw, y + (i / cols) as f32 * th);
            tile(cv, t, i as i32 == sel || t.focused, tx + 2.0, ty + 2.0, tw - 4.0, th - 4.0, None);
        }
    }
    rows as f32 * th
}

#[allow(clippy::too_many_arguments)]
fn tile(cv: &mut Canvas, t: &Tile, focused: bool, x: f32, y: f32, w: f32, h: f32, number: Option<usize>) {
    if let Some(px) = t.pixels.as_ref().filter(|p| t.photo && p.w > 0) {
        // A photo grid: the picture is the tile, the focus a ring around it.
        cv.round(x, y, w, h, R_S, pal().card);
        cv.image(px, x, y, w, h);
        if focused {
            cv.frame(x - 1.0, y - 1.0, w + 2.0, h + 2.0, 4.0, 2.5, pal().accent, pal().accent);
            cv.image(px, x + 1.5, y + 1.5, w - 3.0, h - 3.0);
        }
        if !t.badge.is_empty() {
            // A video's length, on the picture.
            let bw = cv.measure(Face::Bold, MICRO, &t.badge) + 8.0;
            cv.round_a(x + w - bw - 4.0, y + h - 17.0, bw, 13.0, 6.5, Rgb(0, 0, 0), 0.55);
            cv.text(Face::Bold, MICRO, x + w - bw, y + h - 16.5, &t.badge, Rgb(0xff, 0xff, 0xff));
        }
        return;
    }
    if t.photo {
        // Far from the focus the shell sends no pixels: an empty cell.
        cv.round(x, y, w, h, R_S, pal().card);
        if focused {
            cv.frame(x - 1.0, y - 1.0, w + 2.0, h + 2.0, 4.0, 2.5, pal().accent, pal().card);
        }
        return;
    }
    if focused {
        cv.frame(x, y, w, h, 7.0, 1.5, pal().accent, pal().focus);
    } else {
        cv.round(x, y, w, h, R_M, pal().card);
    }
    if t.image.is_empty() && t.icon.is_empty() && t.pixels.is_none() {
        // A key, not an app (the calculator): its label is the content.
        let fs = (h * 0.32).min(22.0);
        let l = cv.fit(Face::Bold, fs, &t.title, w - 6.0);
        let lw = cv.measure(Face::Bold, fs, &l);
        cv.text(Face::Bold, fs, x + (w - lw) / 2.0, y + (h - fs * 1.45) / 2.0, &l, pal().fg);
        return;
    }
    let label_y = y + h - 15.0;
    if let Some(px) = t.pixels.as_ref().filter(|p| p.w > 0) {
        // A photo thumbnail, as large as the tile allows above the label.
        let size = (w - 8.0).min(label_y - y - 6.0);
        cv.image(px, x + (w - size) / 2.0, y + 4.0, size, size);
    } else {
        let size = (w.min(h) * 0.56).min(64.0);
        glyph_icon(cv, &t.image, &t.icon, x + (w - size) / 2.0, y + (label_y - y - size) / 2.0, size, true);
    }
    let label = match number {
        Some(n) => format!("{} {}", n, t.title),
        None => t.title.clone(),
    };
    let l = cv.fit(Face::Sans, CAPTION, &label, w - 6.0);
    let lw = cv.measure(Face::Sans, CAPTION, &l);
    cv.text(Face::Sans, CAPTION, x + (w - lw) / 2.0, label_y, &l, pal().fg);
    if !t.badge.is_empty() {
        let bw = cv.measure(Face::Bold, MICRO, &t.badge) + 7.0;
        cv.round(x + w - bw - 3.0, y + 3.0, bw, 12.0, 6.0, pal().danger);
        cv.text(Face::Bold, MICRO, x + w - bw + 0.5, y + 3.5, &t.badge, Rgb(0xff, 0xff, 0xff));
    }
}

fn bubble(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, area: f32, draw: bool) -> f32 {
    let max_w = w * 0.78;
    let inner = max_w - 14.0;
    // A bubble's message is its title (text when both are set, the title
    // then naming the sender); time and state go in subtitle / trailing.
    let (body, sender) = if n.text.is_empty() { (&n.title, "") } else { (&n.text, n.title.as_str()) };
    let lines = if body.is_empty() { Vec::new() } else { cv.wrap(Face::Sans, BODY, body, inner) };
    let text_w = lines.iter().map(|l| cv.measure(Face::Sans, BODY, l)).fold(0.0, f32::max);
    let img_h = match &n.image {
        Some(img) if img.w > 0 => (inner * img.h as f32 / img.w as f32).min(area * 0.45),
        _ => 0.0,
    };
    let head = if sender.is_empty() || n.out { 0.0 } else { 13.0 };
    let foot = if n.subtitle.is_empty() && n.trailing.is_empty() { 0.0 } else { 12.0 };
    let bw = if img_h > 0.0 { max_w } else { (text_w + 14.0).max(40.0).min(max_w) };
    let h = 8.0 + head + img_h + if img_h > 0.0 && !lines.is_empty() { 4.0 } else { 0.0 } + 15.0 * lines.len() as f32 + foot;
    if draw {
        let bx = if n.out { x + w - bw } else { x };
        let bg = if n.out { pal().bub_out } else { pal().bub_in };
        if n.focused {
            cv.frame(bx - 1.5, y - 1.5, bw + 3.0, h + 3.0, R_L + 1.5, 1.5, pal().accent, bg);
        } else {
            cv.round(bx, y, bw, h, R_L, bg);
        }
        let mut cy = y + 4.0;
        if head > 0.0 {
            cv.text(Face::Bold, CAPTION, bx + 7.0, cy, sender, pal().dim);
            cy += head;
        }
        if let Some(img) = &n.image {
            if img_h > 0.0 {
                cv.image(img, bx + 7.0, cy, inner, img_h);
                cy += img_h + 4.0;
            }
        }
        for l in &lines {
            cv.text(Face::Sans, BODY, bx + 7.0, cy, l, pal().fg);
            cy += 15.0;
        }
        if foot > 0.0 {
            let meta = [n.subtitle.as_str(), n.trailing.as_str()].iter().filter(|s| !s.is_empty()).cloned().collect::<Vec<_>>().join(" · ");
            let m = cv.fit(Face::Sans, MICRO, &meta, bw - 14.0);
            let mw = cv.measure(Face::Sans, MICRO, &m);
            cv.text(Face::Sans, MICRO, bx + bw - 7.0 - mw, cy, &m, pal().dim);
        }
    }
    h
}

fn overlay(cv: &mut Canvas, o: &Overlay) {
    match o.kind.as_str() {
        "menu" => menu(cv, o),
        "scan" | "capture" => viewfinder(cv, o),
        "picker" => picker(cv, o),
        "incoming" | "alarm" => fullscreen_alert(cv, o),
        _ => dialog(cv, o),
    }
}

fn menu(cv: &mut Canvas, o: &Overlay) {
    cv.rect_a(0.0, STATUS_H, cv.w, cv.h - STATUS_H, Rgb(0, 0, 0), 0.5);
    let row = 19.0;
    let head = if o.query.is_some() { 24.0 } else if o.title.is_empty() { 0.0 } else { 20.0 };
    let max_rows = (((cv.h - STATUS_H - SOFT_H - head - 16.0) / row).floor() as usize).max(1);
    let sel = o.items.iter().position(|i| i.sel).unwrap_or(0);
    let start = if sel >= max_rows { sel + 1 - max_rows } else { 0 };
    let shown = &o.items[start..o.items.len().min(start + max_rows)];
    let h = head + shown.len() as f32 * row + 8.0;
    let w = cv.w - 2.0 * PAD;
    let y0 = cv.h - SOFT_H - h - 4.0;
    cv.frame(PAD, y0, w, h, R_L, 1.0, pal().line, pal().sheet);
    let mut y = y0 + 4.0;
    if let Some(q) = &o.query {
        cv.frame(PAD + 5.0, y, w - 10.0, 18.0, R_S, 1.0, pal().accent, pal().card);
        let shown_q = if q.is_empty() { t("Type to search…").to_string() } else { q.clone() };
        cv.text(Face::Sans, SMALL, PAD + 10.0, y + 2.0, &shown_q, if q.is_empty() { pal().dim } else { pal().fg });
        y += head;
    } else if !o.title.is_empty() {
        cv.text(Face::Bold, SMALL, PAD + 8.0, y + 2.0, &o.title, pal().trust);
        y += head;
    }
    for it in shown {
        if it.header {
            cv.text(Face::Bold, CAPTION, PAD + 8.0, y + 4.0, &it.text, pal().dim);
        } else {
            if it.sel {
                cv.round(PAD + 3.0, y, w - 6.0, row - 1.0, R_S, pal().focus);
            }
            let kw = if it.key.is_empty() { 0.0 } else { cv.measure(Face::Bold, CAPTION, &it.key) + 10.0 };
            let t = cv.fit(Face::Sans, BODY, &it.text, w - 22.0 - kw);
            cv.text(if it.sel { Face::Bold } else { Face::Sans }, 11.0, PAD + 10.0, y + 2.0, &t, pal().fg);
            if kw > 0.0 {
                cv.text(Face::Bold, CAPTION, PAD + w - kw, y + 3.5, &it.key, pal().accent);
            }
        }
        y += row;
    }
}

fn dialog(cv: &mut Canvas, o: &Overlay) {
    cv.rect_a(0.0, STATUS_H, cv.w, cv.h - STATUS_H, Rgb(0, 0, 0), 0.55);
    let w = (cv.w - 4.0 * PAD).min(300.0);
    let x = (cv.w - w) / 2.0;
    let inner = w - 20.0;
    let system = matches!(o.kind.as_str(), "perm" | "auth");
    let mut lines: Vec<(String, Rgb, Face)> = Vec::new();
    for l in &o.lines {
        for wl in cv.wrap(Face::Sans, BODY, l, inner) {
            lines.push((wl, pal().fg, Face::Sans));
        }
    }
    // Permission prompts list what is asked for as items.
    for it in &o.items {
        lines.push((format!("· {}", it.text), pal().fg, Face::Bold));
    }
    if !o.warn.is_empty() {
        for wl in cv.wrap(Face::Bold, SMALL, &o.warn, inner) {
            lines.push((wl, pal().warn, Face::Bold));
        }
    }
    let title_h = if o.title.is_empty() && !system { 0.0 } else { 20.0 };
    let pin_h = if o.pin[1] > 0 { 22.0 } else { 0.0 };
    let note_h = if o.note.is_empty() { 0.0 } else { 15.0 };
    let choice_h = if o.choices.is_empty() { 0.0 } else { 30.0 };
    let foot_h = 13.0 * o.footer.len() as f32;
    let h = 12.0 + title_h + 16.0 * lines.len() as f32 + pin_h + note_h + choice_h + foot_h + 6.0;
    let y0 = ((cv.h - h) / 2.0).max(STATUS_H + 4.0);
    cv.frame(x, y0, w, h, R_L, 1.0, if system { pal().trust } else { pal().line }, pal().sheet);
    let mut y = y0 + 10.0;
    if title_h > 0.0 {
        let t = if o.title.is_empty() { t("System") } else { &o.title };
        cv.text(Face::Bold, BODY, x + 10.0, y, t, if system { pal().trust } else { pal().fg });
        y += title_h;
    }
    for (l, c, face) in &lines {
        cv.text(*face, BODY, x + 10.0, y, l, *c);
        y += 16.0;
    }
    if pin_h > 0.0 {
        let n = o.pin[1].min(12) as usize;
        let gap = 14.0;
        let sx = x + (w - gap * n as f32) / 2.0 + gap / 2.0;
        for i in 0..n {
            let c = if (i as i32) < o.pin[0] { pal().fg } else { pal().line };
            cv.circle(sx + gap * i as f32, y + 8.0, 4.0, c);
        }
        y += pin_h;
    }
    if note_h > 0.0 {
        let t = cv.fit(Face::Sans, SMALL, &o.note, inner);
        cv.text(Face::Sans, SMALL, x + 10.0, y, &t, if o.note_bad { pal().danger } else { pal().dim });
        y += note_h;
    }
    if choice_h > 0.0 {
        let n = o.choices.len() as f32;
        let bw = (inner - 6.0 * (n - 1.0)) / n;
        for (i, c) in o.choices.iter().enumerate() {
            let bx = x + 10.0 + i as f32 * (bw + 6.0);
            let sel = i as i32 == o.choice;
            cv.round(bx, y + 2.0, bw, 24.0, 12.0, if sel { pal().ok } else { pal().card });
            let t = cv.fit(Face::Bold, BODY, c, bw - 8.0);
            let tw = cv.measure(Face::Bold, BODY, &t);
            cv.text(Face::Bold, BODY, bx + (bw - tw) / 2.0, y + 6.0, &t, if sel { pal().bg } else { pal().fg });
        }
        y += choice_h;
    }
    for f in &o.footer {
        let t = cv.fit(Face::Sans, CAPTION, f, inner);
        cv.text(Face::Sans, CAPTION, x + 10.0, y, &t, pal().dim);
        y += 13.0;
    }
}

fn viewfinder(cv: &mut Canvas, o: &Overlay) {
    cv.rect(0.0, STATUS_H, cv.w, cv.h - STATUS_H, Rgb(0, 0, 0));
    let top = STATUS_H + 20.0;
    let bottom = cv.h - 16.0 - 13.0 * (o.lines.len() + o.footer.len()) as f32;
    cv.text(Face::Bold, BODY, PAD, STATUS_H + 4.0, &o.title, pal().trust);
    let (w, h) = (cv.w - 2.0 * PAD, bottom - top - 4.0);
    let used = match &o.image {
        Some(img) if img.w > 0 => cv.image(img, PAD, top, w, h),
        _ => {
            cv.round(PAD, top, w, h, R_M, pal().card);
            h
        }
    };
    if o.phase >= 0.0 {
        cv.rect(PAD + 8.0, top + used * o.phase.clamp(0.0, 1.0), w - 16.0, 1.5, pal().accent);
    }
    let mut y = top + used + 6.0;
    for l in o.lines.iter().chain(o.footer.iter()) {
        let t = cv.fit(Face::Sans, CAPTION, l, cv.w - 2.0 * PAD);
        cv.text(Face::Sans, CAPTION, PAD, y, &t, pal().dim);
        y += 13.0;
    }
}

fn picker(cv: &mut Canvas, o: &Overlay) {
    cv.rect(0.0, STATUS_H, cv.w, cv.h - STATUS_H, pal().bg);
    cv.rect(0.0, STATUS_H, cv.w, TITLE_H, pal().title);
    cv.text(Face::Bold, TITLE, PAD, STATUS_H + 3.5, &o.title, pal().trust);
    let cols = o.columns.max(3) as usize;
    let tw = (cv.w - 2.0 * PAD) / cols as f32;
    let th = tw * 1.0;
    let mut y = STATUS_H + TITLE_H + 4.0;
    for (i, t) in o.tiles.iter().enumerate() {
        let (tx, ty) = (PAD + (i % cols) as f32 * tw, y + (i / cols) as f32 * th);
        tile(cv, t, i as i32 == o.choice || t.focused, tx + 2.0, ty + 2.0, tw - 4.0, th - 4.0, Some(i + 1));
    }
    y += o.tiles.len().div_ceil(cols) as f32 * th + 6.0;
    for l in o.lines.iter().chain(o.footer.iter()) {
        let t = cv.fit(Face::Sans, CAPTION, l, cv.w - 2.0 * PAD);
        cv.text(Face::Sans, CAPTION, PAD, y.min(cv.h - 14.0), &t, pal().dim);
        y += 13.0;
    }
}

fn fullscreen_alert(cv: &mut Canvas, o: &Overlay) {
    let base = if o.kind == "alarm" { ALARM_BG } else { CALL_BG };
    cv.rect(0.0, STATUS_H, cv.w, cv.h - STATUS_H, base);
    let cy = STATUS_H + (cv.h - STATUS_H) * 0.3;
    if o.phase >= 0.0 {
        let r = 24.0 + 10.0 * o.phase.clamp(0.0, 1.0);
        cv.circle(cv.w / 2.0, cy, r, base.mix(ON_DARK, 0.15));
        cv.circle(cv.w / 2.0, cy, 22.0, base.mix(ON_DARK, 0.3));
    }
    let mut y = cy + 36.0;
    centered(cv, Face::Bold, HEADLINE, y, &o.title, ON_DARK);
    y += 28.0;
    for l in &o.lines {
        centered(cv, Face::Sans, BODY, y, l, ON_DARK);
        y += 16.0;
    }
    if !o.choices.is_empty() {
        let n = o.choices.len() as f32;
        let bw = (cv.w - 2.0 * PAD - 8.0 * (n - 1.0)) / n;
        let by = cv.h - SOFT_H - 36.0;
        for (i, c) in o.choices.iter().enumerate() {
            let bx = PAD + i as f32 * (bw + 8.0);
            let sel = i as i32 == o.choice;
            cv.round(bx, by, bw, 28.0, 14.0, if sel { ON_DARK } else { base.mix(ON_DARK, 0.2) });
            let t = cv.fit(Face::Bold, BODY, c, bw - 8.0);
            let tw = cv.measure(Face::Bold, BODY, &t);
            cv.text(Face::Bold, BODY, bx + (bw - tw) / 2.0, by + 6.0, &t, if sel { base } else { ON_DARK });
        }
    }
    for (i, f) in o.footer.iter().enumerate() {
        centered(cv, Face::Sans, CAPTION, cv.h - SOFT_H + 2.0 + 10.0 * i as f32, f, ON_DARK_DIM);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn ease_settles_and_retargets_without_jumps() {
        let t0 = Instant::now();
        let mut e = Ease::default();
        e.jump(0.0);
        e.retarget(100.0, Some(t0), SCROLL_MS);
        let mid = e.value(Some(t0 + Duration::from_millis(70)));
        assert!(mid > 50.0 && mid < 100.0, "ease-out is past half way at half time: {mid}");
        assert!(e.running(Some(t0 + Duration::from_millis(70))));
        // A new target mid-way starts from what is shown.
        e.retarget(0.0, Some(t0 + Duration::from_millis(70)), SCROLL_MS);
        assert!((e.value(Some(t0 + Duration::from_millis(70))) - mid).abs() < 0.01);
        let end = t0 + Duration::from_millis(70 + 141);
        assert_eq!(e.value(Some(end)), 0.0);
        assert!(!e.running(Some(end)));
        // No live display: the target at once.
        e.retarget(42.0, None, SCROLL_MS);
        assert_eq!(e.value(None), 42.0);
        assert!(!e.running(None));
    }

    // A list row, then a 3-column grid of 5 tiles 40 high, under a 24-high
    // header on a 240x320 screen.
    #[test]
    fn a_click_finds_the_node_and_tile_under_the_pointer() {
        let mut st = State::default();
        st.drawn = Drawn {
            screen: "app/home".into(),
            width: 240.0,
            top: 24.0,
            bottom: 300.0,
            soft: 300.0,
            x: 6.0,
            w: 228.0,
            nodes: vec![(27.0, 30.0), (60.0, 80.0)],
            grids: vec![None, Some((3, 40.0, 5))],
            covered: false,
        };
        let node = |i, k| Click::Node("app/home".into(), i, k);
        assert_eq!(click(&st, 120.0, 40.0), node(0, 0));
        assert_eq!(click(&st, 10.0, 70.0), node(1, 0));
        assert_eq!(click(&st, 200.0, 70.0), node(1, 2));
        assert_eq!(click(&st, 120.0, 110.0), node(1, 4));
        assert_eq!(click(&st, 200.0, 110.0), Click::Nothing, "past the last tile");
        assert_eq!(click(&st, 120.0, 10.0), Click::Nothing, "the header");
        assert_eq!(click(&st, 120.0, 200.0), Click::Nothing, "below the last node");
        assert_eq!(click(&st, 20.0, 310.0), Click::Key("tab"));
        assert_eq!(click(&st, 120.0, 310.0), Click::Key("enter"));
        assert_eq!(click(&st, 230.0, 310.0), Click::Key("f2"));
        st.drawn.covered = true;
        assert_eq!(click(&st, 120.0, 40.0), Click::Key("enter"), "a dialog is over the body");
    }
}
