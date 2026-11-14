//! Drawing primitives in logical units: shapes with tiny-skia, text with
//! ab_glyph (CJK needs no shaping), images from frames and PNG files.

use ab_glyph::{Font, FontRef, GlyphId, PxScale, ScaleFont};
use base64::Engine as _;
use tiny_skia::{
    Color, FillRule, FilterQuality, GradientStop, LinearGradient, Paint, PathBuilder, Pixmap, PixmapPaint, Point, Rect, SpreadMode, Stroke, Transform,
};

use crate::frame::Image;
use crate::i18n::t;

#[derive(Clone, Copy)]
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

#[derive(Clone, Copy, PartialEq)]
pub enum Face {
    Sans,
    Bold,
    Mono,
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

pub struct Canvas {
    pub pm: Pixmap,
    pub s: f32,
    pub w: f32,
    pub h: f32,
    pub fonts: &'static Fonts,
    /// Secure images (payment codes) are masked in snapshots, never on the
    /// panel: the user must see them, nothing else may.
    pub mask_secure: bool,
    /// How far the focus highlight has faded in (paint's focus motion).
    pub focus_mix: f32,
}

impl Canvas {
    pub fn new(width: u32, height: u32, scale: f32, fonts: &'static Fonts) -> Canvas {
        let s = if scale > 0.0 { scale } else { 1.0 };
        Canvas { pm: Pixmap::new(width.max(1), height.max(1)).unwrap(), s, w: width as f32 / s, h: height as f32 / s, fonts, mask_secure: true, focus_mix: 1.0 }
    }

    fn paint(c: Rgb, a: f32) -> Paint<'static> {
        let mut p = Paint::default();
        p.set_color_rgba8(c.0, c.1, c.2, (a.clamp(0.0, 1.0) * 255.0) as u8);
        p.anti_alias = true;
        p
    }

    /// A canvas that paints over a picture already drawn (the pointer over
    /// the frame under it).
    pub fn over(pm: Pixmap, scale: f32, fonts: &'static Fonts) -> Canvas {
        let s = if scale > 0.0 { scale } else { 1.0 };
        let (w, h) = (pm.width() as f32 / s, pm.height() as f32 / s);
        Canvas { pm, s, w, h, fonts, mask_secure: false, focus_mix: 1.0 }
    }

    /// A filled polygon with an edge of width t.
    pub fn polygon(&mut self, pts: &[(f32, f32)], fill: Rgb, edge: Rgb, t: f32) {
        let s = self.s;
        let mut pb = PathBuilder::new();
        for (i, &(x, y)) in pts.iter().enumerate() {
            if i == 0 {
                pb.move_to(x * s, y * s);
            } else {
                pb.line_to(x * s, y * s);
            }
        }
        pb.close();
        let Some(path) = pb.finish() else { return };
        self.pm.fill_path(&path, &Self::paint(fill, 1.0), FillRule::Winding, Transform::identity(), None);
        let stroke = Stroke { width: t * s, ..Stroke::default() };
        self.pm.stroke_path(&path, &Self::paint(edge, 1.0), &stroke, Transform::identity(), None);
    }

    pub fn clear(&mut self, c: Rgb) {
        self.pm.fill(tiny_skia::Color::from_rgba8(c.0, c.1, c.2, 255));
    }

    pub fn rect(&mut self, x: f32, y: f32, w: f32, h: f32, c: Rgb) {
        self.rect_a(x, y, w, h, c, 1.0);
    }

    /// One colour fading from alpha a0 at the top to a1 at the bottom, in one
    /// fill (bands would add up where they meet).
    pub fn fade(&mut self, x: f32, y: f32, w: f32, h: f32, c: Rgb, a0: f32, a1: f32) {
        let s = self.s;
        let col = |a: f32| Color::from_rgba8(c.0, c.1, c.2, (a.clamp(0.0, 1.0) * 255.0) as u8);
        let stops = vec![GradientStop::new(0.0, col(a0)), GradientStop::new(1.0, col(a1))];
        let Some(shader) = LinearGradient::new(Point::from_xy(0.0, y * s), Point::from_xy(0.0, (y + h) * s), stops, SpreadMode::Pad, Transform::identity())
        else {
            return;
        };
        let p = Paint { shader, anti_alias: false, ..Paint::default() };
        if let Some(r) = Rect::from_xywh(x * s, y * s, w * s, h * s) {
            self.pm.fill_rect(r, &p, Transform::identity(), None);
        }
    }

    pub fn rect_a(&mut self, x: f32, y: f32, w: f32, h: f32, c: Rgb, a: f32) {
        if let Some(r) = Rect::from_xywh(x * self.s, y * self.s, w * self.s, h * self.s) {
            self.pm.fill_rect(r, &Self::paint(c, a), Transform::identity(), None);
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
        let s = self.s;
        let (x, y, w, h) = (x * s, y * s, w * s, h * s);
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
        if let Some(path) = pb.finish() {
            self.pm.fill_path(&path, &Self::paint(c, a), FillRule::Winding, Transform::identity(), None);
        }
    }

    /// A rounded outline of width t.
    #[allow(clippy::too_many_arguments)]
    pub fn frame(&mut self, x: f32, y: f32, w: f32, h: f32, r: f32, t: f32, c: Rgb, inner: Rgb) {
        self.round(x, y, w, h, r, c);
        self.round(x + t, y + t, w - 2.0 * t, h - 2.0 * t, (r - t).max(0.0), inner);
    }

    pub fn circle(&mut self, cx: f32, cy: f32, r: f32, c: Rgb) {
        if let Some(path) = PathBuilder::from_circle(cx * self.s, cy * self.s, r * self.s) {
            self.pm.fill_path(&path, &Self::paint(c, 1.0), FillRule::Winding, Transform::identity(), None);
        }
    }

    fn blend(&mut self, x: i32, y: i32, c: Rgb, cov: f32) {
        let (w, h) = (self.pm.width() as i32, self.pm.height() as i32);
        if x < 0 || y < 0 || x >= w || y >= h || cov <= 0.0 {
            return;
        }
        let a = cov.min(1.0);
        let i = ((y * w + x) * 4) as usize;
        let d = self.pm.data_mut();
        for (k, v) in [c.0, c.1, c.2].into_iter().enumerate() {
            d[i + k] = (v as f32 * a + d[i + k] as f32 * (1.0 - a)).round() as u8;
        }
        d[i + 3] = (255.0 * a + d[i + 3] as f32 * (1.0 - a)).round() as u8;
    }

    /// ab_glyph scales by the font's height (ascent − descent); sizes here
    /// are em sizes, as in CSS.
    fn px(&self, f: &FontRef<'static>, size: f32) -> PxScale {
        let k = f.height_unscaled() / f.units_per_em().unwrap_or(1000.0);
        PxScale::from(size * self.s * k)
    }

    /// Width of text at a logical size.
    pub fn measure(&self, face: Face, size: f32, text: &str) -> f32 {
        let mut wsum = 0.0;
        for ch in text.chars() {
            let (f, id) = self.fonts.glyph(face, ch);
            wsum += f.as_scaled(self.px(f, size)).h_advance(id);
        }
        wsum / self.s
    }

    /// Draws text with its top at y; returns the width.
    pub fn text(&mut self, face: Face, size: f32, x: f32, y: f32, text: &str, c: Rgb) -> f32 {
        let fonts = self.fonts;
        let main = fonts.face(face);
        let base = (y * self.s) + main.as_scaled(self.px(main, size)).ascent();
        let mut pen = x * self.s;
        for ch in text.chars() {
            let (f, id) = fonts.glyph(face, ch);
            let px = self.px(f, size);
            let sf = f.as_scaled(px);
            let g = id.with_scale_and_position(px, ab_glyph::point(pen, base));
            if let Some(og) = f.outline_glyph(g) {
                let bb = og.px_bounds();
                let (bx, by) = (bb.min.x as i32, bb.min.y as i32);
                og.draw(|gx, gy, cov| self.blend(bx + gx as i32, by + gy as i32, c, cov));
            }
            pen += sf.h_advance(id);
        }
        pen / self.s - x
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
        let x = x + (max_w - w) / 2.0;
        if img.secure && self.mask_secure {
            self.rect(x, y, w, h, Rgb(0x2a, 0x2c, 0x36));
            let mut yy = y;
            while yy < y + h {
                self.rect(x, yy, w, 1.0, Rgb(0x3a, 0x3d, 0x4a));
                yy += 4.0;
            }
            let label = t("Secure screen");
            let lw = self.measure(Face::Bold, 11.0, label);
            self.text(Face::Bold, 11.0, x + (w - lw) / 2.0, y + h / 2.0 - 7.0, label, Rgb(0xf2, 0xc9, 0x4c));
            return h;
        }
        let raw = if img.surface.is_empty() {
            let Ok(raw) = base64::engine::general_purpose::STANDARD.decode(&img.rgb) else { return h };
            raw
        } else {
            match crate::surfaces::rgb(&img.surface) {
                Some((sw, sh, px)) if sw == img.w && sh == img.h => px,
                _ => return h,
            }
        };
        if raw.len() < (img.w * img.h * 3) as usize {
            return h;
        }
        let Some(mut src) = Pixmap::new(img.w, img.h) else { return h };
        for (i, px) in src.data_mut().chunks_exact_mut(4).enumerate() {
            px[0] = raw[i * 3];
            px[1] = raw[i * 3 + 1];
            px[2] = raw[i * 3 + 2];
            px[3] = 255;
        }
        self.blit(&src, x, y, w, h, img.pixelated);
        h
    }

    pub fn blit(&mut self, src: &Pixmap, x: f32, y: f32, w: f32, h: f32, pixelated: bool) {
        let s = self.s;
        let t = Transform::from_scale(w * s / src.width() as f32, h * s / src.height() as f32).post_translate(x * s, y * s);
        let paint = PixmapPaint {
            quality: if pixelated { FilterQuality::Nearest } else { FilterQuality::Bilinear },
            ..PixmapPaint::default()
        };
        self.pm.draw_pixmap(0, 0, src.as_ref(), &paint, t, None);
    }

    /// An icon PNG from an app package: size-checked before decoding.
    pub fn icon(&mut self, path: &str, x: f32, y: f32, size: f32) -> bool {
        const MAX_BYTES: u64 = 512 << 10;
        const MAX_SIDE: u32 = 256;
        if path.is_empty() || std::fs::metadata(path).map(|m| m.len() > MAX_BYTES).unwrap_or(true) {
            return false;
        }
        let Ok(p) = Pixmap::load_png(path) else { return false };
        if p.width() > MAX_SIDE || p.height() > MAX_SIDE {
            return false;
        }
        self.blit(&p, x, y, size, size, false);
        true
    }

    pub fn png(&self) -> Result<Vec<u8>, String> {
        self.pm.encode_png().map_err(|e| e.to_string())
    }
}
