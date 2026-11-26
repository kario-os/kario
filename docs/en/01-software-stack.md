> English translation of [the Chinese original](../01-software-stack.md). The Chinese text is authoritative; keep both in sync.

# Software stack

56-product-strategy.md (internal document) · [05-implementation.md](05-implementation.md) · [06-gui-architecture.md](06-gui-architecture.md) · [09-ui-feasibility-review.md](09-ui-feasibility-review.md)

## Layers

```
┌────────────────────────────────────────────────────────────────────┐
│  Apps (separate processes) ── phone.Ui semantic tree + action list │
├────────────────────────────────────────────────────────────────────┤
│  Shell: focus · keys · menu · soft keys · back stack · tasks       │
│    phone-paint (Rust, pixels) · text renderer (Go)                 │
├──────────────────────────────┬─────────────────────────────────────┤
│  phone-displayd              │  Media services (on demand)         │
│  not implemented (120 Hz)    │  images / video / PDF / camera      │
├──────────────────────────────┴─────────────────────────────────────┤
│  telephonyd (C++23)  →  JSON lines (Unix socket)                   │
├────────────────────────────────────────────────────────────────────┤
│  telephonyd backends (per chip) · PipeWire/Pulse                   │
├────────────────────────────────────────────────────────────────────┤
│  Kernel: baseband panel evdev codec GPU V4L2 …                     │
└────────────────────────────────────────────────────────────────────┘
```

## Shell and apps

Apps only describe their content and the actions of each element; key mapping, the options menu, soft keys and Back are handled centrally by the Shell, so every app can be operated completely with keys. Grammar and node table: [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md).

## TUI

The Shell (Go + Bubble Tea) is a general-purpose `phone.Ui` text renderer: lists, soft keys, overlays (full-screen incoming call), with images degraded to `[图]` (image); it is used for SSH / serial console / debugging. The same logic drives the pixel renderer through the frame protocol ([06](06-gui-architecture.md)).

The TUI is not run directly on DRM: the Linux console cannot display Chinese, and Notcurses has no DRM backend ([09](09-ui-feasibility-review.md) §3.2).

## GUI

phone-paint (in-house, Rust, Apache-2.0) draws the frames of `phone.Ui` (the renderer protocol `phone.frame/1`, [23](23-renderer-protocol.md)), today in software; GPU rendering is **not implemented**; phone-gui (Slint, GPL-3.0-only), kept for comparison, is not in the device image. **phone-displayd**, which manages the refresh rate centrally ([08-display-adaptive-refresh.md](08-display-adaptive-refresh.md); 60Hz baseline, 120Hz optional), is **not implemented** and is only needed if a 120Hz panel is chosen. The Shell and Kario's own system services talk JSON lines over Unix sockets (the `wire` package); there is no D-Bus `phone.*` interface, and D-Bus is used only to reach outside services such as iwd, BlueZ and ModemManager.

## Media services

Separate processes, started on demand and destroyed after use (adopted from the new draft). They produce the picture for `Image` / `MediaSurface` nodes; video and camera can be passed straight through to DRM hardware planes to save power, falling back to GPU composition when there are not enough planes.
