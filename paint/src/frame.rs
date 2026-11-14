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
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Frame {
    pub screen: String,
    pub status: Status,
    pub title: String,
    pub loading: String,
    #[serde(deserialize_with = "nullable")]
    pub body: Vec<Node>,
    pub focus: i32,
    pub scroll: Scroll,
    pub soft: [String; 3],
    #[serde(deserialize_with = "nullable")]
    pub ime: Vec<ImeRow>,
    pub toast: String,
    #[serde(deserialize_with = "nullable")]
    pub banner: Vec<String>,
    pub overlay: Option<Overlay>,
    pub boot: Option<Boot>,
    pub idle: Option<Idle>,
    /// Where content shorter than the body sits: "", "center", "bottom".
    pub layout: String,
    /// Settings › Theme: "" (dark), "light", "retro", "amber".
    pub theme: String,
    /// Settings › Large text: everything drawn this much bigger, 0 for the normal size.
    pub zoom: f32,
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
    pub battery: i32,
    pub locked: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct Scroll {
    pub pos: f32,
    pub frac: f32,
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
    pub note: String,
    pub note_bad: bool,
    #[serde(deserialize_with = "nullable")]
    pub footer: Vec<String>,
    pub image: Option<Image>,
    pub phase: f32,
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
    use super::merge;
    use serde_json::json;

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
