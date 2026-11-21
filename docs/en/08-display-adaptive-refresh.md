> English translation of [the Chinese original](../08-display-adaptive-refresh.md). The Chinese text is authoritative; keep both in sync.

# Display infrastructure: 60–120Hz adaptive refresh

> Current state (2026-11-21): **phone-displayd ✓** (Go, `programs/displayd`, one of kario-system's programs like the other system services: started by the Shell on the developer machine and by systemd's `phone-displayd.service` on the device). It alone decides the refresh rate: by the policy of §5 it switches between 60 ⟷ 120Hz automatically, listening to the keys, the renderer's animations, the foreground app's `display.hint` and the frame rate of a video playing, which the Shell passes on; a low battery and a warm phone cap it, and Settings › Display and brightness › Refresh rate can fix it at standard or high. The pixel renderer phone-paint (Rust, Apache-2.0) holds the panel (DRM master): it rasterizes on the CPU and by default composes on the CPU too; with `PHONE_PAINT_GPU=1` it composes with GLES 2 and puts frames on the panel through GBM and page flips ([37](37-motion.md) §5 item 8, **not tried on a phone**, no Adreno, Mali or PowerVR tried). On either path it switches the display mode to the rate displayd chose (the mode of the other rate has the same size, so the GPU's buffers stay valid), takes each step of its motion once a period of the panel, and draws no frames the panel cannot show. The developer machine's and QEMU's selftests have a simulated 60/120Hz panel; unit tests and selftest ✓; **switching modes on a real panel is not tried on a phone** (§9). This document was written when phone-gui, built on Slint, was the device renderer; phone-gui is now only kept for comparison (it does not switch rates), and "the renderer" below means phone-paint.

> **D3 (decided): 60Hz baseline; once a suitable 120Hz panel is chosen, enable the 60↔120 adaptive policy described in this document.** Rationale: as far as we currently know, high-refresh panels in consumer products basically start at around 5.9 inches, ready-made 120Hz DSI panels at 3.5 inches or below are almost impossible to find (to be verified with procurement), and custom panels have very high minimum order quantities; key interaction moves in discrete steps, so the benefit of high refresh is far smaller than for finger dragging. The design in which `phone-displayd` exclusively owns the refresh rate and apps can only send hints still applies: on a panel of one rate displayd reads one mode and switches nothing, and Settings has no refresh rate row. So the "120Hz peak" in §1 / §2 only applies to models that chose a 120Hz panel; 60Hz panels are fine for mass production. Details: [09-ui-feasibility-review.md](09-ui-feasibility-review.md) §6.

**Display infrastructure** aligned with mainstream platforms: the panel goes **up to 120Hz**, and the system **switches automatically between 60Hz and 120Hz** — static content saves power, interaction and animation run at full refresh, and the experience has the same semantics as flagships (ProMotion / adaptive high refresh).

Related: 52-hardware-architecture.md (internal document), [06-gui-architecture.md](06-gui-architecture.md), [05-implementation.md](05-implementation.md), [23-renderer-protocol.md](23-renderer-protocol.md) (`rate`, `motion`), [37-motion.md](37-motion.md) (motion follows the panel's period).

## 1. Product definition

| Item | Requirement |
|----|------|
| Panel capability | **120Hz peak**; supports at least **two DRM modes, 60Hz and 120Hz** (or **VRR 48–120Hz**, see §2.1) |
| System policy | **Automatic**: interaction/animation → **120Hz**; static reading/idle screen → **60Hz** (lower where the panel has lower modes, down to 30Hz) |
| UI at 120Hz | Benchmark scenarios **1% low ≥ 110fps** (§7) |
| UI at 60Hz | Benchmark scenarios **1% low ≥ 55fps**; spinning the GPU at 120fps while in 60Hz mode is **forbidden** |
| Touch to first frame | When triggered by a key press/touch, **request 120Hz immediately** (see §4) |
| Full-screen incoming call | **120Hz** + first Incoming frame **< 100ms** (the 120Hz is **not implemented**: the incoming call's pulse is advanced by the Shell twice a second, so a higher rate would not help; once the renderer advances it, the rate rises with its animation, [37](37-motion.md) §3) |

The TUI phase can use a 60Hz development board; **on models with a 120Hz panel, the renderer and the BOM must support dual 60/120 modes (or VRR)**, and §2 lists the constraints for such models; models with only a 60Hz panel can go into mass production (D3).

## 2. Hardware (hard BOM constraints)

### 2.1 Panel

| Component | Requirement |
|------|------|
| Interface | **MIPI DSI**; SPI/I8080 color panels **are not used for the production GUI** |
| Refresh | **120Hz** in the datasheet, with explicitly specified **60Hz timing** or **VRR range**; the panel driver must switch **seamlessly** (DSI's seamless mode switch), or the whole screen blinks at every switch |
| LTPO | **Not mandatory** in v1; if present, v2 extends `phone-displayd` with fine-grained levels (the policy already picks from all of the panel's modes: with 30Hz or 48Hz modes, still screens and 24-frame video use them) |
| Backlight | PWM must not produce visible beating against the current refresh rate (test while switching 60/120) |

### 2.2 SoC / GPU

| Component | Requirement |
|------|------|
| DRM/KMS | **atomic modesetting**; `modetest` shows **60Hz and 120Hz** modes (or a single VRR mode) |
| GPU | **GLES 3.x / Vulkan**; stable composition at both refresh rates |
| Bandwidth | **120Hz peak** meets the §6 frame budget |

### 2.3 Forbidden

- When a 120Hz panel is chosen, a panel whose only mode is **120Hz** (it cannot drop to 60Hz on static screens, so it saves no power).  
- No **phone-displayd**, with apps changing the refresh rate at will via `ioctl` (the policy must be centralized): apps never reach the display card, they can only send `display.hint` through the broker (§4.2).

## 3. Software stack

```
evdev ──► phone-paint ──key / motion──► Shell ──hint──► phone-displayd (Go)
             ▲  │                         │  ▲              │
             │  │ DRM master              │  └── rate ──────┘
             │  ▼                         │
             │ panel (display mode)       ├── the foreground app's display.hint (rate-limited)
             └──── rate ──────────────────┤── a video playing: its frame rate
                                          └── the refresh rate setting, a low battery's cap
phone-displayd ── reads the DRM card's modes (not as master); caps when a thermal zone is hot
```

| Process | Responsibility |
|------|------|
| **phone-displayd** | The **only** process that decides the refresh rate; executes the §5 policy table; on the device it reads the panel's modes (the connected connector's modes at the current size and the mode the CRTC runs), on the developer machine and in selftests a simulated 60/120Hz panel |
| **phone-paint** | The pixel renderer (rasterizing on the CPU; composing on the CPU by default, optionally on the GPU, **not tried on a phone**), holding the panel: it switches to the mode of the rate displayd chose (`rate`), throttles redraws to the panel's period, and tells the Shell when an animation begins and ends (`motion`) |
| **Shell** | displayd's only client: passes on the keys, the renderer's animations, the foreground app's hints, videos, the setting and the battery, and hands the rate displayd chose to the renderer (`shell/display.go`) |
| **telephonyd** | Notifies displayd of incoming-call/call state: **not implemented** (§1) |
| **Kernel** | Panel driver + dual modes or VRR |

**Why the renderer switches and displayd decides**: a card has one master, and switching display modes takes the master; the renderer opens the card as master at start and runs sandboxed (no network, no new sockets, [23](23-renderer-protocol.md) §5), talking to the Shell only. So the decision is displayd's and the switch the renderer's, passed on by the Shell — the same split as Android's DisplayManager setting the policy and SurfaceFlinger switching the mode. displayd opens the card too, to read the modes: with no master, the first process to open a card becomes its master, so it waits until the Shell says the renderer drives the panel (`panel {kms}`), reads once and closes it, and should it have become master after all, gives that up at once (`DROP_MASTER`).

**phone-paint:** where it can page-flip it follows the flips' completion events (120 steps a second with the panel at 120Hz, 60 at 60Hz); where it cannot, each step waits a period of the panel, and frames coming faster than the panel are merged into one before drawing; with no interaction it commits no empty frames (it paints only the damage, [37](37-motion.md) §5).

## 4. Interface: `phone-displayd`'s JSON-lines socket

> The early draft was D-Bus's `org.phone.Display`; the system services all use JSON lines over Unix sockets instead (package `wire`, [03](03-protocols-and-ui-path.md) §1), and displayd does too: `<run dir>/displayd.sock`, which only the Shell connects to (0600). The methods and fields are in `svc/displayd.go`.

```
methods (a request's arguments → each answers the state):
  state                                     the state
  setting  {setting}                        auto | standard | high (Settings › Refresh rate)
  hint     {source, kind, ms?, fps?}        kind: interaction | animating | end | content | clear
  cap      {source, hz}                     battery: at most hz, 0 to lift it
  panel    {kms?, applied?, err?}           the renderer drives the panel (read the modes) / the rate it switched to / why it did not
  sim.thermal {hot}                         the simulated thermal zone (tests)
state / the rate event:
  {seq, panel, rates, hz, want, reason, setting, caps, switches}
```

- `hz` is the rate the panel runs at, `want` the one displayd chose: on the simulated panel they agree at once; on a real one `hz` waits for the renderer's answer of which mode it switched to. `reason` says why: `idle`, `setting`, `cap:battery`, `cap:thermal`, `content:<frame rate>`, or the source of the hint that decided (`input`, `renderer`, `app:<app>`). `seq` grows with every change: the Shell does not take an answer older than what it holds (an answer may come after a later event).
- `PHONE_DISPLAY_TRACE=1`: displayd writes a line for each decision (the time, the rate chosen, the reason, the panel's rate), to read beside the renderer's `PHONE_PAINT_TRACE`.

### 4.1 The hints the Shell passes on

| Source | When | Hint |
|------|----------|------|
| `input` | Every key press (one per 100ms) | `interaction`: high rate, held until 400ms after |
| `renderer` | The renderer's `motion`: an animation begins / stops (scroll easing, focus slide, transitions, banners and toasts sliding) | `animating` / `end`: high rate from the start to the stop, held 400ms after it; a renderer that crashed without saying it stopped ends by itself after 10 seconds |
| `app:<app>` | The foreground app's `display.hint` (§4.2) | `animating` or `content` |
| `video:<player>` | While a video of `video.open` plays on the foreground app's screen (taken back when paused or off screen) | `content`: the frame rate mediad sends pictures at (15 in the prototype) |

When the Shell connects to displayd it first takes back every app's and video's hint, then gives the setting and the battery; after a displayd restart too.

### 4.2 Apps: `display.hint`

Apps never reach the display card; they can only say what they show ([22](22-developer-api.md) §4.12): `{animating_ms}` (an animation of their own, at most 3000ms; send again to go on), `{fps}` (their content's frame rate, such as a video they decode themselves or a game's fixed frame rate, until taken back), `{clear}`. Only the foreground app is heard (at the front of the screen, the screen on, not locked; a system menu over it does not matter, since the app does not know when it closes and could not ask again); each app at most 8 at once, then 4 a second (a token bucket), more get `too many display hints`; when an app leaves the foreground (another app, back to idle, locked, screen off, exited) the Shell takes its hint back, and the app sends it again once back in front. The answer is the panel's rate now and the rates it has. The system UI's scrolling and transitions need nothing from apps: the renderer says so itself.

## 5. Automatic policy (the setting on Automatic)

`programs/displayd/policy.go`, unit-tested line by line against the clock (`policy_test.go`):

| Condition | Refresh rate |
|------|--------|
| Key down, list scrolling, UI animation in progress (`input`, `renderer`), the foreground app says it animates | **The panel's highest rate** (120) |
| Incoming call ringing, in-call UI, within **3s** after waking from sleep | **120** (**not implemented**, §1) |
| A content hint (a video, an app's frame rate), nothing moving | The lowest rate every content frame rate divides: 24 frames on a 60/120 panel use 120, 30 frames 60, 24 frames 48 where there is a 48Hz mode; when none divides, the lowest at or above the fastest frame rate |
| Static screen with no animation for **≥ 400ms** | The lowest rate at or above 30Hz: **60** on a 60/120 panel, 30 on a panel with a 30Hz mode |
| Battery **< 15%** and not charging (the threshold at which animations reduce by themselves) | At most **60** (`cap:battery`, also with the setting fixed at High); the system has no power saving mode yet |
| A thermal zone at its limit (the zone types and temperature the board configures, `--thermal-zones`, `--thermal-cap`; lifted 3 degrees under it) | At most **60** (`cap:thermal`); no board configures thermal zones yet, **not tried on a phone** |
| Setting "Standard 60 Hz" / "High 120 Hz" | Locked (still capped by the two rows above) |
| Screen off / deep sleep | Panel off, Hz does not apply |

**Raising** the rate happens immediately; **lowering** it is delayed by **400ms** to avoid jitter at the boundary. With several caps the lowest holds.

Camera preview: the preview texture is updated at **30fps**; the UI chrome while browsing goes up and down according to the §5 table (scrolling the gallery raises it to 120).

## 6. Frame budget

| Mode | Frame period | Drawing budget (indicative) |
|------|--------|---------------------------|
| **120Hz** | 8.33ms | ≤ 5ms drawing + 2ms commit |
| **60Hz** | 16.67ms | ≤ 10ms drawing + 3ms commit |

What software rendering measures and is estimated at is in [37](37-motion.md) §2: on the developer machine most steps of motion take under 1ms; on a phone the steps where the whole screen changes are estimated at 2.5–8.5ms (**estimates, not tried on a phone**), so at 120Hz a whole-page transition may not make every period, and a step that misses comes a period late; keys never wait. telephonyd **must not** occupy the UI core and cause dropped frames at 120Hz.

## 7. Acceptance

**When 120Hz is in effect** (forced by the policy or during interaction), the §7 scenarios must reach **1% low ≥ 110fps**:

1. Inertial scrolling in Contacts for 5s  
2. Pressing the arrow keys repeatedly in Settings for 3s  
3. Idle ↔ Dialer transition ×20  
4. Incoming-call Incoming screen popping up  

**When 60Hz is in effect** (measured after 5s of static Idle): the **average power** of the Idle screen is **lower** than with a fixed 120Hz at the same brightness (record the measured percentage in `LOG.md`); frame rate **≥ 55fps** for the redraw after a single key press.

**Switching:** 60→120 enters the 120Hz mode within **≤ 2 frames** after the first key press (compare the times of displayd's `rate` event and of the renderer's answer `rate` once it switched modes with the input event).

Tools: `PHONE_DISPLAY_TRACE=1` (displayd's every decision) and `PHONE_PAINT_TRACE` (the steps, their times and the late frames of each burst of the renderer's motion, [37](37-motion.md) §5) ✓; `kario/tools/fps-bench/` **not implemented**. All of the acceptance above takes a real 120Hz panel, **not tried on a phone**.

What is verified now (developer machine, simulated panel):

| What | How | Result |
|----------|--------|------|
| The policy | `go test ./programs/displayd`: a key raises to 120 and 400ms after the last one it goes back to 60; the renderer's animation until it stops, then 400ms more; which rate content of 24, 30, 60, 15, 25 frames gets on various panels; the standard and high settings, the battery's and the thermal cap; an app's hint taken back when it leaves | ✓ |
| DRM modes | The structures and ioctl numbers against the kernel's layout; rates rounded as the kernel's `drm_mode_vrefresh`; only progressive modes of the current size | ✓ (reading a real card **not tried on a phone**) |
| The Shell | `go test ./shell -run TestDisplay`: keys, the renderer's animations, apps' hints (foreground, rate limit, taken back), the battery and the setting, the rate chosen handed to the renderer, an old answer not taken | ✓ |
| The renderer | `cargo test`: the period, `motion` said once, frames no faster than the panel, motion steps by the rate (a 180ms push in 11 steps at 60Hz, 22 at 120Hz), the mode picked by rate | ✓ |
| Selftest | The cases 「刷新率」 (Refresh rate) and 「刷新率设置」 (Refresh rate setting) ([13](13-prototype.md) §5) | See [13](13-prototype.md) |

The selftest script's commands (`shell/display_headless.go`): `require-display` (skip without phone-displayd), `display <hz> [ms]` (wait until the panel runs at that rate), `display went <hz> [ms]` (the panel went to that rate since the last display step: a key's 400 ms are short, and this catches them on a slow emulator too), `display stayed <hz> [ms]` (the panel stayed at that rate all that time), `display reason <reason> [ms]`, `display thermal hot|cool`, `display report` (prints `PHONE-DISPLAY`: the panel, the rate, the reason, the switches).

## 8. Alignment with mainstream platforms

| Infrastructure | Mainstream flagships | This project |
|----------|----------|--------|
| Adaptive high refresh | ProMotion / LTPO / Android adaptive refresh | **60 ⟷ 120 automatic** + `phone-displayd` ✓ (simulated panel; **not tried on a phone**) |
| GPU UI | Native | **phone-paint** (rasterizing on the CPU; compositing on the GPU optional (GLES 2, [37](37-motion.md) §5), **not tried on a phone**, no Adreno, Mali or PowerVR tried; rasterizing on the GPU **not implemented**) |
| Centralized policy | System compositor / SF | **phone-displayd** decides, the renderer switches |
| Apps' frame rate intent | Android's `Surface.setFrameRate`, iOS's `CADisplayLink.preferredFrameRateRange` | `display.hint`'s `fps` / `animating_ms`, foreground only, rate-limited |
| Separate telephony service | Yes | **telephonyd** |

We align on **refresh-rate policy + GPU UI + service-based architecture**, not on app store scale.

## 9. Implementation (see [05](05-implementation.md) Phase E)

1. Confirm **60/120** dual mode or VRR in the kernel (needs the chosen panel and board, **not tried on a phone**).  
2. **phone-displayd** decides the rate + emits the `rate` event ✓; reads the DRM card's modes ✓ (the structures checked in unit tests only; reading a real card **not tried on a phone**).  
3. **The renderer** follows the rate (the Shell's `rate`), switches the mode by it (`set_crtc`), throttles to the period + sends `motion` ✓; switching on a real panel (whether it blinks, how many frames it takes) **not tried on a phone**. QEMU's virtio-gpu reports one mode (at the size QEMU gives it, its rate set by QEMU's EDID, 60 or 75Hz, **not verified**): displayd reads one rate and switches nothing, and Settings has no row for it; so what QEMU shows is the policy on the simulated panel in the boot selftest (as on the developer machine: the selftest runs a private displayd) and, in device mode, displayd reading virtio-gpu's modes.  
4. telephonyd incoming-call hook → displayd raises to 120 (**not implemented**).  
5. Put the §7 acceptance into CI or the release checklist (needs a real panel).

## 10. Decision record

| Item | Decision |
|----|------|
| Refresh rate | **60Hz baseline**; models with a 120Hz panel switch 60↔120 automatically (D3) |
| Policy daemon | **phone-displayd**: planned in Rust, written in Go like the other system services (a program of kario-system); nothing but the renderer needs Rust |
| Who switches the mode | The renderer (it is the DRM master); displayd only decides, the Shell passes it on |
| Fixed 120 only | The High setting, or during interaction |
| 60Hz-only panel | Fine for mass production (D3); displayd runs, switches nothing, and Settings has no row for it |
