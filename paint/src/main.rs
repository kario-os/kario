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
mod node;
mod pace;
mod paint;
mod perf;
mod play;
mod raster;
mod scan;
mod seccomp;
mod skin;
mod sound_switch;
mod surfaces;
mod timing;
mod touch;

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
    /// hello: the file the numbers of perf.rs go to, when asked ("stats")
    /// and at the end.
    #[serde(default)]
    stats: String,
    /// touch (headless): a finger's phase and where it is (logical pixels).
    #[serde(default)]
    phase: String,
    #[serde(default)]
    x: f32,
    #[serde(default)]
    y: f32,
}

/// A touch on the frame for the Shell (23-renderer-protocol.md §3): the
/// finger's phase and where it is, in logical pixels of the frame (a tenth
/// of one is enough), and at its coming down, what is under it.
fn touch_msg(phase: &str, x: f32, y: f32, hit: Option<&paint::Hit>) -> serde_json::Value {
    let tenth = |v: f32| (v * 10.0).round() / 10.0;
    let mut m = serde_json::json!({"t": "touch", "phase": phase, "x": tenth(x), "y": tenth(y)});
    if let Some(h) = hit {
        m["hit"] = h.json();
    }
    m
}

/// What the Shell is told of a touchscreen's report at (x, y), logical
/// pixels of the frame as drawn (Large text's too); touching is the finger
/// it was told of. Coming down, the finger is on what state was last drawn
/// with: in a scroll or a glide, the step the panel shows. A dark panel
/// takes no touch (the Shell lights the screen for keys only), nor one
/// under the magnifier, where what is under the finger is not where the
/// frame has it (takes).
fn touched(phase: touch::Phase, x: f32, y: f32, takes: bool, state: &paint::State, touching: &mut Option<(f32, f32)>) -> Option<serde_json::Value> {
    match phase {
        touch::Phase::Down if takes => {
            *touching = Some((x, y));
            Some(touch_msg(phase.name(), x, y, Some(&paint::hit(state, x, y))))
        }
        // At most one a logical pixel the finger moves.
        touch::Phase::Move if touching.is_some_and(|(lx, ly)| (x - lx).abs() >= 1.0 || (y - ly).abs() >= 1.0) => {
            *touching = Some((x, y));
            Some(touch_msg(phase.name(), x, y, None))
        }
        touch::Phase::Up | touch::Phase::Cancel if touching.take().is_some() => Some(touch_msg(phase.name(), x, y, None)),
        _ => None,
    }
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
            if fresh || moved || p.shown || g.restyled() {
                show_gpu(d, g, g.shown(), s, fonts, p)?;
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

/// The magnifier's picture of a frame (Settings › Accessibility), None
/// while it is off: the frame drawn again zoomed (its text sharp at the
/// zoomed scale, not a picture scaled up) and cut around the focus. live:
/// for the panel (secure pictures shown), else a snapshot.
#[allow(clippy::too_many_arguments)]
fn magnified(width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State, live: bool) -> Option<tiny_skia::Pixmap> {
    let z = look::Look::of(f).zoom;
    if z <= 0.0 {
        return None;
    }
    let mut cv = canvas::Canvas::new((width as f32 * z).round() as u32, (height as f32 * z).round() as u32, s * z, fonts);
    cv.mask_secure = !live;
    paint::draw(&mut cv, f, st);
    Some(look::crop(&cv.pm, look::window((width, height), s, z, cv.focus_box), width, height))
}

/// What the panel shows of a frame drawn as pm: pm itself (None), or with
/// Settings › Accessibility's magnifier or colour filters on, the
/// magnifier's picture (`magnified`), recoloured.
#[allow(clippy::too_many_arguments)]
fn looked(pm: &tiny_skia::Pixmap, width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State, live: bool) -> Option<tiny_skia::Pixmap> {
    let look = look::Look::of(f);
    if look.plain() {
        return None;
    }
    let mut out = magnified(width, height, s, fonts, f, st, live).unwrap_or_else(|| pm.clone());
    look.filter(&mut out);
    Some(out)
}

/// The look of the frame just drawn for the panel: on the CPU path the
/// picture to show instead of the layers' screen (`looked`); on the GPU
/// path none, the GPU being given the colour filters (its present pass
/// applies them) and the magnifier's picture (shown in place of the
/// composed screen).
#[allow(clippy::too_many_arguments)]
fn looks(gpu: &mut Option<gpu::Gpu>, layers: &layers::Layers, width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State) -> Result<Option<tiny_skia::Pixmap>, String> {
    let Some(g) = gpu.as_mut() else { return Ok(looked(layers.screen(), width, height, s, fonts, f, st, true)) };
    g.set_look(look::Look::of(f));
    match magnified(width, height, s, fonts, f, st, true) {
        Some(pm) => g.put_zoomed(&pm)?,
        None => g.drop_zoom(),
    }
    Ok(None)
}

/// Puts the frame just drawn (`redraw`, `changed`) on the panel as
/// `present` does without a transition, through Settings › Accessibility's
/// look when one is on; true as `present`.
#[allow(clippy::too_many_arguments)]
fn show(d: &mut kms::Display, gpu: &mut Option<gpu::Gpu>, layers: &mut layers::Layers, changed: &[kms::Rect], width: u32, height: u32, s: f32, fonts: &'static canvas::Fonts, f: &frame::Frame, st: &mut paint::State, p: &mut Pointer) -> bool {
    match looks(gpu, layers, width, height, s, fonts, f, st) {
        Ok(Some(out)) => {
            show_cpu(d, &out, None, s, fonts, p);
            false
        }
        Ok(None) => present(d, gpu, layers, Some(changed), s, fonts, p, None),
        Err(e) => give_up(d, gpu, layers, fonts, e),
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

    /// Between an animation's steps: the touchscreen's reports waiting,
    /// taken out in order, for what the finger comes down on to be what
    /// the panel shows now rather than where the motion ends; the rest is
    /// kept, in order, for after it.
    fn touches(&mut self) -> Vec<(touch::Phase, f32, f32, perf::Stamp)> {
        while let Ok(ev) = self.rx.try_recv() {
            self.held.push_back(ev);
        }
        let mut out = Vec::new();
        self.held.retain(|ev| match *ev {
            Event::Touch(phase, x, y, at) => {
                out.push((phase, x, y, at));
                false
            }
            _ => true,
        });
        out
    }

    /// The "power" messages waiting behind one just taken (to turn the
    /// panel to `on`): the panel goes the way the last of them says, now,
    /// and is not turned back and forth (the Shell asks for one change at a
    /// time, but a renderer gets no say in who drives it). The rest keeps
    /// its order. The panel's state and how many it took.
    fn powers(&mut self, mut on: bool) -> (bool, usize) {
        while let Ok(ev) = self.rx.try_recv() {
            self.held.push_back(ev);
        }
        let mut n = 0;
        let mut rest = VecDeque::with_capacity(self.held.len());
        for ev in self.held.drain(..) {
            match &ev {
                Event::Line(l) if l.contains("\"power\"") => match serde_json::from_str::<Msg>(l) {
                    Ok(m) if m.t == "power" => {
                        on = m.on.unwrap_or(true);
                        n += 1;
                    }
                    _ => rest.push_back(ev),
                },
                _ => rest.push_back(ev),
            }
        }
        self.held = rest;
        (on, n)
    }

    /// The frames and patches waiting right behind one just taken, merged
    /// into doc: drawn as one, since the panel would not show them all.
    /// Stops at anything else, which stays first.
    fn frames(&mut self, doc: &mut serde_json::Value, mut dump: Option<&mut std::fs::File>, marks: &mut Marks) {
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
            take_frame(doc, &t, v, dump.as_deref_mut(), marks);
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
    /// The panel's page flips so far: asked for, completed.
    flips: (u64, u64),
    /// Always on: the gate's numbers (perf.rs); the panel's frames put
    /// when last looked at.
    perf: perf::Perf,
    puts: u64,
}

fn ms(d: Duration) -> f32 {
    d.as_secs_f32() * 1000.0
}

/// A burst's PHONE_PAINT_STEPS line on a panel, with its page flips so far
/// (asked for, completed).
fn steps_line(b: &Times, flips: (u64, u64)) -> serde_json::Value {
    serde_json::json!({"draw": b.draws, "transition": b.steps, "present": b.shown, "gap": b.gaps, "flips": [flips.0, flips.1]})
}

impl Trace {
    /// Opened before the sandbox: the file is not reachable after it.
    fn new(live: bool) -> Trace {
        let out = std::env::var_os("PHONE_PAINT_STEPS").filter(|_| live).and_then(|p| std::fs::OpenOptions::new().create(true).append(true).open(p).ok());
        Trace { on: std::env::var_os("PHONE_PAINT_TRACE").is_some(), out, burst: Times::default(), all: Times::default(), ticks: Times::default(), last: None, flips: (0, 0), perf: perf::Perf::new(&perf::INPUTS), puts: 0 }
    }

    fn drawn(&mut self, t0: Instant, share: f32) {
        self.burst.draws.push(ms(t0.elapsed()));
        self.burst.painted.push(share * 100.0);
        self.perf.drew(t0.elapsed());
    }

    fn step(&mut self, t0: Instant) {
        self.burst.steps.push(ms(t0.elapsed()));
        self.perf.drew(t0.elapsed());
    }

    /// A frame handed to the panel from t0 on.
    fn shown(&mut self, t0: Instant, d: &mut kms::Display) {
        let now = Instant::now();
        let wait = d.waited();
        self.flips = (d.flips_asked, d.flips_done);
        self.burst.shown.push(ms(now.duration_since(t0).saturating_sub(wait)));
        self.burst.waits.push(ms(wait));
        // A transition's steps were handed over inside the frame's
        // present: its own hand-over is from the last of them.
        let from = self.last.map_or(t0, |l| l.max(t0));
        self.handed(now.duration_since(from).saturating_sub(wait), self.last.is_some(), d);
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
            let _ = writeln!(f, "{}", steps_line(&b, self.flips));
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
            "phone-paint: {what}; so far: {}; timer frames: {} draws (p50 {:.1} ms, p95 {:.1} ms), handed over p50 {:.1} ms, p95 {:.1} ms; {} page flips, {} completed",
            self.all.summary(),
            t.draws.len(),
            quantile(&t.draws, 0.5),
            quantile(&t.draws, 0.95),
            quantile(&t.shown, 0.5),
            quantile(&t.shown, 0.95),
            self.flips.0,
            self.flips.1,
        );
    }

    /// To perf.rs: the frame drawn last handed over in took, and, when
    /// it put one on the panel, where the inputs a frame named are now.
    fn handed(&mut self, took: Duration, cont: bool, d: &kms::Display) {
        let put = std::mem::replace(&mut self.puts, d.puts) != d.puts;
        let flip = d.put_flip.filter(|_| put);
        self.perf.handed(took, cont, flip);
        if put {
            self.perf.photon(flip.map_or(perf::Photon::At(perf::mono()), perf::Photon::Flip));
        }
    }

    /// A frame due to a timer alone, drawn from t0 and shown from t1; the
    /// sums every TICKS_REPORTED of them.
    fn tick(&mut self, t0: Instant, t1: Instant, d: &mut kms::Display) {
        let wait = d.waited();
        self.perf.drew(t1.duration_since(t0));
        self.handed(t1.elapsed().saturating_sub(wait), false, d);
        if !self.on {
            return;
        }
        self.flips = (d.flips_asked, d.flips_done);
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
        let res = show_gpu(d, g, g.shown(), scale, fonts, p);
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
    // --stats [FILE...]: the summaries perf.rs wrote as a table, against
    // the gate (the device's own by default).
    if let Some(i) = args.iter().position(|a| a == "--stats") {
        let files = if args.len() > i + 1 { args[i + 1..].to_vec() } else { vec!["/run/phone/paint-stats.json".to_string()] };
        match perf::table(&files, &mut std::io::stdout()) {
            Ok(true) => return,
            Ok(false) => std::process::exit(1),
            Err(e) => {
                eprintln!("phone-paint: stats: {e}");
                std::process::exit(2);
            }
        }
    }
    let snap_dir = args.iter().position(|a| a == "--snap-dir").and_then(|i| args.get(i + 1).cloned()).unwrap_or_else(|| ".".into());
    // PHONE_PAINT_HEADLESS=1: the device mode's renderer without a panel, for
    // the gate's run on a machine with none (tools/perf/host.sh).
    let headless = args.iter().any(|a| a == "--headless") || std::env::var("PHONE_PAINT_HEADLESS").is_ok_and(|v| v == "1");
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
    let stats_path = hello.as_ref().map(|m| m.stats.clone()).unwrap_or_default();
    let dev = hello.and_then(|m| m.device).unwrap_or_default();
    let (mut width, mut height, mut scale) = if dev.width > 0 { (dev.width, dev.height, dev.scale) } else { (480, 640, 2.0) };
    // PHONE_PAINT_TARGET=480x640[@2]: a product's screen drawn at its own
    // size in the middle of a bigger panel (the 6T's), the frames costing
    // what they would there (39-oneplus-6t.md §8.1).
    let target = std::env::var("PHONE_PAINT_TARGET").ok().and_then(|v| target_size(&v));
    if let Some(t) = target.filter(|_| headless) {
        (width, height, scale) = t;
    }
    // Before any thread and before the panel: the input opener is forked
    // from this process (input::start).
    let mut inputs = None;
    if !headless {
        match input::start(dev.keymap.keys().copied().collect(), dev.touch, dev.sound_switch.clone()) {
            Ok(sock) => inputs = Some(sock),
            Err(e) => eprintln!("phone-paint: input: {e}"),
        }
    }
    // On the device the panel's mode decides the size, not the profile.
    let mut display = None;
    if !headless {
        match kms::Display::open() {
            Ok(mut d) => {
                let fits = target.filter(|&(w, h, _)| w <= d.width && h <= d.height);
                if target.is_some() && fits.is_none() {
                    eprintln!("phone-paint: PHONE_PAINT_TARGET is bigger than the {}x{} panel; left out", d.width, d.height);
                }
                if let Some((w, h, s)) = fits {
                    (width, height, scale) = (w, h, s);
                    d.origin = kms::letterbox((d.width, d.height), (w, h)).unwrap_or((0, 0));
                } else if dev.width > 0 && d.sideways(dev.width, dev.height) {
                    // A portrait panel in a landscape phone (24-sku-framework.md):
                    // drawn at the device's size, turned onto the panel.
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
    let mut features = if headless { vec!["snap", "images", "patch", "layout", "power", "timing", "phase", "rate", "touch"] } else { vec!["kms", "keys", "images", "patch", "anim", "layout", "power", "mouse", "pointer", "phase", "rate"] };
    // Where the frame is on the panel, for a touchscreen's readings.
    let touch_place = display.as_ref().filter(|_| dev.touch && inputs.is_some()).map(|d| touch::Place { panel: (d.width, d.height), origin: d.origin, rotated: d.rotated, frame: (width, height) });
    if touch_place.is_some() {
        features.push("touch");
    }
    // Inputs carry ids and frames name those they show (perf.rs).
    features.push("latency");
    if let Some(fd) = side_fd {
        surfaces::listen(fd);
        features.push("shm");
    }
    // PHONE_PAINT_DUMP: every frame as merged, a line each, for --bench.
    let mut dump = std::env::var_os("PHONE_PAINT_DUMP").and_then(|p| std::fs::OpenOptions::new().create(true).append(true).open(p).ok());
    // PHONE_PAINT_STEPS or the hello's timing (headless): the live display's
    // steps timed (timing.rs).
    let steps_file = std::env::var_os("PHONE_PAINT_STEPS").filter(|_| display.is_none());
    // PHONE_PAINT_PLAY: the gate's run (play.rs); headless, its frames are
    // drawn as the panel would. Only the renderer the Shell gives a stats
    // file plays it: the Shell's snapshot renderers inherit its environment.
    let mut play_state = serde_json::Value::Null;
    let mut play = match std::env::var_os("PHONE_PAINT_PLAY").filter(|_| !stats_path.is_empty()).map(|p| play::Play::load(&p)) {
        Some(Ok(p)) => Some(p),
        Some(Err(e)) => {
            eprintln!("phone-paint: play: {e}");
            play_state = serde_json::json!({"state": "failed", "line": 0, "error": e});
            None
        }
        None => None,
    };
    let mut timing = if display.is_none() && (steps_file.is_some() || report || play.is_some()) { timing::Timing::open(steps_file.as_deref(), width, height, scale, fonts) } else { None };
    // Frames received ("frame" and "patch"), numbered as the Shell sends them.
    let mut received = 0u64;
    // On a panel: what its motion costs (PHONE_PAINT_TRACE, PHONE_PAINT_STEPS);
    // the gate's numbers, to hello's file (opened before the sandbox).
    let mut trace = Trace::new(display.is_some());
    let mut stats_out = perf::Out::open(Some(&stats_path));
    let panel_size = display.as_ref().map(|d| (d.width, d.height));
    // The GPU path asked for (PHONE_PAINT_GPU, else the board's) and, where
    // the display's device cannot render, the GPU's render node (node.rs),
    // found and opened like the card before the sandbox.
    let ask = gpu::Ask::choose(std::env::var("PHONE_PAINT_GPU").ok().as_deref(), &dev.gpu);
    let card = display.as_ref().filter(|_| ask != gpu::Ask::Cpu).map(|d| d.node());
    let render = card.as_ref().map_or(Ok(None), |c| node::open(c, &dev.gpu_render));
    if display.is_some() {
        match kms::drop_privileges() {
            Ok(got) => features.extend(got),
            Err(e) => {
                eprintln!("phone-paint: sandbox: {e}");
                std::process::exit(6);
            }
        }
    }
    // Compose: the GPU composes (gpu.rs); raster: it paints the display
    // list too (raster.rs); soft: it composes on a software rasterizer too,
    // for tests. It starts inside the sandbox, which must be set up while
    // there is one thread, so Mesa's threads are confined like the rest;
    // the card and the render node were opened before it (the kernel
    // driver loads the GPU's firmware then) and Mesa's libraries are under
    // /usr. What it cannot reach there (sysfs's device details, a shader
    // cache on disk) it does without; if it cannot start, the CPU draws as
    // always.
    let mut gpu = None;
    if let Some(d) = display.as_mut().filter(|_| ask != gpu::Ask::Cpu) {
        match render.as_ref().map_err(String::clone).and_then(|r| gpu::Gpu::on_panel(d, r.as_ref(), width, height, ask == gpu::Ask::Soft)) {
            Ok(g) => {
                let on = match (&render, &card) {
                    (Ok(Some(n)), Some(c)) => format!(" on {} ({}), shown on {} ({}) through PRIME", n.info.path, n.info.driver, c.path, c.driver),
                    _ => String::new(),
                };
                eprintln!("phone-paint: gpu: composing with {}{on}", g.renderer());
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
    // PHONE_PAINT_PLAY's keys go through a keyboard's state machine too.
    let mut typist = kms::Keyboard::new(dev.keypad == "t9", &dev.fn_layer, None);
    if let Some(sock) = inputs {
        kms::read_inputs(sock, dev.keypad == "t9", dev.fn_layer.clone(), dev.ctrl.clone(), dev.keymap.clone(), touch_place, send, events.clone(), keytime);
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
    // The touchscreen's finger the Shell was told of, where it was last
    // said to be (logical pixels).
    let mut touching: Option<(f32, f32)> = None;
    // The live display's pictures (split for the GPU); whether their screen
    // is what the panel shows (a transition starts from it), and its scale;
    // the transition last run; whether the frame is to be drawn again (the
    // GPU gave up).
    let mut layers = if gpu.is_some() { gpu::layers(ask, width, height, fonts) } else { layers::Layers::new(width, height, fonts) };
    let mut on_panel = false;
    let mut last_s = scale;
    let mut last_tn = 0u64;
    let mut redo = false;
    // The rate the panel runs at, until the shell's "rate" changes it.
    let mut pace = pace::Pace::default();
    if let Some(d) = display.as_ref() {
        pace.set(d.rate());
    }
    loop {
        // PHONE_PAINT_PLAY's next keys and touches, once what it waits for
        // came (a frame, the time).
        if let Some(p) = play.as_mut() {
            let s = zoomed(scale, &current);
            typist.set_timing(keytime.now());
            for act in p.step(Instant::now(), &current, (width as f32 / s, height as f32 / s), &perf::INPUTS, &mut typist) {
                match act {
                    play::Act::Key(m) => send(&m),
                    play::Act::Touch(phase, x, y, at) => {
                        if display.is_none() && phase == touch::Phase::Down {
                            // Headless, what is under the finger is known once drawn.
                            let live = state.now.take();
                            let mut cv = canvas::Canvas::new(width, height, s, fonts);
                            paint::draw(&mut cv, &current, &mut state);
                            state.now = live;
                        }
                        let takes = lit && (on_panel || display.is_none()) && look::Look::of(&current).zoom <= 0.0;
                        if let Some(mut m) = touched(phase, x, y, takes, &state, &mut touching) {
                            perf::INPUTS.stamp(&mut m, perf::Kind::Touch, at);
                            send(&m);
                        }
                    }
                }
            }
            if p.ended() {
                play_state = p.state();
                eprintln!("phone-paint: play: {}", if play_state["error"] == "" { "done".to_string() } else { play_state["error"].to_string() });
                play = None;
                let sum = trace.perf.summary(about(gl_of(&gpu, &timing), ask, (width, height, scale), panel_size, target.is_some(), &pace, &play_state));
                eprintln!("phone-paint: {}", perf::line(&sum));
                stats_out.write(&sum);
            }
        }
        // Due without a message: the pointer moved meanwhile, or the text
        // cursor or a spinner changes (only that is painted again).
        let tick = display.as_ref().filter(|_| lit && on_panel && current.motion != "off").and_then(|_| paint::next_tick(&state, Instant::now()));
        let tick = if redo && lit { Some(Instant::now()) } else { tick };
        // A page flip that shows what a frame named, read when it came.
        let flip = display.as_ref().filter(|_| trace.perf.awaiting()).map(|_| Instant::now() + pace.period());
        let wake = [ptr.due.then(|| ptr.drawn + pace.period()), tick, flip, play.as_ref().and_then(|p| p.due())].into_iter().flatten().min();
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
                                trace.puts = d.puts;
                                let changed = redraw(&mut layers, width, height, s, fonts, &current, &mut state);
                                let t1 = Instant::now();
                                redo |= show(d, &mut gpu, &mut layers, &changed, width, height, s, fonts, &current, &mut state, &mut ptr);
                                on_panel = !redo;
                                trace.tick(t0, t1, d);
                            }
                            d.poll();
                            flips_to_perf(d, &mut trace.perf);
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
            Event::Touch(phase, fx, fy, at) => {
                let s = zoomed(scale, &current);
                let takes = lit && on_panel && look::Look::of(&current).zoom <= 0.0;
                if let Some(mut m) = touched(phase, fx / s, fy / s, takes, &state, &mut touching) {
                    perf::INPUTS.stamp(&mut m, perf::Kind::Touch, at);
                    send(&m);
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
                let mut marks = Marks::default();
                take_frame(&mut doc, &m.t, v, dump.as_mut(), &mut marks);
                // On the panel, frames waiting already go into this one: no
                // more are drawn than it shows.
                if display.is_some() && lit {
                    inbox.frames(&mut doc, dump.as_mut(), &mut marks);
                }
                trace.perf.named(&marks.ids, marks.start, perf::mono());
                if let Some(d) = display.as_ref() {
                    trace.puts = d.puts;
                }
                match serde_json::from_value::<frame::Frame>(doc.clone()) {
                    Ok(f) => {
                        current = f;
                        if display.is_none() {
                            let parsed = timing::ms(t_in);
                            let t_layout = Instant::now();
                            let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                            paint::layout(&mut cv, &current, &mut state);
                            let laid_out = timing::ms(t_layout);
                            if let Some(steps) = timing.as_mut().map(|t| t.frame(&current)) {
                                // Headless, drawn is on the panel.
                                for (i, ms) in steps.draw.iter().chain(&steps.transition).enumerate() {
                                    trace.perf.drew(Duration::from_secs_f64(ms / 1000.0));
                                    trace.perf.handed(Duration::ZERO, i > 0, None);
                                }
                                if report {
                                    send(&serde_json::json!({"t": "drawn", "n": received, "parse_ms": parsed, "layout_ms": laid_out,
                                        "draw_ms": steps.draw, "transition_ms": steps.transition}));
                                }
                            }
                            trace.perf.photon(perf::Photon::At(perf::mono()));
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
                                let look = looks(&mut gpu, &layers, width, height, s, fonts, &current, &mut state);
                                if !still && (from.is_some() || paint::animating(&state)) {
                                    if let Some(m) = pace.motion(true) {
                                        send(&m);
                                    }
                                }
                                let start = kind.take().zip(from).map(|(kind, from)| Start { kind, from, st: &state, before, steps: &mut trace, inbox: &mut inbox, pace: &mut pace });
                                last_s = s;
                                let t1 = Instant::now();
                                let gave_up = match look {
                                    Ok(Some(out)) => {
                                        show_cpu(d, &out, None, s, fonts, &mut ptr);
                                        false
                                    }
                                    Ok(None) => present(d, &mut gpu, &mut layers, Some(&changed), s, fonts, &mut ptr, start),
                                    Err(e) => give_up(d, &mut gpu, &mut layers, fonts, e),
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
                                let takes = look::Look::of(&current).zoom <= 0.0;
                                for (phase, fx, fy, at) in inbox.touches() {
                                    if let Some(mut m) = touched(phase, fx / s, fy / s, takes, &state, &mut touching) {
                                        perf::INPUTS.stamp(&mut m, perf::Kind::Touch, at);
                                        send(&m);
                                    }
                                }
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
                // What it named reached nothing new on the panel (off, the
                // same picture, the frame unreadable).
                if trace.perf.naming() {
                    trace.perf.photon(perf::Photon::Unchanged);
                }
                if let Some(d) = display.as_mut() {
                    flips_to_perf(d, &mut trace.perf);
                } else {
                    tell_shown(&mut trace.perf);
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
                let (on, more) = inbox.powers(m.on.unwrap_or(true));
                let (was, t0) = (lit, Instant::now());
                let res = display.as_mut().map_or(Ok(None), |d| d.power(on));
                let ms = t0.elapsed().as_millis() as u64;
                let warn = res.as_ref().ok().cloned().flatten();
                if display.is_some() && (on != was || res.is_err()) {
                    match (&res, &warn) {
                        (Err(e), _) => eprintln!("phone-paint: panel {}: {e} ({ms} ms)", if on { "on" } else { "off" }),
                        (Ok(_), Some(w)) => eprintln!("phone-paint: panel {} in {ms} ms, but {w}", if on { "on" } else { "off" }),
                        (Ok(_), None) => eprintln!("phone-paint: panel {} in {ms} ms", if on { "on" } else { "off" }),
                    }
                }
                if res.is_ok() {
                    lit = on;
                }
                if !lit {
                    on_panel = false;
                    if let Some((x, y)) = touching.take() {
                        send(&touch_msg(touch::Phase::Cancel.name(), x, y, None));
                    }
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
                let mut reply = serde_json::json!({"t": "power", "on": lit, "err": res.err().unwrap_or_default(), "ms": ms});
                if let Some(w) = warn {
                    reply["warn"] = w.into();
                }
                // Each "power" is answered, those taken with it too.
                for _ in 0..=more {
                    send(&reply);
                }
            }
            "rate" => set_rate(display.as_mut(), &mut pace, timing.as_mut(), m.hz),
            "stats" => {
                if let Some(d) = display.as_mut() {
                    d.poll();
                    flips_to_perf(d, &mut trace.perf);
                }
                let st = play.as_ref().map_or(play_state.clone(), |p| p.state());
                stats_out.write(&trace.perf.summary(about(gl_of(&gpu, &timing), ask, (width, height, scale), panel_size, target.is_some(), &pace, &st)));
            }
            // Headless, the Shell's touches are taken as from a touchscreen
            // of the frame's size (the selftest's touch steps) and answered
            // as the panel's renderer answers its own; on a panel only the
            // touchscreen touches.
            "touch" if display.is_none() && matches!(m.phase.as_str(), "down" | "move" | "up" | "cancel") => {
                let hit = (m.phase == "down").then(|| {
                    let live = state.now.take();
                    let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                    paint::draw(&mut cv, &current, &mut state);
                    state.now = live;
                    paint::hit(&state, m.x, m.y)
                });
                send(&touch_msg(&m.phase, m.x, m.y, hit.as_ref()));
            }
            "keys" => {
                if let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) {
                    keytime.set(&v);
                }
            }
            "quit" => break,
            _ => {}
        }
    }
    let st = play.as_ref().map_or(play_state, |p| p.state());
    let sum = trace.perf.summary(about(gl_of(&gpu, &timing), ask, (width, height, scale), panel_size, target.is_some(), &pace, &st));
    stats_out.write(&sum);
    eprintln!("phone-paint: at the end: {}", perf::line(&sum));
}

/// The page flips completed since last asked, to perf.rs, and the app
/// starts they showed to the Shell.
fn flips_to_perf(d: &mut kms::Display, p: &mut perf::Perf) {
    let period = d.period();
    for (n, t) in d.flipped.drain(..) {
        p.flipped(n, t, period);
    }
    tell_shown(p);
}

/// An app's first page reached the panel this long after its start, in
/// whole ms (23-renderer-protocol.md §3 `shown`).
fn tell_shown(p: &mut perf::Perf) {
    for (app, ms) in p.take_shown() {
        send(&serde_json::json!({"t": "shown", "app": app, "ms": ms.round() as u64}));
    }
}

/// The GL renderer drawing, on the panel or (headless) timing the steps;
/// None on the CPU path.
fn gl_of(gpu: &Option<gpu::Gpu>, timing: &Option<timing::Timing>) -> Option<String> {
    gpu.as_ref().map(gpu::Gpu::renderer).or_else(|| timing.as_ref().and_then(timing::Timing::gl))
}

/// What a summary of perf.rs says of the renderer: drawn on a panel or
/// headless, the GPU path and its GL renderer, the frame's size and scale,
/// the panel's, whether PHONE_PAINT_TARGET set the size, the rate,
/// PHONE_PAINT_PLAY's state.
#[allow(clippy::too_many_arguments)]
fn about(gl: Option<String>, ask: gpu::Ask, (w, h, s): (u32, u32, f32), panel: Option<(u32, u32)>, target: bool, pace: &pace::Pace, play: &serde_json::Value) -> serde_json::Value {
    let path = match (gl.is_some(), ask) {
        (false, _) | (_, gpu::Ask::Cpu) => "cpu",
        (true, gpu::Ask::Compose) => "compose",
        (true, gpu::Ask::Raster) => "raster",
        (true, gpu::Ask::Soft) => "soft",
    };
    let mut v = serde_json::json!({
        "renderer": format!("{} {}", env!("CARGO_PKG_NAME"), env!("CARGO_PKG_VERSION")),
        "panel": if panel.is_some() { "kms" } else { "headless" },
        "gpu": path,
        "size": [w, h],
        "scale": s,
        "screen": panel.map_or([w, h], |(pw, ph)| [pw, ph]),
        "target": target,
        "hz": (1.0 / pace.period().as_secs_f64()).round(),
    });
    if let Some(gl) = gl {
        v["gl"] = gl.into();
    }
    if !play.is_null() {
        v["play"] = play.clone();
    }
    v
}

/// PHONE_PAINT_TARGET's size: "480x640", or with the scale, "480x640@2"
/// (2 unless given).
fn target_size(v: &str) -> Option<(u32, u32, f32)> {
    let (size, s) = v.split_once('@').map_or((v, Some(2.0)), |(a, b)| (a, b.parse::<f32>().ok()));
    let (w, h) = size.split_once('x')?;
    let (w, h, s) = (w.parse::<u32>().ok()?, h.parse::<u32>().ok()?, s?);
    (w > 0 && h > 0 && s > 0.0).then_some((w, h, s))
}

/// What frames name for perf.rs (23-renderer-protocol.md §3, feature
/// "latency"): the inputs whose effect they first show (`in`), an app's
/// first page and when its process started (`start`, CLOCK_MONOTONIC µs).
#[derive(Default, Debug, PartialEq)]
struct Marks {
    ids: Vec<u64>,
    start: Option<(String, Duration)>,
}

impl Marks {
    /// The marks of a frame message, taken out of it: the frame held is
    /// the screen alone (a snapshot's echo is compared with the Shell's).
    fn take(&mut self, v: &mut serde_json::Value) {
        let Some(o) = v.as_object_mut() else { return };
        if let Some(ids) = o.remove("in") {
            self.ids.extend(ids.as_array().into_iter().flatten().filter_map(|i| i.as_u64()));
        }
        if let Some(st) = o.remove("start") {
            if let (Some(app), Some(at)) = (st["app"].as_str(), st["at"].as_u64()) {
                self.start = Some((app.to_string(), Duration::from_micros(at)));
            }
        }
    }
}

/// A frame or a patch of one into doc (its marks into marks), the frame as
/// merged into the dump.
fn take_frame(doc: &mut serde_json::Value, t: &str, mut v: serde_json::Value, dump: Option<&mut std::fs::File>, marks: &mut Marks) {
    marks.take(&mut v);
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
        ib.frames(&mut doc, None, &mut Marks::default());
        assert_eq!((doc["focus"].as_i64(), doc["title"].as_str()), (Some(3), Some("B")));
        assert_eq!(rest(&mut ib), [r#"{"t":"snap","name":"x"}"#, r#"{"t":"patch","patch":{"focus":4}}"#]);
    }

    // The inputs frames name (feature "latency") are not part of the frame
    // held (a snapshot's echo is compared with the Shell's); frames drawn
    // as one name the inputs of each, and the last start.
    #[test]
    fn the_inputs_frames_name_are_taken_out_and_gathered() {
        let (mut ib, _tx) = inbox(&[
            r#"{"t":"patch","patch":{"focus":2},"in":[7]}"#,
            r#"{"t":"patch","patch":{"focus":3},"in":[8,9],"start":{"app":"com.phone.contacts","at":2500000}}"#,
            r#"{"t":"snap","name":"x"}"#,
        ]);
        let mut doc = serde_json::Value::Null;
        let mut marks = Marks::default();
        take_frame(&mut doc, "frame", serde_json::json!({"t": "frame", "focus": 1, "in": [5, 6]}), None, &mut marks);
        assert_eq!(doc, serde_json::json!({"t": "frame", "focus": 1}));
        ib.frames(&mut doc, None, &mut marks);
        assert_eq!(doc, serde_json::json!({"t": "frame", "focus": 3}));
        assert_eq!(marks, Marks { ids: vec![5, 6, 7, 8, 9], start: Some(("com.phone.contacts".into(), Duration::from_millis(2500))) });
    }

    #[test]
    fn a_target_size_is_wxh_and_a_scale() {
        assert_eq!(target_size("480x640"), Some((480, 640, 2.0)));
        assert_eq!(target_size("640x480@1.5"), Some((640, 480, 1.5)));
        assert_eq!(target_size("480"), None);
        assert_eq!(target_size("0x640"), None);
        assert_eq!(target_size("480x640@x"), None);
    }

    // The panel turned off, on, off by a renderer behind on its messages:
    // it goes off once, not on and off again; the frame between keeps its
    // place, and every "power" is answered.
    #[test]
    fn queued_powers_go_as_the_last() {
        let (mut ib, tx) = inbox(&[r#"{"t":"power","on":true}"#, r#"{"t":"patch","patch":{"focus":2}}"#]);
        tx.send(Event::Line(r#"{"on":false,"t":"power"}"#.into())).unwrap();
        assert_eq!(ib.powers(false), (false, 2));
        assert_eq!(rest(&mut ib), [r#"{"t":"patch","patch":{"focus":2}}"#]);
        let (mut ib, _tx) = inbox(&[r#"{"t":"snap","name":"x"}"#]);
        assert_eq!(ib.powers(true), (true, 0));
        assert_eq!(rest(&mut ib), [r#"{"t":"snap","name":"x"}"#]);
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

    // A finger that comes down while a list scrolls is on the row the
    // panel shows under it then, not on the one the scroll ends with: the
    // touchscreen's reports are taken between the steps, and what else
    // waits keeps its order for after the motion.
    #[test]
    fn a_touch_during_a_scroll_is_on_what_shows() {
        let Ok(fonts) = canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static canvas::Fonts = Box::leak(Box::new(fonts));
        let list = |focus: i32| -> frame::Frame {
            serde_json::from_value(serde_json::json!({"screen": "a/list", "title": "Settings", "focus": focus, "body": (0..30).map(|i| serde_json::json!({"kind": "item", "title": format!("Row {i}"), "focused": i == focus})).collect::<Vec<_>>()})).unwrap()
        };
        let (w, h, s) = (240, 320, 1.0);
        let t0 = Instant::now();
        let draw = |f: &frame::Frame, st: &mut paint::State, ms: u64| {
            st.now = Some(t0 + Duration::from_millis(ms));
            paint::draw(&mut canvas::Canvas::new(w, h, s, fonts), f, st);
        };
        let mut st = paint::State::default();
        draw(&list(0), &mut st, 0);
        let end = list(29);
        draw(&end, &mut st, 16);
        draw(&end, &mut st, 70);
        assert!(paint::animating(&st), "the scroll runs");
        let (x, y) = (120.0, 160.0);
        let (mut ib, tx) = inbox(&[r#"{"t":"patch","patch":{"focus":28}}"#]);
        tx.send(Event::Touch(touch::Phase::Down, x * s, y * s, perf::Stamp::now(perf::Source::Kernel))).unwrap();
        tx.send(Event::Line(r#"{"t":"snap","name":"x"}"#.into())).unwrap();
        let mut touching = None;
        let got: Vec<_> = ib.touches().into_iter().filter_map(|(p, fx, fy, _)| touched(p, fx / s, fy / s, true, &st, &mut touching)).collect();
        let shown = paint::hit(&st, x, y);
        draw(&end, &mut st, 400);
        assert!(!paint::animating(&st));
        assert_ne!(paint::hit(&st, x, y), shown, "the scroll moved other rows under the finger");
        assert_eq!(got, [touch_msg("down", x, y, Some(&shown))], "taken between the steps, on the row shown");
        assert_eq!(rest(&mut ib), [r#"{"t":"patch","patch":{"focus":28}}"#, r#"{"t":"snap","name":"x"}"#]);
    }

    // Settings › Accessibility's look stays on the GPU path: the filters
    // in its present pass recolour the composed screen as the CPU path's
    // filter does (within a level), the magnifier's picture is the CPU
    // path's byte for byte, and with every look off the composed screen
    // goes to the panel as it is.
    #[test]
    fn the_gpu_shows_a_look_as_the_cpu_path() {
        let Ok(fonts) = canvas::Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static canvas::Fonts = Box::leak(Box::new(fonts));
        let (w, h, s) = (240, 320, 1.0);
        let Ok(g) = gpu::Gpu::headless(w, h) else {
            eprintln!("no GPU: skipped");
            return;
        };
        let mut gpu = Some(g);
        let frame = |extra: serde_json::Value| -> frame::Frame {
            let mut v = serde_json::json!({"screen": "a/list", "title": "Settings", "focus": 5, "body": (0..8).map(|i| serde_json::json!({"kind": "item", "title": format!("Row {i}"), "focused": i == 5})).collect::<Vec<_>>()});
            frame::merge(&mut v, &extra);
            serde_json::from_value(v).unwrap()
        };
        let mut st = paint::State::default();
        let mut layers = gpu::layers(gpu::Ask::Compose, w, h, fonts);
        let plain = frame(serde_json::json!({}));
        redraw(&mut layers, w, h, s, fonts, &plain, &mut st);
        let g = gpu.as_mut().unwrap();
        g.compose(&mut layers).unwrap();
        let screen = g.read(gpu::Pic::Screen(g.front()));
        let shown = |gpu: &mut Option<gpu::Gpu>, layers: &layers::Layers, f: &frame::Frame, st: &mut paint::State| {
            assert!(looks(gpu, layers, w, h, s, fonts, f, st).unwrap().is_none(), "nothing for the CPU to show");
            let g = gpu.as_mut().unwrap();
            g.present_into(g.shown(), None, (w, h), false, (0, 0), false)
        };
        let near = |a: &[u8], b: &[u8]| a.iter().zip(b).map(|(x, y)| x.abs_diff(*y)).max().unwrap_or(0);
        assert_eq!(shown(&mut gpu, &layers, &plain, &mut st), screen, "no look: the composed screen");
        for extra in [serde_json::json!({"invert": true}), serde_json::json!({"daltonize": "protanomaly"}), serde_json::json!({"invert": true, "daltonize": "tritanomaly"})] {
            let f = frame(extra.clone());
            let mut want = tiny_skia::Pixmap::from_vec(screen.clone(), tiny_skia::IntSize::from_wh(w, h).unwrap()).unwrap();
            look::Look::of(&f).filter(&mut want);
            assert!(near(&shown(&mut gpu, &layers, &f, &mut st), want.data()) <= 1, "{extra}");
        }
        let zoomed = frame(serde_json::json!({"magnify": 2}));
        let want = looked(layers.screen(), w, h, s, fonts, &zoomed, &mut st, true).expect("magnified");
        assert_eq!(shown(&mut gpu, &layers, &zoomed, &mut st), want.data(), "the CPU path's magnified picture");
        let both = frame(serde_json::json!({"magnify": 2, "invert": true}));
        let want = looked(layers.screen(), w, h, s, fonts, &both, &mut st, true).expect("magnified and inverted");
        assert!(near(&shown(&mut gpu, &layers, &both, &mut st), want.data()) <= 1);
        assert_eq!(shown(&mut gpu, &layers, &plain, &mut st), screen, "all off again");
    }

    // A burst's line on a panel says how many page flips it asked for and
    // saw complete so far, also when the burst was a single frame and
    // PHONE_PAINT_TRACE reports none of it (yocto/qemu-gui-drive.py).
    #[test]
    fn a_steps_line_counts_the_page_flips() {
        let b = Times { draws: vec![1.5], shown: vec![30.0], ..Times::default() };
        let line = steps_line(&b, (7, 6));
        assert_eq!(line["flips"], serde_json::json!([7, 6]));
        assert_eq!(line["draw"], serde_json::json!([1.5]));
        assert_eq!(line["present"], serde_json::json!([30.0]));
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
