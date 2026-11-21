//! The GPU path of the live display (PHONE_PAINT_GPU=1; 37-motion.md §5):
//! the CPU still paints (layers.rs, split), the GPU composes with GLES 2.
//! The layers are textures, of which only what was painted is uploaded;
//! one shader composes the screen from them into a picture of its own
//! with the CPU's integer sums, so it holds exactly `Layers::composed`; a
//! transition is a shader over the screens before and after
//! (motion::Plan), exactly motion.rs's rows; the screen goes to the panel
//! through a GBM surface and a page flip (kms.rs), turned or letterboxed
//! on the way. libEGL, libGLESv2 and libgbm are opened when it starts;
//! without them, without highp floats in fragment shaders, or on any
//! failure, the renderer draws on the CPU. Headless (tests, --bench, the
//! pixel selftest's timing) it renders without a surface
//! (EGL_MESA_platform_surfaceless).

use std::ffi::c_void;

use glow::HasContext;
use khronos_egl as egl;
use tiny_skia::Pixmap;

use crate::canvas::IRect;
use crate::gbm::{self, Bo, Gbm};
use crate::kms;
use crate::layers::Layers;
use crate::motion::{self, Kind, Plan};

const EGL_LIB: &str = "libEGL.so.1";
const GLES_LIB: &str = "libGLESv2.so.2";
/// EGL_PLATFORM_SURFACELESS_MESA, EGL_PLATFORM_GBM_KHR.
const PLATFORM_SURFACELESS: egl::Enum = 0x31DD;
const PLATFORM_GBM: egl::Enum = 0x31D7;
/// The integer sums need floats exact to 2^17 and divisions by 256: a
/// fragment shader's highp of at least this many bits (fp32 has 23).
const HIGHP_BITS: i32 = 20;
/// How far a screen the GPU path composes may be from the CPU path's, in
/// levels of a channel: the top layer and what is drawn over the body are
/// painted over transparent pixels and laid over (canvas.rs's `over`)
/// instead of painted over the page, so where they are not opaque (edges,
/// a dialog's scrim) tiny-skia's rounding and ours may differ by one, by
/// two where two of them overlap (the antialiased corner of a dialog on
/// its scrim).
pub const LEVELS: u8 = 2;

type Egl = egl::DynamicInstance<egl::EGL1_5>;

const VERTEX: &str = "attribute vec2 pos; void main() { gl_Position = vec4(pos, 0.0, 1.0); }";

/// Shared by the fragment shaders: pixels as integers 0..255 (rows from the
/// top, as the pictures), canvas.rs's `over` and the scrim's integer dim.
const COMMON: &str = "
precision highp float;
precision highp sampler2D;
vec4 px(sampler2D t, vec2 p, vec2 size) { return floor(texture2D(t, (p + 0.5) / size) * 255.0 + 0.5); }
vec4 over(vec4 s, vec4 d) {
    vec4 t = d * (255.0 - s.a) + 128.0;
    return min(s + floor((t + floor(t / 256.0)) / 256.0), 255.0);
}
vec4 dim(vec4 c, float k) { return vec4(floor(c.rgb * k / 256.0), c.a); }
float inside(vec4 r, vec2 p) { return step(r.x, p.x) * step(r.y, p.y) * step(p.x, r.z - 1.0) * step(p.y, r.w - 1.0); }
vec4 put(vec4 c) { return (c + 0.25) / 255.0; }
";

/// The screen from the layers: the body's bands (the one shown first, k0,
/// and the next) in the body's rows, the page layer over them, the top
/// layer, the slides.
const COMPOSE: &str = "
uniform sampler2D page, top, band0, band1, slide0, slide1;
uniform vec2 size;
uniform vec4 body;
uniform vec3 bands;
uniform vec4 rect0, rect1, at0, at1;
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    vec2 bsize = vec2(size.x, body.w);
    float by = p.y + body.z;
    float rows = step(body.x, p.y) * step(p.y, body.y - 1.0);
    float next = step(bands.x + 0.5, floor(by / body.w));
    vec4 c = vec4(0.0);
    c = mix(c, px(band0, vec2(p.x, by - bands.x * body.w), bsize), rows * bands.y * (1.0 - next));
    c = mix(c, px(band1, vec2(p.x, by - (bands.x + 1.0) * body.w), bsize), rows * bands.z * next);
    c = over(px(page, p, size), c);
    c = over(px(top, p, size), c);
    c = mix(c, over(px(slide0, p - at0.xy, at0.zw), c), inside(rect0, p));
    c = mix(c, over(px(slide1, p - at1.xy, at1.zw), c), inside(rect1, p));
    gl_FragColor = put(c);
}
";

/// A page transition (motion::slide): rows top..bottom are the new page
/// with the page under at x offset `under` and the page over at `over`;
/// pop: the old page over the new one, dimmed by k (256: not dimmed).
const SLIDE: &str = "
uniform sampler2D old, new;
uniform vec2 size;
uniform vec4 geo;
uniform vec2 opts;
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    float pop = opts.x;
    vec2 pu = vec2(p.x - geo.z, p.y), po = vec2(p.x - geo.w, p.y);
    vec4 under = mix(dim(px(old, pu, size), opts.y), dim(px(new, pu, size), opts.y), pop);
    vec4 top = mix(px(new, po, size), px(old, po, size), pop);
    float rows = step(geo.x, p.y) * step(p.y, geo.y - 1.0);
    vec4 c = px(new, p, size);
    c = mix(c, under, rows * step(0.0, pu.x) * step(pu.x, size.x - 1.0));
    c = mix(c, top, rows * step(0.0, po.x) * step(po.x, size.x - 1.0));
    gl_FragColor = put(c);
}
";

/// A panel moving (motion::panel): rise, the new frame's panel over the
/// old frame scrimmed from row `scrim` by k; sink, the old frame's panel
/// over the new frame; lowered by `drop`, shown above row `clip`.
const PANEL: &str = "
uniform sampler2D old, new;
uniform vec2 size;
uniform vec4 pn;
uniform vec4 opts;
uniform float scrim;
void main() {
    vec2 p = floor(gl_FragCoord.xy);
    float sink = opts.x;
    vec4 o = px(old, p, size);
    vec4 base = mix(mix(o, dim(o, opts.y), step(scrim, p.y)), px(new, p, size), sink);
    vec2 q = vec2(p.x, p.y - opts.z);
    vec4 src = mix(px(new, q, size), px(old, q, size), sink);
    gl_FragColor = put(mix(base, src, inside(pn, q) * step(p.y, opts.w - 1.0)));
}
";

/// A screen onto the panel (kms.rs's mapping): turned a quarter clockwise
/// or at an origin, black around it, the pointer over it; rows go up in
/// a window.
const PRESENT: &str = "
uniform sampler2D frame, arrow;
uniform vec2 size;
uniform vec4 panel;
uniform vec2 origin;
uniform vec4 rect, at;
void main() {
    vec2 q = floor(gl_FragCoord.xy);
    q.y = mix(q.y, panel.y - 1.0 - q.y, panel.w);
    vec2 p = mix(q - origin, vec2(q.y, size.y - 1.0 - q.x), panel.z);
    vec4 c = px(frame, p, size);
    c = mix(c, over(px(arrow, p - at.xy, at.zw), c), inside(rect, p));
    gl_FragColor = put(mix(vec4(0.0, 0.0, 0.0, 255.0), c, inside(vec4(0.0, 0.0, size), p)));
}
";

#[derive(Clone, Copy)]
struct Tex {
    id: glow::Texture,
    w: u32,
    h: u32,
}

#[derive(Clone, Copy)]
struct Target {
    tex: Tex,
    fb: glow::Framebuffer,
}

/// Where the pictures go: nowhere (headless), or a GBM surface on the
/// panel with the buffers on and going to the screen, and their
/// framebuffers.
struct Panel {
    gbm: Gbm,
    surface: egl::Surface,
    size: (u32, u32),
    turned: bool,
    origin: (usize, usize),
    fbs: Vec<(usize, drm::control::framebuffer::Handle)>,
    shown: Option<Bo>,
    queued: Option<Bo>,
}

/// Which screen picture: one of the two composed (`Gpu::front` is the
/// last), or the transition's step.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Pic {
    Screen(usize),
    Step,
}

/// The pointer for the panel: its picture (premultiplied, transparent
/// around the arrow) and where its corner is on the screen.
pub struct Arrow<'a> {
    pub pm: &'a Pixmap,
    pub at: (i32, i32),
}

pub struct Gpu {
    egl: Egl,
    dpy: egl::Display,
    ctx: egl::Context,
    panel: Option<Panel>,
    gl: glow::Context,
    compose: glow::Program,
    slide: glow::Program,
    panel_prog: glow::Program,
    present: glow::Program,
    size: (u32, u32),
    page: Tex,
    top: Tex,
    bands: Vec<(i32, Tex)>,
    slides: Vec<(u64, Tex, IRect)>,
    spare: Vec<Tex>,
    empty: Tex,
    arrow: Option<Tex>,
    screens: [Target; 2],
    step: Target,
    front: usize,
    /// The textures get their first upload whole.
    fresh: bool,
    /// UNPACK_ROW_LENGTH (GLES 3, GL_EXT_unpack_subimage): a rectangle is
    /// uploaded from the picture's rows as they are.
    row_length: bool,
    /// Pixels uploaded by the last compose.
    pub uploaded: usize,
    _gles: Option<libloading::Library>,
}

fn es(e: egl::Error) -> String {
    format!("EGL: {e}")
}

/// A system library, never unloaded: Mesa's libraries register exit
/// handlers that must not outlive their code.
pub fn library(path: &str) -> Result<libloading::Library, String> {
    // SAFETY: a system library; its initialisers run here.
    unsafe { libloading::os::unix::Library::open(Some(path), libc::RTLD_NOW | libc::RTLD_NODELETE) }.map(Into::into).map_err(|e| format!("{path}: {e}"))
}

fn load_egl(path: &str) -> Result<Egl, String> {
    let lib = library(path)?;
    // SAFETY: the EGL 1.5 entry points, read from libEGL with their
    // signatures by khronos-egl.
    unsafe { Egl::load_required_from(lib) }.map_err(|e| format!("{path}: {e}"))
}

/// A GLES 2 context on dpy with a config for surface_type; with visual, the
/// config whose native visual is that format (GBM's).
fn context(egl: &Egl, dpy: egl::Display, surface_type: egl::Int, visual: Option<u32>) -> Result<(egl::Config, egl::Context), String> {
    egl.bind_api(egl::OPENGL_ES_API).map_err(es)?;
    let attrs = [egl::RENDERABLE_TYPE, egl::OPENGL_ES2_BIT, egl::SURFACE_TYPE, surface_type, egl::RED_SIZE, 8, egl::GREEN_SIZE, 8, egl::BLUE_SIZE, 8, egl::NONE];
    let mut configs = Vec::with_capacity(64);
    egl.choose_config(dpy, &attrs, &mut configs).map_err(es)?;
    let cfg = configs
        .into_iter()
        .find(|&c| visual.is_none_or(|v| egl.get_config_attrib(dpy, c, egl::NATIVE_VISUAL_ID).ok() == Some(v as egl::Int)))
        .ok_or("no EGL config for GLES 2 with 8-bit colour")?;
    let ctx = egl.create_context(dpy, cfg, None, &[egl::CONTEXT_CLIENT_VERSION, 2, egl::NONE]).map_err(es)?;
    Ok((cfg, ctx))
}

impl Gpu {
    /// Without a panel: the screens are only read back.
    pub fn headless(width: u32, height: u32) -> Result<Gpu, String> {
        Self::headless_from(EGL_LIB, width, height)
    }

    fn headless_from(lib: &str, width: u32, height: u32) -> Result<Gpu, String> {
        let egl = load_egl(lib)?;
        // SAFETY: the default display of the surfaceless platform.
        let dpy = unsafe { egl.get_platform_display(PLATFORM_SURFACELESS, egl::DEFAULT_DISPLAY, &[egl::ATTRIB_NONE]) }.map_err(es)?;
        egl.initialize(dpy).map_err(es)?;
        let (_, ctx) = context(&egl, dpy, 0, None)?;
        egl.make_current(dpy, None, None, Some(ctx)).map_err(es)?;
        Self::setup(egl, dpy, ctx, None, width, height)
    }

    /// On the panel: a GBM surface of the mode's size on the display's
    /// device, the screens drawn width × height and put on it as the
    /// display turns or places them.
    pub fn on_panel(d: &kms::Display, width: u32, height: u32) -> Result<Gpu, String> {
        Self::on_device(d.fd(), (d.width, d.height), d.rotated, d.origin, width, height)
    }

    fn on_device(fd: std::os::fd::RawFd, size: (u32, u32), turned: bool, origin: (usize, usize), width: u32, height: u32) -> Result<Gpu, String> {
        let gbm = Gbm::open(fd, size.0, size.1)?;
        let egl = load_egl(EGL_LIB)?;
        // SAFETY: the GBM device lives in Panel, longer than the display
        // (Drop terminates it first).
        let dpy = unsafe { egl.get_platform_display(PLATFORM_GBM, gbm.device(), &[egl::ATTRIB_NONE]) }.map_err(es)?;
        egl.initialize(dpy).map_err(es)?;
        let (cfg, ctx) = context(&egl, dpy, egl::WINDOW_BIT, Some(gbm::XRGB8888)).inspect_err(|_| {
            let _ = egl.terminate(dpy);
        })?;
        // SAFETY: the GBM surface outlives the EGL surface (Drop).
        let surface = match unsafe { egl.create_platform_window_surface(dpy, cfg, gbm.surface(), &[egl::ATTRIB_NONE]) } {
            Ok(s) => s,
            Err(e) => {
                let _ = egl.terminate(dpy);
                return Err(es(e));
            }
        };
        if let Err(e) = egl.make_current(dpy, Some(surface), Some(surface), Some(ctx)) {
            let _ = egl.terminate(dpy);
            return Err(es(e));
        }
        let panel = Panel { gbm, surface, size, turned, origin, fbs: Vec::new(), shown: None, queued: None };
        let g = Self::setup(egl, dpy, ctx, Some(panel), width, height)?;
        // Where no GPU driver takes the device, Mesa renders with llvmpipe
        // into dumb buffers: slower than the CPU path, on many threads.
        let name = g.renderer();
        if ["llvmpipe", "softpipe", "swrast", "software"].iter().any(|s| name.to_lowercase().contains(s)) {
            return Err(format!("{name} is a software rasterizer"));
        }
        g.probe()?;
        Ok(g)
    }

    /// The GL renderer's name (the GPU and its driver).
    pub fn renderer(&self) -> String {
        // SAFETY: a GL call on the current context.
        unsafe { self.gl.get_parameter_string(glow::RENDERER) }
    }

    /// A first frame through the GBM surface, not shown: the surface can
    /// give buffers that DRM takes.
    fn probe(&self) -> Result<(), String> {
        let Some(p) = self.panel.as_ref() else { return Ok(()) };
        // SAFETY: GL calls on the current context.
        unsafe {
            self.bind(None, p.size.0, p.size.1);
            self.gl.clear_color(0.0, 0.0, 0.0, 1.0);
            self.gl.clear(glow::COLOR_BUFFER_BIT);
        }
        self.egl.swap_buffers(self.dpy, p.surface).map_err(es)?;
        let bo = p.gbm.lock_front().ok_or("the GBM surface gave no buffer")?;
        p.gbm.release(bo);
        Ok(())
    }

    fn setup(egl: Egl, dpy: egl::Display, ctx: egl::Context, panel: Option<Panel>, width: u32, height: u32) -> Result<Gpu, String> {
        // For functions EGL does not hand out.
        let gles = library(GLES_LIB).ok();
        // SAFETY: the context is current on this thread; the functions are
        // looked up by their GLES names.
        let gl = unsafe {
            glow::Context::from_loader_function(|name| {
                if let Some(f) = egl.get_proc_address(name) {
                    return f as *const c_void;
                }
                let sym = format!("{name}\0");
                gles.as_ref().and_then(|l| l.get::<*const c_void>(sym.as_bytes()).ok().map(|f| *f)).unwrap_or(std::ptr::null())
            })
        };
        let fail = |e: String| {
            if panel.is_some() {
                let _ = egl.terminate(dpy);
            }
            Err(e)
        };
        // SAFETY: GL calls on the current context.
        unsafe {
            let bits = gl.get_shader_precision_format(glow::FRAGMENT_SHADER, glow::HIGH_FLOAT).map_or(0, |p| p.precision);
            if bits < HIGHP_BITS {
                return fail(format!("fragment shaders have {bits}-bit highp floats; the composition needs {HIGHP_BITS}"));
            }
            let row_length = gl.version().major >= 3 || gl.supported_extensions().contains("GL_EXT_unpack_subimage");
            let progs = [COMPOSE, SLIDE, PANEL, PRESENT].map(|fs| program(&gl, fs));
            let [compose, slide, panel_prog, present] = match progs {
                [Ok(a), Ok(b), Ok(c), Ok(d)] => [a, b, c, d],
                other => return fail(other.into_iter().find_map(Result::err).unwrap_or_default()),
            };
            let quad = match gl.create_buffer() {
                Ok(b) => b,
                Err(e) => return fail(e),
            };
            gl.bind_buffer(glow::ARRAY_BUFFER, Some(quad));
            let corners: Vec<u8> = [-1.0f32, -1.0, 1.0, -1.0, -1.0, 1.0, 1.0, 1.0].iter().flat_map(|f| f.to_ne_bytes()).collect();
            gl.buffer_data_u8_slice(glow::ARRAY_BUFFER, &corners, glow::STATIC_DRAW);
            gl.enable_vertex_attrib_array(0);
            gl.vertex_attrib_pointer_f32(0, 2, glow::FLOAT, false, 8, 0);
            for cap in [glow::DITHER, glow::BLEND, glow::DEPTH_TEST, glow::SCISSOR_TEST, glow::CULL_FACE] {
                gl.disable(cap);
            }
            gl.pixel_store_i32(glow::UNPACK_ALIGNMENT, 4);
            gl.pixel_store_i32(glow::PACK_ALIGNMENT, 4);
            let made = (|| -> Result<_, String> {
                Ok((
                    texture(&gl, width, height)?,
                    texture(&gl, width, height)?,
                    texture(&gl, 1, 1)?,
                    [target(&gl, width, height)?, target(&gl, width, height)?],
                    target(&gl, width, height)?,
                ))
            })();
            let (page, top, empty, screens, step) = match made {
                Ok(m) => m,
                Err(e) => return fail(e),
            };
            gl.bind_texture(glow::TEXTURE_2D, Some(empty.id));
            gl.tex_sub_image_2d(glow::TEXTURE_2D, 0, 0, 0, 1, 1, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(Some(&[0, 0, 0, 0])));
            Ok(Gpu {
                egl,
                dpy,
                ctx,
                panel,
                gl,
                compose,
                slide,
                panel_prog,
                present,
                size: (width, height),
                page,
                top,
                bands: Vec::new(),
                slides: Vec::new(),
                spare: Vec::new(),
                empty,
                arrow: None,
                screens,
                step,
                front: 0,
                fresh: true,
                row_length,
                uploaded: 0,
                _gles: gles,
            })
        }
    }

    /// The screen composed last.
    pub fn front(&self) -> usize {
        self.front
    }

    /// The screens' size.
    pub fn size(&self) -> (u32, u32) {
        self.size
    }

    /// Uploads what the split layers painted since the last call and, if
    /// the screen changed, composes it into the other screen picture (the
    /// new front). Returns whether it did.
    pub fn compose(&mut self, layers: &mut Layers) -> Result<bool, String> {
        let dirty = layers.take_dirty();
        let (w, h) = layers.size();
        if (w, h) != self.size {
            return Err(format!("layers of {w}x{h} for screens of {}x{}", self.size.0, self.size.1));
        }
        let whole = IRect::new(0, 0, w as i32, h as i32);
        let fresh = std::mem::take(&mut self.fresh);
        self.uploaded = 0;
        let top = layers.top().ok_or("the layers are not split")?;
        // SAFETY: GL calls on the current context; the uploads read inside
        // the pictures (rectangles clipped to them).
        unsafe {
            for (pm, tex, rects) in [(layers.page(), self.page, &dirty.page), (top, self.top, &dirty.top)] {
                let rects: &[IRect] = if fresh { std::slice::from_ref(&whole) } else { rects };
                for r in rects {
                    self.uploaded += self.upload(tex, pm, *r, (0, 0));
                }
            }
            let keep: Vec<i32> = layers.bands().iter().map(|(k, _)| *k).collect();
            let (kept, gone): (Vec<_>, Vec<_>) = std::mem::take(&mut self.bands).into_iter().partition(|(k, t)| keep.contains(k) && layers.bands().iter().any(|(j, pm)| j == k && (pm.width(), pm.height()) == (t.w, t.h)));
            self.bands = kept;
            self.spare.extend(gone.into_iter().map(|(_, t)| t));
            for (k, pm) in layers.bands() {
                let band = IRect::new(0, 0, pm.width() as i32, pm.height() as i32);
                let new = !self.bands.iter().any(|(j, _)| j == k);
                if new {
                    let t = match self.spare.iter().position(|t| (t.w, t.h) == (pm.width(), pm.height())) {
                        Some(i) => self.spare.swap_remove(i),
                        None => texture(&self.gl, pm.width(), pm.height())?,
                    };
                    self.bands.push((*k, t));
                }
                let t = self.bands.iter().find(|(j, _)| j == k).map(|(_, t)| *t).expect("band texture");
                if new {
                    self.uploaded += self.upload(t, pm, band, (0, 0));
                } else {
                    for (_, r) in dirty.bands.iter().filter(|(j, _)| j == k) {
                        self.uploaded += self.upload(t, pm, *r, (0, 0));
                    }
                }
            }
            for t in self.spare.split_off(self.spare.len().min(2)) {
                self.gl.delete_texture(t.id);
            }
            let mut slides = Vec::new();
            for sl in layers.slides() {
                let from = sl.from.intersect(&whole);
                if from.is_empty() {
                    continue;
                }
                let t = match self.slides.iter().position(|(k, _, f)| *k == sl.key && *f == from) {
                    Some(i) => self.slides.swap_remove(i).1,
                    None => {
                        let t = texture(&self.gl, (from.x1 - from.x0) as u32, (from.y1 - from.y0) as u32)?;
                        self.uploaded += self.upload(t, &sl.src, from, (from.x0, from.y0));
                        t
                    }
                };
                slides.push((sl.key, t, from));
            }
            for (_, t, _) in std::mem::replace(&mut self.slides, slides) {
                self.gl.delete_texture(t.id);
            }
            if !fresh && layers.last.screen.is_empty() {
                return Ok(false);
            }
            let to = self.screens[1 - self.front];
            self.bind(Some(to.fb), w, h);
            let p = self.compose;
            self.gl.use_program(Some(p));
            self.uniform2(p, "size", w as f32, h as f32);
            let (mut body, mut flags) = ([0.0, 0.0, 0.0, 1.0], [0.0; 3]);
            let mut band_tex = [self.empty, self.empty];
            if let Some((n, rows, band)) = layers.body().filter(|b| b.2 > 0) {
                let k0 = (rows.0 + n).div_euclid(band);
                body = [rows.0 as f32, rows.1 as f32, n as f32, band as f32];
                flags[0] = k0 as f32;
                for (i, k) in [k0, k0 + 1].into_iter().enumerate() {
                    if let Some((_, t)) = self.bands.iter().find(|(j, _)| *j == k) {
                        band_tex[i] = *t;
                        flags[i + 1] = 1.0;
                    }
                }
            }
            self.uniform4(p, "body", body);
            self.gl.uniform_3_f32(self.gl.get_uniform_location(p, "bands").as_ref(), flags[0], flags[1], flags[2]);
            let mut slide_tex = [self.empty, self.empty];
            for (i, tex) in slide_tex.iter_mut().enumerate() {
                let (rect, at) = match (layers.slides().get(i), self.slides.get(i)) {
                    (Some(sl), Some((_, t, from))) => {
                        *tex = *t;
                        let r = sl.shown(whole);
                        ([r.x0, r.y0, r.x1, r.y1].map(|v| v as f32), [from.x0 as f32, (from.y0 + sl.dy) as f32, t.w as f32, t.h as f32])
                    }
                    _ => ([0.0; 4], [0.0, 0.0, 1.0, 1.0]),
                };
                self.uniform4(p, &format!("rect{i}"), rect);
                self.uniform4(p, &format!("at{i}"), at);
            }
            let units = [("page", self.page), ("top", self.top), ("band0", band_tex[0]), ("band1", band_tex[1]), ("slide0", slide_tex[0]), ("slide1", slide_tex[1])];
            self.draw(p, &units);
        }
        self.front = 1 - self.front;
        Ok(true)
    }

    /// A step of the transition plan at eased progress p, from screen old
    /// to screen new, into the step picture.
    pub fn transition(&mut self, plan: &Plan, p: f32, old: usize, new: usize) {
        let (w, h) = self.size;
        let (o, n) = (self.screens[old].tex, self.screens[new].tex);
        // SAFETY: GL calls on the current context.
        unsafe {
            self.bind(Some(self.step.fb), w, h);
            let prog = match *plan {
                Plan::Slide { kind, top, bottom } => {
                    let (ux, ox) = motion::offsets(kind, p, w as usize);
                    let pop = kind == Kind::Pop;
                    let k = if pop && p >= 1.0 { 256.0 } else { 184.0 };
                    let prog = self.slide;
                    self.gl.use_program(Some(prog));
                    self.uniform4(prog, "geo", [top as f32, bottom as f32, ux as f32, ox as f32]);
                    self.uniform2(prog, "opts", if pop { 1.0 } else { 0.0 }, k);
                    prog
                }
                Plan::Panel { kind, pn, clip, top, k, .. } => {
                    let prog = self.panel_prog;
                    self.gl.use_program(Some(prog));
                    let x = |v: usize| v.min(w as usize) as f32;
                    self.uniform4(prog, "pn", [x(pn.x0), pn.y0 as f32, x(pn.x1), pn.y1 as f32]);
                    let sink = if kind == Kind::Sink { 1.0 } else { 0.0 };
                    self.uniform4(prog, "opts", [sink, k as f32, plan.drop(p) as f32, clip.min(h as usize) as f32]);
                    self.gl.uniform_1_f32(self.gl.get_uniform_location(prog, "scrim").as_ref(), top as f32);
                    prog
                }
            };
            self.uniform2(prog, "size", w as f32, h as f32);
            self.draw(prog, &[("old", o), ("new", n)]);
        }
    }

    fn pic(&self, which: Pic) -> Target {
        match which {
            Pic::Screen(i) => self.screens[i],
            Pic::Step => self.step,
        }
    }

    /// Puts a screen picture on the panel, with the pointer over it: drawn
    /// into the GBM surface's next buffer, which a page flip shows (the
    /// buffer shown before the last flip goes back to the surface).
    pub fn show(&mut self, d: &mut kms::Display, which: Pic, arrow: Option<Arrow>) -> Result<(), String> {
        let Some(panel) = self.panel.as_ref() else { return Ok(()) };
        let (size, turned, origin, surface) = (panel.size, panel.turned, panel.origin, panel.surface);
        let src = self.pic(which);
        // SAFETY: GL calls on the current context.
        unsafe {
            self.bind(None, size.0, size.1);
            self.draw_present(src, arrow, size, turned, origin, true)?;
        }
        self.egl.swap_buffers(self.dpy, surface).map_err(es)?;
        let panel = self.panel.as_mut().expect("panel");
        let bo = panel.gbm.lock_front().ok_or("gbm_surface_lock_front_buffer failed")?;
        let fb = match panel.fbs.iter().find(|(p, _)| *p == bo.ptr as usize) {
            Some((_, fb)) => *fb,
            None => {
                let fb = d.add_fb(bo.handle, bo.size, bo.pitch).inspect_err(|_| panel.gbm.release(bo))?;
                panel.fbs.push((bo.ptr as usize, fb));
                fb
            }
        };
        let first = panel.shown.is_none() && panel.queued.is_none();
        let flipping = d.scan_out(fb, first).inspect_err(|_| panel.gbm.release(bo))?;
        let off;
        (panel.shown, panel.queued, off) = advance(panel.shown, panel.queued, bo, flipping);
        for b in off {
            panel.gbm.release(b);
        }
        Ok(())
    }

    /// Gives the panel back to the dumb buffers (the CPU path) and lets go
    /// of everything.
    pub fn close(mut self, d: &mut kms::Display) {
        d.back_to_dumb();
        if let Some(p) = self.panel.as_mut() {
            for (_, fb) in p.fbs.drain(..) {
                d.remove_fb(fb);
            }
        }
    }

    /// The present pass into what is bound: the screen picture src onto a
    /// panel of `size`.
    unsafe fn draw_present(&mut self, src: Target, arrow: Option<Arrow>, size: (u32, u32), turned: bool, origin: (usize, usize), up: bool) -> Result<(), String> {
        let (w, h) = self.size;
        let mut rect = [0.0; 4];
        let mut at = [0.0, 0.0, 1.0, 1.0];
        let mut arrow_tex = self.empty;
        if let Some(a) = arrow {
            let t = match self.arrow.filter(|t| (t.w, t.h) == (a.pm.width(), a.pm.height())) {
                Some(t) => t,
                None => {
                    if let Some(t) = self.arrow.take() {
                        self.gl.delete_texture(t.id);
                    }
                    texture(&self.gl, a.pm.width(), a.pm.height())?
                }
            };
            self.arrow = Some(t);
            self.upload(t, a.pm, IRect::new(0, 0, t.w as i32, t.h as i32), (0, 0));
            let r = IRect::new(a.at.0, a.at.1, a.at.0 + t.w as i32, a.at.1 + t.h as i32).intersect(&IRect::new(0, 0, w as i32, h as i32));
            rect = [r.x0, r.y0, r.x1, r.y1].map(|v| v as f32);
            at = [a.at.0 as f32, a.at.1 as f32, t.w as f32, t.h as f32];
            arrow_tex = t;
        }
        let p = self.present;
        self.gl.use_program(Some(p));
        self.uniform2(p, "size", w as f32, h as f32);
        self.uniform4(p, "panel", [size.0 as f32, size.1 as f32, if turned { 1.0 } else { 0.0 }, if up { 1.0 } else { 0.0 }]);
        self.uniform2(p, "origin", origin.0 as f32, origin.1 as f32);
        self.uniform4(p, "rect", rect);
        self.uniform4(p, "at", at);
        self.draw(p, &[("frame", src.tex), ("arrow", arrow_tex)]);
        Ok(())
    }

    /// A screen picture read back: RGBA rows from the top.
    pub fn read(&self, which: Pic) -> Vec<u8> {
        let (w, h) = self.size;
        let mut out = vec![0u8; (w * h * 4) as usize];
        // SAFETY: GL calls on the current context; out holds the rows.
        unsafe {
            self.gl.bind_framebuffer(glow::FRAMEBUFFER, Some(self.pic(which).fb));
            self.gl.read_pixels(0, 0, w as i32, h as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelPackData::Slice(Some(&mut out)));
        }
        out
    }

    /// Waits for the GPU to finish what it was given (timing).
    pub fn finish(&self) {
        // SAFETY: a GL call on the current context.
        unsafe { self.gl.finish() }
    }

    /// Puts a picture (the screens' size) into screen picture i, to run a
    /// transition from (tests).
    #[cfg(test)]
    fn put_screen(&mut self, i: usize, pm: &Pixmap) {
        let r = IRect::new(0, 0, pm.width() as i32, pm.height() as i32);
        // SAFETY: GL calls on the current context.
        unsafe {
            self.upload(self.screens[i].tex, pm, r, (0, 0));
        }
    }

    /// The present pass into a picture of the panel's size instead of the
    /// panel, read back (tests).
    #[cfg(test)]
    fn present_into(&mut self, which: Pic, arrow: Option<Arrow>, size: (u32, u32), turned: bool, origin: (usize, usize), up: bool) -> Vec<u8> {
        let mut out = vec![0u8; (size.0 * size.1 * 4) as usize];
        // SAFETY: GL calls on the current context; out holds the rows.
        unsafe {
            let t = target(&self.gl, size.0, size.1).unwrap();
            self.bind(Some(t.fb), size.0, size.1);
            self.draw_present(self.pic(which), arrow, size, turned, origin, up).unwrap();
            self.gl.read_pixels(0, 0, size.0 as i32, size.1 as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelPackData::Slice(Some(&mut out)));
            self.gl.delete_framebuffer(t.fb);
            self.gl.delete_texture(t.tex.id);
        }
        out
    }

    /// Uploads rectangle r of pm (clipped to it) into t, where (r - at)
    /// lands; returns the pixels uploaded.
    unsafe fn upload(&self, t: Tex, pm: &Pixmap, r: IRect, at: (i32, i32)) -> usize {
        let sw = pm.width() as usize;
        let r = r.intersect(&IRect::new(0, 0, pm.width() as i32, pm.height() as i32)).intersect(&IRect::new(at.0, at.1, at.0 + t.w as i32, at.1 + t.h as i32));
        if r.is_empty() {
            return 0;
        }
        let (x, y, w, h) = (r.x0 as usize, r.y0 as usize, (r.x1 - r.x0) as usize, (r.y1 - r.y0) as usize);
        let data = pm.data();
        let gl = &self.gl;
        gl.bind_texture(glow::TEXTURE_2D, Some(t.id));
        let put = |px: &[u8]| gl.tex_sub_image_2d(glow::TEXTURE_2D, 0, r.x0 - at.0, r.y0 - at.1, w as i32, h as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(Some(px)));
        if w == sw || self.row_length {
            if w != sw {
                gl.pixel_store_i32(glow::UNPACK_ROW_LENGTH, sw as i32);
            }
            put(&data[(y * sw + x) * 4..((y + h - 1) * sw + x + w) * 4]);
            if w != sw {
                gl.pixel_store_i32(glow::UNPACK_ROW_LENGTH, 0);
            }
        } else {
            let mut rows = Vec::with_capacity(w * h * 4);
            for yy in y..y + h {
                rows.extend_from_slice(&data[(yy * sw + x) * 4..(yy * sw + x + w) * 4]);
            }
            put(&rows);
        }
        w * h
    }

    unsafe fn bind(&self, fb: Option<glow::Framebuffer>, w: u32, h: u32) {
        self.gl.bind_framebuffer(glow::FRAMEBUFFER, fb);
        self.gl.viewport(0, 0, w as i32, h as i32);
    }

    unsafe fn uniform2(&self, p: glow::Program, name: &str, a: f32, b: f32) {
        self.gl.uniform_2_f32(self.gl.get_uniform_location(p, name).as_ref(), a, b);
    }

    unsafe fn uniform4(&self, p: glow::Program, name: &str, v: [f32; 4]) {
        self.gl.uniform_4_f32(self.gl.get_uniform_location(p, name).as_ref(), v[0], v[1], v[2], v[3]);
    }

    /// The whole target covered once by program p, with textures on the
    /// samplers named.
    unsafe fn draw(&self, p: glow::Program, units: &[(&str, Tex)]) {
        for (i, (name, t)) in units.iter().enumerate() {
            self.gl.active_texture(glow::TEXTURE0 + i as u32);
            self.gl.bind_texture(glow::TEXTURE_2D, Some(t.id));
            self.gl.uniform_1_i32(self.gl.get_uniform_location(p, name).as_ref(), i as i32);
        }
        self.gl.draw_arrays(glow::TRIANGLE_STRIP, 0, 4);
        self.gl.active_texture(glow::TEXTURE0);
    }
}

impl Drop for Gpu {
    fn drop(&mut self) {
        let _ = self.egl.make_current(self.dpy, None, None, None);
        let _ = self.egl.destroy_context(self.dpy, self.ctx);
        if let Some(p) = self.panel.take() {
            for b in [p.shown, p.queued].into_iter().flatten() {
                p.gbm.release(b);
            }
            let _ = self.egl.destroy_surface(self.dpy, p.surface);
            // The EGL display refers to the GBM device: it goes first. A
            // headless display is shared by every Gpu of the process and is
            // left initialised.
            let _ = self.egl.terminate(self.dpy);
            drop(p.gbm);
        }
    }
}

/// The buffers once `new` has gone to the screen (scan_out): the one on
/// the screen, the one a flip waits for, and those to give back to the
/// surface. A flip asked for before new's has happened by then (scan_out
/// waits for it), so its buffer is on the screen and the one before it is
/// free; set at once, new is on the screen and both are free.
fn advance<T>(shown: Option<T>, queued: Option<T>, new: T, flipping: bool) -> (Option<T>, Option<T>, Vec<T>) {
    match (flipping, queued) {
        (true, Some(q)) => (Some(q), Some(new), shown.into_iter().collect()),
        (true, None) => (shown, Some(new), Vec::new()),
        (false, q) => (Some(new), None, shown.into_iter().chain(q).collect()),
    }
}

unsafe fn texture(gl: &glow::Context, w: u32, h: u32) -> Result<Tex, String> {
    let id = gl.create_texture()?;
    gl.bind_texture(glow::TEXTURE_2D, Some(id));
    for p in [glow::TEXTURE_MIN_FILTER, glow::TEXTURE_MAG_FILTER] {
        gl.tex_parameter_i32(glow::TEXTURE_2D, p, glow::NEAREST as i32);
    }
    for p in [glow::TEXTURE_WRAP_S, glow::TEXTURE_WRAP_T] {
        gl.tex_parameter_i32(glow::TEXTURE_2D, p, glow::CLAMP_TO_EDGE as i32);
    }
    gl.tex_image_2d(glow::TEXTURE_2D, 0, glow::RGBA as i32, w.max(1) as i32, h.max(1) as i32, 0, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(None));
    Ok(Tex { id, w: w.max(1), h: h.max(1) })
}

unsafe fn target(gl: &glow::Context, w: u32, h: u32) -> Result<Target, String> {
    let tex = texture(gl, w, h)?;
    let fb = gl.create_framebuffer()?;
    gl.bind_framebuffer(glow::FRAMEBUFFER, Some(fb));
    gl.framebuffer_texture_2d(glow::FRAMEBUFFER, glow::COLOR_ATTACHMENT0, glow::TEXTURE_2D, Some(tex.id), 0);
    let status = gl.check_framebuffer_status(glow::FRAMEBUFFER);
    gl.bind_framebuffer(glow::FRAMEBUFFER, None);
    if status != glow::FRAMEBUFFER_COMPLETE {
        return Err(format!("a {w}x{h} RGBA framebuffer is incomplete ({status:#x})"));
    }
    Ok(Target { tex, fb })
}

unsafe fn program(gl: &glow::Context, fragment: &str) -> Result<glow::Program, String> {
    let p = gl.create_program()?;
    for (kind, src) in [(glow::VERTEX_SHADER, VERTEX.to_string()), (glow::FRAGMENT_SHADER, format!("{COMMON}{fragment}"))] {
        let s = gl.create_shader(kind)?;
        gl.shader_source(s, &src);
        gl.compile_shader(s);
        if !gl.get_shader_compile_status(s) {
            return Err(format!("shader: {}", gl.get_shader_info_log(s)));
        }
        gl.attach_shader(p, s);
        gl.delete_shader(s);
    }
    gl.bind_attrib_location(p, 0, "pos");
    gl.link_program(p);
    if !gl.get_program_link_status(p) {
        return Err(format!("program: {}", gl.get_program_info_log(p)));
    }
    Ok(p)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::motion::Panel as Pn;

    fn gpu(w: u32, h: u32) -> Option<Gpu> {
        match Gpu::headless(w, h) {
            Ok(g) => Some(g),
            Err(e) => {
                eprintln!("no GPU ({e}): skipped");
                None
            }
        }
    }

    /// An opaque picture whose every pixel tells where it is from.
    fn pic(w: u32, h: u32, seed: u32) -> Pixmap {
        let mut pm = Pixmap::new(w, h).unwrap();
        for (i, px) in pm.data_mut().chunks_exact_mut(4).enumerate() {
            let v = (i as u32).wrapping_mul(2654435761).wrapping_add(seed.wrapping_mul(40503));
            px.copy_from_slice(&[(v >> 24) as u8, (v >> 16) as u8, (v >> 8) as u8, 255]);
        }
        pm
    }

    // Every step of every kind of transition the GPU composes is exactly
    // the CPU's (motion::Plan::cpu): the slides' offsets and the dimmed
    // page under them, a menu rising from behind the soft keys over the
    // scrim, a dialog's short way, a panel dropping out of sight.
    #[test]
    fn transitions_are_the_cpu_steps() {
        let (w, h) = (60, 80);
        let Some(mut g) = gpu(w, h) else { return };
        let (old, new) = (pic(w, h, 1), pic(w, h, 2));
        g.put_screen(0, &old);
        g.put_screen(1, &new);
        let pn = Pn { x0: 4, y0: 30, x1: 56, y1: 62 };
        let plans = [
            Plan::Slide { kind: Kind::Push, top: 8, bottom: 70 },
            Plan::Slide { kind: Kind::Pop, top: 8, bottom: 70 },
            Plan::Panel { kind: Kind::Rise, pn, clip: 70, menu: true, top: 8, k: motion::scrim_k(0.5) },
            Plan::Panel { kind: Kind::Rise, pn, clip: 80, menu: false, top: 8, k: motion::scrim_k(0.55) },
            Plan::Panel { kind: Kind::Sink, pn, clip: 70, menu: true, top: 8, k: 256 },
            Plan::Panel { kind: Kind::Sink, pn: Pn { x0: 0, y0: 50, x1: 90, y1: 79 }, clip: 80, menu: false, top: 8, k: 256 },
        ];
        let mut want = Pixmap::new(w, h).unwrap();
        for plan in plans {
            let step = plan.cpu(&old, &new);
            for p in [0.0, 0.07, 0.25, 0.5, 0.61, 0.9, 0.999, 1.0] {
                step(want.data_mut(), p);
                g.transition(&plan, p, 0, 1);
                assert!(g.read(Pic::Step) == want.data(), "{plan:?} at {p}: not the CPU's step");
            }
        }
    }

    // The screen onto the panel as kms.rs puts it there: turned a quarter
    // clockwise onto a portrait panel, or in the middle of a bigger one
    // with black around; a window's rows go up; the pointer is laid over.
    #[test]
    fn the_panel_gets_the_screen_turned_or_placed() {
        let (w, h) = (40, 30);
        let Some(mut g) = gpu(w, h) else { return };
        let frame = pic(w, h, 3);
        g.put_screen(0, &frame);
        let at = |d: &[u8], width: usize, x: usize, y: usize| d[(y * width + x) * 4..(y * width + x) * 4 + 4].to_vec();
        let turned = g.present_into(Pic::Screen(0), None, (h, w), true, (0, 0), false);
        let boxed = g.present_into(Pic::Screen(0), None, (60, 50), false, (10, 12), false);
        for y in 0..h as usize {
            for x in 0..w as usize {
                let (px, py) = kms::turned(x, y, h as usize);
                assert_eq!(at(&turned, h as usize, px, py), at(frame.data(), w as usize, x, y), "turned ({x}, {y})");
                assert_eq!(at(&boxed, 60, x + 10, y + 12), at(frame.data(), w as usize, x, y), "placed ({x}, {y})");
            }
        }
        assert_eq!(at(&boxed, 60, 5, 5), [0, 0, 0, 255], "black around");
        assert_eq!(at(&boxed, 60, 55, 45), [0, 0, 0, 255]);
        let up = g.present_into(Pic::Screen(0), None, (60, 50), false, (10, 12), true);
        for y in 0..50 {
            assert_eq!(up[y * 240..(y + 1) * 240], boxed[(49 - y) * 240..(50 - y) * 240], "a window's row {y} is the panel's row {}", 49 - y);
        }
        let mut arrow = Pixmap::new(3, 2).unwrap();
        arrow.data_mut()[..8].copy_from_slice(&[200, 100, 50, 255, 64, 32, 0, 128]);
        let shown = g.present_into(Pic::Screen(0), Some(Arrow { pm: &arrow, at: (5, 7) }), (w, h), false, (0, 0), false);
        assert_eq!(at(&shown, w as usize, 5, 7), [200, 100, 50, 255], "the arrow's opaque pixel");
        let mut under = at(frame.data(), w as usize, 6, 7);
        crate::canvas::over(&mut under, &[64, 32, 0, 128]);
        assert_eq!(at(&shown, w as usize, 6, 7), under, "its translucent pixel laid over the screen");
        assert_eq!(at(&shown, w as usize, 7, 8), at(frame.data(), w as usize, 7, 8), "its transparent pixels show the screen");
    }

    // The GBM buffers through a run of frames: the first set at once, then
    // flips, each asked for once the one before has happened; a buffer goes
    // back to the surface only when it is off the screen, never the one on
    // it or the one a flip waits for; a driver that stops flipping sets
    // each frame, and the buffers before are free.
    #[test]
    fn a_buffer_goes_back_only_off_the_screen() {
        let mut st: (Option<u8>, Option<u8>) = (None, None);
        let (mut screen, mut pending) = (None, None);
        let mut freed = Vec::new();
        for (new, flipping) in [(b'A', false), (b'B', true), (b'C', true), (b'D', true), (b'E', false), (b'F', true)] {
            // The panel: the flip asked for before happens, then new is
            // flipped to or set.
            screen = pending.take().or(screen);
            if flipping {
                pending = Some(new);
            } else {
                screen = Some(new);
            }
            let off;
            (st.0, st.1, off) = advance(st.0, st.1, new, flipping);
            for b in &off {
                assert!(Some(*b) != screen && Some(*b) != pending, "{} freed while in use", *b as char);
            }
            assert_eq!(st, (screen, pending), "after {}", new as char);
            freed.extend(off);
        }
        assert_eq!(freed, b"ABCD", "each freed once, in order");
    }

    // The shader's division by 255 is the CPU's for every product it meets.
    #[test]
    fn the_shaders_rounding_is_the_cpus() {
        for d in 0..=255u32 {
            for a in 0..=255u32 {
                let x = d * (255 - a);
                let t = x + 128;
                assert_eq!((t + (t >> 8)) >> 8, (x + 127) / 255, "{d} under alpha {a}");
            }
        }
    }

    // Without libEGL, or on a device no GPU driver takes (where Mesa would
    // fall back to llvmpipe into dumb buffers), the GPU path says why and
    // the renderer goes on drawing on the CPU (main.rs).
    #[test]
    fn without_egl_or_a_gpu_device_it_says_why() {
        let e = Gpu::headless_from("/nonexistent/libEGL.so.1", 4, 4).err().expect("no EGL there");
        assert!(e.contains("/nonexistent/libEGL.so.1"), "{e}");
        if library("libgbm.so.1").is_err() {
            eprintln!("no libgbm: the GBM part skipped");
            return;
        }
        let null = std::fs::File::open("/dev/null").unwrap();
        let e = Gpu::on_device(std::os::fd::AsRawFd::as_raw_fd(&null), (4, 4), false, (0, 0), 4, 4).err().expect("/dev/null is no GPU");
        assert!(["gbm_", "EGL", "software rasterizer", "buffer"].iter().any(|w| e.contains(w)), "{e}");
    }

    // The GPU starts after the renderer's sandbox (main.rs): under its
    // seccomp filter and Landlock (only /usr readable; where the kernel has
    // it), Mesa still composes. In a child: the filter is the process's.
    #[test]
    fn composes_inside_the_sandbox() {
        if std::env::var_os("PAINT_GPU_SANDBOX_CHILD").is_some() {
            // SAFETY: a plain prctl in the test's own child process.
            unsafe { libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) };
            let landlock = kms::restrict_files().expect("landlock");
            crate::seccomp::apply().expect("seccomp");
            let Some(mut g) = gpu(32, 24) else { std::process::exit(0) };
            let (old, new) = (pic(32, 24, 4), pic(32, 24, 5));
            g.put_screen(0, &old);
            g.put_screen(1, &new);
            let plan = Plan::Slide { kind: Kind::Push, top: 2, bottom: 20 };
            let mut want = Pixmap::new(32, 24).unwrap();
            plan.cpu(&old, &new)(want.data_mut(), 0.5);
            g.transition(&plan, 0.5, 0, 1);
            eprintln!("sandboxed child: landlock {landlock}");
            std::process::exit(if g.read(Pic::Step) == want.data() { 0 } else { 7 });
        }
        let me = std::env::current_exe().unwrap();
        let st = std::process::Command::new(me)
            .args(["--exact", "gpu::tests::composes_inside_the_sandbox", "--nocapture"])
            .env("PAINT_GPU_SANDBOX_CHILD", "1")
            .status()
            .unwrap();
        assert_eq!(st.code(), Some(0), "the GPU did not compose inside the sandbox");
    }
}
