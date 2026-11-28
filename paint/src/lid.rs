//! A flip's lid (24-sku-framework.md §9.10.8): a Hall switch the board
//! makes a gpio-keys device of its own, reporting the switch SW_LID, set
//! while the lid is shut. Where the lid is when the renderer opens the
//! device and at each move goes to the shell, which turns the inner
//! display off and on by it (shell/lid.go).

use evdev::{EventSummary, SwitchCode};

/// Whether a device is the board's lid: the name the SKU gives it (want;
/// none: the board has no lid) and the lid switch it reports.
pub fn matches(name: Option<&str>, has_lid: bool, want: &str) -> bool {
    !want.is_empty() && name == Some(want) && has_lid
}

pub fn is_lid(d: &evdev::Device, want: &str) -> bool {
    matches(d.name(), d.supported_switches().is_some_and(|s| s.contains(SwitchCode::SW_LID)), want)
}

/// Whether an event shuts the lid (true) or opens it, if it moves it.
fn moved(ev: EventSummary) -> Option<bool> {
    match ev {
        EventSummary::Switch(_, SwitchCode::SW_LID, value) => Some(value != 0),
        _ => None,
    }
}

fn report(shut: bool, send: fn(&serde_json::Value)) {
    send(&serde_json::json!({"t": "lid", "shut": shut}));
}

/// Reports where the lid is (EVIOCGSW: gpio-keys reported it when the
/// device was opened, before this reads), then each move; the synced
/// stream reads the state again after SYN_DROPPED and gives what changed
/// as events. A lid it cannot read, or whose device goes, is reported
/// open: the shell drops the keypad's keys while the lid is shut, and
/// nothing would tell it that the lid opened.
pub fn lid_loop(mut dev: evdev::Device, send: fn(&serde_json::Value)) {
    let name = dev.name().unwrap_or("").to_string();
    let shut = match dev.get_switch_state() {
        Ok(sw) => sw.contains(SwitchCode::SW_LID),
        Err(e) => {
            eprintln!("phone-paint: lid {name}: {e}; reported open");
            false
        }
    };
    let moves = std::iter::from_fn(|| dev.fetch_events().ok().map(|evs| evs.filter_map(|ev| moved(ev.destructure())).collect::<Vec<_>>())).flatten();
    if follow(shut, moves, |s| report(s, send)) {
        eprintln!("phone-paint: lid {name}: gone while shut; reported open");
    }
}

/// Reports the lid where it starts and at each move, and open once the
/// moves end (the device went) with the lid shut; whether it was.
fn follow(mut shut: bool, moves: impl Iterator<Item = bool>, mut report: impl FnMut(bool)) -> bool {
    report(shut);
    for s in moves {
        shut = s;
        report(s);
    }
    if shut {
        report(false);
    }
    shut
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_lid_is_the_skus_device() {
        // tools/sku.py's gpio-keys node: the device [input] lid names.
        assert!(matches(Some("Lid"), true, "Lid"));
        assert!(!matches(Some("Lid"), true, ""));
        assert!(!matches(Some("Lid"), false, "Lid"));
        assert!(!matches(Some("gpio-keys"), true, "Lid"));
        assert!(!matches(None, true, "Lid"));
    }

    #[test]
    fn only_the_lid_switch_moves_it() {
        use evdev::{AbsoluteAxisCode, EventType, InputEvent};
        let sw = |code: SwitchCode, value| InputEvent::new(EventType::SWITCH.0, code.0, value).destructure();
        assert_eq!(moved(sw(SwitchCode::SW_LID, 1)), Some(true));
        assert_eq!(moved(sw(SwitchCode::SW_LID, 0)), Some(false));
        assert_eq!(moved(sw(SwitchCode::SW_MUTE_DEVICE, 1)), None);
        assert_eq!(moved(sw(SwitchCode::SW_TABLET_MODE, 1)), None);
        assert_eq!(moved(InputEvent::new(EventType::ABSOLUTE.0, crate::sound_switch::ABS_SND_PROFILE.0, 1).destructure()), None);
        assert_eq!(moved(InputEvent::new(EventType::ABSOLUTE.0, AbsoluteAxisCode::ABS_X.0, 1).destructure()), None);
        assert_eq!(moved(InputEvent::new(EventType::SYNCHRONIZATION.0, 0, 0).destructure()), None);
    }

    #[test]
    fn a_lid_whose_device_goes_shut_is_reported_open() {
        let run = |shut, moves: &[bool]| {
            let mut got = Vec::new();
            let ended_shut = follow(shut, moves.iter().copied(), |s| got.push(s));
            (got, ended_shut)
        };
        assert_eq!(run(false, &[true, false]), (vec![false, true, false], false));
        assert_eq!(run(true, &[]), (vec![true, false], true));
        assert_eq!(run(false, &[true]), (vec![false, true, false], true));
    }
}
