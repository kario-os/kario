> English translation of [the Chinese original](../05-implementation.md). The Chinese text is authoritative; keep both in sync.

# Implementation guide (start here)

## Decided technology stack

| Component | Technology |
|------|------|
| telephonyd | **C++23**, CMake, sdbus-c++ or GIO D-Bus |
| phone-tui | **Go** + Bubble Tea + godbus |
| Cellular | **telephonyd**, with a backend per chip: ModemManager / Android radio interface HAL / vendor interface (61 (internal document) §3.2) |
| Development and validation | A **4G development board / reference-design phone** from the chip vendor or a design house + SIM (no custom PCB for now, 61 (internal document) §7) |
| Protocol | `phone.*` D-Bus v0 ([03-protocols-and-ui-path.md](03-protocols-and-ui-path.md)) |
| phone-gui (after the TUI) | **Rust + Slint**, [06-gui-architecture.md](06-gui-architecture.md) |

telephonyd uses **C++23**, consistent with `example/hello`; cross-compilation reuses the same gcc-toolset / preset conventions.

---

## Repository layout (in creation order)

Under `kario/` in the repository:

```
kario/
├── ipc/
│   └── phone-ipc-v0.md          # copied from 09, with field types refined
├── telephonyd/                  # C++23
│   ├── CMakeLists.txt
│   ├── src/main.cpp
│   ├── src/mm_client.cpp        # ModemManager D-Bus
│   └── src/telephony_service.cpp # exports phone.Telephony
├── phone-tui/                   # Go module
│   ├── go.mod
│   ├── main.go
│   └── internal/ui/             # Bubble Tea screens
└── systemd/
    ├── telephonyd.service
    └── phone-tui.service
```

---

## Phase 0 — Desktop interaction prototype (no hardware to buy; can run in parallel with Phase A)

1. Open a window at the target resolution on the desktop (320×240 landscape, 480×640 portrait) that accepts keyboard input only.
2. Following [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md), implement a minimal Shell: focus, OK, Menu key, Options soft key, back stack, type-to-search.
3. Sample apps with fake data: a "Bilibili-style" recommendation feed → video page → comments; an "IM-style" conversation list → chat → message menu → image viewer; the three phone screens.
4. One pixel version (Slint) and one text version (Bubble Tea).

**Acceptance:** after a week of continuous use, every action in the sample apps can be done with keys, with no dead ends ([09](09-ui-feasibility-review.md) §7). Once this passes, freeze `phone.Ui` v0.

**Status**: both the text and pixel versions are done and have been extended into the platform prototype ([13](13-prototype.md): `make run / make selftest`; for the pixel renderer see [06](06-gui-architecture.md); the Yocto image passes the boot selftest in QEMU, see 13 §5.1). The one-week self-use log has not been done yet (57 (internal document) R9).

---

## Phase A — Development board can make calls (no in-house code)

**Environment:** the board vendor's system (UNISOC Yocto BSP, vendor Android, or postmarketOS on Qualcomm mainline); following the chip vendor's documentation, complete one VoLTE outgoing call / answered call, confirming that the baseband, IMS and call audio work on this board on their own.

**Acceptance:** network registration + one successful VoLTE outgoing call / answered call. Record the board, BSP version and baseband control interface in `phone/LOG.md`.

---

## Phase B — telephonyd v0 (C++23)

1. **CMake project**  
   - `cmake_minimum_required` 3.25+, `CXX_STANDARD 23`.  
   - Dependencies: `sdbus-c++`, or `pkg-config` to find `gio-2.0`.  
   - Link against and listen on the **system D-Bus**.

2. **Implement `phone.Telephony` v0** (the object path `/org/phone/Telephony` can be hard-coded at first)  
   - On startup, connect to ModemManager and cache the modem index.  
   - **properties** (read-only): `Registration`, `SignalQuality` (mapped from MM).  
   - **methods**: `Dial(s number)`, `Hangup()`, `Answer()`, `Reject()`.  
   - **signals**: `CallStateChanged`, `IncomingCall(number)`.

3. **Local test** (dev machine or board)  
   ```bash
   cmake -B build -DCMAKE_BUILD_TYPE=Debug
   cmake --build build
   sudo ./build/telephonyd
   busctl introspect org.phone /org/phone/Telephony
   ```

**Acceptance:** `busctl call` can trigger dial/hang-up; on an incoming call the signal shows up in `busctl monitor`.

---

## Phase C — phone-tui v0 (Go)

1. `go mod init` (fill in the path to match the actual monorepo).  
2. **godbus** connects to `phone.Telephony`; subscribe to `IncomingCall`.  
3. **Bubble Tea** three screens:  
   - **Idle**: status bar (registration, signal), number entry, soft key 「拨打」 (Call)  
   - **InCall**: call duration, hang up  
   - **Incoming**: full-screen overlay, answer/reject  

4. Runs on the same machine as telephonyd; systemd starts telephonyd first, then phone-tui.

**Acceptance:** operating the TUI from the keyboard/SSH completes dialing and answering; the incoming-call overlay pops up.

Status: the Shell is now a general-purpose `phone.Ui` renderer, and the three phone screens are an ordinary semantic app (`apps/dialer`; the incoming-call screen is system UI).

---

## Phase D — Audio and power (still on the development board)

1. During a call, route the baseband's voice to the earpiece / speaker (ALSA UCM or the vendor audio HAL, per the chip vendor's documentation).  
2. Read `power_supply` or upower and show the battery level in the TUI status bar (it can bypass D-Bus at first; add `phone.Power` to telephonyd later).

**Acceptance:** call audio quality is acceptable; the battery level is shown correctly.

---

## Phase E — phone-displayd + phone-gui (Slint)

**Hardware:** a **DSI panel of the same kind as in production, 60Hz baseline** (D3). The 60↔120 steps below are carried out only if a panel with dual 60/120 modes (or VRR) is chosen; the specification is [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md).

1. Confirm the **60Hz and 120Hz** modes (or VRR) with `modetest`.  
2. `kario/phone-displayd/`: DRM switching + **org.phone.Display**; raise to **120Hz** on an incoming call.  
3. Empty full-screen Slint: verify **60↔120** and `RefreshRateChanged`.  
4. `phone-client/` + `phone-gui/`: Idle / Incoming / InCall; interaction hint.  
5. **fps-bench** + `design-system/` (08 §7); camera texture (08 §5).

**Acceptance:** [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md) §7; incoming call < 100ms.

Slint licensing: [06-gui-architecture.md](06-gui-architecture.md).

---

## Phase F — Custom hardware

Following 53-phased-roadmap.md (internal document): matrix keypad, LCD, codec; **IPC does not change**. The GPU driver and the Slint backend are pinned in the BSP.

---

## Weekly checklist

| Week | Goal |
|----|------|
| 0 (about 4 weeks, can run in parallel) | Phase 0 desktop interaction prototype |
| 1 | Phase A; `kario/` + `phone-ipc-v0.md` |
| 2 | telephonyd Dial/Hangup |
| 3 | phone-tui Idle + InCall |
| 4 | Incoming-call overlay + systemd |
| 5–6 | Audio, battery level |
| 7+ | phone-displayd + phone-gui; peripheral matrix and [07](07-ecosystem-and-store.md) |

---

## Relationship with `example/hello`

- telephonyd's CMake can be built standalone, or `kario/telephonyd` can later be hooked into a hello-style preset (optional).  
- The telephony **state machine** can follow the enum and transition-table style of `example/fsm`, and lives inside `telephonyd`.

---

## Document index

| File | Contents |
|------|------|
| [09-ui-feasibility-review.md](09-ui-feasibility-review.md) | Overall review, decisions D1–D3 |
| [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md) | Key interaction grammar, `phone.Ui` |
| [03-protocols-and-ui-path.md](03-protocols-and-ui-path.md) | D-Bus interfaces |
| [04-ui-language-telephonyd.md](04-ui-language-telephonyd.md) | Role of telephonyd |
| 56-product-strategy.md (internal document) | Open-source core / closed-source upper layers |
