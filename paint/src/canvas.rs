//! Drawing primitives in logical units: shapes with tiny-skia, text with
//! ab_glyph (CJK needs no shaping), images from frames and PNG files. A
//! canvas paints at once, or records what it is asked to draw as a list of
//! ops (the live display: layers.rs paints only where the list changed);
//! both paint each op with the same code, so the pixels are the same.

use std::cell::RefCell;
use std::collections::HashMap;
use std::hash::{Hash, Hasher};
use std::rc::Rc;
use std::sync::atomic::{AtomicBool, Ordering};

use ab_glyph::{Font, FontRef, GlyphId, PxScale, ScaleFont};
use base64::Engine as _;
use tiny_skia::{Color, FillRule, FilterQuality, Paint, PathBuilder, Pixmap, PixmapPaint, Rect, Stroke, Transform};

use crate::frame::Image;
use crate::i18n::t;

#[derive(Clone, Copy, PartialEq, Debug)]
pub struct Rgb(pub u8, pub u8, pub u8);

impl Rgb {
    pub fn hex(s: &str) -> Option<Rgb> {
        let s = s.trim_start_matches('#');
        if s.len() != 6 {
            return None;
        }
        let v = u32::from_str_radix(s, 16).ok()?;
        Some(Rgb((v >> 16) as u8, (v >> 8) as u8, v as u8))
    }
    pub fn mix(self, o: Rgb, t: f32) -> Rgb {
        let m = |a: u8, b: u8| (a as f32 + (b as f32 - a as f32) * t).round() as u8;
        Rgb(m(self.0, o.0), m(self.1, o.1), m(self.2, o.2))
    }
}

pub struct Fonts {
    pub sans: FontRef<'static>,
    pub bold: FontRef<'static>,
    pub mono: FontRef<'static>,
    pub fallback: Vec<FontRef<'static>>,
}

#[derive(Clone, Copy, PartialEq, Debug, Hash)]
pub enum Face {
    Sans,
    Bold,
    Mono,
}

thread_local! {
    static BOLD: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
}

/// Settings › Accessibility › Bold text, for the frame being drawn: the
/// regular face is drawn bold (measured so too, so text still fits).
pub fn set_bold(on: bool) {
    BOLD.with(|b| b.set(on));
}

pub fn bold_text() -> bool {
    BOLD.with(|b| b.get())
}

fn shown_face(f: Face) -> Face {
    if f == Face::Sans && bold_text() { Face::Bold } else { f }
}

const FONT_DIRS: &[&str] = &["/usr/share/fonts/noto-cjk", "/usr/share/fonts/google-noto-cjk", "/usr/share/fonts/opentype/noto"];

fn leak(path: &std::path::Path) -> Option<&'static [u8]> {
    std::fs::read(path).ok().map(|b| &*Box::leak(b.into_boxed_slice()))
}

/// Picks a face out of a collection by family name.
fn face_in(data: &'static [u8], family: &str) -> Option<FontRef<'static>> {
    let n = ttf_parser::fonts_in_collection(data).unwrap_or(1);
    for i in 0..n {
        let Ok(face) = ttf_parser::Face::parse(data, i) else { continue };
        let name = face.names().into_iter().find(|nm| nm.name_id == ttf_parser::name_id::FAMILY && nm.is_unicode());
        if name.and_then(|nm| nm.to_string()).is_some_and(|f| f == family) {
            return FontRef::try_from_slice_and_index(data, i).ok();
        }
    }
    None
}

impl Fonts {
    pub fn load() -> Result<Fonts, String> {
        let mut dirs: Vec<String> = std::env::var("PHONE_PAINT_FONTS").ok().into_iter().collect();
        dirs.extend(FONT_DIRS.iter().map(|s| s.to_string()));
        let find = |stem: &str| -> Option<&'static [u8]> {
            for d in &dirs {
                let Ok(rd) = std::fs::read_dir(d) else { continue };
                let mut names: Vec<_> = rd.flatten().map(|e| e.path()).collect();
                names.sort();
                for p in names {
                    let f = p.file_name().and_then(|f| f.to_str()).unwrap_or("");
                    if f.starts_with(stem) && (f.ends_with(".ttc") || f.ends_with(".otc")) {
                        return leak(&p);
                    }
                }
            }
            None
        };
        let regular = find("NotoSansCJK-Regular").ok_or("no NotoSansCJK-Regular font (set PHONE_PAINT_FONTS)")?;
        let bold = find("NotoSansCJK-Bold").unwrap_or(regular);
        let sans = face_in(regular, "Noto Sans CJK SC").ok_or("no Noto Sans CJK SC face")?;
        let mono = face_in(regular, "Noto Sans Mono CJK SC").unwrap_or_else(|| sans.clone());
        let boldf = face_in(bold, "Noto Sans CJK SC").unwrap_or_else(|| sans.clone());
        let mut fallback = Vec::new();
        for p in [
            "/usr/share/fonts/truetype/DejaVuSans.ttf",
            "/usr/share/fonts/dejavu/DejaVuSans.ttf",
            "/usr/share/fonts/dejavu-sans-fonts/DejaVuSans.ttf",
        ] {
            if let Some(d) = leak(std::path::Path::new(p)) {
                if let Ok(f) = FontRef::try_from_slice(d) {
                    fallback.push(f);
                }
            }
        }
        Ok(Fonts { sans, bold: boldf, mono, fallback })
    }

    fn face(&self, f: Face) -> &FontRef<'static> {
        match f {
            Face::Sans => &self.sans,
            Face::Bold => &self.bold,
            Face::Mono => &self.mono,
        }
    }

    /// The font that has the character, the requested face first.
    fn glyph(&self, f: Face, ch: char) -> (&FontRef<'static>, GlyphId) {
        let main = self.face(f);
        let id = main.glyph_id(ch);
        if id.0 != 0 || ch.is_whitespace() {
            return (main, id);
        }
        for fb in std::iter::once(&self.sans).chain(self.fallback.iter()) {
            let id = fb.glyph_id(ch);
            if id.0 != 0 {
                return (fb, id);
            }
        }
        (main, id)
    }
}

/// A rectangle of panel pixels: columns x0..x1, rows y0..y1.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub struct IRect {
    pub x0: i32,
    pub y0: i32,
    pub x1: i32,
    pub y1: i32,
}

impl IRect {
    pub fn new(x0: i32, y0: i32, x1: i32, y1: i32) -> IRect {
        IRect { x0, y0, x1, y1 }
    }

    /// The pixels a shape spanning x0..x1, y0..y1 (panel pixels, any
    /// fraction) may touch, anti-aliasing included.
    pub fn around(x0: f32, y0: f32, x1: f32, y1: f32) -> IRect {
        let (x0, x1) = (x0.min(x1), x0.max(x1));
        let (y0, y1) = (y0.min(y1), y0.max(y1));
        let c = |v: f32| v.clamp(-1e6, 1e6);
        IRect { x0: c(x0).floor() as i32 - 1, y0: c(y0).floor() as i32 - 1, x1: c(x1).ceil() as i32 + 1, y1: c(y1).ceil() as i32 + 1 }
    }

    pub fn is_empty(&self) -> bool {
        self.x0 >= self.x1 || self.y0 >= self.y1
    }

    pub fn area(&self) -> usize {
        if self.is_empty() { 0 } else { (self.x1 - self.x0) as usize * (self.y1 - self.y0) as usize }
    }

    pub fn intersect(&self, o: &IRect) -> IRect {
        IRect { x0: self.x0.max(o.x0), y0: self.y0.max(o.y0), x1: self.x1.min(o.x1), y1: self.y1.min(o.y1) }
    }

    pub fn intersects(&self, o: &IRect) -> bool {
        !self.intersect(o).is_empty()
    }

    pub fn union(&self, o: &IRect) -> IRect {
        if self.is_empty() {
            return *o;
        }
        if o.is_empty() {
            return *self;
        }
        IRect { x0: self.x0.min(o.x0), y0: self.y0.min(o.y0), x1: self.x1.max(o.x1), y1: self.y1.max(o.y1) }
    }

    pub fn grow(&self, n: i32) -> IRect {
        IRect { x0: self.x0 - n, y0: self.y0 - n, x1: self.x1 + n, y1: self.y1 + n }
    }

    pub fn contains(&self, x: i32, y: i32) -> bool {
        x >= self.x0 && x < self.x1 && y >= self.y0 && y < self.y1
    }
}

/// One thing drawn, in the canvas's logical units, colours resolved.
#[derive(Clone)]
pub enum Op {
    Clear(Rgb),
    Rect { x: f32, y: f32, w: f32, h: f32, c: Rgb, a: f32 },
    Round { x: f32, y: f32, w: f32, h: f32, r: f32, c: Rgb, a: f32 },
    Circle { cx: f32, cy: f32, r: f32, c: Rgb },
    Polygon { pts: Vec<(f32, f32)>, fill: Rgb, edge: Rgb, t: f32 },
    Fade { x: f32, y: f32, w: f32, h: f32, c: Rgb, a0: f32, a1: f32 },
    Text { face: Face, size: f32, x: f32, y: f32, text: String, c: Rgb },
    /// Text shown only in a box (x0, y0, x1, y1), its coverage fading out
    /// over the box's first fade.0 and last fade.1 logical pixels: a
    /// marquee's line scrolled (37-motion.md §3).
    Marquee { face: Face, size: f32, x: f32, y: f32, text: String, c: Rgb, clip: (f32, f32, f32, f32), fade: (f32, f32) },
    /// A picture scaled into a box; key names its pixels.
    Image { src: Rc<Pixmap>, key: u64, x: f32, y: f32, w: f32, h: f32, pixelated: bool },
    /// Rows of a premultiplied picture the screen's size (a banner drawn
    /// once) put over what is there, moved down by dy panel pixels, shown
    /// only in rows clip.0..clip.1: a layer sliding in or out.
    Layer { src: Rc<Pixmap>, key: u64, from: IRect, dy: i32, clip: (i32, i32) },
    /// The page's body, scrolled (Body).
    Body(Rc<Body>),
}

/// The page's body (37-motion.md §5): its ops where they are on the page
/// not scrolled, shown in screen rows rows.0..rows.1 moved up n rows. It
/// is painted in bands, `band` rows of the body each from its top, the
/// same way whether drawn at once or kept by the live display (layers.rs),
/// so a scroll step there copies rows of the bands it has. Body rows
/// keep.0..keep.1 are worth keeping (where the last scroll went).
pub struct Body {
    pub list: Vec<Rec>,
    pub n: i32,
    pub rows: (i32, i32),
    pub keep: (i32, i32),
    pub bg: Rgb,
    pub band: i32,
}

impl Body {
    /// The bands body rows a..b are in.
    pub fn bands(&self, a: i32, b: i32) -> std::ops::Range<i32> {
        if b <= a || self.band <= 0 {
            return 0..0;
        }
        a.div_euclid(self.band)..(b - 1).div_euclid(self.band) + 1
    }

    /// Band k painted onto pm (a band's size) where it is in clip.
    pub fn paint_band(&self, pm: &mut Pixmap, k: i32, s: f32, fonts: &'static Fonts, clip: Option<IRect>) {
        let oy = k * self.band;
        clear(pm, self.bg, clip);
        let r = clip.unwrap_or(IRect::new(0, 0, pm.width() as i32, pm.height() as i32));
        let r = IRect::new(r.x0, r.y0 + oy, r.x1, r.y1 + oy);
        for rec in self.list.iter().filter(|x| x.bbox.intersects(&r)) {
            rec.op.paint(pm, s, fonts, clip, oy);
        }
    }

    /// The rows of band k (painted) that show, copied to the screen's
    /// rows inside clip.
    pub fn show_band(&self, band: &Pixmap, k: i32, to: &mut Pixmap, clip: Option<IRect>) {
        let (w, h) = (to.width() as i32, to.height() as i32);
        let at = k * self.band - self.n;
        let mut r = IRect::new(0, self.rows.0.max(at), w.min(band.width() as i32), self.rows.1.min(at + self.band)).intersect(&IRect::new(0, 0, w, h));
        if let Some(c) = clip {
            r = r.intersect(&c);
        }
        if r.is_empty() {
            return;
        }
        let (bw, tw) = (band.width() as usize * 4, w as usize * 4);
        let (x0, x1) = (r.x0 as usize * 4, r.x1 as usize * 4);
        let (src, dst) = (band.data(), to.data_mut());
        for y in r.y0..r.y1 {
            let (si, di) = ((y - at) as usize * bw, y as usize * tw);
            dst[di + x0..di + x1].copy_from_slice(&src[si + x0..si + x1]);
        }
    }
}

/// An op as recorded: where it may paint (panel pixels), what tells it
/// from another, and whether it is in the top layer (the status bar, the
/// banner, overlays and the toast) or the page under it.
#[derive(Clone)]
pub struct Rec {
    pub op: Op,
    pub bbox: IRect,
    pub hash: u64,
    pub top: bool,
}

fn hash_f(h: &mut impl Hasher, v: &[f32]) {
    for x in v {
        x.to_bits().hash(h);
    }
}

fn hash_c(h: &mut impl Hasher, c: Rgb) {
    [c.0, c.1, c.2].hash(h);
}

impl Op {
    fn hash(&self) -> u64 {
        let mut h = std::collections::hash_map::DefaultHasher::new();
        std::mem::discriminant(self).hash(&mut h);
        match self {
            Op::Clear(c) => hash_c(&mut h, *c),
            Op::Rect { x, y, w, h: hh, c, a } => {
                hash_f(&mut h, &[*x, *y, *w, *hh, *a]);
                hash_c(&mut h, *c);
            }
            Op::Round { x, y, w, h: hh, r, c, a } => {
                hash_f(&mut h, &[*x, *y, *w, *hh, *r, *a]);
                hash_c(&mut h, *c);
            }
            Op::Circle { cx, cy, r, c } => {
                hash_f(&mut h, &[*cx, *cy, *r]);
                hash_c(&mut h, *c);
            }
            Op::Polygon { pts, fill, edge, t } => {
                for (x, y) in pts {
                    hash_f(&mut h, &[*x, *y]);
                }
                hash_f(&mut h, &[*t]);
                hash_c(&mut h, *fill);
                hash_c(&mut h, *edge);
            }
            Op::Fade { x, y, w, h: hh, c, a0, a1 } => {
                hash_f(&mut h, &[*x, *y, *w, *hh, *a0, *a1]);
                hash_c(&mut h, *c);
            }
            Op::Text { face, size, x, y, text, c } => {
                face.hash(&mut h);
                hash_f(&mut h, &[*size, *x, *y]);
                text.hash(&mut h);
                hash_c(&mut h, *c);
            }
            Op::Marquee { face, size, x, y, text, c, clip, fade } => {
                face.hash(&mut h);
                hash_f(&mut h, &[*size, *x, *y, clip.0, clip.1, clip.2, clip.3, fade.0, fade.1]);
                text.hash(&mut h);
                hash_c(&mut h, *c);
            }
            Op::Image { key, x, y, w, h: hh, pixelated, .. } => {
                key.hash(&mut h);
                hash_f(&mut h, &[*x, *y, *w, *hh]);
                pixelated.hash(&mut h);
            }
            Op::Layer { key, from, dy, clip, .. } => {
                (key, from.x0, from.y0, from.x1, from.y1, dy, clip).hash(&mut h);
            }
            Op::Body(b) => {
                (b.n, b.rows, b.band).hash(&mut h);
                hash_c(&mut h, b.bg);
            }
        }
        h.finish()
    }

    /// Paints the op onto pm, moved up oy panel rows (a band of the body);
    /// with a clip, it may leave pixels outside the clip changed in any
    /// way, and those inside are exactly as without.
    pub fn paint(&self, pm: &mut Pixmap, s: f32, fonts: &'static Fonts, clip: Option<IRect>, oy: i32) {
        let up = oy as f32;
        match self {
            Op::Clear(c) => clear(pm, *c, clip),
            Op::Rect { x, y, w, h, c, a } => {
                if let Some(r) = Rect::from_xywh(x * s, y * s - up, w * s, h * s) {
                    fill_rect(pm, r, &paint_of(*c, *a), clip);
                }
            }
            Op::Round { x, y, w, h, r, c, a } => round(pm, s, up, (*x, *y, *w, *h), *r, *c, *a),
            Op::Circle { cx, cy, r, c } => {
                if let Some(path) = circle_path(s, up, *cx, *cy, *r) {
                    pm.fill_path(&path, &paint_of(*c, 1.0), FillRule::Winding, Transform::identity(), None);
                }
            }
            Op::Polygon { pts, fill, edge, t } => polygon(pm, s, up, pts, *fill, *edge, *t),
            Op::Fade { x, y, w, h, c, a0, a1 } => fade(pm, s, up, (*x, *y, *w, *h), *c, *a0, *a1, clip),
            Op::Text { face, size, x, y, text, c } => {
                draw_text(Some((pm, clip, oy)), fonts, s, *face, *size, *x, *y, text, *c, None);
            }
            Op::Marquee { face, size, x, y, text, c, clip: b, fade: f } => {
                let r = cut_rect(*b, s);
                let cut = Cut { r: IRect::new(r.x0, r.y0 - oy, r.x1, r.y1 - oy), fade: (f.0 * s, f.1 * s) };
                draw_text(Some((pm, clip, oy)), fonts, s, *face, *size, *x, *y, text, *c, Some(cut));
            }
            Op::Image { src, key, x, y, w, h, pixelated } => blit(pm, s, up, src, *key, (*x, *y, *w, *h), *pixelated, clip),
            Op::Layer { src, from, dy, clip: rows, .. } => layer(pm, src, *from, *dy - oy, (rows.0 - oy, rows.1 - oy), clip),
            Op::Body(b) => {
                // At once: each band it shows painted and copied on.
                let rows = clip.map_or(b.rows, |c| (b.rows.0.max(c.y0), b.rows.1.min(c.y1)));
                let ks = b.bands(rows.0 + b.n, rows.1 + b.n);
                if ks.is_empty() {
                    return;
                }
                let Some(mut band) = Pixmap::new(pm.width(), b.band as u32) else { return };
                for k in ks {
                    b.paint_band(&mut band, k, s, fonts, None);
                    b.show_band(&band, k, pm, clip);
                }
            }
        }
    }

    /// The panel pixels it may touch.
    fn bbox(&self, s: f32, fonts: &Fonts, width: f32) -> IRect {
        let r = |x: f32, y: f32, w: f32, h: f32| IRect::around(x * s, y * s, (x + w) * s, (y + h) * s);
        match self {
            Op::Clear(_) => IRect::new(i32::MIN / 2, i32::MIN / 2, i32::MAX / 2, i32::MAX / 2),
            Op::Rect { x, y, w, h, .. } | Op::Round { x, y, w, h, .. } | Op::Fade { x, y, w, h, .. } => r(*x, *y, *w, *h),
            Op::Image { x, y, w, h, .. } => r(*x, *y, *w, *h),
            Op::Circle { cx, cy, r: rr, .. } => r(cx - rr, cy - rr, 2.0 * rr, 2.0 * rr),
            Op::Polygon { pts, t, .. } => {
                let (mut x0, mut y0, mut x1, mut y1) = (f32::MAX, f32::MAX, f32::MIN, f32::MIN);
                for &(x, y) in pts {
                    (x0, y0, x1, y1) = (x0.min(x), y0.min(y), x1.max(x), y1.max(y));
                }
                // A sharp corner's miter reaches out to the miter limit (4)
                // times half the edge's width.
                let m = 2.0 * t;
                r(x0 - m, y0 - m, x1 - x0 + 2.0 * m, y1 - y0 + 2.0 * m)
            }
            Op::Text { face, size, x, y, .. } => {
                // Ascent to descent, with room for marks above and below
                // and for glyphs reaching past their advance.
                let main = fonts.face(*face);
                let line = size * main.height_unscaled() / main.units_per_em().unwrap_or(1000.0);
                let pad = size * 0.5;
                r(x - pad, y - pad, width + 2.0 * pad, line + 2.0 * pad)
            }
            Op::Marquee { clip, .. } => cut_rect(*clip, s),
            Op::Layer { from, dy, clip, .. } => {
                IRect::new(from.x0, from.y0 + dy, from.x1, from.y1 + dy).intersect(&IRect::new(i32::MIN / 2, clip.0, i32::MAX / 2, clip.1))
            }
            Op::Body(b) => IRect::new(i32::MIN / 2, b.rows.0, i32::MAX / 2, b.rows.1),
        }
    }
}

fn paint_of(c: Rgb, a: f32) -> Paint<'static> {
    let mut p = Paint::default();
    p.set_color_rgba8(c.0, c.1, c.2, (a.clamp(0.0, 1.0) * 255.0) as u8);
    p.anti_alias = true;
    p
}

/// A rectangle cut to the clip and a margin around it: what the cut
/// changes is outside the clip, and tiny-skia cuts at the pixmap's edge
/// the same way.
fn cut(r: Rect, clip: Option<IRect>) -> Option<Rect> {
    let Some(c) = clip else { return Some(r) };
    let c = c.grow(2);
    Rect::from_ltrb(r.left().max(c.x0 as f32), r.top().max(c.y0 as f32), r.right().min(c.x1 as f32), r.bottom().min(c.y1 as f32))
}

fn clear(pm: &mut Pixmap, c: Rgb, clip: Option<IRect>) {
    let Some(clip) = clip else {
        pm.fill(Color::from_rgba8(c.0, c.1, c.2, 255));
        return;
    };
    let (w, h) = (pm.width() as i32, pm.height() as i32);
    let r = clip.intersect(&IRect::new(0, 0, w, h));
    if r.is_empty() {
        return;
    }
    let row: Vec<u8> = [c.0, c.1, c.2, 255].repeat((r.x1 - r.x0) as usize);
    let data = pm.data_mut();
    for y in r.y0..r.y1 {
        data[(y * w + r.x0) as usize * 4..(y * w + r.x1) as usize * 4].copy_from_slice(&row);
    }
}

fn fill_rect(pm: &mut Pixmap, r: Rect, paint: &Paint, clip: Option<IRect>) {
    let Some(r) = cut(r, clip) else { return };
    // tiny-skia 0.11's debug build asserts on a rect whose left and right
    // edges cut into two neighbouring columns, no whole one between (its
    // release build, as 0.12, fills just the two): widened a pixel to the
    // right its left column is filled the same, widened to the left its
    // right one; the other pixels those fill are put back.
    match edges(r, (pm.width(), pm.height())) {
        Some([l, t, rr, b]) if l & 0xff != 0 && rr & 0xff != 0 && rr >> 8 == (l >> 8) + 1 => {
            let (x, w) = (l >> 8, pm.width() as usize);
            let (cols, rows) = ((x - 1).max(0)..(x + 3).min(w as i32), (t >> 8)..(b + 255) >> 8);
            let at = |x: i32, y: i32| (y as usize * w + x as usize) * 4;
            for (keep, dl, dr) in [(x, 0.0, 1.0), (x + 1, -1.0, 0.0)] {
                let Some(wide) = Rect::from_ltrb(r.left() + dl, r.top(), r.right() + dr, r.bottom()) else { continue };
                let under: Vec<u8> = rows.clone().flat_map(|y| pm.data()[at(cols.start, y)..at(cols.end, y)].to_vec()).collect();
                pm.fill_rect(wide, paint, Transform::identity(), None);
                let d = pm.data_mut();
                for (y, row) in rows.clone().zip(under.chunks_exact((cols.end - cols.start) as usize * 4)) {
                    for (c, px) in cols.clone().zip(row.chunks_exact(4)).filter(|(c, _)| *c != keep) {
                        d[at(c, y)..at(c, y) + 4].copy_from_slice(px);
                    }
                }
            }
        }
        _ => pm.fill_rect(r, paint, Transform::identity(), None),
    }
}

#[allow(clippy::too_many_arguments)]
fn round(pm: &mut Pixmap, s: f32, up: f32, b: (f32, f32, f32, f32), r: f32, c: Rgb, a: f32) {
    if let Some(path) = round_path(s, up, b, r) {
        pm.fill_path(&path, &paint_of(c, a), FillRule::Winding, Transform::identity(), None);
    }
}

/// The path a rounded rectangle op fills, moved up `up` rows.
pub fn round_path(s: f32, up: f32, (x, y, w, h): (f32, f32, f32, f32), r: f32) -> Option<tiny_skia::Path> {
    if w <= 0.0 || h <= 0.0 {
        return None;
    }
    let (x, y, w, h) = (x * s, y * s - up, w * s, h * s);
    let r = (r * s).min(w / 2.0).min(h / 2.0);
    let k = 0.5523 * r;
    let mut pb = PathBuilder::new();
    pb.move_to(x + r, y);
    pb.line_to(x + w - r, y);
    pb.cubic_to(x + w - r + k, y, x + w, y + r - k, x + w, y + r);
    pb.line_to(x + w, y + h - r);
    pb.cubic_to(x + w, y + h - r + k, x + w - r + k, y + h, x + w - r, y + h);
    pb.line_to(x + r, y + h);
    pb.cubic_to(x + r - k, y + h, x, y + h - r + k, x, y + h - r);
    pb.line_to(x, y + r);
    pb.cubic_to(x, y + r - k, x + r - k, y, x + r, y);
    pb.close();
    pb.finish()
}

/// The path a circle op fills, moved up `up` rows.
pub fn circle_path(s: f32, up: f32, cx: f32, cy: f32, r: f32) -> Option<tiny_skia::Path> {
    PathBuilder::from_circle(cx * s, cy * s - up, r * s)
}

#[allow(clippy::too_many_arguments)]
fn polygon(pm: &mut Pixmap, s: f32, up: f32, pts: &[(f32, f32)], fill: Rgb, edge: Rgb, t: f32) {
    let mut pb = PathBuilder::new();
    for (i, &(x, y)) in pts.iter().enumerate() {
        if i == 0 {
            pb.move_to(x * s, y * s - up);
        } else {
            pb.line_to(x * s, y * s - up);
        }
    }
    pb.close();
    let Some(path) = pb.finish() else { return };
    pm.fill_path(&path, &paint_of(fill, 1.0), FillRule::Winding, Transform::identity(), None);
    let stroke = Stroke { width: t * s, ..Stroke::default() };
    pm.stroke_path(&path, &paint_of(edge, 1.0), &stroke, Transform::identity(), None);
}

/// A solid paint's colour as tiny-skia's pipeline blends it: premultiplied,
/// 0..255 a channel.
pub fn paint_rgba(c: Rgb, a: f32) -> [u16; 4] {
    let p = Color::from_rgba8(c.0, c.1, c.2, (a.clamp(0.0, 1.0) * 255.0) as u8).premultiply();
    [p.red(), p.green(), p.blue(), p.alpha()].map(|v| (v * 255.0 + 0.5) as u16)
}

/// A rect op's edges as tiny-skia fills them, in 1/256 pixels, cut to the
/// clip and to a picture of `size`; None where it fills nothing.
pub fn rect_edges((x, y, w, h): (f32, f32, f32, f32), s: f32, up: f32, clip: Option<IRect>, size: (u32, u32)) -> Option<[i32; 4]> {
    edges(cut(Rect::from_xywh(x * s, y * s - up, w * s, h * s)?, clip)?, size)
}

/// r's edges as tiny-skia fills it on a picture of `size`, in 1/256 pixels.
fn edges(r: Rect, size: (u32, u32)) -> Option<[i32; 4]> {
    let r = r.intersect(&Rect::from_xywh(0.0, 0.0, size.0 as f32, size.1 as f32)?)?;
    let dot8 = |v: f32| (((v * 65536.0) as i32) + 0x80) >> 8;
    let e = [dot8(r.left()), dot8(r.top()), dot8(r.right()), dot8(r.bottom())];
    (e[0] < e[2] && e[1] < e[3]).then_some(e)
}

/// The pixels whose centres are in v0..v1.
fn centres(v0: f32, v1: f32) -> (i32, i32) {
    let c = |v: f32| (v.clamp(-1e6, 1e6) - 0.5).ceil() as i32;
    (c(v0), c(v1))
}

/// The pixels a fade fills on a picture of `size` inside the clip, and
/// each of their rows' alpha.
#[allow(clippy::too_many_arguments)]
pub fn fade_rows((x, y, w, h): (f32, f32, f32, f32), s: f32, up: f32, a0: f32, a1: f32, clip: Option<IRect>, size: (u32, u32)) -> Option<(IRect, Vec<u8>)> {
    let (top, bottom) = (y * s - up, (y + h) * s - up);
    if bottom <= top {
        return None;
    }
    let (x0, x1) = centres(x * s, (x + w) * s);
    let (y0, y1) = centres(top, bottom);
    let mut r = IRect::new(x0, y0, x1, y1).intersect(&IRect::new(0, 0, size.0 as i32, size.1 as i32));
    if let Some(cl) = clip {
        r = r.intersect(&cl);
    }
    if r.is_empty() {
        return None;
    }
    let rows = (r.y0..r.y1)
        .map(|row| {
            let t = ((row as f32 + 0.5 - top) / (bottom - top)).clamp(0.0, 1.0);
            ((a0 + (a1 - a0) * t).clamp(0.0, 1.0) * 255.0 + 0.5) as u8
        })
        .collect();
    Some((r, rows))
}

/// One colour from alpha a0 at the box's top to a1 at its bottom, a row
/// at a time (each row one alpha), in integers: a gradient's per-pixel
/// floating point is most of drawing the standby screen's scrim.
#[allow(clippy::too_many_arguments)]
fn fade(pm: &mut Pixmap, s: f32, up: f32, b: (f32, f32, f32, f32), c: Rgb, a0: f32, a1: f32, clip: Option<IRect>) {
    let pw = pm.width() as i32;
    let Some((r, rows)) = fade_rows(b, s, up, a0, a1, clip, (pm.width(), pm.height())) else { return };
    let d = pm.data_mut();
    for (row, &a) in (r.y0..r.y1).zip(&rows) {
        let a = a as u32;
        if a == 0 {
            continue;
        }
        let pre = |v: u8| (v as u32 * a + 127) / 255;
        let px = [pre(c.0), pre(c.1), pre(c.2), a];
        let k = 255 - a;
        let at = (row * pw) as usize * 4;
        for p in d[at + r.x0 as usize * 4..at + r.x1 as usize * 4].chunks_exact_mut(4) {
            for i in 0..4 {
                p[i] = (px[i] + (p[i] as u32 * k + 127) / 255).min(255) as u8;
            }
        }
    }
}

/// A glyph's coverage of one pixel, in integers.
fn blend(pm: &mut Pixmap, x: i32, y: i32, c: Rgb, cov: f32) {
    let (w, h) = (pm.width() as i32, pm.height() as i32);
    if x < 0 || y < 0 || x >= w || y >= h || cov <= 0.0 {
        return;
    }
    let a = (cov.min(1.0) * 255.0 + 0.5) as u32;
    if a == 0 {
        return;
    }
    let k = 255 - a;
    let i = ((y * w + x) * 4) as usize;
    let d = pm.data_mut();
    for (j, v) in [c.0 as u32, c.1 as u32, c.2 as u32, 255].into_iter().enumerate() {
        d[i + j] = ((v * a + d[i + j] as u32 * k + 127) / 255) as u8;
    }
}

/// ab_glyph scales by the font's height (ascent − descent); sizes here
/// are em sizes, as in CSS.
fn px_scale(f: &FontRef<'static>, s: f32, size: f32) -> PxScale {
    let k = f.height_unscaled() / f.units_per_em().unwrap_or(1000.0);
    PxScale::from(size * s * k)
}

/// A glyph's font, id, scale (x, y) and the fractions of the pen and the
/// baseline, as bits.
pub type GlyphKey = (usize, u16, u32, u32, u32, u32);

/// A glyph as text lays it on (raster.rs keeps its coverage in an atlas):
/// where its coverage's top left pixel goes (moved up whole rows), its
/// size, and what tells its coverage from any other's. ab_glyph places the
/// outline by the fractions of the pen and the baseline, so the same glyph
/// at the same scale and fractions has the same coverage anywhere.
pub struct Glyph {
    pub key: GlyphKey,
    pub at: (i32, i32),
    pub size: (u32, u32),
    og: ab_glyph::OutlinedGlyph,
}

impl Glyph {
    /// Its coverage as `blend` lays it on, a byte a pixel, rows from the top.
    pub fn coverage(&self) -> Vec<u8> {
        let w = self.size.0 as usize;
        let mut out = vec![0u8; w * self.size.1 as usize];
        self.og.draw(|x, y, cov| {
            if cov > 0.0 {
                out[y as usize * w + x as usize] = (cov.min(1.0) * 255.0 + 0.5) as u8;
            }
        });
        out
    }
}

/// The glyphs of a text op as `draw_text` lays them on.
#[allow(clippy::too_many_arguments)]
pub fn glyphs(fonts: &'static Fonts, s: f32, face: Face, size: f32, x: f32, y: f32, text: &str, up: i32) -> Vec<Glyph> {
    let main = fonts.face(face);
    let base = (y * s) + main.as_scaled(px_scale(main, s, size)).ascent();
    let mut pen = x * s;
    let mut out = Vec::new();
    for ch in text.chars() {
        let (f, id) = fonts.glyph(face, ch);
        let px = px_scale(f, s, size);
        if let Some(og) = outline_glyph(f, id.with_scale_and_position(px, ab_glyph::point(pen, base))) {
            let bb = og.px_bounds();
            let key = (f as *const FontRef as usize, id.0, px.x.to_bits(), px.y.to_bits(), pen.fract().to_bits(), base.fract().to_bits());
            out.push(Glyph { key, at: (bb.min.x as i32, bb.min.y as i32 - up), size: (bb.width() as u32, bb.height() as u32), og });
        }
        pen += f.as_scaled(px).h_advance(id);
    }
    out
}

/// The panel pixels a marquee's box (logical x0, y0, x1, y1) shows text in.
fn cut_rect((x0, y0, x1, y1): (f32, f32, f32, f32), s: f32) -> IRect {
    let p = |v: f32| (v * s).clamp(-1e6, 1e6).round() as i32;
    IRect::new(p(x0), p(y0), p(x1), p(y1))
}

/// Where text shows (panel pixels), and over how many columns from each
/// side its coverage fades in (none at 0).
#[derive(Clone, Copy)]
struct Cut {
    r: IRect,
    fade: (f32, f32),
}

impl Cut {
    /// What share of a glyph's coverage shows at pixel (x, y).
    fn cover(&self, x: i32, y: i32) -> f32 {
        if !self.r.contains(x, y) {
            return 0.0;
        }
        let c = x as f32 + 0.5;
        let side = |d: f32, f: f32| if f > 0.0 { (d / f).clamp(0.0, 1.0) } else { 1.0 };
        side(c - self.r.x0 as f32, self.fade.0) * side(self.r.x1 as f32 - c, self.fade.1)
    }
}

/// Text with its top at y: painted onto the pixmap given (only the glyphs
/// that reach into the clip), moved up whole rows, shown only where cut
/// lets it, or only measured; returns the width.
#[allow(clippy::too_many_arguments)]
fn draw_text(mut to: Option<(&mut Pixmap, Option<IRect>, i32)>, fonts: &'static Fonts, s: f32, face: Face, size: f32, x: f32, y: f32, text: &str, c: Rgb, cut: Option<Cut>) -> f32 {
    let main = fonts.face(face);
    let base = (y * s) + main.as_scaled(px_scale(main, s, size)).ascent();
    let mut pen = x * s;
    for ch in text.chars() {
        let (f, id) = fonts.glyph(face, ch);
        let px = px_scale(f, s, size);
        let sf = f.as_scaled(px);
        if let Some((pm, clip, up)) = to.as_mut() {
            let g = id.with_scale_and_position(px, ab_glyph::point(pen, base));
            if let Some(og) = outline_glyph(f, g) {
                let bb = og.px_bounds();
                let (bx, by) = (bb.min.x as i32, bb.min.y as i32 - *up);
                let reach = IRect::new(bx, by, bb.max.x.ceil() as i32 + 1, bb.max.y.ceil() as i32 + 1 - *up);
                let inside = clip.is_none_or(|c| c.intersects(&reach)) && cut.is_none_or(|k| k.r.intersects(&reach));
                if inside {
                    match cut {
                        None => og.draw(|gx, gy, cov| blend(pm, bx + gx as i32, by + gy as i32, c, cov)),
                        Some(k) => og.draw(|gx, gy, cov| {
                            let (px, py) = (bx + gx as i32, by + gy as i32);
                            blend(pm, px, py, c, cov * k.cover(px, py));
                        }),
                    }
                }
            }
        }
        pen += sf.h_advance(id);
    }
    pen / s - x
}

/// A picture scaled into its box at once (the phone body around a
/// snapshot).
fn scaled(pm: &mut Pixmap, s: f32, src: &Pixmap, (x, y, w, h): (f32, f32, f32, f32), pixelated: bool) {
    let t = Transform::from_scale(w * s / src.width() as f32, h * s / src.height() as f32).post_translate(x * s, y * s);
    let quality = if pixelated { FilterQuality::Nearest } else { FilterQuality::Bilinear };
    pm.draw_pixmap(0, 0, src.as_ref(), &PixmapPaint { quality, ..PixmapPaint::default() }, t, None);
}

/// A picture as it is shown: scaled into its box once, at its size and
/// place on the panel (premultiplied pixels of the rectangle it covers).
/// Drawing it again lays these on, so a repaint, a band or a snapshot
/// scales nothing: every way of drawing goes through the same pixels.
/// The GPU (raster.rs) keeps its texture by key.
pub struct Patch {
    pub key: u64,
    pub rect: IRect,
    pub px: Vec<u8>,
}

fn patch(size: (u32, u32), src: &Pixmap, key: u64, t: Transform, quality: FilterQuality) -> Option<Rc<Patch>> {
    let pk = {
        let mut h = std::collections::hash_map::DefaultHasher::new();
        (key, size, matches!(quality, FilterQuality::Nearest)).hash(&mut h);
        hash_f(&mut h, &[t.sx, t.kx, t.ky, t.sy, t.tx, t.ty]);
        h.finish()
    };
    if caching() {
        let hit = CACHE.with(|c| {
            let mut c = c.borrow_mut();
            let frame = c.frame;
            c.patches.get_mut(&pk).map(|(p, used)| {
                *used = frame;
                p.clone()
            })
        });
        if hit.is_some() {
            return hit;
        }
    }
    let full = Rect::from_xywh(0.0, 0.0, src.width() as f32, src.height() as f32)?;
    let b = PathBuilder::from_rect(full).transform(t)?.bounds();
    let c = |v: f32| v.clamp(-1e6, 1e6);
    let r = IRect::new(c(b.left()).floor() as i32, c(b.top()).floor() as i32, c(b.right()).ceil() as i32, c(b.bottom()).ceil() as i32)
        .intersect(&IRect::new(0, 0, size.0 as i32, size.1 as i32));
    if r.is_empty() {
        return None;
    }
    let mut tmp = Pixmap::new((r.x1 - r.x0) as u32, (r.y1 - r.y0) as u32)?;
    tmp.draw_pixmap(0, 0, src.as_ref(), &PixmapPaint { quality, ..PixmapPaint::default() }, t.post_translate(-r.x0 as f32, -r.y0 as f32), None);
    let p = Rc::new(Patch { key: pk, rect: r, px: tmp.take() });
    if caching() {
        CACHE.with(|c| {
            let mut c = c.borrow_mut();
            let frame = c.frame;
            c.patches.insert(pk, (p.clone(), frame));
        });
    }
    Some(p)
}

/// The patch an image op lays on a picture of `size`, moved up `up` rows.
pub fn image_patch(size: (u32, u32), s: f32, up: f32, src: &Pixmap, key: u64, (x, y, w, h): (f32, f32, f32, f32), pixelated: bool) -> Option<Rc<Patch>> {
    let t = Transform::from_scale(w * s / src.width() as f32, h * s / src.height() as f32).post_translate(x * s, y * s - up);
    let quality = if pixelated { FilterQuality::Nearest } else { FilterQuality::Bilinear };
    patch(size, src, key, t, quality)
}

/// A frame's picture scaled into its box (through its patch), where it is
/// in the clip.
#[allow(clippy::too_many_arguments)]
fn blit(pm: &mut Pixmap, s: f32, up: f32, src: &Pixmap, key: u64, b: (f32, f32, f32, f32), pixelated: bool, clip: Option<IRect>) {
    let Some(p) = image_patch((pm.width(), pm.height()), s, up, src, key, b, pixelated) else { return };
    let mut r = p.rect;
    if let Some(c) = clip {
        r = r.intersect(&c);
    }
    if r.is_empty() {
        return;
    }
    let (w, pw) = (pm.width() as i32, (p.rect.x1 - p.rect.x0) as usize);
    let n = (r.x1 - r.x0) as usize * 4;
    let d = pm.data_mut();
    for y in r.y0..r.y1 {
        let si = ((y - p.rect.y0) as usize * pw + (r.x0 - p.rect.x0) as usize) * 4;
        let di = (y * w + r.x0) as usize * 4;
        over(&mut d[di..di + n], &p.px[si..si + n]);
    }
}

/// Premultiplied pixels laid over others, in integers (the GPU path's
/// compose shader does the same sums, gpu.rs).
pub fn over(d: &mut [u8], s: &[u8]) {
    for (dp, sp) in d.chunks_exact_mut(4).zip(s.chunks_exact(4)) {
        match sp[3] {
            0 => {}
            255 => dp.copy_from_slice(sp),
            a => {
                let k = 255 - a as u32;
                for i in 0..4 {
                    dp[i] = (sp[i] as u32 + (dp[i] as u32 * k + 127) / 255).min(255) as u8;
                }
            }
        }
    }
}

/// Rows of src (premultiplied) over pm, moved down by dy, in rows
/// rows.0..rows.1 and the clip.
pub fn layer(pm: &mut Pixmap, src: &Pixmap, from: IRect, dy: i32, rows: (i32, i32), clip: Option<IRect>) {
    let (w, h) = (pm.width() as i32, pm.height() as i32);
    if (src.width() as i32, src.height() as i32) != (w, h) {
        return;
    }
    let mut r = IRect::new(from.x0, from.y0 + dy, from.x1, from.y1 + dy).intersect(&IRect::new(0, rows.0.max(0), w, rows.1.min(h)));
    if let Some(c) = clip {
        r = r.intersect(&c);
    }
    if r.is_empty() {
        return;
    }
    let s = src.data();
    let d = pm.data_mut();
    for y in r.y0..r.y1 {
        let (si, di) = (((y - dy) * w + r.x0) as usize * 4, (y * w + r.x0) as usize * 4);
        let n = (r.x1 - r.x0) as usize * 4;
        over(&mut d[di..di + n], &s[si..si + n]);
    }
}

/// Decoded pictures and icons kept between frames, and the pictures as
/// shown (Patch): decoding a photo, reading an icon's PNG and scaling them
/// is most of a full draw on the screens that have them. Cleared of what
/// the last frames did not use.
#[derive(Default)]
struct Cache {
    images: HashMap<u64, (Rc<Pixmap>, u64)>,
    icons: HashMap<String, (u64, Option<Rc<Pixmap>>, u64)>,
    patches: HashMap<u64, (Rc<Patch>, u64)>,
    frame: u64,
}

/// What the cache may hold of pictures the last frames did not use, and
/// how many icons.
const CACHE_SPARE: usize = 16 << 20;
const CACHE_ICONS: usize = 256;

thread_local! {
    static CACHE: RefCell<Cache> = RefCell::new(Cache::default());
}

/// Off, every draw decodes again (the benchmark's "before").
pub static CACHING: AtomicBool = AtomicBool::new(true);

fn caching() -> bool {
    CACHING.load(Ordering::Relaxed)
}

/// A frame begins: what the one before last did not use may go.
pub fn next_frame() {
    CACHE.with(|c| {
        let mut c = c.borrow_mut();
        c.frame += 1;
        let keep = c.frame.saturating_sub(2);
        let bytes: usize = c.images.values().filter(|(_, f)| *f < keep).map(|(p, _)| p.data().len()).sum();
        if bytes > CACHE_SPARE {
            c.images.retain(|_, (_, f)| *f >= keep);
        }
        let bytes: usize = c.patches.values().filter(|(_, f)| *f < keep).map(|(p, _)| p.px.len()).sum();
        if bytes > CACHE_SPARE {
            c.patches.retain(|_, (_, f)| *f >= keep);
        }
        if c.icons.len() > CACHE_ICONS {
            c.icons.retain(|_, (_, _, f)| *f >= keep);
        }
    });
}

fn image_key(img: &Image) -> u64 {
    let mut h = std::collections::hash_map::DefaultHasher::new();
    (img.w, img.h).hash(&mut h);
    if img.surface.is_empty() {
        img.rgb.hash(&mut h);
    } else {
        (&img.surface, img.seq).hash(&mut h);
    }
    h.finish()
}

fn decode(img: &Image) -> Option<Pixmap> {
    let raw = if img.surface.is_empty() {
        base64::engine::general_purpose::STANDARD.decode(&img.rgb).ok()?
    } else {
        match crate::surfaces::get(&img.surface) {
            Some((sw, sh, px)) if sw == img.w && sh == img.h => px,
            _ => return None,
        }
    };
    if raw.len() < (img.w * img.h * 3) as usize {
        return None;
    }
    let mut src = Pixmap::new(img.w, img.h)?;
    for (i, px) in src.data_mut().chunks_exact_mut(4).enumerate() {
        px[0] = raw[i * 3];
        px[1] = raw[i * 3 + 1];
        px[2] = raw[i * 3 + 2];
        px[3] = 255;
    }
    Some(src)
}

fn decoded(img: &Image) -> Option<(Rc<Pixmap>, u64)> {
    let key = image_key(img);
    if !caching() {
        return decode(img).map(|p| (Rc::new(p), key));
    }
    CACHE.with(|c| {
        let mut c = c.borrow_mut();
        let frame = c.frame;
        if let Some((p, used)) = c.images.get_mut(&key) {
            *used = frame;
            return Some((p.clone(), key));
        }
        let p = Rc::new(decode(img)?);
        c.images.insert(key, (p.clone(), frame));
        Some((p, key))
    })
}

/// An icon PNG from an app package: size-checked before decoding.
fn load_icon(path: &str) -> Option<Pixmap> {
    const MAX_BYTES: u64 = 512 << 10;
    const MAX_SIDE: u32 = 256;
    if std::fs::metadata(path).map(|m| m.len() > MAX_BYTES).unwrap_or(true) {
        return None;
    }
    let p = Pixmap::load_png(path).ok()?;
    (p.width() <= MAX_SIDE && p.height() <= MAX_SIDE).then_some(p)
}

/// The icon at path, read again when the file changed (its size and time).
fn icon_of(path: &str) -> Option<(Rc<Pixmap>, u64)> {
    let stamp = {
        let m = std::fs::metadata(path).ok()?;
        let mut h = std::collections::hash_map::DefaultHasher::new();
        (path, m.len(), m.modified().ok()).hash(&mut h);
        h.finish()
    };
    if !caching() {
        return load_icon(path).map(|p| (Rc::new(p), stamp));
    }
    CACHE.with(|c| {
        let mut c = c.borrow_mut();
        let frame = c.frame;
        if let Some((st, p, used)) = c.icons.get_mut(path) {
            if *st == stamp {
                *used = frame;
                return p.clone().map(|p| (p, stamp));
            }
        }
        let p = load_icon(path).map(Rc::new);
        c.icons.insert(path.to_string(), (stamp, p.clone(), frame));
        p.map(|p| (p, stamp))
    })
}

/// Glyph outlines kept: Noto CJK's outlines are CFF, interpreted anew
/// every time a glyph is drawn otherwise.
fn outline_glyph(f: &FontRef<'static>, g: ab_glyph::Glyph) -> Option<ab_glyph::OutlinedGlyph> {
    thread_local! {
        static OUTLINES: RefCell<HashMap<(usize, u16), Option<ab_glyph::Outline>>> = RefCell::new(HashMap::new());
    }
    if !caching() {
        return f.outline_glyph(g);
    }
    const MAX: usize = 4096;
    let key = (f as *const FontRef as usize, g.id.0);
    let outline = OUTLINES.with(|o| {
        let mut o = o.borrow_mut();
        if o.len() >= MAX && !o.contains_key(&key) {
            o.clear();
        }
        o.entry(key).or_insert_with(|| f.outline(g.id)).clone()
    })?;
    let scale = f.as_scaled(g.scale).scale_factor();
    Some(ab_glyph::OutlinedGlyph::new(g, outline, scale))
}

pub struct Canvas {
    pub pm: Pixmap,
    pub s: f32,
    pub w: f32,
    pub h: f32,
    pub fonts: &'static Fonts,
    /// Secure images (payment codes) are masked in snapshots, never on the
    /// panel: the user must see them, nothing else may.
    pub mask_secure: bool,
    /// How far the focus highlight has faded in (paint's focus motion);
    /// below 0 the focused row draws none (paint slides one over it).
    pub focus_mix: f32,
    /// What is drawn from here on is in the top layer (the status bar,
    /// banner, overlays and toast), over the page.
    pub top: bool,
    /// Whether a focused field draws its text cursor (it blinks).
    pub caret: bool,
    /// How far the end of a list nudges the focused row's highlight
    /// (paint's bump), logical pixels.
    pub nudge: (f32, f32),
    /// For the row drawn next: how long it has had the focus, where its
    /// text cut with an ellipsis scrolls into view (paint's marquee);
    /// None, it shows cut. And how many panel pixels that text scrolls.
    pub marquee: Option<std::time::Duration>,
    pub marquee_run: u32,
    /// Where the focus was drawn (logical x, y, width, height): the
    /// focused row or tile, the menu entry chosen, the dialog; the
    /// magnifier zooms around it.
    pub focus_box: Option<(f32, f32, f32, f32)>,
    /// Recording: ops go here, not onto pm.
    rec: Option<Vec<Rec>>,
    /// Between begin_body and end_body: the body's ops, recorded either way.
    body: Option<Vec<Rec>>,
    size: (u32, u32),
}

impl Canvas {
    pub fn new(width: u32, height: u32, scale: f32, fonts: &'static Fonts) -> Canvas {
        let s = if scale > 0.0 { scale } else { 1.0 };
        let (width, height) = (width.max(1), height.max(1));
        Canvas {
            pm: Pixmap::new(width, height).unwrap(),
            s,
            w: width as f32 / s,
            h: height as f32 / s,
            fonts,
            mask_secure: true,
            focus_mix: 1.0,
            top: false,
            caret: true,
            nudge: (0.0, 0.0),
            marquee: None,
            marquee_run: 0,
            focus_box: None,
            rec: None,
            body: None,
            size: (width, height),
        }
    }

    /// A canvas that lists what is drawn on it (take it with `take`).
    pub fn recording(width: u32, height: u32, scale: f32, fonts: &'static Fonts) -> Canvas {
        let mut cv = Canvas::new(1, 1, scale, fonts);
        let (width, height) = (width.max(1), height.max(1));
        (cv.w, cv.h, cv.size) = (width as f32 / cv.s, height as f32 / cv.s, (width, height));
        cv.rec = Some(Vec::new());
        cv
    }

    /// What a recording canvas recorded.
    pub fn take(&mut self) -> Vec<Rec> {
        self.rec.take().unwrap_or_default()
    }

    /// The canvas's size in panel pixels.
    pub fn size(&self) -> (u32, u32) {
        self.size
    }

    /// A canvas that paints over a picture already drawn (the pointer over
    /// the frame under it).
    pub fn over(pm: Pixmap, scale: f32, fonts: &'static Fonts) -> Canvas {
        let s = if scale > 0.0 { scale } else { 1.0 };
        let (w, h) = (pm.width() as f32 / s, pm.height() as f32 / s);
        let size = (pm.width(), pm.height());
        Canvas { pm, s, w, h, fonts, mask_secure: false, focus_mix: 1.0, top: false, caret: true, nudge: (0.0, 0.0), marquee: None, marquee_run: 0, focus_box: None, rec: None, body: None, size }
    }

    fn put(&mut self, op: Op, width: f32) {
        if self.rec.is_none() && self.body.is_none() {
            op.paint(&mut self.pm, self.s, self.fonts, None, 0);
            return;
        }
        let rec = Rec { bbox: op.bbox(self.s, self.fonts, width), hash: op.hash(), top: self.top, op };
        if let Some(list) = self.body.as_mut().or(self.rec.as_mut()) {
            list.push(rec);
        }
    }

    /// What is drawn from here to end_body is the page's body, at its
    /// place on the page not scrolled.
    pub fn begin_body(&mut self) {
        self.body = Some(Vec::new());
    }

    /// The body drawn since begin_body, shown in screen rows rows.0..rows.1
    /// scrolled n rows, over bg; keep: see Body.
    pub fn end_body(&mut self, n: i32, rows: (i32, i32), keep: (i32, i32), bg: Rgb) {
        let Some(list) = self.body.take() else { return };
        let band = self.size.1 as i32;
        self.put(Op::Body(Rc::new(Body { list, n, rows, keep, bg, band })), 0.0);
    }

    /// A filled polygon with an edge of width t.
    pub fn polygon(&mut self, pts: &[(f32, f32)], fill: Rgb, edge: Rgb, t: f32) {
        self.put(Op::Polygon { pts: pts.to_vec(), fill, edge, t }, 0.0);
    }

    pub fn clear(&mut self, c: Rgb) {
        self.put(Op::Clear(c), 0.0);
    }

    pub fn rect(&mut self, x: f32, y: f32, w: f32, h: f32, c: Rgb) {
        self.rect_a(x, y, w, h, c, 1.0);
    }

    /// One colour fading from alpha a0 at the top to a1 at the bottom, in one
    /// fill (bands would add up where they meet).
    #[allow(clippy::too_many_arguments)]
    pub fn fade(&mut self, x: f32, y: f32, w: f32, h: f32, c: Rgb, a0: f32, a1: f32) {
        self.put(Op::Fade { x, y, w, h, c, a0, a1 }, 0.0);
    }

    pub fn rect_a(&mut self, x: f32, y: f32, w: f32, h: f32, c: Rgb, a: f32) {
        if Rect::from_xywh(x * self.s, y * self.s, w * self.s, h * self.s).is_some() {
            self.put(Op::Rect { x, y, w, h, c, a }, 0.0);
        }
    }

    pub fn round(&mut self, x: f32, y: f32, w: f32, h: f32, r: f32, c: Rgb) {
        self.round_a(x, y, w, h, r, c, 1.0);
    }

    #[allow(clippy::too_many_arguments)]
    pub fn round_a(&mut self, x: f32, y: f32, w: f32, h: f32, r: f32, c: Rgb, a: f32) {
        if w <= 0.0 || h <= 0.0 {
            return;
        }
        self.put(Op::Round { x, y, w, h, r, c, a }, 0.0);
    }

    /// A rounded outline of width t.
    #[allow(clippy::too_many_arguments)]
    pub fn frame(&mut self, x: f32, y: f32, w: f32, h: f32, r: f32, t: f32, c: Rgb, inner: Rgb) {
        self.round(x, y, w, h, r, c);
        self.round(x + t, y + t, w - 2.0 * t, h - 2.0 * t, (r - t).max(0.0), inner);
    }

    pub fn circle(&mut self, cx: f32, cy: f32, r: f32, c: Rgb) {
        self.put(Op::Circle { cx, cy, r, c }, 0.0);
    }

    /// Width of text at a logical size.
    pub fn measure(&self, face: Face, size: f32, text: &str) -> f32 {
        let face = shown_face(face);
        let mut wsum = 0.0;
        for ch in text.chars() {
            let (f, id) = self.fonts.glyph(face, ch);
            wsum += f.as_scaled(px_scale(f, self.s, size)).h_advance(id);
        }
        wsum / self.s
    }

    /// Draws text with its top at y; returns the width.
    pub fn text(&mut self, face: Face, size: f32, x: f32, y: f32, text: &str, c: Rgb) -> f32 {
        let face = shown_face(face);
        if self.rec.is_none() && self.body.is_none() {
            return draw_text(Some((&mut self.pm, None, 0)), self.fonts, self.s, face, size, x, y, text, c, None);
        }
        let w = draw_text(None, self.fonts, self.s, face, size, x, y, text, c, None);
        self.put(Op::Text { face, size, x, y, text: text.to_string(), c }, w);
        w
    }

    /// Text with its top at y shown only in clip (x0, y0, x1, y1), fading
    /// in over its first and last columns (fade: left, right; 0 for none).
    #[allow(clippy::too_many_arguments)]
    pub fn marquee_text(&mut self, face: Face, size: f32, x: f32, y: f32, text: &str, c: Rgb, clip: (f32, f32, f32, f32), fade: (f32, f32)) {
        self.put(Op::Marquee { face: shown_face(face), size, x, y, text: text.to_string(), c, clip, fade }, 0.0);
    }

    /// Cuts text to fit max_w, with an ellipsis.
    pub fn fit(&self, face: Face, size: f32, text: &str, max_w: f32) -> String {
        if self.measure(face, size, text) <= max_w {
            return text.to_string();
        }
        let ell = self.measure(face, size, "…");
        let mut out = String::new();
        let mut wsum = 0.0;
        for ch in text.chars() {
            let cw = self.measure(face, size, &ch.to_string());
            if wsum + cw + ell > max_w {
                break;
            }
            wsum += cw;
            out.push(ch);
        }
        out + "…"
    }

    /// Breaks text into lines of at most max_w: anywhere between CJK
    /// characters, at spaces in Latin text.
    pub fn wrap(&self, face: Face, size: f32, text: &str, max_w: f32) -> Vec<String> {
        let mut lines = Vec::new();
        for para in text.split('\n') {
            let mut line = String::new();
            let mut wsum = 0.0;
            for ch in para.chars() {
                let cw = self.measure(face, size, &ch.to_string());
                if wsum + cw > max_w && !line.is_empty() {
                    let cut = if ch.is_ascii_alphanumeric() { line.rfind(' ') } else { None };
                    match cut {
                        Some(i) if i > 0 => {
                            let rest = line[i + 1..].to_string();
                            line.truncate(i);
                            lines.push(std::mem::take(&mut line));
                            line = rest;
                            wsum = self.measure(face, size, &line);
                        }
                        _ => {
                            lines.push(std::mem::take(&mut line));
                            wsum = 0.0;
                        }
                    }
                }
                line.push(ch);
                wsum += cw;
            }
            lines.push(line);
        }
        lines
    }

    /// A frame image drawn into the box, keeping its aspect ratio; returns the
    /// height used. Secure images are masked (snapshots never show them).
    pub fn image(&mut self, img: &Image, x: f32, y: f32, max_w: f32, max_h: f32) -> f32 {
        if img.w == 0 || img.h == 0 {
            return 0.0;
        }
        let k = (max_w / img.w as f32).min(max_h / img.h as f32);
        let (w, h) = (img.w as f32 * k, img.h as f32 * k);
        self.image_in(img, (x + (max_w - w) / 2.0, y, w, h));
        h
    }

    /// A frame image drawn filling the box; secure images masked.
    pub fn image_in(&mut self, img: &Image, (x, y, w, h): (f32, f32, f32, f32)) {
        if img.secure && self.mask_secure {
            self.secure_mask(x, y, w, h);
            return;
        }
        if let Some((src, key)) = decoded(img) {
            self.put(Op::Image { src, key, x, y, w, h, pixelated: img.pixelated }, 0.0);
        }
    }

    /// What a snapshot shows in place of a secure picture or text.
    pub fn secure_mask(&mut self, x: f32, y: f32, w: f32, h: f32) {
        self.rect(x, y, w, h, Rgb(0x2a, 0x2c, 0x36));
        let mut yy = y;
        while yy < y + h {
            self.rect(x, yy, w, 1.0, Rgb(0x3a, 0x3d, 0x4a));
            yy += 4.0;
        }
        let label = t("Secure screen");
        let lw = self.measure(Face::Bold, 11.0, label);
        self.text(Face::Bold, 11.0, x + (w - lw) / 2.0, y + h / 2.0 - 7.0, label, Rgb(0xf2, 0xc9, 0x4c));
    }

    /// Where a system-drawn code (an image with `code`) goes in a box w
    /// wide and at most max_h high: a whole number of panel pixels a
    /// module, as many as fit (a barcode, whose rows are all alike, may be
    /// cut down to its image's height), left and top on whole pixels,
    /// centred across; None when one pixel a module does not fit.
    pub fn code_box(&self, img: &Image, x: f32, y: f32, w: f32, max_h: f32) -> Option<(f32, f32, f32, f32)> {
        if img.w == 0 || img.h == 0 {
            return None;
        }
        let s = self.s;
        let (bw, bh, iw, ih) = ((w * s).floor(), (max_h * s).floor(), img.w as f32, img.h as f32);
        let bar = img.code == "bar";
        let k = if bar { (bw / iw).floor() } else { (bw / iw).min(bh / ih).floor() };
        let ph = if bar { (ih * k).min(bh) } else { ih * k };
        if k < 1.0 || ph < ih {
            return None;
        }
        let pw = iw * k;
        let (px, py) = ((x * s + (bw - pw) / 2.0).round(), (y * s).round());
        Some((px / s, py / s, pw / s, ph / s))
    }

    /// A picture drawn scaled into a box, at once (not recorded).
    pub fn blit(&mut self, src: &Pixmap, x: f32, y: f32, w: f32, h: f32, pixelated: bool) {
        scaled(&mut self.pm, self.s, src, (x, y, w, h), pixelated);
    }

    /// An icon PNG from an app package: size-checked before decoding.
    pub fn icon(&mut self, path: &str, x: f32, y: f32, size: f32) -> bool {
        if path.is_empty() {
            return false;
        }
        let Some((src, key)) = icon_of(path) else { return false };
        self.put(Op::Image { src, key, x, y, w: size, h: size, pixelated: false }, 0.0);
        true
    }

    /// Rows `from` of src (a premultiplied picture the canvas's size) over
    /// what is drawn, moved down dy pixels, only in rows clip.0..clip.1.
    pub fn layer(&mut self, src: Rc<Pixmap>, key: u64, from: IRect, dy: i32, clip: (i32, i32)) {
        self.put(Op::Layer { src, key, from, dy, clip }, 0.0);
    }

    pub fn png(&self) -> Result<Vec<u8>, String> {
        self.pm.encode_png().map_err(|e| e.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // A polygon with a thick edge and sharp corners paints only inside its
    // box: the miters reach twice the edge's width out (they reached past
    // a box of one width, so the damage and the GPU missed them).
    #[test]
    fn a_polygon_paints_inside_its_box() {
        let Ok(fonts) = Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static Fonts = Box::leak(Box::new(fonts));
        // Apex angles from just over the miter limit's (29 degrees) up.
        for (half, t) in [(0.27, 1.1), (0.3, 2.0), (0.4, 0.2), (0.58, 1.5)] {
            let pts = [(10.0, 30.0), (50.0, 30.0 - 40.0 * half), (50.0, 30.0 + 40.0 * half)];
            let mut cv = Canvas::recording(200, 180, 3.0, fonts);
            cv.polygon(&pts, Rgb(255, 255, 255), Rgb(200, 0, 0), t);
            let rec = cv.take().remove(0);
            let mut pm = Pixmap::new(200, 180).unwrap();
            rec.op.paint(&mut pm, 3.0, fonts, None, 0);
            for (i, px) in pm.data().chunks_exact(4).enumerate() {
                let (x, y) = (i as i32 % 200, i as i32 / 200);
                assert!(px[3] == 0 || rec.bbox.contains(x, y), "edge {t}: ({x}, {y}) painted outside {:?}", rec.bbox);
            }
        }
    }

    // The integer blending against the floating point it replaced: a
    // glyph's coverage and the scrim's rows land within a level of it.
    #[test]
    fn integer_blending_stays_within_a_level() {
        let mut pm = Pixmap::new(1, 1).unwrap();
        for d in (0..=255u8).step_by(15) {
            for v in (0..=255u8).step_by(17) {
                for k in 0..=64 {
                    let cov = k as f32 / 64.0;
                    pm.data_mut().copy_from_slice(&[d, d, d, 255]);
                    blend(&mut pm, 0, 0, Rgb(v, v, v), cov);
                    let want = (v as f32 * cov + d as f32 * (1.0 - cov)).round();
                    assert!((pm.data()[0] as f32 - want).abs() <= 1.0, "{v} over {d} at {cov}: {} for {want}", pm.data()[0]);
                    assert_eq!(pm.data()[3], 255);
                }
            }
        }
        let h = 300;
        let mut pm = Pixmap::new(2, h).unwrap();
        for (c, d, a0, a1) in [(Rgb(0, 0, 0), 200u8, 0.57, 0.12), (Rgb(255, 255, 255), 30, 0.0, 1.0), (Rgb(90, 140, 220), 128, 0.9, 0.3)] {
            pm.fill(Color::from_rgba8(d, d, d, 255));
            fade(&mut pm, 1.0, 0.0, (0.0, 0.0, 2.0, h as f32), c, a0, a1, None);
            for row in 0..h as usize {
                let t = (row as f32 + 0.5) / h as f32;
                let a = a0 + (a1 - a0) * t;
                let want = (c.0 as f32 * a + d as f32 * (1.0 - a)).round();
                let got = pm.data()[row * 8];
                assert!((got as f32 - want).abs() <= 1.0, "row {row}: {got} for {want}");
            }
        }
    }

    // A rect whose left and right edges cut into two neighbouring columns,
    // no whole column between (tiny-skia 0.11's debug build asserted on it):
    // each column at the coverage fill_dot8 gives it, the pixels tiny-skia's
    // release build paints, at the picture's edges and inside a clip too.
    #[test]
    fn a_rect_between_two_columns_paints_both() {
        let Ok(fonts) = Fonts::load() else {
            eprintln!("no fonts: skipped");
            return;
        };
        let fonts: &'static Fonts = Box::leak(Box::new(fonts));
        let (w, h) = (160u32, 12u32);
        let paint = |r: (f32, f32, f32, f32), s: f32, c: Rgb, a: f32, clip: Option<IRect>| {
            let mut pm = Pixmap::new(w, h).unwrap();
            for (i, px) in pm.data_mut().chunks_exact_mut(4).enumerate() {
                let v = (i * 37 % 251) as u8;
                px.copy_from_slice(&[v, 255 - v, v / 2, 255]);
            }
            let before = pm.clone();
            Op::Rect { x: r.0, y: r.1, w: r.2, h: r.3, c, a }.paint(&mut pm, s, fonts, clip, 0);
            (before, pm)
        };
        let mut pm = Pixmap::new(w, h).unwrap();
        pm.fill(Color::WHITE);
        Op::Rect { x: 10.25, y: 2.5, w: 1.25, h: 4.25, c: Rgb(0, 0, 0), a: 1.0 }.paint(&mut pm, 1.0, fonts, None, 0);
        let col = |x: usize| (1..8).map(|y| pm.data()[(y * w as usize + x) * 4]).collect::<Vec<_>>();
        assert_eq!(col(10), [255, 159, 63, 63, 63, 111, 255]);
        assert_eq!(col(11), [255, 191, 127, 127, 127, 159, 255]);
        assert!([9, 12].iter().all(|&x| col(x).iter().all(|&v| v == 255)));
        let cases = [
            ((137.45854, 1.52812, 0.85160094, 9.313683), 1.0, 1.0, None),
            ((0.3, 0.2, 1.3, 11.5), 1.0, 0.6, None),
            ((158.6, 3.0, 1.2, 8.75), 1.0, 1.0, None),
            ((4.1, 1.25, 0.3, 0.5), 2.4, 0.35, None),
            ((20.7, 5.2, 0.5, 0.4), 1.0, 1.0, None),
            ((6.3, 0.4, 0.62, 3.1), 1.7, 1.0, Some(IRect::new(8, 2, 14, 9))),
        ];
        for (r, s, a, clip) in cases {
            let (before, pm) = paint(r, s, Rgb(200, 30, 90), a, clip);
            assert_ne!(before.data(), pm.data(), "{r:?} at {s}: nothing painted");
            if !cfg!(debug_assertions) {
                let mut want = before.clone();
                let r = cut(Rect::from_xywh(r.0 * s, r.1 * s, r.2 * s, r.3 * s).unwrap(), clip).unwrap();
                want.fill_rect(r, &paint_of(Rgb(200, 30, 90), a), Transform::identity(), None);
                assert_eq!(want.data(), pm.data(), "{r:?} at {s}: not tiny-skia's pixels");
            }
        }
    }
}
