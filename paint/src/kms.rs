//! The device mode: frames go into a DRM/KMS dumb buffer on the panel, and
//! keys come straight from evdev (no libinput, no xkb: a phone keypad and a
//! US layout for USB keyboards are all a phone needs).

use std::collections::HashMap;
use std::fs::OpenOptions;
use std::os::fd::{AsFd, AsRawFd, BorrowedFd};
use std::sync::mpsc::Sender;
use std::time::{Duration, Instant};

use drm::buffer::{Buffer, DrmFourcc};
use drm::control::{connector, crtc, dumbbuffer::DumbBuffer, framebuffer, ClipRect, Device as ControlDevice, Mode};
use evdev::{EventSummary, KeyCode, RelativeAxisCode};

use crate::input::Event;

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
    db: DumbBuffer,
    fb: framebuffer::Handle,
    pub width: u32,
    pub height: u32,
    /// The panel is the device turned sideways (a portrait panel in a
    /// landscape phone): frames are drawn landscape and turned a quarter
    /// clockwise onto it.
    pub rotated: bool,
    /// Where the frame's top left corner sits on the panel: the middle of a
    /// screen bigger than the device's (`letterbox`), (0, 0) otherwise.
    pub origin: (usize, usize),
    /// The frame now on the panel, to find what the next one changes.
    shown: Vec<u8>,
    crtc: crtc::Handle,
    con: connector::Handle,
    mode: Mode,
    /// The panel is lit (the shell turns it off to save power).
    pub on: bool,
}

fn s(e: impl std::fmt::Display) -> String {
    e.to_string()
}

impl Display {
    pub fn open() -> Result<Display, String> {
        let mut last = "no /dev/dri/card*".to_string();
        for path in ["/dev/dri/card0", "/dev/dri/card1"] {
            let Ok(f) = OpenOptions::new().read(true).write(true).open(path) else { continue };
            match Self::setup(Card(f)) {
                Ok(d) => return Ok(d),
                Err(e) => last = format!("{path}: {e}"),
            }
        }
        Err(last)
    }

    fn setup(card: Card) -> Result<Display, String> {
        let res = card.resource_handles().map_err(s)?;
        let con = res
            .connectors()
            .iter()
            .filter_map(|&h| card.get_connector(h, false).ok())
            .find(|c| c.state() == connector::State::Connected)
            .ok_or("no connected display")?;
        let mode = *con.modes().first().ok_or("the display has no mode")?;
        let (w, h) = mode.size();
        let crtc = con
            .current_encoder()
            .and_then(|e| card.get_encoder(e).ok())
            .and_then(|e| e.crtc())
            .or_else(|| res.crtcs().first().copied())
            .ok_or("no CRTC")?;
        let db = card.create_dumb_buffer((w as u32, h as u32), DrmFourcc::Xrgb8888, 32).map_err(s)?;
        let fb = card.add_framebuffer(&db, 24, 32).map_err(s)?;
        card.set_crtc(crtc, Some(fb), (0, 0), &[con.handle()], Some(mode)).map_err(s)?;
        Ok(Display {
            card,
            db,
            fb,
            width: w as u32,
            height: h as u32,
            rotated: false,
            origin: (0, 0),
            shown: Vec::new(),
            crtc,
            con: con.handle(),
            mode,
            on: true,
        })
    }

    /// Turns the panel off (the CRTC disabled: the display controller stops
    /// scanning out and the panel driver powers the panel and its backlight
    /// down) or on again with the frame it last showed.
    pub fn power(&mut self, on: bool) -> Result<(), String> {
        if on == self.on {
            return Ok(());
        }
        if on {
            self.card.set_crtc(self.crtc, Some(self.fb), (0, 0), &[self.con], Some(self.mode)).map_err(s)?;
        } else {
            self.card.set_crtc(self.crtc, None, (0, 0), &[], None).map_err(s)?;
        }
        self.on = on;
        Ok(())
    }

    /// Copies what a drawn frame (premultiplied RGBA, opaque) changed to the
    /// panel (XRGB8888); an unchanged frame costs a comparison and nothing else.
    pub fn show(&mut self, pm: &tiny_skia::Pixmap) {
        let pitch = self.db.pitch() as usize;
        let (src, sw, sh) = (pm.data(), pm.width() as usize, pm.height() as usize);
        let (ox, oy) = self.origin;
        let (w, h) = if self.rotated {
            (sw, sh)
        } else {
            ((self.width as usize - ox).min(sw), (self.height as usize - oy).min(sh))
        };
        let Some(r) = damage(&self.shown, src, sw, h) else { return };
        let (x1, y1) = (r.x1.min(w), r.y1.min(h));
        {
            let Ok(mut map) = self.card.map_dumb_buffer(&mut self.db) else { return };
            let dst = map.as_mut();
            for y in r.y0..y1 {
                for x in r.x0..x1 {
                    let (px, py) = if self.rotated { turned(x, y, sh) } else { (x + ox, y + oy) };
                    let (si, di) = ((y * sw + x) * 4, py * pitch + px * 4);
                    dst[di] = src[si + 2];
                    dst[di + 1] = src[si + 1];
                    dst[di + 2] = src[si];
                    dst[di + 3] = 0xff;
                }
            }
        }
        self.shown.clear();
        self.shown.extend_from_slice(src);
        // Drivers that scan out on demand (virtio-gpu, USB displays) only
        // show what they are told changed; others reject the call, harmlessly.
        let c = if self.rotated {
            turned_rect(&Rect { x0: r.x0, y0: r.y0, x1, y1 }, sh)
        } else {
            Rect { x0: r.x0 + ox, y0: r.y0 + oy, x1: x1 + ox, y1: y1 + oy }
        };
        let clip = ClipRect::new(c.x0 as u16, c.y0 as u16, c.x1 as u16, c.y1 as u16);
        let _ = self.card.dirty_framebuffer(self.fb, &[clip]);
    }
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

/// The part of the screen a frame changed: columns x0..x1, rows y0..y1.
#[derive(Debug, PartialEq)]
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

/// How long a 9-key key must be held to count as a long press (as phone-gui).
const LONG_PRESS: Duration = Duration::from_millis(600);

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
        _ => return None,
    })
}

/// The power key's message when it is let go after being held for held: a
/// long press from LONG_PRESS on. It goes on release, not on the press:
/// the shell turns the screen off on a press, which a hold must not do.
fn power_key(held: Duration) -> serde_json::Value {
    if held >= LONG_PRESS {
        serde_json::json!({"t": "key", "key": "power", "text": "", "long": true})
    } else {
        serde_json::json!({"t": "key", "key": "power", "text": ""})
    }
}

/// The power key while it is down. Most boards' power key (gpio-keys)
/// does not autorepeat, so the long press is due from the renderer's own
/// timer (`due`, the key loop's poll timeout) as much as from a repeat,
/// whichever comes first; a short press still goes on release.
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

    /// A repeat, or the timer running out: the long press, once.
    fn held(&mut self, now: Instant) -> Option<serde_json::Value> {
        let since = self.since?;
        if self.sent || now.duration_since(since) < LONG_PRESS {
            return None;
        }
        self.sent = true;
        Some(power_key(now.duration_since(since)))
    }

    fn release(&mut self, now: Instant) -> Option<serde_json::Value> {
        let since = self.since.take()?;
        (!self.sent).then(|| power_key(now.duration_since(since)))
    }

    /// How long until the long press is due, while the key is down and it
    /// has not gone.
    fn due(&self, now: Instant) -> Option<Duration> {
        let since = self.since.filter(|_| !self.sent)?;
        Some(LONG_PRESS.saturating_sub(now.duration_since(since)))
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

/// Reads the keyboards and mice the input opener delivers, as they come
/// (input.rs), and reports their keys to the shell through send, and each
/// device arriving and leaving.
pub fn read_inputs(sock: std::os::fd::OwnedFd, nine_key: bool, layer: HashMap<String, String>, send: fn(&serde_json::Value), events: Sender<Event>) {
    std::thread::spawn(move || {
        while let Some((dev, kind)) = crate::input::recv(&sock) {
            let name = dev.name().unwrap_or("").to_string();
            send(&serde_json::json!({"t": "input", "name": name, "kind": kind.name(), "added": true}));
            let layer = layer.clone();
            let events = events.clone();
            std::thread::spawn(move || {
                key_loop(dev, nine_key, &layer, send, &events);
                send(&serde_json::json!({"t": "input", "name": name, "kind": kind.name(), "added": false}));
            });
        }
    });
}

/// The character a key types with Fn held: its second legend, if it has one.
fn fn_char(layer: &HashMap<String, String>, ch: char) -> Option<char> {
    layer.get(&ch.to_ascii_lowercase().to_string()).and_then(|s| s.chars().next())
}

/// The renderer sees every screen, payment codes included, and decodes
/// images from app packages, so after opening the panel and the keyboards it
/// gives up everything else: no network (an empty network namespace), no
/// root (user nobody, no groups), no way back (no_new_privs), and with
/// Landlock no files but fonts and icons. Must run while the process is
/// single-threaded. Returns the sandbox features in force (none when not
/// started as root).
pub fn drop_privileges() -> Result<Vec<&'static str>, String> {
    const NOBODY: libc::uid_t = 65534;
    let err = |what: &str| format!("{what}: {}", std::io::Error::last_os_error());
    // SAFETY: plain system calls on the calling (only) thread.
    unsafe {
        if libc::geteuid() != 0 {
            return Ok(Vec::new());
        }
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
    }
    let mut got = vec!["sandboxed"];
    if restrict_files()? {
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

/// Read-only access to the fonts and system icons (/usr) and to installed
/// apps' icons; nothing else, and no writes. The display and keyboards are
/// already open. Returns false on kernels without Landlock.
fn restrict_files() -> Result<bool, String> {
    use landlock::{path_beneath_rules, Access, AccessFs, Ruleset, RulesetAttr, RulesetCreatedAttr, RulesetStatus, ABI};
    let abi = ABI::V3;
    let mut read = vec!["/usr".to_string(), "/var/lib/phone/packages".to_string()];
    if let Ok(extra) = std::env::var("PHONE_RENDERER_READ") {
        read.extend(extra.split(':').filter(|p| !p.is_empty()).map(String::from));
    }
    let status = Ruleset::default()
        .handle_access(AccessFs::from_all(abi))
        .map_err(s)?
        .create()
        .map_err(s)?
        .add_rules(path_beneath_rules(&read, AccessFs::from_read(abi)))
        .map_err(s)?
        .restrict_self()
        .map_err(s)?;
    Ok(status.ruleset != RulesetStatus::NotEnforced)
}

fn key_loop(mut dev: evdev::Device, nine_key: bool, layer: &HashMap<String, String>, send: fn(&serde_json::Value), pointer: &Sender<Event>) {
    let (mut shift, mut ctrl, mut fn_held) = (false, false, false);
    // The end key since it went down, and whether its long press went.
    let mut end_held: Option<(Instant, bool)> = None;
    // A 9-key digit, * or # being held: sent on release, or as a long press.
    let mut held: Option<(char, Instant, bool)> = None;
    let mut power = PowerKey::default();
    loop {
        if let Some(due) = power.due(Instant::now()) {
            if !readable(dev.as_raw_fd(), due) {
                if let Some(m) = power.held(Instant::now()) {
                    send(&m);
                }
                continue;
            }
        }
        let Ok(events) = dev.fetch_events() else { return };
        // One move per batch of events (a report), however many axes it has.
        let (mut dx, mut dy) = (0, 0);
        for ev in events {
            let (code, value) = match ev.destructure() {
                EventSummary::Key(_, code, value) => (code, value),
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
                            send(&serde_json::json!({"t": "key", "key": key, "text": ""}));
                        }
                    }
                    continue;
                }
                _ => continue,
            };
            if code == KeyCode::BTN_LEFT {
                if value == 1 {
                    let _ = pointer.send(Event::Click);
                }
                continue;
            }
            if let Some(key) = crate::input::button(code) {
                if value == 1 {
                    send(&serde_json::json!({"t": "key", "key": key, "text": ""}));
                }
                continue;
            }
            if value == 1 {
                let _ = pointer.send(Event::Typed);
            }
            match code {
                KeyCode::KEY_LEFTSHIFT | KeyCode::KEY_RIGHTSHIFT => shift = value != 0,
                KeyCode::KEY_LEFTCTRL | KeyCode::KEY_RIGHTCTRL => ctrl = value != 0,
                KeyCode::KEY_FN => fn_held = value != 0,
                KeyCode::KEY_POWER => {
                    let now = Instant::now();
                    let m = match value {
                        1 => {
                            power.press(now);
                            None
                        }
                        2 => power.held(now),
                        _ => power.release(now),
                    };
                    if let Some(m) = m {
                        send(&m);
                    }
                }
                _ if ctrl && code == KeyCode::KEY_SPACE => {
                    if value == 1 {
                        send(&serde_json::json!({"t": "key", "key": "ime", "text": ""}));
                    }
                }
                _ => {
                    if let Some(name) = named(code) {
                        // The end key is the power key too where the board has
                        // none: held, it says so once (as the 9-key digits do,
                        // from the key's own repeats); its press goes at once.
                        if name == "f4" {
                            match value {
                                1 => {
                                    end_held = Some((Instant::now(), false));
                                    send(&serde_json::json!({"t": "key", "key": name, "text": ""}));
                                }
                                2 => {
                                    if let Some((since, sent)) = end_held.as_mut() {
                                        if !*sent && since.elapsed() >= LONG_PRESS {
                                            send(&serde_json::json!({"t": "key", "key": name, "text": "", "long": true}));
                                            *sent = true;
                                        }
                                    }
                                }
                                _ => end_held = None,
                            }
                            continue;
                        }
                        if value != 0 {
                            send(&serde_json::json!({"t": "key", "key": name, "text": ""}));
                        }
                        continue;
                    }
                    let Some(ch) = text(code, shift) else { continue };
                    let ch = if fn_held { fn_char(layer, ch).unwrap_or(ch) } else { ch };
                    if nine_key && (ch.is_ascii_digit() || ch == '*' || ch == '#') {
                        match value {
                            1 => held = Some((ch, Instant::now(), false)),
                            2 => {
                                if let Some((c, since, sent)) = held.as_mut() {
                                    if !*sent && since.elapsed() >= LONG_PRESS {
                                        send(&serde_json::json!({"t": "key", "key": "", "text": c.to_string(), "long": true}));
                                        *sent = true;
                                    }
                                }
                            }
                            _ => {
                                if let Some((c, _, sent)) = held.take() {
                                    if !sent {
                                        send(&serde_json::json!({"t": "key", "key": "", "text": c.to_string()}));
                                    }
                                }
                            }
                        }
                    } else if value != 0 {
                        send(&serde_json::json!({"t": "key", "key": "", "text": ch.to_string()}));
                    }
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
    }

    #[test]
    fn the_power_key_is_short_or_held() {
        assert_eq!(power_key(Duration::from_millis(120)), serde_json::json!({"t": "key", "key": "power", "text": ""}));
        assert_eq!(power_key(LONG_PRESS)["long"], serde_json::json!(true));
    }

    #[test]
    fn a_power_key_without_repeats_is_long_while_still_held() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        assert_eq!(k.due(t0), None);
        k.press(t0);
        assert_eq!(k.due(t0), Some(LONG_PRESS));
        assert_eq!(k.due(t0 + Duration::from_millis(200)), Some(LONG_PRESS - Duration::from_millis(200)));
        assert_eq!(k.held(t0 + Duration::from_millis(599)), None);
        assert_eq!(k.held(t0 + LONG_PRESS).unwrap()["long"], serde_json::json!(true));
        assert_eq!(k.due(t0 + LONG_PRESS), None);
        assert_eq!(k.held(t0 + Duration::from_millis(900)), None);
        assert_eq!(k.release(t0 + Duration::from_secs(2)), None);
        assert_eq!(k.due(t0 + Duration::from_secs(2)), None);
    }

    #[test]
    fn a_short_power_press_goes_on_release() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        k.press(t0);
        let m = k.release(t0 + Duration::from_millis(150)).unwrap();
        assert_eq!(m, serde_json::json!({"t": "key", "key": "power", "text": ""}));
        assert_eq!(k.release(t0 + Duration::from_millis(160)), None);
        k.press(t0 + Duration::from_secs(1));
        assert_eq!(k.release(t0 + Duration::from_millis(1700)).unwrap()["long"], serde_json::json!(true));
    }

    #[test]
    fn a_repeating_power_key_is_long_once() {
        let t0 = Instant::now();
        let mut k = PowerKey::default();
        k.press(t0);
        assert_eq!(k.held(t0 + Duration::from_millis(250)), None);
        assert!(k.held(t0 + Duration::from_millis(650)).is_some());
        assert_eq!(k.held(t0 + Duration::from_millis(700)), None);
        assert_eq!(k.release(t0 + Duration::from_millis(800)), None);
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
