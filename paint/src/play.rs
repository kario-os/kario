//! PHONE_PAINT_PLAY=<file>: a fixed sequence of keys and touches played
//! into the renderer as a keyboard and a touchscreen would give them (the
//! gate's run, 39-oneplus-6t.md §8.1: tools/phone-chroot's perf step,
//! tools/perf/host.sh). Keys go through the keyboards' own state machine
//! (kms::Keyboard) and touches through the main loop's, so the Shell gets
//! what it gets from the hardware; each is timed (perf.rs) from when it is
//! made, the kernel's delivery left out. One step a line:
//!
//!   wait <text> [ms]      until the frame's screen id contains text
//!                         (20 s unless given)
//!   key <name> [n]        n taps of a key ("down", "enter", "f4", "5"),
//!                         each waited for until a frame shows it (1.5 s
//!                         at most: one that changes nothing gets none)
//!   type <chars>          a tap of each character (a 9-key pad's digits)
//!   open <dir>            in the grid on screen (the main menu), to the
//!                         tile whose icon is in a directory <dir>, then OK
//!   drag <x0> <y0> <x1> <y1> [ms]
//!                         a finger from one point to the other, logical
//!                         pixels or percentages of the frame (300 ms
//!                         unless given), a report every 8 ms
//!   gap <ms>              the pause after each tap and drag (200 ms)
//!   sleep <ms>
//!
//! `#` starts a comment.

use std::collections::VecDeque;
use std::time::{Duration, Instant};

use crate::frame::Frame;
use crate::kms::Keyboard;
use crate::perf::{Inputs, Kind, Source, Stamp};
use crate::touch::Phase;

#[derive(Debug, PartialEq, Clone)]
enum Step {
    Wait(String, Duration),
    Key(String, usize),
    Type(String),
    Open(String),
    Drag([Coord; 4], Duration),
    Gap(Duration),
    Sleep(Duration),
}

/// A drag's coordinate: logical pixels, or a share of the frame's side.
#[derive(Debug, PartialEq, Clone, Copy)]
enum Coord {
    Px(f32),
    Share(f32),
}

impl Coord {
    fn parse(s: &str) -> Option<Coord> {
        match s.strip_suffix('%') {
            Some(p) => p.parse::<f32>().ok().filter(|v| (0.0..=100.0).contains(v)).map(|v| Coord::Share(v / 100.0)),
            None => s.parse::<f32>().ok().filter(|v| *v >= 0.0).map(Coord::Px),
        }
    }

    fn on(self, side: f32) -> f32 {
        match self {
            Coord::Px(v) => v.min(side - 1.0),
            Coord::Share(f) => (f * side).min(side - 1.0),
        }
    }
}

/// What the main loop is to do: send a key message, take a touch report
/// (logical pixels of the frame) as from the touchscreen.
#[derive(Debug, PartialEq)]
pub enum Act {
    Key(serde_json::Value),
    Touch(Phase, f32, f32, Stamp),
}

enum Wait {
    Until(Instant),
    Screen(String, Instant),
    Answer(Vec<u64>, Instant),
}

struct Drag {
    from: (f32, f32),
    to: (f32, f32),
    start: Instant,
    dur: Duration,
    down: bool,
}

const ANSWER: Duration = Duration::from_millis(1500);
const REPORT: Duration = Duration::from_millis(8);

pub struct Play {
    steps: Vec<(usize, Step)>,
    next: usize,
    wait: Option<Wait>,
    taps: VecDeque<String>,
    drag: Option<Drag>,
    gap: Duration,
    error: Option<String>,
    /// The line of the step running.
    line: usize,
}

fn ms(s: &str) -> Result<Duration, String> {
    s.parse::<u64>().map(Duration::from_millis).map_err(|_| format!("{s:?} is not a number of ms"))
}

/// The steps of a script, each with its line number.
fn parse(text: &str) -> Result<Vec<(usize, Step)>, String> {
    let mut out = Vec::new();
    for (i, raw) in text.lines().enumerate() {
        let line = raw.split('#').next().unwrap_or("").trim();
        if line.is_empty() {
            continue;
        }
        let w: Vec<&str> = line.split_whitespace().collect();
        let bad = |why: String| format!("line {}: {why}: {raw}", i + 1);
        let step = match (w[0], &w[1..]) {
            ("wait", [t]) => Step::Wait(t.to_string(), Duration::from_secs(20)),
            ("wait", [t, m]) => Step::Wait(t.to_string(), ms(m).map_err(bad)?),
            ("key", [k]) => Step::Key(k.to_string(), 1),
            ("key", [k, n]) => Step::Key(k.to_string(), n.parse().map_err(|_| bad(format!("{n:?} is not a count")))?),
            ("type", [t]) => Step::Type(t.to_string()),
            ("open", [d]) => Step::Open(d.to_string()),
            ("drag", [a, b, c, d, rest @ ..]) if rest.len() <= 1 => {
                let mut cs = [Coord::Px(0.0); 4];
                for (c, s) in cs.iter_mut().zip([a, b, c, d]) {
                    *c = Coord::parse(s).ok_or_else(|| bad(format!("{s:?} is not a coordinate")))?;
                }
                let dur = rest.first().map_or(Ok(Duration::from_millis(300)), |m| ms(m)).map_err(bad)?;
                Step::Drag(cs, dur)
            }
            ("gap", [m]) => Step::Gap(ms(m).map_err(bad)?),
            ("sleep", [m]) => Step::Sleep(ms(m).map_err(bad)?),
            _ => return Err(bad("not a step".into())),
        };
        if let Step::Key(k, _) = &step {
            if crate::kms::play_code(k).is_none() {
                return Err(bad(format!("no key {k:?}")));
            }
        }
        if let Step::Type(t) = &step {
            if let Some(c) = t.chars().find(|c| crate::kms::play_code(&c.to_string()).is_none()) {
                return Err(bad(format!("no key types {c:?}")));
            }
        }
        out.push((i + 1, step));
    }
    Ok(out)
}

/// In the grid on screen, the taps from its focused tile to the one whose
/// icon is in a directory dir, and OK.
fn route(f: &Frame, dir: &str) -> Result<Vec<String>, String> {
    let part = format!("/{dir}/");
    for n in f.body.iter().filter(|n| n.kind == "grid") {
        let Some(to) = n.tiles.iter().position(|t| t.image.contains(&part)) else { continue };
        let cols = n.columns.max(1) as usize;
        let from = usize::try_from(n.sel).map_err(|_| format!("open {dir}: nothing focused in the grid"))?;
        let (dr, dc) = (to as i64 / cols as i64 - from as i64 / cols as i64, to as i64 % cols as i64 - from as i64 % cols as i64);
        let mut taps = Vec::new();
        taps.extend(std::iter::repeat_n(if dr > 0 { "down" } else { "up" }, dr.unsigned_abs() as usize));
        taps.extend(std::iter::repeat_n(if dc > 0 { "right" } else { "left" }, dc.unsigned_abs() as usize));
        taps.push("enter");
        return Ok(taps.into_iter().map(String::from).collect());
    }
    Err(format!("open {dir}: no tile of it on {}", f.screen))
}

impl Play {
    pub fn load(path: &std::ffi::OsStr) -> Result<Play, String> {
        let text = std::fs::read_to_string(path).map_err(|e| format!("{}: {e}", path.to_string_lossy()))?;
        Ok(Play::new(parse(&text)?))
    }

    fn new(steps: Vec<(usize, Step)>) -> Play {
        Play { steps, next: 0, wait: None, taps: VecDeque::new(), drag: None, gap: Duration::from_millis(200), error: None, line: 0 }
    }

    /// Whether it has ended, played through or failed.
    pub fn ended(&self) -> bool {
        self.error.is_some() || (self.next == self.steps.len() && self.wait.is_none() && self.taps.is_empty() && self.drag.is_none())
    }

    /// When `step` is next due without a frame coming.
    pub fn due(&self) -> Option<Instant> {
        if self.ended() {
            return None;
        }
        match &self.wait {
            Some(Wait::Until(t) | Wait::Screen(_, t) | Wait::Answer(_, t)) => Some(*t),
            None => Some(Instant::now()),
        }
    }

    pub fn state(&self) -> serde_json::Value {
        let state = match (&self.error, self.ended()) {
            (Some(_), _) => "failed",
            (None, true) => "done",
            _ => "running",
        };
        serde_json::json!({"state": state, "line": self.line, "error": self.error.clone().unwrap_or_default()})
    }

    /// Runs what is due at now on frame f (size: the frame's, logical
    /// pixels): what the main loop is to do.
    pub fn step(&mut self, now: Instant, f: &Frame, size: (f32, f32), inputs: &Inputs, kb: &mut Keyboard) -> Vec<Act> {
        let mut acts = Vec::new();
        while self.error.is_none() {
            match &self.wait {
                Some(Wait::Until(t)) if now < *t => return acts,
                Some(Wait::Screen(s, t)) if !f.screen.contains(s.as_str()) => {
                    if now >= *t {
                        self.error = Some(format!("line {}: no screen {s} in time (on {:?})", self.line, f.screen));
                    }
                    return acts;
                }
                Some(Wait::Answer(ids, t)) if now < *t && ids.iter().any(|&id| inputs.waiting(id)) => return acts,
                Some(Wait::Answer(..)) => self.wait = Some(Wait::Until(now + self.gap)),
                _ => self.wait = None,
            }
            if self.wait.is_some() {
                continue;
            }
            if let Some(name) = self.taps.pop_front() {
                let ids = self.tap(&name, now, inputs, kb, &mut acts);
                self.wait = Some(if ids.is_empty() { Wait::Until(now + self.gap) } else { Wait::Answer(ids, now + ANSWER) });
                return acts;
            }
            if let Some(d) = self.drag.as_mut() {
                let t = now.duration_since(d.start).as_secs_f32() / d.dur.as_secs_f32().max(0.001);
                let at = |t: f32| (d.from.0 + (d.to.0 - d.from.0) * t, d.from.1 + (d.to.1 - d.from.1) * t);
                let stamp = Stamp::now(Source::Play);
                if !d.down {
                    d.down = true;
                    acts.push(Act::Touch(Phase::Down, d.from.0, d.from.1, stamp));
                } else if t < 1.0 {
                    let (x, y) = at(t);
                    acts.push(Act::Touch(Phase::Move, x, y, stamp));
                } else {
                    acts.push(Act::Touch(Phase::Move, d.to.0, d.to.1, stamp));
                    acts.push(Act::Touch(Phase::Up, d.to.0, d.to.1, stamp));
                    self.drag = None;
                    self.wait = Some(Wait::Until(now + self.gap));
                    return acts;
                }
                self.wait = Some(Wait::Until(now + REPORT));
                return acts;
            }
            let Some((line, step)) = self.steps.get(self.next).cloned() else { return acts };
            self.next += 1;
            self.line = line;
            match step {
                Step::Wait(s, d) => self.wait = Some(Wait::Screen(s, now + d)),
                Step::Key(k, n) => self.taps.extend(std::iter::repeat_n(k, n)),
                Step::Type(t) => self.taps.extend(t.chars().map(String::from)),
                Step::Open(dir) => match route(f, &dir) {
                    Ok(taps) => self.taps.extend(taps),
                    Err(e) => self.error = Some(format!("line {line}: {e}")),
                },
                Step::Drag(c, dur) => {
                    let (w, h) = size;
                    self.drag = Some(Drag { from: (c[0].on(w), c[1].on(h)), to: (c[2].on(w), c[3].on(h)), start: now, dur, down: false });
                }
                Step::Gap(d) => self.gap = d,
                Step::Sleep(d) => self.wait = Some(Wait::Until(now + d)),
            }
        }
        acts
    }

    /// A press and release of the key name: the messages for the Shell,
    /// each with its id, into acts; the ids.
    fn tap(&mut self, name: &str, now: Instant, inputs: &Inputs, kb: &mut Keyboard, acts: &mut Vec<Act>) -> Vec<u64> {
        let Some(code) = crate::kms::play_code(name) else {
            self.error = Some(format!("line {}: no key {name:?}", self.line));
            return Vec::new();
        };
        let mut ids = Vec::new();
        let mut msgs = kb.key(code, 1, now);
        msgs.extend(kb.key(code, 0, now));
        for mut m in msgs {
            inputs.stamp(&mut m, Kind::Key, Stamp::now(Source::Play));
            if let Some(id) = m["in"].as_u64() {
                ids.push(id);
            }
            acts.push(Act::Key(m));
        }
        ids
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn frame(v: serde_json::Value) -> Frame {
        serde_json::from_value(v).unwrap()
    }

    fn menu(sel: i32) -> Frame {
        let tile = |dir: &str| serde_json::json!({"title": dir, "image": format!("/usr/share/phone/apps/{dir}/icon.png")});
        frame(serde_json::json!({"screen": "sys/sys.menu", "body": [{"kind": "grid", "columns": 3, "sel": sel,
            "tiles": [tile("messages"), tile("dialer"), tile("contacts"), tile("camera"), tile("gallery"), tile("music"), tile("settings")]}]}))
    }

    #[test]
    fn a_script_parses_and_a_bad_line_is_named() {
        let s = parse("# a run\nwait sys.idle 60000\nkey down 3 # three\ntype 4663\nopen contacts\ndrag 50% 80% 50% 20% 250\ngap 100\nsleep 5\n").unwrap();
        assert_eq!(s.len(), 7);
        assert_eq!(s[0], (2, Step::Wait("sys.idle".into(), Duration::from_secs(60))));
        assert_eq!(s[1], (3, Step::Key("down".into(), 3)));
        assert_eq!(s[4].1, Step::Drag([Coord::Share(0.5), Coord::Share(0.8), Coord::Share(0.5), Coord::Share(0.2)], Duration::from_millis(250)));
        assert!(parse("key nosuch").unwrap_err().contains("line 1: no key"));
        assert!(parse("\nflip it").unwrap_err().starts_with("line 2: not a step"));
        assert!(parse("drag 1 2 3").is_err());
        assert!(parse("type 4€").is_err());
    }

    #[test]
    fn open_goes_rows_then_columns_to_the_tile_then_ok() {
        assert_eq!(route(&menu(0), "settings").unwrap(), ["down", "down", "enter"]);
        assert_eq!(route(&menu(0), "contacts").unwrap(), ["right", "right", "enter"]);
        assert_eq!(route(&menu(5), "messages").unwrap(), ["up", "left", "left", "enter"]);
        assert!(route(&menu(0), "weather").unwrap_err().contains("no tile"));
        assert!(route(&menu(-1), "gallery").unwrap_err().contains("nothing focused"));
    }

    fn run(text: &str) -> (Play, &'static Inputs, HashMap<String, String>) {
        (Play::new(parse(text).unwrap()), Box::leak(Box::new(Inputs::new())), HashMap::new())
    }

    #[test]
    fn keys_wait_for_their_frame_then_the_gap() {
        let (mut p, inputs, layer) = run("wait sys.idle\nkey down 2\ngap 50\nkey enter");
        let mut kb = Keyboard::new(false, &layer, None);
        let t0 = Instant::now();
        let idle = frame(serde_json::json!({"screen": "sys/sys.boot"}));
        assert!(p.step(t0, &idle, (240.0, 320.0), inputs, &mut kb).is_empty());
        let idle = frame(serde_json::json!({"screen": "sys/sys.idle"}));
        let acts = p.step(t0, &idle, (240.0, 320.0), inputs, &mut kb);
        let [Act::Key(m)] = &acts[..] else { panic!("{acts:?}") };
        assert_eq!(m["key"], "down");
        let id = m["in"].as_u64().unwrap();
        // Not answered: nothing more.
        assert!(p.step(t0 + Duration::from_millis(100), &idle, (240.0, 320.0), inputs, &mut kb).is_empty());
        // A frame named it: the gap, then the next.
        inputs.take(&[id], crate::perf::mono());
        assert!(p.step(t0 + Duration::from_millis(110), &idle, (240.0, 320.0), inputs, &mut kb).is_empty());
        let acts = p.step(t0 + Duration::from_millis(320), &idle, (240.0, 320.0), inputs, &mut kb);
        assert_eq!(acts.len(), 1);
        // Never answered (it changed nothing): after ANSWER, the gap, the
        // next, after the new gap.
        let late = t0 + Duration::from_millis(320) + ANSWER;
        assert!(p.step(late, &idle, (240.0, 320.0), inputs, &mut kb).is_empty());
        let t1 = late + Duration::from_millis(200);
        let acts = p.step(t1, &idle, (240.0, 320.0), inputs, &mut kb);
        let [Act::Key(m)] = &acts[..] else { panic!("{acts:?}") };
        assert_eq!(m["key"], "enter");
        p.step(t1 + ANSWER, &idle, (240.0, 320.0), inputs, &mut kb);
        assert!(!p.ended());
        p.step(t1 + ANSWER + Duration::from_millis(50), &idle, (240.0, 320.0), inputs, &mut kb);
        assert!(p.ended());
        assert_eq!(p.state()["state"], "done");
    }

    #[test]
    fn a_screen_that_never_comes_fails_the_run() {
        let (mut p, inputs, layer) = run("wait com.phone.contacts/list 100");
        let mut kb = Keyboard::new(false, &layer, None);
        let t0 = Instant::now();
        let f = frame(serde_json::json!({"screen": "sys/sys.menu"}));
        p.step(t0, &f, (240.0, 320.0), inputs, &mut kb);
        assert_eq!(p.state()["state"], "running");
        p.step(t0 + Duration::from_millis(100), &f, (240.0, 320.0), inputs, &mut kb);
        assert_eq!(p.state()["state"], "failed");
        assert!(p.state()["error"].as_str().unwrap().contains("no screen com.phone.contacts/list"));
        assert!(p.due().is_none());
    }

    #[test]
    fn a_nine_key_digit_goes_on_its_release() {
        let (mut p, inputs, layer) = run("type 4");
        let mut kb = Keyboard::new(true, &layer, None);
        let f = frame(serde_json::json!({"screen": "x"}));
        let acts = p.step(Instant::now(), &f, (240.0, 320.0), inputs, &mut kb);
        let [Act::Key(m)] = &acts[..] else { panic!("{acts:?}") };
        assert_eq!(m["text"], "4");
        assert!(m["in"].is_u64());
    }

    #[test]
    fn a_drag_is_down_moves_every_report_and_up() {
        let (mut p, inputs, layer) = run("drag 50% 75% 50% 25% 32");
        let mut kb = Keyboard::new(false, &layer, None);
        let f = frame(serde_json::json!({"screen": "x"}));
        let t0 = Instant::now();
        let mut all = Vec::new();
        let mut t = t0;
        while !p.ended() && t < t0 + Duration::from_secs(2) {
            all.extend(p.step(t, &f, (240.0, 320.0), inputs, &mut kb));
            t += REPORT;
        }
        let phases: Vec<Phase> = all.iter().map(|a| match a {
            Act::Touch(ph, ..) => *ph,
            Act::Key(_) => panic!("a key"),
        }).collect();
        assert_eq!(phases.first(), Some(&Phase::Down));
        assert_eq!(phases.last(), Some(&Phase::Up));
        assert!(phases.iter().filter(|&&p| p == Phase::Move).count() >= 4, "{phases:?}");
        let Act::Touch(_, x, y, _) = all[0] else { unreachable!() };
        assert_eq!((x, y), (120.0, 240.0));
        let Act::Touch(_, _, y, _) = all[all.len() - 1] else { unreachable!() };
        assert_eq!(y, 80.0);
    }
}
