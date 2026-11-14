> English translation of [the Chinese original](../README.md). The Chinese text is authoritative; keep both in sync.

# Kario documentation

Kario is a keyboard-first phone system: apps describe a semantic UI (`phone.Ui`), and the system takes care of rendering, the input method, permissions and secure UI. These are the documents for developers and device makers; the code is in the parent directory of `docs/`. Start with [05](05-implementation.md) (implementation guide) and [13](13-prototype.md) (how to run the prototype); for app development see [22](22-developer-api.md).

This directory holds the English translations, under the same file names as the Chinese originals, which are one directory up (`docs/`).

License: CC BY 4.0. References marked "internal document" point to non-public business and planning documents. These are the English versions of [the Chinese documents](../README.md), which are authoritative; `scripts/docs-en-check.py` checks that both have the same structure (headings, table rows, code blocks).

Document numbers are split into ranges by repository: 01–50 here, 51–99 for the internal documents (90 and up are drafts and appendices), so each number refers to exactly one document; a new document takes the next free number in its range.

| Document | Contents |
|------|------|
| [01-software-stack.md](01-software-stack.md) | Software stack |
| [02-language-and-tui-stack.md](02-language-and-tui-stack.md) | Languages and frameworks |
| [03-protocols-and-ui-path.md](03-protocols-and-ui-path.md) | Protocols and the UI path |
| [04-ui-language-telephonyd.md](04-ui-language-telephonyd.md) | telephonyd · UI |
| [05-implementation.md](05-implementation.md) | Implementation guide (start here) |
| [06-gui-architecture.md](06-gui-architecture.md) | GUI architecture (keyboard-first pixel UI) |
| [07-ecosystem-and-store.md](07-ecosystem-and-store.md) | Ecosystem and app store |
| [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md) | Display infrastructure: 60–120Hz adaptive refresh |
| [09-ui-feasibility-review.md](09-ui-feasibility-review.md) | Overall review: can a TUI carry complex apps · merging the two proposals |
| [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md) | Key interaction grammar and the semantic UI model |
| [11-platform-architecture.md](11-platform-architecture.md) | Platform architecture: a keyboard-first system at super-app scale |
| [12-super-app-capabilities.md](12-super-app-capabilities.md) | Super-app capability catalog: capabilities used by WeChat / Alipay → platform capabilities |
| [13-prototype.md](13-prototype.md) | Code prototype: phone.Ui + TUI Shell + super-app samples |
| [14-tui-route-and-app-framework.md](14-tui-route-and-app-framework.md) | Second review: the TUI technical route and the app framework (compared with DeepSeek Harness / dsh-TUI) |
| [15-web-vs-semantic-benchmark.md](15-web-vs-semantic-benchmark.md) | Measured comparison: the current implementation (semantic UI) vs the web route |
| [16-input-method.md](16-input-method.md) | Input method: T9 keypad and QWERTY keyboard, Chinese and English, candidates and predictions |
| [17-app-sandbox.md](17-app-sandbox.md) | Restrictions on app behavior: sandbox, permissions, background |
| [18-nokia-form-factors.md](18-nokia-form-factors.md) | Nokia form factors and keys: T9 keypad, QWERTY keyboard, aligning input switching |
| [19-core-apps.md](19-core-apps.md) | Core apps and platform capability check |
| [20-audio.md](20-audio.md) | Audio service (R23) |
| [21-push.md](21-push.md) | Push service (R6) |
| [22-developer-api.md](22-developer-api.md) | Developer documentation: capabilities and interfaces |
| [23-renderer-protocol.md](23-renderer-protocol.md) | Renderer protocol (phone.frame/1): a replaceable pixel UI |
| [24-sku-framework.md](24-sku-framework.md) | SKU framework: one hardware configuration produces one firmware package |
| [25-system-update.md](25-system-update.md) | System update (OTA): A/B system slots and dm-verity |
| [26-design-system.md](26-design-system.md) | phone-paint's design system: color schemes, font sizes, spacing, corner radii and contrast rules of the four themes |
| [27-usb-typec.md](27-usb-typec.md) | USB-C: PD fast charging, keyboards and mice (plugged in later, a mouse used as keys), sharing files with a computer once trusted, as on an iPhone (network sharing or MTP file transfer); compared with how an iPhone connects to a computer for debugging |
| [28-battery.md](28-battery.md) | Battery life: the screen going off and waking on a key, the notification light's slow blink (instead of lighting the screen), the Shell waking less |
| [29-app-development.md](29-app-development.md) | The route for the app development experience: the system middleware as the only way in, TypeScript as the main language (the QuickJS-ng engine), the declarative framework KarioUI, a command line and a VS Code extension, the simulator, guidelines and publishing; P1–P3 done on the developer machine, P4 in part |
| [30-nfc.md](30-nfc.md) | NFC card emulation: door cards (copied UID cards), transit cards (the express transit card works with the screen off), payment cards (the default payment app, unlocked); the system app 卡包 (card pack) manages the cards; locked with the screen off, door cards and the express transit card answer by themselves, the card most likely at that time of day first, the next one when a reader refuses; a double press of the power key picks a card by hand; apps answer readers through `nfc.apdu` / `nfc.reply` (simulated controller, real hardware not implemented) |
| [31-lock-and-biometrics.md](31-lock-and-biometrics.md) | Lock screen and biometrics: the lock-screen PIN (locking when the screen goes off, emergency calls while locked, notifications shown as counts), fingerprint unlock, fingerprint payments (a fingerprint-bound key, voided when a new finger is enrolled); simulated sensor, TEE not implemented |
| [32-messaging.md](32-messaging.md) | Messaging: the system service phone-smsd keeps SMS (conversations, read and unread, deleting, search, the blocked folder, delivery reports, verification codes), telephonyd only does the radio side and lets go of a received message once smsd stored and acknowledged it; emergency alerts (cell broadcast) come up over the lock screen too, the national level cannot be switched off; only the default SMS app deletes and blocks (simulated baseband; the ModemManager side's tests not run, not verified on hardware) |
| [33-app-guidelines.md](33-app-guidelines.md) | App guidelines: how to design the interface, how the store reviews (the automatic checks and the human review's standards), privacy labels (not implemented, a design) |
| [34-app-tutorial.md](34-app-tutorial.md) | Tutorial: an app with KarioUI step by step, debugging with `kario run`, testing with `kario test`, packaging and publishing, the Kario Studio extension |
