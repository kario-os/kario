> English translation of [the Chinese original](../09-ui-feasibility-review.md). The Chinese text is authoritative; keep both in sync.

# Consolidated review: can a TUI carry complex apps? · Merging the two proposals

> Input: documents 01–14 in this directory (QWERTY keyboard body + telephonyd + open-source Go TUI + closed-source Slint GUI) and the new draft "A Small-Screen-Only TUI Hybrid Intelligent Operating System" (original archived as 90-draft-small-screen-tui-os.md (internal document)).
> Status: review conclusions, **not implemented** (all of it). D1–D5 in §8 are confirmed and are being carried out as this document recommends.

## 0. Conclusions first

1. **The pessimists are half right.** "Using the original WeChat / Bilibili on this device" really is impossible, but what blocks it is not the TUI, it is the ecosystem: they are closed-source Android / iOS apps, and even the best GPU GUI could not run them.
2. **The other half does not hold.** "Every window has an options entry, and long-press brings up yet another menu" is **interaction structure**, and a keyboard can express it completely: the S60 "Options" soft key, the BlackBerry menu key, D-pad focus on Android TV and right-click menus in desktop software have all proven it. **Long-press = pressing the menu key on the focused element.** The detailed grammar is in [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md).
3. **The TUI's real weakness is mixed text and images**, which the new draft itself admits in its "weaknesses" section. Yet mixed text and images are exactly the core of modern content apps: the Bilibili home page is a thumbnail feed, WeChat chats contain pictures and stickers, and Moments uses a nine-image grid. A system that can only "show images in a pop-up" cannot build this kind of app.
4. **Approach (D2 decided): keep "keyboard-first", drop the "character grid".** Interaction is designed in the spirit of a TUI (text first, focus-driven, no hidden gestures), rendering uses GPU pixels (the existing Slint route), and apps are described with a **semantic UI model** (list / menu / form / image / media, each element carrying an action list) that the system renders uniformly. The open-source `phone-tui` stays as the text renderer for the same model (SSH / serial / debugging).
5. **Positioning (D1 decided): a second phone / focus phone** (calls, SMS, contacts, notes, music and podcasts, reading, terminal); WeChat stays on the main phone. If "it must run WeChat", the realistic route is to build a keyboard system on AOSP (as Duoqin Qin and Mudita Kompakt do), which would overturn the current Linux + telephonyd stack (§5).

## 1. Splitting the problem into three layers

Both documents mix three separate things together:

| Layer | The question | Deciding factor |
|----|--------|----------|
| **Interaction model** | How the user operates: focus, options menu, long-press, back, search | Design rules; independent of how things are rendered |
| **Rendering model** | What is drawn on screen: character grid vs arbitrary pixels | A character grid cannot draw inline thumbnails, proportional fonts or emoji |
| **App ecosystem** | Is there WeChat / Bilibili / Alipay | Vendor willingness + platform scale; unrelated to TUI vs GUI |

Putting typical Bilibili / WeChat UI elements into these layers:

| UI element | Layer | Keys + character TUI | Keys + pixel GUI |
|----------|--------|:---:|:---:|
| "⋮ More" entry on every page, multi-level settings | Interaction | ✓ | ✓ |
| Long-press a message / video for a pop-up menu | Interaction | ✓ (menu key) | ✓ |
| Bottom tabs, swipe left/right to switch | Interaction | ✓ (number keys / left-right keys) | ✓ |
| Multi-select, batch delete | Interaction | ✓ (mark mode) | ✓ |
| Thumbnail feed (Bilibili home page) | Rendering | ✗ | ✓ |
| Pictures, stickers and avatars in chats | Rendering | ✗ (only a `[图片]` ("[image]") placeholder) | ✓ |
| Moments nine-image grid, comments with images | Rendering | ✗ | ✓ |
| Video playback + danmaku (bullet comments) | Rendering | △ (the new draft's media layer can do it) | ✓ |
| Official-account articles, mini-programs (H5) | Rendering + ecosystem | ✗ | ✗ (needs a browser engine + the vendor) |
| WeChat / Bilibili / Alipay accounts and content | Ecosystem | ✗ | ✗ |

How to read it: a keyboard can handle every interaction row; in the rendering rows the character grid is the bottleneck; in the ecosystem rows no UI technology helps.

## 2. Precedents

| Precedent | What it shows |
|------|-----------|
| **Nokia S60, BlackBerry OS** | 2.4–3-inch screens, QWERTY keyboard, no touchscreen, yet they ran email, browsers, IM and social clients; "Options soft key / menu key + focus" was enough |
| **Bilibili TV edition 「云视听小电视」 (Yunshiting Xiaodianshi)** | A version the vendor officially rebuilt for remote-control D-pads; long-press OK / the menu key brings up options. But it is a **subset built specifically by the vendor** (you cannot send danmaku from a remote; early versions could not play uploaders' videos). Keyboard adaptation is feasible, provided the vendor is willing to do it |
| **Android TV, desktop software, screen readers** | As long as every element declares an "action list", even a very complex app can be fully operated with D-pad / Tab + menu (screen readers expose long-press as a "custom action") |
| **KaiOS** | A feature-phone OS shipped on over a hundred million devices; WhatsApp and YouTube only arrived after Google / Facebook invested in it. Big vendors only adapt to platforms with scale |
| **WeChat for watches** | Tencent did build small-screen versions (Apple Watch, Huawei watches), all for platforms of huge scale |
| **Duoqin Qin series** | Products in China that combine "small screen + keys + must run WeChat" run Android; the market has already validated this route |
| **Mudita** | The Pure runs MuditaOS (open source), an in-house system built on FreeRTOS; the second model, the Kompakt, switched to a **heavily customized AOSP 12** |
| **WeChat risk control** | Logging in from an emulator / virtual machine triggers restrictions or even account bans (the MuMu emulator's official help page says so), so the Android-container route carries a real risk for important accounts |
| **Sxmo (postmarketOS), Beepy** | Small-screen "keys + menus + tiling" systems on Linux already exist as community practice and can be borrowed from directly; they also show that claims like "world's first" do not hold |

## 3. Point-by-point evaluation of the new draft

### 3.1 Adopted

| New draft content | Where it lands |
|----------|----------|
| Small screen first, keys native, not a shrunk-down big-screen version | First product principle; interaction grammar in [10](10-keyboard-interaction-model.md) |
| Media engine as a separate process, started on demand, destroyed after use | System media service (images / video / PDF / camera) that produces frames for the semantic UI's `Image` / `MediaSurface` nodes |
| Decoupled interaction / rendering / media / system layers | Consistent with the narrow-waist principle in 56 (internal document) |
| No reverse engineering or cracking of WeChat / Alipay | Written into the scope boundary (51 (internal document)) |
| Stack-style tasks by default (single foreground + suspended background) | Default of the task model |
| Unified app spec: manifest, lifecycle, tiered permissions | Merged into the `.phoneapp` of [07](07-ecosystem-and-store.md) |
| systemd-managed services, restart on crash | Consistent with [05](05-implementation.md) |
| Three Yocto layers (bsp / os / apps) | How product images are built (development can still use Debian / postmarketOS) |
| RK3566 as the main SoC | Chosen at the time as the preferred AP; **since changed** to an integrated phone SoC (modem on the die), see 61 (internal document) |

### 3.2 Needs correction

| New draft claim | Reality | Correction |
|----------|----------|------|
| Notcurses "natively supports direct DRM output, no TTY needed" | **Not true.** Notcurses only runs in a terminal or on the Linux console; the DRM/KMS backend issue (#1955) is still unimplemented. The Linux console supports at most 256/512 glyphs and **cannot display Chinese** | To show a Chinese TUI on DRM you have to write your own pixel renderer, and that is already a GUI. See §4 |
| Two DRM hardware planes as "zero-CPU-overhead black magic" | Hardware planes are a power-saving optimization that every compositor uses (Android HWC, Wayland); there are only a handful of them, so each thumbnail cannot get its own. GPU composition is still needed | Plane passthrough is kept as an optimization for video / camera, not as an architectural selling point |
| Yocto Kirkstone / Mickledore | Kirkstone LTS reached **end of maintenance in 2026-04**; Mickledore already ended in 2023-11 | Use **Wrynose 6.0 (LTS until 2030-04)**, or Scarthgap 5.0 (LTS until 2028-04) |
| Recommends the i.MX8MM | The 3D GPU of the i.MX8M Mini only supports **OpenGL ES 2.0**, which conflicts with the GLES3 / Vulkan requirement in [06](06-gui-architecture.md) / [08](08-display-adaptive-refresh.md) | Changed at the time to RK3566 / i.MX8M Plus; **since changed** to an integrated phone SoC (61 (internal document)) |
| "A TUI has ultra-low power draw and ultra-long battery life" | With a static screen neither a TUI nor a GUI redraws, so power draw is almost the same. The big consumers are the backlight, the cellular module, SoC standby and background wake-ups. Going by community experience with Linux phones such as the PinePhone and Librem 5, an AP + external LTE module with carefully tuned suspend usually lasts "one to a few days", nowhere near the "weeks" of a feature phone | Battery life comes from: no resident background processes, no polling, aggressive suspend, modem DRX, staying on 4G (61 (internal document) §1), and optionally a low-power display (reflective / memory LCD, at the cost of being unsuitable for video) |
| 6 phases, about 13 weeks | Unrealistic for one person working part-time; "making calls + audio routing + suspend/resume" alone takes months | See the rough estimate in §7: about 1.5–3 years to "a second phone usable every day" |
| No telephony anywhere in the text | Only Wi-Fi / Bluetooth are covered; no cellular, calls or SMS | Merge in the telephonyd stack from 01–11 |
| "World's first / world's only", "no longer has any of the traditional TUI weaknesses" | There are many precedents (§2); the new draft itself admits the mixed text-and-image weakness | Drop these from external messaging; all assessment text is marked "not implemented / unverified" |

### 3.3 Not adopted or deferred

| New draft content | Reason | Handling |
|----------|------|------|
| Reserve interfaces and wait for official WeChat / Alipay adaptation | No commercial incentive; big vendors only adapt to platforms with scale (§2) | Not a precondition of any plan |
| Payment interface | Payments need certification from the central bank / banks / card networks and a secure element | Moved out of scope |
| PC low-code designer + QuickJS | As big as building another product, and it does not solve "no WeChat" | Deferred until after v2; the semantic UI model (§4) by itself makes "drag-and-drop app generation" much simpler |
| Loading `.tapp` native dynamic libraries | Third-party libraries inside the system UI process: one crash takes everything down | Keep the approach of [07](07-ecosystem-and-store.md): each app in its own process |
| i3 tiling split screen | Splitting a 2.4–3.5-inch screen in half leaves only about 10 Chinese characters per line in each pane | No general tiling; only fixed-scenario split layouts (video + comments, call + notes) |

## 4. Recommended approach: keyboard-first interaction + pixel rendering + semantic UI model

```
┌────────────────────────────────────────────────────────────────────────────────┐
│ Apps (one process each): only describe "what content there is and what         │
│   each element can do"                                                         │
│   Phone / SMS / Contacts / Settings / third-party .phoneapp                    │
└───────────────┬────────────────────────────────────────────────────────────────┘
                │ phone.Ui: semantic tree + action lists (draft, not implemented)
┌───────────────▼────────────────────────────────────────────────────────────────┐
│ System Shell: focus, key mapping, options menu, soft keys, back stack,         │
│   search, tasks                                                                │
│   ├─ Pixel renderer phone-gui (Rust + Slint, GPU, closed source) ← production  │
│   └─ Text renderer phone-tui (Go, open source) ← SSH / serial / debugging      │
├────────────────────────────────────────────────────────────────────────────────┤
│ Media service (started on demand): images / hardware video decode / PDF /      │
│   camera                                                                       │
│   → supplies frames to the Shell; video and camera can use hardware plane      │
│     passthrough                                                                │
├────────────────────────────────────────────────────────────────────────────────┤
│ System services: telephonyd (C++23) · audio · power · display · pkg            │
├────────────────────────────────────────────────────────────────────────────────┤
│ Linux + BSP (Yocto)                                                            │
└────────────────────────────────────────────────────────────────────────────────┘
```

Why split it this way:

1. **The system owns keyboard interaction uniformly, so apps cannot build "gesture-only" interfaces.** Apps only declare elements and actions; OK runs the primary action, the menu key lists the remaining actions, and shortcuts are shown on the right of the menu items. That is how Bilibili / WeChat-level complexity is handled: complexity goes into menus, not into gestures.
2. **Pixel rendering solves mixed text and images.** A thumbnail in a list item, pictures and stickers in a chat, a [doge] in a comment: each is just one more `Image` node in the semantic tree.
3. **The TUI work is not wasted.** From the same semantic tree, the text renderer downgrades images to `[图]` ("[img]"), so the whole phone can be operated over SSH, a serial line or a terminal on a development machine; it is also the entry point for open-source contributors. The new draft's "TUI spirit" becomes the **design language**: text first, grid-aligned, high contrast, focus always visible, no hidden gestures.
4. **Keep an escape hatch.** Free-canvas apps such as camera, maps and games draw directly with Slint (the second-tier apps of [07](07-ecosystem-and-store.md)), but they must handle key events and go through manual review before they are listed in the app store.

The nodes, actions and walkthrough examples of `phone.Ui` are in [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md).

## 5. WeChat / Bilibili: three realistic routes

| Route | Approach | What you get | Cost / risk | Recommendation |
|------|------|--------|-------------|------|
| **A. No compatibility (second / focus phone)** | Native apps + open protocols | Calls, SMS, contacts, notes, calendar, music / podcasts, reading, email, terminal / SSH; open-protocol IM (Matrix, XMPP, SIP) | Chinese social apps can only stay on the main phone; the target audience narrows | **Decided** (D1) |
| **B. Linux + Android container (Waydroid)** | Open an Android "compatibility window" inside this system | Some APKs run | Needs 4GB+ of RAM (with 2–3GB Android frequently kills foreground apps), a touchscreen or a virtual cursor, and noticeably more power; WeChat may ban accounts in container environments; phone-sized UIs crammed into a 3-inch screen give a poor experience | Not in v1 / v2; at most an experiment on a high-end SKU |
| **C. Keyboard system on AOSP** | Like Qin and Mudita Kompakt: AOSP + in-house launcher and system apps | WeChat very likely works (but still with the phone UI) | Overturns the current Linux + telephonyd stack; conflicts with the "lean Linux" philosophy | Only if "it must run WeChat" |

Additions to route A (all **unverified**):

- **Notification mirroring**: a companion app on the main phone (similar to KDE Connect) forwards WeChat notifications to this device; whether you can reply depends on whether the source app's notifications support direct reply. It does not touch the WeChat protocol, so the risk is low.
- **Bilibili**: unofficial APIs and third-party clients exist, but they sit in a terms-of-service gray zone and can break at any time. Only for personal tinkering, not a product feature.
- **Telegram**: the official open-source TDLib makes a legal native client possible, but Telegram is not reachable on networks in mainland China.

## 6. Knock-on corrections to the existing documents

| Issue | Location | Handling |
|------|------|------|
| The scope table says "no GUI, no app store", but [06](06-gui-architecture.md) / [07](07-ecosystem-and-store.md) are already designing them | 51 (internal document) | Changed to phased inclusion; add "Not included: Android app compatibility, WeChat / Alipay" |
| The 120Hz hard requirement conflicts with a 2.4–3.5-inch small screen: as far as we currently know, high-refresh screens in consumer products basically start at around 5.9 inches (e.g. Zenfone 9/10), off-the-shelf 120Hz DSI panels under 3.5 inches are almost impossible to find (to be verified with suppliers), and custom panels have very high minimum order quantities; key interaction moves in discrete steps, so high refresh gains far less than it does with finger dragging | 52 (internal document), [06](06-gui-architecture.md), [08](08-display-adaptive-refresh.md) | Downgraded to **a 60Hz baseline, with 120Hz added once a suitable panel is available** (D3 decided); the design in which `phone-displayd` centrally manages the refresh rate is kept |
| The final AP list included the i.MX RT1170 (an MCU that cannot run Linux) and the STM32MP157 (only a GLES2-class GPU) | 52 (internal document) | Changed at the time to RK3566 / i.MX8M Plus; **since changed** to an integrated phone SoC (61 (internal document)) |
| Third-party apps = free-form Slint UIs, so the system cannot guarantee they are usable with keys | [07](07-ecosystem-and-store.md) | Changed to two tiers: semantic apps (default) + canvas apps (exception, review required) |
| Phase 2 shows the TUI menu with fbcon | 53 (internal document) | fbcon cannot display Chinese, so it is only for debugging; menu acceptance uses the Shell's rendering instead |

## 7. Next step: first spend a month answering "does it work?" the cheapest way

Before buying boards or drawing PCBs, validate the interaction grammar on a computer:

1. Open a window with the same resolution as the target screen (e.g. one set at 320×240 landscape and one at 480×640 portrait), keyboard input only.
2. Build two "sample apps" with fake data: **Bilibili-style** (recommendation feed → video page → danmaku toggle → comments → reply) and **IM-style** (conversation list → chat → long-press message menu → send a picture → view the full-size picture), plus three phone screens (idle / incoming call / in call).
3. Build each in both renderings: a pixel version (Slint desktop window) and a text version (Bubble Tea in a terminal).
4. Use it yourself for a week straight, recording the number of key presses per task, any dead ends where you "can't find the entry", and the pages where the text version is unusable.

**Pass threshold**: every action of both sample apps can be done with keys and there are no dead ends → the interaction model holds; move on to stages A / B of [05](05-implementation.md). The list of pages where the text version is unusable → that is the evidence that the pixel GUI is necessary.

Rough effort estimate (one person part-time, 10–15 hours a week, for scheduling reference only):

| Milestone | Rough estimate |
|--------|------|
| Desktop interaction prototype (this section) | 1 month |
| Making calls on a dev board + telephonyd + phone-tui | 2–3 months |
| Shell + semantic model v0 + Phone / SMS / Contacts / Settings | 4–8 months |
| Image / video / camera media service | 2–4 months |
| Custom PCB + enclosure (can run in parallel with the software) | 6–12 months |
| To "a second phone usable every day" | about 1.5–3 years |

## 8. Decisions (confirmed)

| # | Question | Decision |
|---|------|------|
| D1 | Product positioning: a second / focus phone (no WeChat support), or must it run WeChat | **Second / focus phone** (route A), keeping the Linux + telephonyd stack |
| D2 | Rendering: pure character TUI (new draft), or pixel rendering + TUI style + semantic UI model | **Pixel rendering + TUI style + semantic UI model**; the open-source TUI is the text front end |
| D3 | Display: keep the 120Hz hard requirement, or a 60Hz baseline + optional 120Hz | **60Hz baseline**, with 120Hz added once a suitable small 120Hz panel is available |
| D5 | How top-level apps are built | **Semantic UI apps + JS mini-programs form the top level; the web engine is only an on-demand H5 component (tier 4), not the foundation for apps.** Basis: measured on the same chat app, the web route uses about 8–10 times the memory, and 57% of the web version's code is a home-made keyboard interaction layer ([15](15-web-vs-semantic-benchmark.md), [14](14-tui-route-and-app-framework.md)) |
| D4 | Platform vision | **The platform provides every system capability a super app needs** (including video calls inside the TUI and trusted payment); vendors can use the SDK to publish `phone.Ui` (TUI) versions of their apps; see [11](11-platform-architecture.md), [12](12-super-app-capabilities.md) |

Recorded in the §8 decision log of 56-product-strategy.md (internal document).

D4 does not conflict with D1: D1 is the near-term positioning (without vendor apps, the device is a second phone), D4 is the platform goal. The conclusion of §5 still holds: no reverse engineering, no Android container; whether WeChat and similar apps get listed depends on their vendors publishing them on top of the platform capabilities (the non-technical barriers are in [12](12-super-app-capabilities.md) §8). D4 also clarifies the wording of D2: a "TUI app" means an app written with `phone.Ui`; in a terminal it is shown by the text renderer (media as half-block character pixels), and on production devices by the pixel renderer.
