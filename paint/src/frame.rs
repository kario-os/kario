//! The frame of the renderer protocol (phone.frame/1, 23-renderer-protocol.md §4).
//! Every field is optional on the wire; unknown fields are ignored.

use serde::{Deserialize, Deserializer};

/// Go encodes empty slices as null.
fn nullable<'de, D: Deserializer<'de>, T: Default + Deserialize<'de>>(d: D) -> Result<T, D::Error> {
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub struct Device {
    pub id: String,
    pub keypad: String,
    pub width: u32,
    pub height: u32,
    pub scale: f32,
    /// A full keyboard's Fn layer: the character typed with Fn held, by the
    /// key's own character (sku.json, 24-sku-framework.md).
    #[serde(rename = "fn", deserialize_with = "nullable")]
    pub fn_layer: std::collections::HashMap<String, String>,
    /// How a keyboard without a Ctrl key presses it: "fn+sym", Fn then Sym
    /// (Chr), as on the E71; empty: the Ctrl key.
    pub ctrl: String,
    /// A whole phone's own keypad (sku.json's keymap, 24-sku-framework.md
    /// §7.4): the code its kernel reports for a key -> the code to read it
    /// as. Codes not in it are read as they are.
    #[serde(deserialize_with = "nullable")]
    pub keymap: std::collections::HashMap<u16, u16>,
    /// The board's GPU path when PHONE_PAINT_GPU is not set (sku.json's
    /// paint_gpu, 42-gpu-rendering.md §3): "compose", "raster", else off.
    pub gpu: String,
    /// The kernel driver of the GPU's render node where the display's
    /// device cannot render (sku.json's gpu_render, node.rs).
    pub gpu_render: String,
    /// The phone has a touchscreen (sku.json's touch, 24-sku-framework.md):
    /// the renderer reads it (touch.rs).
    pub touch: bool,
    /// The input device's name of the phone's sound switch (sku.json's
    /// sound_switch): the renderer reports its position (sound_switch.rs).
    pub sound_switch: String,
    /// The input device's name of a flip's lid switch (sku.json's lid):
    /// the renderer reports whether the lid is shut (lid.rs).
    pub lid: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Frame {
    pub screen: String,
    pub status: Status,
    pub title: String,
    pub loading: String,
    /// The app has not sent its first page: the outline of one is drawn
    /// under the title instead of `loading`'s words (37-motion.md §3).
    pub skeleton: bool,
    #[serde(deserialize_with = "nullable")]
    pub body: Vec<Node>,
    pub focus: i32,
    pub scroll: Scroll,
    pub soft: [String; 3],
    #[serde(deserialize_with = "nullable")]
    pub ime: Vec<ImeRow>,
    /// The keypad drawn on screen of a phone with a touchscreen and no keys
    /// that type (23-renderer-protocol.md §4), above the soft keys.
    pub keypad: Option<Keypad>,
    pub toast: String,
    #[serde(deserialize_with = "nullable")]
    pub banner: Vec<String>,
    pub overlay: Option<Overlay>,
    pub boot: Option<Boot>,
    pub idle: Option<Idle>,
    /// A test pattern on the whole panel in place of everything else
    /// (factory mode's display and touch checks; feature "pattern").
    pub pattern: Option<Pattern>,
    /// Where content shorter than the body sits: "", "center", "bottom".
    pub layout: String,
    /// Settings › Theme: "" (dark), "light", "retro", "amber".
    pub theme: String,
    /// Settings › Large text: everything drawn this much bigger, 0 for the normal size.
    pub zoom: f32,
    /// How this frame follows the one before (37-motion.md): a page pushed
    /// or popped, a menu or dialog rising or sinking; `n` tells one from the
    /// next (a patch can keep the member).
    pub transition: Option<Transition>,
    /// A key that moved neither the focus nor the scroll (the end of a
    /// list): which way (37-motion.md §3); `n` tells one from the next.
    pub bump: Option<Bump>,
    /// Settings › Animations: "" (standard), "reduced" (no transitions),
    /// "off" (nothing moves).
    pub motion: String,
    /// Settings › Accessibility: text in black or white, all text bold,
    /// the colours inverted and corrected ("deuteranomaly",
    /// "protanomaly", "tritanomaly"), the screen zoomed this much around
    /// the focus (0: not).
    pub contrast: bool,
    pub bold: bool,
    pub invert: bool,
    pub daltonize: String,
    pub magnify: f32,
}

#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub struct Transition {
    pub kind: String,
    pub n: u64,
}

#[derive(Deserialize, Default, Clone)]
#[serde(default)]
pub struct Bump {
    pub dx: i32,
    pub dy: i32,
    pub n: u64,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Status {
    pub time: String,
    pub mid: String,
    pub mid_kind: String,
    pub ime: String,
    pub net: String,
    pub signal: i32,
    /// Calls go over VoLTE: a mark left of the network type, where it fits.
    pub volte: bool,
    pub battery: i32,
    /// On external power: the battery fills up again and again (37-motion.md §3).
    pub charging: bool,
    pub locked: bool,
    pub percent: bool,
    pub location: bool,
    pub vpn: bool,
    pub hotspot: bool,
    pub speed: String,
    /// The sound mode when it is not ring: "vibrate", "silent".
    pub sound: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Scroll {
    pub pos: f32,
    pub frac: f32,
    /// A finger dragged the body this far down its content (logical
    /// pixels): it shows so, wherever the focus is.
    pub offset: Option<f32>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Node {
    pub kind: String,
    pub style: String,
    pub text: String,
    pub title: String,
    pub subtitle: String,
    pub trailing: String,
    pub badge: String,
    pub icon: String,
    pub icon_image: String,
    pub label: String,
    pub placeholder: String,
    pub value: String,
    pub preedit: String,
    /// The caret is this many characters before the end of value.
    pub caret_back: usize,
    /// The characters [from, to) of value are selected.
    #[serde(deserialize_with = "nullable")]
    pub selection: Vec<usize>,
    pub secret: bool,
    pub out: bool,
    pub focused: bool,
    pub image: Option<Image>,
    pub pip: Option<Image>,
    pub waiting: String,
    /// A media node filling the body (a web page).
    pub fill: bool,
    #[serde(deserialize_with = "nullable")]
    pub doc: Vec<Vec<Run>>,
    pub columns: i32,
    #[serde(deserialize_with = "nullable")]
    pub tiles: Vec<Tile>,
    pub sel: i32,
    pub fraction: f32,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Tile {
    pub icon: String,
    pub image: String,
    pub pixels: Option<Image>,
    pub title: String,
    /// A key-like tile's second line, under the title (a calendar day's lunar date).
    pub subtitle: String,
    pub badge: String,
    pub focused: bool,
    /// A photo grid's cell: the picture fills it, no label.
    pub photo: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Image {
    pub w: u32,
    pub h: u32,
    pub rgb: String,
    /// Shared memory holding the pixels instead of rgb (feature "shm").
    pub surface: String,
    pub seq: u32,
    pub secure: bool,
    pub pixelated: bool,
    /// A code the system draws ("qr", "bar"): its pixels are the modules,
    /// quiet zones included (`Canvas::code_box`); code_note, and code_text
    /// under it, show instead when one panel pixel a module does not fit.
    pub code: String,
    pub code_note: String,
    pub code_text: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Run {
    pub text: String,
    pub col: i32,
    pub width: i32,
    #[serde(rename = "box")]
    pub boxed: bool,
    pub fg: String,
    pub bg: String,
    pub bold: bool,
    pub italic: bool,
    pub under: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct ImeRow {
    #[serde(deserialize_with = "nullable")]
    pub items: Vec<String>,
    pub sel: i32,
    pub active: bool,
    pub numbered: bool,
    pub more: bool,
}

/// The on-screen keypad's rows, top first, each row's keys sharing its
/// width: what a key presses is the Shell's; a touch on it says its id.
#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Keypad {
    #[serde(deserialize_with = "nullable")]
    pub rows: Vec<Vec<KeypadKey>>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct KeypadKey {
    pub id: String,
    pub label: String,
    /// The smaller line under the label: a 9-key pad's letters.
    pub sub: String,
    /// "ok": OK's key, drawn in the accent; "fn": a key that edits or
    /// moves (backspace, the arrows), drawn apart from the ones that type.
    pub style: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Overlay {
    pub kind: String,
    pub title: String,
    #[serde(deserialize_with = "nullable")]
    pub lines: Vec<String>,
    pub warn: String,
    #[serde(deserialize_with = "nullable")]
    pub items: Vec<MenuItem>,
    pub query: Option<String>,
    #[serde(deserialize_with = "nullable")]
    pub choices: Vec<String>,
    pub choice: i32,
    pub pin: [i32; 2],
    /// A code the user types on another device, drawn large, its first
    /// `pin[0]` characters marked typed (feature "digits").
    pub digits: String,
    pub note: String,
    pub note_bad: bool,
    #[serde(deserialize_with = "nullable")]
    pub footer: Vec<String>,
    pub image: Option<Image>,
    pub phase: f32,
    /// With a period (ms), the phase runs by itself from `phase`, a cycle
    /// each period, from when the overlay came (feature "phase").
    pub period: u32,
    #[serde(deserialize_with = "nullable")]
    pub tiles: Vec<Tile>,
    pub columns: i32,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct MenuItem {
    pub header: bool,
    pub text: String,
    pub key: String,
    pub sel: bool,
}

/// `color` everywhere; with `grid` (columns, rows) the panel's cells
/// outlined in `line`, those in `done` (row-major) filled with `mark`.
#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Pattern {
    pub color: String,
    pub grid: [u32; 2],
    pub line: String,
    #[serde(deserialize_with = "nullable")]
    pub done: Vec<u32>,
    pub mark: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Boot {
    pub progress: f32,
    pub brand: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Idle {
    pub time: String,
    pub date: String,
    pub operator: String,
    #[serde(deserialize_with = "nullable")]
    pub lines: Vec<String>,
    pub hint: String,
    /// The standby wallpaper, the screen's size (a shared-memory surface).
    pub wallpaper: Option<Image>,
}

/// Applies a JSON merge patch (RFC 7386, the "patch" message) to the last
/// frame: null removes a member, objects merge member by member, anything
/// else replaces; {"$set": {"<index>": patch}} patches an array in place.
pub fn merge(target: &mut serde_json::Value, patch: &serde_json::Value) {
    let serde_json::Value::Object(p) = patch else {
        *target = patch.clone();
        return;
    };
    // Our one extension: an array patched element by element.
    if let (Some(serde_json::Value::Object(set)), serde_json::Value::Array(arr)) = (p.get("$set"), &mut *target) {
        for (i, v) in set {
            if let Some(e) = i.parse::<usize>().ok().and_then(|i| arr.get_mut(i)) {
                merge(e, v);
            }
        }
        return;
    }
    if !target.is_object() {
        *target = serde_json::Value::Object(Default::default());
    }
    let t = target.as_object_mut().expect("object");
    for (k, v) in p {
        if v.is_null() {
            t.remove(k);
        } else {
            merge(t.entry(k.clone()).or_insert(serde_json::Value::Null), v);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{merge, Device};
    use serde_json::json;

    #[test]
    fn the_shells_keymap() {
        let d: Device = serde_json::from_value(json!({"id": "bar-t9", "keypad": "t9", "keymap": {"139": 59, "2": 513}})).unwrap();
        assert_eq!((d.keymap.get(&139), d.keymap.get(&2), d.keymap.len()), (Some(&59), Some(&513), 2));
        let d: Device = serde_json::from_value(json!({"id": "bar-t9", "keymap": null})).unwrap();
        assert!(d.keymap.is_empty());
    }

    #[test]
    fn merge_patch() {
        let mut doc = json!({"status": {"time": "10:00", "bat": 80}, "body": [1], "toast": "x"});
        merge(&mut doc, &json!({"status": {"time": "10:01"}, "toast": null, "body": [1, 2], "idle": {"time": "10:01"}}));
        assert_eq!(doc, json!({"status": {"time": "10:01", "bat": 80}, "body": [1, 2], "idle": {"time": "10:01"}}));
        let mut doc = json!({"body": [{"t": "a", "focused": true}, {"t": "b"}], "soft": ["Options", "", "Back"]});
        merge(&mut doc, &json!({"body": {"$set": {"0": {"focused": null}, "1": {"focused": true}}}, "soft": {"$set": {"1": "Open"}}}));
        assert_eq!(doc, json!({"body": [{"t": "a"}, {"t": "b", "focused": true}], "soft": ["Options", "Open", "Back"]}));
    }
}
