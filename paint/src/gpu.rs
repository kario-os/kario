//! The GPU path of the live display (PHONE_PAINT_GPU=1; 37-motion.md §5,
//! 42-gpu-rendering.md): the CPU still paints (layers.rs, split), the GPU
//! composes with GLES 2.
//! The layers are textures, of which only what was painted is uploaded;
//! one shader composes the screen from them into a picture of its own
//! with the CPU's integer sums, so it holds exactly `Layers::composed`; a
//! transition is a shader over the screens before and after
//! (motion::Plan), exactly motion.rs's rows; the screen goes to the panel
//! through a GBM surface and a page flip (kms.rs), turned or letterboxed
//! and through Settings › Accessibility's colour filters on the way (the
//! magnifier's picture, drawn by the CPU, in place of the screen). The GBM
//! surface is on the display's device or, where that cannot render, on
//! the GPU's render node (node.rs), whose buffers the display imports.
//! libEGL, libGLESv2 and libgbm are opened when it starts;
//! without them, without highp floats in fragment shaders, on a software
//! rasterizer (but in the test mode, PHONE_PAINT_GPU=soft), or on any
//! failure, the renderer draws on the CPU. Headless (tests, --bench, the
//! pixel selftest's timing) it renders without a surface
//! (EGL_MESA_platform_surfaceless). With layers rastered (L2,
//! PHONE_PAINT_GPU=raster) the CPU paints nothing: raster.rs paints the
//! jobs into the layers' textures, which are framebuffers for it.

use std::ffi::c_void;

use glow::HasContext;
use khronos_egl as egl;
use tiny_skia::Pixmap;

use crate::canvas::IRect;
use crate::gbm::{self, Bo, Gbm};
use crate::kms;
use crate::layers::{Layers, Onto};
use crate::look::{self, Look};
use crate::motion::{self, Kind, Plan};
use crate::node::Node;
use crate::raster::Raster;

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

/// The layers for the GPU path asked for.
pub fn layers(ask: Ask, width: u32, height: u32, fonts: &'static crate::canvas::Fonts) -> Layers {
    if ask == Ask::Raster {
        Layers::raster(width, height, fonts)
    } else {
        Layers::split(width, height, fonts)
    }
}

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
/// or at an origin, black around it, recoloured as look.rs's Filter
/// recolours (look.x: corrected by fix in linear sRGB, quantized to
/// LIN_STEPS before it is encoded; look.y: inverted), the pointer over it;
/// rows go up in a window.
const PRESENT: &str = "
uniform sampler2D frame, arrow;
uniform vec2 size;
uniform vec4 panel;
uniform vec2 origin;
uniform vec4 rect, at;
uniform vec2 look;
uniform mat3 fix;
vec3 linear(vec3 v) {
    v = v / 255.0;
    return mix(v / 12.92, pow((v + 0.055) / 1.055, vec3(2.4)), step(10.5 / 255.0, v));
}
vec3 encode(vec3 v) {
    vec3 i = floor(clamp(v, 0.0, 1.0) * 16384.0 + 0.5);
    vec3 e = i / 16384.0;
    e = mix(e * 12.92, 1.055 * pow(max(e, 1e-10), vec3(1.0 / 2.4)) - 0.055, step(51.5, i));
    return floor(e * 255.0 + 0.5);
}
void main() {
    vec2 q = floor(gl_FragCoord.xy);
    q.y = mix(q.y, panel.y - 1.0 - q.y, panel.w);
    vec2 p = mix(q - origin, vec2(q.y, size.y - 1.0 - q.x), panel.z);
    vec4 c = px(frame, p, size);
    if (look.x > 0.5) {
        c.rgb = encode(fix * linear(c.rgb));
    }
    if (look.y > 0.5) {
        vec3 v = c.rgb / 255.0;
        float y = 0.299 * v.r + 0.587 * v.g + 0.114 * v.b;
        c.rgb = floor(clamp(v + 1.0 - 2.0 * y, 0.0, 1.0) * 255.0 + 0.5);
    }
    c = mix(c, over(px(arrow, p - at.xy, at.zw), c), inside(rect, p));
    gl_FragColor = put(mix(vec4(0.0, 0.0, 0.0, 255.0), c, inside(vec4(0.0, 0.0, size), p)));
}
";

#[derive(Clone, Copy)]
pub struct Tex {
    pub id: glow::Texture,
    pub w: u32,
    pub h: u32,
}

/// A texture with a framebuffer to draw into it.
#[derive(Clone, Copy)]
pub struct Target {
    pub tex: Tex,
    pub fb: glow::Framebuffer,
}

/// Where the pictures go: nowhere (headless), or a GBM surface on the
/// panel with the buffers on and going to the screen, and their
/// framebuffers; split: the surface is on another device than the
/// display's (the GPU's render node), whose buffers the display imports.
struct Panel {
    gbm: Gbm,
    surface: egl::Surface,
    size: (u32, u32),
    turned: bool,
    origin: (usize, usize),
    fbs: Vec<(usize, drm::control::framebuffer::Handle)>,
    shown: Option<Bo>,
    queued: Option<Bo>,
    split: bool,
}

impl Panel {
    /// The display's framebuffer for a buffer of the surface, made the
    /// first time the surface gives it: from its handle on the display's
    /// device, or from the dmabuf it exports to it.
    fn framebuffer(&mut self, d: &mut kms::Display, bo: Bo) -> Result<drm::control::framebuffer::Handle, String> {
        if let Some((_, fb)) = self.fbs.iter().find(|(p, _)| *p == bo.ptr as usize) {
            return Ok(*fb);
        }
        let fb = if self.split {
            let dmabuf = self.gbm.export(bo)?;
            d.import_fb(std::os::fd::AsFd::as_fd(&dmabuf), bo.size, bo.pitch, bo.modifier)?
        } else {
            d.add_fb(bo.handle, bo.size, bo.pitch, bo.modifier)?
        };
        self.fbs.push((bo.ptr as usize, fb));
        Ok(fb)
    }
}

/// Which screen picture: one of the two composed (`Gpu::front` is the
/// last), the transition's step, or the magnifier's (`Gpu::put_zoomed`).
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Pic {
    Screen(usize),
    Step,
    Zoom,
}

/// The pointer for the panel: its picture (premultiplied, transparent
/// around the arrow) and where its corner is on the screen.
pub struct Arrow<'a> {
    pub pm: &'a Pixmap,
    pub at: (i32, i32),
}

/// What PHONE_PAINT_GPU, or the board without it (`choose`), asks for: the
/// CPU path (anything but these), the GPU path on a GPU ("1": it composes
/// what the CPU paints, L1; "raster": it paints the display list too, L2,
/// see layers), or L1 on a software rasterizer too ("soft"): the test
/// mode, for a device without a GPU driver (QEMU's virtio-gpu with Mesa's
/// kms_swrast), slower than the CPU path.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Ask {
    Cpu,
    Compose,
    Raster,
    Soft,
}

impl Ask {
    pub fn parse(v: Option<&str>) -> Ask {
        match v {
            Some("1") => Ask::Compose,
            Some("raster") => Ask::Raster,
            Some("soft") => Ask::Soft,
            _ => Ask::Cpu,
        }
    }

    /// PHONE_PAINT_GPU (env) if it is set, whatever it says; else the
    /// board's policy (sku.json's paint_gpu, frame::Device's gpu).
    pub fn choose(env: Option<&str>, board: &str) -> Ask {
        match (env, board) {
            (Some(_), _) => Ask::parse(env),
            (None, "compose") => Ask::Compose,
            (None, "raster") => Ask::Raster,
            (None, _) => Ask::Cpu,
        }
    }

    pub fn from_env() -> Ask {
        Ask::parse(std::env::var("PHONE_PAINT_GPU").ok().as_deref())
    }
}

/// Whether a GL renderer's name is one of Mesa's rasterizers on the CPU,
/// which it falls back to where no GPU driver takes the device.
fn software(renderer: &str) -> bool {
    let name = renderer.to_lowercase();
    ["llvmpipe", "softpipe", "swrast", "software"].iter().any(|s| name.contains(s))
}

/// PHONE_PAINT_GPU_FAIL=<n>: the GPU path fails on purpose at the n-th
/// frame it puts on the panel, as a swap or a flip can fail (a test of the
/// fallback to the CPU path).
#[derive(Debug, Default)]
struct Fail {
    at: Option<u64>,
    shown: u64,
}

impl Fail {
    fn parse(v: Option<&str>) -> Fail {
        Fail { at: v.and_then(|v| v.parse().ok()).filter(|&n| n > 0), shown: 0 }
    }

    /// One more frame for the panel.
    fn frame(&mut self) -> Result<(), String> {
        self.shown += 1;
        match self.at {
            Some(n) if n == self.shown => Err(format!("failing on purpose at frame {n} (PHONE_PAINT_GPU_FAIL)")),
            _ => Ok(()),
        }
    }
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
    quad: glow::Buffer,
    size: (u32, u32),
    page: Target,
    top: Target,
    bands: Vec<(i32, Target)>,
    slides: Vec<(u64, Tex, IRect)>,
    spare: Vec<Target>,
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
    /// L2: what paints rastered layers, made when first needed; whether it
    /// is to read the pixel under by framebuffer fetch.
    raster: Option<Raster>,
    fetch: bool,
    fail: Fail,
    /// Settings › Accessibility's look: the filters the present pass
    /// applies; the magnifier's picture and the copy of it last uploaded;
    /// whether either changed since the panel was last given a frame.
    look: Look,
    zoom: Option<(Target, Pixmap)>,
    restyled: bool,
    _gles: Option<libloading::Library>,
    #[cfg(test)]
    _turn: Option<turn::Turn>,
}

/// Tests' headless contexts take turns: Mesa 23.1's llvmpipe crashes
/// (lp_scene_is_resource_referenced, from glCopyTexSubImage2D) when two
/// contexts of the process copy at once. The renderer has one context.
#[cfg(test)]
mod turn {
    use std::sync::{Condvar, Mutex, PoisonError};
    use std::thread::{self, ThreadId};

    static HELD: Mutex<Option<(ThreadId, usize)>> = Mutex::new(None);
    static FREE: Condvar = Condvar::new();

    pub struct Turn;

    /// Waits for no other thread to hold a context; a thread may hold
    /// several.
    pub fn take() -> Turn {
        let me = thread::current().id();
        let mut held = HELD.lock().unwrap_or_else(PoisonError::into_inner);
        loop {
            match &mut *held {
                None => *held = Some((me, 1)),
                Some((t, n)) if *t == me => *n += 1,
                Some(_) => {
                    held = FREE.wait(held).unwrap_or_else(PoisonError::into_inner);
                    continue;
                }
            }
            return Turn;
        }
    }

    impl Drop for Turn {
        fn drop(&mut self) {
            let mut held = HELD.lock().unwrap_or_else(PoisonError::into_inner);
            if let Some((_, n)) = &mut *held {
                *n -= 1;
                if *n == 0 {
                    *held = None;
                    FREE.notify_all();
                }
            }
        }
    }
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
        #[cfg(test)]
        let turn = turn::take();
        let egl = load_egl(lib)?;
        // SAFETY: the default display of the surfaceless platform.
        let dpy = unsafe { egl.get_platform_display(PLATFORM_SURFACELESS, egl::DEFAULT_DISPLAY, &[egl::ATTRIB_NONE]) }.map_err(es)?;
        egl.initialize(dpy).map_err(es)?;
        let (_, ctx) = context(&egl, dpy, 0, None)?;
        egl.make_current(dpy, None, None, Some(ctx)).map_err(es)?;
        #[cfg_attr(not(test), allow(unused_mut))]
        let mut g = Self::setup(egl, dpy, ctx, None, width, height)?;
        #[cfg(test)]
        {
            g._turn = Some(turn);
        }
        Ok(g)
    }

    /// On the panel: a GBM surface of the mode's size on the display's
    /// device, or on the GPU's render node (its buffers linear, imported
    /// by the display), the screens drawn width × height and put on it as
    /// the display turns or places them; on a software rasterizer only if
    /// soft.
    pub fn on_panel(d: &mut kms::Display, render: Option<&Node>, width: u32, height: u32, soft: bool) -> Result<Gpu, String> {
        let fd = render.map_or(d.fd(), Node::fd);
        let (size, turned, origin) = ((d.width, d.height), d.rotated, d.origin);
        let mut g = Self::on_device(fd, render.is_some(), Some(d), size, turned, origin, width, height, soft)?;
        g.fail = Fail::parse(std::env::var("PHONE_PAINT_GPU_FAIL").ok().as_deref());
        Ok(g)
    }

    /// split: fd is not the display's device; d: the display, to check the
    /// surface's first buffer becomes a framebuffer on it.
    #[allow(clippy::too_many_arguments)]
    fn on_device(fd: std::os::fd::RawFd, split: bool, d: Option<&mut kms::Display>, size: (u32, u32), turned: bool, origin: (usize, usize), width: u32, height: u32, soft: bool) -> Result<Gpu, String> {
        let gbm = Gbm::open(fd, size.0, size.1, split)?;
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
        let panel = Panel { gbm, surface, size, turned, origin, fbs: Vec::new(), shown: None, queued: None, split };
        let mut g = Self::setup(egl, dpy, ctx, Some(panel), width, height)?;
        // Where no GPU driver takes the device, Mesa renders with llvmpipe
        // or softpipe into dumb buffers (kms_swrast): slower than the CPU
        // path, llvmpipe on many threads.
        let name = g.renderer();
        if software(&name) && !soft {
            return Err(format!("{name} is a software rasterizer (PHONE_PAINT_GPU=soft composes on it, for tests)"));
        }
        g.probe(d)?;
        Ok(g)
    }

    /// The GL renderer's name (the GPU and its driver).
    pub fn renderer(&self) -> String {
        // SAFETY: a GL call on the current context.
        unsafe { self.gl.get_parameter_string(glow::RENDERER) }
    }

    /// A first frame through the GBM surface, not shown: the surface can
    /// give buffers that the display takes as framebuffers.
    fn probe(&mut self, d: Option<&mut kms::Display>) -> Result<(), String> {
        let Some(p) = self.panel.as_ref() else { return Ok(()) };
        // SAFETY: GL calls on the current context.
        unsafe {
            self.bind(None, p.size.0, p.size.1);
            self.gl.clear_color(0.0, 0.0, 0.0, 1.0);
            self.gl.clear(glow::COLOR_BUFFER_BIT);
        }
        self.egl.swap_buffers(self.dpy, p.surface).map_err(es)?;
        let bo = p.gbm.lock_front().ok_or("the GBM surface gave no buffer")?;
        let p = self.panel.as_mut().expect("panel");
        let fb = d.map_or(Ok(()), |d| p.framebuffer(d, bo).map(|_| ()));
        p.gbm.release(bo);
        fb
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
            let fetch = gl.version().major >= 3 && gl.supported_extensions().contains("GL_EXT_shader_framebuffer_fetch") && std::env::var("PHONE_PAINT_RASTER_FETCH").map_or(true, |v| v != "0");
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
                    target(&gl, width, height)?,
                    target(&gl, width, height)?,
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
                quad,
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
                raster: None,
                fetch,
                fail: Fail::default(),
                look: Look { invert: false, correct: None, zoom: 0.0 },
                zoom: None,
                restyled: false,
                _gles: gles,
                #[cfg(test)]
                _turn: None,
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
        let (w, h) = layers.size();
        if (w, h) != self.size {
            return Err(format!("layers of {w}x{h} for screens of {}x{}", self.size.0, self.size.1));
        }
        let whole = IRect::new(0, 0, w as i32, h as i32);
        let fresh = std::mem::take(&mut self.fresh);
        self.uploaded = 0;
        // SAFETY: GL calls on the current context; the uploads read inside
        // the pictures (rectangles clipped to them).
        unsafe {
            if layers.rastered() {
                self.paint(layers)?;
            } else {
                let dirty = layers.take_dirty();
                let top = layers.top().ok_or("the layers are not split")?;
                for (pm, tex, rects) in [(layers.page(), self.page.tex, &dirty.page), (top, self.top.tex, &dirty.top)] {
                    let rects: &[IRect] = if fresh { std::slice::from_ref(&whole) } else { rects };
                    for r in rects {
                        self.uploaded += self.upload(tex, pm, *r, (0, 0));
                    }
                }
                let keep: Vec<i32> = layers.bands().iter().map(|(k, _)| *k).collect();
                let (kept, gone): (Vec<_>, Vec<_>) = std::mem::take(&mut self.bands).into_iter().partition(|(k, t)| keep.contains(k) && layers.bands().iter().any(|(j, pm)| j == k && (pm.width(), pm.height()) == (t.tex.w, t.tex.h)));
                self.bands = kept;
                self.spare.extend(gone.into_iter().map(|(_, t)| t));
                for (k, pm) in layers.bands() {
                    let band = IRect::new(0, 0, pm.width() as i32, pm.height() as i32);
                    let new = !self.bands.iter().any(|(j, _)| j == k);
                    if new {
                        let t = self.band_target(pm.width(), pm.height())?;
                        self.bands.push((*k, t));
                    }
                    let t = self.bands.iter().find(|(j, _)| j == k).map(|(_, t)| t.tex).expect("band texture");
                    if new {
                        self.uploaded += self.upload(t, pm, band, (0, 0));
                    } else {
                        for (_, r) in dirty.bands.iter().filter(|(j, _)| j == k) {
                            self.uploaded += self.upload(t, pm, *r, (0, 0));
                        }
                    }
                }
            }
            for t in self.spare.split_off(self.spare.len().min(2)) {
                self.gl.delete_framebuffer(t.fb);
                self.gl.delete_texture(t.tex.id);
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
                        band_tex[i] = t.tex;
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
            let units = [("page", self.page.tex), ("top", self.top.tex), ("band0", band_tex[0]), ("band1", band_tex[1]), ("slide0", slide_tex[0]), ("slide1", slide_tex[1])];
            self.draw(p, &units);
        }
        self.front = 1 - self.front;
        Ok(true)
    }

    /// A target for a band of the body, a spare one if it fits.
    unsafe fn band_target(&mut self, w: u32, h: u32) -> Result<Target, String> {
        match self.spare.iter().position(|t| (t.tex.w, t.tex.h) == (w, h)) {
            Some(i) => Ok(self.spare.swap_remove(i)),
            None => target(&self.gl, w, h),
        }
    }

    /// L2: the bands' targets brought to the layers' bands, and the jobs
    /// painted into the targets.
    unsafe fn paint(&mut self, layers: &mut Layers) -> Result<(), String> {
        let (w, h) = self.size;
        let keep = layers.band_numbers();
        let (kept, gone): (Vec<_>, Vec<_>) = std::mem::take(&mut self.bands).into_iter().partition(|(k, _)| keep.contains(k));
        self.bands = kept;
        self.spare.extend(gone.into_iter().map(|(_, t)| t));
        for k in keep {
            if !self.bands.iter().any(|(j, _)| *j == k) {
                let t = self.band_target(w, h)?;
                self.bands.push((k, t));
            }
        }
        let (jobs, fonts) = layers.take_jobs();
        if self.raster.is_none() {
            let r = match Raster::new(&self.gl, w, h, self.fetch, self.row_length) {
                Err(e) if self.fetch => {
                    eprintln!("phone-paint: gpu: framebuffer fetch: {e}; copying the pixels under instead");
                    self.fetch = false;
                    Raster::new(&self.gl, w, h, false, self.row_length)
                }
                r => r,
            };
            self.raster = Some(r?);
        }
        let r = self.raster.as_mut().expect("raster");
        r.inexact.retain(|(o, _)| !matches!(o, Onto::Band(k) if !self.bands.iter().any(|(j, _)| j == k)));
        let (page, top, bands) = (self.page, self.top, &self.bands);
        let target = |o: Onto| match o {
            Onto::Page => Some(page),
            Onto::Top => Some(top),
            Onto::Band(k) => bands.iter().find(|(j, _)| *j == k).map(|(_, t)| *t),
        };
        let res = self.raster.as_mut().expect("raster").run(&self.gl, &jobs, fonts, &target);
        self.gl.bind_buffer(glow::ARRAY_BUFFER, Some(self.quad));
        self.gl.vertex_attrib_pointer_f32(0, 2, glow::FLOAT, false, 8, 0);
        res
    }

    /// L2's painter (made by the first rastered compose).
    pub fn raster(&mut self) -> Option<&mut Raster> {
        self.raster.as_mut()
    }

    /// L2 without framebuffer fetch (copies of the pixels under instead),
    /// from the next rastered compose on (tests).
    #[cfg(test)]
    pub fn copy_under(&mut self) {
        (self.fetch, self.raster) = (false, None);
    }

    /// A layer's texture read back (RGBA rows from the top): the page, the
    /// top layer, or a band of the body.
    pub fn read_layer(&self, o: Onto) -> Option<Vec<u8>> {
        let t = match o {
            Onto::Page => self.page,
            Onto::Top => self.top,
            Onto::Band(k) => self.bands.iter().find(|(j, _)| *j == k)?.1,
        };
        let mut out = vec![0u8; (t.tex.w * t.tex.h * 4) as usize];
        // SAFETY: GL calls on the current context; out holds the rows.
        unsafe {
            self.gl.bind_framebuffer(glow::FRAMEBUFFER, Some(t.fb));
            self.gl.read_pixels(0, 0, t.tex.w as i32, t.tex.h as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelPackData::Slice(Some(&mut out)));
        }
        Some(out)
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
            Pic::Zoom => self.zoom.as_ref().map_or(self.screens[self.front], |z| z.0),
        }
    }

    /// The picture the panel is to show of the screen composed last: the
    /// magnifier's while it is on.
    pub fn shown(&self) -> Pic {
        if self.zoom.is_some() {
            Pic::Zoom
        } else {
            Pic::Screen(self.front)
        }
    }

    /// The colour filters of a frame's look, from the next frame shown on.
    pub fn set_look(&mut self, look: Look) {
        let filters = |l: &Look| (l.invert, l.correct);
        self.restyled |= filters(&look) != filters(&self.look);
        self.look = look;
    }

    /// The magnifier's picture of the frame (main.rs: the frame drawn
    /// again zoomed and cut around the focus, by the CPU as on the CPU
    /// path), shown in place of the composed screen: only what differs
    /// from the picture before is uploaded.
    pub fn put_zoomed(&mut self, pm: &Pixmap) -> Result<(), String> {
        let (w, h) = self.size;
        if (pm.width(), pm.height()) != (w, h) {
            return Err(format!("a magnified picture of {}x{} for screens of {w}x{h}", pm.width(), pm.height()));
        }
        // SAFETY: GL calls on the current context; uploads inside pm.
        unsafe {
            let (t, r) = match self.zoom.take() {
                Some((t, last)) => (t, kms::damage(last.data(), pm.data(), w as usize, h as usize)),
                None => (target(&self.gl, w, h)?, Some(kms::Rect { x0: 0, y0: 0, x1: w as usize, y1: h as usize })),
            };
            if let Some(r) = r {
                self.uploaded += self.upload(t.tex, pm, IRect::new(r.x0 as i32, r.y0 as i32, r.x1 as i32, r.y1 as i32), (0, 0));
                self.restyled = true;
            }
            self.zoom = Some((t, pm.clone()));
        }
        Ok(())
    }

    /// The magnifier is off: the composed screen is shown again.
    pub fn drop_zoom(&mut self) {
        if let Some((t, _)) = self.zoom.take() {
            // SAFETY: GL calls on the current context.
            unsafe {
                self.gl.delete_framebuffer(t.fb);
                self.gl.delete_texture(t.tex.id);
            }
            self.restyled = true;
        }
    }

    /// Whether the look changed since the panel was last given a frame:
    /// the frame is to be shown again even if the screen did not change.
    pub fn restyled(&self) -> bool {
        self.restyled
    }

    /// Puts a screen picture on the panel, with the pointer over it: drawn
    /// into the GBM surface's next buffer, which a page flip shows (the
    /// buffer shown before the last flip goes back to the surface).
    pub fn show(&mut self, d: &mut kms::Display, which: Pic, arrow: Option<Arrow>) -> Result<(), String> {
        let Some(panel) = self.panel.as_ref() else { return Ok(()) };
        let (size, turned, origin, surface, split) = (panel.size, panel.turned, panel.origin, panel.surface, panel.split);
        self.fail.frame()?;
        let src = self.pic(which);
        // SAFETY: GL calls on the current context.
        unsafe {
            self.bind(None, size.0, size.1);
            self.draw_present(src, arrow, size, turned, origin, true)?;
            // The display is another driver, which may not wait for the
            // GPU's fence on a buffer it imported: the frame is finished
            // before it is flipped to.
            if split {
                self.gl.finish();
            }
        }
        self.egl.swap_buffers(self.dpy, surface).map_err(es)?;
        self.restyled = false;
        let panel = self.panel.as_mut().expect("panel");
        let bo = panel.gbm.lock_front().ok_or("gbm_surface_lock_front_buffer failed")?;
        let fb = panel.framebuffer(d, bo).inspect_err(|_| panel.gbm.release(bo))?;
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
        self.drop_zoom();
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
        let flag = |on: bool| if on { 1.0 } else { 0.0 };
        self.uniform2(p, "look", flag(self.look.correct.is_some()), flag(self.look.invert));
        if let Some(cvd) = self.look.correct {
            // GLSL takes a matrix by columns; f32 as look.rs's Filter.
            let m = look::correction(cvd);
            let cols: Vec<f32> = (0..9).map(|k| m[k % 3][k / 3] as f32).collect();
            self.gl.uniform_matrix_3_f32_slice(self.gl.get_uniform_location(p, "fix").as_ref(), false, &cols);
        }
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
    pub fn present_into(&mut self, which: Pic, arrow: Option<Arrow>, size: (u32, u32), turned: bool, origin: (usize, usize), up: bool) -> Vec<u8> {
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
        upload(&self.gl, self.row_length, t, pm, r, at)
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

/// Uploads rectangle r of pm (clipped to it) into t, where (r - at) lands;
/// row_length: the GL can read a rectangle out of longer rows. Returns the
/// pixels uploaded.
pub unsafe fn upload(gl: &glow::Context, row_length: bool, t: Tex, pm: &Pixmap, r: IRect, at: (i32, i32)) -> usize {
    let sw = pm.width() as usize;
    let r = r.intersect(&IRect::new(0, 0, pm.width() as i32, pm.height() as i32)).intersect(&IRect::new(at.0, at.1, at.0 + t.w as i32, at.1 + t.h as i32));
    if r.is_empty() {
        return 0;
    }
    let (x, y, w, h) = (r.x0 as usize, r.y0 as usize, (r.x1 - r.x0) as usize, (r.y1 - r.y0) as usize);
    let data = pm.data();
    gl.bind_texture(glow::TEXTURE_2D, Some(t.id));
    let put = |px: &[u8]| gl.tex_sub_image_2d(glow::TEXTURE_2D, 0, r.x0 - at.0, r.y0 - at.1, w as i32, h as i32, glow::RGBA, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(Some(px)));
    if w == sw || row_length {
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

unsafe fn texture(gl: &glow::Context, w: u32, h: u32) -> Result<Tex, String> {
    texture_of(gl, w, h, glow::RGBA)
}

/// A texture of w x h in format (RGBA, LUMINANCE), nearest, left bound.
pub unsafe fn texture_of(gl: &glow::Context, w: u32, h: u32, format: u32) -> Result<Tex, String> {
    let id = gl.create_texture()?;
    gl.bind_texture(glow::TEXTURE_2D, Some(id));
    for p in [glow::TEXTURE_MIN_FILTER, glow::TEXTURE_MAG_FILTER] {
        gl.tex_parameter_i32(glow::TEXTURE_2D, p, glow::NEAREST as i32);
    }
    for p in [glow::TEXTURE_WRAP_S, glow::TEXTURE_WRAP_T] {
        gl.tex_parameter_i32(glow::TEXTURE_2D, p, glow::CLAMP_TO_EDGE as i32);
    }
    gl.tex_image_2d(glow::TEXTURE_2D, 0, format as i32, w.max(1) as i32, h.max(1) as i32, 0, format, glow::UNSIGNED_BYTE, glow::PixelUnpackData::Slice(None));
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
    program_from(gl, VERTEX, &format!("{COMMON}{fragment}"), &["pos"])
}

/// A program of the two shaders, its attributes at locations 0, 1, ... in
/// the order named.
pub unsafe fn program_from(gl: &glow::Context, vertex: &str, fragment: &str, attributes: &[&str]) -> Result<glow::Program, String> {
    let p = gl.create_program()?;
    for (kind, src) in [(glow::VERTEX_SHADER, vertex), (glow::FRAGMENT_SHADER, fragment)] {
        let s = gl.create_shader(kind)?;
        gl.shader_source(s, src);
        gl.compile_shader(s);
        if !gl.get_shader_compile_status(s) {
            return Err(format!("shader: {}", gl.get_shader_info_log(s)));
        }
        gl.attach_shader(p, s);
        gl.delete_shader(s);
    }
    for (i, a) in attributes.iter().enumerate() {
        gl.bind_attrib_location(p, i as u32, a);
    }
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

    /// Every grey, ramps of the primaries and secondaries, then colours
    /// from pic's hash: opaque.
    fn colours(w: u32, h: u32) -> Pixmap {
        let mut pm = pic(w, h, 9);
        for (i, px) in pm.data_mut().chunks_exact_mut(4).enumerate().take(256 * 8) {
            let v = (i % 256) as u8;
            let rgb = [[v, v, v], [v, 0, 0], [0, v, 0], [0, 0, v], [v, v, 0], [0, v, v], [v, 0, v], [255, v, 255 - v]][i / 256];
            px.copy_from_slice(&[rgb[0], rgb[1], rgb[2], 255]);
        }
        pm
    }

    // Settings › Accessibility's colour filters in the present pass recolour
    // as look.rs's Filter does on the CPU (inversion, each correction, a
    // correction and inversion): within a level, the GPU's pow and the
    // CPU's differing at the edge of a rounding, which few pixels meet.
    // The pointer is laid over the recoloured screen, not recoloured.
    #[test]
    fn the_filters_are_the_cpus() {
        use crate::look::Cvd;
        let (w, h) = (256, 128);
        let Some(mut g) = gpu(w, h) else { return };
        let pm = colours(w, h);
        g.put_screen(0, &pm);
        let plain = Look { invert: false, correct: None, zoom: 0.0 };
        for look in [
            Look { invert: true, ..plain },
            Look { correct: Some(Cvd::Protan), ..plain },
            Look { correct: Some(Cvd::Deutan), ..plain },
            Look { correct: Some(Cvd::Tritan), ..plain },
            Look { invert: true, correct: Some(Cvd::Deutan), ..plain },
        ] {
            g.set_look(look);
            assert!(g.restyled(), "{look:?}: a new look is shown even if the screen did not change");
            let got = g.present_into(Pic::Screen(0), None, (w, h), false, (0, 0), false);
            let mut want = pm.clone();
            look.filter(&mut want);
            let (mut worst, mut off) = (0u8, 0usize);
            for (a, b) in got.chunks(4).zip(want.data().chunks(4)) {
                let d = (0..4).map(|i| a[i].abs_diff(b[i])).max().unwrap();
                worst = worst.max(d);
                off += usize::from(d > 0);
            }
            let n = (w * h) as usize;
            assert!(worst <= 1 && off * 200 <= n, "{look:?}: {off} of {n} pixels differ, by up to {worst}");
        }
        let mut arrow = Pixmap::new(2, 1).unwrap();
        arrow.data_mut().copy_from_slice(&[200, 100, 50, 255, 0, 0, 0, 0]);
        g.set_look(Look { invert: true, ..plain });
        let shown = g.present_into(Pic::Screen(0), Some(Arrow { pm: &arrow, at: (3, 2) }), (w, h), false, (0, 0), false);
        let at = ((2 * w + 3) * 4) as usize;
        assert_eq!(shown[at..at + 4], [200, 100, 50, 255], "the pointer is not inverted");
        g.set_look(plain);
        assert_eq!(g.present_into(Pic::Screen(0), None, (w, h), false, (0, 0), false), pm.data(), "no filter: the screen as it is");
    }

    // The magnifier's picture is shown in place of the composed screen,
    // as the CPU drew it; a picture that changed uploads what changed; off,
    // the screen is shown again.
    #[test]
    fn the_magnifiers_picture_takes_the_screens_place() {
        let (w, h) = (40, 30);
        let Some(mut g) = gpu(w, h) else { return };
        let screen = pic(w, h, 1);
        g.put_screen(0, &screen);
        assert_eq!(g.shown(), Pic::Screen(0));
        let mut zoomed = pic(w, h, 2);
        g.put_zoomed(&zoomed).unwrap();
        assert_eq!((g.shown(), g.uploaded, g.restyled()), (Pic::Zoom, (w * h) as usize, true));
        assert_eq!(g.present_into(g.shown(), None, (w, h), false, (0, 0), false), zoomed.data());
        g.restyled = false;
        g.uploaded = 0;
        g.put_zoomed(&zoomed).unwrap();
        assert_eq!((g.uploaded, g.restyled()), (0, false), "the same picture: nothing to do");
        zoomed.data_mut()[(5 * w as usize + 7) * 4] ^= 0xff;
        g.put_zoomed(&zoomed).unwrap();
        assert_eq!((g.uploaded, g.restyled()), (1, true), "one pixel changed");
        assert_eq!(g.present_into(g.shown(), None, (w, h), false, (0, 0), false), zoomed.data());
        assert!(g.put_zoomed(&pic(w + 1, h, 3)).is_err(), "not the screens' size");
        g.drop_zoom();
        assert_eq!(g.shown(), Pic::Screen(0));
        assert_eq!(g.present_into(g.shown(), None, (w, h), false, (0, 0), false), screen.data());
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
        let fd = std::os::fd::AsRawFd::as_raw_fd(&null);
        let e = Gpu::on_device(fd, false, None, (4, 4), false, (0, 0), 4, 4, false).err().expect("/dev/null is no GPU");
        assert!(["gbm_", "EGL", "software rasterizer", "buffer"].iter().any(|w| e.contains(w)), "{e}");
        // In the test mode the rasterizer is let through, and the first
        // frame finds that /dev/null gives no scan-out buffers, linear for
        // another device or not.
        for split in [false, true] {
            let e = Gpu::on_device(fd, split, None, (4, 4), false, (0, 0), 4, 4, true).err().expect("/dev/null has no buffers");
            assert!(!e.contains("software rasterizer"), "{e}");
        }
    }

    // PHONE_PAINT_GPU: "1" and "raster" for a GPU, "soft" for Mesa's
    // rasterizers on the CPU too (the test mode); anything else is the CPU
    // path. Set at all, it overrides the board's policy (sku.json's
    // paint_gpu), which is off unless it says compose or raster.
    #[test]
    fn the_test_mode_takes_a_software_rasterizer() {
        assert_eq!(
            [None, Some(""), Some("0"), Some("yes"), Some("1"), Some("raster"), Some("soft")].map(Ask::parse),
            [Ask::Cpu, Ask::Cpu, Ask::Cpu, Ask::Cpu, Ask::Compose, Ask::Raster, Ask::Soft]
        );
        assert_eq!(["", "off", "compose", "raster", "soft", "1"].map(|b| Ask::choose(None, b)), [Ask::Cpu, Ask::Cpu, Ask::Compose, Ask::Raster, Ask::Cpu, Ask::Cpu]);
        assert_eq!(Ask::choose(Some("0"), "raster"), Ask::Cpu, "the environment says the CPU");
        assert_eq!(Ask::choose(Some(""), "compose"), Ask::Cpu);
        assert_eq!(Ask::choose(Some("1"), "raster"), Ask::Compose);
        assert_eq!(Ask::choose(Some("soft"), "off"), Ask::Soft);
        for name in ["llvmpipe (LLVM 19.1.7, 128 bits)", "softpipe", "Software Rasterizer", "virgl (llvmpipe (LLVM 15.0.7, 256 bits))"] {
            assert!(software(name), "{name}");
        }
        for name in ["FD630", "Adreno (TM) 630", "Mali-G52 (Panfrost)", "virgl (Adreno (TM) 630)"] {
            assert!(!software(name), "{name}");
        }
    }

    // PHONE_PAINT_GPU_FAIL=<n>: the n-th frame for the panel fails, once;
    // without a positive number, none does.
    #[test]
    fn a_failure_on_purpose_comes_at_its_frame() {
        let mut f = Fail::parse(Some("3"));
        let got: Vec<bool> = (0..6).map(|_| f.frame().is_ok()).collect();
        assert_eq!(got, [true, true, false, true, true, true]);
        for v in [None, Some(""), Some("0"), Some("x")] {
            let mut f = Fail::parse(v);
            assert!((0..100).all(|_| f.frame().is_ok()), "{v:?}");
        }
    }

    // The GPU starts after the renderer's sandbox (main.rs): under its
    // seccomp filter and Landlock (only /usr readable; where the kernel has
    // it), Mesa still composes. In a child: the filter is the process's.
    #[test]
    fn composes_inside_the_sandbox() {
        if std::env::var_os("PAINT_GPU_SANDBOX_CHILD").is_some() {
            // SAFETY: a plain prctl in the test's own child process.
            unsafe { libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) };
            let landlock = kms::restrict_files(&kms::Reads::renderer(&[])).expect("landlock");
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
