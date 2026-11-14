//! The few words the renderer draws on its own (the shell translates the
//! text it sends): as in the Go package i18n, locale/<lang>.json maps the
//! English text to its translation and PHONE_LANG picks the language.

use std::collections::HashMap;
use std::sync::OnceLock;

const DEFAULT_LANG: &str = "zh-Hans";
const CATALOGS: &[(&str, &str)] = &[
    ("zh-Hans", include_str!("../locale/zh-Hans.json")),
    ("zh-Hant", include_str!("../locale/zh-Hant.json")),
    ("es", include_str!("../locale/es.json")),
];
/// Where a message a language lacks comes from before English, the source
/// (the fallback chains of i18n/languages.json).
const FALLBACKS: &[(&str, &[&str])] = &[("zh-Hant", &["zh-Hans"])];

fn catalog() -> &'static HashMap<String, String> {
    static CATALOG: OnceLock<HashMap<String, String>> = OnceLock::new();
    CATALOG.get_or_init(|| {
        let lang = std::env::var("PHONE_LANG").ok().filter(|l| !l.is_empty());
        load(lang.as_deref().unwrap_or(DEFAULT_LANG))
    })
}

fn load(lang: &str) -> HashMap<String, String> {
    load_from(CATALOGS, lang)
}

/// lang's own catalog over those of the languages it falls back to.
fn load_from(catalogs: &[(&str, &str)], lang: &str) -> HashMap<String, String> {
    let fallbacks = FALLBACKS.iter().find(|(l, _)| *l == lang).map_or(&[][..], |(_, f)| *f);
    let mut out = HashMap::new();
    for l in fallbacks.iter().rev().chain(std::iter::once(&lang)) {
        if let Some((_, src)) = catalogs.iter().find(|(c, _)| c == l) {
            let msgs: HashMap<String, String> = serde_json::from_str(src).expect("paint/locale: bad catalog");
            out.extend(msgs);
        }
    }
    out
}

/// The translation of msg, or msg itself when there is none.
pub fn t(msg: &'static str) -> &'static str {
    catalog().get(msg).map(String::as_str).unwrap_or(msg)
}

#[cfg(test)]
mod tests {
    #[test]
    fn catalogs() {
        let zh = super::load("zh-Hans");
        assert_eq!(zh.get("Back").map(String::as_str), Some("\u{8fd4}\u{56de}"));
        assert_eq!(super::load("zh-Hant").get("Symbols").map(String::as_str), Some("\u{7b26}\u{865f}"));
        assert_eq!(super::load("es").get("Back").map(String::as_str), Some("Atr\u{e1}s"));
        assert!(super::load("en").is_empty());
    }

    #[test]
    fn fallbacks() {
        let catalogs = [
            ("zh-Hans", r#"{"Back": "hans back", "Space": "hans space"}"#),
            ("zh-Hant", r#"{"Back": "hant back"}"#),
            ("es", r#"{"Space": "Espacio"}"#),
        ];
        let hant = super::load_from(&catalogs, "zh-Hant");
        assert_eq!(hant.get("Back").map(String::as_str), Some("hant back"));
        assert_eq!(hant.get("Space").map(String::as_str), Some("hans space"));
        let es = super::load_from(&catalogs, "es");
        assert_eq!(es.get("Space").map(String::as_str), Some("Espacio"));
        assert_eq!(es.get("Back"), None);
    }
}
