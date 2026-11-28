> English translation of [the Chinese original](../03-protocols-and-ui-path.md). The Chinese text is authoritative; keep both in sync.

# Protocols and UI path

Implementation: [05-implementation.md](05-implementation.md). GUI: [06-gui-architecture.md](06-gui-architecture.md).

## 1. Technology stack

| Component | Technology |
|------|------|
| telephonyd | **C++23**, ModemManager D-Bus |
| phone-tui | **Go** + Bubble Tea |
| phone-paint | **Rust** (software rendering), Apache-2.0; phone-gui, kept for comparison, is Rust + Slint, GPL-3.0-only |
| IPC (system services) | JSON lines over Unix sockets (the `wire` package); no D-Bus `phone.*` interface |
| IPC (UI) | **`phone.Ui`**: semantic tree from app → Shell, over the socketpair the Shell hands the app (fd 3, §5) |

## 2. Boundaries

- The Shell reaches telephony, audio and the like only through the system services' JSON-lines sockets, and third-party apps only through the Shell's broker (`req` in `phone.Ui`); neither talks to the modem directly.  
- telephonyd has exclusive ownership of ModemManager.  
- Audio: the system service phone-audiod ([20](20-audio.md)); PipeWire is **not implemented**.  
- Semantic apps do not draw their own UI: they hand over content and actions via `phone.Ui`, and the Shell renders them and handles keys ([10](10-keyboard-interaction-model.md)).

## 3. Linux mapping

| Capability | Upstream |
|------|------|
| Cellular | ModemManager |
| Audio | PipeWire or PulseAudio |
| Power | power_supply / upower |
| Input | evdev → the pixel renderer (phone-paint's input helper) → Shell |
| Display | DRM/KMS (phone-paint holds the panel and switches the display mode); phone-displayd decides the refresh rate ([08](08-display-adaptive-refresh.md)): 60Hz baseline, switching automatically where there is 120Hz, ✓ on the simulated 60/120Hz panel, **not tried on a phone** |

## 4. `phone.*` D-Bus v0

> Current state: this section is an early draft, **not implemented**. System services have no D-Bus interface: the Shell and the services talk JSON lines over Unix sockets (the `wire` package), with the methods in each service's document (telephony [04](04-ui-language-telephonyd.md), audio [20](20-audio.md), SMS [32](32-messaging.md)); `phone.Display` did not become D-Bus either: phone-displayd is JSON lines on `displayd.sock`, methods `state`, `setting`, `hint`, `cap`, `panel`, event `rate` (`svc/displayd.go`, [08](08-display-adaptive-refresh.md) §4).

```
phone.Telephony v1
  properties: registration, signal, operator, sim_state
  methods:    dial, hangup, answer, reject, send_dtmf
  signals:    incoming_call, call_state_changed, sim_changed

phone.Power v1
  properties: percent, charging
  signals:    low_battery

phone.Audio v1
  methods:    set_call_volume, set_route (earpiece | speaker)
  signals:    route_changed

phone.Display v1
  properties: CurrentRefreshRate, MaxRefreshRate, Policy
  methods:    HintInteractionBegin, HintInteractionEnd, HintAnimationUntil
  signals:    RefreshRateChanged
```

Details: [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md). Versioning: major for breaking changes; minor only adds fields.

## 5. `phone.Ui` v0 (implemented in the prototype)

Definitions: [ui/proto.go](../../ui/proto.go) (UI), [ui/caps.go](../../ui/caps.go) (capabilities). Nodes: `text / section / item / input / button / image / media / doc / progress` (`doc` is Markdown, rendered by the system; `progress` is a progress bar, `fraction` 0–1); every actionable node carries `actions[]` (`id / label / key / primary / group / confirm`). An `input` can carry `keyboard` (`number / phone / email`), which tells the system input method (IME) which keyboard to use ([16](16-input-method.md) §4); an input field with `live` sends a `changed` action on every edit (search as you type). `Screen.columns` lays out consecutive `tile` items as a grid (home screen, calculator), and the arrow keys move two-dimensionally within the grid. An action can set `hidden`: it responds only to its shortcut key and does not appear in the menu. Grammar and examples: [10](10-keyboard-interaction-model.md) §4.

`req` can carry `origin`: it is filled in when a host app requests a capability on behalf of content it hosts (such as a mini-program), and the system dialog labels the request accordingly; authorization and responsibility still belong to the host app.

**Connection**: when the Shell starts an app it creates a socketpair and hands the child end to the app as fd 3 (environment variable `PHONE_UI_FD=3`). The app has no other way to reach system services; its identity is this connection.

| Direction | Messages |
|------|------|
| App → Shell | `hello` (protocol version), `push / replace / update / reset / pop`, `toast`, `notify`, `req` (capability request) |
| Shell → App | `start`, `action` (including the values of all input fields on that page), `popped`, `lifecycle`, `res`, `event`, `notif_action` |

**Manifest**: `permissions` (such as `camera`, `keystore`, `voip`, `phone`, `sms`, `contacts`; network access requires `net`), `background` (background mode; currently only `voip`: the app is not frozen after leaving the foreground), `granted` (pre-granted runtime permissions, effective only for apps in the system image). Sandbox and background rules: [17](17-app-sandbox.md).

**Capabilities** (`req.cap`, forwarded by the Shell broker after it checks the manifest permissions and runtime grants): `media.call.start / control / end`, `scan.qr`, `auth.confirm`, `keystore.sign`, `attest.get`, `perm.list / set` (system apps only); telephony `tel.dial / answer / hangup / hold / mute / calls / log` and SMS `sms.send / list / read / threads / alerts / delete / block / blocked` (via phone-smsd, [32](32-messaging.md); sending and receiving in telephonyd, [04](04-ui-language-telephonyd.md)); `contacts.list` (runtime permission 「通讯录」 (Contacts)), `contacts.save / delete / import / export` (each confirmed by the system); `alarm.set / cancel / list` (fired by the system at the set time); `display.hint` (the foreground app says it animates or its content's frame rate, by which the system picks the screen's refresh rate; rate-limited, [08](08-display-adaptive-refresh.md) §4.2). Full catalog and roadmap: [12](12-super-app-capabilities.md).

| Constraint | Prototype | Production |
|------|------|------|
| Serialization | JSON lines | Compact binary + delta updates (D-Bus is unsuitable for frequent large messages, so it is still not used) |
| Images and video | Nodes only hold `src` / `stream` references; pixels travel on the media plane | Same, dmabuf |
| Versioning | Handshake `phone.ui/0` | major for breaking changes; minor only adds fields |

## 5.1 Renderer protocol (Shell ↔ pixel renderer, implemented in the prototype)

Between the Shell and the pixel renderer there is a separate JSON-lines connection (the renderer's stdin / stdout). It has nothing to do with apps, and apps cannot reach it. The specification is `phone.frame/1` ([23](23-renderer-protocol.md)); the renderer on the device is phone-paint, and phone-gui is kept for comparison. The main messages:

| Direction | Messages |
|------|------|
| Shell → renderer | `hello` (device form factor: pixels, scale, keyboard), `frame` (current screen: status bar, title, nodes, focus, scroll intent, soft keys, candidate bar, toast, topmost system UI), `patch` (incremental frame), `snap` (write a PNG), `rate` (the refresh rate phone-displayd chose: the renderer switches the panel's mode and paces its frames by its period), `quit` |
| Renderer → Shell | `ready`, `key` (key name or text; `long` means a long press), `snapped`, `rate` (the rate it switched to), `motion` (an animation begins, stops: the Shell passes it on to phone-displayd) |

All messages and the versioning rules: [23](23-renderer-protocol.md); the Shell side is in `shell/frame.go` and `shell/gui.go`; design notes in [06](06-gui-architecture.md) §8.

## 6. Third parties

- **Semantic apps** (default): describe their UI via `phone.Ui`; capabilities go as `req` on fd 3 to the Shell's broker (§5).  
- **Canvas apps** (exception): put a `canvas` node in the page and hand over graphics, which the system draws; while the canvas has the focus, keys go to the app, and the app must handle key events ([22](22-developer-api.md) §3).  
- **JS / TypeScript mini-programs** (D5): hosted by a host app; the JS requests capabilities through the host's `phone.*` bridge, which goes through the same broker with `origin` on the request; the view is likewise made of `phone.Ui` nodes ([14](14-tui-route-and-app-framework.md) §8.3). In web pages, `phone.call` offers only the capabilities on a whitelist; the Web view component ✓ ([14](14-tui-route-and-app-framework.md) §5) goes through the broker too. Package format: [07-ecosystem-and-store.md](07-ecosystem-and-store.md).
