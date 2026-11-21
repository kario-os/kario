//! How the keys are timed (the shell's "keys" message, Settings ›
//! Accessibility): the repeat's delay and rate, the long press, slow keys
//! (a press counts once the key has been held that long) and sticky keys.
//! The gate sits between a keyboard's events and kms.rs's Keyboard: it
//! repeats the key held itself (the kernel's own repeats are dropped, so
//! a keypad without them repeats too), and says when the held key's long
//! press is due whether or not it repeats.

use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::time::{Duration, Instant};

use evdev::KeyCode;

/// The event value of a held key's long press coming due (evdev's own are
/// 0 up, 1 down, 2 repeat).
pub const TICK: i32 = 3;

/// The timing as the shell last sent it; every keyboard's loop reads it.
pub struct KeyTiming {
    delay: AtomicU32,
    rate: AtomicU32,
    long: AtomicU32,
    slow: AtomicU32,
    sticky: AtomicBool,
}

impl Default for KeyTiming {
    /// The kernel's repeat (250 ms, then 30 a second) and the long press
    /// before it could be set.
    fn default() -> KeyTiming {
        KeyTiming { delay: AtomicU32::new(250), rate: AtomicU32::new(30), long: AtomicU32::new(600), slow: AtomicU32::new(0), sticky: AtomicBool::new(false) }
    }
}

impl KeyTiming {
    /// Takes the "keys" message; what it leaves out stays.
    pub fn set(&self, m: &serde_json::Value) {
        let ms = |k: &str, lo: u64, hi: u64| m.get(k).and_then(|v| v.as_u64()).map(|v| v.clamp(lo, hi) as u32);
        if let Some(v) = ms("repeat_delay", 100, 5000) {
            self.delay.store(v, Ordering::Relaxed);
        }
        if let Some(v) = ms("repeat_rate", 0, 100) {
            self.rate.store(v, Ordering::Relaxed);
        }
        if let Some(v) = ms("long_press", 200, 5000) {
            self.long.store(v, Ordering::Relaxed);
        }
        if let Some(v) = ms("slow", 0, 5000) {
            self.slow.store(v, Ordering::Relaxed);
        }
        if let Some(v) = m.get("sticky").and_then(|v| v.as_bool()) {
            self.sticky.store(v, Ordering::Relaxed);
        }
    }

    pub fn now(&self) -> Timing {
        let rate = self.rate.load(Ordering::Relaxed);
        Timing {
            delay: Duration::from_millis(self.delay.load(Ordering::Relaxed) as u64),
            period: (rate > 0).then(|| Duration::from_micros(1_000_000 / rate as u64)),
            long: Duration::from_millis(self.long.load(Ordering::Relaxed) as u64),
            slow: Duration::from_millis(self.slow.load(Ordering::Relaxed) as u64),
            sticky: self.sticky.load(Ordering::Relaxed),
        }
    }
}

/// The timing at one moment; period None: keys do not repeat.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Timing {
    pub delay: Duration,
    pub period: Option<Duration>,
    pub long: Duration,
    pub slow: Duration,
    pub sticky: bool,
}

impl Default for Timing {
    fn default() -> Timing {
        KeyTiming::default().now()
    }
}

/// The key held since its press counted: when it repeats next and when
/// its long press is due (None once it went).
struct Held {
    code: KeyCode,
    next: Option<Instant>,
    long: Option<Instant>,
}

#[derive(Default)]
pub struct Gate {
    /// Slow keys: down, not counted yet.
    pending: Vec<(KeyCode, Instant)>,
    held: Option<Held>,
}

impl Gate {
    /// A keyboard's event: what Keyboard gets of it now.
    pub fn event(&mut self, code: KeyCode, value: i32, now: Instant, t: &Timing) -> Vec<(KeyCode, i32)> {
        match value {
            1 if t.slow > Duration::ZERO => {
                self.pending.retain(|p| p.0 != code);
                self.pending.push((code, now));
                Vec::new()
            }
            1 => {
                self.count(code, now, t);
                vec![(code, 1)]
            }
            0 => {
                if let Some(i) = self.pending.iter().position(|p| p.0 == code) {
                    // Let go before it counted: as if never pressed.
                    self.pending.remove(i);
                    return Vec::new();
                }
                if self.held.as_ref().is_some_and(|h| h.code == code) {
                    self.held = None;
                }
                vec![(code, 0)]
            }
            _ => Vec::new(),
        }
    }

    fn count(&mut self, code: KeyCode, now: Instant, t: &Timing) {
        self.held = Some(Held { code, next: t.period.map(|_| now + t.delay), long: Some(now + t.long) });
    }

    /// When the gate has something to say next, if anything.
    pub fn due(&self, t: &Timing) -> Option<Instant> {
        let slow = self.pending.iter().map(|p| p.1 + t.slow);
        let held = self.held.iter().flat_map(|h| [h.next, h.long]).flatten();
        slow.chain(held).min()
    }

    /// What came due by now: slow keys' presses, the long press, repeats.
    pub fn fire(&mut self, now: Instant, t: &Timing) -> Vec<(KeyCode, i32)> {
        let mut out = Vec::new();
        let ready: Vec<KeyCode> = self.pending.iter().filter(|p| p.1 + t.slow <= now).map(|p| p.0).collect();
        self.pending.retain(|p| p.1 + t.slow > now);
        for code in ready {
            self.count(code, now, t);
            out.push((code, 1));
        }
        if let Some(h) = self.held.as_mut() {
            if h.long.is_some_and(|at| at <= now) {
                h.long = None;
                out.push((h.code, TICK));
            }
            match (h.next, t.period) {
                (Some(at), Some(p)) if at <= now => {
                    out.push((h.code, 2));
                    // Behind (a busy moment): one repeat, not a burst.
                    h.next = Some(if at + p > now { at + p } else { now + p });
                }
                (Some(_), None) => h.next = None,
                _ => {}
            }
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use KeyCode as K;

    fn run(g: &mut Gate, t: &Timing, events: &[(KeyCode, i32, u64)], until: u64) -> Vec<(u64, i32)> {
        let t0 = Instant::now();
        let ms = |n: u64| t0 + Duration::from_millis(n);
        let mut out = Vec::new();
        let mut i = 0;
        let mut now = 0;
        while now <= until {
            while i < events.len() && events[i].2 <= now {
                for (_, v) in g.event(events[i].0, events[i].1, ms(now), t) {
                    out.push((now, v));
                }
                i += 1;
            }
            if g.due(t).is_some_and(|at| at <= ms(now)) {
                for (_, v) in g.fire(ms(now), t) {
                    out.push((now, v));
                }
            }
            now += 1;
        }
        out
    }

    // The default is the kernel's repeat: 250 ms, then every 33 ms; the
    // long press comes at 600 ms between them; kernel repeats are dropped.
    #[test]
    fn repeats_and_the_long_press() {
        let t = Timing::default();
        let mut g = Gate::default();
        let got = run(&mut g, &t, &[(K::KEY_DOWN, 1, 0), (K::KEY_DOWN, 2, 260), (K::KEY_DOWN, 0, 640)], 700);
        let repeats: Vec<u64> = got.iter().filter(|e| e.1 == 2).map(|e| e.0).collect();
        assert_eq!(repeats.first(), Some(&250));
        assert_eq!(repeats.len(), 12, "{repeats:?}");
        assert!(got.contains(&(600, TICK)) && got.contains(&(0, 1)) && got.last() == Some(&(640, 0)), "{got:?}");
        // Slower: half a second, then 4 a second.
        let slow = Timing { delay: Duration::from_millis(500), period: Some(Duration::from_millis(250)), ..t };
        let got = run(&mut g, &slow, &[(K::KEY_DOWN, 1, 0), (K::KEY_DOWN, 0, 1100)], 1200);
        let repeats: Vec<u64> = got.iter().filter(|e| e.1 == 2).map(|e| e.0).collect();
        assert_eq!(repeats, [500, 750, 1000]);
        // No repeats at all: the long press still comes, at its own time.
        let none = Timing { period: None, long: Duration::from_millis(1000), ..t };
        let got = run(&mut g, &none, &[(K::KEY_1, 1, 0), (K::KEY_1, 0, 1500)], 1600);
        assert_eq!(got, [(0, 1), (1000, TICK), (1500, 0)]);
    }

    // Only the last key pressed repeats, and stops when it is let go.
    #[test]
    fn the_last_key_repeats() {
        let t = Timing { delay: Duration::from_millis(100), period: Some(Duration::from_millis(100)), long: Duration::from_secs(9), ..Timing::default() };
        let mut g = Gate::default();
        let t0 = Instant::now();
        let ms = |n: u64| t0 + Duration::from_millis(n);
        g.event(K::KEY_LEFTSHIFT, 1, ms(0), &t);
        g.event(K::KEY_A, 1, ms(10), &t);
        assert_eq!(g.fire(ms(110), &t), [(K::KEY_A, 2)]);
        assert_eq!(g.event(K::KEY_A, 0, ms(150), &t), [(K::KEY_A, 0)]);
        assert_eq!(g.due(&t), None, "Shift does not take over the repeat");
    }

    // Slow keys: a key counts once held long enough, at that moment; one
    // let go before has never been pressed.
    #[test]
    fn slow_keys() {
        let t = Timing { slow: Duration::from_millis(500), ..Timing::default() };
        let mut g = Gate::default();
        let got = run(&mut g, &t, &[(K::KEY_A, 1, 0), (K::KEY_A, 0, 300), (K::KEY_B, 1, 400), (K::KEY_B, 0, 1000)], 1100);
        assert_eq!(got.iter().filter(|e| e.1 != 2).copied().collect::<Vec<_>>(), [(900, 1), (1000, 0)]);
        assert!(got.iter().filter(|e| e.1 == 2).all(|e| e.0 >= 900 + 250), "repeats count from the counted press: {got:?}");
    }

    #[test]
    fn the_shells_message() {
        let k = KeyTiming::default();
        assert_eq!(k.now(), Timing::default());
        k.set(&serde_json::json!({"t": "keys", "repeat_delay": 1000, "repeat_rate": 0, "long_press": 1500, "slow": 300, "sticky": true}));
        assert_eq!(k.now(), Timing { delay: Duration::from_millis(1000), period: None, long: Duration::from_millis(1500), slow: Duration::from_millis(300), sticky: true });
        k.set(&serde_json::json!({"t": "keys", "repeat_delay": 1, "long_press": 99999}));
        assert_eq!((k.now().delay, k.now().long), (Duration::from_millis(100), Duration::from_millis(5000)));
    }
}
