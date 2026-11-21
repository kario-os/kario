//! Transitions between frames (37-motion.md): the new frame is drawn once,
//! and each step only copies rows of the old and new pictures at an offset,
//! so a step costs about a copy of the screen whatever is on it. Pictures
//! are RGBA rows of the same size, opaque.

use std::time::{Duration, Instant};

use tiny_skia::Pixmap;

use crate::paint;

/// Banners and the toast come in in 150 ms and leave in 100 ms (§3).
pub const ENTER_MS: f32 = 150.0;
pub const LEAVE_MS: f32 = 100.0;

/// The text cursor shows for CARET_MS and hides for as long; a spinner
/// turns once in SPIN_MS, a dot of its SPIN_DOTS at a time (§3).
pub const CARET_MS: u64 = 500;
pub const SPIN_MS: u64 = 1000;
pub const SPIN_DOTS: u32 = 8;

/// While charging, the battery fills from its level to full in
/// CHARGE_STEPS steps of CHARGE_MS each, then again (§3).
pub const CHARGE_MS: u64 = 500;
pub const CHARGE_STEPS: u32 = 4;

/// An overlay's phase (the scan line, the ringing pulse) steps on at most
/// this often: a frame of 60 Hz (§3).
pub const PHASE_MS: u64 = 16;

/// Something sliding in and out (the banner, the toast), timed here, not
/// by the shell: 0 out of sight, 1 in place. Coming in eases out, leaving
/// eases in; turning round mid-way starts from where it is and takes the
/// share of the time the way back is.
#[derive(Default, Clone, Copy, Debug)]
pub struct Slide {
    from: f32,
    to: f32,
    start: Option<Instant>,
    ms: f32,
}

impl Slide {
    fn t(&self, now: Option<Instant>) -> f32 {
        match (self.start, now) {
            (Some(s), Some(n)) if self.ms > 0.0 => (n.saturating_duration_since(s).as_secs_f32() * 1000.0 / self.ms).clamp(0.0, 1.0),
            _ => 1.0,
        }
    }

    /// Where it is at now; without now (no live display) where it goes.
    pub fn value(&self, now: Option<Instant>) -> f32 {
        let t = self.t(now);
        let e = if self.to > self.from { 1.0 - (1.0 - t).powi(3) } else { t * t * t };
        self.from + (self.to - self.from) * e
    }

    /// Comes in (on) or leaves; without now, at once.
    pub fn show(&mut self, on: bool, now: Option<Instant>) {
        let to = if on { 1.0 } else { 0.0 };
        if to == self.to {
            return;
        }
        let from = if now.is_some() { self.value(now) } else { to };
        let ms = if on { ENTER_MS } else { LEAVE_MS } * (to - from).abs();
        *self = Slide { from, to, start: now, ms };
    }

    pub fn running(&self, now: Option<Instant>) -> bool {
        now.is_some() && self.start.is_some() && self.t(now) < 1.0
    }
}

/// What the shell says happened between the last frame and this one.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Kind {
    /// A page went on top (an app opened, the next level): it slides in from the right.
    Push,
    /// Back: the page on top slides out to the right over the one under it.
    Pop,
    /// A menu or dialog came up: its panel rises over the dimmed screen.
    Rise,
    /// It went away: its panel drops out of sight.
    Sink,
}

impl Kind {
    pub fn parse(s: &str) -> Option<Kind> {
        match s {
            "push" => Some(Kind::Push),
            "pop" => Some(Kind::Pop),
            "rise" => Some(Kind::Rise),
            "sink" => Some(Kind::Sink),
            _ => None,
        }
    }

    /// Short, as keys come often: entering takes longer than leaving.
    pub fn duration(self) -> Duration {
        Duration::from_millis(match self {
            Kind::Push => 180,
            Kind::Pop | Kind::Rise => 150,
            Kind::Sink => 100,
        })
    }

    /// Eased progress: what enters or moves eases out, what only leaves eases in.
    pub fn ease(self, t: f32) -> f32 {
        let t = t.clamp(0.0, 1.0);
        match self {
            Kind::Sink => t * t * t,
            _ => 1.0 - (1.0 - t).powi(3),
        }
    }
}

/// The old page's share of the way it moves when the new one slides over it.
const UNDER_SHIFT: f32 = 1.0 / 3.0;

/// The page under a sliding one, a little darker (computed once).
pub fn dimmed(src: &[u8]) -> Vec<u8> {
    let mut out = src.to_vec();
    for px in out.chunks_exact_mut(4) {
        for c in &mut px[..3] {
            *c = ((*c as u32 * 184) >> 8) as u8;
        }
    }
    out
}

/// What a scrim of alpha a keeps of the colour under it, in 256ths.
pub fn scrim_k(a: f32) -> u32 {
    ((1.0 - a.clamp(0.0, 1.0)) * 256.0) as u32
}

/// A scrim keeping k 256ths of the colour (scrim_k) over rows top.. (as
/// the renderer dims under a dialog).
pub fn scrim(src: &[u8], width: usize, top: usize, k: u32) -> Vec<u8> {
    let mut out = src.to_vec();
    for px in out[(top * width * 4).min(src.len())..].chunks_exact_mut(4) {
        for c in &mut px[..3] {
            *c = ((*c as u32 * k) >> 8) as u8;
        }
    }
    out
}

/// One row: `under` at offset ux, `over` on top at offset ox (pixels, may
/// be negative or past the edge); what neither covers keeps `fill`'s pixels.
fn row(out: &mut [u8], under: &[u8], ux: i32, over: &[u8], ox: i32, fill: &[u8]) {
    let w = (out.len() / 4) as i32;
    out.copy_from_slice(fill);
    for (src, off) in [(under, ux), (over, ox)] {
        let (x0, x1) = (off.max(0), (off + w).min(w));
        if x0 < x1 {
            let (s0, s1) = ((x0 - off) as usize * 4, (x1 - off) as usize * 4);
            out[x0 as usize * 4..x1 as usize * 4].copy_from_slice(&src[s0..s1]);
        }
    }
}

/// Where the page under and the page over start at eased progress p of a
/// page transition `width` pixels wide (from the left edge, may be
/// negative or past it).
pub fn offsets(kind: Kind, p: f32, width: usize) -> (i32, i32) {
    let w = width as f32;
    match kind {
        Kind::Pop => ((-(1.0 - p) * w * UNDER_SHIFT).round() as i32, (p * w).round() as i32),
        _ => ((-p * w * UNDER_SHIFT).round() as i32, ((1.0 - p) * w).round() as i32),
    }
}

/// A step of a page transition at eased progress p: rows top..bottom move,
/// the others (the status bar, the soft keys) are the new frame's.
/// `old_dim` is the old frame dimmed (Push) or the new one dimmed (Pop).
#[allow(clippy::too_many_arguments)]
pub fn slide(out: &mut [u8], old: &[u8], new: &[u8], dim: &[u8], width: usize, top: usize, bottom: usize, kind: Kind, p: f32) {
    let stride = width * 4;
    let (ux, ox) = offsets(kind, p, width);
    for y in 0..out.len() / stride {
        let r = y * stride..(y + 1) * stride;
        if y < top || y >= bottom {
            out[r.clone()].copy_from_slice(&new[r]);
            continue;
        }
        match kind {
            Kind::Pop => {
                let under = if p < 1.0 { &dim[r.clone()] } else { &new[r.clone()] };
                row(&mut out[r.clone()], under, ux, &old[r.clone()], ox, &new[r]);
            }
            _ => row(&mut out[r.clone()], &dim[r.clone()], ux, &new[r.clone()], ox, &new[r]),
        }
    }
}

/// A panel in pixels: columns x0..x1, rows y0..y1.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Panel {
    pub x0: usize,
    pub y0: usize,
    pub x1: usize,
    pub y1: usize,
}

/// A step of a panel moving: `base` everywhere, and the panel's rows of
/// `src` lowered by dy, shown only above `clip` (the soft keys' top for a
/// menu, which rises from behind them; the screen's bottom for a dialog).
pub fn panel(out: &mut [u8], base: &[u8], src: &[u8], width: usize, pn: Panel, dy: usize, clip: usize) {
    let stride = width * 4;
    out.copy_from_slice(base);
    let (x0, x1) = (pn.x0.min(width) * 4, pn.x1.min(width) * 4);
    for y in pn.y0..pn.y1 {
        let to = y + dy;
        if to >= clip || to * stride >= out.len() {
            break;
        }
        out[to * stride + x0..to * stride + x1].copy_from_slice(&src[y * stride + x0..y * stride + x1]);
    }
}

/// How far a panel is lowered at eased progress p of a Rise (from the clip
/// edge, or a short way for a dialog in the middle) or of a Sink.
pub fn panel_drop(pn: Panel, clip: usize, menu: bool, kind: Kind, p: f32) -> usize {
    let way = if menu { clip.saturating_sub(pn.y0) } else { (pn.y1 - pn.y0).min(48) / 2 };
    let k = if kind == Kind::Sink { p } else { 1.0 - p };
    (way as f32 * k).round() as usize
}

/// The rows of a picture h rows high that one step of a transition to the
/// next can change: a page's between the bars; a panel's anywhere under
/// the status bar.
pub fn moving_rows(kind: Kind, st: &paint::State, s: f32, h: usize) -> (usize, usize) {
    let (top, bottom) = paint::page_rows(st, s);
    match kind {
        Kind::Push | Kind::Pop => (top.min(h), bottom.min(h)),
        Kind::Rise | Kind::Sink => (top.min(h), h),
    }
}

/// A transition worked out from the frames it goes between; each step
/// draws it at an eased progress from the two pictures, here by copying
/// rows, or composed by the GPU (gpu.rs) to the same pixels.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Plan {
    /// The page sliding in (Push) or out (Pop) in rows top..bottom.
    Slide { kind: Kind, top: usize, bottom: usize },
    /// A panel rising from below clip over the old picture, scrimmed from
    /// row top keeping k 256ths of its colour (Rise), or dropping out of
    /// sight over the new picture (Sink, k 256: no scrim).
    Panel { kind: Kind, pn: Panel, clip: usize, menu: bool, top: usize, k: u32 },
}

impl Plan {
    /// The plan of a transition to the frame drawn with st, `height` rows
    /// tall; before, the panel of the menu or dialog the old frame showed.
    /// None where the two frames cannot make one.
    pub fn new(kind: Kind, st: &paint::State, before: Option<(Panel, f32, bool)>, s: f32, height: usize) -> Option<Plan> {
        let (top, bottom) = paint::page_rows(st, s);
        Some(match kind {
            Kind::Push | Kind::Pop => Plan::Slide { kind, top, bottom },
            Kind::Rise => {
                let (pn, a, menu) = paint::overlay_panel(st, s)?;
                Plan::Panel { kind, pn, clip: if menu { bottom } else { height }, menu, top, k: scrim_k(a) }
            }
            Kind::Sink => {
                let (pn, _, menu) = before?;
                Plan::Panel { kind, pn, clip: if menu { bottom } else { height }, menu, top, k: 256 }
            }
        })
    }

    /// How far the panel is lowered at eased progress p.
    pub fn drop(&self, p: f32) -> usize {
        match *self {
            Plan::Panel { kind, pn, clip, menu, .. } => panel_drop(pn, clip, menu, kind, p),
            Plan::Slide { .. } => 0,
        }
    }

    /// The steps on the CPU, from the picture old to new (the same size):
    /// what does not change made once (the dimmed or scrimmed copy), then
    /// each step at eased progress p only rows copied into the picture
    /// given.
    pub fn cpu<'a>(self, old: &'a Pixmap, new: &'a Pixmap) -> Step<'a> {
        let w = new.width() as usize;
        match self {
            Plan::Slide { kind, top, bottom } => {
                let dim = dimmed(if kind == Kind::Push { old.data() } else { new.data() });
                Box::new(move |o: &mut [u8], p: f32| slide(o, old.data(), new.data(), &dim, w, top, bottom, kind, p))
            }
            Plan::Panel { kind: Kind::Rise, pn, clip, top, k, .. } => {
                let base = scrim(old.data(), w, top, k);
                Box::new(move |o: &mut [u8], p: f32| panel(o, &base, new.data(), w, pn, self.drop(p), clip))
            }
            Plan::Panel { pn, clip, .. } => Box::new(move |o: &mut [u8], p: f32| panel(o, new.data(), old.data(), w, pn, self.drop(p), clip)),
        }
    }
}

/// A transition's step on the CPU: the picture it writes, at eased progress p.
pub type Step<'a> = Box<dyn Fn(&mut [u8], f32) + 'a>;

/// A step of a transition from the picture old to new on the CPU
/// (Plan::cpu). st is the state new was drawn with; before, the panel of
/// the menu or dialog old showed. None where the two cannot make one.
pub fn steps<'a>(kind: Kind, old: &'a Pixmap, new: &'a Pixmap, st: &paint::State, before: Option<(Panel, f32, bool)>, s: f32) -> Option<Step<'a>> {
    if (old.width(), old.height()) != (new.width(), new.height()) {
        return None;
    }
    Some(Plan::new(kind, st, before, s, new.height() as usize)?.cpu(old, new))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pic(w: usize, h: usize, v: u8) -> Vec<u8> {
        let mut p = vec![0u8; w * h * 4];
        for (i, px) in p.chunks_exact_mut(4).enumerate() {
            px.copy_from_slice(&[v, (i % w) as u8, (i / w) as u8, 255]);
        }
        p
    }

    fn px(p: &[u8], w: usize, x: usize, y: usize) -> [u8; 4] {
        let i = (y * w + x) * 4;
        [p[i], p[i + 1], p[i + 2], p[i + 3]]
    }

    #[test]
    fn a_push_slides_the_new_page_in_over_the_old() {
        let (w, h) = (12, 6);
        let (old, new) = (pic(w, h, 10), pic(w, h, 200));
        let dim = dimmed(&old);
        let mut out = vec![0u8; w * h * 4];
        slide(&mut out, &old, &new, &dim, w, 1, 5, Kind::Push, 0.0);
        assert_eq!(px(&out, w, 0, 2)[0], dim[(2 * w) * 4], "at the start the old page, dimmed");
        assert_eq!(px(&out, w, 0, 0), px(&new, w, 0, 0), "the status bar is the new frame's");
        slide(&mut out, &old, &new, &dim, w, 1, 5, Kind::Push, 0.5);
        assert_eq!(px(&out, w, 6, 2), px(&new, w, 0, 2), "half way the new page's left edge is in the middle");
        assert_eq!(px(&out, w, 0, 2), px(&dim, w, 2, 2), "the old page moved a sixth of the width (a third of the way, half done)");
        slide(&mut out, &old, &new, &dim, w, 1, 5, Kind::Push, 1.0);
        assert_eq!(out, new, "the end is the new frame");
    }

    #[test]
    fn a_pop_slides_the_top_page_out_to_the_right() {
        let (w, h) = (12, 4);
        let (old, new) = (pic(w, h, 10), pic(w, h, 200));
        let dim = dimmed(&new);
        let mut out = vec![0u8; w * h * 4];
        slide(&mut out, &old, &new, &dim, w, 0, h, Kind::Pop, 0.0);
        assert_eq!(out, old, "at the start the page on top covers everything");
        slide(&mut out, &old, &new, &dim, w, 0, h, Kind::Pop, 0.5);
        assert_eq!(px(&out, w, 6, 1), px(&old, w, 0, 1), "half way it has moved half the width");
        assert_eq!(px(&out, w, 0, 1), px(&dim, w, 2, 1), "the page under it comes from a sixth to the left");
        slide(&mut out, &old, &new, &dim, w, 0, h, Kind::Pop, 1.0);
        assert_eq!(out, new);
    }

    #[test]
    fn a_menu_rises_from_behind_the_soft_keys() {
        let (w, h) = (10, 20);
        let (base, src) = (pic(w, h, 1), pic(w, h, 99));
        let pn = Panel { x0: 1, y0: 10, x1: 9, y1: 16 };
        let clip = 17;
        assert_eq!(panel_drop(pn, clip, true, Kind::Rise, 0.0), 7, "hidden behind the soft keys");
        assert_eq!(panel_drop(pn, clip, true, Kind::Rise, 1.0), 0);
        let mut out = vec![0u8; w * h * 4];
        panel(&mut out, &base, &src, w, pn, 4, clip);
        assert_eq!(px(&out, w, 2, 14), px(&src, w, 2, 10), "the panel's top row, lowered by 4");
        assert_eq!(px(&out, w, 2, 12), px(&base, w, 2, 12), "above it the dimmed screen");
        assert_eq!(px(&out, w, 2, 17), px(&base, w, 2, 17), "nothing drawn over the soft keys");
        assert_eq!(px(&out, w, 0, 14), px(&base, w, 0, 14), "beside the panel the screen");
        panel(&mut out, &base, &src, w, pn, 0, clip);
        assert_eq!(px(&out, w, 2, 10), px(&src, w, 2, 10));
        assert_eq!(panel_drop(pn, clip, false, Kind::Rise, 0.0), 3, "a dialog moves a short way");
        assert_eq!(panel_drop(pn, clip, false, Kind::Sink, 1.0), 3);
    }

    #[test]
    fn scrim_and_dim_darken_only_colour() {
        let p = pic(4, 2, 200);
        let s = scrim(&p, 4, 1, scrim_k(0.5));
        assert_eq!(px(&s, 4, 0, 0), px(&p, 4, 0, 0), "above top untouched");
        assert_eq!(px(&s, 4, 0, 1), [100, 0, 0, 255]);
        assert_eq!(dimmed(&p)[0], 143);
        assert_eq!(dimmed(&p)[3], 255);
    }

    // A banner comes in in 150 ms, eased out, and leaves in 100 ms, eased
    // in; sent away half way in, it turns round from where it is.
    #[test]
    fn a_banner_slides_in_and_out_on_time() {
        let t0 = Instant::now();
        let ms = |n: u64| Some(t0 + Duration::from_millis(n));
        let mut b = Slide::default();
        assert_eq!(b.value(ms(0)), 0.0, "out of sight at first");
        b.show(true, ms(0));
        assert_eq!(b.value(ms(0)), 0.0);
        assert!(b.value(ms(75)) > 0.8, "coming in eases out: {}", b.value(ms(75)));
        assert!(b.running(ms(149)));
        assert_eq!(b.value(ms(150)), 1.0);
        assert!(!b.running(ms(150)));
        b.show(true, ms(500));
        assert!(!b.running(ms(500)), "shown again: nothing moves");
        b.show(false, ms(1000));
        assert!(b.value(ms(1050)) > 0.8, "leaving eases in: {}", b.value(ms(1050)));
        assert!(b.running(ms(1099)));
        assert_eq!(b.value(ms(1100)), 0.0);
        b.show(true, ms(2000));
        let mid = b.value(ms(2040));
        b.show(false, ms(2040));
        assert!((b.value(ms(2040)) - mid).abs() < 1e-6, "no jump when it turns round");
        assert!(b.running(ms(2040 + (LEAVE_MS * mid) as u64 - 1)));
        assert_eq!(b.value(ms(2040 + (LEAVE_MS * mid).ceil() as u64)), 0.0, "the way back takes its share of the time");
        b.show(true, None);
        assert_eq!(b.value(ms(2041)), 1.0, "no live display: in place at once");
        assert!(!b.running(ms(2041)));
    }

    #[test]
    fn easing_and_durations() {
        assert_eq!(Kind::parse("push"), Some(Kind::Push));
        assert_eq!(Kind::parse("fade"), None);
        assert!(Kind::Push.ease(0.5) > 0.8, "entering eases out");
        assert!(Kind::Sink.ease(0.5) < 0.2, "leaving eases in");
        assert_eq!(Kind::Push.ease(1.2), 1.0);
        assert!(Kind::Sink.duration() < Kind::Push.duration());
    }
}
