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
use crate::layers::{Layers, Onto};
use crate::motion::{self, Plan};
use crate::raster::{compare, Counts, Diff, Kind};
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

/// L2 over the frames: each step's time (recording, the jobs, painting
/// them on the GPU, composing, until the GPU finished) and, checked,
/// against the CPU's split layers drawn beside it (not timed): how the
/// layers differ, by kind, and how far the screen is from the CPU path's;
/// the jobs painted again on the CPU an op at a time, timed by kind (what
/// L2 takes off the CPU); the ops and pixels by kind on the GPU and on the
/// CPU.
struct RasterRun {
    ms: Vec<f32>,
    diff: Diff,
    screen_levels: u8,
    same_screens: usize,
    cpu_ms: [f64; crate::raster::KINDS],
    counts: Counts,
}

fn raster_pass(frames: &[frame::Frame], (w, h, scale): (u32, u32, f32), fonts: &'static canvas::Fonts, g: &mut Gpu, check: bool) -> Result<RasterRun, String> {
    let (mut st, mut sc, mut ss) = (paint::State::default(), paint::State::default(), paint::State::default());
    let (mut ras, mut cpu, mut soft) = (Layers::raster(w, h, fonts), Layers::split(w, h, fonts), Layers::new(w, h, fonts));
    let mut run = RasterRun { ms: Vec::new(), diff: Diff::default(), screen_levels: 0, same_screens: 0, cpu_ms: [0.0; crate::raster::KINDS], counts: Counts::default() };
    let mut scratch = tiny_skia::Pixmap::new(w, h).ok_or("no picture")?;
    let base = Instant::now();
    let ms = |t0: Instant| t0.elapsed().as_secs_f32() * 1000.0;
    if let Some(r) = g.raster() {
        r.counts = Counts::default();
    }
    for (i, f) in frames.iter().enumerate() {
        let s = if f.zoom > 0.0 { scale * f.zoom } else { scale };
        let mut now = base + Duration::from_secs(i as u64);
        loop {
            st.now = if f.motion == "off" { None } else { Some(now) };
            (sc.now, ss.now) = (st.now, st.now);
            let t0 = Instant::now();
            let mut cv = Canvas::recording(w, h, s, fonts);
            cv.mask_secure = false;
            paint::draw(&mut cv, f, &mut st);
            ras.update(cv.take(), s);
            let mut took = ms(t0);
            let jobs = check.then(|| ras.jobs().to_vec());
            let t0 = Instant::now();
            g.compose(&mut ras)?;
            g.finish();
            took += ms(t0);
            run.ms.push(took);
            if let Some(jobs) = jobs {
                for job in &jobs {
                    for rec in &job.ops {
                        let t0 = Instant::now();
                        rec.op.paint(&mut scratch, job.s, fonts, Some(job.rect), job.oy);
                        run.cpu_ms[Kind::of(&rec.op) as usize] += t0.elapsed().as_secs_f64() * 1000.0;
                    }
                }
                for (l, st) in [(&mut cpu, &mut sc), (&mut soft, &mut ss)] {
                    let mut cv = Canvas::recording(w, h, s, fonts);
                    cv.mask_secure = false;
                    paint::draw(&mut cv, f, st);
                    l.update(cv.take(), s);
                }
                let inexact = g.raster().map(|r| r.inexact.clone()).unwrap_or_default();
                let ops = |list: &[canvas::Rec], top: bool, oy: i32| -> Vec<(canvas::IRect, Kind)> {
                    list.iter().filter(|x| x.top == top).map(|x| (canvas::IRect::new(x.bbox.x0, x.bbox.y0 - oy, x.bbox.x1, x.bbox.y1 - oy), Kind::of(&x.op))).collect()
                };
                let (list, body) = cpu.recs();
                let mut layers = vec![(Onto::Page, cpu.page(), ops(list, false, 0)), (Onto::Top, cpu.top().ok_or("not split")?, ops(list, true, 0))];
                for (k, pm) in cpu.bands() {
                    layers.push((Onto::Band(*k), pm, ops(body, false, k * cpu.body().map_or(0, |b| b.2))));
                }
                for (onto, pm, ops) in layers {
                    let on_cpu: Vec<canvas::IRect> = inexact.iter().filter(|x| x.0 == onto).map(|x| x.1).collect();
                    let got = g.read_layer(onto).ok_or("a layer the GPU has not")?;
                    run.diff.add(&compare(&got, pm.data(), w, &ops, &on_cpu));
                }
                let got = g.read(gpu::Pic::Screen(g.front()));
                let levels = got.iter().zip(soft.screen().data()).map(|(a, b)| a.abs_diff(*b)).max().unwrap_or(0);
                run.screen_levels = run.screen_levels.max(levels);
                run.same_screens += (levels == 0) as usize;
            }
            if st.now.is_none() || !paint::animating(&st) {
                break;
            }
            now += pace::period(pace::DEFAULT_HZ);
        }
    }
    if let Some(r) = g.raster() {
        run.counts = std::mem::take(&mut r.counts);
    }
    Ok(run)
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
    let mut l2 = raster_pass(&frames, dims, fonts, &mut g, true)?;
    for _ in 1..ROUNDS {
        for (a, b) in l2.ms.iter_mut().zip(raster_pass(&frames, dims, fonts, &mut g, false)?.ms) {
            *a = a.min(b);
        }
    }
    if l2.ms.len() != new.len() {
        return Err(format!("L2 took {} steps, the CPU path {}", l2.ms.len(), new.len()));
    }
    let (fetch, glyphs) = g.raster().map_or((false, 0), |r| (r.fetches(), r.rasterized()));
    println!("l2: the GPU paints the display list, the pixel under read by {}; {glyphs} glyphs rasterized into the atlas in {ROUNDS} runs", if fetch { "framebuffer fetch" } else { "copies" });
    let lms = |keep: &dyn Fn(usize) -> bool| l2.ms.iter().zip(&new).filter(|(_, n)| keep(n.painted)).map(|(s, _)| *s).collect::<Vec<f32>>();
    row("l2: recorded, painted by the GPU, composed", lms(&all));
    for (name, lo, hi) in [("whole screen", screen, usize::MAX), ("part of the screen", 1, screen - 1), ("nothing changed", 0, 0)] {
        row(&format!("  {name}: l2"), lms(&|p: usize| p >= lo && p <= hi));
    }
    let c = &l2.counts;
    let tot = |k: usize| (c.gpu[k].0 + c.cpu[k].0, c.gpu[k].1 + c.cpu[k].1);
    let pct = |a: f64, b: f64| if b > 0.0 { 100.0 * a / b } else { 0.0 };
    let total_ms: f64 = l2.cpu_ms.iter().sum();
    println!("{:<10}{:>7}{:>8}{:>10}{:>9}{:>9}{:>6}  (the ops L2 painted, their pixels, the share on the GPU; the CPU path's ms painting them, once)", "op kind", "ops", "% gpu", "Mpixels", "% gpu", "cpu ms", "%");
    for k in Kind::ALL {
        let (i, (n, p)) = (k as usize, tot(k as usize));
        if n > 0 {
            println!("{:<10}{:>7}{:>8.1}{:>10.2}{:>9.1}{:>9.1}{:>6.1}", k.name(), n, pct(c.gpu[i].0 as f64, n as f64), p as f64 / 1e6, pct(c.gpu[i].1 as f64, p as f64), l2.cpu_ms[i], pct(l2.cpu_ms[i], total_ms));
        }
    }
    let (n, p) = (0..10).map(tot).fold((0, 0), |a, b| (a.0 + b.0, a.1 + b.1));
    let (gn, gp): (usize, usize) = (c.gpu.iter().map(|x| x.0).sum(), c.gpu.iter().map(|x| x.1).sum());
    println!("{:<10}{:>7}{:>8.1}{:>10.2}{:>9.1}{:>9.1}", "all", n, pct(gn as f64, n as f64), p as f64 / 1e6, pct(gp as f64, p as f64), total_ms);
    if l2.diff.over > 0 {
        return Err(format!("{} pixels L2 painted are further from the CPU's than allowed, the first {:?}", l2.diff.over, l2.diff.first_over));
    }
    let off: Vec<String> = Kind::ALL.iter().filter(|k| l2.diff.kinds[**k as usize].0 > 0).map(|k| format!("{} {} pixels by {}", k.name(), l2.diff.kinds[*k as usize].0, l2.diff.kinds[*k as usize].1)).collect();
    println!(
        "l2: every layer the CPU's but where ops went to the CPU ({}); the screen against the CPU path's: {} of {} steps identical, at most {} levels off",
        if off.is_empty() { "none".to_string() } else { off.join(", ") },
        l2.same_screens,
        l2.ms.len(),
        l2.screen_levels
    );
    Ok(())
}
