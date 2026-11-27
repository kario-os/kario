//! What the phone's speed is, as the gate of 39-oneplus-6t.md §8.1 asks
//! it: each frame's draw and hand-over, the panel's frames during motion
//! (late and dropped against its refresh), each input's time from the
//! kernel's event to the page flip that first shows what it did (key- and
//! touch-to-photon), and each app's start to its first page on the panel.
//! Always on and cheap: fixed rings of samples, no line a frame; the
//! quantiles are worked out only when asked (the Shell's "stats", the end,
//! PHONE_PAINT_PLAY's end) and written to the file hello's `stats` names.
//!
//! An input's id goes to the Shell in its message (`in`); the frame that
//! shows its effect names it back (the frame's `in`, 23-renderer-protocol.md
//! §3). Times are CLOCK_MONOTONIC: evdev's once the clock is set
//! (EVIOCSCLOCKID), DRM's page-flip events', the Shell's start marks'.

use std::collections::VecDeque;
use std::io::{Seek, Write};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;
use std::time::Duration;

/// CLOCK_MONOTONIC now.
pub fn mono() -> Duration {
    let mut ts = libc::timespec { tv_sec: 0, tv_nsec: 0 };
    // SAFETY: clock_gettime writes the timespec it is given.
    unsafe { libc::clock_gettime(libc::CLOCK_MONOTONIC, &mut ts) };
    Duration::new(ts.tv_sec.max(0) as u64, ts.tv_nsec.clamp(0, 999_999_999) as u32)
}

/// Sets an evdev device's event clock to CLOCK_MONOTONIC (the kernel's
/// default is CLOCK_REALTIME, which a clock change moves); whether it took.
pub fn monotonic_events(fd: std::os::fd::RawFd) -> bool {
    // EVIOCSCLOCKID: _IOW('E', 0xa0, int).
    const EVIOCSCLOCKID: libc::c_ulong = (1 << 30) | ((std::mem::size_of::<libc::c_int>() as libc::c_ulong) << 16) | ((b'E' as libc::c_ulong) << 8) | 0xa0;
    let clock: libc::c_int = libc::CLOCK_MONOTONIC;
    // SAFETY: the ioctl reads one int from the pointer it is given.
    unsafe { libc::ioctl(fd, EVIOCSCLOCKID as _, &clock) == 0 }
}

/// An event's time from the kernel (a monotonic evdev clock), unless it is
/// not plausibly one (the clock was not set, or a driver stamps its own):
/// then now.
pub fn event_time(t: std::time::SystemTime, monotonic: bool) -> Stamp {
    let now = mono();
    match t.duration_since(std::time::UNIX_EPOCH) {
        Ok(at) if monotonic && at <= now && now - at < Duration::from_secs(10) => Stamp { at, src: Source::Kernel },
        _ => Stamp { at: now, src: Source::Read },
    }
}

/// What an input is: a key pressed, one the keyboard's timer made (a
/// repeat, a long press), a touchscreen report.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Kind {
    Key,
    Repeat,
    Touch,
}

/// Where an input's time comes from: the kernel's event (evdev), when it
/// was read (no monotonic event time), when PHONE_PAINT_PLAY made it.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Source {
    Kernel,
    Read,
    Play,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Stamp {
    pub at: Duration,
    pub src: Source,
}

impl Stamp {
    pub fn now(src: Source) -> Stamp {
        Stamp { at: mono(), src }
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Pending {
    id: u64,
    kind: Kind,
    stamp: Stamp,
}

/// Inputs sent to the Shell that no frame has named yet; the keyboards'
/// threads add to it. Past PENDING_MAX, or older than PENDING_AGE (the
/// Shell found nothing to show for them), they count as unanswered.
pub struct Inputs {
    next: AtomicU64,
    pending: Mutex<VecDeque<Pending>>,
    unanswered: AtomicU64,
    sources: [AtomicU64; 3],
}

const PENDING_MAX: usize = 512;
const PENDING_AGE: Duration = Duration::from_secs(3);

pub static INPUTS: Inputs = Inputs::new();

impl Inputs {
    pub const fn new() -> Inputs {
        Inputs { next: AtomicU64::new(1), pending: Mutex::new(VecDeque::new()), unanswered: AtomicU64::new(0), sources: [AtomicU64::new(0), AtomicU64::new(0), AtomicU64::new(0)] }
    }

    /// A new input: its id for the Shell.
    pub fn add(&self, kind: Kind, stamp: Stamp) -> u64 {
        let id = self.next.fetch_add(1, Ordering::Relaxed);
        self.sources[stamp.src as usize].fetch_add(1, Ordering::Relaxed);
        let mut p = self.pending.lock().unwrap_or_else(|e| e.into_inner());
        if p.len() == PENDING_MAX {
            p.pop_front();
            self.unanswered.fetch_add(1, Ordering::Relaxed);
        }
        p.push_back(Pending { id, kind, stamp });
        id
    }

    /// A message for the Shell: a key gets an id (`in`); the power key's
    /// answer is the panel, not a frame.
    pub fn stamp(&self, m: &mut serde_json::Value, kind: Kind, stamp: Stamp) {
        if m["t"] == "key" && m["key"] != "power" || m["t"] == "touch" {
            m["in"] = self.add(kind, stamp).into();
        }
    }

    /// The pending inputs of ids, taken out; those older than PENDING_AGE
    /// at now go as unanswered.
    pub(crate) fn take(&self, ids: &[u64], now: Duration) -> Vec<Pending> {
        let mut p = self.pending.lock().unwrap_or_else(|e| e.into_inner());
        let mut out = Vec::new();
        p.retain(|e| {
            if ids.contains(&e.id) {
                out.push(*e);
                return false;
            }
            if now.saturating_sub(e.stamp.at) > PENDING_AGE {
                self.unanswered.fetch_add(1, Ordering::Relaxed);
                return false;
            }
            true
        });
        out
    }

    /// Whether input id is still waiting for a frame to name it.
    pub fn waiting(&self, id: u64) -> bool {
        self.pending.lock().unwrap_or_else(|e| e.into_inner()).iter().any(|e| e.id == id)
    }
}

/// The last RING samples of one measure (ms), and how many there were.
#[derive(Default)]
struct Ring {
    v: VecDeque<f32>,
    n: u64,
}

const RING: usize = 4096;

impl Ring {
    fn push(&mut self, ms: f32) {
        if self.v.len() == RING {
            self.v.pop_front();
        }
        self.v.push_back(ms);
        self.n += 1;
    }

    fn q(&self, p: f32) -> f32 {
        let mut s: Vec<f32> = self.v.iter().copied().collect();
        s.sort_by(|a, b| a.total_cmp(b));
        // Nearest rank: the p95 of 20 samples is the 19th.
        let i = ((s.len() as f32 * p).ceil() as usize).clamp(1, s.len().max(1)) - 1;
        s.get(i).copied().unwrap_or(0.0)
    }

    fn json(&self) -> serde_json::Value {
        let r = |v: f32| (f64::from(v) * 100.0).round() / 100.0;
        serde_json::json!({"n": self.n, "p50": r(self.q(0.5)), "p95": r(self.q(0.95)), "p99": r(self.q(0.99)), "max": r(self.q(1.0))})
    }
}

fn ms(d: Duration) -> f32 {
    d.as_secs_f32() * 1000.0
}

/// The parts of an input's time, in order: until the Shell read it from
/// the renderer, waiting for the Shell's loop to handle it, the foreground
/// app's answer, the Shell making and sending the frame, until the
/// renderer took the frame, drawing it onto the panel.
pub const STAGES: [&str; 6] = ["shell_read", "shell_loop", "app", "shell_frame", "renderer_read", "show"];

/// An input's time in its parts (STAGES), ms.
type Split = [f32; 6];

/// The Shell's stamps of an input, in the frame that names it (`in_t`,
/// CLOCK_MONOTONIC µs): read from the renderer, handled, the foreground
/// app's answer after that (0: none), the frame sent.
pub type ShellStamps = [u64; 4];

/// The parts of an input made at `at` up to the renderer taking its frame
/// at `took`; the drawing's is added when it is shown.
fn split(at: Duration, s: ShellStamps, took: Duration) -> Split {
    let us = Duration::from_micros;
    let (read, handled, answered, sent) = (us(s[0]), us(s[1]), us(s[2]), us(s[3]));
    let (app, made) = if s[2] > 0 { (ms(answered.saturating_sub(handled)), answered.max(handled)) } else { (0.0, handled) };
    [ms(read.saturating_sub(at)), ms(handled.saturating_sub(read)), app, ms(sent.saturating_sub(made)), ms(took.saturating_sub(sent)), 0.0]
}

/// What a frame named: an input (its kind, its time, and with the Shell's
/// stamps its parts so far and when its frame was taken), or an app's
/// start (its id, when its process was spawned).
#[derive(Clone, Debug)]
enum What {
    Input(Kind, Duration, Option<(Split, Duration)>),
    Start(String, Duration),
}

/// Where the frame just presented is: the page flip asked for it (its
/// number, kms::Display::flips_asked), on the panel already (drawn in
/// place, or headless: drawn), nowhere new (the panel showed it already,
/// or is off).
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Photon {
    Flip(u64),
    At(Duration),
    Unchanged,
}

pub struct Perf {
    inputs: &'static Inputs,
    /// Named by the frame being drawn, until it is presented.
    named: Vec<What>,
    /// Presented, until their flip completes.
    flipping: VecDeque<(u64, What)>,
    key: Ring,
    repeat: Ring,
    touch: Ring,
    draw: Ring,
    present: Ring,
    frame: Ring,
    interval: Ring,
    late: u64,
    dropped: u64,
    /// Frames drawn that changed nothing on the panel.
    idle: u64,
    /// Inputs named by a frame that changed nothing on the panel.
    unchanged: u64,
    /// Inputs whose flip was given up (its completion never seen).
    lost: u64,
    starts: VecDeque<(String, f32)>,
    /// Starts shown since the Shell was last told (`take_shown`).
    shown: Vec<(String, f32)>,
    drawn: Option<Duration>,
    /// The flips asked for by a frame of a motion that showed one before
    /// it: the interval since that one's is a frame of the motion.
    cont: VecDeque<u64>,
    last_flip: Option<(u64, Duration)>,
    /// Keys' and touches' parts (STAGES).
    stages: [[Ring; 6]; 2],
    /// Of the inputs slower than the gate: how many had most of their
    /// time in each part, and the slowest SLOWEST of them.
    slow_by: [u64; 6],
    slowest: Vec<(f32, Kind, Split)>,
}

const STARTS: usize = 64;
const SLOWEST: usize = 10;

impl Perf {
    pub fn new(inputs: &'static Inputs) -> Perf {
        Perf {
            inputs,
            named: Vec::new(),
            flipping: VecDeque::new(),
            key: Ring::default(),
            repeat: Ring::default(),
            touch: Ring::default(),
            draw: Ring::default(),
            present: Ring::default(),
            frame: Ring::default(),
            interval: Ring::default(),
            late: 0,
            dropped: 0,
            idle: 0,
            unchanged: 0,
            lost: 0,
            starts: VecDeque::new(),
            shown: Vec::new(),
            drawn: None,
            cont: VecDeque::new(),
            last_flip: None,
            stages: Default::default(),
            slow_by: [0; 6],
            slowest: Vec::new(),
        }
    }

    /// A frame (or a transition's step) drawn in d; one drawn before it
    /// and never handed over changed nothing.
    pub fn drew(&mut self, d: Duration) {
        if self.drawn.replace(d).is_some() {
            self.idle += 1;
        }
    }

    /// The frame drawn last handed over to the panel in d (the waits for
    /// the flip before, or a period, left out); cont: it follows one
    /// shown in the same motion; flip: the flip asked for it.
    pub fn handed(&mut self, d: Duration, cont: bool, flip: Option<u64>) {
        let draw = self.drawn.take().unwrap_or_default();
        self.draw.push(ms(draw));
        self.present.push(ms(d));
        self.frame.push(ms(draw + d));
        if let (true, Some(f)) = (cont, flip) {
            if self.cont.len() == 64 {
                self.cont.pop_front();
            }
            self.cont.push_back(f);
        }
    }

    /// A frame came naming inputs (ids, with the Shell's stamps of those
    /// it gave) and maybe an app's first page (its id, its start); now is
    /// when it arrived.
    pub fn named(&mut self, ids: &[u64], stamps: &[(u64, ShellStamps)], start: Option<(String, Duration)>, now: Duration) {
        for p in self.inputs.take(ids, now) {
            let parts = stamps.iter().find(|(id, _)| *id == p.id).map(|(_, s)| (split(p.stamp.at, *s, now), now));
            self.named.push(What::Input(p.kind, p.stamp.at, parts));
        }
        if let Some((app, at)) = start {
            self.named.push(What::Start(app, at));
        }
    }

    /// Whether a frame named inputs or a start not presented yet.
    pub fn naming(&self) -> bool {
        !self.named.is_empty()
    }

    /// Whether a flip is awaited to show a named input or start.
    pub fn awaiting(&self) -> bool {
        !self.flipping.is_empty()
    }

    /// The frame naming them was presented.
    pub fn photon(&mut self, p: Photon) {
        for w in std::mem::take(&mut self.named) {
            match p {
                Photon::Flip(n) => self.flipping.push_back((n, w)),
                Photon::At(t) => self.reached(w, t),
                Photon::Unchanged => {
                    if let What::Input(..) = w {
                        self.unchanged += 1;
                    }
                }
            }
        }
    }

    /// Page flip n completed at t, the panel at a period's rate.
    pub fn flipped(&mut self, n: u64, t: Duration, period: Duration) {
        while self.flipping.front().is_some_and(|(f, _)| *f <= n) {
            let Some((f, w)) = self.flipping.pop_front() else { break };
            if f == n {
                self.reached(w, t);
            } else if let What::Input(..) = w {
                self.lost += 1;
            }
        }
        if let Some((l, lt)) = self.last_flip {
            if l + 1 == n && self.cont.contains(&n) {
                let gap = t.saturating_sub(lt);
                self.interval.push(ms(gap));
                if gap > period * 3 / 2 && !period.is_zero() {
                    self.late += 1;
                    self.dropped += (gap.as_secs_f64() / period.as_secs_f64()).round().max(1.0) as u64 - 1;
                }
            }
        }
        self.cont.retain(|&f| f > n);
        self.last_flip = Some((n, t));
    }

    fn reached(&mut self, w: What, t: Duration) {
        match w {
            What::Input(kind, at, parts) => {
                let d = ms(t.saturating_sub(at));
                match kind {
                    Kind::Key => self.key.push(d),
                    Kind::Repeat => self.repeat.push(d),
                    Kind::Touch => self.touch.push(d),
                }
                if let Some((mut sp, took)) = parts {
                    sp[5] = ms(t.saturating_sub(took));
                    self.parts(kind, d, sp);
                }
            }
            What::Start(app, at) => {
                let d = ms(t.saturating_sub(at));
                if self.starts.len() == STARTS {
                    self.starts.pop_front();
                }
                self.starts.push_back((app.clone(), d));
                self.shown.push((app, d));
            }
        }
    }

    /// An input of d ms in its parts sp: into the parts' rings, and when
    /// slower than the gate, into the count of the part that took most.
    fn parts(&mut self, kind: Kind, d: f32, sp: Split) {
        let k = match kind {
            Kind::Key => 0,
            Kind::Touch => 1,
            Kind::Repeat => return,
        };
        for (r, v) in self.stages[k].iter_mut().zip(sp) {
            r.push(v);
        }
        if f64::from(d) <= GATE_INPUT_MS {
            return;
        }
        let most = (0..sp.len()).max_by(|&a, &b| sp[a].total_cmp(&sp[b])).unwrap_or(0);
        self.slow_by[most] += 1;
        self.slowest.push((d, kind, sp));
        self.slowest.sort_by(|a, b| b.0.total_cmp(&a.0));
        self.slowest.truncate(SLOWEST);
    }

    /// The starts shown since the last call: the Shell logs them.
    pub fn take_shown(&mut self) -> Vec<(String, f32)> {
        std::mem::take(&mut self.shown)
    }

    /// Everything measured so far, with the gate's verdict; about is what
    /// the caller adds (the panel, the size, the GPU path).
    pub fn summary(&self, about: serde_json::Value) -> serde_json::Value {
        let n = |i: usize| self.inputs.sources[i].load(Ordering::Relaxed);
        let mut s = serde_json::json!({
            "frames": {"frame_ms": self.frame.json(), "draw_ms": self.draw.json(), "present_ms": self.present.json(), "idle": self.idle},
            "motion": {"interval_ms": self.interval.json(), "late": self.late, "dropped": self.dropped},
            "key": self.key.json(),
            "touch": self.touch.json(),
            "repeat": self.repeat.json(),
            "inputs": {"kernel": n(0), "read": n(1), "play": n(2), "unanswered": self.inputs.unanswered.load(Ordering::Relaxed), "unchanged": self.unchanged, "lost": self.lost},
            "start": self.starts.iter().map(|(a, d)| serde_json::json!({"app": a, "ms": (f64::from(*d) * 10.0).round() / 10.0})).collect::<Vec<_>>(),
        });
        let r = |v: f32| (f64::from(v) * 10.0).round() / 10.0;
        let rings = |k: usize| serde_json::Value::Object(STAGES.iter().zip(&self.stages[k]).map(|(n, ring)| (n.to_string(), ring.json())).collect());
        s["stages"] = serde_json::json!({"key": rings(0), "touch": rings(1)});
        s["slow"] = serde_json::json!({
            "by": serde_json::Value::Object(STAGES.iter().zip(self.slow_by).map(|(n, c)| (n.to_string(), c.into())).collect()),
            "slowest": self.slowest.iter().map(|(d, kind, sp)| {
                let mut v = serde_json::json!({"kind": format!("{kind:?}").to_lowercase(), "ms": r(*d)});
                for (n, x) in STAGES.iter().zip(sp) {
                    v[*n] = r(*x).into();
                }
                v
            }).collect::<Vec<_>>(),
        });
        if let (Some(o), serde_json::Value::Object(a)) = (s.as_object_mut(), about) {
            o.extend(a);
        }
        s["gate"] = gate(&s);
        s
    }
}

/// The gate (39-oneplus-6t.md §8.1): p95 frame time, p95 key- and
/// touch-to-photon, each app's start to its first page shown.
pub const GATE_FRAME_MS: f64 = 16.7;
pub const GATE_INPUT_MS: f64 = 50.0;
pub const GATE_START_MS: f64 = 300.0;

/// The gate's checks on a summary and its verdict: "pass", "fail", or
/// "incomplete" when a check has no samples.
pub fn gate(s: &serde_json::Value) -> serde_json::Value {
    let starts: Vec<(f64, String)> = s["start"].as_array().into_iter().flatten().filter_map(|v| Some((v["ms"].as_f64()?, v["app"].as_str().unwrap_or_default().to_string()))).collect();
    let worst = starts.iter().max_by(|a, b| a.0.total_cmp(&b.0)).cloned();
    let checks = [
        ("frame p95", s["frames"]["frame_ms"]["n"].as_u64().unwrap_or(0), s["frames"]["frame_ms"]["p95"].as_f64(), GATE_FRAME_MS, String::new()),
        ("key p95", s["key"]["n"].as_u64().unwrap_or(0), s["key"]["p95"].as_f64(), GATE_INPUT_MS, String::new()),
        ("touch p95", s["touch"]["n"].as_u64().unwrap_or(0), s["touch"]["p95"].as_f64(), GATE_INPUT_MS, String::new()),
        ("start max", starts.len() as u64, worst.as_ref().map(|w| w.0), GATE_START_MS, worst.map(|w| w.1).unwrap_or_default()),
    ];
    let mut verdict = "pass";
    let list: Vec<serde_json::Value> = checks
        .into_iter()
        .map(|(what, n, v, limit, app)| {
            let state = match v {
                _ if n == 0 => "none",
                Some(v) if v <= limit => "pass",
                _ => "fail",
            };
            verdict = match (verdict, state) {
                ("fail", _) | (_, "fail") => "fail",
                (_, "none") => "incomplete",
                (v, _) => v,
            };
            let mut c = serde_json::json!({"what": what, "n": n, "ms": v.unwrap_or(0.0), "limit": limit, "state": state});
            if !app.is_empty() {
                c["app"] = app.into();
            }
            c
        })
        .collect();
    serde_json::json!({"verdict": verdict, "checks": list})
}

/// The file the summaries go to (hello's `stats`), opened before the
/// sandbox; each summary replaces the one before.
pub struct Out(Option<std::fs::File>);

impl Out {
    pub fn open(path: Option<&str>) -> Out {
        Out(path.filter(|p| !p.is_empty()).and_then(|p| {
            if let Some(dir) = std::path::Path::new(p).parent() {
                let _ = std::fs::create_dir_all(dir);
            }
            std::fs::OpenOptions::new().create(true).write(true).truncate(true).open(p).map_err(|e| eprintln!("phone-paint: stats: {p}: {e}")).ok()
        }))
    }

    pub fn write(&mut self, s: &serde_json::Value) {
        let Some(f) = self.0.as_mut() else { return };
        let text = format!("{s:#}\n");
        let res = f.set_len(0).and_then(|_| f.rewind()).and_then(|_| f.write_all(text.as_bytes()));
        if let Err(e) = res {
            eprintln!("phone-paint: stats: {e}");
        }
    }
}

/// One line for the log at the end.
pub fn line(s: &serde_json::Value) -> String {
    let q = |v: &serde_json::Value| format!("n {} p50 {} p95 {} p99 {}", v["n"], v["p50"], v["p95"], v["p99"]);
    format!(
        "frames {} ms; key-to-photon {} ms; touch-to-photon {} ms; {} late frames ({} dropped) in motion; gate: {}",
        q(&s["frames"]["frame_ms"]),
        q(&s["key"]),
        q(&s["touch"]),
        s["motion"]["late"],
        s["motion"]["dropped"],
        s["gate"]["verdict"].as_str().unwrap_or("?"),
    )
}

/// phone-paint --stats FILE...: the summaries as one table, a row each
/// (named after its file), with the gate's verdict; Err when one cannot
/// be read. Ok(true) when every run passed.
pub fn table(paths: &[String], out: &mut impl Write) -> Result<bool, String> {
    let mut rows = Vec::new();
    for p in paths {
        let text = std::fs::read_to_string(p).map_err(|e| format!("{p}: {e}"))?;
        let s: serde_json::Value = serde_json::from_str(&text).map_err(|e| format!("{p}: {e}"))?;
        let name = std::path::Path::new(p).file_stem().map_or(p.clone(), |n| n.to_string_lossy().into_owned());
        rows.push((name, s));
    }
    let w = rows.iter().map(|(n, _)| n.len()).max().unwrap_or(3).max(3);
    let drawn = |s: &serde_json::Value| format!("{} {}x{}", s["gpu"].as_str().unwrap_or("?"), s["size"][0], s["size"][1]);
    let dw = rows.iter().map(|(_, s)| drawn(s).len()).max().unwrap_or(5).max(5);
    let mut all = true;
    let _ = writeln!(out, "gate: frame p95 <= {GATE_FRAME_MS} ms, key and touch p95 <= {GATE_INPUT_MS} ms, app start <= {GATE_START_MS} ms (ms: p50/p95/p99)");
    let _ = writeln!(out, "{:<w$}  {:<dw$}  {:>6} {:>17} {:>9} {:>5} {:>17} {:>5} {:>17} {:>9}  gate", "run", "drawn", "frames", "frame ms", "late/drop", "keys", "key ms", "touch", "touch ms", "start ms");
    for (name, s) in &rows {
        let q = |v: &serde_json::Value| if v["n"].as_u64().unwrap_or(0) == 0 { "-".to_string() } else { format!("{:.1}/{:.1}/{:.1}", v["p50"].as_f64().unwrap_or(0.0), v["p95"].as_f64().unwrap_or(0.0), v["p99"].as_f64().unwrap_or(0.0)) };
        let start = s["start"].as_array().into_iter().flatten().filter_map(|v| v["ms"].as_f64()).fold(None, |m: Option<f64>, v| Some(m.map_or(v, |m| m.max(v))));
        // A run whose playback did not get to its end is not the sequence.
        let played = s["play"].is_null() || s["play"]["state"] == "done";
        let verdict = if played { s["gate"]["verdict"].as_str().unwrap_or("?") } else { "fail" };
        all &= verdict == "pass";
        let _ = writeln!(
            out,
            "{:<w$}  {:<dw$}  {:>6} {:>17} {:>9} {:>5} {:>17} {:>5} {:>17} {:>9}  {}",
            name,
            drawn(s),
            s["frames"]["frame_ms"]["n"],
            q(&s["frames"]["frame_ms"]),
            format!("{}/{}", s["motion"]["late"], s["motion"]["dropped"]),
            s["key"]["n"],
            q(&s["key"]),
            s["touch"]["n"],
            q(&s["touch"]),
            start.map_or("-".to_string(), |v| format!("{v:.0}")),
            verdict.to_uppercase(),
        );
    }
    for (name, s) in &rows {
        for c in s["gate"]["checks"].as_array().into_iter().flatten().filter(|c| c["state"] != "pass") {
            let app = c["app"].as_str().map_or(String::new(), |a| format!(" ({a})"));
            let what = c["what"].as_str().unwrap_or("?");
            match c["state"].as_str() {
                Some("fail") => _ = writeln!(out, "{name}: {what} {} ms{app} > {} ms", c["ms"], c["limit"]),
                _ => _ = writeln!(out, "{name}: {what}: not measured"),
            }
        }
        if let Some(e) = s["play"]["error"].as_str().filter(|e| !e.is_empty()) {
            let _ = writeln!(out, "{name}: play: {e}");
        } else if !s["play"].is_null() && s["play"]["state"] != "done" {
            let _ = writeln!(out, "{name}: play: {} at line {}", s["play"]["state"].as_str().unwrap_or("?"), s["play"]["line"]);
        }
        if let Some(gl) = s["gl"].as_str() {
            let _ = writeln!(out, "{name}: gl: {gl}");
        }
        for kind in ["key", "touch"] {
            let st = &s["stages"][kind];
            if st["show"]["n"].as_u64().unwrap_or(0) > 0 {
                let p: Vec<String> = STAGES.iter().map(|k| format!("{k} {:.1}", st[*k]["p95"].as_f64().unwrap_or(0.0))).collect();
                let _ = writeln!(out, "{name}: {kind} p95 by part (ms): {}", p.join(", "));
            }
        }
        let by = &s["slow"]["by"];
        let slow: Vec<String> = STAGES.iter().filter_map(|k| by[*k].as_u64().filter(|&n| n > 0).map(|n| format!("{k} {n}"))).collect();
        if !slow.is_empty() {
            let _ = writeln!(out, "{name}: inputs over {GATE_INPUT_MS} ms by the part that took most: {}", slow.join(", "));
        }
    }
    let _ = writeln!(out, "{}", if all { "PASS" } else { "FAIL" });
    Ok(all)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn inputs() -> &'static Inputs {
        Box::leak(Box::new(Inputs::new()))
    }

    fn at(ms: u64) -> Duration {
        Duration::from_millis(ms)
    }

    const P60: Duration = Duration::from_nanos(16_666_667);

    #[test]
    fn a_key_is_timed_from_its_event_to_the_flip_that_shows_it() {
        let inp = inputs();
        let mut p = Perf::new(inp);
        let id = inp.add(Kind::Key, Stamp { at: at(1000), src: Source::Kernel });
        assert!(inp.waiting(id));
        p.named(&[id], &[], None, at(1010));
        assert!(!inp.waiting(id));
        p.drew(at(5));
        p.handed(at(2), false, Some(7));
        p.photon(Photon::Flip(7));
        assert!(p.awaiting());
        // An earlier flip completing does not answer it.
        p.flipped(6, at(1020), P60);
        assert_eq!(p.key.n, 0);
        p.flipped(7, at(1032), P60);
        assert!(!p.awaiting());
        assert_eq!(p.key.v, [32.0]);
        assert_eq!(p.frame.v, [7.0]);
    }

    #[test]
    fn inputs_are_told_apart_by_kind_and_the_ones_shown_in_place_at_once() {
        let inp = inputs();
        let mut p = Perf::new(inp);
        let t = inp.add(Kind::Touch, Stamp { at: at(100), src: Source::Kernel });
        let r = inp.add(Kind::Repeat, Stamp { at: at(110), src: Source::Read });
        p.named(&[t, r], &[], None, at(115));
        p.photon(Photon::At(at(130)));
        assert_eq!(p.touch.v, [30.0]);
        assert_eq!(p.repeat.v, [20.0]);
        assert_eq!(p.key.n, 0);
    }

    #[test]
    fn a_frame_that_changed_nothing_or_a_flip_never_seen_is_counted_not_timed() {
        let inp = inputs();
        let mut p = Perf::new(inp);
        let a = inp.add(Kind::Key, Stamp { at: at(0), src: Source::Kernel });
        p.named(&[a], &[], None, at(5));
        p.photon(Photon::Unchanged);
        let b = inp.add(Kind::Key, Stamp { at: at(10), src: Source::Kernel });
        p.named(&[b], &[], None, at(15));
        p.photon(Photon::Flip(3));
        // Flip 3 was given up on; 4 completes.
        p.flipped(4, at(50), P60);
        assert_eq!((p.unchanged, p.lost, p.key.n), (1, 1, 0));
        assert!(!p.awaiting());
    }

    #[test]
    fn inputs_no_frame_names_go_as_unanswered() {
        let inp = inputs();
        let mut p = Perf::new(inp);
        let old = inp.add(Kind::Touch, Stamp { at: at(0), src: Source::Kernel });
        let new = inp.add(Kind::Key, Stamp { at: at(4000), src: Source::Kernel });
        p.named(&[new], &[], None, at(4010));
        assert!(!inp.waiting(old));
        assert_eq!(inp.unanswered.load(Ordering::Relaxed), 1);
        for _ in 0..PENDING_MAX + 2 {
            inp.add(Kind::Key, Stamp { at: at(5000), src: Source::Play });
        }
        assert_eq!(inp.unanswered.load(Ordering::Relaxed), 3);
    }

    #[test]
    fn a_start_is_timed_to_its_first_page_on_the_panel_and_told_once() {
        let inp = inputs();
        let mut p = Perf::new(inp);
        p.named(&[], &[], Some(("com.phone.contacts".into(), at(2000))), at(2200));
        assert!(p.naming());
        p.photon(Photon::Flip(1));
        p.flipped(1, at(2250), P60);
        assert_eq!(p.take_shown(), [("com.phone.contacts".to_string(), 250.0)]);
        assert!(p.take_shown().is_empty());
        let s = p.summary(serde_json::json!({}));
        assert_eq!(s["start"][0]["ms"], 250.0);
    }

    #[test]
    fn frames_of_a_motion_late_against_the_panel_count_their_dropped_frames() {
        let mut p = Perf::new(inputs());
        // A motion: flips 1..4; 2 on time, 3 two periods late, 4 on time.
        for (n, cont) in [(1, false), (2, true), (3, true), (4, true)] {
            p.drew(at(3));
            p.handed(at(1), cont, Some(n));
        }
        let t = [0.0, 16.7, 66.7, 83.4];
        for (i, ms) in t.iter().enumerate() {
            p.flipped(i as u64 + 1, Duration::from_secs_f64(1.0 + ms / 1000.0), P60);
        }
        assert_eq!(p.interval.n, 3);
        assert_eq!((p.late, p.dropped), (1, 2));
        // A frame on its own after the motion is no interval.
        p.drew(at(3));
        p.handed(at(1), false, Some(9));
        p.flipped(9, at(5000), P60);
        assert_eq!(p.interval.n, 3);
    }

    #[test]
    fn a_frame_drawn_and_not_shown_is_idle() {
        let mut p = Perf::new(inputs());
        p.drew(at(4));
        p.drew(at(6));
        p.handed(at(1), false, None);
        assert_eq!(p.idle, 1);
        assert_eq!(p.frame.v, [7.0]);
    }

    #[test]
    fn quantiles_are_nearest_rank_over_the_ring() {
        let mut r = Ring::default();
        for v in 1..=20 {
            r.push(v as f32);
        }
        assert_eq!((r.q(0.5), r.q(0.95), r.q(0.99), r.q(1.0)), (10.0, 19.0, 20.0, 20.0));
        for _ in 0..RING {
            r.push(1.0);
        }
        assert_eq!((r.n, r.q(1.0)), (20 + RING as u64, 1.0));
        assert_eq!(Ring::default().q(0.95), 0.0);
    }

    fn summary(frame_ms: f32, key_ms: f32, touches: usize, start: Option<f32>) -> serde_json::Value {
        let inp = inputs();
        let mut p = Perf::new(inp);
        for _ in 0..20 {
            p.drew(Duration::from_secs_f32(frame_ms / 1000.0));
            p.handed(Duration::ZERO, false, None);
            let id = inp.add(Kind::Key, Stamp { at: at(0), src: Source::Play });
            p.named(&[id], &[], None, at(0));
            p.photon(Photon::At(Duration::from_secs_f32(key_ms / 1000.0)));
        }
        for _ in 0..touches {
            let id = inp.add(Kind::Touch, Stamp { at: at(0), src: Source::Kernel });
            p.named(&[id], &[], None, at(0));
            p.photon(Photon::At(at(20)));
        }
        if let Some(s) = start {
            p.named(&[], &[], Some(("com.phone.gallery".into(), at(0))), at(0));
            p.photon(Photon::At(Duration::from_secs_f32(s / 1000.0)));
        }
        p.summary(serde_json::json!({"panel": "test"}))
    }

    #[test]
    fn the_gate_passes_fails_or_is_incomplete() {
        let s = summary(10.0, 30.0, 3, Some(200.0));
        assert_eq!(s["gate"]["verdict"], "pass", "{s:#}");
        assert_eq!(s["panel"], "test");
        assert_eq!(s["inputs"]["play"], 20);
        assert_eq!(s["inputs"]["kernel"], 3);
        let s = summary(20.0, 30.0, 3, Some(200.0));
        assert_eq!(s["gate"]["verdict"], "fail");
        assert_eq!(s["gate"]["checks"][0]["state"], "fail");
        let s = summary(10.0, 60.0, 3, Some(200.0));
        assert_eq!(s["gate"]["checks"][1]["state"], "fail");
        let s = summary(10.0, 30.0, 3, Some(400.0));
        assert_eq!(s["gate"]["checks"][3]["state"], "fail");
        assert_eq!(s["gate"]["checks"][3]["app"], "com.phone.gallery");
        let s = summary(10.0, 30.0, 0, None);
        assert_eq!(s["gate"]["verdict"], "incomplete");
        assert_eq!(s["gate"]["checks"][2]["state"], "none");
    }

    #[test]
    fn the_table_has_a_row_a_run_and_says_why_one_failed() {
        let dir = std::env::temp_dir().join(format!("perf-table-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let good = dir.join("cpu-full.json");
        let bad = dir.join("gpu-target.json");
        Out::open(good.to_str()).write(&summary(10.0, 30.0, 3, Some(200.0)));
        let mut out = Out::open(bad.to_str());
        out.write(&summary(30.0, 30.0, 3, Some(200.0)));
        // A later summary replaces the earlier one whole.
        out.write(&summary(20.0, 30.0, 3, Some(200.0)));
        let cut = dir.join("raster-full.json");
        let mut s = summary(10.0, 30.0, 3, Some(200.0));
        s["play"] = serde_json::json!({"state": "running", "line": 12, "error": ""});
        s["gpu"] = "raster".into();
        s["size"] = serde_json::json!([1080, 2180]);
        Out::open(cut.to_str()).write(&s);
        let mut text = Vec::new();
        let all = table(&[good.to_string_lossy().into(), bad.to_string_lossy().into()], &mut text).unwrap();
        let text = String::from_utf8(text).unwrap();
        assert!(!all);
        assert!(text.lines().any(|l| l.starts_with("cpu-full ") && l.ends_with("PASS")), "{text}");
        assert!(text.lines().any(|l| l.starts_with("gpu-target ") && l.ends_with("FAIL")), "{text}");
        assert!(text.contains("gpu-target: frame p95 20.0 ms > 16.7 ms"), "{text}");
        assert!(text.ends_with("FAIL\n"));
        // The gate's numbers pass, but the sequence stopped at line 12.
        let mut text = Vec::new();
        assert!(!table(&[good.to_string_lossy().into(), cut.to_string_lossy().into()], &mut text).unwrap());
        let text = String::from_utf8(text).unwrap();
        assert!(text.lines().any(|l| l.starts_with("raster-full  raster 1080x2180 ") && l.ends_with("FAIL")), "{text}");
        assert!(text.contains("raster-full: play: running at line 12"), "{text}");
        let mut text = Vec::new();
        assert!(table(&[good.to_string_lossy().into()], &mut text).unwrap());
        assert!(String::from_utf8(text).unwrap().ends_with("PASS\n"));
        assert!(table(&[dir.join("missing.json").to_string_lossy().into()], &mut Vec::new()).is_err());
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn kernel_times_are_taken_only_from_a_monotonic_clock() {
        let now = mono();
        let t = std::time::UNIX_EPOCH + now - at(5);
        assert_eq!(event_time(t, true), Stamp { at: now - at(5), src: Source::Kernel });
        assert_eq!(event_time(t, false).src, Source::Read);
        // The realtime clock's 2026, not a monotonic time.
        assert_eq!(event_time(std::time::SystemTime::now(), true).src, Source::Read);
    }

    #[test]
    fn keys_and_touches_get_ids_the_power_key_and_others_none() {
        let inp = inputs();
        let st = Stamp { at: at(0), src: Source::Play };
        let mut k = serde_json::json!({"t": "key", "key": "down", "text": ""});
        inp.stamp(&mut k, Kind::Key, st);
        let mut t = serde_json::json!({"t": "touch", "phase": "down"});
        inp.stamp(&mut t, Kind::Touch, st);
        assert!(t["in"].as_u64().unwrap() > k["in"].as_u64().unwrap());
        let mut p = serde_json::json!({"t": "key", "key": "power"});
        inp.stamp(&mut p, Kind::Key, st);
        let mut i = serde_json::json!({"t": "input"});
        inp.stamp(&mut i, Kind::Key, st);
        assert!(p.get("in").is_none() && i.get("in").is_none());
    }
}
