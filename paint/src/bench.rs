//! `phone-paint --bench <frames> [--size WxH@S]`: what the live display
//! costs on a recorded sequence of frames (PHONE_PAINT_DUMP writes one, a
//! frame a line), each frame followed by the animation frames it starts,
//! a period of 60 Hz apart (as the pixel selftest's timing without a
//! "rate"). Drawn three ways: as it used to be (the whole screen every
//! time, nothing kept), the whole screen with the decoded pictures, icons
//! and glyph outlines kept, and as it is now (recorded, only the damage
//! painted). Every step must end on the same picture all three ways. Each
//! way runs three times and a step counts its fastest run; the panel is
//! left out.
//!
//! Then the GPU path (gpu.rs), where EGL works: the same steps with the
//! layers split, uploaded and composed, timed until the GPU has finished;
//! each step's screen must be exactly what the CPU sums from the split
//! layers and within gpu::LEVELS of the CPU path's. The transitions the
//! frames start are timed both ways (rows copied on the CPU, composed by
//! the GPU). Here the GPU is whatever Mesa gives without one (llvmpipe, a
//! rasterizer on the CPU's cores): its times say nothing about a phone's.

use std::hash::{Hash, Hasher};
use std::io::BufRead;
use std::sync::atomic::Ordering;
use std::time::{Duration, Instant};

use crate::canvas::{self, Canvas};
use crate::gpu::{self, Gpu};
use crate::layers::Layers;
use crate::motion::{self, Plan};
use crate::{frame, pace, paint};

const ROUNDS: usize = 3;

#[derive(Clone, Copy, PartialEq)]
enum Way {
    Old,
    Cached,
    Damage,
}

/// One step: how long it took, what it painted (pixels: on the screen,
/// and into the body's bands), its picture.
struct Step {
    ms: f32,
    painted: usize,
    band: usize,
    hash: u64,
}

fn hash(pm: &tiny_skia::Pixmap) -> u64 {
    let mut h = std::collections::hash_map::DefaultHasher::new();
    pm.data().hash(&mut h);
    h.finish()
}

fn size(spec: &str) -> Option<(u32, u32, f32)> {
    let (wh, s) = spec.split_once('@').unwrap_or((spec, "2"));
    let (w, h) = wh.split_once('x')?;
    Some((w.parse().ok()?, h.parse().ok()?, s.parse().ok()?))
}

/// Draws every frame and its animation one way.
fn pass(frames: &[frame::Frame], (w, h, scale): (u32, u32, f32), fonts: &'static canvas::Fonts, way: Way) -> Vec<Step> {
    canvas::CACHING.store(way != Way::Old, Ordering::Relaxed);
    let mut st = paint::State::default();
    let mut layers = Layers::new(w, h, fonts);
    let base = Instant::now();
    let mut out = Vec::new();
    for (i, f) in frames.iter().enumerate() {
        let s = if f.zoom > 0.0 { scale * f.zoom } else { scale };
        let mut now = base + Duration::from_secs(i as u64);
        loop {
            st.now = if f.motion == "off" { None } else { Some(now) };
            let t0 = Instant::now();
            out.push(if way == Way::Damage {
                let mut cv = Canvas::recording(w, h, s, fonts);
                cv.mask_secure = false;
                paint::draw(&mut cv, f, &mut st);
                let painted = layers.update(cv.take(), s).area();
                let ms = t0.elapsed().as_secs_f32() * 1000.0;
                Step { ms, painted, band: layers.band_painted, hash: hash(layers.screen()) }
            } else {
                let mut cv = Canvas::new(w, h, s, fonts);
                cv.mask_secure = false;
                paint::draw(&mut cv, f, &mut st);
                let last = cv.pm.clone();
                let ms = t0.elapsed().as_secs_f32() * 1000.0;
                Step { ms, painted: (w * h) as usize, band: 0, hash: hash(&last) }
            });
            if st.now.is_none() || !paint::animating(&st) {
                break;
            }
            now += pace::period(pace::DEFAULT_HZ);
        }
    }
    canvas::CACHING.store(true, Ordering::Relaxed);
    out
}

/// The fastest of ROUNDS runs of each step.
fn best(frames: &[frame::Frame], dims: (u32, u32, f32), fonts: &'static canvas::Fonts, way: Way) -> Vec<Step> {
    let mut steps = pass(frames, dims, fonts, way);
    for _ in 1..ROUNDS {
        for (a, b) in steps.iter_mut().zip(pass(frames, dims, fonts, way)) {
            a.ms = a.ms.min(b.ms);
        }
    }
    steps
}

/// One step on the GPU path: how long (recording, painting the split
/// layers, uploading, composing, until the GPU finished), the pixels
/// uploaded; against the CPU path's picture of the same step, how many
/// pixels differ and by how many levels at most; whether it is exactly
/// Layers::composed.
struct GpuStep {
    ms: f32,
    uploaded: usize,
    differ: usize,
    levels: u8,
    exact: bool,
}

/// The steps on the GPU path, and each transition step on the CPU (rows
/// copied) and on the GPU, from the same frames.
struct GpuRun {
    steps: Vec<GpuStep>,
    cpu_moves: Vec<f32>,
    gpu_moves: Vec<f32>,
}

/// The GPU path over the frames, the CPU path drawing each step beside it
/// (not timed) to compare with.
fn gpu_pass(frames: &[frame::Frame], (w, h, scale): (u32, u32, f32), fonts: &'static canvas::Fonts, g: &mut Gpu) -> Result<GpuRun, String> {
    let (mut st, mut sc) = (paint::State::default(), paint::State::default());
    let (mut split, mut soft) = (Layers::split(w, h, fonts), Layers::new(w, h, fonts));
    let base = Instant::now();
    let (mut out, mut cpu_moves, mut gpu_moves) = (Vec::new(), Vec::new(), Vec::new());
    let (mut last_tn, mut last_s, mut shown) = (0u64, scale, false);
    let ms = |t0: Instant| t0.elapsed().as_secs_f32() * 1000.0;
    for (i, f) in frames.iter().enumerate() {
        let s = if f.zoom > 0.0 { scale * f.zoom } else { scale };
        let fresh = f.transition.as_ref().filter(|t| t.n != last_tn).and_then(|t| motion::Kind::parse(&t.kind));
        if let Some(t) = &f.transition {
            last_tn = t.n;
        }
        let mut kind = fresh.filter(|_| f.motion.is_empty());
        let before = paint::overlay_panel(&st, last_s);
        let mut now = base + Duration::from_secs(i as u64);
        loop {
            st.now = if f.motion == "off" { None } else { Some(now) };
            sc.now = st.now;
            let old = kind.filter(|_| shown).map(|_| (g.front(), soft.screen().clone()));
            let t0 = Instant::now();
            let mut cv = Canvas::recording(w, h, s, fonts);
            cv.mask_secure = false;
            paint::draw(&mut cv, f, &mut st);
            split.update(cv.take(), s);
            g.compose(&mut split)?;
            g.finish();
            let took = ms(t0);
            let mut cv = Canvas::recording(w, h, s, fonts);
            cv.mask_secure = false;
            paint::draw(&mut cv, f, &mut sc);
            soft.update(cv.take(), s);
            let got = g.read(gpu::Pic::Screen(g.front()));
            let (differ, levels) = got.chunks_exact(4).zip(soft.screen().data().chunks_exact(4)).fold((0, 0u8), |(n, m), (a, b)| {
                let d = a.iter().zip(b).map(|(x, y)| x.abs_diff(*y)).max().unwrap_or(0);
                (n + (d > 0) as usize, m.max(d))
            });
            let exact = split.composed().is_some_and(|c| c.data() == got.as_slice());
            out.push(GpuStep { ms: took, uploaded: g.uploaded, differ, levels, exact });
            if let (Some(k), Some((front, old))) = (kind.take(), old) {
                if let Some(plan) = Plan::new(k, &st, before, s, h as usize) {
                    let dur = k.duration().as_secs_f32() * 1000.0;
                    let step = plan.cpu(&old, soft.screen());
                    let mut pm = tiny_skia::Pixmap::new(w, h).ok_or("no picture")?;
                    let mut at = 0.0;
                    while at < dur {
                        let t0 = Instant::now();
                        step(pm.data_mut(), k.ease(at / dur));
                        cpu_moves.push(ms(t0));
                        let t0 = Instant::now();
                        g.transition(&plan, k.ease(at / dur), front, g.front());
                        g.finish();
                        gpu_moves.push(ms(t0));
                        at += pace::period(pace::DEFAULT_HZ).as_secs_f32() * 1000.0;
                    }
                }
            }
            (shown, last_s) = (true, s);
            if st.now.is_none() || !paint::animating(&st) {
                break;
            }
            now += pace::period(pace::DEFAULT_HZ);
        }
    }
    Ok(GpuRun { steps: out, cpu_moves, gpu_moves })
}

fn row(name: &str, mut v: Vec<f32>) {
    v.sort_by(|a, b| a.total_cmp(b));
    let q = |p: f32| v.get(((v.len() as f32 - 1.0) * p).round() as usize).copied().unwrap_or(0.0);
    let mean = v.iter().sum::<f32>() / v.len().max(1) as f32;
    println!("{name:<40}{:>5} {:7.2} {:7.2} {:7.2} {:7.2} {:7.2}", v.len(), q(0.5), q(0.9), q(1.0), mean, v.iter().sum::<f32>());
}

pub fn run(path: &str, spec: &str) -> Result<(), String> {
    let dims = size(spec).ok_or_else(|| format!("--size {spec}: want WxH@scale"))?;
    let file = std::fs::File::open(path).map_err(|e| format!("{path}: {e}"))?;
    let mut frames = Vec::new();
    for line in std::io::BufReader::new(file).lines() {
        let line = line.map_err(|e| e.to_string())?;
        if let Ok(f) = serde_json::from_str::<frame::Frame>(&line) {
            frames.push(f);
        }
    }
    if frames.is_empty() {
        return Err(format!("{path}: no frames"));
    }
    let fonts: &'static canvas::Fonts = Box::leak(Box::new(canvas::Fonts::load()?));
    let old = best(&frames, dims, fonts, Way::Old);
    let cached = best(&frames, dims, fonts, Way::Cached);
    let new = best(&frames, dims, fonts, Way::Damage);
    if old.len() != new.len() || cached.len() != new.len() {
        return Err(format!("the ways took {}, {} and {} steps", old.len(), cached.len(), new.len()));
    }
    let differ = old.iter().zip(&cached).zip(&new).filter(|((a, b), c)| a.hash != c.hash || b.hash != c.hash).count();
    if differ > 0 {
        return Err(format!("{differ} of {} steps end on another picture than the old full draw", new.len()));
    }
    let screen = (dims.0 * dims.1) as usize;
    let ms = |v: &[Step], keep: &dyn Fn(usize) -> bool| v.iter().zip(&new).filter(|(_, n)| keep(n.painted)).map(|(s, _)| s.ms).collect::<Vec<f32>>();
    let all = |_: usize| true;
    println!("phone-paint bench: {} frames, {} steps with their animation, {}x{}@{}", frames.len(), new.len(), dims.0, dims.1, dims.2);
    println!("{:<40}{:>5} {:>7} {:>7} {:>7} {:>7} {:>7}", "ms a step", "steps", "p50", "p90", "max", "mean", "total");
    row("old: whole screen, nothing kept", ms(&old, &all));
    row("whole screen, pictures and glyphs kept", ms(&cached, &all));
    row("now: recorded, damage painted", ms(&new, &all));
    for (name, lo, hi) in [("whole screen", screen, usize::MAX), ("part of the screen", 1, screen - 1), ("nothing changed", 0, 0)] {
        let keep = |p: usize| p >= lo && p <= hi;
        row(&format!("  {name}: old"), ms(&old, &keep));
        row(&format!("  {name}: now"), ms(&new, &keep));
    }
    let shares: Vec<f32> = new.iter().map(|s| 100.0 * s.painted as f32 / screen as f32).collect();
    row("now: % of the screen painted", shares);
    let bands: Vec<f32> = new.iter().map(|s| 100.0 * s.band as f32 / screen as f32).collect();
    row("now: % of a screen painted in the body", bands);
    println!("every step ends on the same picture all three ways");
    let mut g = match Gpu::headless(dims.0, dims.1) {
        Ok(g) => g,
        Err(e) => {
            println!("gpu: not measured ({e})");
            return Ok(());
        }
    };
    let GpuRun { mut steps, mut cpu_moves, mut gpu_moves } = gpu_pass(&frames, dims, fonts, &mut g)?;
    for _ in 1..ROUNDS {
        let run = gpu_pass(&frames, dims, fonts, &mut g)?;
        for (a, b) in steps.iter_mut().zip(run.steps) {
            a.ms = a.ms.min(b.ms);
        }
        for (a, b) in cpu_moves.iter_mut().zip(run.cpu_moves).chain(gpu_moves.iter_mut().zip(run.gpu_moves)) {
            *a = a.min(b);
        }
    }
    if steps.len() != new.len() {
        return Err(format!("the GPU path took {} steps, the CPU path {}", steps.len(), new.len()));
    }
    println!("gpu: {} (LP_NUM_THREADS {})", g.renderer(), std::env::var("LP_NUM_THREADS").unwrap_or_else(|_| "unset".into()));
    let gms = |keep: &dyn Fn(usize) -> bool| steps.iter().zip(&new).filter(|(_, n)| keep(n.painted)).map(|(s, _)| s.ms).collect::<Vec<f32>>();
    row("gpu: damage painted, uploaded, composed", gms(&all));
    for (name, lo, hi) in [("whole screen", screen, usize::MAX), ("part of the screen", 1, screen - 1), ("nothing changed", 0, 0)] {
        row(&format!("  {name}: gpu"), gms(&|p: usize| p >= lo && p <= hi));
    }
    row("gpu: % of a screen uploaded", steps.iter().map(|s| 100.0 * s.uploaded as f32 / screen as f32).collect());
    row("transition steps: cpu, rows copied", cpu_moves);
    row("transition steps: gpu", gpu_moves);
    let inexact = steps.iter().filter(|s| !s.exact).count();
    if inexact > 0 {
        return Err(format!("{inexact} of {} GPU steps are not what the CPU sums from the split layers", steps.len()));
    }
    let levels = steps.iter().map(|s| s.levels).max().unwrap_or(0);
    if levels > gpu::LEVELS {
        return Err(format!("a GPU step is {levels} levels from the CPU path's (at most {})", gpu::LEVELS));
    }
    let same = steps.iter().filter(|s| s.differ == 0).count();
    let most = steps.iter().map(|s| s.differ).max().unwrap_or(0);
    println!(
        "gpu: every step exactly the split layers' sum; against the CPU path {same} of {} steps identical, the others off by at most {levels} levels in at most {:.1}% of the screen",
        steps.len(),
        100.0 * most as f32 / screen as f32
    );
    Ok(())
}
