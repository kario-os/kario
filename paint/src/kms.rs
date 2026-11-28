//! The device mode: frames go into a DRM/KMS dumb buffer on the panel, and
//! keys come straight from evdev (no libinput, no xkb: a phone keypad and a
//! US layout for USB keyboards are all a phone needs).

use std::collections::{HashMap, VecDeque};
use std::fs::OpenOptions;
use std::os::fd::{AsFd, AsRawFd, BorrowedFd};
use std::sync::mpsc::Sender;
use std::time::{Duration, Instant};

use drm::buffer::{Buffer, DrmFourcc, DrmModifier, PlanarBuffer};
use drm::control::{connector, crtc, framebuffer, ClipRect, Device as ControlDevice, FbCmd2Flags, Mode, ModeFlags, ResourceHandle};
use drm::{ClientCapability, Device as _, DriverCapability};
use evdev::{EventSummary, KeyCode, RelativeAxisCode};

use crate::input::Event;
use crate::keygate::{Gate, KeyTiming, Timing};

struct Card(std::fs::File);

impl AsFd for Card {
    fn as_fd(&self) -> BorrowedFd<'_> {
        self.0.as_fd()
    }
}
impl drm::Device for Card {}
impl ControlDevice for Card {}

pub struct Display {
    card: Card,
    /// Two scan-out buffers in turn: a frame goes into the one not on
    /// screen and a page flip shows it at the next vertical blank, so
    /// nothing tears. Drivers that cannot flip get the first buffer only,
    /// written in place. `held` knows what each holds.
    bufs: Vec<Scanout>,
    held: Held,
    /// The framebuffer on the panel: a dumb buffer, or the GPU's (gpu.rs).
    shown: framebuffer::Handle,
    flips: bool,
    /// The flips asked for whose completion events have not come yet.
    flip: Flips,
    /// Page flips refused in a row, and whether the first refusal's
    /// report went to the log.
    refused: Refused,
    reported: bool,
    /// The framebuffers this program made (the dumb buffers', the GPU's).
    ours: Vec<framebuffer::Handle>,
    path: &'static str,
    /// Page flips the driver took, and their completion events read
    /// (PHONE_PAINT_TRACE).
    pub flips_asked: u64,
    pub flips_done: u64,
    /// The flips asked for that completed (their number in flips_asked)
    /// and when, the latest last (perf.rs takes them); the event's time
    /// where the driver stamps it on CLOCK_MONOTONIC, else when read.
    pub flipped: VecDeque<(u64, Duration)>,
    monotonic: bool,
    /// Frames put on the panel, flipped to or drawn in place, and the
    /// flip that shows the last one (None: drawn in place, or set).
    pub puts: u64,
    pub put_flip: Option<u64>,
    /// Whether flips complete at vertical blanks: their events count them.
    /// A driver without (virtio-gpu) completes a flip at once and its
    /// events all say frame 0; frames are then paced by a timer
    /// (`wait_frame`).
    vblank: bool,
    last_seq: Option<u32>,
    /// When the last frame was handed over, and the time spent waiting for
    /// flips or the timer since `waited` was last asked.
    shown_at: Option<Instant>,
    wait: Duration,
    pub width: u32,
    pub height: u32,
    /// The panel is the device turned sideways (a portrait panel in a
    /// landscape phone): frames are drawn landscape and turned a quarter
    /// clockwise onto it.
    pub rotated: bool,
    /// Where the frame's top left corner sits on the panel: the middle of a
    /// screen bigger than the device's (`letterbox`), (0, 0) otherwise.
    pub origin: (usize, usize),
    crtc: crtc::Handle,
    con: connector::Handle,
    mode: Mode,
    /// The connector's modes: those of other rates at mode's size are
    /// what set_rate switches between.
    modes: Vec<Mode>,
    /// The panel is lit (the shell turns it off to save power).
    pub on: bool,
}

/// A scan-out buffer, mapped for as long as the program runs: mapped a
/// frame at a time, every frame faults its pages in again.
struct Scanout {
    map: drm::control::dumbbuffer::DumbMapping<'static>,
    fb: framebuffer::Handle,
    pitch: usize,
}

fn s(e: impl std::fmt::Display) -> String {
    e.to_string()
}

/// How long a power change or a flip the driver says is still on its way
/// waits for the flips before it.
const SETTLE: Duration = Duration::from_millis(500);

/// The completed flips kept for perf.rs, which takes them after each frame.
const FLIPPED: usize = 64;

/// The page flips on their way: the last one asked for whose completion
/// event has not come (pending), and those given up on whose events may
/// still come (owed). The events come in the order of the flips, so a late
/// one pays for a flip given up on: taken for the next flip's, it had the
/// renderer draw into the buffer still on the panel (a torn, garbled
/// frame) and the next flip fail with EBUSY, after which it drew in place
/// for good.
#[derive(Default, Debug, PartialEq)]
struct Flips {
    pending: bool,
    owed: u32,
}

impl Flips {
    fn asked(&mut self) {
        self.pending = true;
    }

    /// The wait for the last flip ran out: its event is owed.
    fn gave_up(&mut self) {
        if self.pending {
            self.pending = false;
            self.owed += 1;
        }
    }

    /// A completion event: whether it was the last flip's.
    fn event(&mut self) -> bool {
        if self.owed > 0 {
            self.owed -= 1;
            return false;
        }
        std::mem::take(&mut self.pending)
    }

    fn busy(&self) -> bool {
        self.pending || self.owed > 0
    }
}

impl Display {
    pub fn open() -> Result<Display, String> {
        let mut last = "no /dev/dri/card*".to_string();
        for path in ["/dev/dri/card0", "/dev/dri/card1"] {
            let Ok(f) = OpenOptions::new().read(true).write(true).open(path) else { continue };
            match Self::setup(Card(f), path) {
                Ok(d) => return Ok(d),
                Err(e) => last = format!("{path}: {e}"),
            }
        }
        Err(last)
    }

    fn setup(card: Card, path: &'static str) -> Result<Display, String> {
        let res = card.resource_handles().map_err(s)?;
        let con = res
            .connectors()
            .iter()
            .filter_map(|&h| card.get_connector(h, false).ok())
            .find(|c| c.state() == connector::State::Connected)
            .ok_or("no connected display")?;
        let mode = *con.modes().first().ok_or("the display has no mode")?;
        let (w, h) = mode.size();
        let current = con.current_encoder().and_then(|e| card.get_encoder(e).ok()).and_then(|e| e.crtc());
        let possible: Vec<Vec<crtc::Handle>> = con.encoders().iter().filter_map(|&e| card.get_encoder(e).ok()).map(|e| res.filter_crtcs(e.possible_crtcs())).collect();
        let crtc = crtc_for(current, &possible, res.crtcs()).ok_or("no CRTC drives the display")?;
        let mut bufs = Vec::new();
        for _ in 0..2 {
            let db = card.create_dumb_buffer((w as u32, h as u32), DrmFourcc::Xrgb8888, 32).map_err(s)?;
            let fb = card.add_framebuffer(&db, 24, 32).map_err(s)?;
            let pitch = db.pitch() as usize;
            let map = card.map_dumb_buffer(Box::leak(Box::new(db))).map_err(s)?;
            bufs.push(Scanout { map, fb, pitch });
        }
        card.set_crtc(crtc, Some(bufs[0].fb), (0, 0), &[con.handle()], Some(mode)).map_err(s)?;
        let monotonic = card.get_driver_capability(DriverCapability::MonotonicTimestamp).is_ok_and(|v| v == 1);
        Ok(Display {
            card,
            shown: bufs[0].fb,
            ours: bufs.iter().map(|b| b.fb).collect(),
            bufs,
            held: Held::default(),
            flips: true,
            flip: Flips::default(),
            refused: Refused::default(),
            reported: false,
            path,
            flips_asked: 0,
            flips_done: 0,
            flipped: VecDeque::new(),
            monotonic,
            puts: 0,
            put_flip: None,
            vblank: true,
            last_seq: None,
            shown_at: None,
            wait: Duration::ZERO,
            width: w as u32,
            height: h as u32,
            rotated: false,
            origin: (0, 0),
            crtc,
            con: con.handle(),
            mode,
            modes: con.modes().to_vec(),
            on: true,
        })
    }

    /// The rate of the mode the panel runs at.
    pub fn rate(&self) -> u32 {
        rate_of(&self.mode)
    }

    /// One frame of the panel at the rate it runs at: what a timer waits
    /// where flips do not wait for a vertical blank.
    pub fn period(&self) -> Duration {
        crate::pace::period(match self.rate() {
            0 => crate::pace::DEFAULT_HZ,
            r => r,
        })
    }

    /// Switches the panel to its mode of hz at the size it runs at (the
    /// rate phone-displayd chose): the rate it runs at then, or why not.
    /// Off, the mode is the one it comes back on in. The framebuffer on the
    /// panel stays, a dumb buffer or the GPU's: a mode of the same size
    /// scans out the same buffers, so the GPU path's stay valid.
    pub fn set_rate(&mut self, hz: u32) -> Result<u32, String> {
        if hz == self.rate() {
            return Ok(hz);
        }
        let list: Vec<(u32, (u16, u16), bool)> = self.modes.iter().map(|m| (rate_of(m), m.size(), m.flags().contains(ModeFlags::INTERLACE))).collect();
        let Some(i) = pick_rate(&list, self.mode.size(), hz) else {
            let (w, h) = self.mode.size();
            return Err(format!("the panel has no {hz} Hz mode at {w}x{h}"));
        };
        let mode = self.modes[i];
        if self.on {
            self.mode_set(Some(self.shown), Some(mode)).0?;
        }
        self.mode = mode;
        Ok(self.rate())
    }

    /// Turns the panel off (the CRTC disabled: the display controller stops
    /// scanning out and the panel driver powers the panel and its backlight
    /// down) or on again with the frame it last showed. A flip still on its
    /// way is waited for first. Turned on, a flip to the frame it shows
    /// must complete: Ok(Some) says what was wrong when it did not (the
    /// panel may well be dark: a command-mode DSI panel that did not come
    /// up completes no flips), or when the flip before was still on its way.
    pub fn power(&mut self, on: bool) -> Result<Option<String>, String> {
        if on == self.on {
            return Ok(None);
        }
        let (res, settled) = if on { self.mode_set(Some(self.shown), Some(self.mode)) } else { self.mode_set(None, None) };
        res?;
        self.on = on;
        let mut warn = (!settled).then(|| format!("a page flip was still on its way after {} ms", SETTLE.as_millis()));
        if on && self.flips {
            match self.flip_to(self.shown) {
                Ok(()) if !self.settle_flips(SETTLE) => warn = Some(format!("no page flip completed in {} ms after the panel came on", SETTLE.as_millis())),
                Ok(()) => {}
                Err(e) => warn = Some(format!("page flip after the panel came on: {e}")),
            }
        }
        Ok(warn)
    }

    /// Sets the CRTC (set_crtc: off without a framebuffer) once the flips on
    /// their way have completed, waiting at most SETTLE; whether they had.
    /// It completes those still on the way: their events, read after it,
    /// pay for nothing later.
    fn mode_set(&mut self, fb: Option<framebuffer::Handle>, mode: Option<Mode>) -> (Result<(), String>, bool) {
        let settled = self.settle_flips(SETTLE);
        let con = [self.con];
        let conns: &[connector::Handle] = if fb.is_some() { &con } else { &[] };
        let res = self.card.set_crtc(self.crtc, fb, (0, 0), conns, mode).map_err(s);
        while self.take_events(Duration::ZERO) {}
        self.flip = Flips::default();
        (res, settled)
    }

    /// Whether page flips pace the frames (a frame waits for the vertical
    /// blank of the one before); otherwise `show` waits a frame's time.
    fn flips(&self) -> bool {
        self.flips && self.on && self.vblank
    }

    /// Before a frame is handed over where flips do not wait for a
    /// vertical blank: what is left of a period of the mode the panel runs
    /// at since the last one, as a vertical blank would wait.
    fn wait_frame(&mut self) {
        let period = self.period();
        if let Some(left) = self.shown_at.map(|t| crate::pace::wait(period, t.elapsed())).filter(|d| !d.is_zero()) {
            std::thread::sleep(left);
            self.wait += left;
        }
    }

    /// The time spent waiting for flips or the timer since the last call.
    pub fn waited(&mut self) -> Duration {
        std::mem::take(&mut self.wait)
    }

    /// Waits for the flip asked for last (at most a few frames: a driver
    /// that never answers three is taken as unable to flip). One that
    /// answers late is owed its event, not taken for the next flip's.
    fn wait_flip(&mut self) {
        if !self.flip.pending {
            return;
        }
        let (start, limit) = (Instant::now(), Duration::from_millis(100));
        while self.flip.pending && self.take_events(limit.saturating_sub(start.elapsed())) {}
        self.wait += start.elapsed();
        if self.flip.pending {
            self.flip.gave_up();
            if self.flip.owed >= 3 {
                eprintln!("phone-paint: no page-flip events; drawing in place");
                self.flips = false;
            }
        }
    }

    /// Waits for every flip asked for, the late ones too, at most limit:
    /// before a mode set, and when the driver says one is still on its way
    /// (EBUSY). Whether they all completed.
    fn settle_flips(&mut self, limit: Duration) -> bool {
        let start = Instant::now();
        while self.flip.busy() && self.take_events(limit.saturating_sub(start.elapsed())) {}
        self.wait += start.elapsed();
        !self.flip.busy()
    }

    /// Reads the driver's completion events, waiting up to timeout for
    /// them; false when none came.
    fn take_events(&mut self, timeout: Duration) -> bool {
        if !readable(self.card.0.as_raw_fd(), timeout) {
            return false;
        }
        let Ok(evs) = self.card.receive_events() else { return false };
        for ev in evs {
            if let drm::control::Event::PageFlip(f) = ev {
                self.flips_done += 1;
                if self.flip.event() {
                    self.vblank = counts_blanks(&mut self.last_seq, f.frame, self.vblank);
                    let at = if self.monotonic && !f.duration.is_zero() { f.duration } else { crate::perf::mono() };
                    if self.flipped.len() == FLIPPED {
                        self.flipped.pop_front();
                    }
                    self.flipped.push_back((self.flips_asked, at));
                }
            }
        }
        true
    }

    /// Reads the completion events that came, without waiting.
    pub fn poll(&mut self) {
        while self.take_events(Duration::ZERO) {}
    }

    /// Flips to fb at the next vertical blank, waiting once (at most
    /// SETTLE) when the driver still has a flip on its way.
    fn flip_to(&mut self, fb: framebuffer::Handle) -> std::io::Result<()> {
        let flip = |d: &Display| d.card.page_flip(d.crtc, fb, drm::control::PageFlipFlags::EVENT, None);
        let mut res = flip(self);
        if res.as_ref().is_err_and(|e| e.raw_os_error() == Some(libc::EBUSY)) && self.settle_flips(SETTLE) {
            res = flip(self);
        }
        if res.is_ok() {
            self.flip.asked();
            self.flips_asked += 1;
            self.refused.flipped();
        }
        res
    }

    /// Puts a drawn frame (premultiplied RGBA, opaque) on the panel
    /// (XRGB8888): into the buffer not on screen and flipped to at the next
    /// vertical blank, or in place where the driver cannot flip. Only what
    /// changed against the buffer's own last frame is copied: `changed`,
    /// where the frame differs from the one shown before (the live
    /// display's damage), or else what a comparison finds. A frame the
    /// screen shows already costs nothing more.
    pub fn show(&mut self, pm: &tiny_skia::Pixmap, changed: Option<&[Rect]>) {
        let (src, sw, sh) = (pm.data(), pm.width() as usize, pm.height() as usize);
        let (ox, oy) = self.origin;
        let (w, h) = if self.rotated {
            (sw, sh)
        } else {
            ((self.width as usize - ox).min(sw), (self.height as usize - oy).min(sh))
        };
        if self.held.unchanged(src, sw, h, changed) {
            return;
        }
        self.puts += 1;
        self.put_flip = None;
        if self.flips && self.on {
            self.wait_flip();
        }
        if !self.flips() {
            self.wait_frame();
        }
        self.shown_at = Some(Instant::now());
        let target = if self.flips && self.on { 1 - self.held.front } else { self.held.front };
        let (rects, flip) = self.held.put(target, src, sw, (w, h));
        let (rotated, pitch) = (self.rotated, self.bufs[target].pitch);
        if !rects.is_empty() {
            let dst = self.bufs[target].map.as_mut();
            for r in &rects {
                for y in r.y0..r.y1 {
                    if rotated {
                        for x in r.x0..r.x1 {
                            let (px, py) = turned(x, y, sh);
                            let (si, di) = ((y * sw + x) * 4, py * pitch + px * 4);
                            dst[di] = src[si + 2];
                            dst[di + 1] = src[si + 1];
                            dst[di + 2] = src[si];
                            dst[di + 3] = 0xff;
                        }
                    } else {
                        let s = &src[(y * sw + r.x0) * 4..(y * sw + r.x1) * 4];
                        let d0 = (y + oy) * pitch + (r.x0 + ox) * 4;
                        xrgb(&mut dst[d0..d0 + s.len()], s);
                    }
                }
            }
        }
        if flip {
            let fb = self.bufs[target].fb;
            self.held.front = target;
            self.shown = fb;
            let res = self.flip_to(fb);
            if res.is_ok() {
                self.put_flip = Some(self.flips_asked);
            }
            if let Err(e) = res {
                // The frame is in the other buffer: show it the plain way;
                // refused twice in a row, draw in place from now on.
                self.refuse(fb, &e, "drawing in place");
                let _ = self.mode_set(Some(fb), Some(self.mode));
            }
            return;
        }
        // Drivers that scan out on demand (virtio-gpu, USB displays) only
        // show what they are told changed; others reject the call, harmlessly.
        let clips: Vec<ClipRect> = rects
            .iter()
            .map(|r| if self.rotated { turned_rect(r, sh) } else { Rect { x0: r.x0 + ox, y0: r.y0 + oy, x1: r.x1 + ox, y1: r.y1 + oy } })
            .map(|c| ClipRect::new(c.x0 as u16, c.y0 as u16, c.x1 as u16, c.y1 as u16))
            .collect();
        if !clips.is_empty() {
            let _ = self.card.dirty_framebuffer(self.bufs[self.held.front].fb, &clips);
        }
    }
}

/// Opaque RGBA pixels as the panel's XRGB8888 (B, G, R, X in memory), a
/// word at a time: a load and a store a pixel, not seven (on an emulated
/// CPU each is a lookup).
fn xrgb(d: &mut [u8], s: &[u8]) {
    for (d, s) in d.chunks_exact_mut(4).zip(s.chunks_exact(4)) {
        let v = u32::from_le_bytes([s[0], s[1], s[2], s[3]]);
        let o = 0xff00_0000 | (v & 0x0000_ff00) | ((v & 0xff) << 16) | ((v >> 16) & 0xff);
        d.copy_from_slice(&o.to_le_bytes());
    }
}

/// Whether flip events count vertical blanks, after one saying frame seq:
/// a sequence moving on does; the same one twice (a driver without them
/// says 0 every time) does not.
fn counts_blanks(last: &mut Option<u32>, seq: u32, was: bool) -> bool {
    let counts = match *last {
        Some(l) => l != seq,
        None => was,
    };
    *last = Some(seq);
    counts
}

/// What the two scan-out buffers hold: a copy of each one's frame, and
/// where it may differ from the frame shown last (frame pixels; None: not
/// known, compared). The front one is on screen.
#[derive(Default)]
pub struct Held {
    frames: [Vec<u8>; 2],
    since: [Option<Vec<Rect>>; 2],
    pub front: usize,
}

impl Held {
    /// A new frame, `width` pixels a row, changed against the last one
    /// where `changed` says (None: not known): whether the screen shows it
    /// already, comparing only the first `height` rows (the panel's).
    pub fn unchanged(&mut self, src: &[u8], width: usize, height: usize, changed: Option<&[Rect]>) -> bool {
        for s in &mut self.since {
            *s = match (s.take(), changed) {
                (Some(mut v), Some(c)) => {
                    v.extend_from_slice(c);
                    Some(v)
                }
                _ => None,
            };
        }
        match &self.since[self.front] {
            Some(v) if self.frames[self.front].len() == src.len() => v.is_empty(),
            _ => damage(&self.frames[self.front], src, width, height).is_none(),
        }
    }

    /// The frame goes into target: the rectangles of the panel's part
    /// (w × h) to copy there, and whether to flip to it. Target may hold
    /// the frame already while the screen does not (a frame back as it was
    /// two frames ago): then nothing is copied, and it is flipped to.
    pub fn put(&mut self, target: usize, src: &[u8], width: usize, (w, h): (usize, usize)) -> (Vec<Rect>, bool) {
        let held = &mut self.frames[target];
        let rects: Vec<Rect> = match self.since[target].take() {
            Some(v) if held.len() == src.len() => v,
            _ => damage(held, src, width, h).into_iter().collect(),
        };
        if held.len() == src.len() {
            for r in &rects {
                for y in r.y0..r.y1.min(src.len() / 4 / width) {
                    let (a, b) = ((y * width + r.x0.min(width)) * 4, (y * width + r.x1.min(width)) * 4);
                    held[a..b].copy_from_slice(&src[a..b]);
                }
            }
        } else {
            held.clear();
            held.extend_from_slice(src);
        }
        self.since[target] = Some(Vec::new());
        let rects = rects
            .into_iter()
            .map(|r| Rect { x0: r.x0.min(w), y0: r.y0.min(h), x1: r.x1.min(w), y1: r.y1.min(h) })
            .filter(|r| r.x0 < r.x1 && r.y0 < r.y1)
            .collect();
        (rects, target != self.front)
    }

    /// What target holds is not known any more.
    pub fn forget(&mut self, target: usize) {
        self.frames[target].clear();
        self.since[target] = None;
    }
}

/// A mode's refresh rate in Hz, rounded as the kernel's drm_mode_vrefresh
/// rounds it (phone-displayd reads the same modes the same way).
pub fn mode_rate(clock_khz: u32, htotal: u16, vtotal: u16, vscan: u16, interlace: bool, dblscan: bool) -> u32 {
    if htotal == 0 || vtotal == 0 {
        return 0;
    }
    let (mut num, mut den) = (u64::from(clock_khz) * 1000, u64::from(htotal) * u64::from(vtotal));
    if interlace {
        num *= 2;
    }
    if dblscan {
        den *= 2;
    }
    if vscan > 1 {
        den *= u64::from(vscan);
    }
    ((num + den / 2) / den) as u32
}

fn rate_of(m: &Mode) -> u32 {
    let f = m.flags();
    mode_rate(m.clock(), m.hsync().2, m.vsync().2, m.vscan(), f.contains(ModeFlags::INTERLACE), f.contains(ModeFlags::DBLSCAN))
}

/// Which of `modes` (rate, size, interlaced) is the one of `hz` at `size`;
/// progressive modes only.
pub fn pick_rate(modes: &[(u32, (u16, u16), bool)], size: (u16, u16), hz: u32) -> Option<usize> {
    modes.iter().position(|&(rate, sz, interlaced)| rate == hz && sz == size && !interlaced)
}

/// Where pixel (x, y) of a frame `height` rows tall lands on the panel
/// when the frame is turned a quarter clockwise.
pub fn turned(x: usize, y: usize, height: usize) -> (usize, usize) {
    (height - 1 - y, x)
}

/// The panel's rectangle for a frame rectangle, turned as `turned`.
pub fn turned_rect(r: &Rect, height: usize) -> Rect {
    Rect { x0: height - r.y1, y0: r.x0, x1: height - r.y0, y1: r.x1 }
}

/// Where a device screen of `form` sits on a bigger screen of `mode` (an
/// HDMI monitor on a development board): its middle, at its own size, so
/// what is on it is what the phone shows. None when the mode is not bigger
/// both ways; the frame then takes the mode's size.
pub fn letterbox(mode: (u32, u32), form: (u32, u32)) -> Option<(usize, usize)> {
    let ((mw, mh), (fw, fh)) = (mode, form);
    if fw == 0 || fh == 0 || mw < fw || mh < fh || (mw, mh) == (fw, fh) {
        return None;
    }
    Some((((mw - fw) / 2) as usize, ((mh - fh) / 2) as usize))
}

impl Display {
    /// Whether this panel is the device's screen turned sideways.
    pub fn sideways(&self, width: u32, height: u32) -> bool {
        width != height && (self.width, self.height) == (height, width)
    }
}

/// A buffer the GPU rendered into (gpu.rs), as DRM takes it.
struct GbmBuffer {
    handle: drm::buffer::Handle,
    size: (u32, u32),
    pitch: u32,
    modifier: u64,
}

impl Buffer for GbmBuffer {
    fn size(&self) -> (u32, u32) {
        self.size
    }
    fn format(&self) -> DrmFourcc {
        DrmFourcc::Xrgb8888
    }
    fn pitch(&self) -> u32 {
        self.pitch
    }
    fn handle(&self) -> drm::buffer::Handle {
        self.handle
    }
}

impl PlanarBuffer for GbmBuffer {
    fn size(&self) -> (u32, u32) {
        self.size
    }
    fn format(&self) -> DrmFourcc {
        DrmFourcc::Xrgb8888
    }
    fn modifier(&self) -> Option<DrmModifier> {
        Some(DrmModifier::from(self.modifier))
    }
    fn pitches(&self) -> [u32; 4] {
        [self.pitch, 0, 0, 0]
    }
    fn handles(&self) -> [Option<drm::buffer::Handle>; 4] {
        [Some(self.handle), None, None, None]
    }
    fn offsets(&self) -> [u32; 4] {
        [0; 4]
    }
}

/// Whether a buffer of this layout (a GBM modifier) is what a framebuffer
/// without one means: linear, or the driver's own (no modifier said). Any
/// other (tiled, compressed) must be named to the display (ADDFB2).
pub fn plain_layout(modifier: u64) -> bool {
    modifier == 0 || modifier == crate::gbm::MOD_INVALID
}

/// The GPU path's side (gpu.rs): it renders into buffers of its own on
/// this device, or on the GPU's render node (node.rs), and shows them
/// here, paced by the same page flips.
impl Display {
    /// The device's descriptor, for GBM.
    pub fn fd(&self) -> std::os::fd::RawFd {
        self.card.0.as_raw_fd()
    }

    /// What the display's device is (node.rs: whether it renders too).
    pub fn node(&self) -> crate::node::Info {
        crate::node::info(self.path, self.card.as_fd())
    }

    /// A framebuffer for a GPU buffer (XRGB8888) by its GEM handle on
    /// this device, laid out as modifier says.
    pub fn add_fb(&mut self, handle: u32, size: (u32, u32), pitch: u32, modifier: u64) -> Result<framebuffer::Handle, String> {
        let handle = drm::control::from_u32(handle).ok_or("no buffer handle")?;
        self.add_buffer(handle, size, pitch, modifier)
    }

    fn add_buffer(&mut self, handle: drm::buffer::Handle, size: (u32, u32), pitch: u32, modifier: u64) -> Result<framebuffer::Handle, String> {
        let b = GbmBuffer { handle, size, pitch, modifier };
        let fb = if plain_layout(modifier) {
            self.card.add_framebuffer(&b, 24, 32).map_err(s)?
        } else {
            if self.card.get_driver_capability(DriverCapability::AddFB2Modifiers).unwrap_or(0) == 0 {
                return Err(format!("the GPU's buffers are laid out by modifier {modifier:#x} and the display takes none"));
            }
            self.card.add_planar_framebuffer(&b, FbCmd2Flags::MODIFIERS).map_err(|e| format!("a framebuffer of modifier {modifier:#x}: {e}"))?
        };
        self.ours.push(fb);
        Ok(fb)
    }

    /// A framebuffer for a buffer of another device (the GPU's render
    /// node), exported as a dmabuf: imported through PRIME; the import's
    /// handle is closed again, the framebuffer holding the buffer.
    pub fn import_fb(&mut self, dmabuf: BorrowedFd, size: (u32, u32), pitch: u32, modifier: u64) -> Result<framebuffer::Handle, String> {
        let handle = self.card.prime_fd_to_buffer(dmabuf).map_err(|e| format!("PRIME import into {}: {e}", self.path))?;
        let fb = self.add_buffer(handle, size, pitch, modifier);
        let _ = self.card.close_buffer(handle);
        fb.map_err(|e| format!("{e} (a buffer imported into {})", self.path))
    }

    pub fn remove_fb(&mut self, fb: framebuffer::Handle) {
        self.ours.retain(|&f| f != fb);
        let _ = self.card.destroy_framebuffer(fb);
    }

    /// Shows a framebuffer the GPU rendered: flipped to at the next
    /// vertical blank once the flip before has happened, or set at once
    /// (the first, which may change the buffer's layout, and where the
    /// driver cannot flip), paced as `show` paces. Returns whether it waits
    /// for a flip; either way, the frame shown before the last call is off
    /// the screen.
    pub fn scan_out(&mut self, fb: framebuffer::Handle, first: bool) -> Result<bool, String> {
        self.shown = fb;
        if !self.on {
            return Ok(false);
        }
        self.puts += 1;
        self.put_flip = None;
        self.wait_flip();
        if !self.flips() {
            self.wait_frame();
        }
        self.shown_at = Some(Instant::now());
        if self.flips && !first {
            match self.flip_to(fb) {
                Ok(()) => {
                    self.put_flip = Some(self.flips_asked);
                    return Ok(true);
                }
                Err(e) => self.refuse(fb, &e, "setting each frame"),
            }
        }
        self.mode_set(Some(fb), Some(self.mode)).0?;
        Ok(false)
    }

    /// A flip to fb refused with e: the first time, what the kernel shows
    /// goes to the log; refused twice in a row, flips stop (then: what is
    /// done instead). The caller sets the CRTC to fb either way.
    fn refuse(&mut self, fb: framebuffer::Handle, e: &std::io::Error, then: &str) {
        if !std::mem::replace(&mut self.reported, true) {
            for line in describe(&self.flip_facts(fb, e)) {
                eprintln!("phone-paint: page flip: {line}");
            }
        }
        if self.refused.refused() {
            eprintln!("phone-paint: page flip: {e}; set at once, flips again from the next frame");
        } else {
            eprintln!("phone-paint: page flip: {e}, again after the CRTC was set; {then}");
            self.flips = false;
        }
    }

    /// What the kernel shows of the CRTC, its planes and connector and the
    /// framebuffers when a flip to fb fails with e (`describe`). The
    /// client capabilities that show the primary plane and the atomic
    /// properties are set for this and cleared again.
    fn flip_facts(&self, fb: framebuffer::Handle, e: &std::io::Error) -> FlipFacts {
        let c = &self.card;
        let atomic = c.set_client_capability(ClientCapability::Atomic, true).is_ok();
        let _ = c.set_client_capability(ClientCapability::UniversalPlanes, true);
        let driver = c.get_driver().map(|d| format!("{} ({})", d.name().to_string_lossy(), d.date().to_string_lossy())).unwrap_or_default();
        let cap = |k| c.get_driver_capability(k).map_or("?".to_string(), |v| v.to_string());
        let caps = format!(
            "async flips {}, CRTC in vblank events {}, monotonic timestamps {}, ADDFB2 modifiers {}",
            cap(DriverCapability::ASyncPageFlip),
            cap(DriverCapability::CRTCInVBlankEvent),
            cap(DriverCapability::MonotonicTimestamp),
            cap(DriverCapability::AddFB2Modifiers)
        );
        let info = c.get_crtc(self.crtc).ok();
        let fb_of = |h: framebuffer::Handle| -> Option<FbFacts> {
            let id = u32::from(h);
            match c.get_planar_framebuffer(h) {
                Ok(p) => Some(FbFacts { id, size: p.size(), format: p.pixel_format().to_string(), modifier: p.modifier().map(u64::from), pitch: p.pitches()[0] }),
                Err(_) => c.get_framebuffer(h).ok().map(|f| FbFacts { id, size: f.size(), format: format!("depth {} bpp {}", f.depth(), f.bpp()), modifier: None, pitch: f.pitch() }),
            }
        };
        let crtc_fb = info.as_ref().and_then(|i| i.framebuffer());
        let con = c.get_connector(self.con, false).ok();
        let planes = c
            .plane_handles()
            .unwrap_or_default()
            .into_iter()
            .filter_map(|p| c.get_plane(p).ok())
            .filter_map(|p| {
                let on = p.crtc() == Some(self.crtc);
                let can = c.resource_handles().is_ok_and(|r| r.filter_crtcs(p.possible_crtcs()).contains(&self.crtc));
                let kind = self.prop(p.handle(), "type").map(|(_, n)| n).unwrap_or_default();
                (on || (can && kind == "Primary")).then(|| PlaneFacts {
                    id: u32::from(p.handle()),
                    kind,
                    crtc: p.crtc().map_or(0, u32::from),
                    fb: p.framebuffer().map_or(0, u32::from),
                    rotation: self.prop(p.handle(), "rotation").map(|(v, _)| v),
                    xr24: p.formats().contains(&(DrmFourcc::Xrgb8888 as u32)),
                })
            })
            .collect();
        let facts = FlipFacts {
            error: e.to_string(),
            errno: e.raw_os_error().unwrap_or(0),
            driver,
            caps,
            crtc: u32::from(self.crtc),
            mode: mode_facts(&self.mode),
            crtc_mode: info.as_ref().and_then(|i| i.mode()).map(|m| mode_facts(&m)),
            crtc_fb: crtc_fb.map_or(0, u32::from),
            active: self.prop(self.crtc, "ACTIVE").map(|(v, _)| v != 0),
            connector: con.as_ref().map(|k| format!("{}-{} {:?}", k.interface().as_str(), k.interface_id(), k.state())).unwrap_or_default(),
            dpms: self.prop(self.con, "DPMS").map(|(_, n)| n),
            orientation: self.prop(self.con, "panel orientation").map(|(_, n)| n),
            on: crtc_fb.and_then(fb_of),
            new: fb_of(fb),
            ours: self.ours.iter().map(|&f| u32::from(f)).collect(),
            planes,
            pending: self.flip.pending,
            asked: self.flips_asked,
            done: self.flips_done,
            owed: self.flip.owed,
        };
        if atomic {
            let _ = c.set_client_capability(ClientCapability::Atomic, false);
        }
        let _ = c.set_client_capability(ClientCapability::UniversalPlanes, false);
        facts
    }

    /// A property of a CRTC, plane or connector: its raw value and, for
    /// an enum, the value's name.
    fn prop<H: ResourceHandle>(&self, h: H, name: &str) -> Option<(u64, String)> {
        let set = self.card.get_properties(h).ok()?;
        let found = set.iter().find_map(|(&p, &v)| {
            let info = self.card.get_property(p).ok()?;
            (info.name().to_bytes() == name.as_bytes()).then(|| {
                let shown = match info.value_type().convert_value(v) {
                    drm::control::property::Value::Enum(Some(e)) => e.name().to_string_lossy().into_owned(),
                    _ => v.to_string(),
                };
                (v, shown)
            })
        });
        found
    }

    /// The frames go to the dumb buffers again (the GPU path gave up):
    /// what they hold is forgotten, so the next frame is copied whole.
    pub fn back_to_dumb(&mut self) {
        self.held.forget(0);
        self.held.forget(1);
        self.shown = self.bufs[self.held.front].fb;
        if self.on {
            let _ = self.mode_set(Some(self.shown), Some(self.mode));
        }
    }
}

/// The CRTC to drive the connector with: the one its encoder drives now,
/// else the first of the device's CRTCs (all) that one of its encoders
/// can drive (possible: each encoder's). A connector the client before
/// turned off has no encoder now, and the device's first CRTC may be
/// wired to another connector's encoders only.
fn crtc_for(current: Option<crtc::Handle>, possible: &[Vec<crtc::Handle>], all: &[crtc::Handle]) -> Option<crtc::Handle> {
    current.or_else(|| all.iter().find(|c| possible.iter().any(|p| p.contains(c))).copied())
}

/// Page flips refused in a row: after the first, the CRTC is set to the
/// frame and the next frame flips again (the CRTC another client left
/// may hold state a set rewrites); a second refusal in a row stops them.
#[derive(Debug, Default)]
struct Refused {
    row: u32,
}

impl Refused {
    /// A refusal: whether to go on flipping.
    fn refused(&mut self) -> bool {
        self.row += 1;
        self.row < 2
    }

    fn flipped(&mut self) {
        self.row = 0;
    }
}

/// A framebuffer as the kernel describes it.
#[derive(Clone, Debug, Default, PartialEq)]
struct FbFacts {
    id: u32,
    size: (u32, u32),
    format: String,
    modifier: Option<u64>,
    pitch: u32,
}

/// A plane on the CRTC, or its primary plane.
#[derive(Clone, Debug, Default)]
struct PlaneFacts {
    id: u32,
    kind: String,
    crtc: u32,
    fb: u32,
    rotation: Option<u64>,
    xr24: bool,
}

/// What the kernel shows when a page flip fails (Display::flip_facts).
#[derive(Debug, Default)]
struct FlipFacts {
    error: String,
    errno: i32,
    driver: String,
    caps: String,
    crtc: u32,
    /// The mode phone-paint set, and the CRTC's now (width, height, Hz).
    mode: (u16, u16, u32),
    crtc_mode: Option<(u16, u16, u32)>,
    crtc_fb: u32,
    /// The CRTC's ACTIVE property (atomic drivers).
    active: Option<bool>,
    connector: String,
    dpms: Option<String>,
    orientation: Option<String>,
    /// The framebuffer on the CRTC and the one to flip to.
    on: Option<FbFacts>,
    new: Option<FbFacts>,
    ours: Vec<u32>,
    planes: Vec<PlaneFacts>,
    pending: bool,
    asked: u64,
    done: u64,
    owed: u32,
}

fn mode_facts(m: &Mode) -> (u16, u16, u32) {
    let (w, h) = m.size();
    (w, h, rate_of(m))
}

/// DRM_MODE_ROTATE_90 and _270.
const QUARTER_TURNS: u64 = 0x2 | 0x8;

/// The log's lines for a refused page flip: what the kernel shows, then
/// what in it breaks a rule of a legacy flip (the framebuffer's size and
/// format those of the CRTC's mode and framebuffer, the CRTC on, one flip
/// pending at a time, the primary plane not turned a quarter), or that
/// nothing visible does: then a driver's own check refused it, which the
/// kernel's drm debug log names.
fn describe(f: &FlipFacts) -> Vec<String> {
    let mode = |m: (u16, u16, u32)| format!("{}x{}@{}", m.0, m.1, m.2);
    let fb = |b: &Option<FbFacts>| match b {
        Some(b) => format!("{} {}x{} {} modifier {} pitch {}", b.id, b.size.0, b.size.1, b.format, b.modifier.map_or("none".into(), |m| format!("{m:#x}")), b.pitch),
        None => "none".into(),
    };
    let opt = |v: &Option<String>| v.clone().unwrap_or_else(|| "-".into());
    let mut out = vec![
        format!("{} flipping CRTC {} to framebuffer {}; the kernel shows:", f.error, f.crtc, f.new.as_ref().map_or(0, |b| b.id)),
        format!("driver {}; {}", f.driver, f.caps),
        format!(
            "CRTC {}: mode {} (phone-paint set {}), framebuffer {}, ACTIVE {}",
            f.crtc,
            f.crtc_mode.map_or("none".into(), mode),
            mode(f.mode),
            f.crtc_fb,
            f.active.map_or("-".into(), |a| a.to_string())
        ),
        format!("connector {}, DPMS {}, panel orientation {}", f.connector, opt(&f.dpms), opt(&f.orientation)),
        format!("framebuffer on the CRTC: {}", fb(&f.on)),
        format!("framebuffer to flip to: {}", fb(&f.new)),
    ];
    for p in &f.planes {
        out.push(format!(
            "plane {} {}: CRTC {}, framebuffer {}, rotation {}, XRGB8888 {}",
            p.id,
            if p.kind.is_empty() { "?" } else { &p.kind },
            p.crtc,
            p.fb,
            p.rotation.map_or("-".into(), |r| format!("{r:#x}")),
            if p.xr24 { "yes" } else { "no" }
        ));
    }
    out.push(format!("flips: {} asked, {} completed, {} given up on and owed their events, one pending: {}", f.asked, f.done, f.owed, if f.pending { "yes" } else { "no" }));
    let mut why = Vec::new();
    if f.errno == libc::EBUSY {
        why.push(format!("a flip was still pending: the kernel takes one at a time, and its event came later than {} ms", SETTLE.as_millis()));
    }
    if f.active == Some(false) || f.crtc_mode.is_none() {
        why.push("the CRTC is off: the kernel refuses a flip with an event on a CRTC that is off".into());
    }
    if let Some(m) = f.crtc_mode.filter(|&m| m != f.mode) {
        why.push(format!("the CRTC runs {}, not the {} phone-paint set: another client changed it", mode(m), mode(f.mode)));
    }
    if let (Some(b), Some(m)) = (&f.new, f.crtc_mode) {
        if b.size != (u32::from(m.0), u32::from(m.1)) {
            why.push(format!("the framebuffer is {}x{}, the mode {}x{}: a flip keeps the mode's size", b.size.0, b.size.1, m.0, m.1));
        }
    }
    if let (Some(a), Some(b)) = (&f.on, &f.new) {
        if a.format != b.format {
            why.push(format!("the CRTC shows {} and the framebuffer is {}: a flip cannot change the format", a.format, b.format));
        }
    }
    if f.crtc_fb != 0 && !f.ours.contains(&f.crtc_fb) {
        why.push(format!("the CRTC shows framebuffer {}, none of phone-paint's: another client set it", f.crtc_fb));
    }
    for p in f.planes.iter().filter(|p| p.kind == "Primary") {
        if p.rotation.is_some_and(|r| r & QUARTER_TURNS != 0) {
            why.push(format!("primary plane {} is turned a quarter (rotation {:#x}, left by the client before): the kernel checks the framebuffer against the mode turned", p.id, p.rotation.unwrap_or(0)));
        }
        if p.crtc != 0 && p.crtc != f.crtc {
            why.push(format!("primary plane {} is on CRTC {}: a flip may not move it", p.id, p.crtc));
        }
        if !p.xr24 {
            why.push(format!("primary plane {} does not take XRGB8888", p.id));
        }
    }
    if f.dpms.as_deref() == Some("Off") {
        why.push("the connector's DPMS is Off".into());
    }
    if why.is_empty() {
        why.push("nothing here breaks a rule of a legacy flip: a check of the driver's own refused it; drm.debug=0x14 makes the kernel log which (39-oneplus-6t.md §14.4)".into());
    }
    out.extend(why.into_iter().map(|w| format!("likely: {w}")));
    for p in f.planes.iter().filter(|p| p.kind != "Primary" && p.crtc == f.crtc && p.fb != 0) {
        out.push(format!("also: plane {} ({}) is on over the frames, left by the client before", p.id, if p.kind.is_empty() { "?" } else { &p.kind }));
    }
    out
}

/// The part of the screen a frame changed: columns x0..x1, rows y0..y1.
#[derive(Debug, PartialEq, Clone, Copy)]
pub struct Rect {
    pub x0: usize,
    pub y0: usize,
    pub x1: usize,
    pub y1: usize,
}

/// The bounding box of the pixels that differ between two frames of the
/// same size (4 bytes a pixel, `width` pixels a row); all of `cur` when
/// there is no previous frame, None when nothing changed.
pub fn damage(prev: &[u8], cur: &[u8], width: usize, height: usize) -> Option<Rect> {
    if width == 0 || height == 0 {
        return None;
    }
    if prev.len() != cur.len() {
        return Some(Rect { x0: 0, y0: 0, x1: width, y1: height });
    }
    let row = width * 4;
    let mut r: Option<Rect> = None;
    for y in 0..height {
        let (a, b) = (&prev[y * row..(y + 1) * row], &cur[y * row..(y + 1) * row]);
        if a == b {
            continue;
        }
        let first = (0..width).find(|&x| a[x * 4..x * 4 + 4] != b[x * 4..x * 4 + 4]).unwrap_or(0);
        let last = (0..width).rev().find(|&x| a[x * 4..x * 4 + 4] != b[x * 4..x * 4 + 4]).unwrap_or(width - 1);
        r = Some(match r {
            None => Rect { x0: first, y0: y, x1: last + 1, y1: y + 1 },
            Some(r) => Rect { x0: r.x0.min(first), y0: r.y0, x1: r.x1.max(last + 1), y1: y + 1 },
        });
    }
    r
}

fn named(k: KeyCode) -> Option<&'static str> {
    Some(match k {
        KeyCode::KEY_UP => "up",
        KeyCode::KEY_DOWN => "down",
        KeyCode::KEY_LEFT => "left",
        KeyCode::KEY_RIGHT => "right",
        KeyCode::KEY_ENTER | KeyCode::KEY_KPENTER => "enter",
        KeyCode::KEY_ESC => "esc",
        KeyCode::KEY_TAB | KeyCode::KEY_F1 => "tab",
        KeyCode::KEY_BACKSPACE => "backspace",
        KeyCode::KEY_F2 => "f2",
        KeyCode::KEY_F3 => "f3",
        KeyCode::KEY_F4 => "f4",
        KeyCode::KEY_F5 => "call",
        KeyCode::KEY_F6 => "ime",
        KeyCode::KEY_F7 => "sym",
        KeyCode::KEY_VOLUMEDOWN | KeyCode::KEY_F8 => "voldown",
        KeyCode::KEY_VOLUMEUP | KeyCode::KEY_F9 => "volup",
        KeyCode::KEY_PLAYPAUSE | KeyCode::KEY_PLAYCD | KeyCode::KEY_PAUSECD | KeyCode::KEY_F10 => "playpause",
        KeyCode::KEY_PREVIOUSSONG | KeyCode::KEY_F11 => "previous",
        KeyCode::KEY_NEXTSONG | KeyCode::KEY_F12 => "next",
        // A PC keyboard's editing keys (a phone's are Fn + arrows, Shift +
        // Backspace).
        KeyCode::KEY_HOME => "first",
        KeyCode::KEY_END => "last",
        KeyCode::KEY_PAGEUP => "pageup",
        KeyCode::KEY_PAGEDOWN => "pagedown",
        KeyCode::KEY_DELETE => "delete",
        _ => return None,
    })
}

/// The key a step of PHONE_PAINT_PLAY names (play.rs): a named key (as
/// `named` names it) or one character, a 9-key pad's digits, * and # its
/// own codes, the rest a US layout's.
pub fn play_code(name: &str) -> Option<KeyCode> {
    use KeyCode as K;
    const NAMED: &[KeyCode] = &[
        K::KEY_UP, K::KEY_DOWN, K::KEY_LEFT, K::KEY_RIGHT, K::KEY_ENTER, K::KEY_ESC, K::KEY_TAB, K::KEY_BACKSPACE,
        K::KEY_F2, K::KEY_F3, K::KEY_F4, K::KEY_F5, K::KEY_F6, K::KEY_F7, K::KEY_HOME, K::KEY_END, K::KEY_PAGEUP, K::KEY_PAGEDOWN, K::KEY_DELETE,
    ];
    if let Some(&k) = NAMED.iter().find(|&&k| named(k) == Some(name)) {
        return Some(k);
    }
    let mut cs = name.chars();
    let (Some(c), None) = (cs.next(), cs.next()) else { return None };
    const PAD: [KeyCode; 10] = [K::KEY_NUMERIC_0, K::KEY_NUMERIC_1, K::KEY_NUMERIC_2, K::KEY_NUMERIC_3, K::KEY_NUMERIC_4, K::KEY_NUMERIC_5, K::KEY_NUMERIC_6, K::KEY_NUMERIC_7, K::KEY_NUMERIC_8, K::KEY_NUMERIC_9];
    match c {
        '0'..='9' => Some(PAD[c as usize - '0' as usize]),
        '*' => Some(K::KEY_NUMERIC_STAR),
        '#' => Some(K::KEY_NUMERIC_POUND),
        _ => (0..0x100).map(KeyCode::new).find(|&k| text(k, false) == Some(c)),
    }
}

/// The power key's message once it was down for held: a long press from
/// long (the shell's power_long) on, with how long it was down. A short
/// press goes on release, not on the press: the shell turns the screen off
/// on a press, which a hold must not do.
fn power_key(held: Duration, long: Duration) -> serde_json::Value {
    let ms = held.as_millis() as u64;
    if held >= long {
        serde_json::json!({"t": "key", "key": "power", "text": "", "long": true, "ms": ms})
    } else {
        serde_json::json!({"t": "key", "key": "power", "text": "", "ms": ms})
    }
}

/// The power key while it is down. Most boards' power key (gpio-keys)
/// does not autorepeat, so the long press is due from the renderer's own
/// timer (`due`, the key loop's poll timeout) as much as from a repeat,
/// whichever comes first; a short press still goes on release. The long
/// press goes only while the kernel has the key down too: a release this
/// renderer did not see (a device opened again, its buffer overrun) makes
/// it the short press it was, not the power menu. A press after a lost
/// release starts over, so the key is never held for good.
#[derive(Default)]
struct PowerKey {
    since: Option<Instant>,
    sent: bool,
}

impl PowerKey {
    fn press(&mut self, now: Instant) {
        self.since = Some(now);
        self.sent = false;
    }

    /// A repeat, or the timer running out: the long press, once, while
    /// down (the kernel's word, None where it cannot say) has the key down.
    fn held(&mut self, now: Instant, long: Duration, down: impl FnOnce() -> Option<bool>) -> Option<serde_json::Value> {
        let since = self.since?;
        if self.sent || now.duration_since(since) < long {
            return None;
        }
        if down() == Some(false) {
            self.since = None;
            return Some(serde_json::json!({"t": "key", "key": "power", "text": ""}));
        }
        self.sent = true;
        Some(power_key(now.duration_since(since), long))
    }

    fn release(&mut self, now: Instant, long: Duration) -> Option<serde_json::Value> {
        let since = self.since.take()?;
        (!self.sent).then(|| power_key(now.duration_since(since), long))
    }

    /// How long until the long press is due, while the key is down and it
    /// has not gone.
    fn due(&self, now: Instant, long: Duration) -> Option<Duration> {
        let since = self.since.filter(|_| !self.sent)?;
        Some(long.saturating_sub(now.duration_since(since)))
    }
}

/// A line on the renderer's log for what the power key did: down, up and
/// what went to the shell (the journal keeps the sequence, docs/39 §14.10).
fn power_log(event: &str, sent: Option<&serde_json::Value>) {
    match sent {
        Some(m) if m["long"] == serde_json::json!(true) => eprintln!("phone-paint: power key {event}: long press ({} ms)", m["ms"]),
        Some(m) if m["ms"].is_null() => eprintln!("phone-paint: power key {event}: short press (the kernel has it up: its release was lost)"),
        Some(m) => eprintln!("phone-paint: power key {event}: short press ({} ms)", m["ms"]),
        None => eprintln!("phone-paint: power key {event}"),
    }
}

/// Waits for the device to have events, at most timeout; false when the
/// time ran out first.
fn readable(fd: std::os::fd::RawFd, timeout: Duration) -> bool {
    let mut p = libc::pollfd { fd, events: libc::POLLIN, revents: 0 };
    let ms = timeout.as_micros().div_ceil(1000).min(i32::MAX as u128) as i32;
    // SAFETY: one pollfd on the stack, valid for the call.
    unsafe { libc::poll(&mut p, 1, ms) > 0 }
}

/// The character of a key on a US layout.
fn text(k: KeyCode, shift: bool) -> Option<char> {
    const LETTERS: &[(KeyCode, char)] = &[
        (KeyCode::KEY_A, 'a'), (KeyCode::KEY_B, 'b'), (KeyCode::KEY_C, 'c'), (KeyCode::KEY_D, 'd'),
        (KeyCode::KEY_E, 'e'), (KeyCode::KEY_F, 'f'), (KeyCode::KEY_G, 'g'), (KeyCode::KEY_H, 'h'),
        (KeyCode::KEY_I, 'i'), (KeyCode::KEY_J, 'j'), (KeyCode::KEY_K, 'k'), (KeyCode::KEY_L, 'l'),
        (KeyCode::KEY_M, 'm'), (KeyCode::KEY_N, 'n'), (KeyCode::KEY_O, 'o'), (KeyCode::KEY_P, 'p'),
        (KeyCode::KEY_Q, 'q'), (KeyCode::KEY_R, 'r'), (KeyCode::KEY_S, 's'), (KeyCode::KEY_T, 't'),
        (KeyCode::KEY_U, 'u'), (KeyCode::KEY_V, 'v'), (KeyCode::KEY_W, 'w'), (KeyCode::KEY_X, 'x'),
        (KeyCode::KEY_Y, 'y'), (KeyCode::KEY_Z, 'z'),
    ];
    const ROW: &[(KeyCode, char, char)] = &[
        (KeyCode::KEY_1, '1', '!'), (KeyCode::KEY_2, '2', '@'), (KeyCode::KEY_3, '3', '#'), (KeyCode::KEY_4, '4', '$'),
        (KeyCode::KEY_5, '5', '%'), (KeyCode::KEY_6, '6', '^'), (KeyCode::KEY_7, '7', '&'), (KeyCode::KEY_8, '8', '*'),
        (KeyCode::KEY_9, '9', '('), (KeyCode::KEY_0, '0', ')'), (KeyCode::KEY_MINUS, '-', '_'),
        (KeyCode::KEY_EQUAL, '=', '+'), (KeyCode::KEY_COMMA, ',', '<'), (KeyCode::KEY_DOT, '.', '>'),
        (KeyCode::KEY_SLASH, '/', '?'), (KeyCode::KEY_SEMICOLON, ';', ':'), (KeyCode::KEY_APOSTROPHE, '\'', '"'),
        (KeyCode::KEY_LEFTBRACE, '[', '{'), (KeyCode::KEY_RIGHTBRACE, ']', '}'), (KeyCode::KEY_BACKSLASH, '\\', '|'),
        (KeyCode::KEY_GRAVE, '`', '~'), (KeyCode::KEY_SPACE, ' ', ' '),
        (KeyCode::KEY_KP0, '0', '0'), (KeyCode::KEY_KP1, '1', '1'), (KeyCode::KEY_KP2, '2', '2'),
        (KeyCode::KEY_KP3, '3', '3'), (KeyCode::KEY_KP4, '4', '4'), (KeyCode::KEY_KP5, '5', '5'),
        (KeyCode::KEY_KP6, '6', '6'), (KeyCode::KEY_KP7, '7', '7'), (KeyCode::KEY_KP8, '8', '8'),
        (KeyCode::KEY_KP9, '9', '9'), (KeyCode::KEY_KPASTERISK, '*', '*'), (KeyCode::KEY_KPDOT, '.', '.'),
        (KeyCode::KEY_KPPLUS, '+', '+'), (KeyCode::KEY_KPMINUS, '-', '-'), (KeyCode::KEY_KPSLASH, '/', '/'),
        // A phone keypad's own codes (a 9-key pad scanned by TCA8418, 24-sku-framework.md).
        (KeyCode::KEY_NUMERIC_0, '0', '0'), (KeyCode::KEY_NUMERIC_1, '1', '1'), (KeyCode::KEY_NUMERIC_2, '2', '2'),
        (KeyCode::KEY_NUMERIC_3, '3', '3'), (KeyCode::KEY_NUMERIC_4, '4', '4'), (KeyCode::KEY_NUMERIC_5, '5', '5'),
        (KeyCode::KEY_NUMERIC_6, '6', '6'), (KeyCode::KEY_NUMERIC_7, '7', '7'), (KeyCode::KEY_NUMERIC_8, '8', '8'),
        (KeyCode::KEY_NUMERIC_9, '9', '9'), (KeyCode::KEY_NUMERIC_STAR, '*', '*'), (KeyCode::KEY_NUMERIC_POUND, '#', '#'),
    ];
    if let Some(&(_, c)) = LETTERS.iter().find(|(kc, _)| *kc == k) {
        return Some(if shift { c.to_ascii_uppercase() } else { c });
    }
    ROW.iter().find(|(kc, _, _)| *kc == k).map(|&(_, a, b)| if shift { b } else { a })
}

/// A key with a second legend on a phone's keyboard: it types its letter at
/// the press and does not repeat; held for the long press, it asks the
/// shell to replace the letter with the second legend (the shell knows the
/// field).
#[derive(Default)]
struct LetterHold {
    held: Option<(KeyCode, char, Instant, bool)>,
}

impl LetterHold {
    /// A press (1), repeat (2) or release (0) of `code`, which types `ch`:
    /// the message to send, if any.
    fn event(&mut self, code: KeyCode, ch: char, value: i32, now: Instant, long: Duration) -> Option<serde_json::Value> {
        match value {
            1 => {
                self.held = Some((code, ch, now, false));
                Some(serde_json::json!({"t": "key", "key": "", "text": ch.to_string()}))
            }
            2 if self.held.is_some_and(|h| h.0 == code) => self.tick(now, long),
            2 => None,
            _ => {
                self.held = None;
                None
            }
        }
    }

    /// The key still down: its long press, once it is due.
    fn tick(&mut self, now: Instant, long: Duration) -> Option<serde_json::Value> {
        let (_, ch, since, sent) = self.held.as_mut()?;
        if *sent || now.duration_since(*since) < long {
            return None;
        }
        *sent = true;
        Some(serde_json::json!({"t": "key", "key": "", "text": ch.to_string(), "long": true, "retype": true}))
    }
}

/// Reads the keyboards, mice and touchscreens the input opener delivers,
/// as they come (input.rs), and reports their keys to the shell through
/// send, and each device arriving and leaving, with whether it can press
/// OK (a touchscreen can: its taps choose); a touchscreen's finger goes to
/// the main loop, placed on the frame by touch (touch.rs). The sound
/// switch and the lid are no input devices to the shell: their positions
/// go to it (sound_switch.rs, lid.rs).
#[allow(clippy::too_many_arguments)]
pub fn read_inputs(sock: std::os::fd::OwnedFd, nine_key: bool, layer: HashMap<String, String>, ctrl: String, keymap: HashMap<u16, u16>, touch: Option<crate::touch::Place>, send: fn(&serde_json::Value), events: Sender<Event>, timing: &'static KeyTiming) {
    let chord = ctrl_chord(&ctrl);
    std::thread::spawn(move || {
        // Each device's number, which its keys carry ("dev").
        let mut n = 0u32;
        while let Some((dev, kind)) = crate::input::recv(&sock) {
            match kind {
                crate::input::Kind::SoundSwitch => {
                    std::thread::spawn(move || crate::sound_switch::switch_loop(dev, send));
                    continue;
                }
                crate::input::Kind::Lid => {
                    std::thread::spawn(move || crate::lid::lid_loop(dev, send));
                    continue;
                }
                _ => {}
            }
            let place = match (kind, touch) {
                (crate::input::Kind::Touchscreen, None) => continue,
                (_, place) => place,
            };
            let name = dev.name().unwrap_or("").to_string();
            let ok = kind == crate::input::Kind::Touchscreen || dev.supported_keys().is_some_and(|k| crate::input::presses_ok(|c| k.contains(c), &keymap));
            n += 1;
            let (id, bus) = (n, crate::input::bus(dev.input_id().bus_type()));
            send(&serde_json::json!({"t": "input", "name": name, "kind": kind.name(), "added": true, "ok": ok, "id": id, "bus": bus}));
            let layer = layer.clone();
            let keymap = keymap.clone();
            let events = events.clone();
            let chord = chord.clone();
            std::thread::spawn(move || {
                match (kind, place) {
                    (crate::input::Kind::Touchscreen, Some(place)) => crate::touch::touch_loop(dev, id, place, &events),
                    _ => key_loop(dev, id, nine_key, &layer, &keymap, chord.as_deref(), send, &events, timing),
                }
                send(&serde_json::json!({"t": "input", "name": name, "kind": kind.name(), "added": false, "ok": ok, "id": id, "bus": bus}));
            });
        }
    });
}

/// The key that, pressed with Fn, is Ctrl for the next key on a keyboard
/// without a Ctrl key: the keyboard configuration's ctrl, "fn+<key>" (the
/// E71's Fn + Chr is "fn+sym").
fn ctrl_chord(ctrl: &str) -> Option<String> {
    ctrl.strip_prefix("fn+").filter(|k| !k.is_empty()).map(str::to_string)
}

/// What a key does with Ctrl: the editing shortcuts, and Ctrl+Space the
/// input mode (Chinese / English).
fn ctrl_key(ch: char) -> Option<&'static str> {
    Some(match ch.to_ascii_lowercase() {
        'c' => "copy",
        'x' => "cut",
        'v' => "paste",
        'a' => "selectall",
        'z' => "undo",
        ' ' => "ime",
        _ => return None,
    })
}

/// An arrow with Fn: Home, End, Page Up and Page Down (the shell's first,
/// last, pageup, pagedown).
fn fn_arrow(name: &str) -> &str {
    match name {
        "left" => "first",
        "right" => "last",
        "up" => "pageup",
        "down" => "pagedown",
        other => other,
    }
}

/// Chr (the Sym key) as on the E71: tapped alone, the symbol table (sent
/// when it is let go); held with a key, that key's variants, which the
/// shell has: each press says how many times in a row this key was
/// pressed (é, then è in its place, ...).
#[derive(Default)]
struct ChrKey {
    down: bool,
    used: bool,
    last: Option<KeyCode>,
    count: u32,
}

impl ChrKey {
    fn press(&mut self) {
        *self = ChrKey { down: true, ..ChrKey::default() };
    }

    /// Let go: the symbol table, when no key went with it.
    fn release(&mut self) -> Option<serde_json::Value> {
        let tap = self.down && !self.used;
        self.down = false;
        tap.then(|| serde_json::json!({"t": "key", "key": "sym", "text": ""}))
    }

    /// A character key pressed while Chr is down.
    fn typed(&mut self, code: KeyCode, ch: char) -> serde_json::Value {
        self.used = true;
        self.count = if self.last == Some(code) { self.count + 1 } else { 1 };
        self.last = Some(code);
        serde_json::json!({"t": "key", "key": "", "text": ch.to_string(), "chr": self.count})
    }
}

/// Backspace held, off the 9-key pad: from the long press on its repeats
/// are long presses, which delete a word each (the 9-key pad's C key keeps
/// its repeats).
#[derive(Default)]
struct Backspace {
    since: Option<Instant>,
}

impl Backspace {
    fn event(&mut self, value: i32, now: Instant, long: Duration) -> Option<serde_json::Value> {
        match value {
            1 => self.since = Some(now),
            2 => {
                if self.since.is_some_and(|s| now.duration_since(s) >= long) {
                    return Some(serde_json::json!({"t": "key", "key": "backspace", "text": "", "long": true}));
                }
            }
            _ => {
                self.since = None;
                return None;
            }
        }
        Some(serde_json::json!({"t": "key", "key": "backspace", "text": ""}))
    }
}

/// The character a key types with Fn held: its second legend, if it has one.
fn fn_char(layer: &HashMap<String, String>, ch: char) -> Option<char> {
    layer.get(&ch.to_ascii_lowercase().to_string()).and_then(|s| s.chars().next())
}

/// Two taps of Fn this close lock it (Nokia's "press the function key twice
/// quickly").
const DOUBLE_TAP: Duration = Duration::from_millis(500);

#[derive(Clone, Copy, Debug, Default, PartialEq)]
enum FnMode {
    #[default]
    Off,
    Once,
    Lock,
}

impl FnMode {
    /// The key name the shell knows the mode of the modifier `name` (fn,
    /// shift) by: its status bar shows it.
    fn message(self, name: &str) -> serde_json::Value {
        let mode = match self {
            FnMode::Off => "off",
            FnMode::Once => "once",
            FnMode::Lock => "lock",
        };
        serde_json::json!({"t": "key", "key": format!("{name}-{mode}"), "text": ""})
    }
}

/// The Fn key as on Nokia's QWERTY phones: held, the keys type their second
/// legend; tapped alone, the next key does; tapped twice quickly, every key
/// does until it is tapped again. Shift works the same way on those
/// phones: a capital for the next letter, or caps lock.
#[derive(Default)]
struct FnKey {
    held: bool,
    used: bool,
    mode: FnMode,
    tapped: Option<Instant>,
}

impl FnKey {
    /// Fn went down or up: the new mode, when a tap changed it.
    fn key(&mut self, down: bool, now: Instant) -> Option<FnMode> {
        if down {
            self.held = true;
            self.used = false;
            return None;
        }
        self.held = false;
        if self.used {
            return None;
        }
        let quick = self.tapped.is_some_and(|t| now.duration_since(t) <= DOUBLE_TAP);
        self.mode = match self.mode {
            FnMode::Off => FnMode::Once,
            FnMode::Once if quick => FnMode::Lock,
            FnMode::Once | FnMode::Lock => FnMode::Off,
        };
        self.tapped = Some(now);
        Some(self.mode)
    }

    /// Another key went down while this one is held: letting go is no tap.
    fn typed_if_held(&mut self) {
        if self.held {
            self.used = true;
        }
    }

    /// A character key went down: whether it types its second legend, and
    /// the new mode when the tap was for this key only.
    fn typed(&mut self) -> (bool, Option<FnMode>) {
        if self.held {
            self.used = true;
            return (true, None);
        }
        match self.mode {
            FnMode::Once => {
                self.mode = FnMode::Off;
                (true, Some(FnMode::Off))
            }
            FnMode::Lock => (true, None),
            FnMode::Off => (false, None),
        }
    }
}

/// The renderer sees every screen, payment codes included, and decodes
/// images from app packages, so after opening the panel and the keyboards it
/// gives up everything else: no network (an empty network namespace), no
/// root (user nobody, no groups), no way back (no_new_privs), and with
/// Landlock no files but fonts and icons. Must run while the process is
/// single-threaded. Returns the sandbox features in force (none when not
/// started as root). The signal the shell asked for at its own end
/// (Pdeathsig) holds after it: becoming nobody clears it.
pub fn drop_privileges(reads: &Reads) -> Result<Vec<&'static str>, String> {
    const NOBODY: libc::uid_t = 65534;
    let err = |what: &str| format!("{what}: {}", std::io::Error::last_os_error());
    // SAFETY: plain system calls on the calling (only) thread.
    unsafe {
        if libc::geteuid() != 0 {
            return Ok(Vec::new());
        }
        let parent = libc::getppid();
        let mut death: libc::c_int = 0;
        libc::prctl(libc::PR_GET_PDEATHSIG, &mut death as *mut libc::c_int);
        if libc::unshare(libc::CLONE_NEWNET) != 0 {
            return Err(err("unshare(CLONE_NEWNET)"));
        }
        if libc::setgroups(0, std::ptr::null()) != 0 {
            return Err(err("setgroups"));
        }
        if libc::setgid(NOBODY) != 0 || libc::setuid(NOBODY) != 0 {
            return Err(err("setuid nobody"));
        }
        if libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0 {
            return Err(err("no_new_privs"));
        }
        if libc::setuid(0) == 0 {
            return Err("could become root again".into());
        }
        if death != 0 && !crate::input::die_with(death, parent) {
            return Err("the shell that started it is gone".into());
        }
    }
    let mut got = vec!["sandboxed"];
    if restrict_files(reads)? {
        got.push("landlock");
    }
    // Check from the outside view: no secrets, no network, and with Landlock
    // not even world-readable files outside the allowed trees.
    if std::fs::File::open("/etc/shadow").is_ok() {
        return Err("can still read /etc/shadow".into());
    }
    if got.contains(&"landlock") && std::fs::File::open("/etc/passwd").is_ok() {
        return Err("Landlock does not hold: /etc/passwd is readable".into());
    }
    if std::net::TcpStream::connect_timeout(&"127.0.0.1:22".parse().unwrap(), Duration::from_millis(300)).is_ok() {
        return Err("can still reach the network".into());
    }
    crate::seccomp::apply()?;
    got.push("seccomp");
    Ok(got)
}

/// What Landlock leaves the renderer: trees it may read (files and
/// directories under them) and directories it may only list.
#[derive(Debug, Default, PartialEq)]
pub struct Reads {
    pub trees: Vec<String>,
    pub lists: Vec<String>,
}

impl Reads {
    /// The fonts and system icons (/usr), installed apps' icons and
    /// PHONE_RENDERER_READ's; `gpu`: the DRM nodes the GPU path renders on.
    pub fn renderer(gpu: &[&crate::node::Info]) -> Reads {
        Reads::with(std::env::var("PHONE_RENDERER_READ").ok().as_deref(), gpu)
    }

    /// With `gpu`, also what libdrm's drmGetDevice2 reads to find the
    /// nodes' device: EGL takes no GPU without it (42-gpu-rendering.md §4).
    /// It lists /dev/dri and reads the device's uevent through
    /// /sys/dev/char, which resolves to the device's directory.
    fn with(extra: Option<&str>, gpu: &[&crate::node::Info]) -> Reads {
        let mut trees = vec!["/usr".to_string(), "/var/lib/phone/packages".to_string()];
        trees.extend(extra.unwrap_or("").split(':').filter(|p| !p.is_empty()).map(String::from));
        let mut lists = Vec::new();
        if !gpu.is_empty() {
            lists.push("/dev/dri".to_string());
        }
        for n in gpu {
            if !n.device.is_empty() && !trees.contains(&n.device) {
                trees.push(n.device.clone());
            }
        }
        Reads { trees, lists }
    }
}

/// Read-only access to `reads`; nothing else, and no writes. The display
/// and keyboards are already open. Returns false on kernels without
/// Landlock.
pub fn restrict_files(reads: &Reads) -> Result<bool, String> {
    use landlock::{path_beneath_rules, Access, AccessFs, Ruleset, RulesetAttr, RulesetCreatedAttr, RulesetStatus, ABI};
    let abi = ABI::V3;
    let status = Ruleset::default()
        .handle_access(AccessFs::from_all(abi))
        .map_err(s)?
        .create()
        .map_err(s)?
        .add_rules(path_beneath_rules(&reads.trees, AccessFs::from_read(abi)))
        .map_err(s)?
        .add_rules(path_beneath_rules(&reads.lists, AccessFs::ReadDir))
        .map_err(s)?
        .restrict_self()
        .map_err(s)?;
    Ok(status.ruleset != RulesetStatus::NotEnforced)
}

/// A keyboard's keys as the shell gets them (the power key and the mouse
/// aside): Fn and Shift held, tapped or locked, the second legends, Chr,
/// Ctrl or the chord that stands in for it, the editing combinations, the
/// 9-key pad's long presses.
pub struct Keyboard<'a> {
    nine_key: bool,
    layer: &'a HashMap<String, String>,
    // The key that is Ctrl for the next one with Fn (the configuration's).
    chord: Option<&'a str>,
    ctrl: bool,
    ctrl_next: bool,
    chr: ChrKey,
    backspace: Backspace,
    // The key whose press Ctrl or Chr took: its repeats and release too.
    taken: Option<KeyCode>,
    fn_key: FnKey,
    // Plain held Shift where the keyboard is not a phone's (no Fn layer).
    shift_key: FnKey,
    shift_held: bool,
    // Whether the character key down types its second legend (its repeats
    // too), and a capital (sticky Shift).
    second: bool,
    capital: bool,
    letter_held: LetterHold,
    // The end key since it went down, and whether its long press went.
    end_held: Option<(Instant, bool)>,
    // A 9-key digit, * or # being held: sent on release, or as a long press.
    held: Option<(char, Instant, bool)>,
    // Ctrl and Alt held, whether a key went with them (sticky keys: a tap
    // is for the next key), and Alt tapped for the next one: Alt + a key
    // is that key held (as in a terminal).
    ctrl_used: bool,
    alt: bool,
    alt_used: bool,
    alt_next: bool,
    // The volume keys down, and both together since when (the
    // accessibility shortcut) and whether it went.
    vol: [bool; 2],
    both: Option<(Instant, bool)>,
    // The long press and sticky keys (Settings › Accessibility).
    timing: Timing,
}

/// Which volume key a key is, if one (F8 and F9 on a PC keyboard).
fn volume(code: KeyCode) -> Option<usize> {
    match code {
        KeyCode::KEY_VOLUMEDOWN | KeyCode::KEY_F8 => Some(0),
        KeyCode::KEY_VOLUMEUP | KeyCode::KEY_F9 => Some(1),
        _ => None,
    }
}

impl<'a> Keyboard<'a> {
    pub fn new(nine_key: bool, layer: &'a HashMap<String, String>, chord: Option<&'a str>) -> Keyboard<'a> {
        Keyboard {
            nine_key,
            layer,
            chord,
            ctrl: false,
            ctrl_next: false,
            chr: ChrKey::default(),
            backspace: Backspace::default(),
            taken: None,
            fn_key: FnKey::default(),
            shift_key: FnKey::default(),
            shift_held: false,
            second: false,
            capital: false,
            letter_held: LetterHold::default(),
            end_held: None,
            held: None,
            ctrl_used: false,
            alt: false,
            alt_used: false,
            alt_next: false,
            vol: [false; 2],
            both: None,
            timing: Timing::default(),
        }
    }

    pub fn set_timing(&mut self, t: Timing) {
        self.timing = t;
    }

    /// Shift latches (a tap for the next letter, two for caps lock): on a
    /// phone's keyboard always, on another with sticky keys.
    fn shift_latches(&self) -> bool {
        !self.layer.is_empty() || self.timing.sticky
    }

    /// A press (1), repeat (2) or release (0) of code, or its long press
    /// due (TICK): what goes to the shell.
    pub fn key(&mut self, code: KeyCode, value: i32, now: Instant) -> Vec<serde_json::Value> {
        let mut out = Vec::new();
        self.event(code, value, now, &mut out);
        out
    }

    /// The key held: the long presses that are due.
    fn tick(&mut self, now: Instant, out: &mut Vec<serde_json::Value>) {
        out.extend(self.letter_held.tick(now, self.timing.long));
        self.digit_long(now, out);
        self.end_long(now, out);
        if let Some((since, sent)) = self.both.as_mut() {
            if !*sent && self.vol == [true, true] && now.duration_since(*since) >= self.timing.long {
                out.push(serde_json::json!({"t": "key", "key": "a11y", "text": "", "long": true}));
                *sent = true;
            }
        }
    }

    /// A 9-key digit, * or # held: its long press once due.
    fn digit_long(&mut self, now: Instant, out: &mut Vec<serde_json::Value>) {
        if let Some((c, since, sent)) = self.held.as_mut() {
            if !*sent && now.duration_since(*since) >= self.timing.long {
                out.push(serde_json::json!({"t": "key", "key": "", "text": c.to_string(), "long": true}));
                *sent = true;
            }
        }
    }

    /// The end key held: its long press once due.
    fn end_long(&mut self, now: Instant, out: &mut Vec<serde_json::Value>) {
        if let Some((since, sent)) = self.end_held.as_mut() {
            if !*sent && now.duration_since(*since) >= self.timing.long {
                out.push(serde_json::json!({"t": "key", "key": "f4", "text": "", "long": true}));
                *sent = true;
            }
        }
    }

    /// The volume keys: one alone as ever; both at once the accessibility
    /// shortcut, sent when one is let go (the first press has stepped the
    /// volume already), or held for the long press. true: handled.
    fn volume_key(&mut self, i: usize, value: i32, now: Instant, out: &mut Vec<serde_json::Value>) -> bool {
        match value {
            1 => {
                self.vol[i] = true;
                if self.vol[1 - i] {
                    self.both = Some((now, false));
                    return true;
                }
                false
            }
            0 => {
                self.vol[i] = false;
                if let Some((since, sent)) = self.both {
                    if !sent {
                        out.push(serde_json::json!({"t": "key", "key": "a11y", "text": ""}));
                    }
                    self.both = (self.vol[1 - i]).then_some((since, true));
                }
                true
            }
            _ => self.both.is_some(),
        }
    }

    fn event(&mut self, code: KeyCode, value: i32, now: Instant, out: &mut Vec<serde_json::Value>) {
        let key = |name: &str| serde_json::json!({"t": "key", "key": name, "text": ""});
        if value == crate::keygate::TICK {
            self.tick(now, out);
            return;
        }
        if self.taken == Some(code) {
            if value == 0 {
                self.taken = None;
            }
            return;
        }
        if let Some(i) = volume(code) {
            if self.volume_key(i, value, now, out) {
                return;
            }
        }
        let name = named(code);
        let fn_on = self.fn_key.held || self.fn_key.mode == FnMode::Once;
        // A key pressed while Fn or Shift is held makes letting go of it no
        // tap; the chord's Ctrl is for the next character key only.
        if value == 1 && !matches!(code, KeyCode::KEY_FN | KeyCode::KEY_LEFTSHIFT | KeyCode::KEY_RIGHTSHIFT) {
            self.fn_key.typed_if_held();
            self.shift_key.typed_if_held();
            if text(code, false).is_none() {
                self.ctrl_next = false;
            }
        }
        let modifier = matches!(
            code,
            KeyCode::KEY_FN | KeyCode::KEY_LEFTSHIFT | KeyCode::KEY_RIGHTSHIFT | KeyCode::KEY_LEFTCTRL | KeyCode::KEY_RIGHTCTRL | KeyCode::KEY_LEFTALT | KeyCode::KEY_RIGHTALT
        );
        let alt_on = self.alt || self.alt_next;
        if value == 1 && !modifier {
            self.alt_next = false;
            self.alt_used |= self.alt;
            self.ctrl_used |= self.ctrl;
        }
        if value == 1 && !self.nine_key && name.is_some() && name == self.chord && fn_on {
            // Fn + Chr (the configured chord): Ctrl for the next key.
            if let (_, Some(m)) = self.fn_key.typed() {
                out.push(m.message("fn"));
            }
            self.ctrl_next = true;
            self.taken = Some(code);
            return;
        }
        if value == 1 && !self.nine_key && code == KeyCode::KEY_SPACE && fn_on {
            // Fn + Space: the writing language (the E71's input options).
            if let (_, Some(m)) = self.fn_key.typed() {
                out.push(m.message("fn"));
            }
            out.push(key("lang"));
            self.taken = Some(code);
            return;
        }
        if name == Some("sym") && !self.nine_key {
            match value {
                1 if self.shift_held || self.shift_key.mode != FnMode::Off => {
                    // Shift + Chr: the writing language (E90).
                    if let (_, Some(m)) = self.shift_key.typed() {
                        out.push(m.message("shift"));
                    }
                    out.push(key("lang"));
                    self.taken = Some(code);
                }
                1 => self.chr.press(),
                0 => out.extend(self.chr.release()),
                _ => {}
            }
            return;
        }
        if let Some(n) = name.filter(|_| !self.nine_key && value != 0) {
            let arrow = matches!(n, "left" | "right" | "up" | "down");
            if n == "backspace" && value == 1 && (self.shift_held || self.fn_key.held) {
                // Shift or Fn + Backspace deletes forward (no Delete key).
                out.push(key("delete"));
                self.taken = Some(code);
                return;
            }
            if arrow || matches!(n, "first" | "last") {
                let mut n = n;
                if arrow && fn_on {
                    if let (_, Some(m)) = self.fn_key.typed() {
                        out.push(m.message("fn"));
                    }
                    n = fn_arrow(n);
                }
                // Held Shift selects (the E71's "hold shift and scroll"),
                // and with sticky keys Shift tapped.
                let mut m = key(n);
                let latched = self.timing.sticky && !self.shift_held && self.shift_key.mode != FnMode::Off;
                if (self.shift_held || latched) && matches!(n, "left" | "right" | "up" | "down" | "first" | "last") {
                    m["shift"] = serde_json::json!(true);
                    out.push(m);
                    if latched && value == 1 {
                        if let (_, Some(md)) = self.shift_key.typed() {
                            out.push(md.message("shift"));
                        }
                    }
                    return;
                }
                out.push(m);
                return;
            }
        }
        if name == Some("backspace") && !self.nine_key {
            if value == 1 && alt_on {
                // Alt + Backspace: Backspace held, a word at a time.
                out.push(serde_json::json!({"t": "key", "key": "backspace", "text": "", "long": true}));
                self.taken = Some(code);
                return;
            }
            out.extend(self.backspace.event(value, now, self.timing.long));
            return;
        }
        match code {
            KeyCode::KEY_LEFTSHIFT | KeyCode::KEY_RIGHTSHIFT => {
                self.shift_held = value != 0;
                if value != 2 && self.shift_latches() {
                    if let Some(m) = self.shift_key.key(value == 1, now) {
                        out.push(m.message("shift"));
                    }
                }
            }
            KeyCode::KEY_LEFTCTRL | KeyCode::KEY_RIGHTCTRL => {
                self.ctrl = value != 0;
                match value {
                    1 => self.ctrl_used = false,
                    0 if self.timing.sticky && !self.ctrl_used => self.ctrl_next = true,
                    _ => {}
                }
            }
            KeyCode::KEY_LEFTALT | KeyCode::KEY_RIGHTALT => {
                self.alt = value != 0;
                match value {
                    1 => self.alt_used = false,
                    0 if self.timing.sticky && !self.alt_used => self.alt_next = true,
                    _ => {}
                }
            }
            KeyCode::KEY_FN => {
                if value != 2 {
                    if let Some(m) = self.fn_key.key(value == 1, now) {
                        out.push(m.message("fn"));
                    }
                }
            }
            _ if (self.ctrl || self.ctrl_next) && value == 1 && text(code, false).is_some() => {
                self.ctrl_next = false;
                if let Some(k) = text(code, false).and_then(ctrl_key) {
                    out.push(key(k));
                }
                self.taken = Some(code);
            }
            _ => {
                if let Some(name) = name {
                    // The end key is the power key too where the board has
                    // none: held, it says so once (as the 9-key digits do,
                    // from the key's own repeats); its press goes at once.
                    if name == "f4" {
                        match value {
                            1 => {
                                self.end_held = Some((now, false));
                                out.push(key(name));
                            }
                            2 => self.end_long(now, out),
                            _ => self.end_held = None,
                        }
                        return;
                    }
                    if value != 0 {
                        out.push(key(name));
                    }
                    return;
                }
                if value == 1 && self.shift_latches() && text(code, false).is_some() {
                    let (on, changed) = self.shift_key.typed();
                    self.capital = on;
                    if let Some(m) = changed {
                        out.push(m.message("shift"));
                    }
                }
                let shift = if self.shift_latches() { self.capital } else { self.shift_held };
                let Some(ch) = text(code, shift) else { return };
                if self.chr.down && value == 1 {
                    out.push(self.chr.typed(code, ch));
                    self.taken = Some(code);
                    return;
                }
                if value == 1 && !self.layer.is_empty() {
                    let (on, changed) = self.fn_key.typed();
                    self.second = on;
                    if let Some(m) = changed {
                        out.push(m.message("fn"));
                    }
                }
                let ch = if self.second { fn_char(self.layer, ch).unwrap_or(ch) } else { ch };
                if value == 1 && alt_on {
                    // Alt + a key: the key held (its long press), as in a
                    // terminal; with sticky keys Alt tapped first.
                    out.push(serde_json::json!({"t": "key", "key": "", "text": ch.to_string(), "long": true}));
                    self.taken = Some(code);
                    return;
                }
                if self.nine_key && (ch.is_ascii_digit() || ch == '*' || ch == '#') {
                    match value {
                        1 => self.held = Some((ch, now, false)),
                        2 => self.digit_long(now, out),
                        _ => {
                            if let Some((c, _, sent)) = self.held.take() {
                                if !sent {
                                    out.push(serde_json::json!({"t": "key", "key": "", "text": c.to_string()}));
                                }
                            }
                        }
                    }
                } else if !self.second && fn_char(self.layer, ch).is_some() {
                    // Held, a key with a second legend types that instead,
                    // as the E71's keys did: no repeats, and at the long
                    // press the shell replaces the letter typed.
                    out.extend(self.letter_held.event(code, ch, value, now, self.timing.long));
                } else if value != 0 {
                    out.push(serde_json::json!({"t": "key", "key": "", "text": ch.to_string()}));
                }
            }
        }
    }
}

/// The code a key of a whole phone's own keypad is read as (its board's
/// keymap); any other code as it is.
fn remap(keymap: &HashMap<u16, u16>, code: KeyCode) -> KeyCode {
    keymap.get(&code.code()).map_or(code, |&c| KeyCode::new(c))
}

/// Whether the kernel has key down on dev now (EVIOCGKEY), None where it
/// cannot say.
fn key_down(dev: &evdev::Device, key: KeyCode) -> Option<bool> {
    dev.get_key_state().ok().map(|keys| keys.contains(key))
}

/// A key line naming device id, the one it came from ("dev").
fn from_device(m: &serde_json::Value, id: u32) -> serde_json::Value {
    let mut m = m.clone();
    m["dev"] = id.into();
    m
}

#[allow(clippy::too_many_arguments)]
fn key_loop(mut dev: evdev::Device, id: u32, nine_key: bool, layer: &HashMap<String, String>, keymap: &HashMap<u16, u16>, chord: Option<&str>, out: fn(&serde_json::Value), pointer: &Sender<Event>, timing: &KeyTiming) {
    let send = |m: &serde_json::Value| out(&from_device(m, id));
    let mut keyboard = Keyboard::new(nine_key, layer, chord);
    let mut power = PowerKey::default();
    let mut gate = Gate::default();
    // Each key's time for perf.rs: the kernel's, on the clock of the
    // page flips.
    let monotonic = crate::perf::monotonic_events(dev.as_raw_fd());
    let inputs = &crate::perf::INPUTS;
    loop {
        let t = timing.now();
        keyboard.timing = t;
        let now = Instant::now();
        let due = [power.due(now, t.power_long).map(|d| now + d), gate.due(&t)].into_iter().flatten().min();
        if let Some(at) = due {
            if !readable(dev.as_raw_fd(), at.saturating_duration_since(now)) {
                let now = Instant::now();
                if let Some(m) = power.held(now, t.power_long, || key_down(&dev, KeyCode::KEY_POWER)) {
                    power_log("held", Some(&m));
                    send(&m);
                }
                let stamp = crate::perf::Stamp::now(crate::perf::Source::Read);
                for (code, value) in gate.fire(now, &t) {
                    for mut m in keyboard.key(code, value, now) {
                        inputs.stamp(&mut m, crate::perf::Kind::Repeat, stamp);
                        send(&m);
                    }
                }
                continue;
            }
        }
        let Ok(events) = dev.fetch_events() else { return };
        // One move per batch of events (a report), however many axes it has.
        let (mut dx, mut dy) = (0, 0);
        for ev in events {
            let stamp = crate::perf::event_time(ev.timestamp(), monotonic);
            let (code, value) = match ev.destructure() {
                EventSummary::Key(_, code, value) => (remap(keymap, code), value),
                EventSummary::RelativeAxis(_, RelativeAxisCode::REL_X, value) => {
                    dx += value;
                    continue;
                }
                EventSummary::RelativeAxis(_, RelativeAxisCode::REL_Y, value) => {
                    dy += value;
                    continue;
                }
                EventSummary::RelativeAxis(_, axis, value) => {
                    if let Some((key, steps)) = crate::input::wheel(axis, value) {
                        for _ in 0..steps {
                            let mut m = serde_json::json!({"t": "key", "key": key, "text": ""});
                            inputs.stamp(&mut m, crate::perf::Kind::Key, stamp);
                            send(&m);
                        }
                    }
                    continue;
                }
                _ => continue,
            };
            if code == KeyCode::BTN_LEFT {
                if value == 1 {
                    let _ = pointer.send(Event::Click(id));
                }
                continue;
            }
            if let Some(key) = crate::input::button(code) {
                if value == 1 {
                    let mut m = serde_json::json!({"t": "key", "key": key, "text": ""});
                    inputs.stamp(&mut m, crate::perf::Kind::Key, stamp);
                    send(&m);
                }
                continue;
            }
            if value == 1 {
                let _ = pointer.send(Event::Typed);
            }
            if code == KeyCode::KEY_POWER {
                let now = Instant::now();
                let m = match value {
                    1 => {
                        power.press(now);
                        power_log("down", None);
                        None
                    }
                    2 => power.held(now, t.power_long, || Some(true)),
                    _ => {
                        let m = power.release(now, t.power_long);
                        power_log("up", m.as_ref());
                        m
                    }
                };
                if let Some(m) = m {
                    if value == 2 {
                        power_log("repeating", Some(&m));
                    }
                    send(&m);
                }
                continue;
            }
            let now = Instant::now();
            // A repeat is the keyboard's own (autorepeat), not a press.
            let kind = if value == 2 { crate::perf::Kind::Repeat } else { crate::perf::Kind::Key };
            for (code, value) in gate.event(code, value, now, &t) {
                for mut m in keyboard.key(code, value, now) {
                    inputs.stamp(&mut m, kind, stamp);
                    send(&m);
                }
            }
        }
        if dx != 0 || dy != 0 {
            let _ = pointer.send(Event::Move(dx, dy));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // Every key line says which device it came from, so the shell can hold
    // back a keyboard plugged in while the phone is locked; the rest of
    // the line is as the keyboard made it.
    #[test]
    fn a_key_names_its_device() {
        let m = from_device(&serde_json::json!({"t": "key", "key": "call", "text": "", "in": 7}), 3);
        assert_eq!(m, serde_json::json!({"t": "key", "key": "call", "text": "", "in": 7, "dev": 3}));
    }

    // The shell starts the renderer with a parent-death signal (Pdeathsig);
    // becoming nobody cleared it, so on the phone a renderer outlived a
    // killed shell. Root only (the sandbox is not applied otherwise): the
    // phone, QEMU, or podman unshare cargo test --release; in a child, as
    // the sandbox is for good.
    #[test]
    fn the_sandbox_keeps_the_parent_death_signal() {
        // SAFETY: geteuid has no preconditions.
        if unsafe { libc::geteuid() } != 0 {
            eprintln!("not root: skipped");
            return;
        }
        if std::env::var_os("PAINT_PDEATH_CHILD").is_some() {
            let mut sig: libc::c_int = 0;
            // SAFETY: plain prctl calls in the test's own child process.
            unsafe { libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGTERM) };
            drop_privileges(&Reads::renderer(&[])).expect("sandbox");
            unsafe { libc::prctl(libc::PR_GET_PDEATHSIG, &mut sig as *mut libc::c_int) };
            std::process::exit(if sig == libc::SIGTERM { 0 } else { 7 });
        }
        let st = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "kms::tests::the_sandbox_keeps_the_parent_death_signal", "--nocapture", "--test-threads=1"])
            .env("PAINT_PDEATH_CHILD", "1")
            .status()
            .unwrap();
        assert_eq!(st.code(), Some(0), "the parent-death signal is gone after the sandbox");
    }

    // Mesa's EGL takes a GPU only once libdrm has found its device
    // (drmGetDevice2): it lists /dev/dri and reads the node's device in
    // sysfs. Landlock refused both, and the OnePlus 6T's GPU path drew on
    // the CPU with "EGL is not initialized"; the software rasterizer, which
    // needs no device, still worked.
    #[test]
    fn the_sandbox_lets_mesa_find_the_gpus_device() {
        let under = |roots: &[String], p: &str| roots.iter().any(|t| p == t || p.starts_with(&format!("{t}/")));
        let soc = "/sys/devices/platform/soc@0";
        let dpu = format!("{soc}/ae00000.display-subsystem");
        let gpu = format!("{soc}/5000000.gpu");
        let card = crate::node::Info { path: "/dev/dri/card0".into(), driver: "msm".into(), device: dpu.clone() };
        let r = Reads::with(None, &[&card]);
        assert!(under(&r.lists, "/dev/dri"), "libdrm lists /dev/dri: {r:?}");
        assert!(under(&r.trees, &format!("{dpu}/uevent")), "libdrm reads the device's uevent: {r:?}");
        assert!(!under(&r.trees, "/dev/dri/card0"), "the nodes are open already: {r:?}");
        assert!(!under(&r.trees, &format!("{soc}/a600000.usb/uevent")), "no other device: {r:?}");
        // The display's device and the GPU's render node on another (PRIME).
        let render = crate::node::Info { path: "/dev/dri/renderD128".into(), driver: "msm".into(), device: gpu.clone() };
        let r = Reads::with(Some("/opt/x"), &[&card, &render, &card]);
        assert!(under(&r.trees, &format!("{gpu}/uevent")) && under(&r.trees, &format!("{dpu}/uevent")), "{r:?}");
        assert_eq!(r.trees.iter().filter(|t| **t == dpu).count(), 1, "{r:?}");
        assert!(under(&r.trees, "/opt/x/icon.png"), "{r:?}");
        // The CPU path: neither.
        let r = Reads::with(None, &[]);
        assert!(r.lists.is_empty() && !r.trees.iter().any(|t| t.starts_with("/sys")), "{r:?}");
    }

    #[test]
    fn fn_tapped_held_and_locked() {
        let t0 = Instant::now();
        let ms = |n| t0 + Duration::from_millis(n);
        // Held with a key: that key, and no mode.
        let mut f = FnKey::default();
        assert_eq!(f.key(true, ms(0)), None);
        assert_eq!(f.typed(), (true, None));
        assert_eq!(f.key(false, ms(100)), None);
        assert_eq!(f.typed(), (false, None));
        // Tapped: the next key only.
        assert_eq!(f.key(true, ms(1000)), None);
        assert_eq!(f.key(false, ms(1050)), Some(FnMode::Once));
        assert_eq!(f.typed(), (true, Some(FnMode::Off)));
        assert_eq!(f.typed(), (false, None));
        // Tapped twice quickly: locked until tapped again.
        f.key(true, ms(3000));
        assert_eq!(f.key(false, ms(3050)), Some(FnMode::Once));
        f.key(true, ms(3200));
        assert_eq!(f.key(false, ms(3250)), Some(FnMode::Lock));
        assert_eq!(f.typed(), (true, None));
        assert_eq!(f.typed(), (true, None));
        f.key(true, ms(5000));
        assert_eq!(f.key(false, ms(5050)), Some(FnMode::Off));
        assert_eq!(f.typed(), (false, None));
        // A second tap too late takes the tap back.
        f.key(true, ms(8000));
        assert_eq!(f.key(false, ms(8050)), Some(FnMode::Once));
        f.key(true, ms(9000));
        assert_eq!(f.key(false, ms(9050)), Some(FnMode::Off));
    }

    #[test]
    fn modifier_messages() {
        assert_eq!(FnMode::Once.message("fn")["key"], "fn-once");
        assert_eq!(FnMode::Lock.message("shift")["key"], "shift-lock");
        assert_eq!(FnMode::Off.message("shift")["key"], "shift-off");
    }

    #[test]
    fn held_letter_asks_for_its_second_legend() {
        let t0 = Instant::now();
        let ms = |n| t0 + Duration::from_millis(n);
        let mut h = LetterHold::default();
        let long = Timing::default().long;
        assert_eq!(h.event(KeyCode::KEY_W, 'w', 1, ms(0), long).unwrap()["text"], "w");
        assert_eq!(h.event(KeyCode::KEY_W, 'w', 2, ms(300), long), None);
        let m = h.event(KeyCode::KEY_W, 'w', 2, ms(long.as_millis() as u64), long).unwrap();
        assert_eq!((m["text"].as_str(), m["long"].as_bool(), m["retype"].as_bool()), (Some("w"), Some(true), Some(true)));
        assert_eq!(h.event(KeyCode::KEY_W, 'w', 2, ms(900), long), None);
        assert_eq!(h.event(KeyCode::KEY_W, 'w', 0, ms(950), long), None);
        // A quick tap: the letter only. Another key's repeat is not this one's.
        h.event(KeyCode::KEY_E, 'e', 1, ms(2000), long);
        assert_eq!(h.event(KeyCode::KEY_R, 'r', 2, ms(2700), long), None);
        assert_eq!(h.event(KeyCode::KEY_E, 'e', 0, ms(2100), long), None);
    }

    /// What a keyboard sends for presses (1), repeats (2) and releases (0)
    /// at the given milliseconds: each message's key, or its text.
    fn sends(kb: &mut Keyboard, seq: &[(KeyCode, i32, u64)]) -> Vec<String> {
        let t0 = Instant::now();
        let mut out = Vec::new();
        for &(code, value, ms) in seq {
            for m in kb.key(code, value, t0 + Duration::from_millis(ms)) {
                let mut s = m["key"].as_str().unwrap_or_default().to_string();
                if s.is_empty() {
                    s = m["text"].as_str().unwrap_or_default().to_string();
                }
                for flag in ["shift", "long", "retype"] {
                    if m[flag] == serde_json::json!(true) {
                        s += &format!("+{flag}");
                    }
                }
                if let Some(n) = m["chr"].as_u64() {
                    s += &format!("+chr{n}");
                }
                out.push(s);
            }
        }
        out
    }

    fn phone_layer() -> HashMap<String, String> {
        [("w", "1"), ("e", "2"), (".", "0")].iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    use KeyCode as K;

    #[test]
    fn fn_with_arrows_goes_to_the_ends_and_pages() {
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, Some("sym"));
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 0), (K::KEY_LEFT, 1, 10), (K::KEY_RIGHT, 1, 20), (K::KEY_UP, 1, 30), (K::KEY_DOWN, 1, 40), (K::KEY_FN, 0, 50)]);
        assert_eq!(got, ["first", "last", "pageup", "pagedown"]);
        // Tapped, Fn takes the next arrow only.
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 2000), (K::KEY_FN, 0, 2050), (K::KEY_RIGHT, 1, 2100), (K::KEY_RIGHT, 1, 2200)]);
        assert_eq!(got, ["fn-once", "fn-off", "last", "right"]);
        // A PC keyboard's own keys.
        assert_eq!(sends(&mut kb, &[(K::KEY_HOME, 1, 0), (K::KEY_END, 1, 0), (K::KEY_PAGEDOWN, 1, 0), (K::KEY_DELETE, 1, 0)]), ["first", "last", "pagedown", "delete"]);
    }

    #[test]
    fn held_shift_selects_and_is_no_tap() {
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, Some("sym"));
        let got = sends(&mut kb, &[(K::KEY_LEFTSHIFT, 1, 0), (K::KEY_LEFT, 1, 10), (K::KEY_LEFT, 2, 300), (K::KEY_LEFTSHIFT, 0, 400), (K::KEY_LEFT, 1, 500)]);
        assert_eq!(got, ["left+shift", "left+shift", "left"]);
        let got = sends(&mut kb, &[(K::KEY_LEFTSHIFT, 1, 0), (K::KEY_FN, 1, 5), (K::KEY_RIGHT, 1, 10), (K::KEY_FN, 0, 20), (K::KEY_LEFTSHIFT, 0, 30)]);
        assert_eq!(got, ["last+shift"]);
        // Shift (or Fn) with Backspace deletes forward.
        let got = sends(&mut kb, &[(K::KEY_LEFTSHIFT, 1, 0), (K::KEY_BACKSPACE, 1, 10), (K::KEY_BACKSPACE, 0, 20), (K::KEY_LEFTSHIFT, 0, 30)]);
        assert_eq!(got, ["delete"]);
    }

    #[test]
    fn fn_then_chr_is_ctrl_for_the_next_key() {
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, Some("sym"));
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 0), (K::KEY_F7, 1, 10), (K::KEY_F7, 0, 20), (K::KEY_FN, 0, 30), (K::KEY_C, 1, 40), (K::KEY_C, 2, 700), (K::KEY_C, 0, 800), (K::KEY_C, 1, 900)]);
        assert_eq!(got, ["copy", "c"]);
        // Fn tapped, then Chr: the same.
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 2000), (K::KEY_FN, 0, 2050), (K::KEY_F7, 1, 2100), (K::KEY_F7, 0, 2150), (K::KEY_V, 1, 2200)]);
        assert_eq!(got, ["fn-once", "fn-off", "paste"]);
        // Another key first: the chord is spent.
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 3000), (K::KEY_F7, 1, 3010), (K::KEY_F7, 0, 3020), (K::KEY_FN, 0, 3030), (K::KEY_ENTER, 1, 3040), (K::KEY_C, 1, 3050)]);
        assert_eq!(got, ["enter", "c"]);
        // Without the chord in the configuration, Fn + Chr is Chr.
        let mut kb = Keyboard::new(false, &layer, None);
        assert_eq!(sends(&mut kb, &[(K::KEY_FN, 1, 0), (K::KEY_F7, 1, 10), (K::KEY_F7, 0, 20), (K::KEY_FN, 0, 30)]), ["sym"]);
        // A Ctrl key: the shortcuts, Ctrl+Space the input mode; other
        // letters type nothing.
        let empty = HashMap::new();
        let mut kb = Keyboard::new(false, &empty, None);
        let got = sends(&mut kb, &[(K::KEY_LEFTCTRL, 1, 0), (K::KEY_A, 1, 1), (K::KEY_X, 1, 2), (K::KEY_Z, 1, 3), (K::KEY_SPACE, 1, 4), (K::KEY_Q, 1, 5), (K::KEY_Q, 0, 6), (K::KEY_LEFTCTRL, 0, 7), (K::KEY_Q, 1, 8)]);
        assert_eq!(got, ["selectall", "cut", "undo", "ime", "q"]);
    }

    #[test]
    fn chr_tapped_held_and_with_shift() {
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, Some("sym"));
        // Tapped alone: the symbol table, when it is let go.
        assert_eq!(sends(&mut kb, &[(K::KEY_F7, 1, 0), (K::KEY_F7, 2, 700)]), Vec::<String>::new());
        assert_eq!(sends(&mut kb, &[(K::KEY_F7, 0, 800)]), ["sym"]);
        // Held with a letter: its presses counted, no symbol table; a key
        // with a second legend types no digit when held.
        let got = sends(&mut kb, &[(K::KEY_F7, 1, 0), (K::KEY_E, 1, 10), (K::KEY_E, 2, 700), (K::KEY_E, 0, 750), (K::KEY_E, 1, 800), (K::KEY_A, 1, 900), (K::KEY_F7, 0, 1000)]);
        assert_eq!(got, ["e+chr1", "e+chr2", "a+chr1"]);
        // With Shift: the writing language; Shift tapped is used up. Fn +
        // Space too (the E71's), not with Fn locked.
        let got = sends(&mut kb, &[(K::KEY_LEFTSHIFT, 1, 2000), (K::KEY_LEFTSHIFT, 0, 2050), (K::KEY_F7, 1, 2100), (K::KEY_F7, 0, 2200)]);
        assert_eq!(got, ["shift-once", "shift-off", "lang"]);
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 3000), (K::KEY_SPACE, 1, 3010), (K::KEY_SPACE, 0, 3020), (K::KEY_FN, 0, 3030)]);
        assert_eq!(got, ["lang"]);
        let got = sends(&mut kb, &[(K::KEY_FN, 1, 4000), (K::KEY_FN, 0, 4010), (K::KEY_FN, 1, 4100), (K::KEY_FN, 0, 4110), (K::KEY_SPACE, 1, 4200)]);
        assert_eq!(got, ["fn-once", "fn-lock", " "]);
        // The 9-key pad keeps its keys as they were.
        let mut t9 = Keyboard::new(true, &layer, Some("sym"));
        assert_eq!(sends(&mut t9, &[(K::KEY_F7, 1, 0), (K::KEY_F7, 0, 10)]), ["sym"]);
    }

    #[test]
    fn a_boards_keymap_reads_its_keys_as_ours() {
        // A KaiOS phone's keypad: KEY_MENU and KEY_BACK are the soft keys,
        // KEY_1 the 9-key 1 (held: its long press), KEY_POWER the red key.
        let keymap: HashMap<u16, u16> = [(K::KEY_MENU, K::KEY_F1), (K::KEY_BACK, K::KEY_F2), (K::KEY_1, K::KEY_NUMERIC_1), (K::KEY_POWER, K::KEY_F4)]
            .iter()
            .map(|(a, b)| (a.code(), b.code()))
            .collect();
        assert_eq!(remap(&keymap, K::KEY_MENU), K::KEY_F1);
        assert_eq!(remap(&keymap, K::KEY_POWER), K::KEY_F4);
        assert_eq!(remap(&keymap, K::KEY_UP), K::KEY_UP);
        assert_eq!(remap(&HashMap::new(), K::KEY_MENU), K::KEY_MENU);
        let empty = HashMap::new();
        let mut t9 = Keyboard::new(true, &empty, None);
        let seq: Vec<(KeyCode, i32, u64)> = [(K::KEY_MENU, 1, 0), (K::KEY_MENU, 0, 50), (K::KEY_BACK, 1, 100), (K::KEY_BACK, 0, 150), (K::KEY_1, 1, 200), (K::KEY_1, 2, 900), (K::KEY_1, 0, 950), (K::KEY_1, 1, 1000), (K::KEY_1, 0, 1050), (K::KEY_POWER, 1, 1100)]
            .iter()
            .map(|&(c, v, ms)| (remap(&keymap, c), v, ms))
            .collect();
        assert_eq!(sends(&mut t9, &seq), ["tab", "f2", "1+long", "1", "f4"]);
    }

    // PHONE_PAINT_PLAY's key names are the ones the keyboards send, a
    // character the key that types it: a 9-key pad's digits on a t9 phone,
    // a letter's key on a keyboard.
    #[test]
    fn a_played_key_is_the_key_that_sends_its_name() {
        for name in ["up", "down", "enter", "esc", "tab", "backspace", "f2", "f4", "call", "first", "last", "pageup", "delete"] {
            assert_eq!(play_code(name).and_then(named), Some(name));
        }
        for bad in ["power", "voldown", "ab", ""] {
            assert_eq!(play_code(bad), None, "{bad}");
        }
        let empty = HashMap::new();
        let mut t9 = Keyboard::new(true, &empty, None);
        let taps: Vec<(KeyCode, i32, u64)> = "5*#0".chars().flat_map(|c| [(play_code(&c.to_string()).unwrap(), 1, 0), (play_code(&c.to_string()).unwrap(), 0, 50)]).collect();
        assert_eq!(sends(&mut t9, &taps), ["5", "*", "#", "0"]);
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, None);
        assert_eq!(play_code("w"), Some(K::KEY_W));
        assert_eq!(sends(&mut kb, &[(K::KEY_W, 1, 0), (K::KEY_W, 0, 50)]), ["w"]);
    }

    // Both volume keys: the accessibility shortcut when one is let go (the
    // first had stepped the volume), or held for the long press; their
    // repeats meanwhile step nothing. One alone is as ever.
    #[test]
    fn both_volume_keys_are_the_accessibility_shortcut() {
        let empty = HashMap::new();
        let mut t9 = Keyboard::new(true, &empty, None);
        let got = sends(&mut t9, &[(K::KEY_VOLUMEDOWN, 1, 0), (K::KEY_VOLUMEUP, 1, 40), (K::KEY_VOLUMEUP, 2, 300), (K::KEY_VOLUMEDOWN, 0, 320), (K::KEY_VOLUMEUP, 0, 330), (K::KEY_VOLUMEUP, 1, 1000), (K::KEY_VOLUMEUP, 0, 1050)]);
        assert_eq!(got, ["voldown", "a11y", "volup"]);
        let got = sends(&mut t9, &[(K::KEY_VOLUMEUP, 1, 0), (K::KEY_VOLUMEDOWN, 1, 10), (K::KEY_VOLUMEDOWN, crate::keygate::TICK, 610), (K::KEY_VOLUMEDOWN, 2, 700), (K::KEY_VOLUMEDOWN, 0, 900), (K::KEY_VOLUMEUP, 0, 910)]);
        assert_eq!(got, ["volup", "a11y+long"]);
    }

    // The long press is the shell's: due at its time whether the key
    // repeats or not (TICK), once.
    #[test]
    fn the_long_press_takes_its_time() {
        let empty = HashMap::new();
        let mut t9 = Keyboard::new(true, &empty, None);
        t9.timing.long = Duration::from_millis(1500);
        let got = sends(&mut t9, &[(K::KEY_NUMERIC_5, 1, 0), (K::KEY_NUMERIC_5, 2, 700), (K::KEY_NUMERIC_5, crate::keygate::TICK, 1500), (K::KEY_NUMERIC_5, 2, 1600), (K::KEY_NUMERIC_5, 0, 1700)]);
        assert_eq!(got, ["5+long"]);
        let got = sends(&mut t9, &[(K::KEY_NUMERIC_5, 1, 0), (K::KEY_NUMERIC_5, 2, 700), (K::KEY_NUMERIC_5, 0, 800)]);
        assert_eq!(got, ["5"], "let go before 1.5 s: a press");
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, None);
        kb.timing.long = Duration::from_millis(400);
        let got = sends(&mut kb, &[(K::KEY_W, 1, 0), (K::KEY_W, crate::keygate::TICK, 400), (K::KEY_W, 0, 500), (K::KEY_F4, 1, 1000), (K::KEY_F4, crate::keygate::TICK, 1400)]);
        assert_eq!(got, ["w", "w+long+retype", "f4", "f4+long"]);
    }

    // Alt + a key is the key held (as Alt does in a terminal); with sticky
    // keys Alt, Ctrl and Shift need not be held: tapped, they are for the
    // next key, and Shift latches on any keyboard.
    #[test]
    fn alt_and_sticky_keys() {
        let empty = HashMap::new();
        let mut pc = Keyboard::new(false, &empty, None);
        let got = sends(&mut pc, &[(K::KEY_LEFTALT, 1, 0), (K::KEY_W, 1, 10), (K::KEY_W, 2, 300), (K::KEY_W, 0, 400), (K::KEY_BACKSPACE, 1, 500), (K::KEY_LEFTALT, 0, 600), (K::KEY_W, 1, 700)]);
        assert_eq!(got, ["w+long", "backspace+long", "w"]);
        // Without sticky keys a tap of Shift, Ctrl or Alt alone does nothing.
        let got = sends(&mut pc, &[(K::KEY_LEFTSHIFT, 1, 0), (K::KEY_LEFTSHIFT, 0, 50), (K::KEY_A, 1, 100), (K::KEY_LEFTCTRL, 1, 200), (K::KEY_LEFTCTRL, 0, 250), (K::KEY_C, 1, 300), (K::KEY_LEFTALT, 1, 400), (K::KEY_LEFTALT, 0, 450), (K::KEY_E, 1, 500)]);
        assert_eq!(got, ["a", "c", "e"]);
        pc.timing.sticky = true;
        let got = sends(&mut pc, &[(K::KEY_LEFTSHIFT, 1, 0), (K::KEY_LEFTSHIFT, 0, 50), (K::KEY_A, 1, 100), (K::KEY_A, 0, 120), (K::KEY_B, 1, 200)]);
        assert_eq!(got, ["shift-once", "shift-off", "A", "b"]);
        let got = sends(&mut pc, &[(K::KEY_LEFTCTRL, 1, 0), (K::KEY_LEFTCTRL, 0, 50), (K::KEY_C, 1, 100), (K::KEY_C, 0, 120), (K::KEY_C, 1, 200)]);
        assert_eq!(got, ["copy", "c"]);
        let got = sends(&mut pc, &[(K::KEY_LEFTALT, 1, 0), (K::KEY_LEFTALT, 0, 50), (K::KEY_E, 1, 100), (K::KEY_E, 0, 120), (K::KEY_E, 1, 200)]);
        assert_eq!(got, ["e+long", "e"]);
        // Shift tapped then an arrow: one step selected.
        let got = sends(&mut pc, &[(K::KEY_LEFTSHIFT, 1, 0), (K::KEY_LEFTSHIFT, 0, 50), (K::KEY_RIGHT, 1, 100), (K::KEY_RIGHT, 1, 200)]);
        assert_eq!(got, ["shift-once", "right+shift", "shift-off", "right"]);
        // Held with a key, a modifier is not latched by letting it go.
        let got = sends(&mut pc, &[(K::KEY_LEFTCTRL, 1, 0), (K::KEY_A, 1, 10), (K::KEY_A, 0, 20), (K::KEY_LEFTCTRL, 0, 50), (K::KEY_A, 1, 100)]);
        assert_eq!(got, ["selectall", "a"]);
    }

    #[test]
    fn held_backspace_deletes_words() {
        let layer = phone_layer();
        let mut kb = Keyboard::new(false, &layer, Some("sym"));
        let got = sends(&mut kb, &[(K::KEY_BACKSPACE, 1, 0), (K::KEY_BACKSPACE, 2, 300), (K::KEY_BACKSPACE, 2, 650), (K::KEY_BACKSPACE, 0, 700), (K::KEY_BACKSPACE, 1, 800)]);
        assert_eq!(got, ["backspace", "backspace", "backspace+long", "backspace"]);
        let mut t9 = Keyboard::new(true, &layer, None);
        let got = sends(&mut t9, &[(K::KEY_BACKSPACE, 1, 0), (K::KEY_BACKSPACE, 2, 650)]);
        assert_eq!(got, ["backspace", "backspace"]);
    }

    #[test]
    fn fn_types_the_second_legend() {
        let layer: HashMap<String, String> = [("w", "1"), (".", "0")].iter().map(|(k, v)| (k.to_string(), v.to_string())).collect();
        assert_eq!(fn_char(&layer, 'w'), Some('1'));
        assert_eq!(fn_char(&layer, 'W'), Some('1'));
        assert_eq!(fn_char(&layer, '.'), Some('0'));
        assert_eq!(fn_char(&layer, 'q'), None);
    }

    #[test]
    fn a_bigger_screen_shows_the_device_in_its_middle() {
        assert_eq!(letterbox((1920, 1080), (480, 640)), Some((720, 220)));
        assert_eq!(letterbox((480, 640), (480, 640)), None);
        assert_eq!(letterbox((640, 480), (480, 640)), None);
        assert_eq!(letterbox((320, 240), (480, 640)), None);
        assert_eq!(letterbox((1920, 1080), (0, 0)), None);
        // A whole phone's frame, its panel less the inset rows (sku.json):
        // the notch and the rounded corners stay dark.
        assert_eq!(letterbox((1080, 2340), (1080, 2180)), Some((0, 80)));
    }

    const LONG: Duration = Duration::from_millis(600);
    const DOWN: fn() -> Option<bool> = || Some(true);

    #[test]
    fn the_power_key_is_short_or_held() {
        assert_eq!(power_key(Duration::from_millis(120), LONG), serde_json::json!({"t": "key", "key": "power", "text": "", "ms": 120}));
        assert_eq!(power_key(LONG, LONG)["long"], serde_json::json!(true));
        assert_eq!(power_key(Duration::from_millis(550), Duration::from_millis(500))["long"], serde_json::json!(true), "the shell's power_long");
    }

    #[test]
    fn a_power_key_without_repeats_is_long_while_still_held() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        assert_eq!(k.due(t0, LONG), None);
        k.press(t0);
        assert_eq!(k.due(t0, LONG), Some(LONG));
        assert_eq!(k.due(t0 + Duration::from_millis(200), LONG), Some(LONG - Duration::from_millis(200)));
        assert_eq!(k.held(t0 + Duration::from_millis(599), LONG, DOWN), None);
        assert_eq!(k.held(t0 + LONG, LONG, DOWN).unwrap()["long"], serde_json::json!(true));
        assert_eq!(k.due(t0 + LONG, LONG), None);
        assert_eq!(k.held(t0 + Duration::from_millis(900), LONG, DOWN), None);
        assert_eq!(k.release(t0 + Duration::from_secs(2), LONG), None);
        assert_eq!(k.due(t0 + Duration::from_secs(2), LONG), None);
        // Where the kernel cannot say, the events decide.
        k.press(t0 + Duration::from_secs(3));
        assert_eq!(k.held(t0 + Duration::from_millis(3600), LONG, || None).unwrap()["long"], serde_json::json!(true));
    }

    #[test]
    fn a_short_power_press_goes_on_release() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        k.press(t0);
        let m = k.release(t0 + Duration::from_millis(150), LONG).unwrap();
        assert_eq!(m, serde_json::json!({"t": "key", "key": "power", "text": "", "ms": 150}));
        assert_eq!(k.release(t0 + Duration::from_millis(160), LONG), None);
        k.press(t0 + Duration::from_secs(1));
        assert_eq!(k.release(t0 + Duration::from_millis(1700), LONG).unwrap()["long"], serde_json::json!(true));
    }

    #[test]
    fn a_repeating_power_key_is_long_once() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        k.press(t0);
        assert_eq!(k.held(t0 + Duration::from_millis(250), LONG, DOWN), None);
        assert!(k.held(t0 + Duration::from_millis(650), LONG, DOWN).is_some());
        assert_eq!(k.held(t0 + Duration::from_millis(700), LONG, DOWN), None);
        assert_eq!(k.release(t0 + Duration::from_millis(800), LONG), None);
    }

    // The release never reached the renderer, but the kernel has the key
    // up when the long press comes due: it was a short press (the screen
    // goes off), not the power menu; the release arriving after all says
    // nothing more, and the next press starts over. Before, the timer sent
    // the long press for a tap whose release was lost.
    #[test]
    fn a_lost_release_is_a_short_press() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        k.press(t0);
        let m = k.held(t0 + LONG, LONG, || Some(false)).unwrap();
        assert_eq!(m, serde_json::json!({"t": "key", "key": "power", "text": ""}));
        assert_eq!(k.due(t0 + LONG, LONG), None, "not held any more");
        assert_eq!(k.release(t0 + Duration::from_millis(700), LONG), None);
        k.press(t0 + Duration::from_secs(1));
        assert_eq!(k.release(t0 + Duration::from_millis(1100), LONG).unwrap()["ms"], serde_json::json!(100));
    }

    // Pressed ten times 150 ms apart: ten short presses, no long one,
    // whatever the timer says between them.
    #[test]
    fn rapid_power_presses_are_each_short() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        let mut sent = Vec::new();
        for i in 0..10u64 {
            let at = t0 + Duration::from_millis(150 * i);
            k.press(at);
            sent.extend(k.held(at + Duration::from_millis(30), LONG, DOWN));
            sent.extend(k.release(at + Duration::from_millis(70), LONG));
        }
        assert_eq!(sent.len(), 10);
        assert!(sent.iter().all(|m| m["long"].is_null() && m["ms"] == serde_json::json!(70)), "{sent:?}");
    }

    // A flip whose event comes late is owed it: the late event pays for
    // that flip, and the next flip is still waited for. Before, the late
    // event was taken for the next flip's, so the next frame went into the
    // buffer still on the panel.
    #[test]
    fn a_late_flip_event_pays_for_its_own_flip() {
        let mut f = Flips::default();
        f.asked();
        f.gave_up();
        assert_eq!(f, Flips { pending: false, owed: 1 });
        f.asked();
        assert!(!f.event(), "the late event is the first flip's");
        assert!(f.busy(), "the second flip is still on its way");
        assert!(f.event(), "the second flip's own event");
        assert!(!f.busy());
        assert!(!f.event(), "an event for no flip changes nothing");
        assert_eq!(f, Flips::default());
    }

    #[test]
    fn rgba_becomes_xrgb() {
        let src = [10u8, 20, 30, 255, 1, 2, 3, 128];
        let mut dst = [0u8; 8];
        xrgb(&mut dst, &src);
        assert_eq!(dst, [30, 20, 10, 255, 3, 2, 1, 255]);
    }

    // Flip events that count vertical blanks pace the frames; a driver
    // whose events all say frame 0 (virtio-gpu) completes flips at once,
    // and frames are then paced by a timer.
    #[test]
    fn flips_count_blanks_or_not() {
        let feed = |seqs: &[u32]| {
            let (mut last, mut v) = (None, true);
            for &s in seqs {
                v = counts_blanks(&mut last, s, v);
            }
            v
        };
        assert!(feed(&[]), "taken to count them until told otherwise");
        assert!(feed(&[0]));
        assert!(feed(&[812, 813, 815]), "a real panel's vertical blanks");
        assert!(!feed(&[0, 0]), "virtio-gpu");
        assert!(!feed(&[0, 0, 0, 0]));
        assert!(feed(&[0, 0, 9]), "counting again");
    }

    #[test]
    fn the_poll_waits_out_its_time() {
        use std::io::Write;
        let (a, mut b) = std::os::unix::net::UnixStream::pair().unwrap();
        let t0 = Instant::now();
        assert!(!readable(a.as_raw_fd(), Duration::from_millis(30)));
        assert!(t0.elapsed() >= Duration::from_millis(30));
        b.write_all(b"x").unwrap();
        assert!(readable(a.as_raw_fd(), Duration::from_secs(1)));
    }

    // The rates as phone-displayd reads them (its TestPanelRates), and a
    // panel's mode of another rate found at its own size only.
    #[test]
    fn modes_by_rate() {
        assert_eq!(mode_rate(65000, 1344, 806, 0, false, false), 60);
        assert_eq!(mode_rate(297000, 2200, 1125, 0, false, false), 120);
        assert_eq!(mode_rate(74250, 2200, 1125, 0, true, false), 60);
        assert_eq!(mode_rate(12588, 800, 263, 0, false, true), 30);
        assert_eq!(mode_rate(328000, 1180, 2316, 0, false, false), 120);
        assert_eq!(mode_rate(1, 0, 0, 0, false, false), 0);
        let panel = (1080, 2340);
        let modes = [(120, panel, false), (60, (720, 1560), false), (60, panel, true), (60, panel, false), (90, panel, false)];
        assert_eq!(pick_rate(&modes, panel, 60), Some(3), "not the interlaced one, nor another size");
        assert_eq!(pick_rate(&modes, panel, 120), Some(0));
        assert_eq!(pick_rate(&modes, panel, 90), Some(4));
        assert_eq!(pick_rate(&modes, panel, 144), None);
        assert_eq!(pick_rate(&modes, (720, 1560), 120), None);
    }

    #[test]
    fn damage_is_the_changed_box() {
        let (w, h) = (8, 6);
        let a = vec![0u8; w * h * 4];
        assert_eq!(damage(&[], &a, w, h), Some(Rect { x0: 0, y0: 0, x1: w, y1: h }));
        assert_eq!(damage(&a, &a, w, h), None);
        let mut b = a.clone();
        b[(1 * w + 2) * 4] = 9;
        b[(4 * w + 6) * 4 + 3] = 9;
        assert_eq!(damage(&a, &b, w, h), Some(Rect { x0: 2, y0: 1, x1: 7, y1: 5 }));
    }

    /// Shows frames through two flipping buffers as Display::show does:
    /// for each, what is copied and whether it is flipped to (None: the
    /// screen has it already).
    fn shown(held: &mut Held, frames: &[(&[u8], Option<&[Rect]>)], w: usize) -> Vec<Option<(Vec<Rect>, bool)>> {
        let mut out = Vec::new();
        for &(f, changed) in frames {
            let h = f.len() / 4 / w;
            if held.unchanged(f, w, h, changed) {
                out.push(None);
                continue;
            }
            let target = 1 - held.front;
            let (rects, flip) = held.put(target, f, w, (w, h));
            if flip {
                held.front = target;
            }
            out.push(Some((rects, flip)));
        }
        out
    }

    // A frame back as it was two frames ago (a toast come and gone with
    // the animations off) is in the buffer not on screen already: it must
    // still be flipped to. The screen went on showing the toast before.
    #[test]
    fn a_frame_as_it_was_two_frames_ago_is_flipped_to() {
        let (w, h) = (4, 3);
        let a = vec![0u8; w * h * 4];
        let mut b = a.clone();
        b[(w + 1) * 4] = 9;
        let mut held = Held::default();
        let got = shown(&mut held, &[(&a, None), (&b, None), (&a, None), (&a, None)], w);
        let all = Rect { x0: 0, y0: 0, x1: w, y1: h };
        assert_eq!(got[0], Some((vec![all], true)));
        assert_eq!(got[1], Some((vec![all], true)), "the other buffer was empty");
        assert_eq!(got[2], Some((vec![], true)), "nothing to copy, but flipped to");
        assert_eq!(got[3], None, "on screen now");
        assert_eq!(held.front, 1);
    }

    // With the live display's damage, only that is copied, into each
    // buffer what changed since it was last written; without, a comparison.
    #[test]
    fn the_damage_is_what_each_buffer_gets() {
        let w = 8;
        let frame = |marks: &[(usize, usize)]| {
            let mut f = vec![0u8; w * 6 * 4];
            for &(x, y) in marks {
                f[(y * w + x) * 4] = 200;
            }
            f
        };
        let r = |x0, y0, x1, y1| Rect { x0, y0, x1, y1 };
        let (a, b, c) = (frame(&[]), frame(&[(1, 1)]), frame(&[(1, 1), (6, 4)]));
        let (r11, r64) = (r(1, 1, 2, 2), r(6, 4, 7, 5));
        let mut held = Held::default();
        let got = shown(&mut held, &[(&a, None), (&b, Some(&[r11])), (&c, Some(&[r64])), (&c, Some(&[])), (&b, Some(&[r64]))], w);
        assert_eq!(got[0], Some((vec![r(0, 0, 8, 6)], true)));
        assert_eq!(got[1], Some((vec![r(0, 0, 8, 6)], true)), "the other buffer's first frame: all of it");
        assert_eq!(got[2], Some((vec![r11, r64], true)), "the buffer that had a: the damage of b and of c");
        assert_eq!(got[3], None, "no damage: on screen already");
        assert_eq!(got[4], Some((vec![r64, r64], true)), "the buffer that had b: the damage of c and back");
        assert_eq!((&held.frames[0], &held.frames[1]), (&b, &c));
        // Damage not known (the pointer, a transition step): compared.
        let got = shown(&mut held, &[(&a, None)], w);
        assert_eq!(got[0], Some((vec![r(1, 1, 7, 5)], true)), "the buffer that had c, compared with a");
    }

    // A connector the client before turned off has no encoder: its CRTC is
    // the first of the device's that one of its encoders can drive, not the
    // device's first (another connector's), which set_crtc refuses.
    #[test]
    fn the_crtc_is_one_the_connector_can_use() {
        let c = |n: u32| drm::control::from_u32::<crtc::Handle>(n).unwrap();
        let all = [c(40), c(41), c(42)];
        assert_eq!(crtc_for(None, &[vec![c(41), c(42)]], &all), Some(c(41)));
        assert_eq!(crtc_for(None, &[vec![c(42)], vec![c(41)]], &all), Some(c(41)), "the device's order");
        assert_eq!(crtc_for(Some(c(42)), &[vec![c(41), c(42)]], &all), Some(c(42)), "the one it drives now");
        assert_eq!(crtc_for(None, &[], &all), None);
    }

    // A refused flip is set at once and the next frame flips again; refused
    // again in a row, flips stop; one that goes through starts the count
    // again.
    #[test]
    fn a_refused_flip_is_tried_once_more() {
        let mut r = Refused::default();
        assert!(r.refused(), "the first: flip again");
        assert!(!r.refused(), "the second in a row: stop");
        let mut r = Refused::default();
        assert!(r.refused());
        r.flipped();
        assert!(r.refused(), "a flip went through in between");
    }

    fn facts() -> FlipFacts {
        let fb = |id| FbFacts { id, size: (1080, 2340), format: "XR24".into(), modifier: Some(0), pitch: 4352 };
        FlipFacts {
            error: "Invalid argument (os error 22)".into(),
            errno: libc::EINVAL,
            driver: "msm (20130625)".into(),
            crtc: 92,
            mode: (1080, 2340, 60),
            crtc_mode: Some((1080, 2340, 60)),
            crtc_fb: 120,
            active: Some(true),
            connector: "DSI-1 Connected".into(),
            dpms: Some("On".into()),
            on: Some(fb(120)),
            new: Some(fb(121)),
            ours: vec![120, 121],
            planes: vec![PlaneFacts { id: 40, kind: "Primary".into(), crtc: 92, fb: 120, rotation: Some(1), xr24: true }],
            asked: 0,
            ..FlipFacts::default()
        }
    }

    // The report of a refused flip says what the kernel shows and which
    // rule of a legacy flip it breaks; with none broken, that the driver's
    // own check refused it and how to make the kernel say which.
    #[test]
    fn a_refused_flip_says_why() {
        let lines = describe(&facts());
        let all = lines.join("\n");
        for want in ["Invalid argument", "CRTC 92: mode 1080x2340@60", "framebuffer to flip to: 121 1080x2340 XR24 modifier 0x0 pitch 4352", "plane 40 Primary", "DPMS On"] {
            assert!(all.contains(want), "{want:?} not in\n{all}");
        }
        let likely: Vec<&String> = lines.iter().filter(|l| l.starts_with("likely: ")).collect();
        assert_eq!(likely.len(), 1, "{all}");
        assert!(likely[0].contains("drm.debug=0x14"), "{all}");
        let why = |f: FlipFacts| describe(&f).into_iter().filter(|l| l.starts_with("likely: ")).collect::<Vec<_>>().join("\n");
        let mut f = facts();
        f.planes[0].rotation = Some(0x2);
        assert!(why(f).contains("turned a quarter"));
        let mut f = facts();
        f.active = Some(false);
        assert!(why(f).contains("the CRTC is off"));
        let mut f = facts();
        f.new.as_mut().unwrap().size = (1080, 2400);
        assert!(why(f).contains("the framebuffer is 1080x2400, the mode 1080x2340"));
        let mut f = facts();
        f.new.as_mut().unwrap().format = "AR24".into();
        assert!(why(f).contains("cannot change the format"));
        let mut f = facts();
        f.crtc_fb = 77;
        assert!(why(f).contains("framebuffer 77, none of phone-paint's"));
        let mut f = facts();
        (f.errno, f.pending) = (libc::EBUSY, true);
        assert!(why(f).contains("still pending"));
        let mut f = facts();
        f.planes.push(PlaneFacts { id: 46, kind: "Cursor".into(), crtc: 92, fb: 130, rotation: None, xr24: false });
        assert!(describe(&f).iter().any(|l| l.starts_with("also: plane 46 (Cursor)")));
    }

    // A GPU buffer laid out linearly or by the driver's own choice is a
    // plain framebuffer; a tiled or compressed one needs its modifier.
    #[test]
    fn a_tiled_buffer_needs_its_modifier() {
        assert!(plain_layout(0) && plain_layout(crate::gbm::MOD_INVALID));
        // Qualcomm's UBWC, Arm's AFBC (16x16, sparse).
        assert!(!plain_layout(0x0500_0000_0000_0001) && !plain_layout(0x0800_0000_0000_0041));
    }

    // A 640x480 frame on a 480x640 panel: the frame's top left corner is the
    // panel's top right, its bottom left the panel's top left.
    #[test]
    fn turning_a_quarter_clockwise() {
        let h = 480;
        assert_eq!(turned(0, 0, h), (479, 0));
        assert_eq!(turned(0, 479, h), (0, 0));
        assert_eq!(turned(639, 479, h), (0, 639));
        let r = turned_rect(&Rect { x0: 10, y0: 20, x1: 30, y1: 40 }, h);
        assert_eq!(r, Rect { x0: 440, y0: 10, x1: 460, y1: 30 });
        for (x, y) in [(10, 20), (29, 39)] {
            let (px, py) = turned(x, y, h);
            assert!(px >= r.x0 && px < r.x1 && py >= r.y0 && py < r.y1);
        }
    }
}
