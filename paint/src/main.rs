// phone-paint is our own renderer for the renderer slot (phone.frame/1,
// 23-renderer-protocol.md), written without Slint: tiny-skia for shapes and
// images, ab_glyph for text. Headless (--headless) it draws on request and
// writes PNG snapshots; otherwise it drives the panel through DRM/KMS and
// reads the keyboards through evdev.

mod canvas;
mod design;
mod frame;
mod i18n;
mod input;
mod kms;
mod paint;
mod seccomp;
mod skin;
mod surfaces;

use std::io::{BufRead, Write};
use std::sync::mpsc;
use std::time::{Duration, Instant};

use serde::Deserialize;

use input::Event;

const PROTO: &str = "phone.frame/1";

/// How often a moving pointer is redrawn at most.
const POINTER_EVERY: Duration = Duration::from_millis(16);

#[derive(Deserialize)]
struct Msg {
    t: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    proto: String,
    #[serde(default)]
    device: Option<frame::Device>,
    #[serde(default)]
    echo: bool,
    /// The side channel's file descriptor (hello): media surfaces.
    #[serde(default)]
    side_fd: Option<i32>,
    /// power: the panel on or off.
    #[serde(default)]
    on: Option<bool>,
}

fn proto_major(p: &str) -> Option<u32> {
    p.strip_prefix("phone.frame/")?.split('.').next()?.parse().ok()
}

fn send(v: &serde_json::Value) {
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{v}");
    let _ = out.flush();
}

/// The frame's scale: Settings › Large text draws everything bigger on the same panel.
fn zoomed(scale: f32, f: &frame::Frame) -> f32 {
    if f.zoom > 0.0 { scale * f.zoom } else { scale }
}

/// The mouse pointer, in panel pixels: shown from the first move until a
/// key is typed. While shown, the frame under it is kept so that moving it
/// redraws only the arrow.
struct Pointer {
    x: f32,
    y: f32,
    shown: bool,
    under: Option<tiny_skia::Pixmap>,
    drawn: Instant,
    due: bool,
}

/// Puts a drawn frame on the panel, with the pointer over it when shown.
fn present(d: &mut kms::Display, mut cv: canvas::Canvas, p: &mut Pointer) {
    p.under = None;
    if p.shown {
        p.under = Some(cv.pm.clone());
        let s = cv.s;
        paint::pointer(&mut cv, p.x / s, p.y / s);
        (p.drawn, p.due) = (Instant::now(), false);
    }
    d.show(&cv.pm);
}

/// Moves the arrow over the frame kept under it.
fn repoint(d: &mut kms::Display, p: &mut Pointer, scale: f32, fonts: &'static canvas::Fonts) {
    let Some(under) = &p.under else { return };
    let mut cv = canvas::Canvas::over(under.clone(), scale, fonts);
    if p.shown {
        paint::pointer(&mut cv, p.x / scale, p.y / scale);
    }
    d.show(&cv.pm);
    (p.drawn, p.due) = (Instant::now(), false);
}

fn main() {
    let args: Vec<String> = std::env::args().collect();
    let snap_dir = args.iter().position(|a| a == "--snap-dir").and_then(|i| args.get(i + 1).cloned()).unwrap_or_else(|| ".".into());
    let headless = args.iter().any(|a| a == "--headless");
    // --skin: every snapshot also as <name>.skin.png inside a phone body.
    let skin = args.iter().any(|a| a == "--skin");
    let mut first = String::new();
    let hello: Option<Msg> = std::io::stdin().lock().read_line(&mut first).ok().and_then(|_| serde_json::from_str(&first).ok());
    let shell_proto = hello.as_ref().map(|m| m.proto.clone()).unwrap_or_default();
    if proto_major(&shell_proto) != proto_major(PROTO) {
        eprintln!("phone-paint: the shell speaks {shell_proto:?}, this renderer {PROTO}");
        std::process::exit(3);
    }
    let side_fd = hello.as_ref().and_then(|m| m.side_fd);
    let dev = hello.and_then(|m| m.device).unwrap_or_default();
    let (mut width, mut height, scale) = if dev.width > 0 { (dev.width, dev.height, dev.scale) } else { (480, 640, 2.0) };
    // On the device the panel's mode decides the size, not the profile.
    let mut display = None;
    if !headless {
        match kms::Display::open() {
            Ok(mut d) => {
                // A portrait panel in a landscape phone (24-sku-framework.md):
                // drawn at the device's size, turned onto the panel.
                if dev.width > 0 && d.sideways(dev.width, dev.height) {
                    d.rotated = true;
                } else if let Some(o) = kms::letterbox((d.width, d.height), (dev.width, dev.height)) {
                    // A monitor bigger than the device's screen: the device's
                    // screen in its middle (24-sku-framework.md §4).
                    d.origin = o;
                } else {
                    (width, height) = (d.width, d.height);
                }
                display = Some(d);
            }
            Err(e) => {
                eprintln!("phone-paint: display: {e}");
                std::process::exit(5);
            }
        }
    }
    let fonts: &'static canvas::Fonts = match canvas::Fonts::load() {
        Ok(f) => Box::leak(Box::new(f)),
        Err(e) => {
            eprintln!("phone-paint: {e}");
            std::process::exit(4);
        }
    };
    let mut features = if headless { vec!["snap", "images", "patch", "layout", "power"] } else { vec!["kms", "keys", "images", "patch", "anim", "layout", "power", "mouse", "pointer"] };
    // Before any thread: the input opener is forked from this process.
    let mut inputs = None;
    if display.is_some() {
        match input::start() {
            Ok(sock) => inputs = Some(sock),
            Err(e) => eprintln!("phone-paint: input: {e}"),
        }
    }
    if let Some(fd) = side_fd {
        surfaces::listen(fd);
        features.push("shm");
    }
    if display.is_some() {
        match kms::drop_privileges() {
            Ok(got) => features.extend(got),
            Err(e) => {
                eprintln!("phone-paint: sandbox: {e}");
                std::process::exit(6);
            }
        }
    }
    send(&serde_json::json!({
        "t": "ready",
        "proto": PROTO,
        "renderer": {
            "name": env!("CARGO_PKG_NAME"),
            "version": env!("CARGO_PKG_VERSION"),
            "license": env!("CARGO_PKG_LICENSE"),
            "toolkit": "tiny-skia + ab_glyph",
        },
        "features": features,
        "display": {"width": width, "height": height, "scale": scale},
    }));
    let mut current = frame::Frame::default();
    let mut doc = serde_json::Value::Null;
    let mut state = paint::State::default();
    let (events, rx) = mpsc::channel();
    if let Some(sock) = inputs {
        kms::read_inputs(sock, dev.keypad == "t9", dev.fn_layer.clone(), send, events.clone());
    }
    std::thread::spawn(move || {
        for line in std::io::stdin().lines() {
            let Ok(line) = line else { break };
            if events.send(Event::Line(line)).is_err() {
                return;
            }
        }
        let _ = events.send(Event::Eof);
    });
    let mut ptr = Pointer { x: width as f32 / 2.0, y: height as f32 / 2.0, shown: false, under: None, drawn: Instant::now(), due: false };
    // Off, frames only update the document; the one current when the panel
    // comes back is drawn then.
    let mut lit = true;
    let mut stale = false;
    loop {
        let ev = if ptr.due {
            match rx.recv_timeout(POINTER_EVERY.saturating_sub(ptr.drawn.elapsed())) {
                Ok(ev) => ev,
                Err(mpsc::RecvTimeoutError::Timeout) => {
                    if let Some(d) = display.as_mut() {
                        repoint(d, &mut ptr, zoomed(scale, &current), fonts);
                    }
                    continue;
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
            }
        } else {
            match rx.recv() {
                Ok(ev) => ev,
                Err(_) => break,
            }
        };
        let line = match ev {
            Event::Line(line) => line,
            Event::Eof => break,
            Event::Move(dx, dy) => {
                let Some(d) = display.as_mut().filter(|_| lit) else { continue };
                ptr.x = (ptr.x + dx as f32).clamp(0.0, width as f32 - 1.0);
                ptr.y = (ptr.y + dy as f32).clamp(0.0, height as f32 - 1.0);
                if !ptr.shown {
                    ptr.shown = true;
                    state.now = None;
                    let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                    cv.mask_secure = false;
                    paint::draw(&mut cv, &current, &mut state);
                    present(d, cv, &mut ptr);
                } else if ptr.drawn.elapsed() >= POINTER_EVERY {
                    repoint(d, &mut ptr, zoomed(scale, &current), fonts);
                } else {
                    ptr.due = true;
                }
                continue;
            }
            Event::Click => {
                let s = zoomed(scale, &current);
                let hit = if ptr.shown && lit { paint::click(&state, ptr.x / s, ptr.y / s) } else { paint::Click::Key("enter") };
                match hit {
                    paint::Click::Node(screen, node, tile) => send(&serde_json::json!({"t": "tap", "screen": screen, "node": node, "tile": tile})),
                    paint::Click::Key(key) => send(&serde_json::json!({"t": "key", "key": key, "text": ""})),
                    paint::Click::Nothing => {}
                }
                continue;
            }
            Event::Typed => {
                if ptr.shown {
                    ptr.shown = false;
                    if let Some(d) = display.as_mut() {
                        repoint(d, &mut ptr, zoomed(scale, &current), fonts);
                    }
                    ptr.under = None;
                }
                continue;
            }
        };
        let Ok(m) = serde_json::from_str::<Msg>(&line) else { continue };
        match m.t.as_str() {
            "frame" | "patch" => {
                let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) else { continue };
                if m.t == "frame" {
                    doc = v;
                } else if let Some(p) = v.get("patch") {
                    frame::merge(&mut doc, p);
                }
                match serde_json::from_value::<frame::Frame>(doc.clone()) {
                    Ok(f) => {
                        current = f;
                        if display.is_none() {
                            let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                            paint::layout(&mut cv, &current, &mut state);
                        }
                        if !lit {
                            stale = true;
                        } else if let Some(d) = display.as_mut() {
                            // Frames until the motion settles (a scroll or a
                            // focus fade is under a fifth of a second).
                            loop {
                                state.now = Some(std::time::Instant::now());
                                let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                                cv.mask_secure = false;
                                paint::draw(&mut cv, &current, &mut state);
                                present(d, cv, &mut ptr);
                                if !paint::animating(&state) {
                                    break;
                                }
                                std::thread::sleep(std::time::Duration::from_millis(16));
                            }
                        }
                    }
                    Err(e) => eprintln!("phone-paint: bad frame: {e}"),
                }
                if let Some(l) = paint::take_layout(&mut state) {
                    send(&serde_json::json!({"t": "layout", "layout": l}));
                }
            }
            "snap" => {
                // A snapshot is the settled picture, as the protocol asks.
                let live = state.now.take();
                let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                paint::draw(&mut cv, &current, &mut state);
                state.now = live;
                let path = std::path::Path::new(&snap_dir).join(format!("{}.png", m.name));
                let res = cv.png().and_then(|png| {
                    std::fs::create_dir_all(&snap_dir).map_err(|e| e.to_string())?;
                    std::fs::write(&path, png).map_err(|e| e.to_string())?;
                    if skin {
                        let body = skin::skinned(&cv.pm, &dev.keypad, cv.s, fonts);
                        std::fs::write(path.with_extension("skin.png"), body.png()?).map_err(|e| e.to_string())?;
                    }
                    Ok(())
                });
                let mut reply = serde_json::json!({"t": "snapped", "name": m.name, "err": res.err().unwrap_or_default()});
                if m.echo {
                    reply["frame"] = doc.clone();
                }
                send(&reply);
            }
            "power" => {
                let on = m.on.unwrap_or(true);
                let res = display.as_mut().map_or(Ok(()), |d| d.power(on));
                if res.is_ok() {
                    lit = on;
                }
                if lit && stale {
                    stale = false;
                    if let Some(d) = display.as_mut() {
                        state.now = None;
                        let mut cv = canvas::Canvas::new(width, height, zoomed(scale, &current), fonts);
                        cv.mask_secure = false;
                        paint::draw(&mut cv, &current, &mut state);
                        present(d, cv, &mut ptr);
                    }
                }
                send(&serde_json::json!({"t": "power", "on": lit, "err": res.err().unwrap_or_default()}));
            }
            "quit" => break,
            _ => {}
        }
    }
}
