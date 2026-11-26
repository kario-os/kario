//! L2 of the GPU path (PHONE_PAINT_GPU=raster; 37-motion.md §5 item 10):
//! the GPU paints the display list itself into the split layers' textures,
//! a job (layers.rs) at a time, each inside its rectangle (the scissor);
//! gpu.rs composes them as at L1. Each kind of op is painted with the
//! CPU's arithmetic, so its pixels are the CPU's byte for byte: a
//! rectangle's edges with tiny-skia's fixed-point coverage; rounded
//! rectangles and circles from the spans tiny-skia's edges give each
//! quarter-pixel row (scan.rs), covered 4 x 4 a pixel as its supersampler
//! does; text with the coverage ab_glyph gives the CPU (kept in an atlas,
//! a glyph rasterized when first shown); images and sliding layers from
//! the CPU's pixels as shown; a fade by the CPU's row alphas. Each is
//! blended in tiny-skia's or canvas.rs's integers with the pixel under
//! it, read by framebuffer fetch or, without it, from a copy of the op's
//! rectangle made just before. What it cannot paint (polygons, shapes
//! tiny-skia clips at the picture's edge, glyphs bigger than the atlas,
//! any kind PHONE_PAINT_RASTER_CPU names) is painted on the CPU over
//! transparent pixels and laid over: within FALLBACK_LEVELS.

use std::collections::HashMap;
use std::hash::{Hash, Hasher};

use glow::HasContext;
use tiny_skia::Pixmap;

use crate::canvas::{self, Fonts, IRect, Op, Rec};
use crate::gpu::{self, Target, Tex};
use crate::layers::{Job, Onto};
use crate::scan;

/// How many kinds of ops there are.
pub const KINDS: usize = 11;

/// The kinds of ops, for the switch to the CPU and the counts.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Kind {
    Clear,
    Rect,
    Round,
    Circle,
    Polygon,
    Fade,
    Text,
    Image,
    Layer,
    Body,
    Marquee,
}

impl Kind {
    pub const ALL: [Kind; KINDS] = [Kind::Clear, Kind::Rect, Kind::Round, Kind::Circle, Kind::Polygon, Kind::Fade, Kind::Text, Kind::Image, Kind::Layer, Kind::Body, Kind::Marquee];

    pub fn of(op: &Op) -> Kind {
        match op {
            Op::Clear(_) => Kind::Clear,
            Op::Rect { .. } => Kind::Rect,
            Op::Round { .. } => Kind::Round,
            Op::Circle { .. } => Kind::Circle,
            Op::Polygon { .. } => Kind::Polygon,
            Op::Fade { .. } => Kind::Fade,
            Op::Text { .. } => Kind::Text,
            Op::Image { .. } => Kind::Image,
            Op::Layer { .. } => Kind::Layer,
            Op::Body(_) => Kind::Body,
            Op::Marquee { .. } => Kind::Marquee,
        }
    }

    pub fn name(self) -> &'static str {
        ["clear", "rect", "round", "circle", "polygon", "fade", "text", "image", "layer", "body", "marquee"][self as usize]
    }
}

/// An op painted on the CPU over transparent pixels and laid over may be
/// this far from it painted straight onto the picture, in levels of a
/// channel: tiny-skia's rounding against canvas.rs's `over`, once for
/// each of a polygon's fill, edge and laying over (measured). What the
/// GPU paints itself is exact.
pub const FALLBACK_LEVELS: u8 = 3;

/// Ops and the pixels of their rectangles, by kind (Kind::ALL's order):
/// painted by the GPU, and on the CPU for it.
#[derive(Default, Clone, Debug)]
pub struct Counts {
    pub gpu: [(usize, usize); KINDS],
    pub cpu: [(usize, usize); KINDS],
}

/// How far a layer the GPU painted is from the CPU's: by the kind of the
/// last op covering them, the pixels that differ and by how many levels at
/// most; the pixels further off than they may be (exact, but for
/// FALLBACK_LEVELS for each op painted on the CPU over them), and the
/// first of those (x, y, GPU's level, CPU's level).
#[derive(Default, Clone, Debug)]
pub struct Diff {
    pub kinds: [(usize, u8); KINDS],
    pub over: usize,
    pub first_over: Option<(i32, i32, u8, u8)>,
}

impl Diff {
    pub fn add(&mut self, o: &Diff) {
        for (a, b) in self.kinds.iter_mut().zip(&o.kinds) {
            (a.0, a.1) = (a.0 + b.0, a.1.max(b.1));
        }
        self.over += o.over;
        self.first_over = self.first_over.or(o.first_over);
    }
}

/// Compares two RGBA pictures `width` wide; ops: where each op of the
/// layer may paint (in the picture's pixels) and its kind, in order;
/// on_cpu: where ops were painted on the CPU (`Raster::inexact`).
pub fn compare(gpu: &[u8], cpu: &[u8], width: u32, ops: &[(IRect, Kind)], on_cpu: &[IRect]) -> Diff {
    let mut d = Diff::default();
    let inside = |r: &IRect, x: i32, y: i32| x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1;
    for (i, (p, q)) in gpu.chunks_exact(4).zip(cpu.chunks_exact(4)).enumerate() {
        let off = p.iter().zip(q).map(|(a, b)| a.abs_diff(*b)).max().unwrap_or(0);
        if off == 0 {
            continue;
        }
        let (x, y) = ((i as u32 % width) as i32, (i as u32 / width) as i32);
        let last = ops.iter().rev().find(|(r, _)| inside(r, x, y)).map_or(Kind::Clear, |o| o.1);
        let may = on_cpu.iter().filter(|r| inside(r, x, y)).count() * FALLBACK_LEVELS as usize;
        let e = &mut d.kinds[last as usize];
        (e.0, e.1) = (e.0 + 1, e.1.max(off));
        if off as usize > may {
            d.over += 1;
            let c = p.iter().zip(q).max_by_key(|(a, b)| a.abs_diff(**b)).map_or((0, 0), |(a, b)| (*a, *b));
            d.first_over.get_or_insert((x, y, c.0, c.1));
        }
    }
    d
}

/// The atlas's side: a megabyte of coverage, some two thousand glyphs at
/// the sizes of a 480x640 screen.
const ATLAS: u32 = 1024;
/// The spans' texture, used round: a path's rows from the start of a row
/// of it, a texel a quarter-pixel row; a full-height shape of 640 rows
/// takes 3 of its rows.
const SPAN_W: u32 = 1024;
const SPAN_H: u32 = 64;
/// Pictures (images as shown, sliding layers) kept while unused, in bytes.
const PICS_SPARE: usize = 16 << 20;

const VERTEX: &str = "
attribute vec2 pos;
attribute vec4 aux;
uniform vec2 tsize;
varying vec4 v;
void main() { v = aux; gl_Position = vec4(pos / tsize * 2.0 - 1.0, 0.0, 1.0); }
";

/// The pixel under the fragment, 0..255 a channel: fetched through the
/// output in GLSL ES 3.00 (Mesa's llvmpipe gives no usable
/// gl_LastFragData in GLSL ES 1.00), or read from the copy.
const FETCH: &str = "#version 300 es
#extension GL_EXT_shader_framebuffer_fetch : require
precision highp float;
layout(location = 0) inout highp vec4 frag;
vec4 dest(vec2 p) { return floor(frag * 255.0 + 0.5); }
";
const COPIED: &str = "precision highp float;
uniform sampler2D dst;
uniform vec2 tsize;
vec4 dest(vec2 p) { return floor(texture2D(dst, (p + 0.5) / tsize) * 255.0 + 0.5); }
";

/// tiny-skia's lowp division by 255 (`div255`) and canvas.rs's rounded one;
/// a solid paint at coverage c over d as tiny-skia blends it (an opaque
/// paint is reduced to Source and lerped, a translucent one scaled and
/// laid over); canvas.rs's `over`.
const COMMON: &str = "
varying vec4 v;
uniform vec4 col;
uniform float opaque;
vec4 d255(vec4 x) { return floor((x + 255.0) / 256.0); }
vec4 r255(vec4 x) { vec4 t = x + 128.0; return floor((t + floor(t / 256.0)) / 256.0); }
vec4 put(vec4 c) { return (c + 0.25) / 255.0; }
vec4 fill(float c, vec4 d) {
    if (opaque > 0.5) return d255(d * (255.0 - c) + col * c);
    vec4 s = d255(col * c);
    return min(s + d255(d * (255.0 - s.a)), 255.0);
}
vec4 over(vec4 s, vec4 d) { return min(s + r255(d * (255.0 - s.a)), 255.0); }
vec2 at() { return floor(v.xy + 0.5); }
";

/// A rectangle's coverage as tiny-skia's fill_dot8 gives it, from its
/// edges in 1/256 pixels (l, t, r, b).
const RECT: &str = "
uniform vec4 e;
float mul(float a, float n) { return floor(a * n / 256.0); }
float line(float x, float a) {
    float left = floor(e.x / 256.0);
    if (left == floor((e.z - 1.0) / 256.0)) return x == left ? mul(a, e.z - e.x) : 0.0;
    float lf = e.x - left * 256.0;
    if (lf != 0.0) {
        if (x == left) return mul(a, 256.0 - lf);
        left += 1.0;
    }
    float right = floor(e.z / 256.0);
    if (x >= left && x < right) return a;
    float rf = e.z - right * 256.0;
    return rf != 0.0 && x == right ? mul(a, rf) : 0.0;
}
float cover(vec2 p) {
    float top = floor(e.y / 256.0);
    if (top == floor((e.w - 1.0) / 256.0)) return p.y == top ? line(p.x, e.w - e.y - 1.0) : 0.0;
    float tf = e.y - top * 256.0;
    if (tf != 0.0) {
        if (p.y == top) return line(p.x, 256.0 - tf);
        top += 1.0;
    }
    float bottom = floor(e.w / 256.0);
    if (p.y >= top && p.y < bottom) {
        float left = floor(e.x / 256.0);
        if (left == floor((e.z - 1.0) / 256.0)) return p.x == left ? e.z - e.x - 1.0 : 0.0;
        float lf = e.x - left * 256.0;
        if (lf != 0.0) {
            if (p.x == left) return 256.0 - lf;
            left += 1.0;
        }
        float right = floor(e.z / 256.0);
        if (p.x >= left && p.x < right) return 255.0;
        float rf = e.z - right * 256.0;
        return rf != 0.0 && p.x == right ? rf : 0.0;
    }
    float bf = e.w - bottom * 256.0;
    return bf != 0.0 && p.y == bottom ? line(p.x, bf) : 0.0;
}
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    float c = cover(p);
    if (c == 0.0) discard;
    gl_FragColor = put(fill(c, dest(p)));
}
";

/// A path's coverage as tiny-skia's supersampler gives it, from its spans
/// (scan.rs) on quarter-pixel rows rows.x .. rows.x + rows.y, kept from
/// texel rows.z of spans (left and right, 16 bits each): a quarter pixel
/// of a row is 16 levels, a row covering the whole pixel 64, the last 63.
const SPANS: &str = "
uniform sampler2D spans;
uniform vec3 rows;
float cover(vec2 p) {
    float sum = 0.0;
    for (int j = 0; j < 4; j++) {
        float i = p.y * 4.0 + float(j) - rows.x;
        if (i < 0.0 || i >= rows.y) continue;
        float k = rows.z + i;
        vec4 t = floor(texture2D(spans, (vec2(mod(k, SPAN_W), floor(k / SPAN_W)) + 0.5) / vec2(SPAN_W, SPAN_H)) * 255.0 + 0.5);
        float n = clamp(min(t.b * 256.0 + t.a, p.x * 4.0 + 4.0) - max(t.r * 256.0 + t.g, p.x * 4.0), 0.0, 4.0);
        sum += n == 4.0 ? (j == 3 ? 63.0 : 64.0) : 16.0 * n;
    }
    return sum;
}
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    float c = cover(p);
    if (c == 0.0) discard;
    gl_FragColor = put(fill(c, dest(p)));
}
";

/// Glyphs from the atlas (at() from the pixel to its coverage), blended as
/// canvas.rs's `blend`.
const GLYPH: &str = "
uniform sampler2D atlas;
uniform vec2 asize;
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    float a = floor(texture2D(atlas, (p + at() + 0.5) / asize).r * 255.0 + 0.5);
    if (a == 0.0) discard;
    gl_FragColor = put(r255(col * a + dest(p) * (255.0 - a)));
}
";

/// Premultiplied pixels laid over (canvas.rs's `over`): a texture's (at()
/// from the pixel to its texel), or col at a fade row's alpha (v.z).
const OVER: &str = "
uniform sampler2D src;
uniform vec2 ssize;
uniform float mode;
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    vec4 s;
    if (mode < 0.5) {
        s = floor(texture2D(src, (p + at() + 0.5) / ssize) * 255.0 + 0.5);
    } else {
        float a = floor(v.z + 0.5);
        s = vec4(r255(col * a).rgb, a);
    }
    if (s.a == 0.0) discard;
    gl_FragColor = put(over(s, dest(p)));
}
";

/// A rectangle to cover and what its fragments get in `aux`.
type Quad = (IRect, [f32; 4]);

struct Shelf {
    y: u32,
    /// The rows it was given, and the height of the glyphs on it now.
    cap: u32,
    h: u32,
    x: u32,
    last: u64,
}

/// Glyph coverage, a byte a pixel, on shelves side by side from the top;
/// when full, the run of shelves used longest ago that is tall enough
/// goes (never one the op being painted uses), made one shelf.
struct Atlas {
    tex: Tex,
    shelves: Vec<Shelf>,
    next_y: u32,
    /// Each glyph's place and its shelf's top row.
    at: HashMap<canvas::GlyphKey, (u32, u32, u32)>,
    /// Glyph coverages rasterized and put in.
    rasterized: usize,
}

impl Atlas {
    /// Where glyph g's coverage is, put there if new.
    unsafe fn get(&mut self, gl: &glow::Context, g: &canvas::Glyph, now: u64) -> Option<(u32, u32)> {
        if let Some(&(x, y, sy)) = self.at.get(&g.key) {
            if let Some(s) = self.shelves.iter_mut().find(|s| s.y == sy) {
                s.last = now;
            }
            return Some((x, y));
        }
        let (w, h) = g.size;
        let (x, y) = self.place(w, h, now)?;
        gl.bind_texture(glow::TEXTURE_2D, Some(self.tex.id));
        gl.pixel_store_i32(glow::UNPACK_ALIGNMENT, 1);
        gl.tex_sub_image_2d(glow::TEXTURE_2D, 0, x as i32, y as i32, w as i32, h as i32, glow::LUMINANCE, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(Some(&g.coverage())));
        gl.pixel_store_i32(glow::UNPACK_ALIGNMENT, 4);
        self.at.insert(g.key, (x, y, self.shelves.iter().find(|s| s.y <= y && y < s.y + s.cap).map_or(y, |s| s.y)));
        self.rasterized += 1;
        Some((x, y))
    }

    fn place(&mut self, w: u32, h: u32, now: u64) -> Option<(u32, u32)> {
        if w > ATLAS || h > ATLAS {
            return None;
        }
        let h = h.div_ceil(4) * 4;
        let i = match self.shelves.iter().position(|s| s.h == h && s.x + w <= ATLAS) {
            Some(i) => i,
            None if self.next_y + h <= ATLAS => {
                self.shelves.push(Shelf { y: self.next_y, cap: h, h, x: 0, last: now });
                self.next_y += h;
                self.shelves.len() - 1
            }
            None => {
                let mut best: Option<(usize, usize, u64)> = None;
                for i in 0..self.shelves.len() {
                    let (mut cap, mut newest) = (0, 0);
                    for j in i..self.shelves.len() {
                        if self.shelves[j].last >= now {
                            break;
                        }
                        (cap, newest) = (cap + self.shelves[j].cap, newest.max(self.shelves[j].last));
                        if cap >= h {
                            if best.is_none_or(|b| newest < b.2) {
                                best = Some((i, j + 1, newest));
                            }
                            break;
                        }
                    }
                }
                let (i, j, _) = best?;
                let gone: Vec<u32> = self.shelves[i..j].iter().map(|s| s.y).collect();
                self.at.retain(|_, (_, _, sy)| !gone.contains(sy));
                let cap = self.shelves[i..j].iter().map(|s| s.cap).sum();
                let y = self.shelves[i].y;
                self.shelves.splice(i..j, [Shelf { y, cap, h, x: 0, last: now }]);
                i
            }
        };
        let s = &mut self.shelves[i];
        let at = (s.x, s.y);
        (s.x, s.last) = (s.x + w, now);
        Some(at)
    }
}

pub struct Raster {
    rect: glow::Program,
    path: glow::Program,
    glyph: glow::Program,
    over: glow::Program,
    buf: glow::Buffer,
    spans: Tex,
    /// The next row of spans to fill.
    span_row: u32,
    /// Framebuffer fetch reads the pixel under; else dst holds a copy.
    fetch: bool,
    dst: Tex,
    row_length: bool,
    atlas: Atlas,
    /// Pictures by key (images as shown, layers' rows): texture, frame used.
    pics: HashMap<u64, (Tex, u64)>,
    frame: u64,
    serial: u64,
    /// Ops painted on the CPU: drawn here, uploaded into loose.
    scratch: Option<Pixmap>,
    loose: Tex,
    /// Kinds always painted on the CPU.
    pub cpu: Vec<Kind>,
    pub counts: Counts,
    /// Where the pictures hold ops painted on the CPU (within
    /// FALLBACK_LEVELS of the CPU's pixels): the picture, the rectangle;
    /// gone once a job paints over all of it.
    pub inexact: Vec<(Onto, IRect)>,
}

impl Raster {
    /// For targets up to width x height; fetch: read the pixel under by
    /// framebuffer fetch (else by copies).
    pub unsafe fn new(gl: &glow::Context, width: u32, height: u32, fetch: bool, row_length: bool) -> Result<Raster, String> {
        let es3 = |src: &str, put: &str| src.replace("attribute ", "in ").replace("varying ", put).replace("texture2D(", "texture(").replace("gl_FragColor", "frag");
        let vertex = if fetch { format!("#version 300 es\n{}", es3(VERTEX, "out ")) } else { VERTEX.to_string() };
        let mut progs = Vec::new();
        let spans = SPANS.replace("SPAN_W", &format!("{SPAN_W}.0")).replace("SPAN_H", &format!("{SPAN_H}.0"));
        for fs in [RECT, &spans, GLYPH, OVER] {
            let fragment = if fetch { format!("{FETCH}{}", es3(&format!("{COMMON}{fs}"), "in ")) } else { format!("{COPIED}{COMMON}{fs}") };
            progs.push(gpu::program_from(gl, &vertex, &fragment, &["pos", "aux"])?);
        }
        let buf = gl.create_buffer()?;
        let atlas = gpu::texture_of(gl, ATLAS, ATLAS, glow::LUMINANCE)?;
        let cpu = std::env::var("PHONE_PAINT_RASTER_CPU").unwrap_or_default().split(',').filter_map(|n| Kind::ALL.into_iter().find(|k| k.name() == n.trim())).collect();
        Ok(Raster {
            rect: progs[0],
            path: progs[1],
            glyph: progs[2],
            over: progs[3],
            buf,
            spans: gpu::texture_of(gl, SPAN_W, SPAN_H, glow::RGBA)?,
            span_row: 0,
            fetch,
            dst: if fetch { gpu::texture_of(gl, 1, 1, glow::RGBA)? } else { gpu::texture_of(gl, width, height, glow::RGBA)? },
            row_length,
            atlas: Atlas { tex: atlas, shelves: Vec::new(), next_y: 0, at: HashMap::new(), rasterized: 0 },
            pics: HashMap::new(),
            frame: 0,
            serial: 0,
            scratch: None,
            loose: gpu::texture_of(gl, width, height, glow::RGBA)?,
            cpu,
            counts: Counts::default(),
            inexact: Vec::new(),
        })
    }

    /// Whether the pixel under is read by framebuffer fetch.
    pub fn fetches(&self) -> bool {
        self.fetch
    }

    /// Glyph coverages rasterized into the atlas so far.
    pub fn rasterized(&self) -> usize {
        self.atlas.rasterized
    }

    /// Paints the jobs into their targets, in order. Leaves the scissor
    /// off and the second vertex attribute disabled; the array buffer and
    /// attribute 0 are this one's.
    pub unsafe fn run(&mut self, gl: &glow::Context, jobs: &[Job], fonts: &'static Fonts, target: &dyn Fn(Onto) -> Option<Target>) -> Result<(), String> {
        self.frame += 1;
        gl.enable(glow::SCISSOR_TEST);
        gl.bind_buffer(glow::ARRAY_BUFFER, Some(self.buf));
        gl.enable_vertex_attrib_array(0);
        gl.vertex_attrib_pointer_f32(0, 2, glow::FLOAT, false, 24, 0);
        gl.enable_vertex_attrib_array(1);
        gl.vertex_attrib_pointer_f32(1, 4, glow::FLOAT, false, 24, 8);
        let res = (|| -> Result<(), String> {
            for job in jobs {
                let Some(t) = target(job.onto) else { continue };
                let size = (t.tex.w, t.tex.h);
                let rect = job.rect.intersect(&IRect::new(0, 0, size.0 as i32, size.1 as i32));
                if rect.is_empty() {
                    continue;
                }
                self.inexact.retain(|(o, r)| *o != job.onto || r.intersect(&rect) != *r);
                gl.bind_framebuffer(glow::FRAMEBUFFER, Some(t.fb));
                gl.viewport(0, 0, size.0 as i32, size.1 as i32);
                scissor(gl, rect);
                let c = job.base.map_or([0.0; 4], |c| [c.0 as f32 / 255.0, c.1 as f32 / 255.0, c.2 as f32 / 255.0, 1.0]);
                gl.clear_color(c[0], c[1], c[2], c[3]);
                gl.clear(glow::COLOR_BUFFER_BIT);
                for rec in &job.ops {
                    self.op(gl, rec, job, rect, size, fonts)?;
                }
            }
            Ok(())
        })();
        gl.disable(glow::SCISSOR_TEST);
        gl.disable_vertex_attrib_array(1);
        gl.active_texture(glow::TEXTURE0);
        self.evict(gl);
        res
    }

    unsafe fn op(&mut self, gl: &glow::Context, rec: &Rec, job: &Job, rect: IRect, size: (u32, u32), fonts: &'static Fonts) -> Result<(), String> {
        let b = rec.bbox;
        let region = IRect::new(b.x0, b.y0 - job.oy, b.x1, b.y1 - job.oy).intersect(&rect);
        if region.is_empty() {
            return Ok(());
        }
        let kind = Kind::of(&rec.op);
        let on_gpu = !self.cpu.contains(&kind) && self.paint(gl, &rec.op, job, region, size, fonts)?;
        if !on_gpu {
            self.fallback(gl, rec, job, region, size, fonts)?;
            self.inexact.push((job.onto, region));
        }
        let c = if on_gpu { &mut self.counts.gpu[kind as usize] } else { &mut self.counts.cpu[kind as usize] };
        (c.0, c.1) = (c.0 + 1, c.1 + region.area());
        Ok(())
    }

    /// Paints op inside region on the GPU; false if it cannot.
    unsafe fn paint(&mut self, gl: &glow::Context, op: &Op, job: &Job, region: IRect, size: (u32, u32), fonts: &'static Fonts) -> Result<bool, String> {
        let (s, oy, up) = (job.s, job.oy, job.oy as f32);
        match op {
            Op::Clear(c) => {
                scissor(gl, region);
                gl.clear_color(c.0 as f32 / 255.0, c.1 as f32 / 255.0, c.2 as f32 / 255.0, 1.0);
                gl.clear(glow::COLOR_BUFFER_BIT);
            }
            Op::Rect { x, y, w, h, c, a } => {
                let Some(e) = canvas::rect_edges((*x, *y, *w, *h), s, up, Some(job.rect), size) else { return Ok(true) };
                let r = IRect::new(e[0] >> 8, e[1] >> 8, (e[2] + 255) >> 8, (e[3] + 255) >> 8).intersect(&region);
                let col = canvas::paint_rgba(*c, *a);
                let whole = col[3] == 255 && e.iter().all(|v| v & 0xff == 0);
                if !r.is_empty() {
                    self.begin(gl, self.rect, r, size, !whole);
                    uniform4(gl, self.rect, "e", e.map(|v| v as f32));
                    self.colour(gl, self.rect, col);
                    self.quads(gl, &[(r, [0.0; 4])]);
                }
            }
            Op::Round { x, y, w, h, r, c, a } => return self.shape(gl, canvas::round_path(s, up, (*x, *y, *w, *h), *r), canvas::paint_rgba(*c, *a), region, size),
            Op::Circle { cx, cy, r, c } => return self.shape(gl, canvas::circle_path(s, up, *cx, *cy, *r), canvas::paint_rgba(*c, 1.0), region, size),
            Op::Fade { x, y, w, h, c, a0, a1 } => {
                let Some((r, rows)) = canvas::fade_rows((*x, *y, *w, *h), s, up, *a0, *a1, Some(job.rect), size) else { return Ok(true) };
                let qs: Vec<Quad> = (r.y0..r.y1)
                    .zip(&rows)
                    .filter(|(_, &a)| a > 0)
                    .map(|(row, &a)| (IRect::new(r.x0, row, r.x1, row + 1).intersect(&region), [0.0, 0.0, a as f32, 0.0]))
                    .filter(|(q, _)| !q.is_empty())
                    .collect();
                if let Some(cover) = bounds(&qs) {
                    self.begin(gl, self.over, cover, size, true);
                    uniform1(gl, self.over, "mode", 1.0);
                    self.colour(gl, self.over, [c.0 as u16, c.1 as u16, c.2 as u16, 255]);
                    self.quads(gl, &qs);
                }
            }
            Op::Text { face, size: sz, x, y, text, c } => {
                self.serial += 1;
                let now = self.serial;
                let mut qs = Vec::new();
                for g in canvas::glyphs(fonts, s, *face, *sz, *x, *y, text, oy) {
                    let r = IRect::new(g.at.0, g.at.1, g.at.0 + g.size.0 as i32, g.at.1 + g.size.1 as i32).intersect(&region);
                    if r.is_empty() {
                        continue;
                    }
                    let Some((ax, ay)) = self.atlas.get(gl, &g, now) else { return Ok(false) };
                    qs.push((r, [(ax as i32 - g.at.0) as f32, (ay as i32 - g.at.1) as f32, 0.0, 0.0]));
                }
                let p = self.glyph;
                let groups = if self.fetch { vec![qs] } else { apart(qs) };
                for g in groups {
                    let Some(cover) = bounds(&g) else { continue };
                    self.begin(gl, p, cover, size, true);
                    self.colour(gl, p, [c.0 as u16, c.1 as u16, c.2 as u16, 255]);
                    gl.bind_texture(glow::TEXTURE_2D, Some(self.atlas.tex.id));
                    uniform1i(gl, p, "atlas", 0);
                    uniform2(gl, p, "asize", ATLAS as f32, ATLAS as f32);
                    self.quads(gl, &g);
                }
            }
            Op::Image { src, key, x, y, w, h, pixelated } => {
                let Some(pt) = canvas::image_patch(size, s, up, src, *key, (*x, *y, *w, *h), *pixelated) else { return Ok(true) };
                let r = pt.rect.intersect(&region);
                if !r.is_empty() {
                    let (pw, ph) = ((pt.rect.x1 - pt.rect.x0) as u32, (pt.rect.y1 - pt.rect.y0) as u32);
                    let t = self.picture(gl, pt.key, pw, ph, || pt.px.clone())?;
                    self.lay(gl, t, (pt.rect.x0, pt.rect.y0), r, size);
                }
            }
            Op::Layer { src, key, from, dy, clip: rows } => {
                let (w, h) = (size.0 as i32, size.1 as i32);
                if (src.width() as i32, src.height() as i32) != (w, h) {
                    return Ok(true);
                }
                let f = from.intersect(&IRect::new(0, 0, w, h));
                let dy = dy - oy;
                let r = IRect::new(f.x0, f.y0 + dy, f.x1, f.y1 + dy).intersect(&IRect::new(0, (rows.0 - oy).max(0), w, (rows.1 - oy).min(h))).intersect(&region);
                if !r.is_empty() {
                    let mut hs = std::collections::hash_map::DefaultHasher::new();
                    ("layer", key, f.x0, f.y0, f.x1, f.y1).hash(&mut hs);
                    let rows = || {
                        let sw = src.width() as usize * 4;
                        (f.y0..f.y1).flat_map(|y| src.data()[y as usize * sw + f.x0 as usize * 4..y as usize * sw + f.x1 as usize * 4].iter().copied()).collect()
                    };
                    let t = self.picture(gl, hs.finish(), (f.x1 - f.x0) as u32, (f.y1 - f.y0) as u32, rows)?;
                    self.lay(gl, t, (f.x0, f.y0 + dy), r, size);
                }
            }
            Op::Polygon { .. } | Op::Body(_) | Op::Marquee { .. } => return Ok(false),
        }
        Ok(true)
    }

    /// A path (a rounded rectangle, a circle) filled in colour col, from
    /// its spans; false where they cannot be had as tiny-skia's.
    unsafe fn shape(&mut self, gl: &glow::Context, path: Option<tiny_skia::Path>, col: [u16; 4], region: IRect, size: (u32, u32)) -> Result<bool, String> {
        let Some(path) = path else { return Ok(true) };
        let b = path.bounds();
        // tiny-skia fills nothing so thin.
        if b.width().abs() <= 1.0 / 4096.0 || b.height().abs() <= 1.0 / 4096.0 {
            return Ok(true);
        }
        let Some(sp) = scan::spans(&path, size) else { return Ok(false) };
        let n = sp.rows.len() as u32;
        let r = IRect::new(b.left().floor() as i32, b.top().floor() as i32, b.right().ceil() as i32, b.bottom().ceil() as i32).intersect(&region);
        if n == 0 || r.is_empty() {
            return Ok(true);
        }
        let need = n.div_ceil(SPAN_W);
        if need > SPAN_H {
            return Ok(false);
        }
        if self.span_row + need > SPAN_H {
            self.span_row = 0;
        }
        let at = self.span_row;
        self.span_row += need;
        let mut px = vec![0u8; (need * SPAN_W * 4) as usize];
        for (t, (l, rr)) in px.chunks_exact_mut(4).zip(&sp.rows) {
            t.copy_from_slice(&[(l >> 8) as u8, *l as u8, (rr >> 8) as u8, *rr as u8]);
        }
        let p = self.path;
        self.begin(gl, p, r, size, true);
        gl.bind_texture(glow::TEXTURE_2D, Some(self.spans.id));
        let w = if need == 1 { n } else { SPAN_W };
        gl.tex_sub_image_2d(glow::TEXTURE_2D, 0, 0, at as i32, w as i32, need as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(Some(&px[..(w * need * 4) as usize])));
        uniform1i(gl, p, "spans", 0);
        gl.uniform_3_f32(gl.get_uniform_location(p, "rows").as_ref(), sp.top as f32, n as f32, (at * SPAN_W) as f32);
        self.colour(gl, p, col);
        self.quads(gl, &[(r, [0.0; 4])]);
        Ok(true)
    }

    /// Texture t, whose top left corner is at `at`, laid over inside r.
    unsafe fn lay(&mut self, gl: &glow::Context, t: Tex, at: (i32, i32), r: IRect, size: (u32, u32)) {
        let p = self.over;
        self.begin(gl, p, r, size, true);
        uniform1(gl, p, "mode", 0.0);
        gl.bind_texture(glow::TEXTURE_2D, Some(t.id));
        uniform1i(gl, p, "src", 0);
        uniform2(gl, p, "ssize", t.w as f32, t.h as f32);
        self.quads(gl, &[(r, [-at.0 as f32, -at.1 as f32, 0.0, 0.0])]);
    }

    /// The op painted on the CPU over transparent pixels, inside region,
    /// and laid over.
    unsafe fn fallback(&mut self, gl: &glow::Context, rec: &Rec, job: &Job, region: IRect, size: (u32, u32), fonts: &'static Fonts) -> Result<(), String> {
        let pm = match self.scratch.take().filter(|p| (p.width(), p.height()) == size) {
            Some(p) => p,
            None => Pixmap::new(size.0, size.1).ok_or("no picture for the CPU's ops")?,
        };
        let pm = self.scratch.insert(pm);
        let w = size.0 as usize;
        for y in region.y0..region.y1 {
            pm.data_mut()[(y as usize * w + region.x0 as usize) * 4..(y as usize * w + region.x1 as usize) * 4].fill(0);
        }
        rec.op.paint(pm, job.s, fonts, Some(region), job.oy);
        if (self.loose.w, self.loose.h) != size {
            gl.delete_texture(self.loose.id);
            self.loose = gpu::texture_of(gl, size.0, size.1, glow::RGBA)?;
        }
        gpu::upload(gl, self.row_length, self.loose, pm, region, (0, 0));
        self.lay(gl, self.loose, (0, 0), region, size);
        Ok(())
    }

    /// The texture of the picture with this key, made from its pixels (w x
    /// h, premultiplied RGBA) if new.
    unsafe fn picture(&mut self, gl: &glow::Context, key: u64, w: u32, h: u32, px: impl FnOnce() -> Vec<u8>) -> Result<Tex, String> {
        if let Some((t, used)) = self.pics.get_mut(&key) {
            *used = self.frame;
            return Ok(*t);
        }
        let t = gpu::texture_of(gl, w, h, glow::RGBA)?;
        gl.tex_sub_image_2d(glow::TEXTURE_2D, 0, 0, 0, w as i32, h as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(Some(&px())));
        self.pics.insert(key, (t, self.frame));
        Ok(t)
    }

    /// Pictures the last two frames did not use go, once they are many.
    unsafe fn evict(&mut self, gl: &glow::Context) {
        let keep = self.frame.saturating_sub(1);
        let unused: usize = self.pics.values().filter(|(_, f)| *f < keep).map(|(t, _)| (t.w * t.h * 4) as usize).sum();
        if unused > PICS_SPARE {
            self.pics.retain(|_, (t, f)| {
                let gone = *f < keep;
                if gone {
                    gl.delete_texture(t.id);
                }
                !gone
            });
        }
    }

    /// Program p for a draw inside r of a target of `size`; reads: the
    /// pixels under r are read (copied first, without framebuffer fetch).
    unsafe fn begin(&mut self, gl: &glow::Context, p: glow::Program, r: IRect, size: (u32, u32), reads: bool) {
        gl.use_program(Some(p));
        scissor(gl, r);
        uniform2(gl, p, "tsize", size.0 as f32, size.1 as f32);
        if reads && !self.fetch {
            gl.active_texture(glow::TEXTURE1);
            gl.bind_texture(glow::TEXTURE_2D, Some(self.dst.id));
            gl.copy_tex_sub_image_2d(glow::TEXTURE_2D, 0, r.x0, r.y0, r.x0, r.y0, r.x1 - r.x0, r.y1 - r.y0);
            uniform1i(gl, p, "dst", 1);
        }
        gl.active_texture(glow::TEXTURE0);
    }

    unsafe fn colour(&self, gl: &glow::Context, p: glow::Program, c: [u16; 4]) {
        uniform4(gl, p, "col", c.map(|v| v as f32));
        uniform1(gl, p, "opaque", if c[3] == 255 { 1.0 } else { 0.0 });
    }

    /// Two triangles a quad, each vertex its corner and the quad's aux.
    unsafe fn quads(&self, gl: &glow::Context, qs: &[Quad]) {
        let mut v: Vec<u8> = Vec::with_capacity(qs.len() * 6 * 24);
        for (r, a) in qs {
            let (x0, y0, x1, y1) = (r.x0 as f32, r.y0 as f32, r.x1 as f32, r.y1 as f32);
            for (x, y) in [(x0, y0), (x1, y0), (x0, y1), (x0, y1), (x1, y0), (x1, y1)] {
                v.extend([x, y, a[0], a[1], a[2], a[3]].iter().flat_map(|f| f.to_ne_bytes()));
            }
        }
        gl.buffer_data_u8_slice(glow::ARRAY_BUFFER, &v, glow::STREAM_DRAW);
        gl.draw_arrays(glow::TRIANGLES, 0, qs.len() as i32 * 6);
    }
}

/// The quads in groups none of whose quads overlap, in an order that keeps
/// each quad after those before it that it overlaps: with copies of the
/// pixels under, a group reads them once.
fn apart(qs: Vec<Quad>) -> Vec<Vec<Quad>> {
    let mut level = Vec::with_capacity(qs.len());
    for i in 0..qs.len() {
        let l = (0..i).filter(|&j| qs[j].0.intersects(&qs[i].0)).map(|j| level[j] + 1).max().unwrap_or(0);
        level.push(l);
    }
    let mut out: Vec<Vec<Quad>> = vec![Vec::new(); level.iter().max().map_or(0, |m| m + 1)];
    for (q, l) in qs.into_iter().zip(level) {
        out[l].push(q);
    }
    out
}

fn bounds(qs: &[Quad]) -> Option<IRect> {
    let r = qs.iter().fold(IRect::default(), |a, (q, _)| a.union(q));
    (!r.is_empty()).then_some(r)
}

unsafe fn scissor(gl: &glow::Context, r: IRect) {
    gl.scissor(r.x0, r.y0, r.x1 - r.x0, r.y1 - r.y0);
}

unsafe fn uniform1(gl: &glow::Context, p: glow::Program, name: &str, a: f32) {
    gl.uniform_1_f32(gl.get_uniform_location(p, name).as_ref(), a);
}

unsafe fn uniform1i(gl: &glow::Context, p: glow::Program, name: &str, a: i32) {
    gl.uniform_1_i32(gl.get_uniform_location(p, name).as_ref(), a);
}

unsafe fn uniform2(gl: &glow::Context, p: glow::Program, name: &str, a: f32, b: f32) {
    gl.uniform_2_f32(gl.get_uniform_location(p, name).as_ref(), a, b);
}

unsafe fn uniform4(gl: &glow::Context, p: glow::Program, name: &str, v: [f32; 4]) {
    gl.uniform_4_f32(gl.get_uniform_location(p, name).as_ref(), v[0], v[1], v[2], v[3]);
}

#[cfg(test)]
mod tests {
    use std::rc::Rc;

    use base64::Engine as _;

    use super::*;
    use crate::canvas::{Canvas, Face, Rgb};
    use crate::gpu::Gpu;
    use crate::layers::Layers;

    const W: u32 = 240;
    const H: u32 = 320;

    fn fonts() -> Option<&'static Fonts> {
        let Ok(f) = Fonts::load() else {
            eprintln!("no fonts: skipped");
            return None;
        };
        Some(Box::leak(Box::new(f)))
    }

    fn gpu(w: u32, h: u32) -> Option<Gpu> {
        match Gpu::headless(w, h) {
            Ok(g) => Some(g),
            Err(e) => {
                eprintln!("no GPU ({e}): skipped");
                None
            }
        }
    }

    struct Rng(u64);

    impl Rng {
        fn next(&mut self) -> u32 {
            self.0 ^= self.0 << 13;
            self.0 ^= self.0 >> 7;
            self.0 ^= self.0 << 17;
            (self.0 >> 32) as u32
        }
        fn f(&mut self, lo: f32, hi: f32) -> f32 {
            lo + (hi - lo) * (self.next() as f32 / u32::MAX as f32)
        }
        fn c(&mut self) -> Rgb {
            Rgb(self.next() as u8, self.next() as u8, self.next() as u8)
        }
        fn a(&mut self) -> f32 {
            [1.0, 1.0, 0.5, 0.18, 0.93][self.next() as usize % 5]
        }
    }

    fn image(rng: &mut Rng, w: u32, h: u32) -> crate::frame::Image {
        let px: Vec<u8> = (0..w * h * 3).map(|_| rng.next() as u8).collect();
        let pixelated = rng.next().is_multiple_of(2);
        serde_json::from_value(serde_json::json!({"w": w, "h": h, "rgb": base64::engine::general_purpose::STANDARD.encode(px), "pixelated": pixelated})).unwrap()
    }

    /// Ops of one kind at fractional places, in colours opaque and
    /// translucent, over a background of rectangles, every other one in
    /// the top layer (a layer only in the page: the top layer's are taken
    /// out to slide).
    fn ops(kind: Kind, seed: u64, s: f32, fonts: &'static Fonts) -> Vec<Rec> {
        let mut rng = Rng(seed.wrapping_mul(0x9e37_79b9_7f4a_7c15) | 1);
        let mut cv = Canvas::recording(W, H, s, fonts);
        let (lw, lh) = (W as f32 / s, H as f32 / s);
        cv.clear(rng.c());
        for _ in 0..6 {
            let (x, y) = (rng.f(-10.0, lw), rng.f(-10.0, lh));
            cv.rect_a(x, y, rng.f(5.0, lw / 2.0), rng.f(5.0, lh / 2.0), rng.c(), rng.a());
        }
        let texts = ["Hello, world", "\u{4f60}\u{597d}\u{4e16}\u{754c}", "Kario 0123 \u{2026}", "fi \u{00c5}\u{00e9}g", "\u{6d4b}\u{8bd5}: 10:42"];
        let layer = Rc::new({
            let mut pm = Pixmap::new(W, H).unwrap();
            for (i, px) in pm.data_mut().chunks_exact_mut(4).enumerate() {
                let a = [0u8, 255, 128, 37][(i / 7) % 4] as u32;
                let mut v = || ((rng.next() % 256) * a / 255) as u8;
                px.copy_from_slice(&[v(), v(), v(), a as u8]);
            }
            pm
        });
        for i in 0..40 {
            cv.top = i % 2 == 1 && kind != Kind::Layer;
            let (x, y) = (rng.f(-8.0, lw - 4.0), rng.f(-8.0, lh - 4.0));
            let (w, h) = (rng.f(0.3, lw / 3.0), rng.f(0.3, lh / 4.0));
            match kind {
                Kind::Clear => cv.clear(rng.c()),
                Kind::Rect => cv.rect_a(x, y, w, h, rng.c(), rng.a()),
                Kind::Round => cv.round_a(x, y, w, h, rng.f(0.0, 14.0), rng.c(), rng.a()),
                Kind::Circle => cv.circle(x, y, rng.f(0.2, 20.0), rng.c()),
                Kind::Polygon => {
                    let pts = [(x, y), (x + w, y + h / 3.0), (x + w / 2.0, y + h)];
                    cv.polygon(&pts, rng.c(), rng.c(), [0.2, 1.1][i % 2]);
                }
                Kind::Fade => cv.fade(x, y, w, h * 2.0, rng.c(), rng.f(0.0, 1.0), rng.f(0.0, 1.0)),
                Kind::Text => {
                    let face = [Face::Sans, Face::Bold, Face::Mono][i % 3];
                    cv.text(face, rng.f(9.0, 30.0), x, y, texts[i % texts.len()], rng.c());
                }
                Kind::Image => {
                    let img = image(&mut rng, 3 + i as u32 % 13, 2 + i as u32 % 9);
                    cv.image(&img, x, y, w, h);
                }
                Kind::Layer => {
                    let from = IRect::new(rng.f(0.0, W as f32 / 2.0) as i32, rng.f(0.0, H as f32 / 2.0) as i32, rng.f(W as f32 / 2.0, W as f32) as i32, rng.f(H as f32 / 2.0, H as f32) as i32);
                    let dy = rng.f(-40.0, 40.0) as i32;
                    let rows = (rng.f(0.0, H as f32 / 2.0) as i32, rng.f(H as f32 / 2.0, H as f32 + 10.0) as i32);
                    cv.layer(layer.clone(), 7 + i as u64 % 3, from, dy, rows);
                }
                Kind::Marquee => {
                    let face = [Face::Sans, Face::Bold][i % 2];
                    let clip = (x + w * 0.2, y, x + w, y + rng.f(4.0, 30.0));
                    cv.marquee_text(face, rng.f(9.0, 30.0), x, y, texts[i % texts.len()], rng.c(), clip, (rng.f(0.0, 8.0), rng.f(0.0, 8.0)));
                }
                Kind::Body => {}
            }
        }
        cv.take()
    }

    /// The layer's ops where they may paint, and their kinds.
    fn kinds(list: &[Rec], top: bool) -> Vec<(IRect, Kind)> {
        list.iter().filter(|r| r.top == top).map(|r| (r.bbox, Kind::of(&r.op))).collect()
    }

    // Every kind of op at fractional places and scales, opaque and
    // translucent, over varied pixels: the page and the top layer the GPU
    // paints are the CPU's within the kind's tolerance (exact but for
    // rounded shapes; polygons and a marquee's faded text go to the CPU),
    // with framebuffer fetch and with copies of the pixels under; each kind
    // but those on the GPU.
    #[test]
    fn each_kind_paints_the_cpus_pixels() {
        let Some(fonts) = fonts() else { return };
        for fetch in [true, false] {
            let Some(mut g) = gpu(W, H) else { return };
            if !fetch {
                g.copy_under();
            }
            let kinds_all = [Kind::Clear, Kind::Rect, Kind::Round, Kind::Circle, Kind::Polygon, Kind::Fade, Kind::Text, Kind::Image, Kind::Layer, Kind::Marquee];
            for kind in kinds_all {
                let (mut worst, mut exact) = (0, 0);
                for (seed, s) in [(1, 1.0), (2, 2.0), (3, 2.4), (4, 1.7), (5, 3.0)] {
                    let list = ops(kind, seed, s, fonts);
                    let (mut cpu, mut ras) = (Layers::split(W, H, fonts), Layers::raster(W, H, fonts));
                    cpu.update(list.clone(), s);
                    ras.update(list, s);
                    g.compose(&mut ras).unwrap();
                    for (onto, pm, top) in [(Onto::Page, cpu.page(), false), (Onto::Top, cpu.top().unwrap(), true)] {
                        let on_cpu: Vec<IRect> = g.raster().unwrap().inexact.iter().filter(|f| f.0 == onto).map(|f| f.1).collect();
                        let d = compare(&g.read_layer(onto).unwrap(), pm.data(), W, &kinds(cpu.recs().0, top), &on_cpu);
                        assert_eq!(d.over, 0, "{kind:?} at scale {s}, fetch {fetch}, {onto:?}: {:?} ({:?})", d.first_over, d.kinds);
                        worst = worst.max(d.kinds.iter().map(|k| k.1).max().unwrap_or(0));
                        exact += (on_cpu.is_empty() && d.kinds.iter().all(|k| k.0 == 0)) as usize;
                    }
                }
                let c = &g.raster().unwrap().counts;
                eprintln!("{kind:?} (fetch {fetch}): {exact} of 10 layers exact, at most {worst} levels off; ops on the GPU {:?}, on the CPU {:?}", c.gpu[kind as usize], c.cpu[kind as usize]);
                let on_gpu = kind != Kind::Polygon && kind != Kind::Marquee;
                assert!((c.gpu[kind as usize].0 > 0) == on_gpu, "{kind:?}: {:?} {:?}", c.gpu[kind as usize], c.cpu[kind as usize]);
                assert!(on_gpu || worst <= FALLBACK_LEVELS);
            }
        }
    }

    // Glyphs are rasterized into the atlas the first time they are shown
    // (CJK as any other, none baked before): frames of 49 big CJK glyphs
    // each overflow it after four, the shelves used longest ago go, and
    // glyphs shown again after that are rasterized again; every frame is
    // the CPU's. A glyph taller than the atlas is painted on the CPU.
    #[test]
    fn the_glyph_atlas_keeps_what_was_used_last() {
        let Some(fonts) = fonts() else { return };
        let (w, h, s) = (480u32, 640u32, 2.0);
        let Some(mut g) = gpu(w, h) else { return };
        let line = |from: u32, n: u32| -> String { (from..from + n).filter_map(|c| char::from_u32(0x4e00 + c)).collect() };
        let frame = |from: u32, lines: u32, size: f32| {
            let mut cv = Canvas::recording(w, h, s, fonts);
            cv.clear(Rgb(250, 250, 245));
            for i in 0..lines {
                cv.text(Face::Sans, size, 2.0, 2.0 + i as f32 * size * 1.3, &line(from + i * 7, 7), Rgb(20, 20, 30));
            }
            cv.take()
        };
        let (mut cpu, mut ras) = (Layers::split(w, h, fonts), Layers::raster(w, h, fonts));
        let mut counts = Vec::new();
        for (from, lines, size) in [(0, 7, 34.0), (100, 7, 34.0), (200, 7, 34.0), (300, 7, 34.0), (400, 7, 34.0), (0, 7, 34.0), (0, 7, 34.0), (500, 1, 560.0)] {
            let list = frame(from, lines, size);
            cpu.update(list.clone(), s);
            ras.update(list, s);
            g.compose(&mut ras).unwrap();
            let r = g.raster().unwrap();
            let on_cpu: Vec<IRect> = r.inexact.iter().map(|f| f.1).collect();
            counts.push((r.rasterized(), on_cpu.len()));
            let d = compare(&g.read_layer(Onto::Page).unwrap(), cpu.page().data(), w, &kinds(cpu.recs().0, false), &on_cpu);
            assert_eq!(d.over, 0, "glyphs from {from}: {:?}", d.first_over);
        }
        let new = |i: usize| counts[i].0 - if i == 0 { 0 } else { counts[i - 1].0 };
        assert!((0..5).all(|i| new(i) == 49), "each new glyph rasterized once: {counts:?}");
        assert!(new(5) > 0, "the first glyphs went to make room and came back: {counts:?}");
        assert_eq!(new(6), 0, "shown again at once, all still there: {counts:?}");
        assert!(counts[..7].iter().all(|c| c.1 == 0) && counts[7].1 > 0, "only the glyph taller than the atlas on the CPU: {counts:?}");
    }
}
