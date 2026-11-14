//! phone-paint's design system: the palettes of the four themes, the type
//! scale, spacing, radii and motion. docs/26-design-system.md is the
//! same table in words; the tests below hold the two together and check
//! the contrast rules on every theme.
//!
//! Units are the canvas's logical pixels (a 480×640 panel at scale 2 is
//! 240×320 of them).

use crate::canvas::Rgb;

#[derive(Clone, Copy)]
pub struct Palette {
    pub bg: Rgb,
    pub fg: Rgb,
    /// Secondary text: subtitles, hints, metadata.
    pub dim: Rgb,
    pub accent: Rgb,
    pub ok: Rgb,
    pub warn: Rgb,
    pub danger: Rgb,
    /// The system's own trusted surfaces (secure confirmations).
    pub trust: Rgb,
    pub bar: Rgb,
    pub title: Rgb,
    pub focus: Rgb,
    pub card: Rgb,
    /// Raised surfaces over the page: menus, the command palette, dialogs,
    /// notification banners.
    pub sheet: Rgb,
    pub line: Rgb,
    pub bub_in: Rgb,
    pub bub_out: Rgb,
    /// The standby screen's gradient, top to bottom.
    pub idle: (Rgb, Rgb),
}

/// The themes of Settings › Theme (Frame.theme); dark is the default.
pub const DARK: Palette = Palette {
    bg: Rgb(0x10, 0x12, 0x18),
    fg: Rgb(0xe6, 0xe6, 0xea),
    dim: Rgb(0x8a, 0x8d, 0x99),
    accent: Rgb(0x4c, 0xa3, 0xff),
    ok: Rgb(0x5c, 0xc8, 0x7a),
    warn: Rgb(0xf0, 0xa8, 0x3c),
    danger: Rgb(0xef, 0x5a, 0x5a),
    trust: Rgb(0xf2, 0xc9, 0x4c),
    bar: Rgb(0x07, 0x08, 0x0c),
    title: Rgb(0x1b, 0x1e, 0x2a),
    focus: Rgb(0x1f, 0x33, 0x52),
    card: Rgb(0x1a, 0x1c, 0x25),
    sheet: Rgb(0x20, 0x23, 0x30),
    line: Rgb(0x2c, 0x2f, 0x3b),
    bub_in: Rgb(0x2c, 0x2f, 0x3a),
    bub_out: Rgb(0x24, 0x5a, 0x3a),
    idle: (Rgb(0x1c, 0x2a, 0x4a), Rgb(0x2a, 0x1c, 0x3a)),
};

pub const LIGHT: Palette = Palette {
    bg: Rgb(0xf4, 0xf5, 0xf7),
    fg: Rgb(0x1d, 0x21, 0x29),
    dim: Rgb(0x5f, 0x66, 0x75),
    accent: Rgb(0x1f, 0x6f, 0xeb),
    ok: Rgb(0x1a, 0x8f, 0x4a),
    warn: Rgb(0xc2, 0x6a, 0x00),
    danger: Rgb(0xd0, 0x3a, 0x3a),
    trust: Rgb(0xa8, 0x76, 0x00),
    bar: Rgb(0xe4, 0xe6, 0xeb),
    title: Rgb(0xff, 0xff, 0xff),
    focus: Rgb(0xd6, 0xe6, 0xff),
    card: Rgb(0xff, 0xff, 0xff),
    sheet: Rgb(0xff, 0xff, 0xff),
    line: Rgb(0xd5, 0xd9, 0xe0),
    bub_in: Rgb(0xe4, 0xe7, 0xec),
    bub_out: Rgb(0xcf, 0xec, 0xd6),
    idle: (Rgb(0xdc, 0xe8, 0xfa), Rgb(0xf6, 0xe9, 0xf2)),
};

/// A monochrome LCD's green, for the retro line.
pub const RETRO: Palette = Palette {
    bg: Rgb(0xb4, 0xc6, 0x9c),
    fg: Rgb(0x1c, 0x28, 0x14),
    dim: Rgb(0x3a, 0x48, 0x30),
    accent: Rgb(0x24, 0x38, 0x18),
    ok: Rgb(0x24, 0x38, 0x18),
    warn: Rgb(0x5a, 0x40, 0x10),
    danger: Rgb(0x6a, 0x20, 0x10),
    trust: Rgb(0x3a, 0x30, 0x08),
    bar: Rgb(0xa2, 0xb6, 0x88),
    title: Rgb(0xa8, 0xbb, 0x8e),
    focus: Rgb(0x92, 0xa8, 0x76),
    card: Rgb(0xa9, 0xbc, 0x90),
    sheet: Rgb(0xa9, 0xbc, 0x90),
    line: Rgb(0x8a, 0x9e, 0x70),
    bub_in: Rgb(0xa9, 0xbc, 0x90),
    bub_out: Rgb(0x92, 0xa8, 0x76),
    idle: (Rgb(0xb8, 0xca, 0xa0), Rgb(0xa6, 0xba, 0x8c)),
};

pub const AMBER: Palette = Palette {
    bg: Rgb(0x15, 0x12, 0x0c),
    fg: Rgb(0xf3, 0xe6, 0xc8),
    dim: Rgb(0xa8, 0x9a, 0x7c),
    accent: Rgb(0xff, 0xb0, 0x00),
    ok: Rgb(0x9c, 0xc8, 0x5a),
    warn: Rgb(0xff, 0x8a, 0x3c),
    danger: Rgb(0xef, 0x5a, 0x4a),
    trust: Rgb(0xff, 0xd0, 0x50),
    bar: Rgb(0x0c, 0x0a, 0x06),
    title: Rgb(0x24, 0x1e, 0x13),
    focus: Rgb(0x3e, 0x30, 0x12),
    card: Rgb(0x22, 0x1d, 0x14),
    sheet: Rgb(0x22, 0x1d, 0x14),
    line: Rgb(0x3b, 0x33, 0x26),
    bub_in: Rgb(0x2a, 0x24, 0x18),
    bub_out: Rgb(0x46, 0x34, 0x10),
    idle: (Rgb(0x2a, 0x1e, 0x0c), Rgb(0x16, 0x10, 0x08)),
};

/// The incoming call and the alarm keep their own dark colours whatever the
/// theme (they must be recognised at a glance); their text is these.
pub const ON_DARK: Rgb = Rgb(0xec, 0xee, 0xf4);
pub const ON_DARK_DIM: Rgb = Rgb(0xa0, 0xa6, 0xb8);
pub const CALL_BG: Rgb = Rgb(0x10, 0x32, 0x22);
pub const ALARM_BG: Rgb = Rgb(0x3a, 0x24, 0x10);

thread_local! {
    static PAL: std::cell::Cell<Palette> = const { std::cell::Cell::new(DARK) };
}

pub fn pal() -> Palette {
    PAL.with(|p| p.get())
}

pub fn theme(name: &str) -> Palette {
    match name {
        "light" => LIGHT,
        "retro" => RETRO,
        "amber" => AMBER,
        _ => DARK,
    }
}

/// Every frame names its theme; unknown names are the default.
pub fn set_theme(name: &str) {
    PAL.with(|p| p.set(theme(name)));
}

// The type scale. Every text is one of these sizes.
/// Badges, message times.
pub const MICRO: f32 = 8.5;
/// The status bar, subtitles, trailing values, section headers, hints.
pub const CAPTION: f32 = 9.5;
/// Banners, toasts, the standby lines, secondary paragraphs.
pub const SMALL: f32 = 10.5;
/// Body text, soft keys, buttons, menu items.
pub const BODY: f32 = 11.0;
/// Title bars, headings, the operator and date on standby.
pub const TITLE: f32 = 12.5;
/// The caller's name on the incoming call, the alarm's time.
pub const HEADLINE: f32 = 18.0;
/// Text nodes styled "big".
pub const LARGE: f32 = 22.0;
/// The name on the boot screen.
pub const BRAND: f32 = 26.0;
/// The standby clock; also the most a "display" text grows to.
pub const DISPLAY: f32 = 40.0;
/// The least a "display" text shrinks to before it is cut.
pub const DISPLAY_MIN: f32 = 16.0;

// Bars and spacing.
pub const STATUS_H: f32 = 16.0;
pub const TITLE_H: f32 = 22.0;
pub const SOFT_H: f32 = 20.0;
pub const IME_H: f32 = 18.0;
/// The margin at the screen's edges and inside cards.
pub const PAD: f32 = 6.0;
/// Between the body's nodes.
pub const GAP: f32 = 3.0;

// Corner radii; pills (badges, toasts, buttons in a row, progress bars) use
// half their height.
/// Tiles, fields, candidates, small highlights.
pub const R_S: f32 = 4.0;
/// Rows, cards, the standby lines.
pub const R_M: f32 = 6.0;
/// Bubbles, sheets, dialogs.
pub const R_L: f32 = 8.0;

// Motion: ease-out cubic.
pub const SCROLL_MS: f32 = 140.0;
pub const FOCUS_MS: f32 = 120.0;

#[cfg(test)]
mod tests {
    use super::*;

    fn lum(c: Rgb) -> f64 {
        let ch = |v: u8| {
            let v = v as f64 / 255.0;
            if v <= 0.03928 { v / 12.92 } else { ((v + 0.055) / 1.055).powf(2.4) }
        };
        0.2126 * ch(c.0) + 0.7152 * ch(c.1) + 0.0722 * ch(c.2)
    }

    /// WCAG 2 contrast ratio.
    fn contrast(a: Rgb, b: Rgb) -> f64 {
        let (x, y) = (lum(a), lum(b));
        (x.max(y) + 0.05) / (x.min(y) + 0.05)
    }

    const THEMES: [(&str, Palette); 4] = [("dark", DARK), ("light", LIGHT), ("retro", RETRO), ("amber", AMBER)];

    #[test]
    fn contrast_rules() {
        let mut bad = Vec::new();
        let mut need = |what: String, a: Rgb, b: Rgb, min: f64| {
            let r = contrast(a, b);
            if r < min {
                bad.push(format!("{what}: {r:.2} < {min}"));
            }
        };
        for (name, p) in THEMES {
            need(format!("{name} fg/bg"), p.fg, p.bg, 7.0);
            for (s, c) in [("bar", p.bar), ("title", p.title), ("focus", p.focus), ("card", p.card), ("sheet", p.sheet), ("bub_in", p.bub_in), ("bub_out", p.bub_out)] {
                need(format!("{name} fg/{s}"), p.fg, c, 4.5);
            }
            for (s, c) in [("bg", p.bg), ("card", p.card), ("sheet", p.sheet)] {
                need(format!("{name} dim/{s}"), p.dim, c, 4.5);
            }
            for (s, c) in [("accent", p.accent), ("ok", p.ok), ("warn", p.warn), ("danger", p.danger), ("trust", p.trust)] {
                need(format!("{name} {s}/bg"), c, p.bg, 3.0);
            }
            need(format!("{name} bg/accent"), p.bg, p.accent, 3.0);
        }
        for (s, c) in [("call", CALL_BG), ("alarm", ALARM_BG)] {
            need(format!("on-dark/{s}"), ON_DARK, c, 7.0);
            need(format!("on-dark-dim/{s}"), ON_DARK_DIM, c, 4.5);
        }
        assert!(bad.is_empty(), "contrast below the rule:\n{}", bad.join("\n"));
    }

    fn hex(c: Rgb) -> String {
        format!("#{:02x}{:02x}{:02x}", c.0, c.1, c.2)
    }

    fn roles(p: &Palette) -> Vec<(&'static str, Rgb)> {
        vec![
            ("bg", p.bg), ("fg", p.fg), ("dim", p.dim), ("accent", p.accent), ("ok", p.ok), ("warn", p.warn),
            ("danger", p.danger), ("trust", p.trust), ("bar", p.bar), ("title", p.title), ("focus", p.focus),
            ("card", p.card), ("sheet", p.sheet), ("line", p.line), ("bub_in", p.bub_in), ("bub_out", p.bub_out),
            ("idle_top", p.idle.0), ("idle_bottom", p.idle.1),
        ]
    }

    /// The document's tables, row by row: the first cell in backticks, the
    /// rest as written.
    fn table_rows(doc: &str, heading: &str) -> Vec<Vec<String>> {
        let Some(at) = doc.find(heading) else { panic!("no section {heading:?} in the design document") };
        doc[at..]
            .lines()
            .skip(1)
            .skip_while(|l| !l.starts_with('|'))
            .take_while(|l| l.starts_with('|'))
            .skip(2)
            .map(|l| l.trim_matches('|').split('|').map(|c| c.trim().trim_matches('`').to_string()).collect())
            .collect()
    }

    fn doc() -> String {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../docs/26-design-system.md");
        std::fs::read_to_string(path).unwrap_or_else(|e| panic!("{path}: {e}"))
    }

    #[test]
    fn document_has_the_palettes() {
        let rows = table_rows(&doc(), "## 2.");
        let mut want: Vec<Vec<String>> = Vec::new();
        let all: Vec<Vec<(&str, Rgb)>> = THEMES.iter().map(|(_, p)| roles(p)).collect();
        for (i, (role, _)) in all[0].iter().enumerate() {
            let mut row = vec![role.to_string()];
            row.extend(all.iter().map(|r| hex(r[i].1)));
            want.push(row);
        }
        let got: Vec<Vec<String>> = rows.iter().map(|r| r.iter().take(5).cloned().collect()).collect();
        assert_eq!(got, want, "26-design-system.md §2 differs from design.rs");
    }

    #[test]
    fn document_has_the_scale() {
        let rows = table_rows(&doc(), "## 3.");
        let want = [
            ("MICRO", MICRO), ("CAPTION", CAPTION), ("SMALL", SMALL), ("BODY", BODY), ("TITLE", TITLE),
            ("HEADLINE", HEADLINE), ("LARGE", LARGE), ("BRAND", BRAND), ("DISPLAY", DISPLAY), ("DISPLAY_MIN", DISPLAY_MIN),
        ];
        let got: Vec<(String, String)> = rows.iter().map(|r| (r[0].clone(), r[1].clone())).collect();
        let want: Vec<(String, String)> = want.iter().map(|(n, v)| (n.to_string(), format!("{v}"))).collect();
        assert_eq!(got, want, "26-design-system.md §3 differs from design.rs");
    }

    #[test]
    fn document_has_the_spacing() {
        let rows = table_rows(&doc(), "## 4.");
        let want = [
            ("STATUS_H", STATUS_H), ("TITLE_H", TITLE_H), ("SOFT_H", SOFT_H), ("IME_H", IME_H), ("PAD", PAD), ("GAP", GAP),
            ("R_S", R_S), ("R_M", R_M), ("R_L", R_L), ("SCROLL_MS", SCROLL_MS), ("FOCUS_MS", FOCUS_MS),
        ];
        let got: Vec<(String, String)> = rows.iter().map(|r| (r[0].clone(), r[1].clone())).collect();
        let want: Vec<(String, String)> = want.iter().map(|(n, v)| (n.to_string(), format!("{v}"))).collect();
        assert_eq!(got, want, "26-design-system.md §4 differs from design.rs");
    }
}
