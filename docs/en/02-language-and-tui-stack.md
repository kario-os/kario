> English translation of [the Chinese original](../02-language-and-tui-stack.md). The Chinese text is authoritative; keep both in sync.

# Languages and frameworks

| Layer | Technology | Status |
|----|------|------|
| telephonyd | **C++23** | Decided |
| phone-shell (formerly phone-tui) | **Go** + Bubble Tea: text renderer + broker + system UI | Decided; prototype ✓ ([13](13-prototype.md)) |
| phone-mediad / phone-keystored | Prototype in Go; for production, mediad in Rust or C (libcamera / V4L2), and keystored becomes an OP-TEE trusted application (C) | Prototype ✓; production recommendations in [11](11-platform-architecture.md) §12 |
| phone-displayd | **Rust** (DRM refresh-rate policy) | Decided; 60Hz baseline, 120Hz optional |
| phone-gui (closed source) | **Rust** + **Slint** (GPU) | Decided |
| Semantic UI model `phone.Ui` | Data structure + JSON-lines protocol (fd 3) | v0 prototype ✓ ([03](03-protocols-and-ui-path.md) §5) |
| App SDK | Go (prototype ✓); any language can connect directly (Python example ✓); a C ABI (for vendors reusing the C++ core) and Rust are planned | Partial |
| Third-party apps | Semantic apps (default); canvas apps (exception) hand over graphics in a `canvas` node and the system draws them | Semantic-app prototype ✓; canvas-app prototype ✓ |
| App scripting (QuickJS / Lua etc.) | Undecided | Deferred until after v2 |
| Cellular | **ModemManager** | Decided |
| Product image | **Yocto** Wrynose 6.0 LTS + kas, layer `yocto/meta-phone` | Prototype ✓ (qemuarm64 boot selftest passes, [13](13-prototype.md) §5.1) |

Not adopted: **Notcurses** as the UI layer on DRM — it has no DRM backend, and the Linux console cannot display Chinese ([09](09-ui-feasibility-review.md) §3.2).

IPC: [03-protocols-and-ui-path.md](03-protocols-and-ui-path.md). GUI: [06-gui-architecture.md](06-gui-architecture.md). Implementation: [05-implementation.md](05-implementation.md).
