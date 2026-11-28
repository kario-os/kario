//! The phone's sound switch (24-sku-framework.md §6.1): a three-position
//! slider (the OnePlus 6T's alert slider: gpio-keys reporting
//! ABS_SND_PROFILE) or a two-position mute switch (gpio-keys reporting the
//! switch SW_MUTE_DEVICE). Its position when the renderer opens it and at
//! each move goes to the shell, which sets the sound mode by it
//! (shell/soundmode.go).

use evdev::{AbsoluteAxisCode, EventSummary, SwitchCode};

/// ABS_SND_PROFILE (Linux 6.7), which the evdev crate does not name.
pub const ABS_SND_PROFILE: AbsoluteAxisCode = AbsoluteAxisCode(0x22);

/// The shell's name of a position: the kernel's SND_PROFILE_SILENT,
/// SND_PROFILE_VIBRATE and SND_PROFILE_RING.
pub fn position(value: i32) -> Option<&'static str> {
    match value {
        0 => Some("silent"),
        1 => Some("vibrate"),
        2 => Some("ring"),
        _ => None,
    }
}

/// The shell's name of the mute switch's position: SW_MUTE_DEVICE set (the
/// device muted) is silent, clear is ring.
pub fn mute_position(set: bool) -> &'static str {
    if set {
        "silent"
    } else {
        "ring"
    }
}

/// Whether a device is the board's sound switch: the name the SKU gives it
/// (want; none: the board has no switch) and the sound profile axis or the
/// mute switch it reports.
pub fn matches(name: Option<&str>, has_axis: bool, has_mute: bool, want: &str) -> bool {
    !want.is_empty() && name == Some(want) && (has_axis || has_mute)
}

fn has_axis(d: &evdev::Device) -> bool {
    d.supported_absolute_axes().is_some_and(|a| a.contains(ABS_SND_PROFILE))
}

fn has_mute(d: &evdev::Device) -> bool {
    d.supported_switches().is_some_and(|s| s.contains(SwitchCode::SW_MUTE_DEVICE))
}

pub fn is_switch(d: &evdev::Device, want: &str) -> bool {
    matches(d.name(), has_axis(d), has_mute(d), want)
}

/// The position an event moves the switch to, if it does.
fn moved(ev: EventSummary) -> Option<&'static str> {
    match ev {
        EventSummary::AbsoluteAxis(_, code, value) if code == ABS_SND_PROFILE => position(value),
        EventSummary::Switch(_, SwitchCode::SW_MUTE_DEVICE, value) => Some(mute_position(value != 0)),
        _ => None,
    }
}

fn report(pos: &str, send: fn(&serde_json::Value)) {
    send(&serde_json::json!({"t": "sound_switch", "position": pos}));
}

/// Where the switch is now (EVIOCGABS or EVIOCGSW: gpio-keys reported it
/// when the device was opened, before this reads).
fn initial(dev: &evdev::Device, name: &str) -> Option<&'static str> {
    if has_axis(dev) {
        match dev.get_abs_state() {
            Ok(abs) => {
                let v = abs[ABS_SND_PROFILE.0 as usize].value;
                let pos = position(v);
                if pos.is_none() {
                    eprintln!("phone-paint: sound switch {name}: at {v}, no position");
                }
                pos
            }
            Err(e) => {
                eprintln!("phone-paint: sound switch {name}: {e}");
                None
            }
        }
    } else {
        match dev.get_switch_state() {
            Ok(sw) => Some(mute_position(sw.contains(SwitchCode::SW_MUTE_DEVICE))),
            Err(e) => {
                eprintln!("phone-paint: sound switch {name}: {e}");
                None
            }
        }
    }
}

/// Reports where the switch is, then each move, until the device goes.
/// The synced stream reads the state again after SYN_DROPPED and gives
/// what changed as events.
pub fn switch_loop(mut dev: evdev::Device, send: fn(&serde_json::Value)) {
    let name = dev.name().unwrap_or("").to_string();
    if let Some(pos) = initial(&dev, &name) {
        report(pos, send);
    }
    loop {
        let Ok(evs) = dev.fetch_events() else { return };
        for ev in evs {
            if let Some(pos) = moved(ev.destructure()) {
                report(pos, send);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_three_positions() {
        assert_eq!(position(0), Some("silent"));
        assert_eq!(position(1), Some("vibrate"));
        assert_eq!(position(2), Some("ring"));
        assert_eq!(position(3), None);
        assert_eq!(position(-1), None);
    }

    #[test]
    fn the_two_positions() {
        assert_eq!(mute_position(true), "silent");
        assert_eq!(mute_position(false), "ring");
    }

    #[test]
    fn the_switch_is_the_skus_device() {
        // The OnePlus 6T's alert slider (sdm845-oneplus-common.dtsi).
        assert!(matches(Some("Alert slider"), true, false, "Alert slider"));
        assert!(!matches(Some("Alert slider"), true, false, ""));
        assert!(!matches(Some("Alert slider"), false, false, "Alert slider"));
        assert!(!matches(Some("Volume keys"), true, false, "Alert slider"));
        assert!(!matches(None, true, false, "Alert slider"));
        // A mute switch in a gpio-keys device of its own.
        assert!(matches(Some("Mute switch"), false, true, "Mute switch"));
        assert!(!matches(Some("Mute switch"), false, true, ""));
        assert!(!matches(Some("gpio-keys"), false, true, "Mute switch"));
    }

    #[test]
    fn only_the_sound_profile_axis_and_the_mute_switch_move_it() {
        use evdev::{EventType, InputEvent};
        let abs = |code: AbsoluteAxisCode, value| InputEvent::new(EventType::ABSOLUTE.0, code.0, value).destructure();
        let sw = |code: SwitchCode, value| InputEvent::new(EventType::SWITCH.0, code.0, value).destructure();
        assert_eq!(moved(abs(ABS_SND_PROFILE, 1)), Some("vibrate"));
        assert_eq!(moved(abs(ABS_SND_PROFILE, 7)), None);
        assert_eq!(moved(abs(AbsoluteAxisCode::ABS_VOLUME, 2)), None);
        assert_eq!(moved(sw(SwitchCode::SW_MUTE_DEVICE, 1)), Some("silent"));
        assert_eq!(moved(sw(SwitchCode::SW_MUTE_DEVICE, 0)), Some("ring"));
        assert_eq!(moved(sw(SwitchCode::SW_LID, 1)), None);
        assert_eq!(moved(InputEvent::new(EventType::SYNCHRONIZATION.0, 0, 0).destructure()), None);
    }
}
