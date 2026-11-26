//! Headless, each frame received is also drawn the way the live display
//! draws it (recorded, only the damage painted, each step of the motion it
//! starts on a clock advanced a period of the panel's rate at a time, the
//! shell's "rate" or else 60 Hz, a transition's steps composed), with no
//! panel, and the time of each step kept: with PHONE_PAINT_STEPS=<file>
//! (the pixel selftest, 37-motion.md §5) appended to the file, a line a
//! frame, {"draw": [ms, ...], "transition": [ms, ...]}, which the selftest
//! sums up at its end; asked by the Shell's hello ("timing": the
//! performance timeline, 23-renderer-protocol.md) in a "drawn" message. A
//! step's time is this machine's, not a phone's. With PHONE_PAINT_GPU=1 (or
//! soft) the steps are the GPU path's (gpu.rs; without a GPU, Mesa's
//! llvmpipe on this machine's cores), each until the GPU finished.

use std::io::Write;
use std::time::{Duration, Instant};

use tiny_skia::Pixmap;

use crate::canvas::{Canvas, Fonts};
use crate::frame::Frame;
use crate::gpu::{Ask, Gpu};
use crate::layers::Layers;
use crate::{motion, pace, paint};

/// The times of one frame's steps, in milliseconds.
pub struct Steps {
    pub draw: Vec<f64>,
    pub transition: Vec<f64>,
}

pub struct Timing {
    out: Option<std::fs::File>,
    st: paint::State,
    layers: Layers,
    gpu: Option<Gpu>,
    size: (u32, u32),
    scale: f32,
    fonts: &'static Fonts,
    clock: Instant,
    last_tn: u64,
    last_s: f32,
    shown: bool,
    period: Duration,
}

pub fn ms(t0: Instant) -> f64 {
    (t0.elapsed().as_secs_f64() * 1e6).round() / 1e3
}

impl Timing {
    /// A timing that writes each frame's steps to path, or (None) keeps
    /// them for the caller only.
    pub fn open(path: Option<&std::ffi::OsStr>, width: u32, height: u32, scale: f32, fonts: &'static Fonts) -> Option<Timing> {
        let out = match path {
            Some(path) => {
                if let Some(dir) = std::path::Path::new(path).parent() {
                    let _ = std::fs::create_dir_all(dir);
                }
                Some(std::fs::OpenOptions::new().create(true).append(true).open(path).ok()?)
            }
            None => None,
        };
        let gpu = (Ask::from_env() != Ask::Cpu).then(|| Gpu::headless(width, height)).and_then(|g| g.map_err(|e| eprintln!("phone-paint: gpu: {e}; timing the CPU path")).ok());
        Some(Timing {
            out,
            st: paint::State::default(),
            layers: if gpu.is_some() { crate::gpu::layers(Ask::from_env(), width, height, fonts) } else { Layers::new(width, height, fonts) },
            gpu,
            size: (width, height),
            scale,
            fonts,
            clock: Instant::now(),
            last_tn: 0,
            last_s: scale,
            shown: false,
            period: pace::period(pace::DEFAULT_HZ),
        })
    }

    /// The GL renderer timing the GPU path; None on the CPU's.
    pub fn gl(&self) -> Option<String> {
        self.gpu.as_ref().map(Gpu::renderer)
    }

    /// The panel's rate: motion steps a period of it at a time.
    pub fn set_rate(&mut self, hz: u32) {
        self.period = pace::period(hz);
    }

    /// Draws f, a second after the frame before (its motion has settled),
    /// and each step of the motion it starts.
    pub fn frame(&mut self, f: &Frame) -> Steps {
        let (w, h) = self.size;
        let s = if f.zoom > 0.0 { self.scale * f.zoom } else { self.scale };
        let fresh = f.transition.as_ref().filter(|t| t.n != self.last_tn).and_then(|t| motion::Kind::parse(&t.kind));
        if let Some(t) = &f.transition {
            self.last_tn = t.n;
        }
        let still = f.motion == "off";
        let mut kind = fresh.filter(|_| f.motion.is_empty());
        let before = paint::overlay_panel(&self.st, self.last_s);
        self.clock += Duration::from_secs(1);
        let (mut draws, mut steps) = (Vec::new(), Vec::new());
        loop {
            self.st.now = if still { None } else { Some(self.clock) };
            let old = kind.filter(|_| self.shown).map(|_| match &self.gpu {
                Some(g) => (g.front(), None),
                None => (0, Some(self.layers.screen().clone())),
            });
            let t0 = Instant::now();
            let mut cv = Canvas::recording(w, h, s, self.fonts);
            cv.mask_secure = false;
            paint::draw(&mut cv, f, &mut self.st);
            self.layers.update(cv.take(), s);
            if let Some(g) = self.gpu.as_mut() {
                if let Err(e) = g.compose(&mut self.layers) {
                    eprintln!("phone-paint: gpu: {e}; timing the CPU path");
                    (self.gpu, self.layers, self.shown) = (None, Layers::new(w, h, self.fonts), false);
                    return Steps { draw: draws, transition: steps };
                }
                g.finish();
            }
            draws.push(ms(t0));
            if let (Some(k), Some((front, old))) = (kind.take(), old) {
                if let Some(plan) = motion::Plan::new(k, &self.st, before, s, h as usize) {
                    let dur = k.duration().as_secs_f32();
                    let cpu = old.as_ref().map(|o| plan.cpu(o, self.layers.screen()));
                    let mut out = Pixmap::new(w, h);
                    let mut at = 0.0;
                    while at < dur {
                        let t0 = Instant::now();
                        match (&cpu, self.gpu.as_mut(), out.as_mut()) {
                            (Some(step), _, Some(out)) => step(out.data_mut(), k.ease(at / dur)),
                            (None, Some(g), _) => {
                                g.transition(&plan, k.ease(at / dur), front, g.front());
                                g.finish();
                            }
                            _ => break,
                        }
                        steps.push(ms(t0));
                        at += self.period.as_secs_f32();
                    }
                }
            }
            (self.shown, self.last_s) = (true, s);
            if still || !paint::animating(&self.st) {
                break;
            }
            self.clock += self.period;
        }
        if let Some(out) = self.out.as_mut() {
            let _ = writeln!(out, "{}", serde_json::json!({"draw": draws, "transition": steps}));
        }
        Steps { draw: draws, transition: steps }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // A page pushed in with a list that scrolls: a line with each step
    // drawn and each step of the push; the same on the GPU path, composing
    // (L1) and painting (L2).
    #[test]
    fn each_frame_writes_its_steps() {
        let Ok(fonts) = Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static Fonts = Box::leak(Box::new(fonts));
        let path = std::env::temp_dir().join(format!("phone-paint-steps-{}.jsonl", std::process::id()));
        for (gpu, raster) in [(false, false), (true, false), (true, true)] {
            let _ = std::fs::remove_file(&path);
            let mut t = Timing::open(Some(path.as_os_str()), 480, 640, 2.0, fonts).unwrap();
            (t.gpu, t.layers) = (None, Layers::new(480, 640, fonts));
            if gpu {
                match Gpu::headless(480, 640) {
                    Ok(g) => (t.gpu, t.layers) = (Some(g), if raster { Layers::raster(480, 640, fonts) } else { Layers::split(480, 640, fonts) }),
                    Err(e) => {
                        eprintln!("no GPU ({e}): skipped");
                        break;
                    }
                }
            }
            let page = |focus: i32, n: u64| {
                let body: Vec<serde_json::Value> = (0..20).map(|i| serde_json::json!({"kind": "item", "title": format!("Item {i}"), "focused": i == focus})).collect();
                let f = serde_json::json!({"screen": "app/list", "title": "List", "focus": focus, "body": body, "transition": {"kind": "push", "n": n}});
                serde_json::from_value::<Frame>(f).unwrap()
            };
            t.frame(&page(0, 1));
            let pushed = t.frame(&serde_json::from_value(serde_json::json!({"screen": "app/other", "title": "Other", "transition": {"kind": "push", "n": 2}})).unwrap());
            t.frame(&page(19, 3));
            assert_eq!(pushed.transition.len(), 11, "the steps returned are the ones written");
            let menu = |sel: i32| {
                let tiles: Vec<serde_json::Value> = (0..6).map(|i| serde_json::json!({"title": format!("App {i}"), "icon": "A", "focused": i == sel})).collect();
                serde_json::from_value::<Frame>(serde_json::json!({"screen": "system/sys.menu", "title": "Menu", "focus": 0, "body": [{"kind": "grid", "columns": 3, "sel": sel, "tiles": tiles}]})).unwrap()
            };
            t.frame(&menu(0));
            t.frame(&menu(1));
            assert_eq!(t.gpu.is_some(), gpu, "the GPU path gave up");
            let lines: Vec<serde_json::Value> = std::fs::read_to_string(&path).unwrap().lines().map(|l| serde_json::from_str(l).unwrap()).collect();
            let _ = std::fs::remove_file(&path);
            assert_eq!(lines.len(), 5);
            let n = |l: &serde_json::Value, k: &str| l[k].as_array().map_or(0, |a| a.len());
            assert_eq!((n(&lines[0], "draw"), n(&lines[0], "transition")), (1, 0), "the first frame: nothing on a panel to push from");
            assert_eq!(n(&lines[1], "transition"), 11, "a push of 180 ms at 60 Hz");
            assert!(n(&lines[2], "draw") >= 8, "the last frame scrolls to its last row: {}", lines[2]);
            assert!(n(&lines[4], "draw") >= 6, "the menu's highlight glides 100 ms: {}", lines[4]);
        }
    }

    // At 120 Hz the same motion takes twice the steps: one a period of the
    // panel's, not of 60 Hz; on the GPU path too, composing and painting.
    #[test]
    fn steps_follow_the_rate() {
        let Ok(fonts) = Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static Fonts = Box::leak(Box::new(fonts));
        let page = |screen: &str, n: u64| serde_json::from_value::<Frame>(serde_json::json!({"screen": screen, "title": screen, "transition": {"kind": "push", "n": n}})).unwrap();
        for (gpu, raster) in [(false, false), (true, false), (true, true)] {
            let mut t = Timing::open(None, 480, 640, 2.0, fonts).unwrap();
            (t.gpu, t.layers) = (None, Layers::new(480, 640, fonts));
            if gpu {
                match Gpu::headless(480, 640) {
                    Ok(g) => (t.gpu, t.layers) = (Some(g), if raster { Layers::raster(480, 640, fonts) } else { Layers::split(480, 640, fonts) }),
                    Err(e) => {
                        eprintln!("no GPU ({e}): skipped");
                        break;
                    }
                }
            }
            t.frame(&page("a", 1));
            let at60 = t.frame(&page("b", 2)).transition.len();
            t.set_rate(120);
            let at120 = t.frame(&page("c", 3)).transition.len();
            assert_eq!((at60, at120), (11, 22), "a push of 180 ms at 60 Hz, then at 120 Hz (gpu: {gpu}, raster: {raster})");
            assert_eq!(t.gpu.is_some(), gpu, "the GPU path gave up");
        }
    }
}
