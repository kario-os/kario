//! The panel's refresh rate (08-display-adaptive-refresh.md): phone-displayd
//! chooses it and the shell passes it on ("rate"). The live display
//! switches the panel to the mode of that rate (kms.rs) and hands frames
//! over at its vertical blanks where page flips wait for one, else a period
//! of the mode it runs at after the last (`wait`), on the CPU and the GPU
//! path alike: motion never takes steps more often than the panel shows
//! them. The renderer says when it begins and ends animating ("motion"),
//! which asks for the high rate.

use std::time::Duration;

/// The rate until the shell says otherwise: the baseline panel's.
pub const DEFAULT_HZ: u32 = 60;

pub struct Pace {
    hz: u32,
    moving: bool,
}

impl Default for Pace {
    fn default() -> Pace {
        Pace { hz: DEFAULT_HZ, moving: false }
    }
}

/// One frame of a panel at hz.
pub fn period(hz: u32) -> Duration {
    Duration::from_nanos(1_000_000_000 / u64::from(hz.clamp(1, 1000)))
}

/// How long to wait before showing the next frame, `since` the last one
/// shown, on a panel whose frames last `period`: none once it has passed.
pub fn wait(period: Duration, since: Duration) -> Duration {
    period.saturating_sub(since)
}

impl Pace {
    pub fn period(&self) -> Duration {
        period(self.hz)
    }

    /// The rate the shell said; false for none a panel has.
    pub fn set(&mut self, hz: u32) -> bool {
        if !(1..=1000).contains(&hz) {
            return false;
        }
        self.hz = hz;
        true
    }

    /// An animation began (on) or the motion settled: the message for the
    /// shell when that changes.
    pub fn motion(&mut self, on: bool) -> Option<serde_json::Value> {
        if on == self.moving {
            return None;
        }
        self.moving = on;
        Some(serde_json::json!({"t": "motion", "on": on}))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_period_is_the_panels() {
        assert_eq!(period(60), Duration::from_nanos(16_666_666));
        assert_eq!(period(120), Duration::from_nanos(8_333_333));
        assert_eq!(period(90), Duration::from_nanos(11_111_111));
        assert_eq!(period(0), Duration::from_secs(1), "clamped, never a division by zero");
        let mut p = Pace::default();
        assert_eq!(p.period(), period(60));
        assert!(p.set(120));
        assert_eq!(p.period(), period(120));
        assert!(!p.set(0) && !p.set(5000));
        assert_eq!(p.period(), period(120), "a rate refused leaves the one before");
    }

    #[test]
    fn motion_is_said_once_each_way() {
        let mut p = Pace::default();
        assert_eq!(p.motion(false), None, "nothing moved");
        assert_eq!(p.motion(true), Some(serde_json::json!({"t": "motion", "on": true})));
        assert_eq!(p.motion(true), None);
        assert_eq!(p.motion(false), Some(serde_json::json!({"t": "motion", "on": false})));
        assert_eq!(p.motion(false), None);
    }

    // Frames closer than a period wait for the rest of it: at 60 Hz no
    // more than 60 a second reach the panel, at 120 no more than 120.
    #[test]
    fn frames_wait_for_the_period() {
        assert_eq!(wait(period(60), Duration::from_millis(4)), period(60) - Duration::from_millis(4));
        assert_eq!(wait(period(60), Duration::from_millis(20)), Duration::ZERO);
        assert_eq!(wait(period(120), Duration::from_millis(4)), period(120) - Duration::from_millis(4));
        assert_eq!(wait(period(120), Duration::from_millis(9)), Duration::ZERO);
        for hz in [60, 120] {
            let (mut t, mut shown) = (Duration::ZERO, 0);
            let mut last = None::<Duration>;
            // Frames offered every 2 ms for a second.
            while t < Duration::from_secs(1) {
                if last.is_none_or(|l| wait(period(hz), t - l).is_zero()) {
                    shown += 1;
                    last = Some(t);
                }
                t += Duration::from_millis(2);
            }
            assert!(shown <= hz as usize && shown > hz as usize / 2, "{shown} frames shown in a second at {hz} Hz");
        }
    }
}
