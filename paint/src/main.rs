// phone-paint is our own renderer for the renderer slot (phone.frame/1,
// 23-renderer-protocol.md), written without Slint: tiny-skia for shapes and
// images, ab_glyph for text. Headless (--headless) it draws on request and
// writes PNG snapshots; otherwise it drives the panel through DRM/KMS and
// reads the keyboards through evdev.

mod bench;
mod canvas;
mod design;
mod frame;
mod gbm;
mod gpu;
mod i18n;
mod input;
mod keygate;
mod kms;
mod layers;
mod look;
mod motion;
mod pace;
mod paint;
mod seccomp;
mod skin;
mod surfaces;
mod timing;

use std::collections::VecDeque;
use std::io::{BufRead, Write};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use serde::Deserialize;

use input::Event;

const PROTO: &str = "phone.frame/1";

#[derive(Deserialize)]
struct Msg {
    t: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    proto: String,
    #[serde(default)]
    device: Option<frame::Device>,
    #[serde(default)]
    echo: bool,
    /// The side channel's file descriptor (hello): media surfaces.
    #[serde(default)]
    side_fd: Option<i32>,
    /// power: the panel on or off.
    #[serde(default)]
    on: Option<bool>,
    /// hello: say what each frame took ("drawn"), for the performance
    /// timeline; headless only.
    #[serde(default)]
    timing: bool,
    /// rate: the refresh rate phone-displayd chose.
    #[serde(default)]
    hz: u32,
}

fn proto_major(p: &str) -> Option<u32> {
    p.strip_prefix("phone.frame/")?.split('.').next()?.parse().ok()
}

fn send(v: &serde_json::Value) {
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{v}");
    let _ = out.flush();
}

/// The frame's scale: Settings › Large text draws everything bigger on the same panel.
fn zoomed(scale: f32, f: &frame::Frame) -> f32 {
    if f.zoom > 0.0 { scale * f.zoom } else { scale }
}

/// The mouse pointer, in panel pixels: shown from the first move until a
/// key is typed. While shown, the frame under it is kept so that moving it
/// redraws only the arrow, at most once a period of the panel.
struct Pointer {
    x: f32,
    y: f32,
    shown: bool,
    under: Option<tiny_skia::Pixmap>,
    drawn: Instant,
    due: bool,
}

/// The picture a transition starts from: the screen the CPU path showed,
/// or the GPU's screen picture.
enum From {
    Cpu(tiny_skia::Pixmap),
    Gpu(usize),
}

/// A transition the frame just drawn starts (37-motion.md §3); between its
/// steps a "rate" waiting in the inbox is taken.
struct Start<'a> {
    kind: motion::Kind,
    from: From,
    st: &'a paint::State,
    before: Option<(motion::Panel, f32, bool)>,
    steps: &'a mut Trace,
    inbox: &'a mut Inbox,
    pace: &'a mut pace::Pace,
}

/// Puts the frame just drawn on the panel, after the transition it starts,
/// with the pointer over it when shown; `changed`: where it differs from
/// the frame shown before, if known. With the GPU the layers are composed
/// first; if that fails, the CPU path takes the panel back for good and
/// true says the frame must be drawn again.
#[allow(clippy::too_many_arguments)]
fn present(d: &mut kms::Display, gpu: &mut Option<gpu::Gpu>, layers: &mut layers::Layers, changed: Option<&[kms::Rect]>, s: f32, fonts: &'static canvas::Fonts, p: &mut Pointer, start: Option<Start>) -> bool {
    p.under = None;
    if let Some(g) = gpu.as_mut() {
        let res = (|| -> Result<(), String> {
            let fresh = g.compose(layers)?;
            let moved = match start {
                Some(Start { kind, from: From::Gpu(old), st, before, steps, inbox, pace }) => transition_gpu(d, g, old, kind, st, before, s, steps, inbox, pace)?,
                _ => false,
            };
            if fresh || moved || p.shown {
                show_gpu(d, g, gpu::Pic::Screen(g.front()), s, fonts, p)?;
            }
            Ok(())
        })();
        return res.is_err_and(|e| give_up(d, gpu, layers, fonts, e));
    }
    let pm = layers.screen();
    if let Some(Start { kind, from: From::Cpu(old), st, before, steps, inbox, pace }) = start {
        transition(d, &old, pm, kind, st, before, s, steps, inbox, pace);
    }
    show_cpu(d, pm, changed, s, fonts, p);
    false
}

/// A picture onto the panel through the dumb buffers, with the pointer
/// over it when shown (the picture kept under it for `repoint`).
fn show_cpu(d: &mut kms::Display, pm: &tiny_skia::Pixmap, changed: Option<&[kms::Rect]>, s: f32, fonts: &'static canvas::Fonts, p: &mut Pointer) {
    p.under = None;
    if !p.shown {
        d.show(pm, changed);
        return;
    }
    p.under = Some(pm.clone());
    let mut cv = canvas::Canvas::over(pm.clone(), s, fonts);
    paint::pointer(&mut cv, p.x / s, p.y / s);
    (p.drawn, p.due) = (Instant::now(), false);
    d.show(&cv.pm, None);
}

/// The pointer on a picture of its own, its tip PAD pixels in, for the GPU
/// to lay over the screen; and where that picture's corner goes.
fn arrow(p: &Pointer, s: f32, fonts: &'static canvas::Fonts) -> Option<(tiny_skia::Pixmap, (i32, i32))> {
    const PAD: f32 = 4.0;
    let pm = tiny_skia::Pixmap::new((13.0 * s + 2.0 * PAD).ceil() as u32, (20.0 * s + 2.0 * PAD).ceil() as u32)?;
    let mut cv = canvas::Canvas::over(pm, s, fonts);
    paint::pointer(&mut cv, PAD / s, PAD / s);
    Some((cv.pm, ((p.x - PAD).round() as i32, (p.y - PAD).round() as i32)))
}

/// A GPU screen picture onto the panel, with the pointer when shown.
fn show_gpu(d: &mut kms::Display, g: &mut gpu::Gpu, which: gpu::Pic, s: f32, fonts: &'static canvas::Fonts, p: &mut Pointer) -> Result<(), String> {
    let pointer = if p.shown { arrow(p, s, fonts) } else { None };
    if p.shown {
        (p.drawn, p.due) = (Instant::now(), false);
    }
    g.show(d, which, pointer.as_ref().map(|(pm, at)| gpu::Arrow { pm, at: *at }))
}

/// The GPU failed on the way: the panel goes back to the dumb buffers and
/// the CPU path, the layers start again (the next frame is drawn whole).
fn give_up(d: &mut kms::Display, gpu: &mut Option<gpu::Gpu>, layers: &mut layers::Layers, fonts: &'static canvas::Fonts, e: String) -> bool {
    eprintln!("phone-paint: gpu: {e}; drawing on the CPU from now on");
    if let Some(g) = gpu.take() {
        g.close(d);
    }
    let (w, h) = layers.size();
    *layers = layers::Layers::new(w, h, fonts);
    true
}

/// A transition on the GPU: each step composed from the screen pictures
/// old and the one just composed, and flipped to, paced as `transition`.
/// Returns whether it ran.
#[allow(clippy::too_many_arguments)]
fn transition_gpu(d: &mut kms::Display, g: &mut gpu::Gpu, old: usize, kind: motion::Kind, st: &paint::State, before: Option<(motion::Panel, f32, bool)>, s: f32, steps: &mut Trace, inbox: &mut Inbox, pace: &mut pace::Pace) -> Result<bool, String> {
    let new = g.front();
    let Some(plan) = motion::Plan::new(kind, st, before, s, g.size().1 as usize).filter(|_| old != new) else { return Ok(false) };
    let start = Instant::now();
    let dur = kind.duration().as_secs_f32();
    loop {
        let t = start.elapsed().as_secs_f32() / dur;
        if t >= 1.0 {
            return Ok(true);
        }
        let t0 = Instant::now();
        g.transition(&plan, kind.ease(t), old, new);
        g.show(d, gpu::Pic::Step, None)?;
        steps.step(t0);
        steps.shown(t0, d);
        inbox.rates(|hz| set_rate(Some(&mut *d), &mut *pace, None, hz));
    }
}

/// What the panel shows of a frame drawn as pm: pm itself (None), or with
/// Settings › Accessibility's magnifier or colour filters on, the frame
/// drawn again zoomed and cut around the focus, and recoloured. live: for
/// the panel (secure pictures shown), else a snapshot.
#[allow(clippy::too_many_arguments)]
fn looked(pm: &tiny_skia::Pixmap, width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State, live: bool) -> Option<tiny_skia::Pixmap> {
    let look = look::Look::of(f);
    if look.plain() {
        return None;
    }
    let mut out = if look.zoom > 0.0 {
        let z = look.zoom;
        let mut cv = canvas::Canvas::new((width as f32 * z).round() as u32, (height as f32 * z).round() as u32, s * z, fonts);
        cv.mask_secure = !live;
        paint::draw(&mut cv, f, st);
        look::crop(&cv.pm, look::window((width, height), s, z, cv.focus_box), width, height)
    } else {
        pm.clone()
    };
    look.filter(&mut out);
    Some(out)
}

/// The look of the frame just drawn for the panel (`looked`), on the CPU
/// path only: while the GPU composes, the frame asks for none
/// (`gpu_for_look` closes it for one).
#[allow(clippy::too_many_arguments)]
fn look_cpu(gpu: &Option<gpu::Gpu>, layers: &layers::Layers, width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State) -> Option<tiny_skia::Pixmap> {
    gpu.is_none().then(|| looked(layers.screen(), width, height, s, fonts, f, st, true)).flatten()
}

/// Puts the frame just drawn (`redraw`, `changed`) on the panel as
/// `present` does without a transition, through Settings › Accessibility's
/// look when one is on; true as `present`.
#[allow(clippy::too_many_arguments)]
fn show(d: &mut kms::Display, gpu: &mut Option<gpu::Gpu>, layers: &mut layers::Layers, changed: &[kms::Rect], width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State, p: &mut Pointer) -> bool {
    match look_cpu(gpu, layers, width, height, s, fonts, f, st) {
        Some(out) => {
            show_cpu(d, &out, None, s, fonts, p);
            false
        }
        None => present(d, gpu, layers, Some(changed), s, fonts, p, None),
    }
}

/// What the GPU path does about a frame's look (look.rs): the colour
/// filters and the magnifier are applied to the CPU's picture, so the GPU
/// is closed while one is on and starts again once they are all off.
#[derive(Debug, PartialEq)]
enum ForLook {
    Keep,
    Close,
    Reopen,
}

/// plain: the frame asks for no filter or magnifier; on: the GPU
/// composes; closed: it was closed for a look.
fn for_look(plain: bool, on: bool, closed: bool) -> ForLook {
    match (plain, on, closed) {
        (false, true, _) => ForLook::Close,
        (true, false, true) => ForLook::Reopen,
        _ => ForLook::Keep,
    }
}

/// Closes or restarts the GPU path for the look of the frame about to be
/// drawn (`for_look`), the layers starting again either way; true if they
/// did (nothing on the panel to start a transition from).
fn gpu_for_look(d: &mut kms::Display, gpu: &mut Option<gpu::Gpu>, closed: &mut bool, layers: &mut layers::Layers, plain: bool, fonts: &'static canvas::Fonts) -> bool {
    let (w, h) = layers.size();
    match for_look(plain, gpu.is_some(), *closed) {
        ForLook::Keep => false,
        ForLook::Close => {
            if let Some(g) = gpu.take() {
                g.close(d);
            }
            (*closed, *layers) = (true, layers::Layers::new(w, h, fonts));
            true
        }
        ForLook::Reopen => {
            *closed = false;
            match gpu::Gpu::on_panel(d, w, h) {
                Ok(g) => {
                    (*gpu, *layers) = (Some(g), layers::Layers::split(w, h, fonts));
                    true
                }
                Err(e) => {
                    eprintln!("phone-paint: gpu: {e}; drawing on the CPU");
                    false
                }
            }
        }
    }
}

/// Draws the current frame on the live display: recorded, then painted
/// where it differs from the one before (layers.rs). Returns where the
/// screen changed.
fn redraw(layers: &mut layers::Layers, width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State) -> Vec<kms::Rect> {
    let mut cv = canvas::Canvas::recording(width, height, s, fonts);
    cv.mask_secure = false;
    paint::draw(&mut cv, f, st);
    let px = |v: i32| v.max(0) as usize;
    layers.update(cv.take(), s).screen.iter().map(|r| kms::Rect { x0: px(r.x0), y0: px(r.y0), x1: px(r.x1), y1: px(r.y1) }).collect()
}

/// What the shell sent and the keyboards reported, in order: the events
/// put aside while frames were coalesced or an animation ran come first.
struct Inbox {
    rx: mpsc::Receiver<Event>,
    held: VecDeque<Event>,
}

impl Inbox {
    /// The next event waiting, if one is.
    fn queued(&mut self) -> Option<Event> {
        self.held.pop_front().or_else(|| self.rx.try_recv().ok())
    }

    /// Between an animation's steps: each "rate" waiting goes to apply at
    /// once (the high rate the animation asked for, so that it runs at it),
    /// the rest is kept, in order, for after it.
    fn rates(&mut self, mut apply: impl FnMut(u32)) {
        while let Ok(ev) = self.rx.try_recv() {
            self.held.push_back(ev);
        }
        let mut rest = VecDeque::with_capacity(self.held.len());
        for ev in self.held.drain(..) {
            match &ev {
                Event::Line(l) if l.contains("\"rate\"") => match serde_json::from_str::<Msg>(l) {
                    Ok(m) if m.t == "rate" => apply(m.hz),
                    _ => rest.push_back(ev),
                },
                _ => rest.push_back(ev),
            }
        }
        self.held = rest;
    }

    /// The frames and patches waiting right behind one just taken, merged
    /// into doc: drawn as one, since the panel would not show them all.
    /// Stops at anything else, which stays first.
    fn frames(&mut self, doc: &mut serde_json::Value, mut dump: Option<&mut std::fs::File>) {
        while let Some(next) = self.queued() {
            let frame = match &next {
                Event::Line(l) => serde_json::from_str::<serde_json::Value>(l).ok().filter(|v| matches!(v["t"].as_str(), Some("frame" | "patch"))),
                _ => None,
            };
            let Some(v) = frame else {
                self.held.push_front(next);
                return;
            };
            let t = v["t"].as_str().unwrap_or_default().to_string();
            take_frame(doc, &t, v, dump.as_deref_mut());
        }
    }
}

/// The shell's "rate": the panel goes to the mode of hz where it has one,
/// and motion follows; the answer is the rate the panel runs at.
fn set_rate(display: Option<&mut kms::Display>, pace: &mut pace::Pace, timing: Option<&mut timing::Timing>, hz: u32) {
    if !pace.set(hz) {
        return;
    }
    if let Some(t) = timing {
        t.set_rate(hz);
    }
    let (got, err) = match display {
        Some(d) => match d.set_rate(hz) {
            Ok(r) => (r, String::new()),
            Err(e) => (d.rate(), e),
        },
        None => (hz, String::new()),
    };
    if got != hz {
        pace.set(got);
    }
    send(&serde_json::json!({"t": "rate", "hz": got, "err": err}));
}

/// Runs a transition from the frame on the panel to the one just drawn
/// (37-motion.md): each step is rows of the two pictures copied at an
/// offset, paced by the panel (`kms::Display::show` waits for the flip
/// before, or else a period of the rate it runs at).
#[allow(clippy::too_many_arguments)]
fn transition(d: &mut kms::Display, old: &tiny_skia::Pixmap, new: &tiny_skia::Pixmap, kind: motion::Kind, st: &paint::State, before: Option<(motion::Panel, f32, bool)>, s: f32, trace: &mut Trace, inbox: &mut Inbox, pace: &mut pace::Pace) {
    let Some(prep) = motion::steps(kind, old, new, st, before, s) else { return };
    let Some(mut out) = tiny_skia::Pixmap::new(new.width(), new.height()) else { return };
    // After the first step only the rows that move change: the panel need
    // not compare whole frames to find them.
    let (top, bottom) = motion::moving_rows(kind, st, s, new.height() as usize);
    let moving = [kms::Rect { x0: 0, y0: top, x1: new.width() as usize, y1: bottom }];
    let start = Instant::now();
    let dur = kind.duration().as_secs_f32();
    let mut first = true;
    loop {
        let t = start.elapsed().as_secs_f32() / dur;
        if t >= 1.0 {
            break;
        }
        let t0 = Instant::now();
        prep(out.data_mut(), kind.ease(t));
        trace.step(t0);
        let t0 = Instant::now();
        d.show(&out, (!first).then_some(&moving[..]));
        trace.shown(t0, d);
        first = false;
        inbox.rates(|hz| set_rate(Some(&mut *d), &mut *pace, None, hz));
    }
}

/// Step times, in ms: the draws (recording and painting the damage) and
/// the share of the screen they painted, the transition steps (composing),
/// the hand-overs to the panel (copying into the scan-out buffer, asking
/// for the flip), the waits before them (for the flip before, or a period
/// of the panel), the gaps between frames shown, and how many of those
/// were later than a period and a half of the rate the panel ran at.
#[derive(Default)]
struct Times {
    draws: Vec<f32>,
    painted: Vec<f32>,
    steps: Vec<f32>,
    shown: Vec<f32>,
    waits: Vec<f32>,
    gaps: Vec<f32>,
    late: usize,
}

impl Times {
    fn add(&mut self, o: &Times) {
        for (a, b) in [(&mut self.draws, &o.draws), (&mut self.painted, &o.painted), (&mut self.steps, &o.steps), (&mut self.shown, &o.shown), (&mut self.waits, &o.waits), (&mut self.gaps, &o.gaps)] {
            a.extend_from_slice(b);
        }
        self.late += o.late;
    }

    fn summary(&self) -> String {
        let q = quantile;
        format!(
            "{} draws (p50 {:.1} ms, p95 {:.1} ms, max {:.1} ms, p50 {:.0}% of the screen painted), {} transition steps (p50 {:.1} ms, p95 {:.1} ms), {} frames handed over (p50 {:.1} ms, p95 {:.1} ms; waiting before them p50 {:.1} ms, p95 {:.1} ms), a frame every p50 {:.1} ms, p95 {:.1} ms, {} later than a frame and a half",
            self.draws.len(),
            q(&self.draws, 0.5),
            q(&self.draws, 0.95),
            q(&self.draws, 1.0),
            q(&self.painted, 0.5),
            self.steps.len(),
            q(&self.steps, 0.5),
            q(&self.steps, 0.95),
            self.shown.len(),
            q(&self.shown, 0.5),
            q(&self.shown, 0.95),
            q(&self.waits, 0.5),
            q(&self.waits, 0.95),
            q(&self.gaps, 0.5),
            q(&self.gaps, 0.95),
            self.late,
        )
    }
}

fn quantile(v: &[f32], p: f32) -> f32 {
    let mut s = v.to_vec();
    s.sort_by(|a, b| a.total_cmp(b));
    s.get(((s.len() as f32 - 1.0) * p).round() as usize).copied().unwrap_or(0.0)
}

/// Whether a frame shown `gap` after the one before came a frame late on
/// a panel whose frames last `period` (60 Hz: later than 25 ms).
fn late(gap: Duration, period: Duration) -> bool {
    gap > period * 3 / 2
}

/// What the live display's motion costs (37-motion.md §5): with
/// PHONE_PAINT_TRACE set, a line on stderr after each burst of motion
/// (the frames until it settles), with the sums of every frame so far, and
/// of the frames due to a timer alone (the cursor, a spinner, the charging
/// battery, an overlay's phase); with PHONE_PAINT_STEPS=<file>, each frame
/// a line there as the headless renderer writes them (timing.rs), with the
/// hand-overs and the gaps.
struct Trace {
    on: bool,
    out: Option<std::fs::File>,
    burst: Times,
    all: Times,
    ticks: Times,
    last: Option<Instant>,
}

fn ms(d: Duration) -> f32 {
    d.as_secs_f32() * 1000.0
}

impl Trace {
    /// Opened before the sandbox: the file is not reachable after it.
    fn new(live: bool) -> Trace {
        let out = std::env::var_os("PHONE_PAINT_STEPS").filter(|_| live).and_then(|p| std::fs::OpenOptions::new().create(true).append(true).open(p).ok());
        Trace { on: std::env::var_os("PHONE_PAINT_TRACE").is_some(), out, burst: Times::default(), all: Times::default(), ticks: Times::default(), last: None }
    }

    fn drawn(&mut self, t0: Instant, share: f32) {
        self.burst.draws.push(ms(t0.elapsed()));
        self.burst.painted.push(share * 100.0);
    }

    fn step(&mut self, t0: Instant) {
        self.burst.steps.push(ms(t0.elapsed()));
    }

    /// A frame handed to the panel from t0 on.
    fn shown(&mut self, t0: Instant, d: &mut kms::Display) {
        let now = Instant::now();
        let wait = d.waited();
        self.burst.shown.push(ms(now.duration_since(t0).saturating_sub(wait)));
        self.burst.waits.push(ms(wait));
        if let Some(l) = self.last {
            let gap = now.duration_since(l);
            self.burst.gaps.push(ms(gap));
            self.burst.late += usize::from(late(gap, d.period()));
        }
        self.last = Some(now);
    }

    /// The burst is over.
    fn end(&mut self) {
        let b = std::mem::take(&mut self.burst);
        self.last = None;
        if b.draws.is_empty() {
            return;
        }
        if let Some(f) = self.out.as_mut() {
            let _ = writeln!(f, "{}", serde_json::json!({"draw": b.draws, "transition": b.steps, "present": b.shown, "gap": b.gaps}));
        }
        if !self.on {
            return;
        }
        self.all.add(&b);
        // A frame that set nothing moving goes into the sums only.
        if b.draws.len() > 1 || !b.steps.is_empty() {
            self.report(&format!("motion: {}", b.summary()));
        }
    }

    fn report(&self, what: &str) {
        let t = &self.ticks;
        eprintln!(
            "phone-paint: {what}; so far: {}; timer frames: {} draws (p50 {:.1} ms, p95 {:.1} ms), handed over p50 {:.1} ms, p95 {:.1} ms",
            self.all.summary(),
            t.draws.len(),
            quantile(&t.draws, 0.5),
            quantile(&t.draws, 0.95),
            quantile(&t.shown, 0.5),
            quantile(&t.shown, 0.95)
        );
    }

    /// A frame due to a timer alone, drawn from t0 and shown from t1; the
    /// sums every TICKS_REPORTED of them.
    fn tick(&mut self, t0: Instant, t1: Instant, d: &mut kms::Display) {
        if !self.on {
            return;
        }
        let wait = d.waited();
        self.ticks.draws.push(ms(t1.duration_since(t0)));
        self.ticks.shown.push(ms(t1.elapsed().saturating_sub(wait)));
        if self.ticks.draws.len().is_multiple_of(TICKS_REPORTED) {
            self.report("timer frames");
        }
    }
}

/// About every two seconds of an overlay's phase running.
const TICKS_REPORTED: usize = 120;

/// Moves the arrow over the frame kept under it (with the GPU, over its
/// screen picture); true as `present`.
fn repoint(d: &mut kms::Display, gpu: &mut Option<gpu::Gpu>, layers: &mut layers::Layers, p: &mut Pointer, scale: f32, fonts: &'static canvas::Fonts) -> bool {
    if let Some(g) = gpu.as_mut() {
        let res = show_gpu(d, g, gpu::Pic::Screen(g.front()), scale, fonts, p);
        (p.drawn, p.due) = (Instant::now(), false);
        return res.is_err_and(|e| give_up(d, gpu, layers, fonts, e));
    }
    let Some(under) = &p.under else { return false };
    let mut cv = canvas::Canvas::over(under.clone(), scale, fonts);
    if p.shown {
        paint::pointer(&mut cv, p.x / scale, p.y / scale);
    }
    d.show(&cv.pm, None);
    (p.drawn, p.due) = (Instant::now(), false);
    false
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if let Some(i) = args.iter().position(|a| a == "--bench") {
        let size = args.iter().position(|a| a == "--size").and_then(|i| args.get(i + 1)).map_or("480x640@2", |s| s.as_str());
        if let Err(e) = bench::run(args.get(i + 1).map_or("", |s| s.as_str()), size) {
            eprintln!("phone-paint: bench: {e}");
            std::process::exit(2);
        }
        return;
    }
    let snap_dir = args.iter().position(|a| a == "--snap-dir").and_then(|i| args.get(i + 1).cloned()).unwrap_or_else(|| ".".into());
    let headless = args.iter().any(|a| a == "--headless");
    // --skin: every snapshot also as <name>.skin.png inside a phone body.
    let skin = args.iter().any(|a| a == "--skin");
    let mut first = String::new();
    let hello: Option<Msg> = std::io::stdin().lock().read_line(&mut first).ok().and_then(|_| serde_json::from_str(&first).ok());
    let shell_proto = hello.as_ref().map(|m| m.proto.clone()).unwrap_or_default();
    if proto_major(&shell_proto) != proto_major(PROTO) {
        eprintln!("phone-paint: the shell speaks {shell_proto:?}, this renderer {PROTO}");
        std::process::exit(3);
    }
    let side_fd = hello.as_ref().and_then(|m| m.side_fd);
    let report = headless && hello.as_ref().is_some_and(|m| m.timing);
    let dev = hello.and_then(|m| m.device).unwrap_or_default();
    let (mut width, mut height, scale) = if dev.width > 0 { (dev.width, dev.height, dev.scale) } else { (480, 640, 2.0) };
    // On the device the panel's mode decides the size, not the profile.
    let mut display = None;
    if !headless {
        match kms::Display::open() {
            Ok(mut d) => {
                // A portrait panel in a landscape phone (24-sku-framework.md):
                // drawn at the device's size, turned onto the panel.
                if dev.width > 0 && d.sideways(dev.width, dev.height) {
                    d.rotated = true;
                } else if let Some(o) = kms::letterbox((d.width, d.height), (dev.width, dev.height)) {
                    // A monitor bigger than the device's screen: the device's
                    // screen in its middle (24-sku-framework.md §4).
                    d.origin = o;
                } else {
                    (width, height) = (d.width, d.height);
                }
                display = Some(d);
            }
            Err(e) => {
                eprintln!("phone-paint: display: {e}");
                std::process::exit(5);
            }
        }
    }
    let fonts: &'static canvas::Fonts = match canvas::Fonts::load() {
        Ok(f) => Box::leak(Box::new(f)),
        Err(e) => {
            eprintln!("phone-paint: {e}");
            std::process::exit(4);
        }
    };
    let mut features = if headless { vec!["snap", "images", "patch", "layout", "power", "timing", "phase", "rate"] } else { vec!["kms", "keys", "images", "patch", "anim", "layout", "power", "mouse", "pointer", "phase", "rate"] };
    // Before any thread: the input opener is forked from this process.
    let mut inputs = None;
    if display.is_some() {
        match input::start(dev.keymap.keys().copied().collect()) {
            Ok(sock) => inputs = Some(sock),
            Err(e) => eprintln!("phone-paint: input: {e}"),
        }
    }
    if let Some(fd) = side_fd {
        surfaces::listen(fd);
        features.push("shm");
    }
    // PHONE_PAINT_DUMP: every frame as merged, a line each, for --bench.
    let mut dump = std::env::var_os("PHONE_PAINT_DUMP").and_then(|p| std::fs::OpenOptions::new().create(true).append(true).open(p).ok());
    // PHONE_PAINT_STEPS or the hello's timing (headless): the live display's
    // steps timed (timing.rs).
    let steps_file = std::env::var_os("PHONE_PAINT_STEPS").filter(|_| display.is_none());
    let mut timing = if display.is_none() && (steps_file.is_some() || report) { timing::Timing::open(steps_file.as_deref(), width, height, scale, fonts) } else { None };
    // Frames received ("frame" and "patch"), numbered as the Shell sends them.
    let mut received = 0u64;
    // On a panel: what its motion costs (PHONE_PAINT_TRACE, PHONE_PAINT_STEPS).
    let mut trace = Trace::new(display.is_some());
    if display.is_some() {
        match kms::drop_privileges() {
            Ok(got) => features.extend(got),
            Err(e) => {
                eprintln!("phone-paint: sandbox: {e}");
                std::process::exit(6);
            }
        }
    }
    // PHONE_PAINT_GPU=1: the GPU composes (gpu.rs). It starts inside the
    // sandbox, so Mesa's threads are confined like the rest; what it cannot
    // reach there (a render node of its own) it does without, and then
    // the CPU draws as always.
    let mut gpu = None;
    if let Some(d) = display.as_ref().filter(|_| std::env::var("PHONE_PAINT_GPU").is_ok_and(|v| v == "1")) {
        match gpu::Gpu::on_panel(d, width, height) {
            Ok(g) => {
                features.push("gpu");
                gpu = Some(g);
            }
            Err(e) => eprintln!("phone-paint: gpu: {e}; drawing on the CPU"),
        }
    }
    send(&serde_json::json!({
        "t": "ready",
        "proto": PROTO,
        "renderer": {
            "name": env!("CARGO_PKG_NAME"),
            "version": env!("CARGO_PKG_VERSION"),
            "license": env!("CARGO_PKG_LICENSE"),
            "toolkit": "tiny-skia + ab_glyph",
        },
        "features": features,
        "display": {"width": width, "height": height, "scale": scale},
    }));
    let mut current = frame::Frame::default();
    let mut doc = serde_json::Value::Null;
    let mut state = paint::State::default();
    let (events, rx) = mpsc::channel();
    let mut inbox = Inbox { rx, held: VecDeque::new() };
    // The keys' timing, as the shell sends it ("keys"); the keyboards'
    // threads read it.
    let keytime: &'static keygate::KeyTiming = Box::leak(Box::default());
    if let Some(sock) = inputs {
        kms::read_inputs(sock, dev.keypad == "t9", dev.fn_layer.clone(), dev.ctrl.clone(), dev.keymap.clone(), send, events.clone(), keytime);
    }
    std::thread::spawn(move || {
        for line in std::io::stdin().lines() {
            let Ok(line) = line else { break };
            if events.send(Event::Line(line)).is_err() {
                return;
            }
        }
        let _ = events.send(Event::Eof);
    });
    let mut ptr = Pointer { x: width as f32 / 2.0, y: height as f32 / 2.0, shown: false, under: None, drawn: Instant::now(), due: false };
    // Off, frames only update the document; the one current when the panel
    // comes back is drawn then.
    let mut lit = true;
    let mut stale = false;
    // The live display's pictures (split for the GPU); whether their screen
    // is what the panel shows (a transition starts from it), and its scale;
    // the transition last run; whether the frame is to be drawn again (the
    // GPU gave up).
    let mut layers = if gpu.is_some() { layers::Layers::split(width, height, fonts) } else { layers::Layers::new(width, height, fonts) };
    let mut on_panel = false;
    let mut last_s = scale;
    let mut last_tn = 0u64;
    let mut redo = false;
    // The GPU was closed for Settings › Accessibility's look (gpu_for_look).
    let mut gpu_closed = false;
    // The rate the panel runs at, until the shell's "rate" changes it.
    let mut pace = pace::Pace::default();
    if let Some(d) = display.as_ref() {
        pace.set(d.rate());
    }
    loop {
        // Due without a message: the pointer moved meanwhile, or the text
        // cursor or a spinner changes (only that is painted again).
        let tick = display.as_ref().filter(|_| lit && on_panel && current.motion != "off").and_then(|_| paint::next_tick(&state, Instant::now()));
        let tick = if redo && lit { Some(Instant::now()) } else { tick };
        let wake = [ptr.due.then(|| ptr.drawn + pace.period()), tick].into_iter().flatten().min();
        let ev = if let Some(ev) = inbox.held.pop_front() {
            ev
        } else {
            match wake {
                None => match inbox.rx.recv() {
                    Ok(ev) => ev,
                    Err(_) => break,
                },
                Some(at) => match inbox.rx.recv_timeout(at.saturating_duration_since(Instant::now())) {
                    Ok(ev) => ev,
                    Err(mpsc::RecvTimeoutError::Timeout) => {
                        if let Some(d) = display.as_mut() {
                            let s = zoomed(scale, &current);
                            if ptr.due && ptr.drawn.elapsed() >= pace.period() {
                                redo |= repoint(d, &mut gpu, &mut layers, &mut ptr, s, fonts);
                            }
                            if tick.is_some_and(|t| Instant::now() >= t) {
                                let t0 = Instant::now();
                                state.now = if current.motion == "off" { None } else { Some(t0) };
                                redo = false;
                                let changed = redraw(&mut layers, width, height, s, fonts, &current, &mut state);
                                let t1 = Instant::now();
                                redo |= show(d, &mut gpu, &mut layers, &changed, width, height, s, fonts, &current, &mut state, &mut ptr);
                                on_panel = !redo;
                                trace.tick(t0, t1, d);
                            }
                        }
                        continue;
                    }
                    Err(mpsc::RecvTimeoutError::Disconnected) => break,
                },
            }
        };
        let line = match ev {
            Event::Line(line) => line,
            Event::Eof => break,
            Event::Move(dx, dy) => {
                let Some(d) = display.as_mut().filter(|_| lit) else { continue };
                ptr.x = (ptr.x + dx as f32).clamp(0.0, width as f32 - 1.0);
                ptr.y = (ptr.y + dy as f32).clamp(0.0, height as f32 - 1.0);
                if !ptr.shown {
                    ptr.shown = true;
                    state.now = None;
                    let s = zoomed(scale, &current);
                    let changed = redraw(&mut layers, width, height, s, fonts, &current, &mut state);
                    redo |= show(d, &mut gpu, &mut layers, &changed, width, height, s, fonts, &current, &mut state, &mut ptr);
                } else if ptr.drawn.elapsed() >= pace.period() {
                    redo |= repoint(d, &mut gpu, &mut layers, &mut ptr, zoomed(scale, &current), fonts);
                } else {
                    ptr.due = true;
                }
                continue;
            }
            Event::Click => {
                let s = zoomed(scale, &current);
                let hit = if ptr.shown && lit { paint::click(&state, ptr.x / s, ptr.y / s) } else { paint::Click::Key("enter") };
                match hit {
                    paint::Click::Node(screen, node, tile) => send(&serde_json::json!({"t": "tap", "screen": screen, "node": node, "tile": tile})),
                    paint::Click::Key(key) => send(&serde_json::json!({"t": "key", "key": key, "text": ""})),
                    paint::Click::Nothing => {}
                }
                continue;
            }
            Event::Typed => {
                if ptr.shown {
                    ptr.shown = false;
                    if let Some(d) = display.as_mut() {
                        redo |= repoint(d, &mut gpu, &mut layers, &mut ptr, zoomed(scale, &current), fonts);
                    }
                    ptr.under = None;
                }
                continue;
            }
        };
        let Ok(m) = serde_json::from_str::<Msg>(&line) else { continue };
        match m.t.as_str() {
            "frame" | "patch" => {
                received += 1;
                let t_in = Instant::now();
                let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) else { continue };
                take_frame(&mut doc, &m.t, v, dump.as_mut());
                // On the panel, frames waiting already go into this one: no
                // more are drawn than it shows.
                if display.is_some() && lit {
                    inbox.frames(&mut doc, dump.as_mut());
                }
                match serde_json::from_value::<frame::Frame>(doc.clone()) {
                    Ok(f) => {
                        current = f;
                        if let Some(d) = display.as_mut() {
                            if gpu_for_look(d, &mut gpu, &mut gpu_closed, &mut layers, look::Look::of(&current).plain(), fonts) {
                                on_panel = false;
                            }
                        }
                        if display.is_none() {
                            let parsed = timing::ms(t_in);
                            let t_layout = Instant::now();
                            let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                            paint::layout(&mut cv, &current, &mut state);
                            let laid_out = timing::ms(t_layout);
                            if let Some(steps) = timing.as_mut().map(|t| t.frame(&current)).filter(|_| report) {
                                send(&serde_json::json!({"t": "drawn", "n": received, "parse_ms": parsed, "layout_ms": laid_out,
                                    "draw_ms": steps.draw, "transition_ms": steps.transition}));
                            }
                        }
                        let fresh = current.transition.as_ref().filter(|t| t.n != last_tn).map(|t| t.kind.clone());
                        if let Some(t) = &current.transition {
                            last_tn = t.n;
                        }
                        if !lit {
                            stale = true;
                        } else if let Some(d) = display.as_mut() {
                            let still = current.motion == "off";
                            // Not through the magnifier or a colour filter:
                            // a transition moves the pictures as drawn.
                            let mut kind = fresh.and_then(|k| motion::Kind::parse(&k)).filter(|_| current.motion.is_empty() && look::Look::of(&current).plain());
                            let before = paint::overlay_panel(&state, last_s);
                            // Frames until the motion settles (a scroll, a
                            // focus glide or a banner is under a fifth of a
                            // second).
                            loop {
                                let t0 = Instant::now();
                                state.now = if still { None } else { Some(t0) };
                                let s = zoomed(scale, &current);
                                let from = kind.filter(|_| on_panel).map(|_| match gpu.as_ref() {
                                    Some(g) => From::Gpu(g.front()),
                                    None => From::Cpu(layers.screen().clone()),
                                });
                                let changed = redraw(&mut layers, width, height, s, fonts, &current, &mut state);
                                let painted: usize = changed.iter().map(|r| (r.x1 - r.x0) * (r.y1 - r.y0)).sum();
                                trace.drawn(t0, painted as f32 / (width * height) as f32);
                                let look = look_cpu(&gpu, &layers, width, height, s, fonts, &current, &mut state);
                                if !still && (from.is_some() || paint::animating(&state)) {
                                    if let Some(m) = pace.motion(true) {
                                        send(&m);
                                    }
                                }
                                let start = kind.take().zip(from).map(|(kind, from)| Start { kind, from, st: &state, before, steps: &mut trace, inbox: &mut inbox, pace: &mut pace });
                                last_s = s;
                                let t1 = Instant::now();
                                let gave_up = match &look {
                                    Some(out) => {
                                        show_cpu(d, out, None, s, fonts, &mut ptr);
                                        false
                                    }
                                    None => present(d, &mut gpu, &mut layers, Some(&changed), s, fonts, &mut ptr, start),
                                };
                                if gave_up {
                                    (redo, on_panel) = (true, false);
                                    break;
                                }
                                if !changed.is_empty() {
                                    trace.shown(t1, d);
                                }
                                on_panel = true;
                                if still || !paint::animating(&state) {
                                    break;
                                }
                                inbox.rates(|hz| set_rate(Some(&mut *d), &mut pace, None, hz));
                                // A step that changed nothing waited for no
                                // flip.
                                if changed.is_empty() {
                                    std::thread::sleep(pace.period());
                                }
                            }
                            if let Some(m) = pace.motion(false) {
                                send(&m);
                            }
                            trace.end();
                        }
                    }
                    Err(e) => eprintln!("phone-paint: bad frame: {e}"),
                }
                if let Some(l) = paint::take_layout(&mut state) {
                    send(&serde_json::json!({"t": "layout", "layout": l}));
                }
            }
            "snap" => {
                // A snapshot is the settled picture, as the protocol asks.
                let live = state.now.take();
                let s = zoomed(scale, &current);
                let mut cv = canvas::Canvas::new(width, height, s, fonts);
                paint::draw(&mut cv, &current, &mut state);
                if let Some(out) = looked(&cv.pm, width, height, s, fonts, &current, &mut state, false) {
                    cv.pm = out;
                }
                state.now = live;
                let path = std::path::Path::new(&snap_dir).join(format!("{}.png", m.name));
                let res = cv.png().and_then(|png| {
                    std::fs::create_dir_all(&snap_dir).map_err(|e| e.to_string())?;
                    std::fs::write(&path, png).map_err(|e| e.to_string())?;
                    if skin {
                        let body = skin::skinned(&cv.pm, &dev.keypad, cv.s, fonts);
                        std::fs::write(path.with_extension("skin.png"), body.png()?).map_err(|e| e.to_string())?;
                    }
                    Ok(())
                });
                let mut reply = serde_json::json!({"t": "snapped", "name": m.name, "err": res.err().unwrap_or_default()});
                if m.echo {
                    reply["frame"] = doc.clone();
                }
                send(&reply);
            }
            "power" => {
                let on = m.on.unwrap_or(true);
                let res = display.as_mut().map_or(Ok(()), |d| d.power(on));
                if res.is_ok() {
                    lit = on;
                }
                if !lit {
                    on_panel = false;
                }
                if lit && stale {
                    stale = false;
                    if let Some(d) = display.as_mut() {
                        state.now = None;
                        let s = zoomed(scale, &current);
                        let changed = redraw(&mut layers, width, height, s, fonts, &current, &mut state);
                        redo |= show(d, &mut gpu, &mut layers, &changed, width, height, s, fonts, &current, &mut state, &mut ptr);
                        on_panel = !redo;
                    }
                }
                send(&serde_json::json!({"t": "power", "on": lit, "err": res.err().unwrap_or_default()}));
            }
            "rate" => set_rate(display.as_mut(), &mut pace, timing.as_mut(), m.hz),
            "keys" => {
                if let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) {
                    keytime.set(&v);
                }
            }
            "quit" => break,
            _ => {}
        }
    }
}

/// A frame or a patch of one into doc, the frame as merged into the dump.
fn take_frame(doc: &mut serde_json::Value, t: &str, v: serde_json::Value, dump: Option<&mut std::fs::File>) {
    if t == "frame" {
        *doc = v;
    } else if let Some(p) = v.get("patch") {
        frame::merge(doc, p);
    }
    if let Some(f) = dump {
        let _ = writeln!(f, "{doc}");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn inbox(lines: &[&str]) -> (Inbox, mpsc::Sender<Event>) {
        let (tx, rx) = mpsc::channel();
        for l in lines {
            tx.send(Event::Line(l.to_string())).unwrap();
        }
        (Inbox { rx, held: VecDeque::new() }, tx)
    }

    fn rest(inbox: &mut Inbox) -> Vec<String> {
        let mut out = Vec::new();
        while let Some(ev) = inbox.queued() {
            out.push(match ev {
                Event::Line(l) => l,
                _ => "?".into(),
            });
        }
        out
    }

    // An arrow key held: the frames queued behind the one taken are drawn
    // as one; a snapshot asked for in between stays where it was, before
    // the frames after it.
    #[test]
    fn queued_frames_are_drawn_as_one() {
        let (mut ib, _tx) = inbox(&[
            r#"{"t":"patch","patch":{"focus":2}}"#,
            r#"{"t":"patch","patch":{"focus":3,"title":"B"}}"#,
            r#"{"t":"snap","name":"x"}"#,
            r#"{"t":"patch","patch":{"focus":4}}"#,
        ]);
        let mut doc = serde_json::json!({"t": "frame", "title": "A", "focus": 1});
        ib.frames(&mut doc, None);
        assert_eq!((doc["focus"].as_i64(), doc["title"].as_str()), (Some(3), Some("B")));
        assert_eq!(rest(&mut ib), [r#"{"t":"snap","name":"x"}"#, r#"{"t":"patch","patch":{"focus":4}}"#]);
    }

    // The high rate an animation asked for comes while it runs: it is
    // taken between two steps, ahead of a frame queued before it, which
    // keeps its place.
    #[test]
    fn a_rate_is_taken_between_steps() {
        let (mut ib, tx) = inbox(&[r#"{"t":"patch","patch":{"focus":2}}"#, r#"{"hz":120,"t":"rate"}"#]);
        ib.held.push_back(Event::Line(r#"{"t":"snap","name":"y"}"#.into()));
        let mut got = Vec::new();
        ib.rates(|hz| got.push(hz));
        tx.send(Event::Line(r#"{"hz":60,"t":"rate"}"#.into())).unwrap();
        ib.rates(|hz| got.push(hz));
        assert_eq!(got, [120, 60]);
        assert_eq!(rest(&mut ib), [r#"{"t":"snap","name":"y"}"#, r#"{"t":"patch","patch":{"focus":2}}"#]);
    }

    // A colour filter or the magnifier on closes the GPU (it cannot apply
    // them yet) and the CPU draws; once they are all off it starts again;
    // a GPU that gave up or never started stays off.
    #[test]
    fn the_gpu_steps_aside_for_a_look() {
        assert_eq!(for_look(true, true, false), ForLook::Keep, "plain on the GPU");
        assert_eq!(for_look(false, true, false), ForLook::Close, "a filter on the GPU");
        assert_eq!(for_look(false, false, true), ForLook::Keep, "still filtered: stays closed");
        assert_eq!(for_look(true, false, true), ForLook::Reopen, "plain again");
        assert_eq!(for_look(true, false, false), ForLook::Keep, "no GPU to start: it gave up or was never asked for");
        assert_eq!(for_look(false, false, false), ForLook::Keep, "a filter on the CPU path");
    }

    // With Settings › Accessibility's look off a frame's picture is the
    // frame as drawn (snapshots stay byte for byte); the magnifier draws
    // it again twice the size around the focus; the filters recolour.
    #[test]
    fn the_look_of_a_frame() {
        let Ok(fonts) = canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static canvas::Fonts = Box::leak(Box::new(fonts));
        let frame = |extra: serde_json::Value| -> frame::Frame {
            let mut v = serde_json::json!({"screen": "a/list", "title": "Settings", "focus": 5, "body": (0..8).map(|i| serde_json::json!({"kind": "item", "title": format!("Row {i}"), "focused": i == 5})).collect::<Vec<_>>()});
            frame::merge(&mut v, &extra);
            serde_json::from_value(v).unwrap()
        };
        let (w, h, s) = (240, 320, 1.0);
        let mut st = paint::State::default();
        let mut cv = canvas::Canvas::new(w, h, s, fonts);
        let plain = frame(serde_json::json!({}));
        paint::draw(&mut cv, &plain, &mut st);
        assert!(looked(&cv.pm, w, h, s, fonts, &plain, &mut st, false).is_none(), "nothing to do: the drawn picture goes out");
        let zoomed = frame(serde_json::json!({"magnify": 2}));
        let out = looked(&cv.pm, w, h, s, fonts, &zoomed, &mut st, false).expect("magnified");
        let mut big = canvas::Canvas::new(2 * w, 2 * h, 2.0 * s, fonts);
        paint::draw(&mut big, &plain, &mut st);
        let at = look::window((w, h), s, 2.0, big.focus_box);
        assert!(at.1 > 0, "the focused row is low on the screen: the window moved down");
        assert_eq!(out.data(), look::crop(&big.pm, at, w, h).data());
        let inverted = looked(&cv.pm, w, h, s, fonts, &frame(serde_json::json!({"invert": true})), &mut st, false).expect("inverted");
        let (a, b) = (cv.pm.pixel(2, h - 2).unwrap(), inverted.pixel(2, h - 2).unwrap());
        assert!(a.red() < 40 && b.red() > 215, "the dark soft key bar turned light: {} → {}", a.red(), b.red());
    }
}
