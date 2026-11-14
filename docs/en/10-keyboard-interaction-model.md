> English translation of [the Chinese original](../10-keyboard-interaction-model.md). The Chinese text is authoritative; keep both in sync.

# Keyboard interaction grammar and the semantic UI model

> Answers the question "can keys handle interfaces like Bilibili and WeChat, with entry points everywhere and yet more menus on long-press?"
> Status: design draft; the rules in §3 and `phone.Ui` v0 in §4 are implemented in the prototype ([13](13-prototype.md)), the rest is **not implemented**. Why these choices were made: [09](09-ui-feasibility-review.md); IPC: [03](03-protocols-and-ui-path.md); input method: [16](16-input-method.md).

## 1. Physical keys (target body: QWERTY candybar)

| Key | Function |
|----|------|
| D-pad / trackpad | Move focus; two-dimensional movement in grids |
| OK (center key) | The **primary action** of the focused element |
| Menu key (or long-press OK) | The **context menu** of the focused element (= touchscreen long-press) |
| Left soft key 「选项」 (Options) | Page menu: focus actions + page actions + global actions |
| Right soft key | The page's create / main operation (post a comment, write an SMS); the label changes with the page |
| Back | Go back one level, keeping state (list position, drafts) |
| Call / End | Global: dial, answer; End also doubles as "back to the idle screen"; on boards without a separate power key End is the power key too (the Nokia way): held, it opens the power menu; pressed twice on the idle or lock screen, it opens the quick card view ([18](18-nokia-form-factors.md) §5.1) |
| Power key (`power`, where the board has a separate one) | A press turns the screen off or on; two presses open the quick card view (on any screen, locked too, [30](30-nfc.md) §5); held, it opens the power menu (power off, restart) |
| QWERTY letters | Outside an input field: shortcuts, or type-to-search; inside an input field: handed to the system input method |
| Chinese / English toggle | QWERTY keyboard: Ctrl+Space (the Nokia E-series habit; in the prototype terminal F6 also works); T9 keypad: `#` |
| Chr / Sym | Open the system symbol table (`*` on the T9 keypad; F7 in the prototype terminal) |
| `/` (or a dedicated key) | Command palette: fuzzy search over all actions on the page + global commands |
| Side keys | Volume; also zoom when viewing pictures / video |

When the body has no separate menu key, "long-press OK" replaces it (that is what the Bilibili TV edition does).

**Home screen grid**: the home screen is a 3-column app grid with two-dimensional D-pad movement; pressing number keys 1–9 opens the app at the matching position directly (the way Nokia's menu grid worked), and the calculator's keys form a grid too.

**T9 keypad body**: in an input field, number keys 2–9 type pinyin or English (T9), 0 commits, 1 separates syllables or opens punctuation, and `#` switches between Chinese / English / 123; outside an input field, number keys work like the QWERTY letters, for shortcuts or type-to-search. The prototype simulates it with `phone-shell --device bar-t9` (or `--keypad t9`); key details are in [16](16-input-method.md) §2, and a key-by-key comparison with each generation of Nokia models is in [18](18-nokia-form-factors.md).

## 2. Touchscreen gestures → keys

| Touch | Meaning | Keys | Precedent |
|------|------|------|------|
| Tap | Primary action | OK | Every feature phone |
| Long-press | Secondary actions | Menu key / long-press OK | S60 Options, BlackBerry menu key, Bilibili TV edition |
| ⋮ in the top-right corner | Page menu | Left soft key 「选项」 (Options) | S60 |
| Bottom tabs | Top-level sections | Number keys 1–5 jump directly; or move focus to the tab bar and switch left/right | |
| Swipe left/right to change page | Switch between siblings | `Alt+←/→` | Desktop browsers |
| Pull to refresh | Refresh | `R` (also in the menu) | |
| Swipe left to delete | Delete | `Del` (with a confirmation) | BlackBerry |
| Back gesture | Back | Back key | |
| Double-tap to like | Quick action | `L` (shown on the right of the menu item) | |
| Long-press to enter multi-select | Batch operation | Menu 「标记」 (Mark) / `Shift+↑↓` | S60 "Mark / Unmark" |
| Select text to copy | Text selection | Selection mode + `Shift+arrow` | BlackBerry |
| Drag the progress bar | Seek | `←/→` ±10 seconds; `0–9` jump to 0–90% | YouTube on the web |
| Pinch to zoom / drag | Zoom and pan | `+/-` or side keys; D-pad pans | |
| Tap the search box | Search | Just start typing | BlackBerry global search |
| Floating "+" button | Create | Right soft key | |
| Hold to talk | Voice message | Hold a dedicated key to talk, release to send (walkie-talkie style) | |

Here a QWERTY keyboard has an advantage over a remote: the Bilibili TV edition cannot send danmaku because a remote cannot type, but a QWERTY phone can.

## 3. Seven hard rules (enforced by the system, not left to app discipline)

1. **No feature can be triggered only by a gesture.** Every actionable element declares an action list: one primary action (OK), and the rest go into the context menu.
2. **The page menu has three groups**: focused element / this page / global. Shortcuts are written on the right of the menu items, so users learn the shortcuts from the menu (the old desktop-software approach).
3. **Menus are at most two levels deep**; anything deeper is split into a separate page.
4. **Back is always available** and returns to the original position (list scroll position and input drafts are kept).
5. **Type to search**; the command palette finds every action on the page.
6. **Focus is always visible** (high-contrast focus frame); focus order = reading order.
7. **A row of soft-key labels is always shown at the bottom** and changes with focus (S60 style), so users always know what the left and right soft keys do at this moment.

## 4. Semantic UI model `phone.Ui` (v0 implemented in the prototype)

Apps do not draw their own interface; they only hand over a tree of "content + actions", and the Shell takes care of focus, keys, menus and rendering.

> **Prototype implementation**: the type definitions are in [ui/proto.go](../../ui/proto.go), the capability catalog in [ui/caps.go](../../ui/caps.go). Differences from the draft below: the v0 nodes are `text / section / item / input / button / image / media / doc / progress` (`media` is the draft's `MediaSurface`, implemented; `doc` is a Markdown document whose links automatically become `link:N` menu actions; `progress` is a progress bar; `Grid` is implemented with `Screen.Columns` + `tile` items); `item` carries `style: bubble-in / bubble-out` for chat bubbles; `input` carries `keyboard: number / phone / email` to choose the keyboard type; the transport is JSON lines (not the binary format of §4.4); capability requests (`req / res`) go over the same connection. Of the seven rules in §3, rules 1–5 and 7 are enforced by the Shell, and the focus frame of rule 6 is implemented.

### 4.1 Nodes v0

| Node | Purpose | In the text renderer |
|------|------|--------------|
| `Screen` | Title, soft keys, body | Top bar + bottom bar |
| `List` / `Grid` | Lists, image grids | List; grids are listed row by row |
| `Item` | One piece of content: avatar / thumbnail, title, subtitle, badge, **action list** | One or two lines of text |
| `Text` | Rich text that can embed emoji, links and small images | Emoji degrade to `[doge]`, images to `[图]` ("[img]") |
| `Image` | Thumbnails, avatars, full-size pictures | `[图]` |
| `MediaSurface` | Video, camera preview, PDF pages (frames supplied by the media service) | `[视频 03:12 ▶]` ("[video 03:12 ▶]") + playback controls |
| `Form` | Input fields, switches, radio buttons, checkboxes | Native form |
| `Menu` / `Dialog` / `Toast` | Generated by the Shell from the action list; apps do not draw them directly | Pop-up layer |

### 4.2 Actions

```
Action {
  id:      "reply"
  label:   "回复"
  key:     "R"        # optional shortcut, shown on the right of the menu item
  primary: false      # true = run by the OK key; at most one per Item
  group:   "item"     # item | page | global | danger
  confirm: false      # the danger group asks for confirmation by default
}
```

### 4.3 Example: a comment

```
Item {
  id: "c123"
  leading:   Image(avatar://u42, 32x32)
  primary:   "某某"
  secondary: Text["说得对 ", Emoji("doge")]
  media:     [Image(thumb://p9)]
  trailing:  "128 赞"
  actions: [
    { id: "open",   label: "查看回复", primary: true },
    { id: "reply",  label: "回复",     key: "R" },
    { id: "like",   label: "点赞",     key: "L" },
    { id: "copy",   label: "复制",     key: "C" },
    { id: "report", label: "举报",     group: "danger" }
  ]
}
```

### 4.4 Implementation constraints

- **Transport**: D-Bus is only used for the control plane of system services; the UI tree goes over a Unix socket + compact serialization + incremental updates (D-Bus is not suited to frequent large messages).
- **Images do not go into the message body**: only references are passed (file path / shared memory / dmabuf), and the Shell or the media service decodes and caches them.
- **An app crash does not affect the Shell**: each app runs in its own process; the Shell shows 「应用无响应」 (App not responding) and can restart it.
- **v0 only does** `Screen / List / Item / Text / Image / Form / Menu`; `Grid` and `MediaSurface` go into v1.
- Precedents: Android RemoteViews (home-screen widgets), early WatchKit (the app runs on the phone, the UI shows on the watch), server-driven UI.

## 5. Walkthrough: testing the grammar against Bilibili / WeChat UI structures

> Only the **UI structure** of these two apps is borrowed to test whether the interaction is sufficient; this does not mean building clients for them (see 09 §5).
> Mock screen: 480×640 portrait, body text about 20 Chinese characters per line, around 20 lines.

### 5.1 Bilibili-style: recommendation feed → video page → comments

Recommendation feed (pixel rendering; the mock screen text is Chinese: tabs Recommended / Popular / Following / Me, `缩略` = thumbnail, soft keys Options / OK Play / Search):

```
┌ 推荐 │ 热门 │ 动态 │ 我的 ─────────┐  ← 1–4 jump straight to a tab
│ ┌──────┐ 【4K】某纪录片 第3集       │
│ │ 缩略 │ UP 某某 · 12.3万播放        │  ← focus
│ └──────┘                            │
│ ┌──────┐ 三分钟看懂 RK3566          │
│ │ 缩略 │ UP 某某 · 8.1万播放         │
│ └──────┘                            │
│ ...                                 │
├─────────────────────────────────────┤
│ 选项           OK 播放          搜索 │
└─────────────────────────────────────┘
```

| What the user wants | On a phone | Here |
|----------|--------|------|
| Open a video | Tap the card | OK |
| Watch later / Not interested / Share | Long-press the card or tap ⋮ | Menu key → list; or the shortcuts `W` / `X` |
| Go to the uploader's page | Tap the avatar | Menu key → 「UP 主主页」 (Uploader's page), or `U` |
| Refresh | Pull down | `R` |
| Search | Tap the search box | Just start typing |

Video page: a `MediaSurface` at the top (16:9, about 480×270), and two tabs below it, 「简介 / 评论」 (Intro / Comments).

| What the user wants | Here |
|----------|------|
| Play / pause, fast-forward | With focus on the video: OK, `←/→` |
| Toggle danmaku / send danmaku | `D` / `T` (an input line pops up at the bottom; type on the QWERTY keyboard) |
| Like / coin / favorite / triple | `L` / `C` / `F` / 「三连」 (triple: like + coin + favorite at once) in the menu |
| Quality, playback speed | Left soft key 「选项」 (Options) |
| Full screen | `Z` |
| Reply to a specific comment | Move focus to that comment → `R` |

The same page in the text renderer: thumbnails become `[图]` ("[img]"), the video becomes `[视频 12:31 ▶]` ("[video 12:31 ▶]"). The comment section is fully usable, while the home recommendation feed becomes noticeably worse. This is the "rendering-layer bottleneck" described in 09 §1.

### 5.2 IM-style: conversation list → chat

| Action in WeChat | Here |
|--------------|------|
| Long-press a conversation: pin / mark as unread / delete | Menu key |
| Find a person | Type directly in the conversation list |
| Type in a chat | Focus defaults to the input field; just type, Enter sends |
| Scroll back through history | `↑` moves focus into the message area, then one message at a time |
| Long-press a message: copy / forward / quote / multi-select / delete | Press the menu key with focus on the message (one-to-one with WeChat's long-press menu) |
| Send a picture | Menu 「附件 → 相册」 (Attachment → Album), pick from the image grid with the D-pad, OK sends |
| View a full-size picture | Press OK with focus on the picture → full-screen viewer, `←/→` for the next/previous picture, `+/-` to zoom |
| Voice message | Hold a dedicated key to talk, release to send |
| Moments nine-image grid | Displays normally on the pixel renderer; the text renderer only shows `[图×9]` ("[img×9]") |
| Official-account articles, mini-programs, payments | **Not supported** (needs a browser engine and the vendor, see [09](09-ui-feasibility-review.md)) |

Conclusion: in both samples every entry point of the interaction layer maps to keys, and nothing impossible turned up; everything that works poorly is concentrated in the rendering layer (thumbnail feeds, image grids) and the ecosystem layer (H5, mini-programs, payments).

## 6. Estimating how much fits on screen

| Screen | Body font size | Chinese characters per line | Lines (excluding status bar / soft-key bar) |
|----|----------|----------|------------------------------|
| 2.4" 320×240 landscape (about 167 ppi) | 16 px | ~20 | ~11 |
| 2.8" 640×480 landscape (about 286 ppi) | 26 px | ~24 | ~15 |
| 3.5" 480×640 portrait (about 229 ppi) | 24 px | ~20 | ~20 |

About 3–5 list items with thumbnails fit on one screen, close to what one screen of the Bilibili phone app shows. What is really limited is split screen: cut any of these screens in half and each pane has only about 10 Chinese characters per line, so there is no general tiling (09 §3.3).

## 7. Scenarios keys handle poorly

| Scenario | Reason | Handling |
|------|------|------|
| Free map dragging | Continuous two-dimensional operation | Built mainly around search / route lists, with D-pad panning; canvas app |
| Free web browsing | Pages are not designed for focus | Reading mode + link list; a virtual cursor when needed (the KaiOS approach) |
| Games, drawing, image editing | Continuous input | Canvas app, or not done |
| Mini-programs / H5 apps | Need a browser engine + the vendor | Not done |

## 8. Freeze conditions

Once the desktop prototype (09 §7) passes: freeze the node table and the action fields of `phone.Ui` v0 and write them into [03](03-protocols-and-ui-path.md).

Current state: the prototype is complete ([13](13-prototype.md)); the Bilibili-style and IM-style samples are covered by the 「聊天」 (Chat) app (conversations, bubbles, message menu, forwarding, pictures, video calls); the Shell is a generic text renderer, and the pixel renderer runs the same apps and the same selftests ([06](06-gui-architecture.md)). Before freezing, one week of self-use records is still missing (57 (internal document) R9).
