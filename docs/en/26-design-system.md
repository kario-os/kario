> English translation of [the Chinese original](../26-design-system.md). The Chinese text is authoritative; keep both in sync.

# phone-paint design system

> The finalized look of the product renderer phone-paint: the colors, font sizes, spacing, corner radii, animation and contrast rules of the four themes. The same table lives in code in `paint/src/design.rs`; `cargo test` (part of `make test`) checks row by row that the tables in §2–§4 of the Chinese original match the code, and checks the §5 contrast rules on all four themes; when changing colors, font sizes or spacing, change both sides together. phone-gui has its own styling and is not bound by this document ([23](23-renderer-protocol.md) §10).

## 1. Principles

- **Flat, high contrast, grid-aligned**: no shadows or gradients (except on the idle screen); hierarchy comes from the shade of the background and a thin border line.
- **A screen uses only the colors and font sizes defined here**: every piece of text on screen is one of the §3 levels, and every background is one of the §2 roles; for a new screen, look here first, and if nothing fits, add a row here — never write values in code.
- **Themes change only colors, not layout**: the four themes share the same font sizes, spacing and corner radii, so switching themes never makes text rewrap or the screen jump.
- **Screens that are always dark**: the incoming-call and alarm screens do not follow the theme (they must be recognizable at a glance); their backgrounds are `CALL_BG` / `ALARM_BG`, and the text on them uses `ON_DARK` / `ON_DARK_DIM`.
- **Text over photos is always white**: the idle wallpaper is overlaid with a black layer that fades from top to bottom (opacity 0.57 to 0.12), and the status line sits on a semi-transparent black plate.
- Units are logical pixels of the canvas: on a 480×640 screen at scale 2 that is 240×320.

## 2. Color palette

| Role | Dark (default) | Light | Retro green | Amber | Usage |
|------|------|------|------|------|------|
| `bg` | `#101218` | `#f4f5f7` | `#b4c69c` | `#15120c` | Page background |
| `fg` | `#e6e6ea` | `#1d2129` | `#1c2814` | `#f3e6c8` | Body text |
| `dim` | `#8a8d99` | `#5f6675` | `#3a4830` | `#a89a7c` | Secondary text: subtitles, hints, times |
| `accent` | `#4ca3ff` | `#1f6feb` | `#243818` | `#ffb000` | Focus bar, selected candidate, progress, links |
| `ok` | `#5cc87a` | `#1a8f4a` | `#243818` | `#9cc85a` | Success, answer |
| `warn` | `#f0a83c` | `#c26a00` | `#5a4010` | `#ff8a3c` | Alerts |
| `danger` | `#ef5a5a` | `#d03a3a` | `#6a2010` | `#ef5a4a` | Errors, hang up, unread badges |
| `trust` | `#f2c94c` | `#a87600` | `#3a3008` | `#ffd050` | The system's own trusted UI (border and title of secure confirmations) |
| `bar` | `#07080c` | `#e4e6eb` | `#a2b688` | `#0c0a06` | Status bar, soft-key bar |
| `title` | `#1b1e2a` | `#ffffff` | `#a8bb8e` | `#241e13` | Title bar |
| `focus` | `#1f3352` | `#d6e6ff` | `#92a876` | `#3e3012` | The row or cell that has focus |
| `card` | `#1a1c25` | `#ffffff` | `#a9bc90` | `#221d14` | Cards, cells, input fields, IME candidate row |
| `sheet` | `#202330` | `#ffffff` | `#a9bc90` | `#221d14` | Surfaces floating over the page: options menu, command palette, dialogs, notification banners |
| `line` | `#2c2f3b` | `#d5d9e0` | `#8a9e70` | `#3b3326` | Borders, dividers, progress bar track |
| `bub_in` | `#2c2f3a` | `#e4e7ec` | `#a9bc90` | `#2a2418` | Received message bubble |
| `bub_out` | `#245a3a` | `#cfecd6` | `#92a876` | `#463410` | Sent message bubble |
| `idle_top` | `#1c2a4a` | `#dce8fa` | `#b8caa0` | `#2a1e0c` | Idle-screen gradient when there is no wallpaper (top) |
| `idle_bottom` | `#2a1c3a` | `#f6e9f2` | `#a6ba8c` | `#161008` | Same as above (bottom) |

Fixed colors: `CALL_BG` `#103222` (incoming call), `ALARM_BG` `#3a2410` (alarm), text `ON_DARK` `#eceef4`, `ON_DARK_DIM` `#a0a6b8`. Toasts are inverted: the background is `fg` and the text is `bg`, so they stand out on any page. App icon backgrounds are picked by name from `accent`, `ok`, `warn`, `danger` and two fixed colors (purple `#b07cff`, cyan `#3cc8c8`); the text on icons and on badges is white.

## 3. Font sizes

| Level | Size | Usage |
|------|------|------|
| `MICRO` | 8.5 | Badges, message times |
| `CAPTION` | 9.5 | Status bar, subtitles, values at the end of rows, section headers, action hints on the idle screen, dialog footnotes |
| `SMALL` | 10.5 | Notification banners, toasts, information lines on the idle screen, paragraphs in `dim` style, dialog descriptions |
| `BODY` | 11 | Body text, soft keys, buttons, menu items, input fields |
| `TITLE` | 12.5 | Title bar, `title` style, operator and date on the idle screen |
| `HEADLINE` | 18 | Caller name on the incoming-call screen, alarm time |
| `LARGE` | 22 | `big` style |
| `BRAND` | 26 | Name on the boot screen |
| `DISPLAY` | 40 | Clock on the idle screen; the largest size of the `display` style (calculator result) |
| `DISPLAY_MIN` | 16 | The smallest size the `display` style shrinks to; anything longer is truncated |

The fonts are Noto Sans CJK regular and bold (`Face::Sans`, `Face::Bold`); monospaced content (code, Mermaid diagrams) uses the monospaced glyphs of the same family. Bold is used only for titles, the confirm key in the middle of the soft-key bar, buttons, badges and senders.

## 4. Spacing, corner radii, animation

| Name | Value | Usage |
|------|------|------|
| `STATUS_H` | 16 | Status bar height |
| `TITLE_H` | 22 | Title bar height |
| `SOFT_H` | 20 | Soft-key bar height |
| `IME_H` | 18 | IME candidate row height |
| `PAD` | 6 | Screen margin, card padding |
| `GAP` | 3 | Gap between adjacent nodes in body content |
| `R_S` | 4 | Corner radius of cells, input fields, candidate highlight, small blocks |
| `R_M` | 6 | Corner radius of rows, cards, idle-screen information lines |
| `R_L` | 8 | Corner radius of bubbles, overlays, dialogs |
| `SCROLL_MS` | 140 | Scroll easing (cubic ease-out) |
| `FOCUS_MS` | 120 | Focus fade |

Pill shapes (badges, toasts, a row of buttons, progress bars) have a corner radius of half their own height.

## 5. Contrast rules

Computed with the WCAG 2 contrast ratio; the tests in `design.rs` check these on all four themes:

| Text | Background | At least |
|------|------|------|
| `fg` | `bg` | 7:1 |
| `fg` | `bar`, `title`, `focus`, `card`, `sheet`, `bub_in`, `bub_out` | 4.5:1 |
| `dim` | `bg`, `card`, `sheet` | 4.5:1 |
| `accent`, `ok`, `warn`, `danger`, `trust` (only for bold text, graphics and color blocks) | `bg` | 3:1 |
| `bg` (text on the selected candidate) | `accent` | 3:1 |
| `ON_DARK` / `ON_DARK_DIM` | `CALL_BG`, `ALARM_BG` | 7:1 / 4.5:1 |

Two things were adjusted when this version was finalized: `dim` in the light and retro-green themes was originally only 4.4:1 and 3.5:1, and was darkened to above 4.5:1; chat bubbles, the options menu, the command palette, dialogs and notification banners used to be hard-coded dark colors, so in the light and retro-green themes dark text sat on a dark background and was hard to read; they now all take the theme's `bub_in` / `bub_out` / `sheet`.

## 6. Review

- Screenshots: `make pixel-selftest` draws every selftest step as a PNG (`build/pixel/<形态>/`, where `<形态>` is the form factor), and `make skins` additionally saves a copy placed in a phone body (`build/skins/<形态>/`); review the look from these.
- Large text: when 「设置 › 大字号」 (Settings › Large text) is turned on, the frame carries `zoom: 1.2`, and phone-paint draws the whole frame at a 1.2× scale; text, line height, margins, the status bar and the bottom bar all grow together, the proportions and line-breaking rules stay the same, and fewer lines fit on screen; the layout report changes accordingly, and the Shell's scrolling and visible range adjust with it (like the "display size" setting on phones, rather than enlarging only the font and squeezing it into the original line height). The case 「大字号」 (Large text) captures the Settings home page and the idle screen (`91-large-text-*.png`). The character grid of the text UI does not change; phone-gui is only used for comparison and does not handle `zoom`.
- Four themes: the selftest takes its screenshots in the default theme; the case 「四套主题的截图」 (Screenshots of the four themes) switches the theme in Settings to light, retro green, amber and dark in turn, and for each captures the idle screen, the main menu and the Settings home page (`90-theme-<主题>-<界面>.png`, where `<主题>` is the theme and `<界面>` the screen).

## 7. Not implemented

| Item | Notes |
|------|------|
| GPU rendering | Needs real hardware (R21) |
