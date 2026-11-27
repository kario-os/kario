> English translation of [the Chinese original](../12-super-app-capabilities.md). The Chinese text is authoritative; keep both in sync.

# Super app capability catalog: capabilities used by WeChat / Alipay → platform capabilities

> Purpose: map "every capability that WeChat and Alipay use", item by item, onto platform interfaces, and record the prototype status and the production dependencies. Architecture: [11](11-platform-architecture.md); prototype: [13](13-prototype.md).
> Status column: **✓** implemented in the prototype with selftest cases; **Simulated** works end to end in the prototype, but the data source is simulated; **Design** designed only, **not implemented**.

## 1. UI and interaction

| Capability | Used by | Platform interface | Status |
|------|--------|----------|------|
| Semantic UI: lists, bubbles, forms, buttons, images, media surfaces | All | `phone.Ui` v0 nodes | ✓ |
| Context menu (= long-press), command palette, back stack, soft keys | All | Provided uniformly by the Shell | ✓ |
| Chinese / English input method (QWERTY keyboard / T9 keypad, candidates, predictions) | Chat, search, transfer notes | System input method; apps only receive the committed text; input fields declare `keyboard` (number / phone / email) ([16](16-input-method.md)) | ✓ (prototype, in-house engine + AOSP dictionary, user dictionary, frequency adjustment, fuzzy pinyin, [16](16-input-method.md) §5.1) |
| Article rich text (official-account articles) | WeChat | `doc` node (Markdown + Mermaid diagrams, rendered by the system); full H5 goes through the web view component | ✓ (Markdown, Mermaid, code highlighting, formulas); web view ✓ |
| Mini-program hosting | WeChat, Alipay | JS logic layer + `phone.Ui` view, run inside a host; `req.origin` source labeling; subtree delegation ([11](11-platform-architecture.md) §10) | ✓ (host + source labeling); subtree delegation ✓ |
| Screen reader / accessibility | — | The semantic tree can naturally be read aloud | Screen reader won't do (decided 2026-11, R39) |

## 2. Communication

| Capability | Used by | Platform interface | Status |
|------|--------|----------|------|
| Message push (received even when the app is not resident) | All | `push.register` + `push` event; one long-lived system connection in `pushd` + a push gateway ([21](21-push.md)) | ✓ (simulated gateway; end-to-end encryption and TLS ✓) |
| Wake on incoming call + full-screen system incoming-call screen | WeChat calls | A VoIP push wakes or launches the app → `notify{kind: call}` + `voip` permission | ✓ |
| In-call status bar indicator, one-key return to the call | WeChat calls | Shell status bar | ✓ |
| Cellular calls, SMS | System | `telephonyd` ([03](03-protocols-and-ui-path.md)–[05](05-implementation.md)) | ✓ (simulated modem, mock ModemManager) |
| SMS verification code autofill | Login | `sms.otp`: waits for the next message with a code; the system shows which app asks, the message and the code, and hands the code alone to the app once the user agrees ([32](32-messaging.md) §6.1) | ✓ (simulated baseband, dev-machine selftest; **not tried on a phone**); copying the code ✓ |
| Carrier one-tap login (verification of the device's own number) | Login | `net.cellular`: send a given request over cellular data | Design |

## 3. Audio and video

| Capability | Used by | Platform interface | Status |
|------|--------|----------|------|
| Video call session (local stream + remote stream + picture-in-picture) | WeChat | `media.call.start / control / end` | Simulated (synthetic frames, no network transport) |
| Camera capture | QR scanning, photos, calls | mediad camera | V4L2 ✓ (vivid on the emulator) |
| Decode and composite only the visible frames | All media | Shell subscription management | ✓ |
| Hardware codecs | Calls, video playback | `media.codec` (V4L2 M2M) | Design |
| Echo cancellation, noise suppression, automatic gain control | Calls, voice messages | Audio processing built into mediad | Design |
| App-produced frames (the vendor's own codec) | WeChat | `media.surface.produce`: shared buffers, composited by the system | Design |
| Recording, voice messages | WeChat | `audio.record`: runtime 「麦克风」 (Microphone) permission, status bar indicator, foreground or in-call only ([20](20-audio.md) §3.1) | ✓ (simulated microphone; Hongyan voice messages can be recorded and played back) |
| Audio focus (incoming call pauses music, notification sounds duck other audio) | All apps that play sound | `phone-audiod` category priorities ([20](20-audio.md)) | ✓ |
| Audio routing (earpiece / speaker / Bluetooth) | Calls | `audio.route` | Partial (interface ✓; earpiece and speaker switched through ALSA UCM on the OnePlus 6T, [20](20-audio.md) §10, **not tried on a phone**; Bluetooth needs PipeWire, **not implemented**) |
| Music playback, background playback | Music | `media.list` + `audio.play / control / state`; `background: ["audio"]` | ✓ |
| The app's own audio stream (decoded VoIP audio) | WeChat calls | `audio.stream`: a shared-memory ring buffer handed to the app via an fd; the data does not pass through the Shell | ✓ |
| Video playback | Bilibili | `video.open` | ✓ |

## 4. Payment and security

| Capability | Used by | Platform interface | Status |
|------|--------|----------|------|
| Device attestation (risk control checks "real device, not tampered with") | WeChat and Alipay login risk control | `attest.get{nonce}` | ✓ (software key) |
| User-confirmed signing (payments, transfers) | Alipay, WeChat Pay | `auth.confirm{key, prompt, payload}` | ✓ (software key) |
| Signing without confirmation (payment code) | Payment code | `keystore.sign` | ✓ |
| Trusted input (payment PIN) | Payments | System trusted confirmation screen | ✓ (production needs a TEE trusted input path) |
| Server-side verification library | Vendor servers | `keystore.VerifyAttestation / VerifyConfirm / VerifySign` | ✓ |
| Secure screen (blocks screenshots, screen recording, casting) | Payment code, PIN | Node `secure: true`; payment codes also have `bright` (full brightness, no dimming while the code is in front) and barcodes `bar:` (Code 128, for the till's laser scanner, [22](22-developer-api.md) §13); the codes the system draws take a whole number of screen pixels a module with their quiet zones, and a screen too small says so and gives the number | ✓; whole-screen blocking **not implemented** |
| QR scanning (returns only the result, not the frames) | Scan | `scan.qr` | ✓ (mediad decodes the camera's picture, tested with the developer machine's synthetic camera and the emulator's vivid; a real phone camera **not yet tried on a real device**) |
| Biometric payment | Fingerprint payment | `auth.confirm{key, bio_key, prompt, payload}`: once the user turns on fingerprints for payments, a match signs with the fingerprint-bound key and the payment PIN still works; enrolling or removing a finger voids that key, and confirming once with the payment PIN makes it again ([31](31-lock-and-biometrics.md) §5); integrating with IFAA / SOTER-style standards **not implemented**; on a keypad phone the sensor may be on the OK key (under the cap, reading a resting finger with no press), powered only in the system's fingerprint prompt and never for unlocking ([31](31-lock-and-biometrics.md) §4.5); proofs say `security` `software`, truthfully | ✓ (simulated sensor, keystored standing in for the TEE); real sensors and the TEE **not implemented** |
| Chinese national cryptographic algorithms SM2 / SM3 / SM4 | Financial compliance | keystore algorithm suite: app keys can choose SM2 (`alg: "sm2"`, SM3 digest) | SM2, SM3, SM4 ✓ (software implementation, SM4 constant-time); in the TEE **not implemented** |
| NFC card emulation (transit cards, access control, payment) | Alipay, WeChat | The manifest's `nfc` (AID groups, category) + the `nfc.apdu` event / `nfc.reply`; the system card pack, copied door cards, the express transit card, the default payment app ([30](30-nfc.md)) | Simulated (simulated controller and readers; the NCI backend on real hardware **not implemented**) |
| Inter-app payment invocation (the system proves the caller's identity) | Third-party apps launching a payment | `intent.invoke` / `intent.reply` ([22](22-developer-api.md) §4.10) | ✓ (Hongyan's transfer asks Feiqian to pay) |
| Resettable, per-app isolated device identifier | Risk control, ads | `device.id` (similar to OAID) | Design |

## 5. System integration

| Capability | Used by | Platform interface | Status |
|------|--------|----------|------|
| Notifications: banner, notification centre, tap to open | All | `notify` | ✓ |
| Runtime permissions and revocation | All | broker + Settings app (`perm.*`, system apps only) | ✓ |
| App isolation | All, especially payments | Launcher `phone-sandbox`: separate uid, namespaces, Landlock, seccomp, rlimit ([17](17-app-sandbox.md)) | ✓ (per-app firewall and cgroup resource limits included) |
| Share sheet | Forwarding to other apps | `share` | ✓ |
| Photo / file picker (only the selected items are handed over) | Sending pictures and files | `media.pick`: system picker; the selected photos are handed to the app as read-only files, and `max_side` asks for a JPEG copy the system made smaller (sending in a chat); `camera.capture`: photo taken through the system viewfinder ([22](22-developer-api.md) §4.6) | ✓ (photos; arbitrary files with `file.pick` ✓) |
| Location (WGS-84; apps convert to GCJ-02 themselves) | Location sharing, nearby | `location`: `location.get` one fix, `location.watch` continuous, for the foreground app; the grant dialog offers precise or approximate (a cell of about 2 km), and a request may ask for the approximate location only; `location_background`: also in the background once the user allows it all the time (the app asks with `location.background` in a dialog of its own, or in Settings), the status bar and a notification saying so while it is used there, with periodic reminders; network location by Wi-Fi and the cell while the satellites give no fix; A-GNSS ([22](22-developer-api.md) §4.7) | ✓ (simulated receiver, QEMU's NMEA pipe, the fake network location service; a phone's GNSS and A-GNSS **not yet tried on a real device**; real network location services **not verified**, the Chinese map providers' own API formats **not implemented**) |
| Clipboard (prompt on read) | Share codes (text passphrases) | `clipboard` | Design |
| Phone and SMS | Dialing, verification-code SMS | `tel.*`, `sms.*` (telephonyd, [04](04-ui-language-telephonyd.md)); apps without the `phone` permission need user confirmation to dial | ✓ (simulated modem) |
| Contacts (permission on demand) | Adding friends, dialing, saving numbers | Reading: `contacts.list` (the runtime permission `contacts`); writing: `contacts.save` / `delete` / `import` / `export`, each in a system dialog that says what changes and waits for the user ([22](22-developer-api.md) §4.2); handing a number to Contacts to save (`intent.invoke` with `contact.save`, [22](22-developer-api.md) §4.10); picking one contact: `contacts.pick`, the system's picker, which hands over only the number the user chose, no permission | Read and write ✓; the system's contact picker ✓ (dev-machine selftest); CardDAV sync ✓ ([19](19-core-apps.md) §1.1, **not tried on a phone**) |
| Alarms and timed reminders | Reminders, calendar | `alarm.*`; at the set time the system rings full screen and plays the alarm sound | ✓ (when suspended with the screen off, the RTC wakes the phone before the alarm; simulated on the emulator) |
| Background execution | Sync, incoming calls, music | Frozen 10 seconds after leaving the foreground; exceptions: `audio` mode while music is playing, during a call; push wakes apps within a budget. Hongyan is not resident; it receives messages and calls through its own server and push ([17](17-app-sandbox.md) §5, [21](21-push.md) §4) | ✓ (scheduled background tasks with `job.schedule` ✓) |
| App signing, installation, updates, app store | Publishing | `.phoneapp` + app store ([07](07-ecosystem-and-store.md)) | ✓ |

## 6. Developer tools

| Tool | Status |
|------|------|
| TypeScript apps: KarioUI and `@kario/sdk`, running on kario-jsrt ([29](29-app-development.md), [34](34-app-tutorial.md)) | ✓ |
| The `kario` command line (`new`, `build`, `run`, `test`, `package`, `publish`, `device`) | ✓ |
| Kario Studio (VS Code / Cursor extension) | ✓ |
| Go SDK (`sdk`) | ✓ |
| Direct protocol connection from any language (Python example, no SDK) | ✓ |
| JS mini-program framework: `Page` / `setData` / `phone.*` (same callback style as WeChat) | ✓ |
| TypeScript mini-programs: types in `app.ts` are stripped at load time; TSX views (`h()` produces `phone.Ui` nodes); `phone.d.ts` + strict checking with `tsc` | ✓; compiled to JS when packaged with `phone-pkg build` ✓ |
| C ABI SDK (lets vendors reuse their existing C++ cores) | ✓ `sdk/c` (`phoneui.h`), C++ sample app |
| Desktop simulator: phone-shell runs directly in a terminal on the development machine | ✓ |
| Automated testing: `--selftest` / `--script` key scripts + text snapshots | ✓ |
| Separate log for each app | ✓ |

## 7. Vendor porting path (hypothetical, unverified)

Taking an app like WeChat as an example, porting splits into three parts:

| Part | Current state (public information) | On this platform |
|------|------------------|-----------|
| Core logic: networking, storage, protocols | The cores of large IM apps are mostly cross-platform C++; Tencent has open-sourced Mars (networking) and WCDB (database) | Compiled and reused directly through the C ABI SDK |
| UI layer | A separate one is written for each platform | Rewritten with `phone.Ui`: pages and actions are described following the key grammar of [10](10-keyboard-interaction-model.md), without drawing pixels |
| Mini-programs | Their own JS runtime: logic layer (V8 / JavaScriptCore) + view layer (WebView or the in-house Skyline) | The logic layer is carried over as is; the view layer maps components to `phone.Ui` nodes (the prototype's `apps/miniapp` demonstrates this structure); subtree delegation ([11](11-platform-architecture.md) §10) |

## 8. Non-technical barriers

| Barrier | Explanation | What the platform can do |
|------|------|-----------|
| User base | Big vendors only adapt to platforms with scale (see the precedents of KaiOS and WeChat for watches in [09](09-ui-feasibility-review.md) §2) | First build the product with its own native apps and open-protocol apps |
| Financial security certification | Payments need a TEE, national cryptography, integration with biometric authentication standards, and certification testing | The trust plane is designed to these requirements (§4, [11](11-platform-architecture.md) §7) |
| Distribution and signing | App store, signing protocol, updates | The `.phoneapp` signing system ([07](07-ecosystem-and-store.md)) |
| Compliance | App filing (mandatory app registration in China), personal information protection | System-level permission prompts and a privacy dashboard lower vendors' compliance costs |
