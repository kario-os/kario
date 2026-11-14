> English translation of [the Chinese original](../01-software-stack.md). The Chinese text is authoritative; keep both in sync.

# Software stack

56-product-strategy.md (internal document) · [05-implementation.md](05-implementation.md) · [06-gui-architecture.md](06-gui-architecture.md) · [09-ui-feasibility-review.md](09-ui-feasibility-review.md)

## Layers

```
┌────────────────────────────────────────────────────────────────────┐
│  Apps (separate processes) ── phone.Ui semantic tree + action list │
├────────────────────────────────────────────────────────────────────┤
│  Shell: focus · keys · menu · soft keys · back stack · tasks       │
│    phone-gui (Slint, pixels) · phone-tui (Go, text)                │
├──────────────────────────────┬─────────────────────────────────────┤
│  phone-displayd              │  Media services (on demand)         │
│  (Rust) refresh-rate policy  │  images / video / PDF / camera      │
├──────────────────────────────┴─────────────────────────────────────┤
│  telephonyd (C++23)  →  phone.* D-Bus                              │
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

Slint + GPU renders `phone.Ui`; **phone-displayd** manages the refresh rate centrally ([08-display-adaptive-refresh.md](08-display-adaptive-refresh.md); 60Hz baseline, 120Hz optional).

## Media services

Separate processes, started on demand and destroyed after use (adopted from the new draft). They produce the picture for `Image` / `MediaSurface` nodes; video and camera can be passed straight through to DRM hardware planes to save power, falling back to GPU composition when there are not enough planes.
