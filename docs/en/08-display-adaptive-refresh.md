> English translation of [the Chinese original](../08-display-adaptive-refresh.md). The Chinese text is authoritative; keep both in sync.

# Display infrastructure: 60–120Hz adaptive refresh

> **D3 (decided): 60Hz baseline; once a suitable 120Hz panel is chosen, enable the 60↔120 adaptive policy described in this document.** Rationale: as far as we currently know, high-refresh panels in consumer products basically start at around 5.9 inches, ready-made 120Hz DSI panels at 3.5 inches or below are almost impossible to find (to be verified with procurement), and custom panels have very high minimum order quantities; key interaction moves in discrete steps, so the benefit of high refresh is far smaller than for finger dragging. The design in which `phone-displayd` exclusively owns the refresh rate and apps can only send hints still applies. So the "120Hz peak" in §1 / §2 only applies to models that chose a 120Hz panel; 60Hz panels are fine for mass production. Details: [09-ui-feasibility-review.md](09-ui-feasibility-review.md) §6.

**Display infrastructure** aligned with mainstream platforms: the panel goes **up to 120Hz**, and the system **switches automatically between 60Hz and 120Hz** — static content saves power, interaction and animation run at full refresh, and the experience has the same semantics as flagships (ProMotion / adaptive high refresh).

Related: 52-hardware-architecture.md (internal document), [06-gui-architecture.md](06-gui-architecture.md), [05-implementation.md](05-implementation.md).

## 1. Product definition

| Item | Requirement |
|----|------|
| Panel capability | **120Hz peak**; supports at least **two DRM modes, 60Hz and 120Hz** (or **VRR 48–120Hz**, see §2.1) |
| System policy | **Automatic**: interaction/animation → **120Hz**; static reading/idle screen → **60Hz** |
| UI at 120Hz | Benchmark scenarios **1% low ≥ 110fps** (§7) |
| UI at 60Hz | Benchmark scenarios **1% low ≥ 55fps**; spinning the GPU at 120fps while in 60Hz mode is **forbidden** |
| Touch to first frame | When triggered by a key press/touch, **request 120Hz immediately** (see §4) |
| Full-screen incoming call | **120Hz** + first Incoming frame **< 100ms** |

The TUI phase can use a 60Hz development board; **on models with a 120Hz panel, phone-gui and the BOM must support dual 60/120 modes (or VRR)**, and §2 lists the constraints for such models; models with only a 60Hz panel can go into mass production (D3).

## 2. Hardware (hard BOM constraints)

### 2.1 Panel

| Component | Requirement |
|------|------|
| Interface | **MIPI DSI**; SPI/I8080 color panels **are not used for the production GUI** |
| Refresh | **120Hz** in the datasheet, with explicitly specified **60Hz timing** or **VRR range** |
| LTPO | **Not mandatory** in v1; if present, v2 extends `phone-displayd` with fine-grained levels |
| Backlight | PWM must not produce visible beating against the current refresh rate (test while switching 60/120) |

### 2.2 SoC / GPU

| Component | Requirement |
|------|------|
| DRM/KMS | **atomic modesetting**; `modetest` shows **60Hz and 120Hz** modes (or a single VRR mode) |
| GPU | **GLES 3.x / Vulkan**; stable composition at both refresh rates |
| Bandwidth | **120Hz peak** meets the §6 frame budget |

### 2.3 Forbidden

- When a 120Hz panel is chosen, a panel whose only mode is **120Hz** (it cannot drop to 60Hz on static screens, so it saves no power).  
- No **phone-displayd**, with apps changing the refresh rate at will via `ioctl` (the policy must be centralized).

## 3. Software stack

```
evdev ──► phone-gui (Slint) ──hint──► phone-displayd (Rust)
              │                              │
              │ GPU                          │ DRM atomic
              ▼                              ▼
Slint schedules frames at current Hz   60Hz ⟷ 120Hz
                                             │
                                           panel
telephonyd ──incoming call──► phone-displayd (force 120Hz)
phone.Power ──low battery──► phone-displayd (may cap at 60Hz)
```

| Process | Responsibility |
|------|------|
| **phone-displayd** | The **only** process allowed to switch the DRM refresh rate; executes the §5 policy table |
| **phone-gui** | Slint drawing; reports **interaction/scroll/animation** hints; **throttles** redraws to the current Hz |
| **telephonyd** | Notifies displayd of incoming-call/call state (D-Bus or internal signal) |
| **Kernel** | Panel driver + dual modes or VRR |

**Slint:** at 120Hz it follows vsync at full rate; at 60Hz it **redraws only damaged regions**, and commits no empty frames when there is no interaction.

## 4. D-Bus: `org.phone.Display` v1

Owned by **phone-displayd**.

```
properties:
  CurrentRefreshRate   u32   # 60 or 120
  MaxRefreshRate       u32   # 120
  Policy               s     # auto | fixed120 | fixed60

methods:
  HintInteractionBegin()
  HintInteractionEnd()
  HintAnimationUntil(u64 deadline_ms_monotonic)

signals:
  RefreshRateChanged(u32 hz)
```

**phone-gui** calls `HintInteractionBegin` when a key press/touch/scroll starts, and `HintInteractionEnd` after **≥ 400ms** idle with no input and no animation.

**phone-runtime** (third party): only `HintInteractionBegin/End` is allowed; changing `Policy` directly is **forbidden** (except for the built-in Settings app).

## 5. Automatic policy (`Policy=auto`)

| Condition | Refresh rate |
|------|--------|
| Touch/key down, list scrolling, UI animation in progress | **120** |
| Incoming call ringing, in-call UI, within **3s** after waking from sleep | **120** |
| Static screen with no animation for **≥ 400ms** | **60** |
| User setting 「省电」 (Power saving) or battery **< 15%** (configurable) | **60** (raising to 120 can be blocked until charging) |
| Setting `Policy=fixed120` / `fixed60` | Locked |
| Screen off / deep sleep | Panel off, Hz does not apply |

**Raising** the rate happens immediately; **lowering** it is delayed by **400ms** to avoid jitter at the boundary.

Camera preview: the preview texture is updated at **30fps**; the UI chrome while browsing goes up and down according to the §5 table (scrolling the gallery raises it to 120).

## 6. Frame budget

| Mode | Frame period | Slint+GPU drawing budget (indicative) |
|------|--------|---------------------------|
| **120Hz** | 8.33ms | ≤ 5ms drawing + 2ms commit |
| **60Hz** | 16.67ms | ≤ 10ms drawing + 3ms commit |

telephonyd **must not** occupy the UI core and cause dropped frames at 120Hz.

## 7. Acceptance

**When 120Hz is in effect** (forced by the policy or during interaction), the §7 scenarios must reach **1% low ≥ 110fps**:

1. Inertial scrolling in Contacts for 5s  
2. Pressing the arrow keys repeatedly in Settings for 3s  
3. Idle ↔ Dialer transition ×20  
4. Incoming-call Incoming screen popping up  

**When 60Hz is in effect** (measured after 5s of static Idle): the **average power** of the Idle screen is **lower** than with a fixed 120Hz at the same brightness (record the measured percentage in `LOG.md`); frame rate **≥ 55fps** for the redraw after a single key press.

**Switching:** 60→120 enters the 120Hz mode within **≤ 2 frames** after the first key press (compare the `RefreshRateChanged` timestamp with the input event).

Tools: `kario/tools/fps-bench/`, `PHONE_DISPLAY_TRACE=1` (implementation phase).

## 8. Alignment with mainstream platforms

| Infrastructure | Mainstream flagships | This project |
|----------|----------|--------|
| Adaptive high refresh | ProMotion / LTPO / Android adaptive refresh | **60 ⟷ 120 automatic** + `phone-displayd` |
| GPU UI | Native | **Slint + GPU** |
| Centralized policy | System compositor / SF | **phone-displayd** |
| Separate telephony service | Yes | **telephonyd** |

We align on **refresh-rate policy + GPU UI + service-based architecture**, not on app store scale.

## 9. Implementation (see [05](05-implementation.md) Phase E)

1. Confirm **60/120** dual mode or VRR in the kernel.  
2. **phone-displayd** switches the mode + emits `RefreshRateChanged`.  
3. **phone-gui** subscribes to Hz, adjusts the Slint redraw policy + sends hints.  
4. telephonyd incoming-call hook → displayd raises to 120.  
5. Put the §7 acceptance into CI or the release checklist.

## 10. Decision record

| Item | Decision |
|----|------|
| Refresh rate | **60Hz baseline**; models with a 120Hz panel switch 60↔120 automatically (D3) |
| Policy daemon | **phone-displayd** (Rust) |
| Fixed 120 only | Developer `fixed120`, or during interaction |
| 60Hz-only panel | Fine for mass production (D3) |
