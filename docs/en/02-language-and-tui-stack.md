> English translation of [the Chinese original](../02-language-and-tui-stack.md). The Chinese text is authoritative; keep both in sync.

# Languages and frameworks

| Layer | Technology | Status |
|----|------|------|
| telephonyd | **C++23** | Decided |
| phone-shell (formerly phone-tui) | **Go** + Bubble Tea: text renderer + broker + system UI | Decided; prototype ✓ ([13](13-prototype.md)) |
| phone-mediad / phone-keystored | Prototype in Go; for production, mediad in Rust or C (libcamera / V4L2), and keystored becomes an OP-TEE trusted application (C) | Prototype ✓; production recommendations in [11](11-platform-architecture.md) §12 |
| phone-displayd | **Rust** (DRM refresh-rate policy) | **Not implemented**; only needed if a 120Hz panel is chosen, 60Hz is the baseline |
| Pixel renderer phone-paint | **Rust** (tiny-skia, software rendering), Apache-2.0; phone-gui, kept for comparison, is Rust + Slint, GPL-3.0-only, and not in the device image | Prototype ✓ ([06](06-gui-architecture.md), [23](23-renderer-protocol.md)) |
| Semantic UI model `phone.Ui` | Data structure + JSON-lines protocol (fd 3) | v0 prototype ✓ ([03](03-protocols-and-ui-path.md) §5) |
| App SDK | TypeScript (the main language, KarioUI on kario-jsrt, [29](29-app-development.md)) ✓; a C ABI (`sdk/c`, for vendors reusing the C++ core) ✓; Go ✓; any language can connect directly (Python example ✓); Rust for native modules, an SDK for whole apps in Rust **not implemented** | Prototype ✓ |
| Third-party apps | Semantic apps (default); canvas apps (exception) hand over graphics in a `canvas` node and the system draws them | Semantic-app prototype ✓; canvas-app prototype ✓ |
| App scripting | QuickJS-ng: the engine of TypeScript apps and mini-programs ([29](29-app-development.md)) | Decided; prototype ✓ |
| Cellular | **ModemManager** | Decided |
| Product image | **Yocto** Wrynose 6.0 LTS + kas, layer `yocto/meta-phone` | Prototype ✓ (qemuarm64 boot selftest passes, [13](13-prototype.md) §5.1) |

Not adopted: **Notcurses** as the UI layer on DRM — it has no DRM backend, and the Linux console cannot display Chinese ([09](09-ui-feasibility-review.md) §3.2).

IPC: [03-protocols-and-ui-path.md](03-protocols-and-ui-path.md). GUI: [06-gui-architecture.md](06-gui-architecture.md). Implementation: [05-implementation.md](05-implementation.md).
