> English translation of [the Chinese original](../03-protocols-and-ui-path.md). The Chinese text is authoritative; keep both in sync.

# Protocols and UI path

Implementation: [05-implementation.md](05-implementation.md). GUI: [06-gui-architecture.md](06-gui-architecture.md).

## 1. Technology stack

| Component | Technology |
|------|------|
| telephonyd | **C++23**, ModemManager D-Bus |
| phone-tui | **Go** + Bubble Tea |
| phone-gui | **Rust** + **Slint** (GPU, closed source) |
| IPC (system services) | **D-Bus `phone.*`** |
| IPC (UI) | **`phone.Ui`**: semantic tree from app → Shell, Unix socket (draft) |

## 2. Boundaries

- TUI / GUI / third-party apps **only call `phone.*`** and never talk to the modem directly.  
- telephonyd has exclusive ownership of ModemManager.  
- Audio: PipeWire/Pulse; the UI goes through `phone.Audio`.  
- Semantic apps do not draw their own UI: they hand over content and actions via `phone.Ui`, and the Shell renders them and handles keys ([10](10-keyboard-interaction-model.md)).

## 3. Linux mapping

| Capability | Upstream |
|------|------|
| Cellular | ModemManager |
| Audio | PipeWire or PulseAudio |
| Power | power_supply / upower |
| Input | evdev → phone-tui / phone-gui |
| Display | DRM/KMS; **org.phone.Display** (phone-displayd); 60Hz baseline, 120Hz optional |

## 4. `phone.*` D-Bus v0

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

**Capabilities** (`req.cap`, forwarded by the Shell broker after it checks the manifest permissions and runtime grants): `media.call.start / control / end`, `scan.qr`, `auth.confirm`, `keystore.sign`, `attest.get`, `perm.list / set` (system apps only); telephony `tel.dial / answer / hangup / hold / mute / calls / log` and SMS `sms.send / list / read / threads / alerts / delete / block / blocked` (via phone-smsd, [32](32-messaging.md); sending and receiving in telephonyd, [04](04-ui-language-telephonyd.md)); `contacts.list` (runtime permission 「通讯录」 (Contacts)); `alarm.set / cancel / list` (fired by the system at the set time). Full catalog and roadmap: [12](12-super-app-capabilities.md).

| Constraint | Prototype | Production |
|------|------|------|
| Serialization | JSON lines | Compact binary + delta updates (D-Bus is unsuitable for frequent large messages, so it is still not used) |
| Images and video | Nodes only hold `src` / `stream` references; pixels travel on the media plane | Same, dmabuf |
| Versioning | Handshake `phone.ui/0` | major for breaking changes; minor only adds fields |

## 5.1 Renderer protocol (Shell ↔ phone-gui, implemented in the prototype)

Between the Shell and the pixel renderer there is a separate JSON-lines connection (phone-gui's stdin / stdout). It has nothing to do with apps, and apps cannot reach it:

| Direction | Messages |
|------|------|
| Shell → phone-gui | `hello` (device form factor: pixels, scale, keyboard), `frame` (current screen: status bar, title, nodes, focus, scroll intent, soft keys, candidate bar, toast, topmost system UI), `snap` (write a PNG), `quit` |
| phone-gui → Shell | `ready`, `key` (key name or text; `long` means a long press), `snapped` |

Definitions in `shell/frame.go` and `shell/gui.go`; design notes in [06](06-gui-architecture.md) §8.

## 6. Third parties

- **Semantic apps** (default): describe their UI via `phone.Ui`; capabilities go through `phone-runtime` → D-Bus.  
- **Canvas apps** (exception): put a `canvas` node in the page and hand over graphics, which the system draws; while the canvas has the focus, keys go to the app, and the app must handle key events ([22](22-developer-api.md) §3).  
- **JS / TypeScript mini-programs** (D5): hosted by a host app; the JS requests capabilities through the host's `phone.*` bridge, which goes through the same broker with `origin` on the request; the view is likewise made of `phone.Ui` nodes ([14](14-tui-route-and-app-framework.md) §8.3). There is no general-purpose JS bridge for web pages; the Web view component (in design) reuses the same `phone.*` interface. Package format: [07-ecosystem-and-store.md](07-ecosystem-and-store.md).
