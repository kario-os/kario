//! The live display's pictures (37-motion.md §5): the page layer (the
//! page, its title, the input method's rows and the soft keys), the screen
//! (the page with the top layer over it: the status bar, the banner,
//! overlays and the toast), the bands of the page's body and a scratch
//! picture. Each frame is recorded as a list of ops (canvas.rs); comparing
//! it with the last frame's gives the damage, and only the damage is
//! painted. A clock tick repaints the clock; a banner sliding in is the
//! page's pixels copied back with the banner over them; a scroll step is
//! rows of the body's bands copied, the body painted nowhere; a frame that
//! changed nothing paints nothing.
//!
//! For the GPU path (gpu.rs) the pictures are split instead of merged on
//! the CPU: the page layer without the body (transparent in the body's
//! rows but for what is drawn over it there: the scroll bar, a spinner),
//! the top layer on its own over transparent pixels, the bands, and the
//! banner and toast sliding as pictures of their own. The GPU uploads what
//! was painted and composes them: a scroll step or a slide paints and
//! uploads nothing.
//!
//! Rasterized by the GPU (L2, raster.rs) the pictures are split the same
//! way, but the CPU paints none of them: each rectangle it would paint is
//! a job, the ops to paint there, which the GPU paints into its textures.

use std::collections::HashMap;
use std::hash::{Hash, Hasher};
use std::rc::Rc;

use tiny_skia::Pixmap;

use crate::canvas::{Body, Fonts, IRect, Op, Rec, Rgb};

/// Damage over this share of the screen is painted as the whole screen.
const WHOLE: f32 = 0.6;
/// At most this many rectangles; more become their bounding box.
const MAX_RECTS: usize = 8;
/// Rectangles closer than this are joined.
const JOIN: i32 = 4;
/// At most this many bands of the body are kept (a screen's height each).
const MAX_BANDS: usize = 4;

/// Where two frames differ: in the page layer, and on the screen (the
/// page's damage and the top layer's).
#[derive(Debug, Default, Clone)]
pub struct Damage {
    pub page: Vec<IRect>,
    pub screen: Vec<IRect>,
}

impl Damage {
    fn whole(bounds: IRect) -> Damage {
        Damage { page: vec![bounds], screen: vec![bounds] }
    }

    /// The pixels the screen's damage covers.
    pub fn area(&self) -> usize {
        self.screen.iter().map(IRect::area).sum()
    }
}

/// What changed between two display lists. An op in one list and not in
/// the other damages where it paints; ops in both but in another order
/// damage where they paint from the first one out of order (the pixels
/// under both depend on the order). A body scrolled as far in both
/// damages where its own ops changed, as they show.
pub fn damage(old: &[Rec], new: &[Rec], bounds: IRect) -> Damage {
    let (mut page, top) = changes(old, new);
    if let (Some((ha, a)), Some((hb, b))) = (body_of(old), body_of(new)) {
        if ha == hb {
            let rows = IRect::new(bounds.x0, b.rows.0, bounds.x1, b.rows.1);
            page.extend(changes(&a.list, &b.list).0.iter().map(|r| IRect::new(r.x0, r.y0 - b.n, r.x1, r.y1 - b.n).intersect(&rows)));
        }
    }
    let page = merge(page, bounds);
    let mut screen = page.clone();
    screen.extend(top);
    Damage { page, screen: merge(screen, bounds) }
}

/// The list's body, and its op's hash.
fn body_of(list: &[Rec]) -> Option<(u64, &Rc<Body>)> {
    list.iter().find_map(|r| match &r.op {
        Op::Body(b) => Some((r.hash, b)),
        _ => None,
    })
}

/// Where two lists differ, not merged: in the page, and in the top layer.
fn changes(old: &[Rec], new: &[Rec]) -> (Vec<IRect>, Vec<IRect>) {
    let (mut page, mut top) = (Vec::new(), Vec::new());
    let mut add = |r: &Rec| if r.top { top.push(r.bbox) } else { page.push(r.bbox) };
    let key = |r: &Rec| (r.hash, r.top);
    let count = |list: &[Rec]| {
        let mut m: HashMap<(u64, bool), usize> = HashMap::with_capacity(list.len());
        for r in list {
            *m.entry(key(r)).or_default() += 1;
        }
        m
    };
    let (mut in_new, mut in_old) = (count(new), count(old));
    let kept = |list: &[Rec], other: &mut HashMap<(u64, bool), usize>, add: &mut dyn FnMut(&Rec)| -> Vec<usize> {
        let mut out = Vec::new();
        for (i, r) in list.iter().enumerate() {
            match other.get_mut(&key(r)) {
                Some(n) if *n > 0 => {
                    *n -= 1;
                    out.push(i);
                }
                _ => add(r),
            }
        }
        out
    };
    let kept_old = kept(old, &mut in_new, &mut add);
    let kept_new = kept(new, &mut in_old, &mut add);
    if let Some(i) = kept_old.iter().zip(&kept_new).position(|(&a, &b)| key(&old[a]) != key(&new[b])) {
        for &k in &kept_old[i..] {
            add(&old[k]);
        }
        for &k in &kept_new[i..] {
            add(&new[k]);
        }
    }
    (page, top)
}

/// The rectangles inside bounds, those that overlap or nearly joined; many,
/// or most of the screen, become one.
pub fn merge(rects: Vec<IRect>, bounds: IRect) -> Vec<IRect> {
    let mut out: Vec<IRect> = Vec::new();
    for r in rects {
        let mut r = r.intersect(&bounds);
        if r.is_empty() {
            continue;
        }
        while let Some(i) = out.iter().position(|o| o.grow(JOIN).intersects(&r)) {
            r = r.union(&out.swap_remove(i));
        }
        out.push(r);
    }
    if out.len() > MAX_RECTS {
        out = vec![out.iter().fold(IRect::default(), |a, r| a.union(r))];
    }
    let area: usize = out.iter().map(IRect::area).sum();
    if area as f32 > WHOLE * bounds.area() as f32 {
        return vec![bounds];
    }
    out
}

fn copy(from: &Pixmap, to: &mut Pixmap, r: IRect) {
    let w = from.width() as usize;
    let (x0, x1) = (r.x0 as usize * 4, r.x1 as usize * 4);
    let (src, dst) = (from.data(), to.data_mut());
    for y in r.y0 as usize..r.y1 as usize {
        dst[y * w * 4 + x0..y * w * 4 + x1].copy_from_slice(&src[y * w * 4 + x0..y * w * 4 + x1]);
    }
}

fn zero(pm: &mut Pixmap, r: IRect) {
    let w = pm.width() as usize;
    let data = pm.data_mut();
    for y in r.y0 as usize..r.y1 as usize {
        data[(y * w + r.x0 as usize) * 4..(y * w + r.x1 as usize) * 4].fill(0);
    }
}

/// A banner or the toast sliding (canvas.rs's Op::Layer) that the GPU
/// path composes itself: its picture (the screen's size), the part of it
/// that slides, how far down, and the screen rows it shows in.
#[derive(Clone)]
pub struct Slide {
    pub key: u64,
    pub src: Rc<Pixmap>,
    pub from: IRect,
    pub dy: i32,
    pub clip: (i32, i32),
}

impl Slide {
    /// The screen pixels it covers.
    pub fn shown(&self, bounds: IRect) -> IRect {
        let moved = IRect::new(self.from.x0, self.from.y0 + self.dy, self.from.x1, self.from.y1 + self.dy);
        moved.intersect(&IRect::new(bounds.x0, self.clip.0, bounds.x1, self.clip.1)).intersect(&bounds)
    }

    fn same(&self, o: &Slide) -> bool {
        (self.key, self.from, self.dy, self.clip) == (o.key, o.from, o.dy, o.clip)
    }
}

/// What the GPU has to upload since it last took this: rectangles painted
/// in the page layer, in the top layer and in the body's bands (by band
/// number).
#[derive(Default, Debug)]
pub struct Dirty {
    pub page: Vec<IRect>,
    pub top: Vec<IRect>,
    pub bands: Vec<(i32, IRect)>,
}

/// Which picture a job paints: the page layer, the top layer, a band of
/// the body (by number).
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Onto {
    Page,
    Top,
    Band(i32),
}

/// A rectangle of a picture for the GPU to paint as the CPU would: filled
/// with base (None: transparent), then the ops at scale s, moved up oy
/// rows (a band of the body).
#[derive(Clone)]
pub struct Job {
    pub onto: Onto,
    pub rect: IRect,
    pub base: Option<Rgb>,
    pub ops: Vec<Rec>,
    pub oy: i32,
    pub s: f32,
}

/// The GPU path's own pictures: the top layer, the slides taken out of it;
/// rasterized by the GPU, the jobs instead of what was painted.
struct Split {
    top: Pixmap,
    slides: Vec<Slide>,
    dirty: Dirty,
    jobs: Option<Vec<Job>>,
}

/// At most this many slides are composed by the GPU (the banner and the
/// toast); more are painted into the top layer.
const MAX_SLIDES: usize = 2;

/// Takes the sliding layers that nothing drawn after them in the top
/// layer covers out of the list: the GPU composes them over everything,
/// at their place. Others stay, painted into the top layer.
fn lift(list: &mut Vec<Rec>, bounds: IRect) -> Vec<Slide> {
    let mut out = Vec::new();
    let mut i = 0;
    while i < list.len() {
        if let (Op::Layer { src, key, from, dy, clip }, true) = (&list[i].op, list[i].top) {
            let r = list[i].bbox.intersect(&bounds);
            if out.len() < MAX_SLIDES && !list[i + 1..].iter().any(|x| x.top && x.bbox.intersects(&r)) {
                out.push(Slide { key: *key, src: src.clone(), from: *from, dy: *dy, clip: *clip });
                list.remove(i);
                continue;
            }
        }
        i += 1;
    }
    out
}

/// The body's op told apart by its place only: the GPU path moves the
/// bands, so a scroll changes nothing in the page layer.
fn unscrolled(list: &mut [Rec]) {
    for r in list {
        if let Op::Body(b) = &r.op {
            let mut h = std::collections::hash_map::DefaultHasher::new();
            ("body", b.rows, b.band, b.bg.0, b.bg.1, b.bg.2).hash(&mut h);
            r.hash = h.finish();
        }
    }
}

/// A rectangle split at the body's rows: the part inside them (true) and
/// the parts above and below.
fn parts(r: IRect, rows: IRect) -> Vec<(IRect, bool)> {
    let inside = r.intersect(&rows);
    if inside.is_empty() {
        return vec![(r, false)];
    }
    let above = IRect::new(r.x0, r.y0, r.x1, r.y1.min(rows.y0));
    let below = IRect::new(r.x0, r.y0.max(rows.y1), r.x1, r.y1);
    std::iter::once((inside, true)).chain([above, below].into_iter().filter(|p| !p.is_empty()).map(|p| (p, false))).collect()
}

pub struct Layers {
    page: Pixmap,
    screen: Pixmap,
    scratch: Pixmap,
    size: (u32, u32),
    list: Vec<Rec>,
    s: f32,
    fonts: &'static Fonts,
    /// The body's bands drawn (each band's number and picture, as the
    /// body last recorded paints it), and pictures for more.
    bands: Vec<(i32, Pixmap)>,
    spare: Vec<Pixmap>,
    body: Option<Rc<Body>>,
    /// What the last update painted; and of the body's bands, how many
    /// pixels.
    pub last: Damage,
    pub band_painted: usize,
    split: Option<Box<Split>>,
}

impl Layers {
    pub fn new(width: u32, height: u32, fonts: &'static Fonts) -> Layers {
        Self::make(width, height, fonts, false, false)
    }

    /// The pictures split for the GPU path: no screen picture; `composed`
    /// is what the GPU makes of them.
    pub fn split(width: u32, height: u32, fonts: &'static Fonts) -> Layers {
        Self::make(width, height, fonts, true, false)
    }

    /// Split, and painted by the GPU (L2): no pictures at all, the jobs
    /// (`take_jobs`) instead.
    pub fn raster(width: u32, height: u32, fonts: &'static Fonts) -> Layers {
        Self::make(width, height, fonts, true, true)
    }

    fn make(width: u32, height: u32, fonts: &'static Fonts, split: bool, raster: bool) -> Layers {
        let pm = || Pixmap::new(width.max(1), height.max(1)).unwrap();
        let none = || Pixmap::new(1, 1).unwrap();
        Layers {
            page: if raster { none() } else { pm() },
            screen: if split { none() } else { pm() },
            scratch: if raster { none() } else { pm() },
            size: (width.max(1), height.max(1)),
            list: Vec::new(),
            s: 0.0,
            fonts,
            bands: Vec::new(),
            spare: Vec::new(),
            body: None,
            last: Damage::default(),
            band_painted: 0,
            split: split.then(|| Box::new(Split { top: if raster { none() } else { pm() }, slides: Vec::new(), dirty: Dirty::default(), jobs: raster.then(Vec::new) })),
        }
    }

    /// The picture for the panel (not kept when split).
    pub fn screen(&self) -> &Pixmap {
        &self.screen
    }

    /// The screen's size in pixels.
    pub fn size(&self) -> (u32, u32) {
        self.size
    }

    /// Whether the GPU paints the pictures (L2).
    pub fn rastered(&self) -> bool {
        self.split.as_ref().is_some_and(|sp| sp.jobs.is_some())
    }

    /// L2: what to paint since the last call, in order, and the fonts.
    pub fn take_jobs(&mut self) -> (Vec<Job>, &'static Fonts) {
        (self.split.as_mut().and_then(|sp| sp.jobs.as_mut()).map(std::mem::take).unwrap_or_default(), self.fonts)
    }

    /// L2: the jobs not taken yet.
    pub fn jobs(&self) -> &[Job] {
        self.split.as_ref().and_then(|sp| sp.jobs.as_deref()).unwrap_or(&[])
    }

    /// The numbers of the body's bands drawn.
    pub fn band_numbers(&self) -> Vec<i32> {
        self.bands.iter().map(|(k, _)| *k).collect()
    }

    /// The last frame's ops, and its body's (where they are on the page
    /// not scrolled).
    pub fn recs(&self) -> (&[Rec], &[Rec]) {
        (&self.list, self.body.as_ref().map_or(&[], |b| &b.list))
    }

    /// The page layer: split, transparent where the bands show.
    pub fn page(&self) -> &Pixmap {
        &self.page
    }

    /// Split: the top layer, over transparent pixels.
    pub fn top(&self) -> Option<&Pixmap> {
        self.split.as_ref().map(|sp| &sp.top)
    }

    /// The body shown: scrolled n rows, in screen rows rows.0..rows.1,
    /// in bands `band` rows tall.
    pub fn body(&self) -> Option<(i32, (i32, i32), i32)> {
        self.body.as_ref().map(|b| (b.n, b.rows, b.band))
    }

    /// The body's bands drawn: each band's number and picture.
    pub fn bands(&self) -> &[(i32, Pixmap)] {
        &self.bands
    }

    /// Split: the slides the GPU composes over the rest, in order.
    pub fn slides(&self) -> &[Slide] {
        self.split.as_ref().map_or(&[], |sp| &sp.slides)
    }

    /// Split: what was painted since the last call.
    pub fn take_dirty(&mut self) -> Dirty {
        self.split.as_mut().map(|sp| std::mem::take(&mut sp.dirty)).unwrap_or_default()
    }

    /// Split: the screen as the GPU composes it from the pictures (the
    /// compose shader in gpu.rs does the same sums), for tests and --bench:
    /// the bands, the page layer over them, the top layer, the slides.
    pub fn composed(&self) -> Option<Pixmap> {
        let sp = self.split.as_ref().filter(|sp| sp.jobs.is_none())?;
        let (w, h) = self.size();
        let mut out = Pixmap::new(w, h)?;
        if let Some(b) = &self.body {
            for (k, band) in &self.bands {
                b.show_band(band, *k, &mut out, None);
            }
        }
        crate::canvas::over(out.data_mut(), self.page.data());
        crate::canvas::over(out.data_mut(), sp.top.data());
        for sl in &sp.slides {
            crate::canvas::layer(&mut out, &sl.src, sl.from, sl.dy, sl.clip, None);
        }
        Some(out)
    }

    /// Puts the frame recorded in list on the screen, painting only where
    /// it differs from the last one; returns the screen's damage.
    pub fn update(&mut self, list: Vec<Rec>, s: f32) -> &Damage {
        if self.split.is_some() {
            return self.update_split(list, s);
        }
        let bounds = IRect::new(0, 0, self.size.0 as i32, self.size.1 as i32);
        let whole = self.list.is_empty() || s != self.s;
        let d = if whole { Damage::whole(bounds) } else { damage(&self.list, &list, bounds) };
        self.follow(body_of(&list).map(|(_, b)| b.clone()), whole, s);
        // Ops that reach outside the rectangle (shapes, glyphs) paint the
        // scratch picture there: only the rectangle is copied on. The
        // whole screen needs no scratch.
        let cleared = matches!(list.first(), Some(Rec { op: Op::Clear(_), top: false, .. }));
        let Layers { page, screen, scratch, fonts, bands, .. } = self;
        for &r in &d.page {
            let to = if r == bounds { &mut *page } else { &mut *scratch };
            if !cleared {
                zero(to, r);
            }
            for rec in list.iter().filter(|x| !x.top && x.bbox.intersects(&r)) {
                match &rec.op {
                    Op::Body(b) => bands.iter().for_each(|(k, band)| b.show_band(band, *k, to, Some(r))),
                    op => op.paint(to, s, fonts, Some(r), 0),
                }
            }
            if r != bounds {
                copy(scratch, page, r);
            }
        }
        for &r in &d.screen {
            let to = if r == bounds { &mut *screen } else { &mut *scratch };
            copy(page, to, r);
            for rec in list.iter().filter(|x| x.top && x.bbox.intersects(&r)) {
                rec.op.paint(to, s, fonts, Some(r), 0);
            }
            if r != bounds {
                copy(scratch, screen, r);
            }
        }
        (self.list, self.s, self.last) = (list, s, d);
        &self.last
    }

    /// The split update: the page layer and the top layer painted where
    /// they changed (each over transparent pixels), the bands brought to
    /// the body, the slides taken out; returns where the composed screen
    /// may differ.
    fn update_split(&mut self, mut list: Vec<Rec>, s: f32) -> &Damage {
        let bounds = IRect::new(0, 0, self.size.0 as i32, self.size.1 as i32);
        let whole = self.list.is_empty() || s != self.s;
        let slides = lift(&mut list, bounds);
        unscrolled(&mut list);
        let (page, top) = if whole {
            (vec![bounds], vec![bounds])
        } else {
            let (p, t) = changes(&self.list, &list);
            (merge(p, bounds), merge(t, bounds))
        };
        let before = self.body.as_ref().map(|b| (b.n, b.rows));
        let painted = self.follow(body_of(&list).map(|(_, b)| b.clone()), whole, s);
        let body = self.body.clone();
        let rows = body.as_ref().map_or(IRect::default(), |b| IRect::new(bounds.x0, b.rows.0, bounds.x1, b.rows.1).intersect(&bounds));
        // Ops before the body are under it: where it shows they are not seen.
        let at = list.iter().position(|r| matches!(r.op, Op::Body(_)));
        let Layers { page: pg, scratch, fonts, split, .. } = self;
        let sp = split.as_mut().expect("split layers");
        for &r in &page {
            for (part, inside) in parts(r, rows) {
                let ops = list.iter().enumerate().filter(|(i, x)| !x.top && x.bbox.intersects(&part) && !matches!(x.op, Op::Body(_)) && (!inside || at.is_none_or(|a| *i >= a))).map(|(_, x)| x);
                if let Some(jobs) = sp.jobs.as_mut() {
                    jobs.push(Job { onto: Onto::Page, rect: part, base: None, ops: ops.cloned().collect(), oy: 0, s });
                    continue;
                }
                let to = if part == bounds { &mut *pg } else { &mut *scratch };
                zero(to, part);
                for rec in ops {
                    rec.op.paint(to, s, fonts, Some(part), 0);
                }
                if part != bounds {
                    copy(scratch, pg, part);
                }
            }
        }
        for &r in &top {
            let ops = list.iter().filter(|x| x.top && x.bbox.intersects(&r));
            if let Some(jobs) = sp.jobs.as_mut() {
                jobs.push(Job { onto: Onto::Top, rect: r, base: None, ops: ops.cloned().collect(), oy: 0, s });
                continue;
            }
            let to = if r == bounds { &mut sp.top } else { &mut *scratch };
            zero(to, r);
            for rec in ops {
                rec.op.paint(to, s, fonts, Some(r), 0);
            }
            if r != bounds {
                copy(scratch, &mut sp.top, r);
            }
        }
        let mut screen: Vec<IRect> = page.iter().chain(&top).copied().collect();
        if let Some(b) = &body {
            if before != Some((b.n, b.rows)) {
                screen.push(rows);
            }
            screen.extend(painted.iter().map(|&(k, r)| IRect::new(r.x0, r.y0 + k * b.band - b.n, r.x1, r.y1 + k * b.band - b.n).intersect(&rows)));
        }
        if sp.slides.len() != slides.len() || sp.slides.iter().zip(&slides).any(|(a, b)| !a.same(b)) {
            screen.extend(sp.slides.iter().chain(&slides).map(|sl| sl.shown(bounds)));
        }
        sp.slides = slides;
        if sp.jobs.is_none() {
            sp.dirty.page.extend_from_slice(&page);
            sp.dirty.top.extend_from_slice(&top);
            sp.dirty.bands.extend(painted);
        }
        (self.list, self.s) = (list, s);
        self.last = Damage { page, screen: merge(screen, bounds) };
        &self.last
    }

    /// Brings the bands to the body just recorded: where its ops changed
    /// is painted again in the bands there are, the bands it neither shows
    /// nor keeps go, and those it does that are missing are drawn. Returns
    /// what it painted: each band's number and rectangle.
    fn follow(&mut self, body: Option<Rc<Body>>, whole: bool, s: f32) -> Vec<(i32, IRect)> {
        let mut painted = Vec::new();
        self.band_painted = 0;
        let old = self.body.take();
        let Some(b) = body else {
            self.spare.extend(self.bands.drain(..).map(|(_, pm)| pm));
            self.spare.truncate(1);
            return painted;
        };
        let (w, h) = (self.size.0 as i32, self.size.1 as i32);
        let old = old.filter(|o| !whole && o.bg == b.bg && o.band == b.band && b.band == h);
        let mut need = b.bands(b.keep.0.min(b.rows.0 + b.n), b.keep.1.max(b.rows.1 + b.n));
        if need.len() > MAX_BANDS {
            need = b.bands(b.rows.0 + b.n, b.rows.1 + b.n);
        }
        let (keep, gone): (Vec<_>, Vec<_>) = self.bands.drain(..).partition(|(k, _)| old.is_some() && need.contains(k));
        self.bands = keep;
        self.spare.extend(gone.into_iter().map(|(_, pm)| pm));
        self.spare.truncate(1);
        let area = IRect::new(0, 0, w, b.band);
        let mut todo = Vec::new();
        if let Some(o) = old {
            let (moved, _) = changes(&o.list, &b.list);
            for (k, _) in &self.bands {
                let at = *k * b.band;
                todo.extend(merge(moved.iter().map(|r| IRect::new(r.x0, r.y0 - at, r.x1, r.y1 - at)).collect(), area).into_iter().map(|r| (*k, r)));
            }
        }
        let rastered = self.rastered();
        for k in need {
            if self.bands.iter().any(|(j, _)| *j == k) {
                continue;
            }
            let pm = if rastered { Pixmap::new(1, 1) } else { self.spare.pop().filter(|p| p.width() as i32 == w && p.height() as i32 == b.band).or_else(|| Pixmap::new(w as u32, b.band as u32)) };
            let Some(pm) = pm else { continue };
            self.bands.push((k, pm));
            todo.push((k, area));
        }
        for (k, r) in todo {
            self.paint_band(&b, k, r, s);
            self.band_painted += r.area();
            painted.push((k, r));
        }
        self.body = Some(b);
        painted
    }

    /// Paints rectangle r of band k as body b paints it, or lists it for
    /// the GPU.
    fn paint_band(&mut self, b: &Body, k: i32, r: IRect, s: f32) {
        let Layers { bands, scratch, fonts, split, size, .. } = self;
        if let Some(jobs) = split.as_mut().and_then(|sp| sp.jobs.as_mut()) {
            let at = k * b.band;
            let moved = IRect::new(r.x0, r.y0 + at, r.x1, r.y1 + at);
            jobs.push(Job { onto: Onto::Band(k), rect: r, base: Some(b.bg), ops: b.list.iter().filter(|x| x.bbox.intersects(&moved)).cloned().collect(), oy: at, s });
            return;
        }
        let Some((_, pm)) = bands.iter_mut().find(|(j, _)| *j == k) else { return };
        if r == IRect::new(0, 0, size.0 as i32, b.band) {
            b.paint_band(pm, k, s, fonts, None);
        } else {
            b.paint_band(scratch, k, s, fonts, Some(r));
            copy(scratch, pm, r);
        }
    }
}

#[cfg(test)]
mod tests {
    use std::time::{Duration, Instant};

    use base64::Engine as _;
    use serde_json::{json, Value};

    use super::*;
    use crate::canvas::{Canvas, Rgb};
    use crate::frame::{self, Frame};
    use crate::paint::{self, State};

    const W: u32 = 480;
    const H: u32 = 640;
    const S: f32 = 2.0;
    const BOUNDS: IRect = IRect { x0: 0, y0: 0, x1: W as i32, y1: H as i32 };

    fn fonts() -> Option<&'static Fonts> {
        let Ok(f) = Fonts::load() else {
            eprintln!("no fonts: skipped");
            return None;
        };
        Some(Box::leak(Box::new(f)))
    }

    fn parse(v: &Value) -> Frame {
        serde_json::from_value(v.clone()).unwrap()
    }

    fn patched(v: &Value, p: Value) -> Value {
        let mut v = v.clone();
        frame::merge(&mut v, &p);
        v
    }

    /// The whole screen drawn at once, as before layers (and as snapshots).
    fn full(f: &Frame, st: &mut State, fonts: &'static Fonts) -> Pixmap {
        full_at(f, st, fonts, S)
    }

    fn full_at(f: &Frame, st: &mut State, fonts: &'static Fonts, s: f32) -> Pixmap {
        let mut cv = Canvas::new(W, H, s, fonts);
        cv.mask_secure = false;
        paint::draw(&mut cv, f, st);
        cv.pm
    }

    fn record(f: &Frame, st: &mut State, fonts: &'static Fonts) -> Vec<Rec> {
        record_at(f, st, fonts, S)
    }

    fn record_at(f: &Frame, st: &mut State, fonts: &'static Fonts, s: f32) -> Vec<Rec> {
        let mut cv = Canvas::recording(W, H, s, fonts);
        cv.mask_secure = false;
        paint::draw(&mut cv, f, st);
        cv.take()
    }

    /// The live display going from frame a (settled) to b, a step a period
    /// of 60 Hz apart until b's motion settles, each step checked against
    /// a full draw at the same moment; for each step, the pixels painted
    /// into the body's bands and the screen's damage.
    fn live(a: &Value, b: &Value, s: f32, fonts: &'static Fonts) -> Vec<(usize, usize)> {
        live_from(a, b, s, fonts, false)
    }

    /// As live, a drawn again a second later first (settled: a list
    /// scrolled to its end has stopped scrolling).
    fn live_from(a: &Value, b: &Value, s: f32, fonts: &'static Fonts, settle: bool) -> Vec<(usize, usize)> {
        let (fa, fb) = (parse(a), parse(b));
        let t0 = Instant::now();
        let (mut sd, mut sr) = (State::default(), State::default());
        let mut layers = Layers::new(W, H, fonts);
        let mut now = t0;
        for _ in 0..1 + settle as usize {
            (sd.now, sr.now) = (Some(now), Some(now));
            full_at(&fa, &mut sd, fonts, s);
            layers.update(record_at(&fa, &mut sr, fonts, s), s);
            now += Duration::from_secs(1);
        }
        let mut out = Vec::new();
        loop {
            (sd.now, sr.now) = (Some(now), Some(now));
            let want = full_at(&fb, &mut sd, fonts, s);
            let d = layers.update(record_at(&fb, &mut sr, fonts, s), s).area();
            assert!(layers.screen() == &want, "{}: step {} is not what a full draw shows", fb.screen, out.len());
            assert!(layers.bands.len() <= MAX_BANDS, "{} bands kept", layers.bands.len());
            out.push((layers.band_painted, d));
            if !paint::animating(&sr) {
                break;
            }
            now += crate::pace::period(crate::pace::DEFAULT_HZ);
        }
        out
    }

    fn doc(pos: f32, paragraphs: usize) -> Value {
        let body: Vec<Value> = (0..paragraphs).map(|i| json!({"kind": "text", "text": format!("Paragraph {i}: words to wrap over the line, and more words after them.")})).collect();
        json!({"screen": "app/doc", "title": "Document", "focus": -1, "scroll": {"pos": pos, "frac": -1}, "body": body, "soft": ["Options", "", "Back"],
            "status": {"time": "10:00", "battery": 80}})
    }

    fn long_list(focus: i32, n: i32) -> Value {
        let body: Vec<Value> = (0..n)
            .map(|i| {
                let mut v = json!({"kind": "item", "title": format!("Contact {i}"), "subtitle": format!("+86 138 0000 {i:04}"), "focused": i == focus});
                if i % 5 == 2 {
                    v = json!({"kind": "image", "label": format!("Picture {i}"), "image": picture(), "focused": i == focus});
                }
                v
            })
            .collect();
        json!({"screen": "app/contacts", "title": "Contacts", "focus": focus, "body": body, "soft": ["Options", "Open", "Back"],
            "status": {"time": "10:00", "battery": 80}})
    }

    // A scroll step copies the body's rows: what the scroll goes over is
    // painted once, when the frame comes, and each step after it paints
    // nothing of the body (37-motion.md §3); every step is what a full
    // draw at that moment shows.
    #[test]
    fn scrolling_copies_the_body() {
        let Some(fonts) = fonts() else { return };
        for (a, b) in [(doc(0.0, 40), doc(0.15, 40)), (doc(0.15, 40), doc(0.05, 40))] {
            let steps = live(&a, &b, S, fonts);
            assert!(steps.len() >= 6, "{} steps for 140 ms at 60 Hz", steps.len());
            for (i, &(band, _)) in steps.iter().enumerate().skip(1) {
                assert_eq!(band, 0, "step {i} painted the body");
            }
        }
    }

    // The focus going down a long list, with pictures, scrolls it across
    // the bands; at 2.4 (Large text) the bars' edges fall inside rows; a
    // jump to the end keeps only the bands it shows.
    #[test]
    fn scrolled_steps_are_full_draws() {
        let Some(fonts) = fonts() else { return };
        for (a, b, s) in [
            (long_list(1, 14), long_list(13, 14), S),
            (long_list(13, 14), long_list(2, 14), S),
            (long_list(1, 14), long_list(13, 14), 2.4),
            (doc(0.0, 40), doc(0.3, 40), 2.4),
            (long_list(0, 60), long_list(59, 60), S),
            (long_list(59, 60), long_list(0, 60), S),
        ] {
            live(&a, &b, s, fonts);
        }
    }

    // The text cursor blinks, 500 ms on and 500 ms off, painting only
    // itself; typing shows it at once. A snapshot always shows it.
    #[test]
    fn the_cursor_blinks_in_its_own_rectangle() {
        let Some(fonts) = fonts() else { return };
        let field = |v: &str| {
            json!({"screen": "app/compose", "title": "To Zhang", "focus": 1, "soft": ["Options", "Send", "Back"], "status": {"time": "10:00"},
                "body": [{"kind": "bubble", "title": "Hello", "subtitle": "09:58"}, {"kind": "input", "placeholder": "Message", "value": v, "focused": true}]})
        };
        let t0 = Instant::now();
        let (mut sd, mut sr) = (State::default(), State::default());
        let mut layers = Layers::new(W, H, fonts);
        let mut at = |ms: u64, f: &Frame| {
            let now = t0 + Duration::from_millis(ms);
            (sd.now, sr.now) = (Some(now), Some(now));
            let want = full(f, &mut sd, fonts);
            let d = layers.update(record(f, &mut sr, fonts), S).clone();
            assert!(layers.screen() == &want, "{ms} ms: not what a full draw shows");
            (d.area(), paint::next_tick(&sr, now).map(|t| t.duration_since(t0).as_millis()))
        };
        let f = parse(&field("ni hao"));
        assert_eq!(at(0, &f).1, Some(500));
        assert_eq!(at(300, &f), (0, Some(500)), "still shown");
        let (hide, next) = at(500, &f);
        assert!(hide > 0 && hide < (W * H / 100) as usize, "hiding it painted {hide} pixels");
        assert_eq!(next, Some(1000));
        assert!(at(1000, &f).0 > 0, "shown again");
        let typed = parse(&field("ni hao a"));
        assert!(at(1700, &typed).0 > 0);
        assert_eq!(at(1800, &typed), (0, Some(2200)), "typing shows it at once, for 500 ms");
        let menu = json!({"overlay": {"kind": "menu", "title": "Message", "items": [{"text": "Copy", "sel": true}], "phase": -1}});
        let covered = parse(&patched(&field("ni hao a"), menu));
        assert_eq!(at(1900, &covered).1, None, "under a menu it does not blink");
        let standby = parse(&json!({"screen": "system/sys.idle", "status": {"time": "10:00"}, "idle": {"time": "10:00", "date": "Friday"}}));
        assert_eq!(at(2000, &typed).1, Some(2500));
        assert_eq!(at(2100, &standby).1, None, "nothing blinks on standby");
        let mut snap = State::default();
        let shown = full(&typed, &mut snap, fonts);
        sd.now = None;
        assert!(full(&typed, &mut sd, fonts) == shown, "a snapshot shows the cursor");
    }

    // While an app has not sent its first page: the outline of one under
    // its title, and a spinner turning a dot every 125 ms, painting only
    // itself. The first page ends it.
    #[test]
    fn a_skeleton_page_and_its_spinner() {
        let Some(fonts) = fonts() else { return };
        let start = json!({"screen": "", "title": "Notes", "loading": "Starting \u{201c}Notes\u{201d}\u{2026}", "skeleton": true, "soft": ["Options", "", ""],
            "status": {"time": "10:00", "battery": 80}});
        let plain = patched(&start, json!({"skeleton": null}));
        let mut st = State::default();
        let (sk, generic) = (full(&parse(&start), &mut st, fonts), full(&parse(&plain), &mut State::default(), fonts));
        let at = |pm: &Pixmap, x: f32, y: f32| pm.pixel((x * S) as u32, (y * S) as u32).unwrap();
        let line = crate::design::DARK.line;
        let (x, y) = (crate::design::PAD + 15.0, crate::design::STATUS_H + crate::design::TITLE_H + crate::design::GAP + 18.0);
        assert_eq!((at(&sk, x, y).red(), at(&sk, x, y).green(), at(&sk, x, y).blue()), (line.0, line.1, line.2), "a row's shape under the title");
        assert!(at(&generic, x, y) != at(&sk, x, y), "the plain loading page has none");
        let f = parse(&start);
        let t0 = Instant::now();
        let (mut sd, mut sr) = (State::default(), State::default());
        let mut layers = Layers::new(W, H, fonts);
        for k in 0..10u64 {
            let now = t0 + Duration::from_millis(125 * k);
            (sd.now, sr.now) = (Some(now), Some(now));
            let want = full(&f, &mut sd, fonts);
            let d = layers.update(record(&f, &mut sr, fonts), S).area();
            assert!(layers.screen() == &want, "step {k}: not what a full draw shows");
            assert!(k == 0 || (d > 0 && d < (W * H / 100) as usize), "step {k} painted {d} pixels");
            assert_eq!(paint::next_tick(&sr, now), Some(now + Duration::from_millis(125)));
        }
        let page = parse(&list(1, "10:00"));
        sr.now = Some(t0 + Duration::from_secs(2));
        layers.update(record(&page, &mut sr, fonts), S);
        assert_eq!(paint::next_tick(&sr, t0 + Duration::from_secs(2)), None, "the first page has no spinner");
    }

    fn list(focus: i32, time: &str) -> Value {
        let body: Vec<Value> = (0..14)
            .map(|i| json!({"kind": "item", "title": format!("Item {i}"), "subtitle": format!("line {i}"), "focused": i == focus}))
            .collect();
        json!({"screen": "app/list", "title": "Messages", "focus": focus, "body": body, "soft": ["Options", "Open", "Back"],
            "status": {"time": time, "net": "4G", "signal": 3, "battery": 80, "percent": true}})
    }

    fn picture() -> Value {
        let (w, h) = (24u32, 16u32);
        let px: Vec<u8> = (0..w * h).flat_map(|i| [(i * 7) as u8, (i * 3) as u8, 200]).collect();
        json!({"w": w, "h": h, "rgb": base64::engine::general_purpose::STANDARD.encode(px)})
    }

    fn icon() -> String {
        // A file for each call: the icon cache knows a file by its time,
        // and tests running at once would write each other's again.
        static N: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);
        let n = N.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!("phone-paint-test-icon-{}-{n}.png", std::process::id()));
        let mut pm = Pixmap::new(32, 32).unwrap();
        pm.fill(tiny_skia::Color::from_rgba8(0xd0, 0x40, 0x40, 255));
        pm.save_png(&path).unwrap();
        path.to_string_lossy().into_owned()
    }

    fn grid(sel: i32, icon: &str) -> Value {
        let tiles: Vec<Value> = (0..9).map(|i| json!({"title": format!("App {i}"), "icon": "A", "image": if i == 4 { icon } else { "" }, "focused": i == sel})).collect();
        json!({"screen": "system/sys.menu", "title": "Menu", "focus": 0, "body": [{"kind": "grid", "columns": 3, "sel": sel, "focused": true, "tiles": tiles}],
            "soft": ["Options", "Open", "Back"], "status": {"time": "10:00", "battery": 80}})
    }

    /// Pairs of frames, one after the other, and whether the page under
    /// the top layer (status bar, banner, overlays, toast) may change.
    fn pairs() -> Vec<(&'static str, Value, Value, bool)> {
        let l = list(1, "10:00");
        let menu = |sel: usize| {
            let items: Vec<Value> = ["Reply", "Forward", "Delete", "Copy"].iter().enumerate().map(|(i, t)| json!({"text": t, "sel": i == sel})).collect();
            patched(&l, json!({"overlay": {"kind": "menu", "title": "Message", "items": items, "phase": -1}}))
        };
        let input = |v: &str| {
            json!({"screen": "app/compose", "title": "To Zhang", "focus": 1, "soft": ["Options", "Send", "Back"],
                "status": {"time": "10:00", "battery": 80, "ime": "Pinyin"},
                "body": [{"kind": "bubble", "title": "Hello there, see you at eight", "subtitle": "09:58"},
                         {"kind": "input", "placeholder": "Message", "value": v, "focused": true}],
                "ime": [{"items": ["zhong", "zhongguo"], "sel": 0, "active": true}, {"items": ["\u{4e2d}", "\u{79cd}", "\u{91cd}"], "sel": 0, "numbered": true}]})
        };
        let idle = |t: &str| json!({"screen": "system/sys.idle", "status": {"time": t, "battery": 80}, "soft": ["Menu", "", "Contacts"],
            "idle": {"time": t, "date": "Wednesday 18 November", "operator": "Kario", "lines": ["2 messages"], "hint": "OK: menu"}});
        let pic = json!({"screen": "app/photo", "title": "Photo", "focus": 0, "status": {"time": "10:00"},
            "body": [{"kind": "image", "label": "Evening", "image": picture(), "focused": true}, {"kind": "text", "text": "A caption"}]});
        let wall = |t: &str| patched(&idle(t), json!({"idle": {"wallpaper": picture()}}));
        let ic = icon();
        vec![
            ("clock tick", l.clone(), list(1, "10:01"), false),
            ("focus moves down two rows", l.clone(), list(3, "10:00"), true),
            ("focus scrolls the list", l.clone(), list(13, "10:00"), true),
            ("banner comes", l.clone(), patched(&l, json!({"banner": ["New message", "Hello there"]})), false),
            ("banner goes", patched(&l, json!({"banner": ["New message", "Hello there"]})), l.clone(), false),
            ("toast", l.clone(), patched(&l, json!({"toast": "Copied"})), false),
            ("menu opens", l.clone(), menu(0), false),
            ("menu selection moves", menu(0), menu(2), false),
            ("dialog over the list", l.clone(), patched(&l, json!({"overlay": {"kind": "confirm", "title": "Delete?", "lines": ["It cannot be undone"], "choices": ["Cancel", "Delete"], "choice": 1, "phase": -1}})), false),
            ("theme", l.clone(), patched(&l, json!({"theme": "light"})), true),
            ("typing with candidates", input("ni"), input("nih"), true),
            ("standby clock", idle("10:00"), idle("10:01"), true),
            ("grid focus with an icon", grid(0, &ic), grid(4, &ic), true),
            ("picture page, caption changes", pic.clone(), patched(&pic, json!({"body": {"$set": {"1": {"text": "Another caption"}}}})), true),
            ("another page", l.clone(), pic.clone(), true),
            ("standby clock over a wallpaper", wall("10:00"), wall("10:01"), true),
            ("a long page scrolled", doc(0.0, 40), doc(0.6, 40), true),
            ("a list with pictures scrolled to its end", long_list(1, 30), long_list(29, 30), true),
        ]
    }

    // Every pixel that differs between two full draws lies in the damage,
    // and painting only the damage gives exactly the full draw; changes in
    // the top layer leave the page alone.
    #[test]
    fn every_changed_pixel_is_inside_the_damage() {
        let Some(fonts) = fonts() else { return };
        for (name, a, b, page) in pairs() {
            let (a, b) = (parse(&a), parse(&b));
            let mut st = State::default();
            let pa = full(&a, &mut st, fonts);
            let pb = full(&b, &mut st, fonts);
            let mut sr = State::default();
            let (la, lb) = (record(&a, &mut sr, fonts), record(&b, &mut sr, fonts));
            let d = damage(&la, &lb, BOUNDS);
            let mut changed = 0;
            for (i, (x, y)) in pa.pixels().iter().zip(pb.pixels()).enumerate() {
                if x != y {
                    changed += 1;
                    let (px, py) = ((i as u32 % W) as i32, (i as u32 / W) as i32);
                    assert!(d.screen.iter().any(|r| r.contains(px, py)), "{name}: ({px}, {py}) changed outside {:?}", d.screen);
                }
            }
            assert!(changed > 0, "{name}: the frames look the same");
            assert!(page || d.page.is_empty(), "{name}: repainted the page at {:?}", d.page);
            let mut layers = Layers::new(W, H, fonts);
            layers.update(la, S);
            layers.update(lb, S);
            assert!(layers.screen().data() == pb.data(), "{name}: the painted damage differs from a full draw");
        }
    }

    #[test]
    fn unchanged_frames_paint_nothing() {
        let Some(fonts) = fonts() else { return };
        for (name, a, _, _) in pairs() {
            let a = parse(&a);
            let mut st = State::default();
            let mut layers = Layers::new(W, H, fonts);
            assert_eq!(layers.update(record(&a, &mut st, fonts), S).area(), (W * H) as usize, "{name}: the first frame paints everything");
            let before = layers.screen().clone();
            let d = layers.update(record(&a, &mut st, fonts), S);
            assert!(d.page.is_empty() && d.screen.is_empty(), "{name}: the same frame damaged {:?}", d.screen);
            assert!(layers.screen() == &before, "{name}");
        }
    }

    // How much of the 480x640 screen the common changes repaint.
    #[test]
    fn small_changes_paint_little() {
        let Some(fonts) = fonts() else { return };
        let screen = (W * H) as f32;
        for (name, a, b, most) in [
            ("clock tick", list(1, "10:00"), list(1, "10:01"), 0.01),
            ("focus moves down a row (two rows of 72 pixels)", list(1, "10:00"), list(2, "10:00"), 0.25),
            ("banner comes (its strip)", list(1, "10:00"), patched(&list(1, "10:00"), json!({"banner": ["New message", "Hello"]})), 0.15),
        ] {
            let mut st = State::default();
            let la = record(&parse(&a), &mut st, fonts);
            let lb = record(&parse(&b), &mut st, fonts);
            let share = damage(&la, &lb, BOUNDS).area() as f32 / screen;
            assert!(share > 0.0 && share <= most, "{name}: {:.1}% of the screen", share * 100.0);
        }
    }

    // A banner and the toast sliding in and out: each step is what a full
    // draw at that moment shows, only their strip is painted, and the
    // page under them is never repainted. Reduced motion: no slide.
    #[test]
    fn banners_slide_over_the_page_without_repainting_it() {
        let Some(fonts) = fonts() else { return };
        let l = list(1, "10:00");
        let seq = [
            ("banner", patched(&l, json!({"banner": ["New message", "Hello there"]}))),
            ("no banner", l.clone()),
            ("toast", patched(&l, json!({"toast": "Copied"}))),
            ("no toast", l.clone()),
        ];
        let t0 = Instant::now();
        let (mut sd, mut sr) = (State::default(), State::default());
        let mut layers = Layers::new(W, H, fonts);
        sd.now = Some(t0);
        sr.now = Some(t0);
        full(&parse(&l), &mut sd, fonts);
        layers.update(record(&parse(&l), &mut sr, fonts), S);
        for (k, (name, v)) in seq.iter().enumerate() {
            let f = parse(v);
            let mut now = t0 + Duration::from_secs(k as u64 + 1);
            let mut steps = 0;
            loop {
                (sd.now, sr.now) = (Some(now), Some(now));
                let want = full(&f, &mut sd, fonts);
                let d = layers.update(record(&f, &mut sr, fonts), S).clone();
                assert!(layers.screen() == &want, "{name}, step {steps}: not what a full draw shows");
                assert!(d.page.is_empty(), "{name}, step {steps}: the page repainted at {:?}", d.page);
                assert!(d.area() as f32 <= 0.15 * (W * H) as f32, "{name}, step {steps}: painted {:?}", d.screen);
                steps += 1;
                if !paint::animating(&sr) {
                    break;
                }
                now += crate::pace::period(crate::pace::DEFAULT_HZ);
            }
            assert!((6..=12).contains(&steps), "{name}: {steps} steps for 100-150 ms at 60 Hz");
        }
        let reduced = parse(&patched(&seq[0].1, json!({"motion": "reduced"})));
        sr.now = Some(t0 + Duration::from_secs(9));
        record(&reduced, &mut sr, fonts);
        assert!(!paint::animating(&sr), "reduced motion: the banner is there at once");
    }

    fn month(sel: i32) -> Value {
        let tiles: Vec<Value> = (1..=35).map(|d| json!({"title": format!("{}", (d - 1) % 31 + 1), "subtitle": "\u{521d}\u{4e00}", "focused": d - 1 == sel})).collect();
        json!({"screen": "com.phone.calendar/month", "title": "November 2026", "focus": 0, "soft": ["Options", "Open", "Back"], "status": {"time": "10:00", "battery": 80},
            "body": [{"kind": "grid", "columns": 7, "sel": sel, "focused": true, "tiles": tiles}]})
    }

    fn compose(sel: i32, more: bool) -> Value {
        json!({"screen": "app/compose", "title": "To Zhang", "focus": 1, "soft": ["Options", "Send", "Back"],
            "status": {"time": "10:00", "battery": 80, "ime": "Pinyin"},
            "body": [{"kind": "bubble", "title": "Hello there", "subtitle": "09:58"}, {"kind": "input", "placeholder": "Message", "value": "", "preedit": "zhong", "focused": true}],
            "ime": [{"items": ["zhong", "zhongguo"], "sel": 0}, {"items": if more { json!(["\u{949f}", "\u{7ec8}"]) } else { json!(["\u{4e2d}", "\u{79cd}", "\u{91cd}", "\u{4f17}"]) }, "sel": sel, "active": true, "numbered": true}]})
    }

    // The focus moving in a grid (the menu's icons, a calendar's days) and
    // along the candidates: the highlight glides in 100 ms as the list's,
    // each step what a full draw shows, painting only the cells it passes;
    // new candidates (another page) put it in place. Reduced motion glides
    // as a list's focus does; motion off: no glide.
    #[test]
    fn highlights_glide_over_grids_and_candidates() {
        let Some(fonts) = fonts() else { return };
        let ic = icon();
        let screen = (W * H) as usize;
        for (name, a, b, most) in [
            ("menu, a tile right", grid(0, &ic), grid(1, &ic), 0.18),
            ("menu, a row up", grid(4, &ic), grid(1, &ic), 0.18),
            ("month, a week down", month(10), month(17), 0.06),
            ("candidates, two on", compose(0, false), compose(2, false), 0.06),
        ] {
            let steps = live(&a, &b, S, fonts);
            assert!((6..=9).contains(&steps.len()), "{name}: {} steps for 100 ms at 60 Hz", steps.len());
            for (i, &(band, d)) in steps.iter().enumerate() {
                assert!((i == 0 || d > 0) && d as f32 <= most * screen as f32, "{name}, step {i}: painted {:.1}% of the screen", 100.0 * d as f32 / screen as f32);
                assert!(band <= d, "{name}, step {i}: the body painted {band}");
            }
            let with = |v: &Value, m: &str| patched(v, json!({"motion": m}));
            assert_eq!(live(&with(&a, "reduced"), &with(&b, "reduced"), S, fonts).len(), steps.len(), "{name}: reduced motion glides as a list does");
            assert_eq!(live(&with(&a, "off"), &with(&b, "off"), S, fonts).len(), 1, "{name}: motion off, no glide");
        }
        assert_eq!(live(&compose(1, false), &compose(1, true), S, fonts).len(), 1, "another page of candidates: in place");
    }

    // While charging, the battery fills from its level to full and again,
    // a step every 500 ms, painting only itself (on the standby screen the
    // big one under the date too); a snapshot shows its level, as the
    // frame at the start; full, or with motion off, it stays.
    #[test]
    fn the_charging_battery_fills_in_its_own_rectangle() {
        let Some(fonts) = fonts() else { return };
        let charging = |v: &Value, level: i32| patched(v, json!({"status": {"charging": true, "battery": level}}));
        let standby = json!({"screen": "system/sys.idle", "status": {"time": "10:00", "battery": 80}, "soft": ["Menu", "", "Contacts"],
            "idle": {"time": "10:00", "date": "Saturday 21 November", "operator": "Kario", "lines": ["2 messages"]}});
        let screen = (W * H) as f32;
        for (name, v, most) in [("list", charging(&list(1, "10:00"), 40), 0.002), ("standby", charging(&standby, 40), 0.01)] {
            let f = parse(&v);
            let t0 = Instant::now();
            let (mut sd, mut sr) = (State::default(), State::default());
            let mut layers = Layers::new(W, H, fonts);
            let mut at = |ms: u64| {
                let now = t0 + Duration::from_millis(ms);
                (sd.now, sr.now) = (Some(now), Some(now));
                let want = full(&f, &mut sd, fonts);
                let d = layers.update(record(&f, &mut sr, fonts), S).area();
                assert!(layers.screen() == &want, "{name}, {ms} ms: not what a full draw shows");
                (d, paint::next_tick(&sr, now).map(|t| t.duration_since(t0).as_millis()), want)
            };
            let (_, next, first) = at(0);
            assert_eq!(next, Some(500), "{name}");
            assert_eq!(at(300).0, 0, "{name}: nothing between the steps");
            for k in 1..=4u64 {
                let (d, next, _) = at(500 * k);
                assert!(d > 0 && d as f32 <= most * screen, "{name}, step {k}: painted {:.2}% of the screen", 100.0 * d as f32 / screen);
                assert_eq!(next, Some(500 * (k as u128 + 1)), "{name}");
            }
            assert!(at(2000).2 == first, "{name}: after the fourth step it starts again from its level");
            assert!(full(&f, &mut State::default(), fonts) == first, "{name}: a snapshot shows its level");
        }
        for (name, v) in [("full", charging(&list(1, "10:00"), 100)), ("not charging", list(1, "10:00")), ("motion off", patched(&charging(&list(1, "10:00"), 40), json!({"motion": "off"})))] {
            let mut st = State::default();
            st.now = if name == "motion off" { None } else { Some(Instant::now()) };
            record(&parse(&v), &mut st, fonts);
            assert_eq!(paint::next_tick(&st, Instant::now()), None, "{name}: the battery moves");
        }
    }

    // An incoming call's pulse and the scan line run by themselves from
    // the phase sent, a step at most every 16 ms, each painting only the
    // ring or the line, and only when they move by a panel pixel; a cycle
    // later the picture is the first again; a snapshot shows the phase
    // sent. Without a period (the shell steps it) nothing runs.
    #[test]
    fn an_overlays_phase_runs_by_itself() {
        let Some(fonts) = fonts() else { return };
        let call = json!({"screen": "system/incoming", "status": {"time": "10:00", "battery": 80},
            "overlay": {"kind": "incoming", "title": "Zhang San", "lines": ["Mobile"], "choices": ["Answer", "Decline"], "phase": 0.0, "period": 1500}});
        let scan = json!({"screen": "system/scan", "status": {"time": "10:00", "battery": 80},
            "overlay": {"kind": "scan", "title": "Scan", "lines": ["Put the code in the frame"], "footer": ["Back cancel"], "phase": 0.0, "period": 2400}});
        let screen = (W * H) as f32;
        for (name, v, period, most) in [("pulse", call.clone(), 1500u64, 0.07), ("scan line", scan, 2400, 0.02)] {
            let f = parse(&v);
            let t0 = Instant::now();
            let (mut sd, mut sr) = (State::default(), State::default());
            let mut layers = Layers::new(W, H, fonts);
            let (mut painted, mut still) = (0, 0);
            let mut first = None;
            let mut ms = 0;
            while ms < period + 16 {
                let now = t0 + Duration::from_millis(ms);
                (sd.now, sr.now) = (Some(now), Some(now));
                let want = full(&f, &mut sd, fonts);
                let d = layers.update(record(&f, &mut sr, fonts), S).area();
                assert!(layers.screen() == &want, "{name}, {ms} ms: not what a full draw shows");
                assert_eq!(paint::next_tick(&sr, now), Some(now + Duration::from_millis(16)), "{name}, {ms} ms");
                assert!(ms == 0 || d as f32 <= most * screen, "{name}, {ms} ms: painted {:.1}% of the screen", 100.0 * d as f32 / screen);
                if ms > 0 {
                    if d > 0 { painted += 1 } else { still += 1 }
                }
                let first = first.get_or_insert_with(|| want.clone());
                if ms >= period {
                    assert!(&want == first, "{name}: a cycle later it is where it began");
                }
                ms += 16;
            }
            assert!(painted >= 8, "{name}: {painted} steps moved it");
            assert!(full(&f, &mut State::default(), fonts) == first.unwrap(), "{name}: a snapshot shows the phase sent");
            if name == "pulse" {
                assert!(still > painted, "{name}: {still} steps moved it less than a pixel, {painted} more");
            }
        }
        let mut st = State::default();
        st.now = Some(Instant::now());
        record(&parse(&patched(&call, json!({"overlay": {"period": null}}))), &mut st, fonts);
        assert_eq!(paint::next_tick(&st, Instant::now()), None, "without a period it does not run");
    }

    /// Devices: row 1 cut in its title and subtitle, the Wi-Fi row (3)
    /// fitting, the others cut in their titles.
    fn devices(focus: i32, motion: &str) -> Value {
        let body: Vec<Value> = (0..6)
            .map(|i| match i {
                1 => json!({"kind": "item", "title": "Headset in the family car, front seat", "subtitle": "Hands-free profile, connected, battery at 80 percent", "icon": "B", "focused": i == focus}),
                3 => json!({"kind": "item", "title": "Wi-Fi", "subtitle": "Home", "focused": i == focus}),
                _ => json!({"kind": "item", "title": format!("Speaker {i} in the living room upstairs"), "focused": i == focus}),
            })
            .collect();
        json!({"screen": "app/devices", "title": "Devices", "focus": focus, "body": body, "soft": ["Options", "Open", "Back"], "motion": motion,
            "status": {"time": "10:00", "battery": 80}})
    }

    /// A marquee drawn: where its text starts, its box, its fading edges
    /// and the text.
    type Scrolled = (f32, (f32, f32, f32, f32), (f32, f32), String);

    /// The marquees a frame drew (its ops' and its body's).
    fn marquees(list: &[Rec]) -> Vec<Scrolled> {
        let mut out = Vec::new();
        for r in list {
            match &r.op {
                Op::Marquee { x, clip, fade, text, .. } => out.push((*x, *clip, *fade, text.clone())),
                Op::Body(b) => out.extend(marquees(&b.list)),
                _ => {}
            }
        }
        out
    }

    /// The panel rows of body node i as the frame just recorded laid it out.
    fn node_rows(st: &mut State, i: usize) -> (i32, i32) {
        let l = paint::take_layout(st).expect("a layout");
        let top = crate::design::STATUS_H + crate::design::TITLE_H + crate::design::GAP - l["offset"].as_f64().unwrap() as f32;
        let (y, h) = (l["nodes"][i]["y"].as_f64().unwrap() as f32, l["nodes"][i]["h"].as_f64().unwrap() as f32);
        (((top + y) * S).round() as i32, ((top + y + h) * S).round() as i32)
    }

    // A focused row's text cut with an ellipsis (37-motion.md §3): cut for
    // the dwell, then a panel pixel a step its hidden end scrolls into view
    // (both lines of the row, together), stays there for the pause and
    // shows cut again. Every step is what a full draw shows and paints only
    // inside that row (the steps to and from the cut text: inside it and
    // the text's own margin); the cut text is the snapshot's byte for byte,
    // also when one is taken mid-way. A row that fits, a row under a menu
    // (the menu's chosen entry scrolls instead) and motion off do not
    // move; reduced motion does; the focus moving on stops it at once.
    #[test]
    fn a_focused_rows_cut_text_scrolls_into_view() {
        let Some(fonts) = fonts() else { return };
        use crate::motion::{MARQUEE_DWELL_MS, MARQUEE_PAUSE_MS};
        let f = parse(&devices(1, ""));
        let snap = full(&f, &mut State::default(), fonts);
        let t0 = Instant::now();
        let step = Duration::from_nanos((1e9 / (crate::motion::MARQUEE_SPEED * S) as f64).round() as u64);
        let rows = {
            let mut st = State::default();
            record(&f, &mut st, fonts);
            node_rows(&mut st, 1)
        };
        let (mut sd, mut sr) = (State::default(), State::default());
        let mut layers = Layers::new(W, H, fonts);
        let at = |layers: &mut Layers, sd: &mut State, sr: &mut State, t: Instant, f: &Frame, check: bool| {
            (sd.now, sr.now) = (Some(t), Some(t));
            let list = record(f, sr, fonts);
            let found = marquees(&list);
            let d = layers.update(list, S).clone();
            if check {
                assert!(layers.screen() == &full(f, sd, fonts), "{:?}: not what a full draw shows", t - t0);
            }
            (d, found, paint::next_tick(sr, t), layers.screen().clone())
        };
        let (_, found, next, first) = at(&mut layers, &mut sd, &mut sr, t0, &f, true);
        assert!(first == snap && found.is_empty(), "at first the cut text, as a snapshot");
        assert_eq!(next, Some(t0 + Duration::from_millis(MARQUEE_DWELL_MS) + step), "the first step after the dwell");
        let (d, _, _, still) = at(&mut layers, &mut sd, &mut sr, t0 + Duration::from_millis(500), &f, false);
        assert!(d.screen.is_empty() && still == snap, "nothing moves in the dwell");
        let pad = (crate::design::TITLE * 0.5 * S).ceil() as i32 + 1;
        let mut t = next.unwrap();
        let (mut scrolled, mut end) = (0, None);
        loop {
            let (d, found, next, shown) = at(&mut layers, &mut sd, &mut sr, t, &f, scrolled % 8 == 0);
            let back = found.is_empty();
            let margin = if scrolled == 0 || back { pad } else { 0 };
            assert!(!d.screen.is_empty(), "step {scrolled} painted nothing");
            for r in &d.screen {
                assert!(r.y0 >= rows.0 - margin && r.y1 <= rows.1 + margin, "step {scrolled} painted {r:?} outside the row {rows:?}");
            }
            if back {
                assert!(shown == snap, "back to the cut text");
                assert_eq!(next, Some(t + Duration::from_millis(MARQUEE_DWELL_MS) + step), "and the dwell again");
                break;
            }
            assert_eq!(found.len(), 2, "both lines scroll");
            let title = found.iter().find(|m| m.3.starts_with("Headset")).unwrap();
            assert!(title.1 .1 >= rows.0 as f32 / S - 0.01 && title.1 .3 <= rows.1 as f32 / S + 0.01, "the box is the row's");
            if scrolled == 3 {
                assert!(full(&f, &mut State::default(), fonts) == snap, "a snapshot mid-way draws the cut text");
                sd.now = None;
                assert!(full(&f, &mut sd, fonts) == snap, "a snapshot with the live state, too");
            }
            if next.unwrap() - t > step {
                end = Some((t, found.clone()));
            }
            scrolled += 1;
            t = next.unwrap();
        }
        let (end_at, found) = end.expect("a pause at the end");
        assert!(scrolled >= 10, "{scrolled} steps");
        let title = found.iter().find(|m| m.3.starts_with("Headset")).unwrap();
        let shown = Canvas::new(1, 1, S, fonts).measure(crate::canvas::Face::Bold, crate::design::TITLE, &title.3);
        let right = title.1 .2;
        assert!(title.2 .1 == 0.0 && title.2 .0 > 0.0, "at the end only the left edge fades: {:?}", title.2);
        assert!(title.0 + shown <= right + 0.01 && title.0 + shown > right - 1.0 / S, "its end at the box's right edge");
        assert_eq!(t - end_at, Duration::from_millis(MARQUEE_PAUSE_MS), "the pause");

        // The focus moving on: no marquee from that frame on, none due; to
        // another cut row, its dwell starts.
        let t = t + Duration::from_millis(MARQUEE_DWELL_MS) + step * 3;
        assert_eq!(at(&mut layers, &mut sd, &mut sr, t, &f, false).1.len(), 2, "scrolling again");
        let (_, found, next, _) = at(&mut layers, &mut sd, &mut sr, t, &parse(&devices(3, "")), true);
        assert!(found.is_empty() && next.is_none(), "the focus on a row that fits: nothing scrolls");
        let (_, found, next, _) = at(&mut layers, &mut sd, &mut sr, t + step, &parse(&devices(0, "")), true);
        assert!(found.is_empty() && next == Some(t + step + Duration::from_millis(MARQUEE_DWELL_MS) + step), "another cut row: its dwell");

        let later = t + Duration::from_secs(10);
        let in_dwell = |v: Value| {
            let mut st = State::default();
            st.now = Some(later);
            let list = record(&parse(&v), &mut st, fonts);
            (paint::next_tick(&st, later), list)
        };
        assert!(in_dwell(devices(1, "off")).0.is_none(), "motion off: nothing scrolls");
        assert!(in_dwell(devices(1, "reduced")).0.is_some(), "reduced motion: it shows text that is cut");
        let items: Vec<Value> = ["Reply", "Forward to everybody in the family group chat", "Delete"].iter().enumerate().map(|(i, t)| json!({"text": t, "sel": i == 1})).collect();
        let menu = parse(&patched(&devices(1, ""), json!({"overlay": {"kind": "menu", "title": "Device", "items": items, "phase": -1}})));
        let mut st = State::default();
        st.now = Some(later);
        record(&menu, &mut st, fonts);
        st.now = Some(later + Duration::from_millis(MARQUEE_DWELL_MS) + step * 4);
        let list = record(&menu, &mut st, fonts);
        let found = marquees(&list);
        assert!(found.len() == 1 && found[0].3.starts_with("Forward") && list.iter().any(|r| r.top && matches!(r.op, Op::Marquee { .. })), "under a menu its entry scrolls, not the row: {found:?}");
    }

    // The end of a list (37-motion.md §3): a frame with a new `bump`
    // nudges the focus highlight BUMP_PX that way and back in BUMP_MS,
    // each step what a full draw shows and painting only around the
    // focused row (in a grid, the tiles it touches); after it the screen is
    // the settled one. The same bump kept by a patch does nothing; a held
    // key's next bump waits for the first to end; motion off does nothing,
    // reduced motion as standard.
    #[test]
    fn a_bump_nudges_the_highlight_and_back() {
        let Some(fonts) = fonts() else { return };
        let bump = |v: &Value, dx: i32, dy: i32, n: u64| patched(v, json!({"bump": {"dx": dx, "dy": dy, "n": n}}));
        let screen = (W * H) as f32;
        let ic = icon();
        let l = list(13, "10:00");
        for (name, a, most) in [("list, down at the end", l.clone(), 0.15), ("list, up at the top", list(0, "10:00"), 0.15), ("grid, right at the edge", grid(8, &ic), 0.18)] {
            let (dx, dy) = if name.starts_with("grid") { (1, 0) } else if name.contains("up") { (0, -1) } else { (0, 1) };
            let b = bump(&a, dx, dy, 1);
            let steps = live_from(&a, &b, S, fonts, true);
            assert!((6..=9).contains(&steps.len()), "{name}: {} steps for 120 ms at 60 Hz", steps.len());
            assert!(steps.iter().filter(|s| s.1 > 0).count() >= 4, "{name}: {steps:?}");
            for (i, &(_, d)) in steps.iter().enumerate() {
                assert!(d as f32 <= most * screen, "{name}, step {i}: painted {:.1}% of the screen", 100.0 * d as f32 / screen);
            }
            let with = |v: &Value, m: &str| patched(v, json!({"motion": m}));
            assert_eq!(live_from(&with(&a, "reduced"), &with(&b, "reduced"), S, fonts, true).len(), steps.len(), "{name}: reduced motion bumps");
            assert_eq!(live_from(&with(&a, "off"), &with(&b, "off"), S, fonts, true).len(), 1, "{name}: motion off, no bump");
            assert_eq!(live_from(&b, &patched(&b, json!({"status": {"time": "10:01"}})), S, fonts, true).len(), 1, "{name}: the same bump again");
        }
        // The last step is the frame settled, byte for byte.
        let b = parse(&bump(&l, 0, 1, 1));
        let t0 = Instant::now();
        let mut st = State::default();
        st.now = Some(t0);
        record(&parse(&l), &mut st, fonts);
        let mut layers = Layers::new(W, H, fonts);
        let ms = |n: u64| Some(t0 + Duration::from_millis(n));
        for (n, f) in [(1000, &b), (1060, &b), (1200, &b)] {
            st.now = ms(n);
            layers.update(record(f, &mut st, fonts), S);
        }
        assert!(layers.screen() == &full(&parse(&l), &mut State::default(), fonts), "back where it rests");
        // A held key: the next bump while one runs waits.
        let mut st = State::default();
        st.now = ms(0);
        record(&parse(&l), &mut st, fonts);
        st.now = ms(1000);
        record(&b, &mut st, fonts);
        st.now = ms(1033);
        record(&parse(&bump(&l, 0, 1, 2)), &mut st, fonts);
        st.now = ms(1121);
        assert!(!paint::animating(&st), "not drawn out by the second");
        st.now = ms(1140);
        record(&parse(&bump(&l, 0, 1, 3)), &mut st, fonts);
        assert!(paint::animating(&st), "the next knocks again");
    }

    // Pictures, icons and glyph outlines kept between frames draw the
    // same pixels as decoding them each time (snapshots stay the same).
    #[test]
    fn kept_pictures_and_glyphs_draw_the_same() {
        let Some(fonts) = fonts() else { return };
        let ic = icon();
        // CJK titles: Noto CJK's outlines are CFF.
        let pic = json!({"screen": "app/photo", "title": "\u{7167}\u{7247}", "focus": 0, "status": {"time": "10:00"},
            "body": [{"kind": "image", "label": "\u{508d}\u{665a}", "image": picture(), "focused": true}]});
        for v in [grid(4, &ic), pic, list(2, "10:00")] {
            let f = parse(&v);
            let draw = |keep: bool| {
                crate::canvas::CACHING.store(keep, std::sync::atomic::Ordering::Relaxed);
                let pm = full(&f, &mut State::default(), fonts);
                crate::canvas::CACHING.store(true, std::sync::atomic::Ordering::Relaxed);
                pm
            };
            let fresh = draw(false);
            assert!(draw(true) == fresh, "{}: first draw with the caches", f.screen);
            assert!(draw(true) == fresh, "{}: from the caches", f.screen);
        }
    }

    // A touch on the panel, where the frame is recorded for the layers (on
    // the CPU path and the GPU's alike), hits what it hits on the frame
    // drawn whole, as the headless renderer of the selftest's touch steps
    // draws it: each frame of the pairs, every 8 logical pixels.
    #[test]
    fn a_touch_hits_the_same_on_the_live_display() {
        let Some(fonts) = fonts() else { return };
        for (name, a, b, _) in pairs() {
            for f in [parse(&a), parse(&b)] {
                let (mut sd, mut sr) = (State::default(), State::default());
                full(&f, &mut sd, fonts);
                record(&f, &mut sr, fonts);
                for y in (0..(H as f32 / S) as u32).step_by(8) {
                    for x in (0..(W as f32 / S) as u32).step_by(8) {
                        let (x, y) = (x as f32, y as f32);
                        assert_eq!(paint::hit(&sr, x, y), paint::hit(&sd, x, y), "{name}, {}: at ({x}, {y})", f.screen);
                    }
                }
            }
        }
    }

    fn gpu() -> Option<crate::gpu::Gpu> {
        match crate::gpu::Gpu::headless(W, H) {
            Ok(g) => Some(g),
            Err(e) => {
                eprintln!("no GPU ({e}): skipped");
                None
            }
        }
    }

    /// How many pixels of two RGBA pictures differ, and by how many levels
    /// at most in a channel.
    fn differ(a: &[u8], b: &[u8]) -> (usize, u8) {
        a.chunks_exact(4).zip(b.chunks_exact(4)).fold((0, 0), |(n, m), (p, q)| {
            let d = p.iter().zip(q).map(|(x, y)| x.abs_diff(*y)).max().unwrap_or(0);
            (n + (d > 0) as usize, m.max(d))
        })
    }

    // The GPU path (gpu.rs, on llvmpipe here) on the pairs of frames: what
    // it composes is exactly what Layers::composed sums on the CPU, and
    // within gpu::LEVELS of the CPU path's screen; apart from the dialog's
    // scrim (translucent over most of the screen), the pixels that differ
    // are a few edges.
    #[test]
    fn the_gpu_composes_the_cpu_screen() {
        let Some(fonts) = fonts() else { return };
        let Some(mut g) = gpu() else { return };
        for (name, a, b, _) in pairs() {
            let (mut soft, mut split) = (Layers::new(W, H, fonts), Layers::split(W, H, fonts));
            let (mut s1, mut s2) = (State::default(), State::default());
            for f in [parse(&a), parse(&b)] {
                soft.update(record(&f, &mut s1, fonts), S);
                split.update(record(&f, &mut s2, fonts), S);
                g.compose(&mut split).unwrap();
                let got = g.read(crate::gpu::Pic::Screen(g.front()));
                assert!(got == split.composed().unwrap().data(), "{name}: the GPU's screen is not Layers::composed");
                let (n, m) = differ(&got, soft.screen().data());
                assert!(m <= crate::gpu::LEVELS, "{name}: {n} pixels differ by up to {m} levels");
                assert!(name.starts_with("dialog") || n * 1000 < (W * H) as usize, "{name}: {n} pixels differ");
            }
        }
    }

    // Steps of a scroll and of a banner sliding in and out, on the GPU path:
    // each composed screen is Layers::composed and within gpu::LEVELS of a
    // full draw. After the step the frame came in, a scroll step uploads
    // only the scroll bar (the bands move); after the banner's picture is
    // uploaded (the first step it shows, or leaves the top layer), a slide
    // step uploads nothing (the picture moves) until the last, where it
    // settles into the top layer.
    #[test]
    fn gpu_steps_move_pictures_without_uploading_them() {
        let Some(fonts) = fonts() else { return };
        let Some(mut g) = gpu() else { return };
        let l = list(1, "10:00");
        let banner = patched(&l, json!({"banner": ["New message", "Hello there"]}));
        for (name, a, b, from, most) in [
            ("scroll down", doc(0.0, 40), doc(0.15, 40), 1, 0.02),
            ("scroll up", doc(0.15, 40), doc(0.05, 40), 1, 0.02),
            ("banner comes", l.clone(), banner.clone(), 2, 0.0),
            ("banner goes", banner, l, 2, 0.0),
        ] {
            let (fa, fb) = (parse(&a), parse(&b));
            let t0 = Instant::now();
            let (mut sd, mut sr) = (State::default(), State::default());
            let mut split = Layers::split(W, H, fonts);
            (sd.now, sr.now) = (Some(t0), Some(t0));
            full(&fa, &mut sd, fonts);
            split.update(record(&fa, &mut sr, fonts), S);
            g.compose(&mut split).unwrap();
            let mut now = t0 + Duration::from_secs(1);
            for step in 0.. {
                (sd.now, sr.now) = (Some(now), Some(now));
                let want = full(&fb, &mut sd, fonts);
                split.update(record(&fb, &mut sr, fonts), S);
                g.compose(&mut split).unwrap();
                let got = g.read(crate::gpu::Pic::Screen(g.front()));
                assert!(got == split.composed().unwrap().data(), "{name}, step {step}: not Layers::composed");
                assert!(differ(&got, want.data()).1 <= crate::gpu::LEVELS, "{name}, step {step}: not a full draw");
                let last = !paint::animating(&sr);
                assert!(step < from || last || g.uploaded as f32 <= most * (W * H) as f32, "{name}, step {step}: uploaded {} pixels", g.uploaded);
                if last {
                    assert!(step >= 5, "{name}: {step} steps");
                    break;
                }
                now += crate::pace::period(crate::pace::DEFAULT_HZ);
            }
        }
    }

    /// After the same update of CPU split layers and GPU rastered ones
    /// (composed): each layer the GPU painted is the CPU's, but where it
    /// painted ops on the CPU (within raster::FALLBACK_LEVELS); the screen
    /// is Layers::composed where nothing went to the CPU, and within
    /// gpu::LEVELS of the CPU path's (soft) otherwise. Returns the ops
    /// painted on the GPU and on the CPU.
    fn rastered_as_cpu(g: &mut crate::gpu::Gpu, cpu: &Layers, ras: &Layers, soft: &Layers, name: &str) -> (usize, usize) {
        use crate::raster::{compare, Kind, FALLBACK_LEVELS};
        let fallbacks = g.raster().expect("rastered").inexact.clone();
        let ops = |list: &[Rec], top: bool, oy: i32| -> Vec<(IRect, Kind)> {
            list.iter().filter(|x| x.top == top).map(|x| (IRect::new(x.bbox.x0, x.bbox.y0 - oy, x.bbox.x1, x.bbox.y1 - oy), Kind::of(&x.op))).collect()
        };
        let (list, body) = cpu.recs();
        let mut layers = vec![(Onto::Page, cpu.page(), ops(list, false, 0)), (Onto::Top, cpu.top().unwrap(), ops(list, true, 0))];
        assert_eq!(cpu.band_numbers(), ras.band_numbers(), "{name}: other bands");
        for (k, pm) in cpu.bands() {
            layers.push((Onto::Band(*k), pm, ops(body, false, k * cpu.body().unwrap().2)));
        }
        for (onto, pm, ops) in layers {
            let on_cpu: Vec<IRect> = fallbacks.iter().filter(|f| f.0 == onto).map(|f| f.1).collect();
            let d = compare(&g.read_layer(onto).unwrap(), pm.data(), W, &ops, &on_cpu);
            assert_eq!(d.over, 0, "{name}, {onto:?}: {:?} ({:?})", d.first_over, d.kinds);
        }
        let got = g.read(crate::gpu::Pic::Screen(g.front()));
        if fallbacks.is_empty() {
            assert!(got == cpu.composed().unwrap().data(), "{name}: the screen is not Layers::composed");
        }
        let (n, m) = differ(&got, soft.screen().data());
        assert!(m <= crate::gpu::LEVELS + if fallbacks.is_empty() { 0 } else { FALLBACK_LEVELS }, "{name}: {n} pixels of the screen differ by up to {m} levels");
        let c = &g.raster().unwrap().counts;
        (c.gpu.iter().map(|k| k.0).sum(), c.cpu.iter().map(|k| k.0).sum())
    }

    // L2: the GPU paints every layer from the display list (on llvmpipe
    // here) as the CPU does, on the pairs of frames and through each step
    // of scrolls, slides and glides; all but the polygon (the charging
    // bolt) is painted by the GPU.
    #[test]
    fn the_gpu_rasters_the_cpu_layers() {
        let Some(fonts) = fonts() else { return };
        let Some(mut g) = gpu() else { return };
        for (name, a, b, _) in pairs() {
            let (mut soft, mut cpu, mut ras) = (Layers::new(W, H, fonts), Layers::split(W, H, fonts), Layers::raster(W, H, fonts));
            let (mut s1, mut s2, mut s3) = (State::default(), State::default(), State::default());
            for f in [parse(&a), parse(&b)] {
                soft.update(record(&f, &mut s1, fonts), S);
                cpu.update(record(&f, &mut s2, fonts), S);
                ras.update(record(&f, &mut s3, fonts), S);
                g.compose(&mut ras).unwrap();
                rastered_as_cpu(&mut g, &cpu, &ras, &soft, name);
            }
        }
        let l = list(1, "10:00");
        let charging = |v: &Value| patched(v, json!({"status": {"charging": true, "battery": 40}}));
        let ic = icon();
        for (name, a, b) in [
            ("scroll down", doc(0.0, 40), doc(0.15, 40)),
            ("scroll a list with pictures", long_list(1, 30), long_list(29, 30)),
            ("banner comes", l.clone(), patched(&l, json!({"banner": ["New message", "Hello there"]}))),
            ("toast", l.clone(), patched(&l, json!({"toast": "Copied"}))),
            ("menu, a tile right", grid(0, &ic), grid(1, &ic)),
            ("month, a week down", month(10), month(17)),
            ("candidates, two on", compose(0, false), compose(2, false)),
            ("charging", charging(&l), charging(&list(2, "10:00"))),
        ] {
            let (fa, fb) = (parse(&a), parse(&b));
            let t0 = Instant::now();
            let (mut soft, mut cpu, mut ras) = (Layers::new(W, H, fonts), Layers::split(W, H, fonts), Layers::raster(W, H, fonts));
            let (mut s1, mut s2, mut s3) = (State::default(), State::default(), State::default());
            let mut now = t0;
            let (mut on_gpu, mut on_cpu) = (0, 0);
            for (i, f) in std::iter::once(&fa).chain(std::iter::repeat_n(&fb, 40)).enumerate() {
                (s1.now, s2.now, s3.now) = (Some(now), Some(now), Some(now));
                soft.update(record(f, &mut s1, fonts), S);
                cpu.update(record(f, &mut s2, fonts), S);
                ras.update(record(f, &mut s3, fonts), S);
                g.compose(&mut ras).unwrap();
                let (a, b) = rastered_as_cpu(&mut g, &cpu, &ras, &soft, &format!("{name}, step {i}"));
                (on_gpu, on_cpu) = (on_gpu + a, on_cpu + b);
                g.raster().unwrap().counts = Default::default();
                if i > 0 && !paint::animating(&s3) {
                    break;
                }
                now += if i == 0 { Duration::from_secs(1) } else { crate::pace::period(crate::pace::DEFAULT_HZ) };
            }
            assert!(on_gpu > 0 && on_cpu * 20 <= on_gpu, "{name}: {on_gpu} ops on the GPU, {on_cpu} on the CPU");
        }
    }

    #[test]
    fn ops_in_another_order_damage_where_they_overlap() {
        let Some(fonts) = fonts() else { return };
        let draw = |first: Rgb, second: Rgb| {
            let mut cv = Canvas::recording(W, H, S, fonts);
            cv.clear(Rgb(0, 0, 0));
            cv.rect(10.0, 10.0, 20.0, 20.0, first);
            cv.rect(20.0, 20.0, 20.0, 20.0, second);
            cv.take()
        };
        let (red, blue) = (Rgb(255, 0, 0), Rgb(0, 0, 255));
        let mut a = draw(red, blue);
        let b = draw(blue, red);
        let d = damage(&a, &b, BOUNDS);
        assert!(d.screen.iter().any(|r| r.contains(50, 50) && r.contains(70, 70)), "{:?}", d.screen);
        // The same ops, the overlapping two swapped.
        a.swap(1, 2);
        let d = damage(&draw(red, blue), &a, BOUNDS);
        assert!(d.screen.iter().any(|r| r.contains(45, 45)), "swapped: {:?}", d.screen);
        assert!(damage(&a, &a, BOUNDS).screen.is_empty());
    }

    #[test]
    fn damage_rectangles_join_and_give_way_to_the_screen() {
        let r = |x0, y0, x1, y1| IRect::new(x0, y0, x1, y1);
        assert_eq!(merge(vec![r(0, 0, 10, 10), r(8, 8, 20, 20), r(100, 100, 110, 110)], BOUNDS), vec![r(0, 0, 20, 20), r(100, 100, 110, 110)]);
        assert_eq!(merge(vec![r(-5, -5, 10, 10), r(470, 630, 500, 700)], BOUNDS), vec![r(0, 0, 10, 10), r(470, 630, 480, 640)]);
        assert_eq!(merge(vec![r(0, 0, 400, 600)], BOUNDS), vec![BOUNDS], "most of the screen is the screen");
        let many: Vec<IRect> = (0..20).map(|i| r(i * 20, i * 20, i * 20 + 5, i * 20 + 5)).collect();
        assert_eq!(merge(many, BOUNDS), vec![r(0, 0, 385, 385)]);
        assert!(merge(vec![r(5, 5, 5, 9)], BOUNDS).is_empty());
    }
}
