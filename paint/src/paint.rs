//! Lays out and draws one frame: our own design (flat, high contrast, text
//! on a grid), independent of phone-gui. The shell owns all semantics; this
//! only decides how things look.

use std::hash::{Hash, Hasher};
use std::rc::Rc;
use std::time::{Duration, Instant};

use tiny_skia::Pixmap;

use crate::canvas::{Canvas, Face, IRect, Rgb};
use crate::design::*;
use crate::frame::{Boot, Frame, Idle, Image, ImeRow, Keypad, Node, Overlay, Status, Tile};
use crate::i18n::t;
use crate::motion::Slide;

/// A value easing from what is on screen to its target in `ms`: from rest
/// eased out (cubic); a new target while it moves goes on from where it is
/// at the speed it has, on a cubic Hermite curve that comes to rest on the
/// target in the same time, so neither the place nor the speed jumps
/// (37-motion.md §3). The speed it goes on with is cut to what never takes
/// it past the target, and is 0 when the target is behind it.
#[derive(Default, Clone, Copy)]
pub struct Ease {
    from: f32,
    to: f32,
    start: Option<Instant>,
    ms: f32,
    /// The speed at the start, in averages ((to - from) / ms): 3 is the
    /// ease-out cubic's; from 0 to 3 the curve never passes the target.
    a: f32,
}

impl Ease {
    fn progress(&self, now: Option<Instant>) -> f32 {
        match (self.start, now) {
            (Some(s), Some(n)) if self.ms > 0.0 => (n.saturating_duration_since(s).as_secs_f32() * 1000.0 / self.ms).clamp(0.0, 1.0),
            _ => 1.0,
        }
    }

    /// The share of the way gone at progress t: the ease-out cubic, less
    /// what a slower start keeps back (nothing at 3, so a move from rest
    /// is the ease-out to the bit).
    fn eased(&self, t: f32) -> f32 {
        1.0 - (1.0 - t).powi(3) - (3.0 - self.a) * t * (1.0 - t).powi(2)
    }

    pub fn value(&self, now: Option<Instant>) -> f32 {
        self.from + (self.to - self.from) * self.eased(self.progress(now))
    }

    /// How fast it moves at now, in units a millisecond.
    pub fn speed(&self, now: Option<Instant>) -> f32 {
        if !self.running(now) {
            return 0.0;
        }
        let t = self.progress(now);
        (self.to - self.from) / self.ms * (1.0 - t) * (self.a + t * (6.0 - 3.0 * self.a))
    }

    /// Without now (no live display) the value is the target at once.
    pub fn retarget(&mut self, to: f32, now: Option<Instant>, ms: f32) {
        if (to - self.to).abs() < 0.01 {
            return;
        }
        if now.is_none() {
            *self = Ease { from: to, to, start: None, ms, a: 3.0 };
            return;
        }
        let from = self.value(now);
        let d = to - from;
        let a = if !self.running(now) {
            3.0
        } else if d.abs() > f32::EPSILON {
            (self.speed(now) * ms / d).clamp(0.0, 3.0)
        } else {
            0.0
        };
        *self = Ease { from, to, start: now, ms, a };
    }

    pub fn jump(&mut self, to: f32) {
        *self = Ease { from: to, to, start: None, ms: 0.0, a: 3.0 };
    }

    pub fn running(&self, now: Option<Instant>) -> bool {
        now.is_some() && self.progress(now) < 1.0
    }

    /// Where the last move started and where it goes.
    fn ends(&self) -> (f32, f32) {
        (self.from, self.to)
    }
}

/// A highlight gliding from one place to another in FOCUS_MS, each of its
/// two numbers eased as the list's scroll (a list row: its top and
/// height; a grid's tile: its column and row; a candidate: its x and
/// width); a new place mid-way goes on from where it is, as fast.
#[derive(Default, Clone, Copy)]
struct Glide {
    a: Ease,
    b: Ease,
}

impl Glide {
    fn at(&self, now: Option<Instant>) -> (f32, f32) {
        (self.a.value(now), self.b.value(now))
    }

    /// Without now (no live display, motion reduced or off) it is there at once.
    fn retarget(&mut self, to: (f32, f32), now: Option<Instant>) {
        self.a.retarget(to.0, now, FOCUS_MS);
        self.b.retarget(to.1, now, FOCUS_MS);
    }

    fn jump(to: (f32, f32)) -> Glide {
        let mut g = Glide::default();
        g.a.jump(to.0);
        g.b.jump(to.1);
        g
    }

    fn running(&self, now: Option<Instant>) -> bool {
        self.a.running(now) || self.b.running(now)
    }

    /// Where it is while it moves; None once it is there.
    fn moving(&self, now: Option<Instant>) -> Option<(f32, f32)> {
        self.running(now).then(|| self.at(now))
    }
}

/// A key for what a highlight glides over: a new one starts it in place.
fn key_of(what: &impl Hash) -> u64 {
    let mut h = std::collections::hash_map::DefaultHasher::new();
    what.hash(&mut h);
    h.finish()
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
    /// The list's highlight sliding from row to row (its top and height),
    /// and whether the focus moved in the frame being laid out.
    bar: Glide,
    moved: bool,
    /// The end of the list (the frame's `bump`): the last one taken, and
    /// since when the highlight is nudged which way.
    bumped: u64,
    bump: Option<(Instant, (i32, i32))>,
    /// The focused row's (or menu entry's) text cut with an ellipsis,
    /// scrolling into view; whether the frame being drawn has one.
    marquee: Option<Marquee>,
    marqueed: bool,
    /// The time on a live display, where scrolling eases (140 ms) and the
    /// focus highlight slides over (100 ms); None (snapshots) draws the end.
    pub now: Option<Instant>,
    /// The body's geometry as last laid out, whether the shell has it, and
    /// the number of the frame it is of (laid); frame: the number of the
    /// frame being laid out (the frames and patches received, as "drawn"
    /// counts them); every_frame: each frame's layout is sent, though only
    /// its number changed (feature "touch": the selftest's touch steps aim
    /// with the layout of the frame on screen).
    layout: Option<serde_json::Value>,
    layout_sent: bool,
    laid: u64,
    pub frame: u64,
    pub every_frame: bool,
    /// Where the last frame drew its body, for the pointer.
    drawn: Drawn,
    /// The notification banner and the toast sliding in and out, what
    /// they show while they leave, and each drawn once to slide.
    banner: Slide,
    banner_lines: Vec<String>,
    toast: Slide,
    toast_text: String,
    sliding: Vec<(u64, Rc<Pixmap>)>,
    /// The focused field (what is typed in it, and where) and since when
    /// its cursor blinks; since when a spinner turns; whether the frame
    /// being drawn has either.
    caret: Option<(u64, Instant)>,
    spin: Option<Instant>,
    typed: bool,
    spun: bool,
    /// The highlight gliding over the focused grid's tiles and over each
    /// candidate row, with what it glides over.
    grid: Option<(u64, Glide)>,
    ime: Vec<Option<(u64, Glide)>>,
    /// Since when the battery charges and since when the overlay's phase
    /// runs (which overlay); whether the frame being drawn has either.
    charge: Option<Instant>,
    phase: Option<(u64, Instant)>,
    charged: bool,
    phased: bool,
}

/// A marquee's clock (37-motion.md §3): what has the focus (a key of the
/// row and its text) and since when, how many panel pixels its text
/// scrolls and how long one takes.
#[derive(Clone, Copy)]
struct Marquee {
    key: u64,
    since: Instant,
    run: u32,
    step: Duration,
}

impl State {
    /// How long what key names (the focused row and its text) has had the
    /// focus, on a live display with motion on (now); a new key starts
    /// the clock again. In snapshots None: the text shows cut.
    fn marquee_clock(&mut self, key: u64, now: Option<Instant>) -> Option<Duration> {
        self.marqueed = true;
        let now = now?;
        let since = match self.marquee {
            Some(m) if m.key == key => m.since,
            _ => now,
        };
        self.marquee = Some(Marquee { key, since, run: 0, step: Duration::ZERO });
        Some(now.saturating_duration_since(since))
    }

    /// After drawing with the clock of key: how far that text scrolls
    /// (cv.marquee_run), for next_tick.
    fn marquee_drawn(&mut self, key: u64, cv: &mut Canvas) {
        if cv.marquee.take().is_none() {
            return;
        }
        if let Some(m) = self.marquee.as_mut().filter(|m| m.key == key) {
            (m.run, m.step) = (cv.marquee_run, marquee_step(cv.s));
        }
        cv.marquee_run = 0;
    }

    /// Where the bump nudges the highlight now (logical pixels, whole
    /// panel pixels at scale s); None once it is back.
    fn nudge(&self, s: f32) -> Option<(f32, f32)> {
        let (t0, (dx, dy)) = self.bump?;
        let ms = self.now?.saturating_duration_since(t0).as_secs_f32() * 1000.0;
        (ms < crate::motion::BUMP_MS).then(|| {
            let d = (crate::motion::nudge(ms) * s).round() / s;
            (dx as f32 * d, dy as f32 * d)
        })
    }
}

/// How long a marquee takes to scroll a panel pixel at scale s.
fn marquee_step(s: f32) -> Duration {
    Duration::from_nanos((1e9 / (crate::motion::MARQUEE_SPEED * s.max(0.1)) as f64).round() as u64)
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
    /// A menu's or dialog's panel (x, y, w, h), its scrim's alpha, and
    /// whether it is a menu (rising from behind the soft keys).
    overlay: Option<((f32, f32, f32, f32), f32, bool)>,
    /// What is drawn besides the body, for a touch: the frame's title bar,
    /// the standby or boot screen, the overlay's kind and what in it a
    /// touch picks (an entry, a choice, a tile: where it is drawn).
    title: bool,
    idle: bool,
    boot: bool,
    overlay_kind: String,
    picks: Vec<Spot>,
    /// The on-screen keypad's keys: each one's cell (x, y, w, h, the gaps
    /// around the key in) and id.
    keys: Vec<((f32, f32, f32, f32), String)>,
}

/// Where an overlay's entry, choice or tile is drawn (x, y, w, h), and which.
type Spot = ((f32, f32, f32, f32), Pick);

/// What a touch picks in an overlay, by its index in the frame's overlay:
/// an entry of `items` (a menu's, the symbols), a button of `choices`, a
/// tile of `tiles` (a picker's).
#[derive(Debug, PartialEq, Clone, Copy)]
pub enum Pick {
    Item(usize),
    Choice(usize),
    Tile(usize),
}

/// What a touch is on (the renderer protocol's `hit`, 23-renderer-protocol.md §3).
#[derive(Debug, PartialEq)]
pub enum Hit {
    /// A soft key: 0, 1, 2 from left to right.
    Soft(usize),
    Status,
    Title,
    /// A candidate row of the input method, from the top.
    Ime(usize),
    /// A key of the on-screen keypad, by its id.
    Key(String),
    /// The body of the frame for this screen, on a node (and in a grid, its
    /// tile) or between them.
    Body(String, Option<(usize, usize)>),
    /// An overlay of this kind, on what it picks or elsewhere on it.
    Overlay(String, Option<Pick>),
    /// The dimmed screen around a menu or dialog of this kind.
    Outside(String),
    Idle,
    Boot,
}

impl Hit {
    pub fn json(&self) -> serde_json::Value {
        use serde_json::json;
        match self {
            Hit::Soft(i) => json!({"area": "soft", "soft": i}),
            Hit::Status => json!({"area": "status"}),
            Hit::Title => json!({"area": "title"}),
            Hit::Ime(r) => json!({"area": "ime", "row": r}),
            Hit::Key(id) => json!({"area": "keypad", "key": id}),
            Hit::Body(screen, None) => json!({"area": "body", "screen": screen}),
            Hit::Body(screen, Some((node, tile))) => json!({"area": "body", "screen": screen, "node": node, "tile": tile}),
            Hit::Overlay(kind, pick) => {
                let mut v = json!({"area": "overlay", "kind": kind});
                match pick {
                    Some(Pick::Item(i)) => v["item"] = json!(i),
                    Some(Pick::Choice(i)) => v["choice"] = json!(i),
                    Some(Pick::Tile(i)) => v["tile"] = json!(i),
                    None => {}
                }
                v
            }
            Hit::Outside(kind) => json!({"area": "outside", "kind": kind}),
            Hit::Idle => json!({"area": "idle"}),
            Hit::Boot => json!({"area": "boot"}),
        }
    }
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
    match node_at(d, x, y) {
        Some((i, k)) => Click::Node(d.screen.clone(), i, k),
        None => Click::Nothing,
    }
}

/// The body node (and in a grid, its tile) drawn at (x, y), if any.
fn node_at(d: &Drawn, x: f32, y: f32) -> Option<(usize, usize)> {
    if y < d.top || y >= d.bottom {
        return None;
    }
    for (i, &(ny, nh)) in d.nodes.iter().enumerate() {
        if y < ny || y >= ny + nh {
            continue;
        }
        let Some(Some((cols, th, count))) = d.grids.get(i).copied() else {
            return Some((i, 0));
        };
        let col = (((x - d.x) / (d.w / cols as f32)).max(0.0) as usize).min(cols - 1);
        let k = ((y - ny) / th) as usize * cols + col;
        return (k < count).then_some((i, k));
    }
    None
}

/// What a touch at (x, y) (logical pixels) is on, in the frame last drawn.
/// Over a menu or dialog: what it picks there, its panel, the soft keys
/// under its scrim, or the screen around it; a full-screen overlay (the
/// viewfinder, a picker, an incoming call) covers the soft keys too.
pub fn hit(st: &State, x: f32, y: f32) -> Hit {
    let d = &st.drawn;
    let inside = |&(px, py, pw, ph): &(f32, f32, f32, f32)| x >= px && x < px + pw && y >= py && y < py + ph;
    let soft = || {
        Hit::Soft(match x {
            x if x < d.width / 3.0 => 0,
            x if x < d.width * 2.0 / 3.0 => 1,
            _ => 2,
        })
    };
    if d.boot {
        return Hit::Boot;
    }
    if !d.overlay_kind.is_empty() {
        let kind = d.overlay_kind.clone();
        if let Some((_, p)) = d.picks.iter().find(|(r, _)| inside(r)) {
            return Hit::Overlay(kind, Some(*p));
        }
        return match d.overlay {
            Some((panel, _, _)) if inside(&panel) => Hit::Overlay(kind, None),
            _ if y < STATUS_H => Hit::Status,
            Some(_) if y >= d.soft => soft(),
            Some(_) => Hit::Outside(kind),
            None => Hit::Overlay(kind, None),
        };
    }
    if y >= d.soft {
        return soft();
    }
    if y < STATUS_H {
        return Hit::Status;
    }
    if d.idle {
        return Hit::Idle;
    }
    if d.title && y < STATUS_H + TITLE_H {
        return Hit::Title;
    }
    if let Some((_, id)) = d.keys.iter().find(|(r, _)| inside(r)) {
        return Hit::Key(id.clone());
    }
    if d.bottom > d.top && y >= d.bottom {
        return Hit::Ime(((y - d.bottom) / IME_H) as usize);
    }
    Hit::Body(d.screen.clone(), node_at(d, x, y))
}

/// The pointer: an arrow with its tip at (x, y).
pub fn pointer(cv: &mut Canvas, x: f32, y: f32) {
    const ARROW: [(f32, f32); 7] = [(0.0, 0.0), (0.0, 16.0), (4.0, 12.4), (6.8, 18.6), (9.2, 17.6), (6.5, 11.6), (11.5, 11.6)];
    let pts: Vec<(f32, f32)> = ARROW.iter().map(|(dx, dy)| (x + dx, y + dy)).collect();
    cv.polygon(&pts, Rgb(0xff, 0xff, 0xff), Rgb(0x12, 0x12, 0x12), 1.1);
}

/// The geometry of the frame just laid out ("layout" message, R21), once
/// per change (with every_frame, once per frame): the body area's height,
/// each node's top and height, and the scroll offset (logical pixels).
pub fn take_layout(st: &mut State) -> Option<serde_json::Value> {
    if st.layout_sent {
        return None;
    }
    st.layout_sent = true;
    st.layout.clone()
}

/// The "layout" message of the frame just laid out, when there is one to
/// send: take_layout's geometry with the number of the frame it is of.
pub fn layout_msg(st: &mut State) -> Option<serde_json::Value> {
    let n = st.laid;
    take_layout(st).map(|l| serde_json::json!({"t": "layout", "n": n, "layout": l}))
}

/// Lays the frame out without painting (headless: the shell still learns
/// the geometry of every frame).
pub fn layout(cv: &mut Canvas, f: &Frame, st: &mut State) {
    set_theme(&f.theme, f.contrast);
    crate::canvas::set_bold(f.bold);
    if f.boot.is_some() || f.idle.is_some() {
        return;
    }
    let bottom = body_bottom(cv, f);
    let top = if f.title.is_empty() { STATUS_H } else { STATUS_H + TITLE_H };
    arrange(cv, f, st, top, bottom);
}

/// More frames are due before the motion settles.
pub fn animating(st: &State) -> bool {
    let glides = st.grid.iter().chain(st.ime.iter().flatten()).any(|(_, g)| g.running(st.now));
    let bump = st.nudge(1.0).is_some();
    st.shown.running(st.now) || st.fade.running(st.now) || st.banner.running(st.now) || st.toast.running(st.now) || glides || bump
}

/// How many of `every` have passed from since to now.
fn ticks(since: Instant, now: Instant, every: Duration) -> u32 {
    (now.saturating_duration_since(since).as_nanos() / every.as_nanos()).min(u32::MAX as u128 - 1) as u32
}

/// When the next frame is due after now though nothing moves: the text
/// cursor shows or hides, a spinner's dot steps on, the charging battery
/// fills a step, the overlay's phase steps on, a marquee's text moves a
/// panel pixel or starts or ends its pause (only that little is painted
/// again).
pub fn next_tick(st: &State, now: Instant) -> Option<Instant> {
    let next = |since: Instant, every: Duration| since + every * (ticks(since, now, every) + 1);
    let marquee = |m: Marquee| now + crate::motion::marquee_at(now.saturating_duration_since(m.since), m.run, m.step).1;
    [
        st.caret.map(|(_, t)| next(t, CARET)),
        st.spin.map(|t| next(t, SPIN_STEP)),
        st.charge.map(|t| next(t, CHARGE_STEP)),
        st.phase.map(|(_, t)| next(t, PHASE_STEP)),
        st.marquee.filter(|m| m.run > 0).map(marquee),
    ]
    .into_iter()
    .flatten()
    .min()
}

const CARET: Duration = Duration::from_millis(crate::motion::CARET_MS);
const SPIN_STEP: Duration = Duration::from_millis(crate::motion::SPIN_MS / crate::motion::SPIN_DOTS as u64);
const CHARGE_STEP: Duration = Duration::from_millis(crate::motion::CHARGE_MS);
const PHASE_STEP: Duration = Duration::from_millis(crate::motion::PHASE_MS);

pub fn draw(cv: &mut Canvas, f: &Frame, st: &mut State) {
    crate::canvas::next_frame();
    set_theme(&f.theme, f.contrast);
    crate::canvas::set_bold(f.bold);
    (st.typed, st.spun, st.charged, st.phased, st.marqueed) = (false, false, false, false, false);
    draw_frame(cv, f, st);
    if !st.typed {
        st.caret = None;
    }
    if !st.marqueed {
        st.marquee = None;
    }
    if !st.spun {
        st.spin = None;
    }
    if !st.charged {
        st.charge = None;
    }
    if !st.phased {
        st.phase = None;
    }
}

fn draw_frame(cv: &mut Canvas, f: &Frame, st: &mut State) {
    cv.focus_box = None;
    st.drawn = Drawn {
        width: cv.w,
        soft: cv.h - SOFT_H,
        covered: f.overlay.is_some() || f.boot.is_some() || f.idle.is_some(),
        title: !f.title.is_empty(),
        idle: f.idle.is_some(),
        boot: f.boot.is_some(),
        overlay_kind: f.overlay.as_ref().map(|o| o.kind.clone()).unwrap_or_default(),
        ..Drawn::default()
    };
    cv.clear(pal().bg);
    if let Some(b) = &f.boot {
        boot(cv, b);
        return;
    }
    let mut bottom = body_bottom(cv, f);
    let fill = charge_fill(st, &f.status);
    if let Some(idle) = &f.idle {
        let charging = f.status.charging.then_some((fill, f.status.battery));
        idle_screen(cv, idle, STATUS_H, bottom, charging);
    } else {
        let top = if f.title.is_empty() { STATUS_H } else { STATUS_H + TITLE_H };
        body(cv, f, st, top, bottom);
        if !f.title.is_empty() {
            cv.rect(0.0, STATUS_H, cv.w, TITLE_H, pal().title);
            // A page still to come turns its spinner where the title ends.
            let spin = if f.skeleton { 2.0 * SPIN_R + PAD } else { 0.0 };
            let t = cv.fit(Face::Bold, TITLE, &f.title, cv.w - 2.0 * PAD - spin);
            cv.text(Face::Bold, TITLE, PAD, STATUS_H + 3.5, &t, pal().fg);
            if f.skeleton {
                spinner(cv, st, cv.w - PAD - SPIN_R, STATUS_H + TITLE_H / 2.0, pal().title);
            }
        }
    }
    // Highlights glide unless motion is off, as a list's focus does; banners
    // and the toast slide only with standard motion.
    let glide = if f.motion != "off" { st.now } else { None };
    let now = if f.motion.is_empty() { st.now } else { None };
    st.ime.resize(f.ime.len(), None);
    for (i, row) in f.ime.iter().enumerate() {
        ime_row(cv, row, bottom, &mut st.ime[i], i, glide);
        bottom += IME_H;
    }
    if let Some(k) = &f.keypad {
        st.drawn.keys = keypad(cv, k);
    }
    soft(cv, &f.soft);
    cv.top = true;
    status(cv, &f.status, fill);
    if !f.banner.is_empty() {
        st.banner_lines.clone_from(&f.banner);
    }
    st.banner.show(!f.banner.is_empty(), now);
    let p = st.banner.value(now);
    if p >= 1.0 {
        banner(cv, &st.banner_lines);
    } else if p > 0.0 {
        // Down from behind the status bar.
        let lines = st.banner_lines.clone();
        let from = banner_rect(cv, lines.len());
        let top = (STATUS_H * cv.s).ceil() as i32;
        let dy = -((from.y1 - top) as f32 * (1.0 - p)).round() as i32;
        let (pm, key) = sliding(cv, st, &("banner", &lines), |cv| banner(cv, &lines));
        cv.layer(pm, key, from, dy, (top, cv.size().1 as i32));
    }
    if let Some(o) = &f.overlay {
        let p = phase(st, o);
        // The menu's entry chosen scrolls its cut text as a focused row.
        let entry = o.items.iter().position(|it| it.sel && !it.header).filter(|_| o.kind == "menu");
        let key = entry.map(|i| key_of(&("menu", &o.title, i, &o.items[i].text, &o.items[i].key, cv.w.to_bits(), crate::canvas::bold_text())));
        if let Some(k) = key {
            cv.marquee = st.marquee_clock(k, glide);
        }
        st.drawn.overlay = overlay(cv, o, p, &mut st.drawn.picks);
        if let Some(k) = key {
            st.marquee_drawn(k, cv);
        }
    }
    let bottom = body_bottom(cv, f);
    if !f.toast.is_empty() {
        st.toast_text.clone_from(&f.toast);
    }
    st.toast.show(!f.toast.is_empty(), now);
    let p = st.toast.value(now);
    if p >= 1.0 {
        toast(cv, &st.toast_text, bottom - 6.0);
    } else if p > 0.0 {
        // Up from behind the soft keys (or the candidate rows).
        let text = st.toast_text.clone();
        let from = toast_rect(cv, &text, bottom - 6.0);
        let end = (bottom * cv.s).floor() as i32;
        let dy = ((end - from.y0) as f32 * (1.0 - p)).round() as i32;
        let (pm, key) = sliding(cv, st, &("toast", &text, bottom.to_bits()), |cv| toast(cv, &text, bottom - 6.0));
        cv.layer(pm, key, from, dy, (0, end));
    }
}

/// A banner or the toast drawn on its own (transparent) picture, to slide
/// it without drawing it again each step; kept while it moves.
fn sliding(cv: &Canvas, st: &mut State, what: &impl Hash, draw: impl FnOnce(&mut Canvas)) -> (Rc<Pixmap>, u64) {
    let mut h = std::collections::hash_map::DefaultHasher::new();
    what.hash(&mut h);
    (cv.size(), cv.s.to_bits(), format!("{:?}", pal().sheet), format!("{:?}", pal().fg), crate::canvas::bold_text()).hash(&mut h);
    let key = h.finish();
    if let Some((_, pm)) = st.sliding.iter().find(|(k, _)| *k == key) {
        return (pm.clone(), key);
    }
    let (w, ht) = cv.size();
    let mut lc = Canvas::new(w, ht, cv.s, cv.fonts);
    draw(&mut lc);
    let pm = Rc::new(lc.pm);
    st.sliding.truncate(1);
    st.sliding.insert(0, (key, pm.clone()));
    (pm, key)
}

fn banner_rect(cv: &Canvas, lines: usize) -> IRect {
    let (s, h) = (cv.s, 8.0 + 14.0 * lines as f32);
    IRect::around(PAD * s, (STATUS_H + 4.0) * s, (cv.w - PAD) * s, (STATUS_H + 4.0 + h) * s)
}

fn toast_rect(cv: &Canvas, text: &str, bottom: f32) -> IRect {
    let t = cv.fit(Face::Sans, SMALL, text, cv.w - 6.0 * PAD);
    let w = cv.measure(Face::Sans, SMALL, &t) + 20.0;
    let s = cv.s;
    IRect::around((cv.w - w) / 2.0 * s, (bottom - 22.0) * s, (cv.w + w) / 2.0 * s, (bottom - 2.0) * s)
}

/// Draws text centred across the screen, cut to fit; its width.
fn centered(cv: &mut Canvas, face: Face, size: f32, y: f32, text: &str, c: Rgb) -> f32 {
    let t = cv.fit(face, size, text, cv.w - 2.0 * PAD);
    let w = cv.measure(face, size, &t);
    cv.text(face, size, (cv.w - w) / 2.0, y, &t, c);
    w
}

/// The battery's fill: its level, and while charging (below full) filling
/// on from there to full in CHARGE_STEPS steps, CHARGE_MS each, and again;
/// at its level in snapshots.
fn charge_fill(st: &mut State, s: &Status) -> f32 {
    let level = s.battery.clamp(0, 100) as f32 / 100.0;
    if !s.charging || s.battery >= 100 {
        return level;
    }
    st.charged = true;
    let steps = crate::motion::CHARGE_STEPS;
    let k = match st.now {
        Some(now) => ticks(*st.charge.get_or_insert(now), now, CHARGE_STEP) % steps,
        None => 0,
    };
    level + (1.0 - level) * k as f32 / (steps - 1) as f32
}

/// A battery k times the status bar's (14 × 8) at (x, y), filled to fill.
#[allow(clippy::too_many_arguments)]
fn battery(cv: &mut Canvas, x: f32, y: f32, k: f32, fill: f32, c: Rgb, low: Rgb, bg: Rgb, level: i32) {
    cv.frame(x, y, 14.0 * k, 8.0 * k, 1.5 * k, 1.0 * k, c, bg);
    cv.rect(x + 14.0 * k, y + 2.5 * k, 1.5 * k, 3.0 * k, c);
    cv.rect(x + 2.0 * k, y + 2.0 * k, 10.0 * k * fill, 4.0 * k, if level <= 15 { low } else { c });
}

/// The charging sign: a lightning bolt h high, its box's top left at (x, y).
fn bolt(cv: &mut Canvas, x: f32, y: f32, h: f32, c: Rgb) {
    const BOLT: [(f32, f32); 6] = [(0.62, 0.0), (0.0, 0.58), (0.42, 0.58), (0.3, 1.0), (1.0, 0.38), (0.55, 0.38)];
    let w = h * 0.55;
    let pts: Vec<(f32, f32)> = BOLT.iter().map(|(px, py)| (x + px * w, y + py * h)).collect();
    cv.polygon(&pts, c, c, 0.2);
}

/// The status bar's mark of calls going over VoLTE.
const VOLTE: &str = "VoLTE";

fn status(cv: &mut Canvas, s: &Status, fill: f32) {
    cv.rect(0.0, 0.0, cv.w, STATUS_H, pal().bar);
    cv.text(Face::Sans, CAPTION, PAD, 2.0, &s.time, pal().fg);
    // Where the clock, or the middle's text, ends.
    let mut taken = PAD + cv.measure(Face::Sans, CAPTION, &s.time);
    if !s.mid.is_empty() {
        let c = match s.mid_kind.as_str() {
            "call" => pal().ok,
            "secure" => pal().trust,
            "mic" => pal().warn,
            _ => pal().fg,
        };
        let w = centered(cv, Face::Bold, CAPTION, 2.0, &s.mid, c);
        taken = taken.max((cv.w + w) / 2.0);
    }
    // Right to left: battery and its percentage, signal bars, network,
    // VoLTE, then status_rest's.
    let mut x = cv.w - PAD - 16.0;
    battery(cv, x, 4.0, 1.0, fill, pal().fg, pal().danger, pal().bar, s.battery);
    if s.charging {
        x -= 6.0;
        bolt(cv, x, 3.0, 9.0, pal().fg);
    }
    if s.percent {
        let t = format!("{}%", s.battery.clamp(0, 100));
        let w = cv.measure(Face::Bold, MICRO, &t);
        x -= w + 2.0;
        cv.text(Face::Bold, MICRO, x, 3.0, &t, pal().fg);
    }
    x -= 16.0;
    for i in 0..4 {
        let h = 3.0 + i as f32 * 2.2;
        let c = if i < s.signal { pal().fg } else { pal().line };
        cv.rect(x + i as f32 * 3.2, 12.0 - h, 2.2, h, c);
    }
    if !s.net.is_empty() {
        x -= cv.measure(Face::Bold, MICRO, &s.net) + 4.0;
        cv.text(Face::Bold, MICRO, x, 3.0, &s.net, pal().fg);
    }
    // The mark is the first to go on a full bar (a narrow screen with
    // text in the middle): drawn only where the rest still clears it.
    if s.volte {
        let at = x - cv.measure(Face::Sans, MICRO, VOLTE) - 4.0;
        if status_rest(cv, s, at, false) >= taken + 4.0 {
            x = at;
            cv.text(Face::Sans, MICRO, x, 3.0, VOLTE, pal().fg);
        }
    }
    status_rest(cv, s, x, true);
}

/// The status bar right to left from x on, after the network: VPN, input
/// method, personal hotspot, sound mode, location in use, padlock, network
/// speed. Where they end; drawn only with draw.
fn status_rest(cv: &mut Canvas, s: &Status, mut x: f32, draw: bool) -> f32 {
    if s.vpn {
        x -= 16.0;
        if draw {
            key(cv, x + 2.0, pal().fg);
        }
    }
    if !s.ime.is_empty() {
        x -= cv.measure(Face::Bold, MICRO, &s.ime) + 4.0;
        if draw {
            cv.text(Face::Bold, MICRO, x, 3.0, &s.ime, pal().fg);
        }
    }
    if s.hotspot {
        x -= 15.0;
        if draw {
            hotspot(cv, x + 6.5, pal().accent);
        }
    }
    match s.sound.as_str() {
        "vibrate" => {
            x -= 16.0;
            if draw {
                vibrate(cv, x + 7.0, pal().fg);
            }
        }
        "silent" => {
            x -= 13.0;
            if draw {
                silent(cv, x + 5.5, pal().fg);
            }
        }
        _ => {}
    }
    if s.location {
        x -= 10.0;
        if draw {
            pin(cv, x + 4.0, pal().accent);
        }
    }
    if s.locked {
        x -= 11.0;
        if draw {
            cv.frame(x + 1.5, 2.5, 6.0, 7.0, 3.0, 1.2, pal().fg, pal().bar);
            cv.rect(x, 6.0, 9.0, 7.0, pal().fg);
        }
    }
    if !s.speed.is_empty() {
        x -= cv.measure(Face::Sans, MICRO, &s.speed) + 4.0;
        if draw {
            cv.text(Face::Sans, MICRO, x, 3.0, &s.speed, pal().fg);
        }
    }
    x
}

// key is the VPN-up icon: a key lying flat, its bow at x.
fn key(cv: &mut Canvas, x: f32, c: Rgb) {
    cv.circle(x + 3.0, 8.0, 3.0, c);
    cv.circle(x + 3.0, 8.0, 1.2, pal().bar);
    cv.rect(x + 5.5, 7.3, 6.0, 1.5, c);
    cv.rect(x + 9.0, 8.8, 1.3, 2.0, c);
    cv.rect(x + 10.8, 8.8, 1.2, 1.5, c);
}

// pin is the location-in-use icon: a map pin centred on cx.
fn pin(cv: &mut Canvas, cx: f32, c: Rgb) {
    cv.circle(cx, 6.0, 3.4, c);
    cv.circle(cx, 6.0, 1.3, pal().bar);
    cv.rect(cx - 2.0, 8.6, 4.0, 1.2, c);
    cv.rect(cx - 1.2, 9.8, 2.4, 1.2, c);
    cv.rect(cx - 0.5, 11.0, 1.0, 1.4, c);
}

// hotspot is the personal hotspot's icon: a mast sending two waves,
// centred on cx.
fn hotspot(cv: &mut Canvas, cx: f32, c: Rgb) {
    for r in [6.0, 3.6] {
        cv.circle(cx, 7.5, r, c);
        cv.circle(cx, 7.5, r - 1.3, pal().bar);
    }
    cv.rect(cx - 6.5, 8.6, 13.0, 5.0, pal().bar);
    cv.circle(cx, 7.5, 1.5, c);
    cv.rect(cx - 0.6, 7.5, 1.2, 5.0, c);
}

// vibrate is the vibrate mode's icon: a phone between two shakes, centred
// on cx.
fn vibrate(cv: &mut Canvas, cx: f32, c: Rgb) {
    cv.frame(cx - 2.5, 2.5, 5.0, 9.5, 1.2, 1.1, c, pal().bar);
    for (dx, h) in [(4.0, 5.0), (6.0, 3.0)] {
        cv.rect(cx - dx - 1.0, 7.25 - h / 2.0, 1.0, h, c);
        cv.rect(cx + dx, 7.25 - h / 2.0, 1.0, h, c);
    }
}

// silent is the silent mode's icon: a bell struck through, centred on cx.
fn silent(cv: &mut Canvas, cx: f32, c: Rgb) {
    cv.circle(cx, 6.5, 3.5, c);
    cv.rect(cx - 3.5, 6.5, 7.0, 3.5, c);
    cv.rect(cx - 4.8, 10.0, 9.6, 1.2, c);
    cv.circle(cx, 12.0, 1.0, c);
    let slash = |w: f32| [(cx - 5.5, 2.0 - w), (cx - 5.5 + w, 2.0 - w), (cx + 5.5, 13.0 + w), (cx + 5.5 - w, 13.0 + w)];
    cv.polygon(&slash(1.6), pal().bar, pal().bar, 0.2);
    cv.polygon(&slash(0.8), c, c, 0.2);
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

/// A row of the on-screen keypad: a fingertip's height on the phone that
/// has one (9.5 mm on the OnePlus 6T's panel).
const KEY_H: f32 = 34.0;

/// Where the body ends: above the soft keys, the on-screen keypad and the
/// candidate rows.
fn body_bottom(cv: &Canvas, f: &Frame) -> f32 {
    cv.h - SOFT_H - f.keypad.as_ref().map_or(0.0, |k| KEY_H * k.rows.len() as f32) - IME_H * f.ime.len() as f32
}

/// Each key of keypad k on a screen w × h, with its cell (x, y, w, h): the
/// rows right above the soft keys, each row's keys sharing its width. A key
/// without an id is a gap: nothing is drawn there, and a touch on it is on
/// the keypad but no key.
type KeyCell<'a> = ((f32, f32, f32, f32), &'a crate::frame::KeypadKey);

fn key_cells(w: f32, h: f32, k: &Keypad) -> Vec<KeyCell<'_>> {
    let top = h - SOFT_H - KEY_H * k.rows.len() as f32;
    let mut cells = Vec::new();
    for (r, row) in k.rows.iter().enumerate() {
        let kw = w / row.len().max(1) as f32;
        for (i, key) in row.iter().enumerate() {
            cells.push(((i as f32 * kw, top + r as f32 * KEY_H, kw, KEY_H), key));
        }
    }
    cells
}

/// The on-screen keypad: each key a rounded key cap in its cell, its label
/// and under it the smaller line (a digit's letters). Where the cells are,
/// for a touch.
fn keypad(cv: &mut Canvas, k: &Keypad) -> Vec<((f32, f32, f32, f32), String)> {
    let h = KEY_H * k.rows.len() as f32;
    let top = cv.h - SOFT_H - h;
    cv.rect(0.0, top, cv.w, h, pal().bar);
    cv.rect(0.0, top, cv.w, 0.5, pal().line);
    let mut cells = Vec::new();
    for ((x, y, w, kh), key) in key_cells(cv.w, cv.h, k) {
        cells.push(((x, y, w, kh), key.id.clone()));
        if key.id.is_empty() {
            continue;
        }
        let (cap, fg) = match key.style.as_str() {
            "ok" => (pal().accent, pal().bg),
            "fn" => (pal().title, pal().fg),
            _ => (pal().card, pal().fg),
        };
        cv.round(x + 2.0, y + 2.0, w - 4.0, kh - 4.0, R_S, cap);
        let size = if key.sub.is_empty() { TITLE } else { HEADLINE };
        let label = cv.fit(Face::Bold, size, &key.label, w - 6.0);
        let lw = cv.measure(Face::Bold, size, &label);
        let ly = if key.sub.is_empty() { y + (kh - size) / 2.0 - 1.5 } else { y + 2.0 };
        cv.text(Face::Bold, size, x + (w - lw) / 2.0, ly, &label, fg);
        if !key.sub.is_empty() {
            let sub = cv.fit(Face::Sans, MICRO, &key.sub, w - 6.0);
            let sw = cv.measure(Face::Sans, MICRO, &sub);
            cv.text(Face::Sans, MICRO, x + (w - sw) / 2.0, y + kh - MICRO - 6.5, &sub, pal().dim);
        }
    }
    cells
}

/// Candidate row i at y. On the active row the highlight glides from
/// candidate to candidate (glide: what it glides over, and how), each
/// candidate's text as light as the highlight covers it meanwhile.
fn ime_row(cv: &mut Canvas, row: &ImeRow, y: f32, glide: &mut Option<(u64, Glide)>, i: usize, now: Option<Instant>) {
    cv.rect(0.0, y, cv.w, IME_H, pal().card);
    let mut cells = Vec::new();
    let mut x = PAD;
    for (i, item) in row.items.iter().enumerate() {
        let label = if row.numbered { format!("{} {}", i + 1, item) } else { item.clone() };
        let w = cv.measure(Face::Sans, BODY, &label) + 8.0;
        if x + w > cv.w - 12.0 {
            break;
        }
        cells.push((x, w, label));
        x += w + 2.0;
    }
    let sel = usize::try_from(row.sel).ok().filter(|_| row.active).and_then(|s| cells.get(s)).map(|&(x, w, _)| (x, w));
    let moving = match sel {
        Some(to) => {
            let key = key_of(&(i, &row.items, row.numbered));
            match glide {
                Some((k, g)) if *k == key => g.retarget(to, now),
                _ => *glide = Some((key, Glide::jump(to))),
            }
            glide.as_ref().and_then(|(_, g)| g.moving(now))
        }
        None => {
            *glide = None;
            None
        }
    };
    if let Some((hx, hw)) = moving.or(sel) {
        cv.round(hx, y + 2.0, hw, IME_H - 4.0, R_S, pal().accent);
    }
    for (k, (x, w, label)) in cells.iter().enumerate() {
        let c = match moving {
            Some((hx, hw)) => pal().fg.mix(pal().bg, ((hx + hw).min(x + w) - hx.max(*x)).max(0.0) / w),
            None if sel.is_some() && k as i32 == row.sel => pal().bg,
            None if row.active => pal().fg,
            None => pal().dim,
        };
        cv.text(Face::Sans, BODY, x + 4.0, y + 2.5, label, c);
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

/// The standby screen; charging (the battery's fill and level), a big
/// battery under the date fills up as the status bar's does (the screen a
/// charger lights, 28-battery.md §1).
fn idle_screen(cv: &mut Canvas, idle: &Idle, top: f32, bottom: f32, charging: Option<(f32, i32)>) {
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
    if let Some((fill, level)) = charging {
        // Bolt, battery (twice the status bar's) and its percentage, centred.
        let t = format!("{}%", level.clamp(0, 100));
        let tw = cv.measure(Face::Bold, SMALL, &t);
        // Inside it, the gradient's band at its middle.
        let bg = if wallpaper.is_some() { Rgb(0, 0, 0) } else { pal().idle.0.mix(pal().idle.1, ((y + 8.0 - top) / band).floor() / steps as f32) };
        let x = (col_w - (9.0 + 31.0 + 6.0 + tw)) / 2.0;
        bolt(cv, x, y + 1.0, 14.0, fg);
        battery(cv, x + 9.0, y, 2.0, fill, fg, pal().danger, bg, level);
        cv.text(Face::Bold, SMALL, x + 46.0, y + 0.5, &t, fg);
        y += 26.0;
    }
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
    st.moved = false;
    if st.screen != f.screen {
        st.screen = f.screen.clone();
        st.offset = 0.0;
        st.shown.jump(0.0);
        (st.focus, st.old_focus) = (f.focus, -1);
        st.fade.jump(1.0);
        st.bump = None;
    }
    if f.focus != st.focus {
        (st.old_focus, st.focus) = (st.focus, f.focus);
        st.fade.jump(0.0);
        st.fade.retarget(1.0, st.now, FOCUS_MS);
        st.moved = true;
        st.bump = None;
    }
    let max_off = (total - area).max(0.0);
    let focus = f.focus;
    if let Some(o) = f.scroll.offset.filter(|o| o.is_finite()) {
        st.offset = o;
    } else if focus >= 0 && (focus as usize) < heights.len() {
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
    let mut geometry = serde_json::json!({"screen": f.screen, "area": area, "offset": st.offset, "top": top + GAP + lead(f, area, total),
        "left": x, "width": w, "content": total, "nodes": nodes});
    if let Some(k) = &f.keypad {
        let keys: Vec<serde_json::Value> = key_cells(cv.w, cv.h, k)
            .into_iter()
            .filter(|(_, key)| !key.id.is_empty())
            .map(|((kx, ky, kw, kh), key)| serde_json::json!({"id": key.id, "x": kx, "y": ky, "w": kw, "h": kh}))
            .collect();
        geometry["keys"] = serde_json::json!(keys);
    }
    if st.layout.as_ref() != Some(&geometry) || st.every_frame && st.laid != st.frame {
        (st.layout, st.layout_sent, st.laid) = (Some(geometry), false, st.frame);
    }
    Arranged { heights, pos, total, max_off }
}

/// Where content that fits the body area starts in it: around the optical
/// centre (a little below the middle) or against the soft keys, as the
/// screen asks.
fn lead(f: &Frame, area: f32, total: f32) -> f32 {
    let spare = (area - total).max(0.0);
    match f.layout.as_str() {
        "center" => spare * 0.55,
        "bottom" => spare,
        _ => 0.0,
    }
}

fn body(cv: &mut Canvas, f: &Frame, st: &mut State, top: f32, bottom: f32) {
    let (x, w) = (PAD, cv.w - 2.0 * PAD);
    let area = bottom - top - 2.0 * GAP;
    let Arranged { heights, pos, total, max_off } = arrange(cv, f, st, top, bottom);
    // Under a finger the body follows it, with no glide behind.
    if f.scroll.offset.is_some() {
        st.shown.jump(st.offset);
    } else {
        st.shown.retarget(st.offset, st.now, SCROLL_MS);
    }
    // The body scrolls by whole panel rows: drawn once, it is copied up
    // or down (layers.rs).
    let s = cv.s;
    let rows_of = |off: f32| (off.clamp(0.0, max_off) * s).round() as i32;
    let dy = rows_of(st.shown.value(st.now));
    let offset = dy as f32 / s;
    let fade = st.fade.value(st.now);
    let lead = lead(f, area, total);
    st.drawn.screen = f.screen.clone();
    (st.drawn.top, st.drawn.bottom, st.drawn.x, st.drawn.w) = (top, bottom, x, w);
    // What the last scroll goes over is drawn (and kept) from its start:
    // its steps paint nothing of the body. A long jump keeps only what
    // shows.
    let rows = ((top * s).floor() as i32, (bottom * s).ceil() as i32);
    let (a, b) = st.shown.ends();
    let (lo, hi) = (rows_of(a.min(b)).min(dy), rows_of(a.max(b)).max(dy));
    let (lo, hi) = if hi - lo > 2 * (rows.1 - rows.0) { (dy, dy) } else { (lo, hi) };
    let keep = (rows.0 + lo, rows.1 + hi);
    let (k0, k1) = (keep.0 as f32 / s, keep.1 as f32 / s);
    cv.caret = caret(st, f);
    let live = if f.motion != "off" { st.now } else { None };
    if let Some(b) = f.bump.as_ref().filter(|b| b.n != st.bumped) {
        st.bumped = b.n;
        // One knock at a time: a held key knocks again once it is back.
        if st.nudge(s).is_none() {
            st.bump = live.map(|t| (t, (b.dx.signum(), b.dy.signum())));
        }
    }
    let nudge = st.nudge(s);
    let glide = nudged(f, w, grid_glide(st, f, live), nudge);
    cv.begin_body();
    // Between two rows the highlight slides from one to the other; rows
    // draw no highlight of their own meanwhile (focus_mix < 0).
    let row = |i: i32| (i >= 0 && (i as usize) < f.body.len() && f.body[i as usize].kind == "item").then(|| (top + GAP + lead + pos[i as usize], heights[i as usize]));
    if st.moved {
        if let (Some(from), Some(to)) = (row(st.old_focus), row(st.focus)) {
            if !st.bar.running(st.now) {
                st.bar = Glide::jump(from);
            }
            st.bar.retarget(to, st.now);
        }
    }
    let slide = (fade < 1.0).then(|| row(st.old_focus).zip(row(st.focus))).flatten().map(|_| st.bar.at(st.now));
    // A bump nudges the sliding highlight, or the row's own (cv.nudge): one
    // drawn before the rows for it would put the list in another order,
    // and every row after it would be painted again (layers.rs).
    let (nx, ny) = nudge.filter(|_| fade >= 1.0 || slide.is_some()).unwrap_or_default();
    if let Some((y, h)) = slide {
        cv.round(x - 2.0 + nx, y + ny, w + 4.0, h, R_M, pal().focus);
        cv.round(x - 2.0 + nx, y + ny + 4.0, 2.5, h - 8.0, 1.25, pal().accent);
    }
    for (i, n) in f.body.iter().enumerate() {
        let y = top + GAP + lead + pos[i];
        st.drawn.nodes.push((y - offset, heights[i]));
        st.drawn.grids.push((n.kind == "grid").then(|| {
            let cols = n.columns.max(1) as usize;
            (cols, tile_height(&n.tiles, w / cols as f32), n.tiles.len())
        }));
        if i as i32 == f.focus {
            cv.focus_box = Some(match st.drawn.grids[i] {
                Some((cols, th, _)) if n.sel >= 0 => {
                    let (col, row) = (n.sel as usize % cols, n.sel as usize / cols);
                    let tw = w / cols as f32;
                    (x + col as f32 * tw, y - offset + row as f32 * th, tw, th)
                }
                _ => (x, y - offset, w, heights[i]),
            });
        }
        if y + heights[i] < k0 || y > k1 {
            continue;
        }
        if slide.is_none() && i as i32 == st.old_focus && fade < 1.0 && n.kind == "item" {
            cv.round(x - 2.0, y, w + 4.0, heights[i], R_M, pal().bg.mix(pal().focus, 1.0 - fade));
        }
        cv.focus_mix = match (i as i32 == st.focus, slide.is_some()) {
            (true, true) => -1.0,
            (true, false) => fade,
            _ => 1.0,
        };
        cv.nudge = if i as i32 == st.focus && slide.is_none() { (nx, ny) } else { (0.0, 0.0) };
        match glide {
            Some((g, at)) if g == i => {
                grid(cv, &n.tiles, n.columns, n.sel, x, y, w, true, Some(at));
            }
            _ => {
                // The focused row's text cut with an ellipsis scrolls into
                // view, unless a menu or dialog is over it.
                let marquee = (i as i32 == f.focus && n.kind == "item" && f.overlay.is_none()).then(|| {
                    let icon = !n.icon.is_empty() || !n.icon_image.is_empty() || n.image.is_some();
                    key_of(&(&f.screen, i, &n.title, &n.text, &n.subtitle, &n.trailing, &n.badge, icon, cv.w.to_bits(), crate::canvas::bold_text()))
                });
                if let Some(k) = marquee {
                    cv.marquee = st.marquee_clock(k, live);
                }
                node(cv, n, x, y, w, area, true);
                if let Some(k) = marquee {
                    st.marquee_drawn(k, cv);
                }
            }
        }
    }
    (cv.focus_mix, cv.nudge) = (1.0, (0.0, 0.0));
    cv.end_body(dy, rows, keep, pal().bg);
    // Content scrolled under the bars is covered by them; under the soft
    // keys and candidate rows too.
    cv.rect(0.0, bottom, cv.w, cv.h - bottom, pal().bar);
    if max_off > 0.0 {
        scroll_bar(cv, top, bottom, area / total, offset / max_off);
    }
    if f.skeleton {
        skeleton(cv, x, top + GAP, w, bottom);
    } else if !f.loading.is_empty() {
        let y = (top + bottom) / 2.0 - 8.0;
        spinner(cv, st, cv.w / 2.0, y - 2.0 * SPIN_R, pal().bg);
        centered(cv, Face::Sans, BODY, y, &f.loading, pal().dim);
    }
}

/// Says there is more above or below: a bar at the right edge between top
/// and bottom, as long as the part shown is of the whole (at least 12),
/// as far down as at (0 at the top, 1 at the end).
fn scroll_bar(cv: &mut Canvas, top: f32, bottom: f32, part: f32, at: f32) {
    let track = bottom - top - 4.0;
    let len = (part * track).max(12.0);
    let y = top + 2.0 + (track - len) * at;
    cv.round(cv.w - 3.0, y, 2.0, len, 1.0, pal().line);
}

/// Whether the focused field shows its text cursor: on a live display it
/// blinks (CARET_MS on, CARET_MS off), shown again at once when what is
/// typed or where changes; under a menu or dialog, and in snapshots, it
/// is always shown.
fn caret(st: &mut State, f: &Frame) -> bool {
    let field = usize::try_from(f.focus).ok().and_then(|i| f.body.get(i)).filter(|n| n.kind == "input" && n.focused && f.overlay.is_none());
    let Some(n) = field else { return true };
    st.typed = true;
    let Some(now) = st.now else { return true };
    let mut h = std::collections::hash_map::DefaultHasher::new();
    (&f.screen, f.focus, &n.value, &n.preedit, n.caret_back, &n.selection).hash(&mut h);
    let key = h.finish();
    let since = match st.caret {
        Some((k, t)) if k == key => t,
        _ => {
            st.caret = Some((key, now));
            now
        }
    };
    ticks(since, now, CARET).is_multiple_of(2)
}

/// A spinner's radius (logical pixels).
const SPIN_R: f32 = 5.0;

/// A spinner centred on (cx, cy) over bg: SPIN_DOTS dots in a ring, the
/// brightest one stepping on round it, a turn in SPIN_MS (still in
/// snapshots).
fn spinner(cv: &mut Canvas, st: &mut State, cx: f32, cy: f32, bg: Rgb) {
    let dots = crate::motion::SPIN_DOTS;
    let head = match st.now {
        Some(now) => ticks(*st.spin.get_or_insert(now), now, SPIN_STEP) % dots,
        None => 0,
    };
    st.spun = true;
    for i in 0..dots {
        let a = std::f32::consts::TAU * i as f32 / dots as f32 - std::f32::consts::FRAC_PI_2;
        let behind = (head + dots - i) % dots;
        let c = bg.mix(pal().fg, 1.0 - 0.8 * behind as f32 / dots as f32);
        cv.circle(cx + (SPIN_R - 1.3) * a.cos(), cy + (SPIN_R - 1.3) * a.sin(), 1.3, c);
    }
}

/// An app's page before it has sent one (37-motion.md §3): under its
/// title, the shapes of list rows, so opening it shows at once where its
/// content will be.
fn skeleton(cv: &mut Canvas, x: f32, top: f32, w: f32, bottom: f32) {
    const SHARE: [f32; 6] = [0.62, 0.45, 0.7, 0.52, 0.38, 0.66];
    let mut y = top;
    for i in 0.. {
        if y + 36.0 > bottom {
            break;
        }
        cv.round(x + 5.0, y + 8.0, 20.0, 20.0, R_S, pal().line);
        cv.round(x + 31.0, y + 8.0, (w - 37.0) * SHARE[i % 6], 8.0, 4.0, pal().line);
        cv.round(x + 31.0, y + 21.0, (w - 37.0) * SHARE[(i + 3) % 6] * 0.6, 6.0, 3.0, pal().line);
        y += 36.0 + GAP;
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
        "grid" => grid(cv, &n.tiles, n.columns, n.sel, x, y, w, draw, None),
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
    // A picture of the item's (a contact's photo) takes the icon's place.
    let photo = n.image.as_ref().filter(|i| i.w > 0 && i.h > 0);
    let has_icon = photo.is_some() || !n.icon.is_empty() || !n.icon_image.is_empty();
    let h = if n.subtitle.is_empty() { 26.0 } else { 36.0 };
    if !draw {
        return h;
    }
    if n.focused && cv.focus_mix >= 0.0 {
        let (nx, ny) = cv.nudge;
        cv.round(x - 2.0 + nx, y + ny, w + 4.0, h, R_M, pal().bg.mix(pal().focus, cv.focus_mix));
        cv.round(x - 2.0 + nx, y + ny + 4.0, 2.5, h - 8.0, 1.25, pal().bg.mix(pal().accent, cv.focus_mix));
    }
    let mut tx = x + 6.0;
    if has_icon {
        match photo {
            Some(img) => {
                cv.image(img, x + 5.0, y + (h - 20.0) / 2.0, 20.0, 20.0);
            }
            None => glyph_icon(cv, &n.icon_image, &n.icon, x + 5.0, y + (h - 20.0) / 2.0, 20.0, true),
        }
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
    let (ty, tw, sw) = (y + if n.subtitle.is_empty() { 5.0 } else { 3.0 }, right - tx, x + w - 4.0 - tx);
    let hid = [hidden(cv, face, TITLE, title, tw), hidden(cv, Face::Sans, CAPTION, &n.subtitle, sw)];
    let k = scrolled(cv, hid[0].max(hid[1]));
    if k.min(hid[0]) == 0 {
        let t = cv.fit(face, TITLE, title, tw);
        cv.text(face, TITLE, tx, ty, &t, pal().fg);
    } else {
        scroll_line(cv, face, TITLE, title, (tx, ty, tw), pal().fg, k, hid[0], (y, y + h));
    }
    if !n.subtitle.is_empty() {
        if k.min(hid[1]) == 0 {
            let s = cv.fit(Face::Sans, CAPTION, &n.subtitle, sw);
            cv.text(Face::Sans, CAPTION, tx, y + 20.0, &s, pal().dim);
        } else {
            scroll_line(cv, Face::Sans, CAPTION, &n.subtitle, (tx, y + 20.0, sw), pal().dim, k, hid[1], (y, y + h));
        }
    }
    h
}

/// How many panel pixels text is wider than w where the marquee's clock
/// runs (cv.marquee): how far it scrolls to show its end.
fn hidden(cv: &Canvas, face: Face, size: f32, text: &str, w: f32) -> u32 {
    if cv.marquee.is_none() || text.is_empty() {
        return 0;
    }
    let over = cv.measure(face, size, text) - w;
    if over > 0.0 { (over * cv.s).ceil() as u32 } else { 0 }
}

/// How many panel pixels a marquee `run` pixels long has scrolled by the
/// canvas's clock (0: its text shows cut, as without one); the run is
/// kept for next_tick.
fn scrolled(cv: &mut Canvas, run: u32) -> u32 {
    let Some(e) = cv.marquee else { return 0 };
    cv.marquee_run = cv.marquee_run.max(run);
    if run == 0 {
        return 0;
    }
    crate::motion::marquee_at(e, run, marquee_step(cv.s)).0
}

/// A line of text at (x, y) shown in w, k of its `hid` hidden panel pixels
/// scrolled into view: clipped to w and rows, its edges fading where text
/// is hidden beyond them (fully MARQUEE_FADE in).
#[allow(clippy::too_many_arguments)]
fn scroll_line(cv: &mut Canvas, face: Face, size: f32, text: &str, (x, y, w): (f32, f32, f32), c: Rgb, k: u32, hid: u32, rows: (f32, f32)) {
    let (s, k, fade) = (cv.s, k.min(hid), crate::motion::MARQUEE_FADE);
    let edges = ((k as f32 / s).min(fade), ((hid - k) as f32 / s).min(fade));
    cv.marquee_text(face, size, x - k as f32 / s, y, text, c, (x, rows.0, x + w, rows.1), edges);
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
    let value: Vec<char> = if n.secret { vec!['•'; n.value.chars().count()] } else { n.value.chars().collect() };
    if value.is_empty() && n.preedit.is_empty() {
        let p = cv.fit(Face::Sans, BODY, &n.placeholder, w - 14.0);
        cv.text(Face::Sans, BODY, tx, by + 5.5, &p, pal().dim);
    } else {
        // Keep the caret in view: it is where typing happens.
        let caret = value.len().saturating_sub(n.caret_back);
        let part = |a: usize, b: usize| value[a.min(b)..b].iter().collect::<String>();
        let mut start = 0;
        while start < caret && cv.measure(Face::Sans, BODY, &part(start, caret)) + cv.measure(Face::Sans, BODY, &n.preedit) > w - 18.0 {
            start += 1;
        }
        if let [from, to] = n.selection[..] {
            let (a, b) = (from.clamp(start, value.len()), to.clamp(start, value.len()));
            if a < b {
                let sx = tx + cv.measure(Face::Sans, BODY, &part(start, a));
                let sw = cv.measure(Face::Sans, BODY, &part(a, b)).min(x + w - 5.0 - sx);
                cv.round_a(sx, by + 4.0, sw, 18.0, 3.0, pal().accent, 0.4);
            }
        }
        tx += cv.text(Face::Sans, BODY, tx, by + 5.5, &part(start, caret), pal().fg);
        if !n.preedit.is_empty() {
            let pw = cv.text(Face::Sans, BODY, tx, by + 5.5, &n.preedit, pal().accent);
            cv.rect(tx, by + 21.0, pw, 1.0, pal().accent);
            tx += pw;
        }
        if caret < value.len() {
            let rest = cv.fit(Face::Sans, BODY, &part(caret, value.len()), x + w - 7.0 - tx);
            cv.text(Face::Sans, BODY, tx, by + 5.5, &rest, pal().fg);
        }
    }
    if n.focused && cv.caret {
        cv.rect(tx + 1.0, by + 6.0, 1.2, 14.0, pal().accent);
    }
    lh + 26.0
}

fn picture(cv: &mut Canvas, n: &Node, x: f32, y: f32, w: f32, area: f32, draw: bool) -> f32 {
    let max_h = if n.fill { area } else { (area * 0.72).max(60.0) };
    let label_h = if n.label.is_empty() { 0.0 } else { 15.0 };
    if let Some(img) = n.image.as_ref().filter(|i| !i.code.is_empty()) {
        let h = code(cv, img, x, y, w, max_h, draw);
        if draw && !n.label.is_empty() {
            let t = cv.fit(Face::Sans, CAPTION, &n.label, w);
            cv.text(Face::Sans, CAPTION, x, y + h + 2.0, &t, pal().dim);
        }
        return h + label_h + 2.0;
    }
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

/// A code the system draws (`picture`) at a whole number of panel pixels a
/// module (`Canvas::code_box`): a reader takes modules of uneven widths for
/// others. Where one pixel a module does not fit, the Shell's note says so,
/// with a barcode's number under it; returns the height used.
#[allow(clippy::too_many_arguments)]
fn code(cv: &mut Canvas, img: &Image, x: f32, y: f32, w: f32, max_h: f32, draw: bool) -> f32 {
    if let Some(b) = cv.code_box(img, x, y, w, max_h) {
        let h = b.1 + b.3 - y;
        if draw {
            cv.rect(x, y, w, h, pal().bar);
            cv.image_in(img, b);
        }
        return h;
    }
    let (lh, nh) = (SMALL * 1.5, BODY * 1.7);
    let note = cv.wrap(Face::Sans, SMALL, &img.code_note, w - 12.0);
    // Every character of the number, which a till takes typed in.
    let number = if img.code_text.is_empty() { Vec::new() } else { cv.wrap(Face::Bold, BODY, &img.code_text, w - 12.0) };
    let ny = y + 5.0 + lh * note.len() as f32;
    let h = ny - y + nh * number.len() as f32 + 5.0;
    if draw {
        cv.round(x, y, w, h, R_M, pal().card);
        for (i, l) in note.iter().enumerate() {
            let lw = cv.measure(Face::Sans, SMALL, l);
            cv.text(Face::Sans, SMALL, x + (w - lw) / 2.0, y + 5.0 + lh * i as f32 + (lh - SMALL * 1.45) / 2.0, l, pal().dim);
        }
        if img.secure && cv.mask_secure && !number.is_empty() {
            cv.secure_mask(x + 6.0, ny, w - 12.0, nh * number.len() as f32);
        } else {
            for (i, l) in number.iter().enumerate() {
                let lw = cv.measure(Face::Bold, BODY, l);
                cv.text(Face::Bold, BODY, x + (w - lw) / 2.0, ny + nh * i as f32 + (nh - BODY * 1.45) / 2.0, l, pal().fg);
            }
        }
    }
    h
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

/// Keys (tiles without an icon, like the calculator's) are short, taller
/// with a second line (a calendar's days); app tiles square.
fn tile_height(tiles: &[Tile], tw: f32) -> f32 {
    if tiles.iter().all(|t| t.icon.is_empty() && t.image.is_empty() && t.pixels.is_none()) {
        if tiles.iter().any(|t| !t.subtitle.is_empty()) {
            (tw * 0.86).max(28.0)
        } else {
            tw * 0.62
        }
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

/// The tile a grid's highlight is on.
fn selected_in(tiles: &[Tile], sel: i32) -> Option<usize> {
    usize::try_from(sel).ok().filter(|&s| s < tiles.len()).or_else(|| tiles.iter().position(|t| t.focused))
}

fn selected(n: &Node) -> Option<usize> {
    selected_in(&n.tiles, n.sel)
}

/// The highlight on the focused grid glides from tile to tile: the grid's
/// node and where the highlight is (column, row) while it moves.
fn grid_glide(st: &mut State, f: &Frame, now: Option<Instant>) -> Option<(usize, (f32, f32))> {
    let focused = usize::try_from(f.focus).ok().filter(|&i| f.body.get(i).is_some_and(|n| n.kind == "grid"));
    let Some((i, k)) = focused.and_then(|i| selected(&f.body[i]).map(|k| (i, k))) else {
        st.grid = None;
        return None;
    };
    let n = &f.body[i];
    let cols = n.columns.max(1) as usize;
    let to = ((k % cols) as f32, (k / cols) as f32);
    let key = key_of(&(&f.screen, i, cols, n.tiles.len()));
    match &mut st.grid {
        Some((k, g)) if *k == key => g.retarget(to, now),
        g => *g = Some((key, Glide::jump(to))),
    }
    st.grid.as_ref().and_then(|(_, g)| g.moving(now)).map(|at| (i, at))
}

/// The focused grid's highlight nudged by a bump (logical pixels): from
/// where it glides, or its tile, that much on (in columns and rows).
fn nudged(f: &Frame, w: f32, glide: Option<(usize, (f32, f32))>, by: Option<(f32, f32)>) -> Option<(usize, (f32, f32))> {
    let Some((dx, dy)) = by else { return glide };
    let focused = usize::try_from(f.focus).ok().filter(|&i| f.body.get(i).is_some_and(|n| n.kind == "grid"));
    let Some((i, k)) = focused.and_then(|i| selected(&f.body[i]).map(|k| (i, k))) else { return glide };
    let n = &f.body[i];
    let cols = n.columns.max(1) as usize;
    let tw = w / cols as f32;
    let th = tile_height(&n.tiles, tw);
    let (c, r) = glide.map_or(((k % cols) as f32, (k / cols) as f32), |(_, at)| at);
    Some((i, (c + dx / tw, r + dy / th)))
}

/// A grid: the tiles' backgrounds, the highlight, then what is on them
/// (tiles do not overlap, so the order draws what a tile at a time did).
/// Gliding (at: column and row), every tile is plain and the highlight
/// is there: over the backgrounds, or a ring over the photos.
#[allow(clippy::too_many_arguments)]
fn grid(cv: &mut Canvas, tiles: &[Tile], columns: i32, sel: i32, x: f32, y: f32, w: f32, draw: bool, at: Option<(f32, f32)>) -> f32 {
    let cols = columns.max(1) as usize;
    let tw = w / cols as f32;
    let th = tile_height(tiles, tw);
    let rows = tiles.len().div_ceil(cols);
    if draw {
        let (cw, ch) = (tw - 4.0, th - 4.0);
        let place = |i: usize| (x + (i % cols) as f32 * tw + 2.0, y + (i / cols) as f32 * th + 2.0);
        let lit = |i: usize, t: &Tile| at.is_none() && (i as i32 == sel || t.focused);
        let ring = at.is_some() && selected_in(tiles, sel).is_some_and(|s| tiles[s].photo);
        for (i, t) in tiles.iter().enumerate() {
            if !t.photo && !lit(i, t) {
                let (tx, ty) = place(i);
                cv.round(tx, ty, cw, ch, R_M, pal().card);
            }
        }
        match at {
            Some((c, r)) if !ring => highlight(cv, x + c * tw + 2.0, y + r * th + 2.0, cw, ch),
            Some(_) => {}
            None => {
                for (i, t) in tiles.iter().enumerate() {
                    if !t.photo && lit(i, t) {
                        let (tx, ty) = place(i);
                        highlight(cv, tx, ty, cw, ch);
                    }
                }
            }
        }
        for (i, t) in tiles.iter().enumerate() {
            let (tx, ty) = place(i);
            if t.photo {
                tile(cv, t, lit(i, t), tx, ty, cw, ch, None);
            } else {
                tile_face(cv, t, tx, ty, cw, ch, None);
            }
        }
        if let Some((c, r)) = at.filter(|_| ring) {
            photo_ring(cv, x + c * tw + 1.0, y + r * th + 1.0, cw + 2.0, ch + 2.0);
        }
    }
    rows as f32 * th
}

/// A key or app tile's highlight.
fn highlight(cv: &mut Canvas, x: f32, y: f32, w: f32, h: f32) {
    cv.frame(x, y, w, h, 7.0, 1.5, pal().accent, pal().focus);
}

/// A photo tile's highlight on its way: the ring around the picture, over
/// the pictures it passes.
fn photo_ring(cv: &mut Canvas, x: f32, y: f32, w: f32, h: f32) {
    const T: f32 = 2.5;
    let c = pal().accent;
    cv.rect(x, y, w, T, c);
    cv.rect(x, y + h - T, w, T, c);
    cv.rect(x, y + T, T, h - 2.0 * T, c);
    cv.rect(x + w - T, y + T, T, h - 2.0 * T, c);
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
        highlight(cv, x, y, w, h);
    } else {
        cv.round(x, y, w, h, R_M, pal().card);
    }
    tile_face(cv, t, x, y, w, h, number);
}

/// What is on a key or app tile: its label, or its icon and name.
#[allow(clippy::too_many_arguments)]
fn tile_face(cv: &mut Canvas, t: &Tile, x: f32, y: f32, w: f32, h: f32, number: Option<usize>) {
    if t.image.is_empty() && t.icon.is_empty() && t.pixels.is_none() {
        // A key, not an app (the calculator): its label is the content.
        if !t.subtitle.is_empty() {
            // A day of a month: the number, its second line under it, a
            // mark (its events) in the corner.
            let fs = (h * 0.36).min(18.0);
            let l = cv.fit(Face::Bold, fs, &t.title, w - 4.0);
            let lw = cv.measure(Face::Bold, fs, &l);
            cv.text(Face::Bold, fs, x + (w - lw) / 2.0, y + h * 0.1, &l, pal().fg);
            let sz = MICRO.min(h * 0.28);
            let s = cv.fit(Face::Sans, sz, &t.subtitle, w - 2.0);
            let sw = cv.measure(Face::Sans, sz, &s);
            cv.text(Face::Sans, sz, x + (w - sw) / 2.0, y + h * 0.1 + fs * 1.3, &s, pal().dim);
        } else {
            let fs = (h * 0.32).min(22.0);
            let l = cv.fit(Face::Bold, fs, &t.title, w - 6.0);
            let lw = cv.measure(Face::Bold, fs, &l);
            cv.text(Face::Bold, fs, x + (w - lw) / 2.0, y + (h - fs * 1.45) / 2.0, &l, pal().fg);
        }
        if !t.badge.is_empty() {
            cv.circle(x + w - 4.5, y + 4.5, 2.5, pal().accent);
        }
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

/// Where an overlay's cycle is (the scan line, the ringing pulse). With a
/// period it runs by itself from the phase sent, a step every PHASE_MS,
/// from when the overlay came; in snapshots, and when nothing moves, it is
/// the phase sent.
fn phase(st: &mut State, o: &Overlay) -> f32 {
    let Some(now) = st.now.filter(|_| o.period > 0 && o.phase >= 0.0) else { return o.phase };
    st.phased = true;
    let key = key_of(&(&o.kind, o.period));
    let since = match st.phase {
        Some((k, t)) if k == key => t,
        _ => {
            st.phase = Some((key, now));
            now
        }
    };
    let ms = ticks(since, now, PHASE_STEP) as u64 * crate::motion::PHASE_MS;
    (o.phase + (ms % o.period as u64) as f32 / o.period as f32).fract()
}

/// Draws an overlay at phase p; for a menu or dialog, its panel (see
/// Drawn::overlay). What a touch picks in it goes into picks.
fn overlay(cv: &mut Canvas, o: &Overlay, p: f32, picks: &mut Vec<Spot>) -> Option<((f32, f32, f32, f32), f32, bool)> {
    cv.focus_box = None;
    let panel = match o.kind.as_str() {
        "menu" => Some((menu(cv, o, picks), MENU_SCRIM, true)),
        "symbols" => Some((symbols(cv, o, picks), MENU_SCRIM, true)),
        "scan" | "capture" => {
            viewfinder(cv, o, p);
            None
        }
        "picker" => {
            picker(cv, o, picks);
            None
        }
        "incoming" | "alarm" => {
            fullscreen_alert(cv, o, p, picks);
            None
        }
        _ => Some((dialog(cv, o, picks), DIALOG_SCRIM, false)),
    };
    if cv.focus_box.is_none() {
        cv.focus_box = panel.map(|p| p.0);
    }
    panel
}

const MENU_SCRIM: f32 = 0.5;
const DIALOG_SCRIM: f32 = 0.55;

/// The panel of the menu or dialog last drawn, in panel pixels, with its
/// scrim and whether it is a menu; and where the soft keys start.
pub fn overlay_panel(st: &State, s: f32) -> Option<(crate::motion::Panel, f32, bool)> {
    let ((x, y, w, h), a, menu) = st.drawn.overlay?;
    let px = |v: f32| (v * s).round().max(0.0) as usize;
    Some((crate::motion::Panel { x0: px(x), y0: px(y), x1: px(x + w), y1: px(y + h) }, a, menu))
}

/// Rows of the frame last drawn that a page transition moves: below the
/// status bar, above the soft keys (panel pixels).
pub fn page_rows(st: &State, s: f32) -> (usize, usize) {
    ((STATUS_H * s).round() as usize, (st.drawn.soft * s).round() as usize)
}

fn menu(cv: &mut Canvas, o: &Overlay, picks: &mut Vec<Spot>) -> (f32, f32, f32, f32) {
    cv.rect_a(0.0, STATUS_H, cv.w, cv.h - STATUS_H, Rgb(0, 0, 0), MENU_SCRIM);
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
    for (j, it) in shown.iter().enumerate() {
        if it.header {
            cv.text(Face::Bold, CAPTION, PAD + 8.0, y + 4.0, &it.text, pal().dim);
        } else {
            picks.push(((PAD + 3.0, y, w - 6.0, row), Pick::Item(start + j)));
            if it.sel {
                cv.round(PAD + 3.0, y, w - 6.0, row - 1.0, R_S, pal().focus);
                cv.focus_box = Some((PAD + 3.0, y, w - 6.0, row - 1.0));
            }
            let kw = if it.key.is_empty() { 0.0 } else { cv.measure(Face::Bold, CAPTION, &it.key) + 10.0 };
            let tw = w - 22.0 - kw;
            let t = cv.fit(Face::Sans, BODY, &it.text, tw);
            let face = if it.sel { Face::Bold } else { Face::Sans };
            let hid = if it.sel && t != it.text { hidden(cv, face, 11.0, &it.text, tw) } else { 0 };
            match scrolled(cv, hid) {
                0 => {
                    cv.text(face, 11.0, PAD + 10.0, y + 2.0, &t, pal().fg);
                }
                k => scroll_line(cv, face, 11.0, &it.text, (PAD + 10.0, y + 2.0, tw), pal().fg, k, hid, (y, y + row - 1.0)),
            }
            if kw > 0.0 {
                cv.text(Face::Bold, CAPTION, PAD + w - kw, y + 3.5, &it.key, pal().accent);
            }
        }
        y += row;
    }
    (PAD, y0, w, h)
}

/// The symbol table (Chr / Sym, * on the 9-key pad) and a key's variants:
/// `choice` columns of characters in a sheet over the soft keys, the one
/// OK picks highlighted.
fn symbols(cv: &mut Canvas, o: &Overlay, picks: &mut Vec<Spot>) -> (f32, f32, f32, f32) {
    cv.rect_a(0.0, STATUS_H, cv.w, cv.h - STATUS_H, Rgb(0, 0, 0), MENU_SCRIM);
    let cols = o.choice.max(1) as usize;
    let rows = o.items.len().div_ceil(cols).max(1);
    let (head, cell) = (20.0, 22.0);
    let w = cv.w - 2.0 * PAD;
    let h = head + rows as f32 * cell + 8.0;
    let y0 = (cv.h - SOFT_H - h - 4.0).max(STATUS_H + 4.0);
    cv.frame(PAD, y0, w, h, R_L, 1.0, pal().line, pal().sheet);
    let t = cv.fit(Face::Bold, SMALL, &o.title, w - 16.0);
    cv.text(Face::Bold, SMALL, PAD + 8.0, y0 + 5.0, &t, pal().fg);
    let cw = (w - 12.0) / cols as f32;
    for (i, it) in o.items.iter().enumerate() {
        let (cx, cy) = (PAD + 6.0 + (i % cols) as f32 * cw, y0 + head + (i / cols) as f32 * cell);
        picks.push(((cx, cy, cw, cell), Pick::Item(i)));
        cv.round(cx + 1.5, cy + 1.0, cw - 3.0, cell - 2.0, R_S, if it.sel { pal().accent } else { pal().card });
        let s = cv.fit(Face::Sans, BODY, &it.text, cw - 6.0);
        let sw = cv.measure(Face::Sans, BODY, &s);
        cv.text(Face::Sans, BODY, cx + (cw - sw) / 2.0, cy + 3.0, &s, if it.sel { pal().bg } else { pal().fg });
    }
    (PAD, y0, w, h)
}

fn dialog(cv: &mut Canvas, o: &Overlay, picks: &mut Vec<Spot>) -> (f32, f32, f32, f32) {
    cv.rect_a(0.0, STATUS_H, cv.w, cv.h - STATUS_H, Rgb(0, 0, 0), DIALOG_SCRIM);
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
            picks.push(((bx, y, bw, choice_h), Pick::Choice(i)));
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
    (x, y0, w, h)
}

/// How far something has moved, rounded to whole panel pixels: a phase
/// step that moves it less paints nothing, and where it starts stays put.
fn on_pixels(cv: &Canvas, v: f32) -> f32 {
    (v * cv.s).round() / cv.s
}

fn viewfinder(cv: &mut Canvas, o: &Overlay, p: f32) {
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
    if p >= 0.0 {
        let y = top + on_pixels(cv, used * p.clamp(0.0, 1.0));
        cv.rect(PAD + 8.0, y, w - 16.0, 1.5, pal().accent);
    }
    let mut y = top + used + 6.0;
    for l in o.lines.iter().chain(o.footer.iter()) {
        let t = cv.fit(Face::Sans, CAPTION, l, cv.w - 2.0 * PAD);
        cv.text(Face::Sans, CAPTION, PAD, y, &t, pal().dim);
        y += 13.0;
    }
}

fn picker(cv: &mut Canvas, o: &Overlay, picks: &mut Vec<Spot>) {
    cv.rect(0.0, STATUS_H, cv.w, cv.h - STATUS_H, pal().bg);
    cv.rect(0.0, STATUS_H, cv.w, TITLE_H, pal().title);
    cv.text(Face::Bold, TITLE, PAD, STATUS_H + 3.5, &o.title, pal().trust);
    let cols = o.columns.max(3) as usize;
    let tw = (cv.w - 2.0 * PAD) / cols as f32;
    let th = tw * 1.0;
    let top = STATUS_H + TITLE_H + 4.0;
    // Whole rows above the lines under them, from the first that keeps
    // the chosen tile's row on screen, as a menu keeps its entry.
    let notes = 13.0 * (o.lines.len() + o.footer.len()) as f32;
    let fit = (((cv.h - 7.0 - notes - top) / th).floor() as usize).max(1);
    let rows = o.tiles.len().div_ceil(cols);
    let sel = usize::try_from(o.choice).ok().filter(|&i| i < o.tiles.len()).or_else(|| o.tiles.iter().position(|t| t.focused)).unwrap_or(0);
    let first = (sel / cols + 1).saturating_sub(fit);
    let shown = rows.min(first + fit) - first;
    for (i, t) in o.tiles.iter().enumerate().skip(first * cols).take(shown * cols) {
        let (tx, ty) = (PAD + (i % cols) as f32 * tw, top + (i / cols - first) as f32 * th);
        picks.push(((tx, ty, tw, th), Pick::Tile(i)));
        tile(cv, t, i as i32 == o.choice || t.focused, tx + 2.0, ty + 2.0, tw - 4.0, th - 4.0, Some(i + 1));
    }
    if rows > shown {
        scroll_bar(cv, top, top + shown as f32 * th, shown as f32 / rows as f32, first as f32 / (rows - shown) as f32);
    }
    let mut y = top + shown as f32 * th + 6.0;
    for l in o.lines.iter().chain(o.footer.iter()) {
        let t = cv.fit(Face::Sans, CAPTION, l, cv.w - 2.0 * PAD);
        cv.text(Face::Sans, CAPTION, PAD, y.min(cv.h - 14.0), &t, pal().dim);
        y += 13.0;
    }
}

fn fullscreen_alert(cv: &mut Canvas, o: &Overlay, p: f32, picks: &mut Vec<Spot>) {
    let base = if o.kind == "alarm" { ALARM_BG } else { CALL_BG };
    cv.rect(0.0, STATUS_H, cv.w, cv.h - STATUS_H, base);
    let cy = STATUS_H + (cv.h - STATUS_H) * 0.3;
    if p >= 0.0 {
        let r = 24.0 + on_pixels(cv, 10.0 * p.clamp(0.0, 1.0));
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
            picks.push(((bx, by, bw, 28.0), Pick::Choice(i)));
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

    /// The speed at t, from the values a quarter of a millisecond either
    /// side (before: up to t, after: from t), in units a millisecond.
    fn speeds(e: &Ease, t: Instant) -> (f32, f32) {
        let d = Duration::from_micros(250);
        let v = |t: Instant| e.value(Some(t));
        ((v(t) - v(t - d)) * 4.0, (v(t + d) - v(t)) * 4.0)
    }

    // A new target while it moves: the value and its speed go on without a
    // jump (the old ease restarted at three times the average speed), the
    // value never passes the target and comes to rest on it in the time
    // given; from rest the move is the ease-out cubic to the bit, and a
    // target behind it starts from standing.
    #[test]
    fn ease_keeps_its_speed_on_a_new_target() {
        let t0 = Instant::now();
        let ms = |n: f32| t0 + Duration::from_micros((n * 1000.0).round() as u64);
        let mut e = Ease::default();
        e.jump(0.0);
        e.retarget(100.0, Some(t0), FOCUS_MS);
        for n in [0.0, 13.0, 50.0, 99.0, 100.0] {
            let p = e.progress(Some(ms(n)));
            assert_eq!(e.value(Some(ms(n))), 100.0 * (1.0 - (1.0 - p).powi(3)), "from rest, the ease-out at {n} ms");
        }
        for (at, to) in [(40.0, 200.0), (70.0, 101.0), (20.0, 104.0)] {
            let mut e = Ease::default();
            e.jump(0.0);
            e.retarget(100.0, Some(t0), FOCUS_MS);
            let (before, _) = speeds(&e, ms(at));
            let x = e.value(Some(ms(at)));
            e.retarget(to, Some(ms(at)), FOCUS_MS);
            let (_, after) = speeds(&e, ms(at));
            assert!((e.value(Some(ms(at))) - x).abs() < 1e-3, "retargeted at {at} ms: the value jumped");
            // Cut only where going on as fast would pass the target.
            let fits = before * FOCUS_MS <= 3.0 * (to - x);
            assert!(!fits || (after - before).abs() <= 0.02 * before.abs().max(0.1), "at {at} ms to {to}: {before} then {after} a ms");
            assert!(fits || after < before, "at {at} ms to {to}: going on at {before} a ms would pass it");
            let mut last = x;
            for k in 1..=110 {
                let v = e.value(Some(ms(at + k as f32)));
                assert!(v >= last - 1e-4 && v <= to + 1e-4, "at {at} ms to {to}, {k} ms on: {v} after {last}");
                last = v;
            }
            assert!(e.running(Some(ms(at + FOCUS_MS - 1.0))));
            assert!(!e.running(Some(ms(at + FOCUS_MS))), "it takes FOCUS_MS");
            assert_eq!(e.value(Some(ms(at + FOCUS_MS))), to, "it rests on the target");
        }
        let mut e = Ease::default();
        e.jump(0.0);
        e.retarget(100.0, Some(t0), FOCUS_MS);
        let x = e.value(Some(ms(30.0)));
        e.retarget(0.0, Some(ms(30.0)), FOCUS_MS);
        assert!(speeds(&e, ms(30.0)).1.abs() < 0.05, "turned round: from standing");
        let mut last = x;
        for k in 1..=100 {
            let v = e.value(Some(ms(30.0 + k as f32)));
            assert!(v <= last + 1e-4 && v >= -1e-4, "on the way back, {k} ms on: {v} after {last}");
            last = v;
        }
        assert_eq!(last, 0.0);
    }

    // A held key moves the target a row on every 70 ms: the speed changes
    // by at most a twentieth of the average from a quarter millisecond to
    // the next, also at each new target (the old ease jumped from a fifth
    // to over twice the average there), and the value only goes forward.
    #[test]
    fn a_held_key_glides_without_jolts() {
        let t0 = Instant::now();
        let ms = |n: f32| t0 + Duration::from_micros((n * 1000.0).round() as u64);
        let row = 26.0;
        let mut e = Ease::default();
        e.jump(0.0);
        let mut target = 0.0;
        let mut worst: f32 = 0.0;
        let mut last = 0.0;
        for k in 0..12 {
            let at = 70.0 * k as f32;
            target += row;
            let (before, _) = speeds(&e, ms(at));
            e.retarget(target, Some(ms(at)), FOCUS_MS);
            if k > 0 {
                worst = worst.max((speeds(&e, ms(at)).1 - before).abs());
            }
            for n in 1..70 {
                let t = at + n as f32;
                let (a, b) = speeds(&e, ms(t));
                let v = e.value(Some(ms(t)));
                assert!(v >= last - 1e-3, "{t} ms: {v} after {last}");
                last = v;
                worst = worst.max((b - a).abs());
            }
        }
        let average = row / 70.0;
        assert!(worst <= average / 20.0, "the speed jumped by {worst} a ms (average {average})");
    }

    // A held key in a list: the highlight goes on from where it is at the
    // speed it has to each next row (it restarted from the row before).
    #[test]
    fn the_list_highlight_keeps_its_speed() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let frame = |focus: i32| -> Frame {
            let body: Vec<serde_json::Value> = (0..8).map(|i| serde_json::json!({"kind": "item", "title": format!("Row {i}"), "focused": i == focus})).collect();
            serde_json::from_value(serde_json::json!({"screen": "a/list", "title": "T", "focus": focus, "body": body})).unwrap()
        };
        let t0 = Instant::now();
        let ms = |n: f32| t0 + Duration::from_micros((n * 1000.0).round() as u64);
        let mut st = State::default();
        let draw_at = |st: &mut State, focus: i32, at: f32| {
            st.now = Some(ms(at));
            draw(&mut Canvas::new(240, 320, 1.0, fonts), &frame(focus), st);
        };
        draw_at(&mut st, 0, 0.0);
        let mut last = None;
        for k in 1..5 {
            let at = 1000.0 + 70.0 * (k - 1) as f32;
            let before = last.map(|(y0, y1): (f32, f32)| y1 - y0);
            draw_at(&mut st, k, at);
            let y = |t: f32| st.bar.at(Some(ms(t))).0;
            let after = y(at + 0.25) - y(at);
            if let Some(b) = before {
                assert!((after - b).abs() <= 0.05 * b.abs().max(0.1), "row {k}: {b} then {after} a quarter ms");
            }
            last = Some((y(at + 69.75), y(at + 70.0)));
        }
    }

    // A finger dragged the body: it shows at that offset wherever the
    // focus is, at once (no glide behind the finger), a touch at its top is
    // on the row there, and the layout says how wide the nodes are and how
    // tall all of them; after the drag a focus on what shows keeps it so.
    #[test]
    fn a_dragged_body_shows_where_the_finger_left_it() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let frame = |focus: i32, offset: Option<f32>| -> Frame {
            let body: Vec<serde_json::Value> = (0..30).map(|i| serde_json::json!({"kind": "item", "title": format!("Row {i}"), "focused": i == focus})).collect();
            let mut v = serde_json::json!({"screen": "a/list", "title": "T", "focus": focus, "body": body});
            if let Some(o) = offset {
                v["scroll"] = serde_json::json!({"pos": 0, "frac": -1, "offset": o});
            }
            serde_json::from_value(v).unwrap()
        };
        let mut st = State { now: Some(Instant::now()), ..State::default() };
        let mut cv = Canvas::new(240, 320, 1.0, fonts);
        draw(&mut cv, &frame(0, None), &mut st);
        let _ = take_layout(&mut st);
        draw(&mut cv, &frame(0, Some(120.0)), &mut st);
        assert_eq!((st.offset, st.shown.value(st.now)), (120.0, 120.0));
        let l = take_layout(&mut st).unwrap();
        assert_eq!((l["offset"].as_f64(), l["left"].as_f64(), l["width"].as_f64()), (Some(120.0), Some(PAD as f64), Some(240.0 - 2.0 * PAD as f64)));
        let (area, content) = (l["area"].as_f64().unwrap(), l["content"].as_f64().unwrap());
        let last = &l["nodes"][29];
        assert_eq!(content, last["y"].as_f64().unwrap() + last["h"].as_f64().unwrap() + GAP as f64);
        assert!(content > area);
        let pitch = l["nodes"][1]["y"].as_f64().unwrap() as f32;
        let top = l["top"].as_f64().unwrap() as f32;
        let first = (120.0 / pitch).ceil() as usize;
        assert_eq!(hit(&st, 120.0, top + (first as f32 * pitch - 120.0) + 2.0), Hit::Body("a/list".into(), Some((first, 0))));
        draw(&mut cv, &frame(first as i32, None), &mut st);
        assert_eq!(st.offset, 120.0, "the focus on a row that shows moved it");
    }

    // Each layout says which frame it is of, as "drawn" counts them. With
    // every_frame (feature "touch") each frame laid out sends one, though
    // only the number changed: the selftest's touch steps wait for the
    // frame on screen's. Without it only a new geometry does, with the
    // number of the frame that brought it.
    #[test]
    fn the_layout_names_the_frame_it_is_of() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let call = |lines: &[&str]| -> Frame {
            let body: Vec<serde_json::Value> = lines.iter().map(|l| serde_json::json!({"kind": "text", "text": l})).collect();
            serde_json::from_value(serde_json::json!({"screen": "a/call", "title": "Call", "body": body})).unwrap()
        };
        let mut cv = Canvas::new(240, 320, 1.0, fonts);
        let mut laid = |st: &mut State, n: u64, f: &Frame| {
            st.frame = n;
            layout(&mut cv, f, st);
            layout_msg(st)
        };
        let mut st = State { every_frame: true, ..State::default() };
        let first = laid(&mut st, 1, &call(&["Ringing", "Mute"])).expect("frame 1's layout");
        assert_eq!((first["t"].as_str(), first["n"].as_u64()), (Some("layout"), Some(1)));
        let same = laid(&mut st, 2, &call(&["Speaker", "Mute"])).expect("frame 2's layout, of the same geometry");
        assert_eq!((same["n"].as_u64(), &same["layout"]), (Some(2), &first["layout"]));
        assert_eq!(laid(&mut st, 2, &call(&["Speaker", "Mute"])), None, "frame 2 laid out again");

        let mut st = State::default();
        assert_eq!(laid(&mut st, 1, &call(&["Ringing", "Mute"])).and_then(|m| m["n"].as_u64()), Some(1));
        assert_eq!(laid(&mut st, 2, &call(&["Speaker", "Mute"])), None, "without every_frame the same geometry sends nothing");
        let grown = laid(&mut st, 3, &call(&["Speaker", "Mute", "Hold"])).expect("a new geometry");
        assert_eq!((grown["n"].as_u64(), grown["layout"]["nodes"].as_array().map(Vec::len)), (Some(3), Some(3)));
    }

    fn focus_at(cv: &Canvas, x: f32, y: f32) -> bool {
        let (px, py) = ((x * cv.s) as u32, (y * cv.s) as u32);
        let c = cv.pm.pixel(px, py).unwrap();
        let f = pal().focus;
        (c.red(), c.green(), c.blue()) == (f.0, f.1, f.2)
    }

    // Moving the focus two rows down: half way the highlight covers the row
    // between them, at the end only the row it went to.
    #[test]
    fn the_focus_highlight_slides_between_rows() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let frame = |focus: i32| -> Frame {
            let item = |t: &str, on: bool| serde_json::json!({"kind": "item", "title": t, "focused": on});
            serde_json::from_value(serde_json::json!({"screen": "a/list", "title": "T", "focus": focus,
                "body": [item("A", focus == 0), item("B", focus == 1), item("C", focus == 2)]})).unwrap()
        };
        let mut st = State::default();
        let t0 = Instant::now();
        let draw = |st: &mut State, f: &Frame, now: Instant| {
            st.now = Some(now);
            let mut cv = Canvas::new(240, 320, 1.0, fonts);
            draw(&mut cv, f, st);
            cv
        };
        draw(&mut st, &frame(0), t0);
        draw(&mut st, &frame(2), t0);
        let rows: Vec<(f32, f32)> = st.drawn.nodes.clone();
        let x = PAD + 100.0;
        let mid = draw(&mut st, &frame(2), t0 + std::time::Duration::from_millis(30));
        assert!(st.fade.running(st.now), "still moving");
        assert!(focus_at(&mid, x, rows[1].0 + rows[1].1 - 3.0), "half way: over the middle row");
        assert!(!focus_at(&mid, x, rows[0].0 + 3.0), "half way: past the first row's top");
        let end = draw(&mut st, &frame(2), t0 + std::time::Duration::from_millis(400));
        assert!(focus_at(&end, x, rows[2].0 + 3.0), "at the end: the row it went to");
        assert!(!focus_at(&end, x, rows[1].0 + rows[1].1 / 2.0), "at the end: not the middle row");
    }

    fn menu_frame(sel: i32, motion: &str) -> Frame {
        let tiles: Vec<serde_json::Value> = (0..6).map(|i| serde_json::json!({"title": format!("App {i}"), "icon": "A", "focused": i == sel})).collect();
        serde_json::from_value(serde_json::json!({"screen": "system/sys.menu", "title": "Menu", "focus": 0, "motion": motion,
            "body": [{"kind": "grid", "columns": 3, "sel": sel, "focused": true, "tiles": tiles}]}))
        .unwrap()
    }

    // The focus moving right in a grid: the highlight glides over the gap
    // between the two tiles (it is in neither at the start or the end), in
    // FOCUS_MS, with motion reduced too, as a list's focus; with motion off
    // it is there at once.
    #[test]
    fn the_grid_highlight_glides_between_tiles() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let draw = |st: &mut State, f: &Frame, now: Option<Instant>| {
            st.now = now;
            let mut cv = Canvas::new(240, 320, 1.0, fonts);
            draw(&mut cv, f, st);
            cv
        };
        let t0 = Instant::now();
        let ms = |n: u64| Some(t0 + Duration::from_millis(n));
        let mut st = State::default();
        let start = draw(&mut st, &menu_frame(0, ""), ms(0));
        let (top, _) = st.drawn.nodes[0];
        let tw = (240.0 - 2.0 * PAD) / 3.0;
        let gap = (PAD + tw, top + tw * 0.98 / 2.0);
        assert!(!focus_at(&start, gap.0, gap.1), "settled on the first tile");
        draw(&mut st, &menu_frame(1, ""), ms(1000));
        let mid = draw(&mut st, &menu_frame(1, ""), ms(1040));
        assert!(animating(&st), "still gliding");
        assert!(focus_at(&mid, gap.0, gap.1), "on its way over the gap");
        let end = draw(&mut st, &menu_frame(1, ""), ms(1000 + FOCUS_MS as u64 + 1));
        assert!(!animating(&st) && !focus_at(&end, gap.0, gap.1), "settled on the second tile");
        assert!(end.pm == draw(&mut State::default(), &menu_frame(1, ""), None).pm, "settled as a snapshot draws it");
        let mut st = State::default();
        draw(&mut st, &menu_frame(0, "reduced"), ms(0));
        draw(&mut st, &menu_frame(1, "reduced"), ms(1000));
        assert!(animating(&st), "reduced motion: it glides as a list's focus does");
        let mut st = State::default();
        draw(&mut st, &menu_frame(0, "off"), ms(0));
        let now = draw(&mut st, &menu_frame(1, "off"), ms(1000));
        assert!(!animating(&st) && !focus_at(&now, gap.0, gap.1), "motion off: no glide");
    }

    // The grid's passes (backgrounds, highlight, faces) draw what a tile at
    // a time did, on every kind of tile: snapshots stay the same.
    #[test]
    fn grid_passes_draw_what_a_tile_at_a_time_did() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        use base64::Engine as _;
        let px = base64::engine::general_purpose::STANDARD.encode((0..64u32).flat_map(|i| [(i * 4) as u8, 90, 200]).collect::<Vec<u8>>());
        let path = std::env::temp_dir().join(format!("phone-paint-grid-icon-{}.png", std::process::id()));
        let mut ic = Pixmap::new(24, 24).unwrap();
        ic.fill(tiny_skia::Color::from_rgba8(0x30, 0xa0, 0x60, 255));
        ic.save_png(&path).unwrap();
        let tile_of = |v: serde_json::Value| serde_json::from_value::<Tile>(v).unwrap();
        let apps: Vec<Tile> = (0..7)
            .map(|i| tile_of(serde_json::json!({"title": format!("App {i} with a long name"), "icon": "\u{4fe1}", "image": if i == 2 { path.to_str().unwrap() } else { "" }, "badge": if i == 4 { "12" } else { "" }})))
            .collect();
        let keys: Vec<Tile> = ["7", "8", "9", "\u{00f7}", "C"].iter().map(|k| tile_of(serde_json::json!({"title": k}))).collect();
        let days: Vec<Tile> = (1..=14).map(|d| tile_of(serde_json::json!({"title": format!("{d}"), "subtitle": "\u{521d}\u{4e00}", "badge": if d % 5 == 0 { "1" } else { "" }, "focused": d == 9}))).collect();
        let photos: Vec<Tile> = (0..6).map(|i| tile_of(serde_json::json!({"photo": true, "pixels": if i < 4 { serde_json::json!({"w": 8, "h": 8, "rgb": px}) } else { serde_json::Value::Null }, "badge": if i == 1 { "0:12" } else { "" }}))).collect();
        for (name, tiles, cols, sel) in [("apps", &apps, 3, 4), ("keys", &keys, 4, 1), ("days", &days, 7, -1), ("photos", &photos, 3, 2), ("photos, an empty cell", &photos, 3, 5)] {
            let mut a = Canvas::new(480, 640, 2.0, fonts);
            grid(&mut a, tiles, cols, sel, PAD, 40.0, 228.0, true, None);
            let mut b = Canvas::new(480, 640, 2.0, fonts);
            let tw = 228.0 / cols as f32;
            let th = tile_height(tiles, tw);
            for (i, t) in tiles.iter().enumerate() {
                let (tx, ty) = (PAD + (i % cols as usize) as f32 * tw, 40.0 + (i / cols as usize) as f32 * th);
                super::tile(&mut b, t, i as i32 == sel || t.focused, tx + 2.0, ty + 2.0, tw - 4.0, th - 4.0, None);
            }
            assert!(a.pm == b.pm, "{name}: the passes draw another picture");
        }
        let _ = std::fs::remove_file(&path);
    }

    // The candidate row: the highlight drawn before the words draws what
    // drawing it with its own word did.
    #[test]
    fn the_candidate_row_draws_as_it_did() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let old = |cv: &mut Canvas, row: &ImeRow, y: f32| {
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
        };
        let items: Vec<String> = ["\u{4e2d}", "\u{79cd}", "\u{91cd}", "\u{4f17}", "zhongguo", "\u{949f}", "\u{7ec8}", "\u{80bf}", "\u{4ef2}", "\u{5fe0}", "\u{8877}", "\u{51a2}"].iter().map(|s| s.to_string()).collect();
        for (sel, active, numbered) in [(0, true, true), (3, true, true), (4, true, false), (11, true, true), (1, false, true)] {
            let row = ImeRow { items: items.clone(), sel, active, numbered, more: false };
            let mut a = Canvas::new(480, 640, 2.0, fonts);
            ime_row(&mut a, &row, 280.0, &mut None, 0, None);
            let mut b = Canvas::new(480, 640, 2.0, fonts);
            old(&mut b, &row, 280.0);
            assert!(a.pm == b.pm, "sel {sel}, active {active}: another picture");
        }
    }

    // The status bar shows the sound mode but ring, each with its own icon.
    #[test]
    fn the_status_bar_shows_the_sound_mode() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let bar = |sound: &str| {
            let mut cv = Canvas::new(480, 640, 2.0, fonts);
            status(&mut cv, &Status { sound: sound.to_string(), signal: 3, battery: 80, ..Default::default() }, 1.0);
            cv.pm
        };
        let (ring, vibrating, silent, unknown) = (bar(""), bar("vibrate"), bar("silent"), bar("loud"));
        assert!(ring != vibrating && ring != silent && vibrating != silent, "the modes look alike");
        assert!(ring == unknown, "a mode it does not know drew something");
    }

    // The VoLTE mark is drawn left of the network type on every form
    // factor, but not where it would push the icons into the middle's text
    // (a call on the 12-key phone's narrow bar).
    #[test]
    fn the_status_bar_shows_volte_where_it_has_room() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let bar = |(w, h, k): (u32, u32, f32), volte: bool, mid: &str| {
            let mut cv = Canvas::new(w, h, k, fonts);
            let s = Status { time: "12:34".into(), net: "4G".into(), signal: 3, battery: 80, percent: true, volte, mid: mid.into(), mid_kind: "call".into(), ..Default::default() };
            status(&mut cv, &s, 1.0);
            cv.pm
        };
        let (t9, qwerty, wide) = ((480, 640, 2.0), (640, 480, 2.0), (800, 352, 1.6));
        let call = "● Call 01:23";
        for form in [t9, qwerty, wide] {
            assert!(bar(form, true, "") != bar(form, false, ""), "{form:?}: no mark");
        }
        for form in [qwerty, wide] {
            assert!(bar(form, true, call) != bar(form, false, call), "{form:?}: no mark beside a call");
        }
        assert!(bar(t9, true, call) == bar(t9, false, call), "the mark crowded the call");
    }

    /// A code as the Shell sends one, w modules by h rows (a barcode's rows
    /// all alike), one pixel a module; dark says which modules are.
    fn code_frame(kind: &str, w: u32, h: u32, secure: bool, dark: impl Fn(u32, u32) -> bool) -> Frame {
        use base64::Engine as _;
        let px: Vec<u8> = (0..h).flat_map(|j| (0..w).map(move |i| (i, j))).flat_map(|(i, j)| [if dark(i, j) { 0u8 } else { 255 }; 3]).collect();
        let rgb = base64::engine::general_purpose::STANDARD.encode(px);
        serde_json::from_value(serde_json::json!({"screen": "a/code", "title": "T", "focus": -1, "body": [
            {"kind": "image", "image": {"w": w, "h": h, "rgb": rgb, "pixelated": true, "secure": secure,
                "code": kind, "code_note": "The barcode does not fit:", "code_text": "2812 3456 7890 1234 56"}},
            {"kind": "text", "text": "Refreshes every 30 seconds"}]})).unwrap()
    }

    // On every form factor's panel, and a 240x320 one at one pixel a unit,
    // a code is drawn at a whole number of panel pixels a module (a reader
    // takes modules of uneven widths for others), as many as fit, centred.
    #[test]
    fn codes_are_drawn_at_whole_pixels_a_module() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let bar = |i: u32, _: u32| i.is_multiple_of(3) || i % 7 == 1;
        let qr = |i: u32, j: u32| (i * 7 + j * 3) % 5 < 2;
        for (pw, ph, s) in [(480, 640, 2.0), (640, 480, 2.0), (800, 352, 1.6), (240, 320, 1.0)] {
            for (kind, w, h, dark) in [("bar", 154, 38, &bar as &dyn Fn(u32, u32) -> bool), ("qr", 41, 41, &qr)] {
                let mut st = State::default();
                let mut cv = Canvas::new(pw, ph, s, fonts);
                draw(&mut cv, &code_frame(kind, w, h, false, dark), &mut st);
                let px = |x: u32, y: u32| cv.pm.pixel(x, y).map(|c| (c.red(), c.green(), c.blue()));
                let code = |x: u32, y: u32| matches!(px(x, y), Some((0, 0, 0)) | Some((255, 255, 255)));
                let (top, _) = st.drawn.nodes[0];
                let y0 = (top * s).ceil() as u32 + 1;
                let x0 = (0..pw).find(|&x| code(x, y0)).unwrap_or_else(|| panic!("{kind} at {pw}x{ph}: not drawn"));
                let x1 = (x0..pw).take_while(|&x| code(x, y0)).last().unwrap() + 1;
                let k = (x1 - x0) / w;
                assert!(k >= 1 && x1 - x0 == k * w, "{kind} at {pw}x{ph}@{s}: {} pixels for {w} modules", x1 - x0);
                let (y0, y1) = ((0..y0).rev().take_while(|&y| code(x0, y)).last().unwrap(), (y0..ph).take_while(|&y| code(x0, y)).last().unwrap() + 1);
                if kind == "qr" {
                    assert_eq!(y1 - y0, k * h, "{kind} at {pw}x{ph}@{s}: square");
                } else {
                    assert!(y1 - y0 >= h && y1 - y0 <= k * h, "{kind} at {pw}x{ph}@{s}: {} rows", y1 - y0);
                }
                for y in y0..y1 {
                    for x in x0..x1 {
                        let v = if dark((x - x0) / k, (y - y0) / k) { 0 } else { 255 };
                        assert_eq!(px(x, y), Some((v, v, v)), "{kind} at {pw}x{ph}@{s}, {k} pixels a module: pixel {x},{y}");
                    }
                }
                let (left, right) = (x0 as f32 - PAD * s, (pw as f32 - PAD * s) - x1 as f32);
                assert!((left - right).abs() <= 1.0, "{kind} at {pw}x{ph}@{s}: margins {left} and {right}");
                if kind == "bar" {
                    assert!(((k + 1) * w) as f32 > (pw as f32 - 2.0 * PAD * s), "{kind} at {pw}x{ph}@{s}: {k} pixels a module, more fit");
                }
            }
        }
    }

    // A code that does not fit at one pixel a module is not drawn: the
    // Shell's note and the number show, the number masked in snapshots
    // when the code is secure.
    #[test]
    fn a_code_too_wide_says_so() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let shown = |secure: bool, mask: bool| {
            let mut st = State::default();
            let mut cv = Canvas::new(240, 320, 1.0, fonts);
            cv.mask_secure = mask;
            draw(&mut cv, &code_frame("bar", 300, 75, secure, |i, _| i % 2 == 0), &mut st);
            let (top, h) = st.drawn.nodes[0];
            let colours: std::collections::HashSet<_> = (top as u32..(top + h) as u32)
                .flat_map(|y| (0..240).map(move |x| (x, y)))
                .filter_map(|(x, y)| cv.pm.pixel(x, y).map(|c| (c.red(), c.green(), c.blue())))
                .collect();
            (h, colours)
        };
        let (h, plain) = shown(false, true);
        assert!(h > 20.0 && h < 75.0, "the note is {h} high");
        assert!(!plain.contains(&(0, 0, 0)), "bars drawn");
        // The mask's label is drawn in its own yellow.
        let masked = |c: &std::collections::HashSet<(u8, u8, u8)>| c.contains(&(0xf2, 0xc9, 0x4c));
        assert!(masked(&shown(true, true).1) && !masked(&plain), "the number not masked in a snapshot");
        assert!(!masked(&shown(true, false).1), "the number masked on the panel");
    }

    // An item's picture (a contact's photo) is drawn where its icon goes.
    #[test]
    fn an_items_picture_is_its_icon() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        use base64::Engine as _;
        let red = base64::engine::general_purpose::STANDARD.encode([200u8, 20, 20].repeat(16));
        let f: Frame = serde_json::from_value(serde_json::json!({"screen": "a/list", "title": "T", "focus": -1, "body": [
            {"kind": "item", "title": "Ann", "subtitle": "1001", "icon": "A", "image": {"w": 4, "h": 4, "rgb": red}}]})).unwrap();
        let mut st = State::default();
        let mut cv = Canvas::new(240, 320, 1.0, fonts);
        draw(&mut cv, &f, &mut st);
        let (top, h) = st.drawn.nodes[0];
        let c = cv.pm.pixel((PAD + 15.0) as u32, (top + h / 2.0) as u32).unwrap();
        assert_eq!((c.red(), c.green(), c.blue()), (200, 20, 20), "the picture in the icon's place");
        assert_eq!(h, 36.0, "the row is as high as any with a subtitle");
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
            ..Drawn::default()
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

    // Settings › Accessibility: bold text draws (and measures) every text in
    // the bold face, high contrast puts it in black or white; the focus is
    // known for the magnifier: the focused row, a tile, a menu's entry.
    #[test]
    fn bold_high_contrast_and_the_focus_box() {
        let Ok(fonts) = crate::canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let frame = |extra: serde_json::Value| -> Frame {
            let mut v = serde_json::json!({"screen": "a/list", "title": "T", "focus": 1, "theme": "light", "body": [
                {"kind": "item", "title": "Bluetooth", "subtitle": "On"}, {"kind": "item", "title": "Wi-Fi", "subtitle": "Home", "focused": true},
                {"kind": "grid", "columns": 3, "sel": 4, "tiles": [{"title": "1"}, {"title": "2"}, {"title": "3"}, {"title": "4"}, {"title": "5"}]}]});
            crate::frame::merge(&mut v, &extra);
            serde_json::from_value(v).unwrap()
        };
        let mut st = State::default();
        let mut plain = Canvas::new(240, 320, 1.0, fonts);
        draw(&mut plain, &frame(serde_json::json!({})), &mut st);
        let narrow = plain.measure(Face::Sans, BODY, "Bluetooth");
        let rows = st.drawn.nodes.clone();
        assert_eq!(plain.focus_box, Some((PAD, rows[1].0, plain.w - 2.0 * PAD, rows[1].1)));
        let mut bold = Canvas::new(240, 320, 1.0, fonts);
        draw(&mut bold, &frame(serde_json::json!({"bold": true})), &mut st);
        assert!(bold.measure(Face::Sans, BODY, "Bluetooth") > narrow, "the regular face measured bold");
        assert_ne!(bold.pm.data(), plain.pm.data());
        crate::canvas::set_bold(false);
        let mut hc = Canvas::new(240, 320, 1.0, fonts);
        draw(&mut hc, &frame(serde_json::json!({"contrast": true})), &mut st);
        assert_eq!(pal().dim, Rgb(0, 0, 0), "light theme, high contrast: black text");
        assert_ne!(hc.pm.data(), plain.pm.data(), "the subtitles turned black");
        // The grid's fifth tile: second row, second column.
        let mut cv = Canvas::new(240, 320, 1.0, fonts);
        draw(&mut cv, &frame(serde_json::json!({"focus": 2})), &mut st);
        let (cols, th, _) = st.drawn.grids[2].unwrap();
        let tw = (cv.w - 2.0 * PAD) / cols as f32;
        assert_eq!(cv.focus_box, Some((PAD + tw, st.drawn.nodes[2].0 + th, tw, th)));
        let menu = serde_json::json!({"overlay": {"kind": "menu", "title": "Options", "items": [{"text": "Copy"}, {"text": "Forward", "sel": true}]}});
        draw(&mut cv, &frame(menu), &mut st);
        let ((px, py, pw, ph), _, _) = st.drawn.overlay.unwrap();
        let (x, y, w, h) = cv.focus_box.unwrap();
        assert!(x >= px && y > py + 20.0 && x + w <= px + pw && y + h <= py + ph && h < 20.0, "the entry chosen: {:?} in {:?}", cv.focus_box, (px, py, pw, ph));
        set_theme("", false);
    }

    // The symbol table is a grid above the soft keys on every form factor
    // (it was drawn as a dialog, a line per symbol, taller than any screen).
    #[test]
    fn the_symbol_table_fits_the_screen() {
        let Ok(fonts) = crate::canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let items = (0..24).map(|i| crate::frame::MenuItem { text: format!("{i}"), sel: i == 7, ..Default::default() }).collect();
        let o = Overlay { kind: "symbols".into(), title: "Symbols 1/3".into(), items, choice: 6, ..Default::default() };
        for (w, h, s) in [(480, 640, 2.0), (640, 480, 2.0), (800, 352, 1.6)] {
            let mut cv = Canvas::new(w, h, s, fonts);
            let ((_, y, _, ph), _, _) = overlay(&mut cv, &o, 0.0, &mut Vec::new()).expect("a panel");
            assert!(y >= STATUS_H && y + ph <= cv.h - SOFT_H, "{w}x{h}: the panel is {y}..{} of {}", y + ph, cv.h);
        }
    }

    // The on-screen keypad sits right above the soft keys, the candidate
    // rows and the body above it; a touch on a key, or the gap around its
    // cap, says the key's id, a gap without one nothing of the keypad;
    // the layout gives the keys' cells for the selftest to aim at.
    #[test]
    fn the_keypad_is_drawn_above_the_soft_keys() {
        let Ok(fonts) = crate::canvas::Fonts::load() else { return };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let key = |id: &str, label: &str| serde_json::json!({"id": id, "label": label});
        let f: Frame = serde_json::from_value(serde_json::json!({
            "screen": "app/form", "title": "Form", "soft": ["Options", "", ""],
            "body": [{"kind": "input", "placeholder": "Time", "focused": true}],
            "ime": [{"items": ["a", "b"], "sel": 0, "active": true}],
            "keypad": {"rows": [
                [key("1", "1"), key("2", "2"), key("3", "3"), key("backspace", "\u{232b}")],
                [key("", ""), key("0", "0"), key("", ""), key("enter", "OK")],
            ]},
        }))
        .unwrap();
        let mut cv = Canvas::new(480, 640, 2.0, fonts);
        let mut st = State::default();
        draw(&mut cv, &f, &mut st);
        let top = 320.0 - SOFT_H - 2.0 * KEY_H;
        assert_eq!(st.drawn.bottom, top - IME_H, "the body ends above the candidate row");
        assert_eq!(hit(&st, 90.0, top + 10.0), Hit::Key("2".into()));
        assert_eq!(hit(&st, 61.0, top + 1.0), Hit::Key("2".into()), "the gap around the cap");
        assert_eq!(hit(&st, 230.0, top + KEY_H + 20.0), Hit::Key("enter".into()));
        assert_eq!(hit(&st, 10.0, top + KEY_H + 20.0), Hit::Key(String::new()), "a gap: no key");
        assert_eq!(hit(&st, 120.0, top - 5.0), Hit::Ime(0));
        assert_eq!(hit(&st, 120.0, 315.0), Hit::Soft(1));
        let l = take_layout(&mut st).unwrap();
        let keys = l["keys"].as_array().unwrap();
        assert_eq!(keys.len(), 6);
        assert_eq!(keys[5], serde_json::json!({"id": "enter", "x": 180.0, "y": top + KEY_H, "w": 60.0, "h": KEY_H}));
        let plain: Frame = serde_json::from_value(serde_json::json!({"screen": "app/form", "body": []})).unwrap();
        draw(&mut cv, &plain, &mut st);
        assert!(st.drawn.keys.is_empty(), "no keypad, no keys");
        assert_eq!(hit(&st, 90.0, top + 10.0), Hit::Body("app/form".into(), None));
    }

    // A touch on the screen last drawn: the bars, a candidate row, a body
    // node and a grid's tile; over a menu what it picks, its panel, the
    // soft keys under its scrim and the screen around it; a full-screen
    // overlay covers the soft keys too.
    #[test]
    fn a_touch_hits_what_is_drawn_there() {
        let mut st = State {
            drawn: Drawn {
                screen: "app/home".into(),
                width: 240.0,
                top: 38.0,
                bottom: 282.0,
                soft: 300.0,
                x: 6.0,
                w: 228.0,
                nodes: vec![(41.0, 30.0), (74.0, 80.0)],
                grids: vec![None, Some((3, 40.0, 5))],
                title: true,
                ..Drawn::default()
            },
            ..State::default()
        };
        let body = |n| Hit::Body("app/home".into(), n);
        assert_eq!(hit(&st, 120.0, 8.0), Hit::Status);
        assert_eq!(hit(&st, 120.0, 30.0), Hit::Title);
        assert_eq!(hit(&st, 120.0, 50.0), body(Some((0, 0))));
        assert_eq!(hit(&st, 120.0, 120.0), body(Some((1, 4))), "the second row's middle tile");
        assert_eq!(hit(&st, 200.0, 120.0), body(None), "past the last tile");
        assert_eq!(hit(&st, 120.0, 200.0), body(None), "below the last node");
        assert_eq!(hit(&st, 120.0, 290.0), Hit::Ime(0));
        assert_eq!(hit(&st, 10.0, 310.0), Hit::Soft(0));
        assert_eq!(hit(&st, 120.0, 310.0), Hit::Soft(1));
        assert_eq!(hit(&st, 239.0, 310.0), Hit::Soft(2));
        st.drawn.overlay_kind = "menu".into();
        st.drawn.overlay = Some(((6.0, 200.0, 228.0, 96.0), MENU_SCRIM, true));
        st.drawn.picks = vec![((9.0, 224.0, 222.0, 19.0), Pick::Item(1)), ((9.0, 243.0, 222.0, 19.0), Pick::Item(2))];
        assert_eq!(hit(&st, 120.0, 250.0), Hit::Overlay("menu".into(), Some(Pick::Item(2))));
        assert_eq!(hit(&st, 120.0, 210.0), Hit::Overlay("menu".into(), None), "the menu's title");
        assert_eq!(hit(&st, 120.0, 100.0), Hit::Outside("menu".into()));
        assert_eq!(hit(&st, 230.0, 310.0), Hit::Soft(2), "the soft keys show under the scrim");
        assert_eq!(hit(&st, 120.0, 8.0), Hit::Status);
        st.drawn.overlay_kind = "incoming".into();
        st.drawn.overlay = None;
        st.drawn.picks = vec![((6.0, 244.0, 110.0, 28.0), Pick::Choice(0)), ((124.0, 244.0, 110.0, 28.0), Pick::Choice(1))];
        assert_eq!(hit(&st, 200.0, 250.0), Hit::Overlay("incoming".into(), Some(Pick::Choice(1))));
        assert_eq!(hit(&st, 120.0, 310.0), Hit::Overlay("incoming".into(), None), "full screen: no soft keys");
        st.drawn = Drawn { width: 240.0, soft: 300.0, idle: true, ..Drawn::default() };
        assert_eq!(hit(&st, 120.0, 150.0), Hit::Idle);
        assert_eq!(hit(&st, 120.0, 310.0), Hit::Soft(1));
        st.drawn.boot = true;
        assert_eq!(hit(&st, 120.0, 310.0), Hit::Boot);
        assert_eq!(Hit::Soft(1).json(), serde_json::json!({"area": "soft", "soft": 1}));
        assert_eq!(body(Some((1, 2))).json(), serde_json::json!({"area": "body", "screen": "app/home", "node": 1, "tile": 2}));
        assert_eq!(body(None).json(), serde_json::json!({"area": "body", "screen": "app/home"}));
        assert_eq!(Hit::Overlay("perm".into(), Some(Pick::Choice(0))).json(), serde_json::json!({"area": "overlay", "kind": "perm", "choice": 0}));
        assert_eq!(Hit::Overlay("picker".into(), Some(Pick::Tile(4))).json()["tile"], 4);
        assert_eq!(Hit::Outside("menu".into()).json(), serde_json::json!({"area": "outside", "kind": "menu"}));
    }

    // What a touch picks is where it is drawn: a menu's entries by their
    // index in the frame's items (headers counted, the entries above the
    // shown ones too), a dialog's buttons, a picker's tiles.
    #[test]
    fn an_overlay_says_where_its_entries_are() {
        let Ok(fonts) = crate::canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let frame = |overlay: serde_json::Value| -> Frame {
            serde_json::from_value(serde_json::json!({"screen": "a/list", "title": "T", "soft": ["Options", "Select", "Back"], "body": [{"kind": "item", "title": "Row"}], "overlay": overlay})).unwrap()
        };
        let centre = |st: &State, p: Pick| {
            let ((x, y, w, h), _) = *st.drawn.picks.iter().find(|(_, q)| *q == p).unwrap_or_else(|| panic!("{p:?} not drawn"));
            (x + w / 2.0, y + h / 2.0)
        };
        let mut st = State::default();
        let mut cv = Canvas::new(240, 320, 1.0, fonts);
        let mut items: Vec<serde_json::Value> = vec![serde_json::json!({"header": true, "text": "Row"})];
        items.extend((0..30).map(|i| serde_json::json!({"text": format!("Entry {i}"), "sel": i == 25})));
        draw(&mut cv, &frame(serde_json::json!({"kind": "menu", "title": "Options", "items": items})), &mut st);
        assert!(!st.drawn.picks.iter().any(|(_, p)| *p == Pick::Item(0)), "the header is no entry, and scrolled off");
        let (x, y) = centre(&st, Pick::Item(26));
        assert_eq!(hit(&st, x, y), Hit::Overlay("menu".into(), Some(Pick::Item(26))), "the entry chosen, item 26 of the frame");
        let below = centre(&st, Pick::Item(25));
        assert_eq!(hit(&st, below.0, below.1 + 19.0), hit(&st, x, y), "the rows meet");
        draw(&mut cv, &frame(serde_json::json!({"kind": "confirm", "lines": ["Delete it?"], "choices": ["Delete", "Cancel"]})), &mut st);
        for i in 0..2 {
            let (x, y) = centre(&st, Pick::Choice(i));
            assert_eq!(hit(&st, x, y), Hit::Overlay("confirm".into(), Some(Pick::Choice(i))));
        }
        assert_eq!(hit(&st, 120.0, 30.0), Hit::Outside("confirm".into()));
        let tiles: Vec<serde_json::Value> = (0..6).map(|i| serde_json::json!({"title": format!("{i}")})).collect();
        draw(&mut cv, &frame(serde_json::json!({"kind": "picker", "title": "Photos", "columns": 3, "tiles": tiles})), &mut st);
        let (x, y) = centre(&st, Pick::Tile(4));
        assert_eq!(hit(&st, x, y), Hit::Overlay("picker".into(), Some(Pick::Tile(4))));
        assert_eq!(hit(&st, 120.0, 310.0), Hit::Overlay("picker".into(), None), "the picker covers the soft keys");
    }

    // The photo picker and the share sheet keep the chosen tile on screen
    // on each form factor, whole rows above their lines, with the body's
    // scroll bar at the top, at the end or between; a grid that fits is
    // drawn from its top as before, with no bar.
    #[test]
    fn a_picker_keeps_its_choice_on_screen() {
        let Ok(fonts) = crate::canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static crate::canvas::Fonts = Box::leak(Box::new(fonts));
        let photos = |n: usize, columns: i32, choice: i32| Overlay {
            kind: "picker".into(),
            title: "Choose a photo".into(),
            lines: vec!["Only the chosen one goes to Chat".into()],
            footer: vec!["OK choose · Back cancel".into()],
            tiles: (0..n).map(|i| Tile { icon: "P".into(), title: format!("Photo {i}"), focused: i as i32 == choice, ..Default::default() }).collect(),
            columns,
            choice,
            ..Default::default()
        };
        let line = |cv: &Canvas, y: f32| {
            let c = cv.pm.pixel(((cv.w - 2.0) * cv.s) as u32, (y * cv.s) as u32).unwrap();
            let l = pal().line;
            (c.red(), c.green(), c.blue()) == (l.0, l.1, l.2)
        };
        let top = STATUS_H + TITLE_H + 4.0;
        for (w, h, s, cols, fit) in [(480, 640, 2.0, 3, 3), (640, 480, 2.0, 4, 2), (800, 352, 1.6, 5, 1)] {
            let mut cv = Canvas::new(w, h, s, fonts);
            let th = (cv.w - 2.0 * PAD) / cols as f32;
            let end = top + fit as f32 * th;
            for choice in [0, 1, cols * fit, 17, 38, 39] {
                let mut picks = Vec::new();
                picker(&mut cv, &photos(40, cols, choice), &mut picks);
                let at = |i: usize| picks.iter().find(|(_, p)| *p == Pick::Tile(i)).map(|((_, y, _, _), _)| *y);
                let y = at(choice as usize).unwrap_or_else(|| panic!("{w}x{h}: tile {choice} chosen, not drawn"));
                let (row, cols, fit) = ((choice / cols) as usize, cols as usize, fit as usize);
                assert!(y >= top && y + th <= end + 0.01, "{w}x{h}: tile {choice} at {y}..{}, the grid {top}..{end}", y + th);
                assert!(picks.iter().all(|((_, y, _, h), _)| *y >= top && y + h <= end + 0.01), "{w}x{h}, tile {choice}: a tile below the grid");
                let first = row.saturating_sub(fit - 1);
                assert_eq!(at(first * cols), Some(top), "{w}x{h}, tile {choice}: row {first} first, as a menu scrolls");
                assert!(first == 0 || at(first * cols - 1).is_none(), "{w}x{h}, tile {choice}: the row above still drawn");
                let last = 40usize.div_ceil(cols) - fit;
                assert_eq!((line(&cv, top + 4.0), line(&cv, end - 4.0)), (first == 0, first == last), "{w}x{h}, tile {choice}: the bar for row {first} of {last}");
            }
            let all = cols * fit;
            let mut picks = Vec::new();
            picker(&mut cv, &photos(all as usize, cols, all - 1), &mut picks);
            assert_eq!(picks.len(), all as usize, "{w}x{h}: {all} tiles fit");
            for ((_, y, _, _), p) in &picks {
                let Pick::Tile(i) = *p else { panic!("{p:?}") };
                assert_eq!(*y, top + (i / cols as usize) as f32 * th, "{w}x{h}: tile {i} moved");
            }
            assert!(!line(&cv, top + 4.0) && !line(&cv, end - 4.0), "{w}x{h}: a bar on a grid that fits");
        }
    }
}
