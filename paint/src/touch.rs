//! Touchscreens: a whole phone's own (the 6T's Synaptics RMI4), QEMU's
//! virtio multitouch device. The kernel's contacts come down to the one
//! finger the phone follows, placed on the frame; main.rs says what is
//! under it there and sends it to the Shell, which decides what a touch
//! means (shell/touch.go, 23-renderer-protocol.md §3 `touch`).

use std::os::fd::{AsFd, AsRawFd};
use std::sync::mpsc::Sender;

use evdev::raw_stream::RawDevice;
use evdev::{AbsoluteAxisCode, EventSummary, KeyCode, SynchronizationCode};

use crate::input::Event;

/// What the finger followed did in one report of the device.
#[derive(Debug, PartialEq, Clone, Copy)]
pub enum Phase {
    Down,
    Move,
    Up,
    /// The device went away (or lost events) with the finger down.
    Cancel,
}

impl Phase {
    pub fn name(self) -> &'static str {
        match self {
            Phase::Down => "down",
            Phase::Move => "move",
            Phase::Up => "up",
            Phase::Cancel => "cancel",
        }
    }
}

/// Slots past this many are left alone (the 6T's controller has 10).
const SLOTS: usize = 16;

/// One slot of the multitouch protocol: its contact's tracking id (-1:
/// none) and position.
#[derive(Clone, Copy)]
struct Slot {
    id: i32,
    x: i32,
    y: i32,
}

/// A touchscreen's contacts as the kernel reports them: slots, each with
/// a tracking id that is -1 once lifted (multitouch protocol B), or on a
/// device without slots one contact (ABS_X, ABS_Y, BTN_TOUCH). The phone
/// follows one finger: the first to come down while none is followed,
/// until it lifts; other fingers meanwhile, and those still down after
/// it, are left alone. Contacts down before the device was opened are not
/// known (their tracking id came before) and are left alone too.
pub struct Contacts {
    multi: bool,
    slot: usize,
    slots: Vec<Slot>,
    single: Slot,
    /// The slot followed and its contact's tracking id.
    followed: Option<(usize, i32)>,
    /// Where the finger followed was last reported.
    at: (i32, i32),
    /// The finger followed lifted while others stayed down: none is
    /// followed until all are up.
    others: bool,
    /// Events were lost: the rest of this report is left out.
    dropped: bool,
}

impl Contacts {
    pub fn new(multi: bool) -> Contacts {
        Contacts { multi, slot: 0, slots: vec![Slot { id: -1, x: 0, y: 0 }; SLOTS], single: Slot { id: -1, x: 0, y: 0 }, followed: None, at: (0, 0), others: false, dropped: false }
    }

    pub fn abs(&mut self, code: AbsoluteAxisCode, value: i32) {
        if self.dropped {
            return;
        }
        if !self.multi {
            match code {
                AbsoluteAxisCode::ABS_X => self.single.x = value,
                AbsoluteAxisCode::ABS_Y => self.single.y = value,
                _ => {}
            }
            return;
        }
        if code == AbsoluteAxisCode::ABS_MT_SLOT {
            self.slot = usize::try_from(value).unwrap_or(SLOTS);
            return;
        }
        let Some(s) = self.slots.get_mut(self.slot) else { return };
        match code {
            AbsoluteAxisCode::ABS_MT_TRACKING_ID => s.id = value,
            AbsoluteAxisCode::ABS_MT_POSITION_X => s.x = value,
            AbsoluteAxisCode::ABS_MT_POSITION_Y => s.y = value,
            _ => {}
        }
    }

    /// BTN_TOUCH: the single contact of a device without slots.
    pub fn button(&mut self, down: bool) {
        if !self.multi && !self.dropped {
            self.single.id = if down { 0 } else { -1 };
        }
    }

    /// SYN_DROPPED: what follows until the next report is left out, and
    /// the finger followed is let go (Cancel at that report).
    pub fn lost(&mut self) {
        self.dropped = true;
    }

    /// SYN_REPORT: what the finger followed did since the last report,
    /// and where it is (the device's units).
    pub fn report(&mut self) -> Option<(Phase, i32, i32)> {
        let slots: &[Slot] = if self.multi { &self.slots } else { std::slice::from_ref(&self.single) };
        if std::mem::take(&mut self.dropped) {
            // Whatever is down now is no longer known: a contact counts
            // again from its next tracking id.
            for s in self.slots.iter_mut() {
                s.id = -1;
            }
            self.single.id = -1;
            self.others = false;
            return self.followed.take().map(|_| (Phase::Cancel, self.at.0, self.at.1));
        }
        if let Some((i, id)) = self.followed {
            let s = slots[i];
            if s.id != id {
                self.followed = None;
                self.others = slots.iter().any(|s| s.id >= 0);
                return Some((Phase::Up, self.at.0, self.at.1));
            }
            if (s.x, s.y) == self.at {
                return None;
            }
            self.at = (s.x, s.y);
            return Some((Phase::Move, s.x, s.y));
        }
        if self.others {
            self.others = slots.iter().any(|s| s.id >= 0);
            return None;
        }
        let (i, s) = slots.iter().enumerate().find(|(_, s)| s.id >= 0)?;
        self.followed = Some((i, s.id));
        self.at = (s.x, s.y);
        Some((Phase::Down, s.x, s.y))
    }
}

/// A device axis's range, as its absinfo says.
#[derive(Clone, Copy, Debug)]
pub struct Range {
    pub min: i32,
    pub max: i32,
}

/// Where the frame is on the panel: at origin (the middle of a bigger
/// screen, the inset rows of a whole phone, kms::letterbox), or turned a
/// quarter clockwise onto it (a portrait panel in a landscape phone).
#[derive(Clone, Copy, Debug)]
pub struct Place {
    pub panel: (u32, u32),
    pub origin: (usize, usize),
    pub rotated: bool,
    pub frame: (u32, u32),
}

impl Place {
    /// Where on the frame (its pixels) a reading (x, y) of a touchscreen
    /// whose axes span the panel is; None outside the frame (the letterbox,
    /// the inset rows) unless clamp, which puts it on the frame's nearest
    /// edge. A reading is the middle of its part of the axis.
    pub fn frame_px(&self, rx: Range, ry: Range, x: i32, y: i32, clamp: bool) -> Option<(f32, f32)> {
        let along = |v: i32, r: Range, n: u32| ((v - r.min) as f32 + 0.5) / ((r.max - r.min) as f32 + 1.0) * n as f32;
        let (px, py) = (along(x, rx, self.panel.0), along(y, ry, self.panel.1));
        let (fw, fh) = (self.frame.0 as f32, self.frame.1 as f32);
        let (fx, fy) = if self.rotated { (py, fh - px) } else { (px - self.origin.0 as f32, py - self.origin.1 as f32) };
        if clamp {
            return Some((fx.clamp(0.0, fw - 0.01), fy.clamp(0.0, fh - 0.01)));
        }
        (fx >= 0.0 && fx < fw && fy >= 0.0 && fy < fh).then_some((fx, fy))
    }
}

/// Whether a device is a touchscreen: it says its axes are the screen's
/// (INPUT_PROP_DIRECT) and has a position, of slots or of one contact. A
/// touchpad's axes are not the screen's.
pub fn is_touchscreen(d: &evdev::Device) -> bool {
    let abs = d.supported_absolute_axes();
    d.properties().contains(evdev::PropType::DIRECT)
        && abs.is_some_and(|a| a.contains(AbsoluteAxisCode::ABS_MT_POSITION_X) || a.contains(AbsoluteAxisCode::ABS_X))
}

/// Reads touchscreen id (its number in "input") until it goes away: the
/// finger followed, in frame pixels, goes to the main loop. Raw events,
/// not evdev's synchronised ones: a SYN_DROPPED must reach Contacts,
/// which lets the finger go.
pub fn touch_loop(dev: evdev::Device, id: u32, place: Place, events: &Sender<Event>) {
    let Ok(fd) = dev.as_fd().try_clone_to_owned() else { return };
    drop(dev);
    let Ok(mut dev) = RawDevice::from_fd(fd) else { return };
    let multi = dev.supported_absolute_axes().is_some_and(|a| a.contains(AbsoluteAxisCode::ABS_MT_POSITION_X));
    let (cx, cy) = if multi { (AbsoluteAxisCode::ABS_MT_POSITION_X, AbsoluteAxisCode::ABS_MT_POSITION_Y) } else { (AbsoluteAxisCode::ABS_X, AbsoluteAxisCode::ABS_Y) };
    let (mut rx, mut ry) = (None, None);
    if let Ok(info) = dev.get_absinfo() {
        for (code, a) in info {
            let r = Some(Range { min: a.minimum(), max: a.maximum() });
            if code == cx {
                rx = r;
            } else if code == cy {
                ry = r;
            }
        }
    }
    let (Some(rx), Some(ry)) = (rx, ry) else {
        eprintln!("phone-paint: touchscreen {}: no range for its axes", dev.name().unwrap_or(""));
        return;
    };
    eprintln!("phone-paint: touchscreen {}: x {}..{}, y {}..{} on a {}x{} panel", dev.name().unwrap_or(""), rx.min, rx.max, ry.min, ry.max, place.panel.0, place.panel.1);
    let mut c = Contacts::new(multi);
    let mut down = false;
    let monotonic = crate::perf::monotonic_events(dev.as_raw_fd());
    loop {
        let Ok(evs) = dev.fetch_events() else {
            if down {
                let _ = events.send(Event::Touch(id, Phase::Cancel, 0.0, 0.0, crate::perf::Stamp::now(crate::perf::Source::Read)));
            }
            return;
        };
        for ev in evs {
            let at = ev.timestamp();
            match ev.destructure() {
                EventSummary::AbsoluteAxis(_, code, v) => c.abs(code, v),
                EventSummary::Key(_, KeyCode::BTN_TOUCH, v) => c.button(v != 0),
                EventSummary::Synchronization(_, SynchronizationCode::SYN_DROPPED, _) => c.lost(),
                EventSummary::Synchronization(_, SynchronizationCode::SYN_REPORT, _) => {
                    let Some((phase, x, y)) = c.report() else { continue };
                    // A finger coming down outside the frame is left alone
                    // until it lifts; one that moves out stays on its edge.
                    let Some((fx, fy)) = place.frame_px(rx, ry, x, y, phase != Phase::Down) else { continue };
                    match phase {
                        Phase::Down => down = true,
                        _ if !down => continue,
                        Phase::Move => {}
                        Phase::Up | Phase::Cancel => down = false,
                    }
                    let _ = events.send(Event::Touch(id, phase, fx, fy, crate::perf::event_time(at, monotonic)));
                }
                _ => {}
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use AbsoluteAxisCode as A;

    /// Feeds reports, each a list of (axis, value) events, and collects what
    /// each report says.
    fn feed(c: &mut Contacts, reports: &[&[(A, i32)]]) -> Vec<Option<(Phase, i32, i32)>> {
        reports
            .iter()
            .map(|evs| {
                for &(code, v) in evs.iter() {
                    c.abs(code, v);
                }
                c.report()
            })
            .collect()
    }

    // Protocol B as the 6T's controller and QEMU's virtio-multitouch send
    // it: a finger down (a tracking id), moving, a second finger that is
    // left alone, the first lifting (-1) while the second stays: nothing
    // is followed until both are up.
    #[test]
    fn the_first_finger_is_followed() {
        let mut c = Contacts::new(true);
        let got = feed(
            &mut c,
            &[
                &[(A::ABS_MT_SLOT, 0), (A::ABS_MT_TRACKING_ID, 7), (A::ABS_MT_POSITION_X, 100), (A::ABS_MT_POSITION_Y, 200)],
                &[(A::ABS_MT_POSITION_Y, 180)],
                &[(A::ABS_MT_SLOT, 1), (A::ABS_MT_TRACKING_ID, 8), (A::ABS_MT_POSITION_X, 900), (A::ABS_MT_POSITION_Y, 900)],
                &[(A::ABS_MT_POSITION_X, 950)],
                &[(A::ABS_MT_SLOT, 0), (A::ABS_MT_POSITION_X, 110)],
                &[(A::ABS_MT_TRACKING_ID, -1)],
                &[(A::ABS_MT_SLOT, 1), (A::ABS_MT_POSITION_X, 960)],
                &[(A::ABS_MT_TRACKING_ID, -1)],
                &[(A::ABS_MT_SLOT, 0), (A::ABS_MT_TRACKING_ID, 9), (A::ABS_MT_POSITION_X, 50), (A::ABS_MT_POSITION_Y, 60)],
            ],
        );
        assert_eq!(
            got,
            [
                Some((Phase::Down, 100, 200)),
                Some((Phase::Move, 100, 180)),
                None,
                None,
                Some((Phase::Move, 110, 180)),
                Some((Phase::Up, 110, 180)),
                None,
                None,
                Some((Phase::Down, 50, 60)),
            ]
        );
    }

    // A slot whose tracking id changes in one report: the finger lifted and
    // another came down there; the new one is not the one followed.
    #[test]
    fn a_new_tracking_id_is_another_finger() {
        let mut c = Contacts::new(true);
        let got = feed(&mut c, &[&[(A::ABS_MT_TRACKING_ID, 1), (A::ABS_MT_POSITION_X, 5), (A::ABS_MT_POSITION_Y, 5)], &[(A::ABS_MT_TRACKING_ID, 2), (A::ABS_MT_POSITION_X, 9)], &[(A::ABS_MT_TRACKING_ID, -1)], &[]]);
        assert_eq!(got, [Some((Phase::Down, 5, 5)), Some((Phase::Up, 5, 5)), None, None]);
    }

    // A finger already down when the device was opened reports positions
    // for a slot with no tracking id seen: left alone.
    #[test]
    fn a_finger_down_before_is_left_alone() {
        let mut c = Contacts::new(true);
        let got = feed(&mut c, &[&[(A::ABS_MT_POSITION_X, 5)], &[(A::ABS_MT_TRACKING_ID, -1)], &[(A::ABS_MT_TRACKING_ID, 3), (A::ABS_MT_POSITION_X, 6), (A::ABS_MT_POSITION_Y, 1)]]);
        assert_eq!(got, [None, None, Some((Phase::Down, 6, 1))]);
    }

    // Lost events cancel the finger followed; what is down then counts only
    // from its next tracking id.
    #[test]
    fn lost_events_cancel_the_touch() {
        let mut c = Contacts::new(true);
        feed(&mut c, &[&[(A::ABS_MT_TRACKING_ID, 1), (A::ABS_MT_POSITION_X, 5), (A::ABS_MT_POSITION_Y, 5)]]);
        c.lost();
        c.abs(A::ABS_MT_POSITION_X, 99);
        assert_eq!(c.report(), Some((Phase::Cancel, 5, 5)));
        assert_eq!(feed(&mut c, &[&[(A::ABS_MT_POSITION_X, 7)], &[(A::ABS_MT_TRACKING_ID, 4)]]), [None, Some((Phase::Down, 7, 5))]);
    }

    // A device without slots: BTN_TOUCH and ABS_X, ABS_Y.
    #[test]
    fn one_contact_without_slots() {
        let mut c = Contacts::new(false);
        c.abs(A::ABS_X, 10);
        c.abs(A::ABS_Y, 20);
        c.button(true);
        assert_eq!(c.report(), Some((Phase::Down, 10, 20)));
        c.abs(A::ABS_MT_POSITION_X, 500);
        c.abs(A::ABS_X, 12);
        assert_eq!(c.report(), Some((Phase::Move, 12, 20)));
        assert_eq!(c.report(), None);
        c.button(false);
        assert_eq!(c.report(), Some((Phase::Up, 12, 20)));
    }

    const QEMU: Range = Range { min: 0, max: 0x7fff };

    /// The frame pixel a reading lands on.
    fn pixel(p: &Place, rx: Range, ry: Range, x: i32, y: i32, clamp: bool) -> Option<(i32, i32)> {
        p.frame_px(rx, ry, x, y, clamp).map(|(x, y)| (x.floor() as i32, y.floor() as i32))
    }

    // QEMU's 480x640 virtio-gpu display, all of it the frame: the device's
    // 0..32767 spans it.
    #[test]
    fn the_whole_panel_is_the_frame() {
        let p = Place { panel: (480, 640), origin: (0, 0), rotated: false, frame: (480, 640) };
        assert_eq!(pixel(&p, QEMU, QEMU, 0, 0, false), Some((0, 0)));
        assert_eq!(pixel(&p, QEMU, QEMU, 0x4000, 0x7fff, false), Some((240, 639)));
        assert_eq!(pixel(&p, QEMU, QEMU, 0x7fff, 0x2000, false), Some((479, 160)));
    }

    // The 6T: 1080x2340, the frame 1080x2180 between the inset rows. A
    // touch on a dark row is outside; a finger moving onto one stays on
    // the frame's edge.
    #[test]
    fn the_inset_rows_are_outside_the_frame() {
        let p = Place { panel: (1080, 2340), origin: (0, 80), rotated: false, frame: (1080, 2180) };
        let (rx, ry) = (Range { min: 0, max: 1079 }, Range { min: 0, max: 2339 });
        assert_eq!(pixel(&p, rx, ry, 540, 40, false), None);
        assert_eq!(pixel(&p, rx, ry, 540, 2300, false), None);
        assert_eq!(pixel(&p, rx, ry, 540, 80, false), Some((540, 0)));
        assert_eq!(pixel(&p, rx, ry, 1079, 2259, false), Some((1079, 2179)));
        assert_eq!(pixel(&p, rx, ry, 540, 40, true), Some((540, 0)));
        assert_eq!(pixel(&p, rx, ry, 540, 2339, true), Some((540, 2179)));
        // A controller of another resolution than the panel's.
        assert_eq!(pixel(&p, Range { min: 0, max: 2159 }, Range { min: 0, max: 4679 }, 1080, 2340, false), Some((540, 1090)));
    }

    // A 640x480 frame turned a quarter clockwise onto a 480x640 panel: the
    // frame's top left is the panel's top right, its bottom left the
    // panel's top left (kms::turned).
    #[test]
    fn a_turned_frame() {
        let p = Place { panel: (480, 640), origin: (0, 0), rotated: true, frame: (640, 480) };
        let (rx, ry) = (Range { min: 0, max: 479 }, Range { min: 0, max: 639 });
        assert_eq!(pixel(&p, rx, ry, 479, 0, false), Some((0, 0)));
        assert_eq!(pixel(&p, rx, ry, 0, 0, false), Some((0, 479)));
        assert_eq!(pixel(&p, rx, ry, 0, 639, false), Some((639, 479)));
        let (x, y) = crate::kms::turned(100, 200, 480);
        assert_eq!(pixel(&p, rx, ry, x as i32, y as i32, false), Some((100, 200)));
    }
}
