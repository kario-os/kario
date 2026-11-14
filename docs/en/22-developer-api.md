> English translation of [the Chinese original](../22-developer-api.md). The Chinese text is authoritative; keep both in sync.

# Developer documentation: capabilities and interfaces

> For vendors and individual developers building apps on this platform (the system is called Kario, see 58 (internal document) §1).
> - This document matches the code: the capability catalog corresponds to `ui/caps.go` and the Shell's permission table, and `shell/apidoc_test.go` checks that every capability appears in this document. Add a capability without documenting it and the test fails.
> - Items marked "**not implemented**" are plans.
> - The protocol version is `phone.ui/0`. It is not frozen yet; for the compatibility policy see [11](11-platform-architecture.md) §11.

## 0. Why develop on this platform

| What you get | Details |
|------------|------|
| **Write the UI once, run it on every form factor** | The app only describes "what content there is and what can be done" (semantic UI); focus, menus, soft keys, back, input method and layout are all handled by the system. The same app, without changing a single line, runs on three kinds of device (portrait with a T9 keypad, candybar with a full keyboard, landscape with a wide keyboard), and it also runs in a terminal |
| **Write it in any language** | Apps and the system talk over a JSON-lines protocol on fd 3. Go has an SDK; Python needs only a few dozen lines even without an SDK (`apps/hello-py`); mini-programs use JS or TypeScript |
| **Light on resources** | The same chat app takes 66 MB of memory and starts in 1.1 seconds on the device as a semantic UI app; the web version takes 413 MB and 6.7 seconds ([15](15-web-vs-semantic-benchmark.md)). It runs even on low-cost devices |
| **The system does the hard parts for you** | Trusted payment confirmation (what you see is what you sign), the device attestation certificate chain, code scanning that returns only the result, the system incoming-call screen, push (your app does not need to stay resident), shared-memory audio, the photo picker. Your server can check signatures with the verification library the system provides |
| **Secure by default** | Every app has its own uid and sandbox. Other apps cannot touch your data, and they cannot spoof the system UI either |
| **Easy to test** | The same key script runs the full flow in a terminal on your development machine, on the pixel renderer and on a real device (§10) |
| **Open source** | The platform and SDK are open source under Apache-2.0 (58 (internal document) §3.1). You can build closed-source apps on top of it, and no single company can lock you in |

## 1. What an app consists of

An app is a directory: `manifest.json`, an executable (for a TypeScript app, the compiled `main.js`, §9.2), and an optional icon `icon.png`.

| Manifest field | Description |
|----------|------|
| `id` | Reverse domain name, globally unique, e.g. `com.example.notes`; IDs starting with `system.` are reserved for the system |
| `name` / `icon` / `icon_image` | Name; single-character icon for the text UI; PNG for the pixel UI (inside the app directory, ≤ 512 KB, ≤ 256×256) |
| `description` | One-sentence description |
| `exec`, `args` | Program to launch and its arguments |
| `runtime`, `main` | `"runtime": "js"` is a TypeScript app (§9.2): no `exec`; the system runs the module `main` (default `main.js`) in the package root with `kario-jsrt`; the package may hold no executable file and no bytecode. Without `runtime` the app brings its own program |
| `permissions` | Permissions the app uses (§6); capabilities that are not declared are always denied |
| `purposes` | Each runtime permission's purpose: `{"camera": "Scan to pay"}`, 1–80 characters, shown in the grant dialog; an app that declares a runtime permission without a purpose does not install (§6) |
| `privacy` | The privacy label: which data leaves the phone and what for (`collects`), which is used on the phone only (`local`); shown on the store's Details page; when given it must match the permissions, when not the store shows "not provided" ([33](33-app-guidelines.md) §3) |
| `background` | Background mode: `voip` (requires the `voip` permission), `audio` (not frozen while playing) |
| `requires` | Runtime dependencies, e.g. `["python3"]`; if one is missing, the app is not shown |
| `order` | Position in the main menu |
| `share` | What the app can receive from the share sheet: a combination of `text`, `image`, `link`, `file` (§4.9) |
| `intents` | What the app can do on behalf of other apps: `[{"action", "label"}]`, at most 8; `action` is a lowercase name (e.g. `pay`), `label` is 1–8 characters (§4.10) |
| `commands` | Entries contributed to the command palette: `[{"id", "label", "arg"}]`, at most 8, labels 1–16 characters. When the user picks one in the command palette, the system opens the app with `arg` as the deep link (e.g. Phone's 「最近通话」 (Recent calls) is `log`) |
| `settings` | The app's settings: `[{"key", "title", "type", "default", ...}]`. `type` is `toggle` (switch), `choice` (options, with `choices`), `number` (number, with `min`, `max` and an optional `step`; the system provides two actions, + and −) or `text` (one line of text; `max_len` defaults to 64 characters; no newlines or tabs). The system presents them uniformly under 「设置 › 应用设置」 (Settings › App settings), saves them and checks their types and ranges, so the app does not draw its own settings page. After the user changes one, the app gets a `settings` event and reads the current values with `app.settings` |
| `widget` | When `true`, the app can use `widget.set` to put one line of text on the idle screen (at most 32 characters, shown as 「应用名：内容」 (App name: content), at most 3 apps). When the user selects this line on the idle screen with ↓ and presses OK, the system opens the app with the line's `arg` as the deep link |
| `version` | Version number, written into the signature at packaging time (§12) |
| `notify_actions` | Actions attached to a notification when the system displays a push notification on the app's behalf (a push with only a title and body): `[{"id", "label", "key", "input"}]`, e.g. Hongyan's 「回复」 (Reply). When the user selects one in the notification center, the system delivers `notif_action` to the app in the background without bringing it to the foreground; an action with `input` first brings up the system input screen (quick reply), and the text goes in the message's `text`. Notifications the app sends itself (`notify`) carry the same kind of actions in `actions` |
| `nfc` | The app's contactless cards (NFC card emulation, [30](30-nfc.md) §6): `[{"id", "category", "description", "aids", "requires_unlock", "express", "locales"}]`, at most 8; `category` is `payment` (for the default payment app only, answering only unlocked) or `other`; `aids` are AIDs in hex (5 to 16 bytes, an AID in one card only, the PPSE in payment cards only); an `other` card with `express: true` may be made the express transit card by the user (it works with the screen off and locked); `locales` gives the card's name by language. Needs the `nfc` permission |
| `filing` | The app's ICP filing number in China (e.g. `京ICP备2026001234号-1A`). The store backend checks the format and displays it publicly on the app page; in stores open to the public in China, apps without a filing number cannot be listed ([07](07-ecosystem-and-store.md) §4.1) |
| `lang`, `locales` | `lang`: the language the manifest's text is written in (`zh-Hans`, `zh-Hant`, `en`, `es`; `zh-Hans` when absent). `locales`: the same text in other languages, by language, leaving out what is not translated: `{"es": {"name": "Notas", "description": "…", "settings": {"sort": {"title": "Orden", "choices": ["Fecha", "Nombre"]}}, "purposes": {"camera": "…"}, "commands": {"log": "…"}, "intents": {"pay": "…"}, "notify_actions": {"reply": "…"}}}`. `settings` gives, by setting key, the `title` and display names matching `choices` one to one (the values the app stores do not change); `commands` and `notify_actions` go by `id`, `intents` by `action`. The system picks by the device language's fallback chain (§9.1): the main menu, Switch apps, the permission dialog and Settings › App settings show the name in the device's language, and the choices their translated names too (`labels` in `settings.apps`). The installer and the store backend both check: only these four languages, the same length limits as the manifest itself (names 1–32 characters), no translation of a setting, permission, command, intent or notification action the manifest does not declare; the store catalogue's entries carry the name, summary and purposes in each language |
| `granted` | Pre-granted permissions, **only valid for apps in the system image**; the installer rejects this field |

## 2. Protocol

When the system launches an app, it puts a Unix socket on fd 3 (environment variable `PHONE_UI_FD`) and also provides `PHONE_APP_ID`, `PHONE_DATA_DIR` (the app's own data directory, which is also `HOME`), `PHONE_LANG` (the UI language, §9.1) and `TZ` (the time zone, an IANA name such as `America/Mexico_City`). The device's clock itself runs in UTC; the zone is given when the app starts, so when the user changes the zone while an app runs, the app gets the new one at its next start (**not implemented**: telling running apps). Each message is one line of JSON, and the `t` field gives its type:

| Direction | `t` | Purpose |
|------|-----|------|
| App → system | `hello` | First message, with `proto: "phone.ui/0"` |
| | `reset` / `push` / `replace` / `update` / `pop` | Manage the page stack: reset, push, replace the top, update by `id`, pop |
| | `toast` / `notify` | Toast; notification (`kind: "call"` is an incoming call and requires the `voip` permission) |
| | `req` | Request a capability: `req` (request number), `cap`, `args` |
| | `pong` | Answer to `ping`, with the same `value` |
| | `state` | `data`: what is needed to rebuild the pages (at most 64 KB of JSON). If the system ended the app in the background because memory was tight, it hands this back unchanged at the next launch (`data` of `start`, [17](17-app-sandbox.md) §5.3); the Go SDK calls `OnSave` and sends it every time the app leaves the foreground |
| System → app | `start` | Launch; `value` is the launch argument (deep link); `data` is the `state` handed over by the previous process (present only when the system ended it and the user came back; the Go SDK's `Restore`) |
| | `action` | The user triggered an action: `screen`, `node`, `action`, `value`, `fields` (the values of all input fields on the screen) |
| | `popped` / `lifecycle` | The screen was closed with the back key; foreground / background |
| | `res` | Capability result: `req`, `ok`, `data` or `err`; when files are attached, there is also `fds` (§7) |
| | `event` / `notif_action` | System event (§5); the user's action on a notification |
| | `ping` | The system checks whether the app is stuck: sent after a user action. The app must reply `pong` in the same loop that handles actions (the Go SDK replies automatically). If a foreground app does not reply within 5 seconds, the system shows the dialog 「没有响应」 (Not responding), and the user can wait or close the app |
| | `resync` | Sent by a new Shell that takes over this connection (the previous Shell crashed or restarted while the app process kept running): the app must resend its page stack, first a `reset` with the first page, then a `push` for each further page. Requests sent earlier that have not been answered yet will never get an answer. The Go SDK handles this automatically (it remembers the page stack; state outside the pages can be refreshed in `OnResync`); the C SDK passes it on to the app |

Minimal example (Python, no SDK; the page title `你好` means "Hello" and the button text `点我` means "Press me"):

```python
send({"t": "hello", "app": os.environ["PHONE_APP_ID"], "proto": "phone.ui/0"})
for line in rx:
    msg = json.loads(line)
    if msg["t"] == "ping":
        send({"t": "pong", "value": msg.get("value", "")})
    elif msg["t"] == "start":
        send({"t": "reset", "screen": {"id": "main", "title": "你好", "body": [
            {"kind": "button", "id": "inc", "text": "点我",
             "actions": [{"id": "inc", "label": "点我", "primary": True}]}]}})
```

## 3. UI: describe the content, don't draw pixels

**Screen**:

| Field | Description |
|------|------|
| `id`, `title`, `body` | Screen ID, title, list of nodes |
| `actions` | Screen actions, shown in the 「选项」 (Options) menu and in the command palette; `key` is the shortcut key |
| `right` | Screen action run by the right soft key |
| `center` | Screen action run by OK when nothing has focus |
| `focus` | Initial focus; `bottom: true` means start from the bottom (chats) |
| `columns` | Consecutive `tile` items are laid out as a grid, and the arrow keys move in two dimensions within it; `src` turns a cell into a photo or video cell (`media:<id>`), which the system fills with the image and shows without a title (the grid in the Photos app) |
| `layout` | Where to place the content when it is shorter than the screen: at the top by default, `center` centered slightly below the middle (Music's now-playing screen, the image viewer), `bottom` stuck to the bottom (Calculator). The system places the content where it is comfortable for the eye; the app does not compute coordinates |

**Node**:

| Kind `kind` | Purpose | Main fields |
|-------------|------|----------|
| `text`, `section` | Text, group heading | `text`; `style`: `dim` / `accent` / `title` / `ok` / `warn` / `big` / `display` (the calculator result: as large as the screen width allows, right-aligned) |
| `item` | List item | `title`, `subtitle`, `trailing`, `badge`, `icon`; `style: bubble-in / bubble-out` is a chat bubble, `tile` is a grid item; `src` can attach an image |
| `input` | Input field | `placeholder`, `value`, `secret`, `submit` (the action run by OK), `keyboard` (`number` / `phone` / `email`), `live` (notify the app on every edit) |
| `button` | Button | `text`; the actions go in `actions` |
| `image` | Image | `src`: `media:<id>` (a photo from the gallery), `qr:<text>` (QR code); `rows` is the height |
| `media` | Live video (video calls, video playback) | `stream`, `pip`: only stream names handed out by your own calls and players; if you write another app's stream name, the system does not display it |
| `doc` | Markdown document (including Mermaid diagrams, code highlighting, `$…$` formulas), laid out by the system | `text`; links automatically become menu actions |
| `web` | Web page (existing H5 content), rendered by the system web view in a separate process (all web pages of one app share one process). The arrow keys move between links, text fields are typed with the system input method, drop-downs and `alert` / `confirm` are shown as system UI, and the back key first goes back within the page ([14](14-tui-route-and-app-framework.md) §5); the app cannot get the page content | `src`: an http / https URL |
| `progress` | Progress bar | `fraction` (0–1), `text`, `trailing` |
| `canvas` | The app hands over graphics and the system draws them (games, maps, charts; the exception tier of [07](07-ecosystem-and-store.md) §0): the system draws the graphics as pixels (one pixel per unit), which in the pixel UI are an image scaled up without interpolation and in the text UI half-block characters: a cell is one unit wide and two units high, and a canvas wider than the screen is sampled in proportion — on a 40-column screen a canvas 76 units wide whose height is a multiple of 4 is sampled at exactly every other unit both ways (Snake is drawn at this size, with every dot that must show in the text UI on even coordinates); text to be read goes in ordinary nodes next to it (every renderer lays them out as text, and search and the selftest understand them), the canvas has no text drawing, and pixel lettering such as a game's score is put together from rectangles by the app itself | `canvas`: `w`, `h` (in your own units, at most 320 each), `bg`, `ops` (at most 4000: `rect` with `x y w h`, `line` with `x y x2 y2 width`, `circle` with the center `x y` and `r`, `dot`, `poly` with `pts`; `color` is `#rrggbb`, `fill` fills the shape), `keys`: when true, once the canvas has the focus, OK, the arrow keys, digits, letters, `*` and `#` go to the app as a `key` action whose `value` is the key name (`enter`, `up`, `5`…), while Back, the soft keys and End stay with the system |

**Action**:
- `id`, `label`, `key`;
- `primary` (OK runs it);
- `group: "danger"` or `confirm` (the system shows a confirmation dialog first);
- `hidden` (responds only to its shortcut key, not shown in the menu);
- `input` (only for actions on notifications): first lets the user type some text; the value is the hint shown in the input field, and the text is delivered to the app with `notif_action` (`OnNotifText` in the Go SDK).

**Left and right arrow keys**: actions on the focused element or on the screen whose `key` is `left` / `right` are triggered by ← → (volume down / up, the player's previous / next track, the image viewer's previous / next image). When nothing is bound, ← goes back one level and → opens the focused item (same as OK). In grids and input fields the arrow keys move as usual.

**Action naming conventions**: a few action IDs are bound to hardware keys, so the app does not need to handle key presses itself.
- `call` / `dial`: triggered by the call key (green key). The actions on the focused element are checked first, then the screen actions; when the `submit` of a dial input field is `dial`, the call key dials directly.
- `hangup`: the end key (red key) triggers this action on the screen; if the screen has no such action, the end key returns to the idle screen.
- The incoming-call screen is drawn by the system, and answering with the call key and rejecting with the end key are handled by the system too.

Apps never draw pixels and cannot see other apps' screens. Images are decoded and displayed by the system: `media:<id>` is only displayed for apps that can access that photo (§4.6).

**Capabilities in web pages**: a page inside a `web` node has `phone.call(capability, params)`, which returns a Promise; results and errors are the same as for an app's `req`. Requests are handled by the capability broker under the host app's identity, with the origin 「网页 <主机>」 (web page <host>): grants are recorded under that origin, and the system UI states which web page is asking (the same mechanism as a mini-program's `origin`). Only `share.open`, `clipboard.set`, `location.get` and `auth.confirm` are available; all other capabilities return an error.

## 4. Capability catalog

Request format: `{"t": "req", "req": <number>, "cap": <capability name>, "args": {...}}`.

The permission column:
- "—" means no permission is needed;
- entries marked "runtime" bring up the system grant dialog the first time they are used.

### 4.1 Payments and security

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `attest.get` | `{nonce, alg?}` | Device attestation: device ID, device certificate, app, boot state, signature; the server checks it with `keystore.VerifyAttestation`. `alg: "sm2"` signs with the device's SM2 key and attaches the SM2 device certificate (Chinese national cryptography chain); when omitted, Ed25519 is used. A boot state `boot` of `unlocked` means the owner has unlocked the device (system components may have been replaced), and payment services can refuse it. The server can also pass the attestation it received to the trust service for checking (`POST /v1/attest/verify`, `{app, nonce, attestation}` → `{valid, model, certified, provisioned, unlocked}`: whether the certificate chain leads to the certifier's root, which certified model the device is, and on which day it was provisioned on the factory line) | `attest` |
| | | `attest.get`, `auth.confirm` and `keystore.sign` called by a mini-program use **the mini-program's own identity and keys**: the identity is "host ID/mini-program name" (e.g. `com.phone.miniapp/晨光咖啡点餐`, a demo coffee-ordering mini-program), and it is written into the certificate and the signature digest; the server uses this identity when checking. The host cannot sign with a mini-program's keys, and neither can mini-programs with each other's keys, nor a mini-program with the host's | |
| `auth.confirm` | `{key, prompt, payload, alg?, bio_key?}` | System trusted confirmation dialog: after the user enters the password, the app key signs "the displayed text + payload", and the signature and certificate chain are returned (what you see is what you sign). `alg: "sm2"` selects Chinese national cryptography SM2 (with SM3 digests) when this key is first created, and the key keeps that algorithm from then on; when omitted, Ed25519 is used. `bio_key` adds a fingerprint-bound key: if the user turned on fingerprints for payments in Settings and has one enrolled, the dialog takes a fingerprint too, and a match signs with `bio_key` (the proof's `key_alias` is it and `bio` is true; its certificate says it is fingerprint-bound, and `keystore.VerifyConfirm` checks it as usual); the payment PIN still signs with `key` and also makes `bio_key` (only then); enrolling or removing a finger voids `bio_key`, the dialog falls back to the payment PIN, and confirming makes it again ([31](31-lock-and-biometrics.md) §5) | `keystore` |
| `keystore.sign` | `{key, payload, alg?}` | Signing without confirmation (e.g. payment codes); `alg` as above | `keystore` |
| `scan.qr` | — | System scanning screen; only the decoded text is returned, and the app cannot get the camera image | `camera` (runtime) |

### 4.2 Communication

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `push.register` | — | `{token}`: give it to your server, which then sends pushes to your app on this device through the gateway ([21](21-push.md)). A 410 from the gateway means the app has been uninstalled, and the server should delete the token. End-to-end encryption of the data part is recommended, so that the gateway only forwards ciphertext: get the public key with `push.key` and give it to the server, the server encrypts with `Seal` from the `push` package, and the app decrypts on receipt with `push.open` | — |
| `push.key` | `{rotate}` (optional) | `{key}`: this app's end-to-end push public key (X25519). The private key is kept by the system key service and the app cannot get it; later it will move into the TEE. `rotate: true` switches to a new key: the previous key can still decrypt data encrypted before the rotation, but anything older can no longer be decrypted | — |
| `push.open` | `{data}`: ciphertext starting with `e1.` | `{data}`: the plaintext in base64. The system decrypts with this app's private key; other apps' ciphertext cannot be decrypted | — |
| `media.call.start` | `{peer, video}` | `{call_id, local, remote}`: names of the video streams of the audio/video call | `microphone` (runtime); video additionally needs `camera` |
| `media.call.control` | `{call_id, mic, cam}` | Turn the microphone and camera on or off | — |
| `media.call.end` | `{call_id}` | End the call | — |
| `tel.dial` | `{number}` | Place a phone call; for an app installed from the store, the system states the number every time and asks the user to confirm, even when the app declares `phone` (premium-rate numbers are the oldest scam); the Phone app in the system image does not ask | — |
| `tel.answer` / `tel.hangup` / `tel.hold` / `tel.mute` | `{call_id, on}` | Answer, hang up, hold, mute | `phone` |
| `tel.calls` / `tel.log` | — | Calls in progress; call log | `phone` |
| `sms.send` / `sms.list` / `sms.read` | `{to, text}` (the default SMS app may also resend one that was not sent with `{id}`) / `{peer?, query?, folder?}` / `{peer}` | Send an SMS; list SMS (one conversation's, its number written any way; or those whose text or number contains `query`; or the blocked folder's with `folder: "blocked"`; with none of them, everything outside the blocked folder); mark a conversation as read. SMS are kept by phone-smsd ([32](32-messaging.md)); each carries `thread` (the conversation's number), `state` (`sending` / `sent` / `delivered` / `failed`) and `code` (a verification code found in it). For every message an app installed from the store sends, the system states the recipient and the content, and sends it only once the user confirms | `sms` |
| `sms.threads` / `sms.alerts` | `{folder?}` / — | The conversations (last message, count, unread count, newest first); the emergency alerts received (cell broadcasts: level, channel, text, time) | `sms` |
| `sms.delete` / `sms.block` / `sms.blocked` | `{ids}` or `{peer}` / `{number, on}` / — | Delete messages or a whole conversation; block or unblock a number (answers with the block list); the block list. Only for the default SMS app, the system image's Messages (`com.phone.messages`): other apps cannot, `sms` permission or not (Android lets only the default SMS app write the SMS store) | `sms` |
| `tel.ussd` | `{code}` / `{text}` / `{cancel}` | Send a USSD code (`*100#`), reply to the network's menu or question, end the session; returns `{state, text}`: `done` is the network's final answer, `input` means the network is waiting for a reply. `*#06#` is answered by the phone itself with the IMEI and is not sent to the network. USSD the network sends by itself is delivered to the Phone app as a `tel.ussd` event. Only for apps in the system image: such codes can subscribe to paid services | `phone` |
| `contacts.list` | — | Contacts (name, number, pinyin) | `contacts` (runtime) |

### 4.3 Sound ([20](20-audio.md))

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `media.list` | `{kind}` (defaults to `audio`) | Entries in the media library; `kind: "image"` lists photos and requires `photos` (runtime) | — / `photos` |
| `audio.play` | `{media}` | Play one item from the media library (category `media`) | — |
| `audio.control` | `{id, op}` | `pause` / `resume` / `stop`; can only control the app's own streams | — |
| `audio.state` | — | The app's own streams | — |
| `audio.route` | `{route}` | `earpiece` / `speaker` | `phone` |
| `audio.stream` | `{name}` | One audio stream of the app's own: the reply carries a shared-memory ring buffer (§7) | — |
| `audio.record` | `{name}` | Recording: the reply carries a ring buffer into which the microphone audio is written. Only available in the foreground or during a call; stops as soon as the app leaves the foreground; the status bar shows an indicator | `microphone` (runtime) |

### 4.4 Time

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `alarm.set` | `{id, time: "HH:MM", label, daily, on}` | System alarm: rings on time even when the app is not running | — |
| `alarm.cancel` / `alarm.list` | `{id}` / — | Cancel; list the app's own alarms | — |

### 4.5 System settings (system apps only)

| Capability | Description | Permission |
|------|------|------|
| `perm.list` / `perm.set` | List and change the runtime permissions of all apps (used by the Settings app); each entry has `last_used` (millisecond timestamp) and `uses`: when this grant was last used and how many times it has been used; `reset`: the day the system revoked this grant because the app had not been opened for 90 days ([17](17-app-sandbox.md) §1) | `system` |
| `wifi.state` / `wifi.set` / `wifi.connect` / `wifi.forget` | Wi-Fi (iwd): the state, nearby networks (strongest signal first; `signal` is 0–4 bars, `known` saved, `connected`), the address once connected; `set` with `{enabled}` turns it on or off; `connect` with `{ssid, passphrase}`: an encrypted network that has not been saved needs its passphrase, and without one the reply is `the network needs its passphrase`; an enterprise (802.1X) network that has not been saved needs `{identity, passphrase, domain}`, with `eap` either `peap` (the default) or `ttls` (both with MSCHAPv2 inside), or `ttls-pap` (TTLS with PAP inside), and without them the reply is `the network needs a user name, password and domain`; the passphrase is handed only to iwd, which stores it (`/var/lib/iwd`) to reconnect automatically; `forget` with `{ssid}` deletes the saved network. On the dev machine the networks are simulated | `system` |
| `bt.state` / `bt.set` / `bt.pair` / `bt.forget` | Bluetooth (BlueZ): the local name, on or off, whether it is discovering, paired and nearby devices (a paired device has `phonebook`: `allowed` / `denied`, whether it may read the contacts and the call log over PBAP; absent until it first asked); `set` with `{enabled, discover}`, or `{address, phonebook}` to allow that device the contacts or take them back; `pair` with `{address}`: when the other device has a screen (a phone, a car), the system shows a dialog with the pairing code, and the devices are paired only once the user has checked that it matches the one on the other device; the reply comes once the user has answered; devices without a screen, such as headsets, pair directly (Just Works); `forget` unpairs (and drops the contacts permission). On the dev machine the devices are simulated | `system` |
| `bt.send` | `{address}`: sends one file to a paired device over Bluetooth (OBEX Object Push). The system lists the files in Files, the user picks one, and the calling app never sees it; the reply `{sent}` (the file's name) comes once the other side took it; a refusal answers `“…” declined the file`, no choice `cancelled`. On the dev machine the devices are simulated | `system` |
| `radio.state` / `radio.set` | Airplane mode and mobile data: `{airplane, mobile_data, cellular, data}`, `cellular` being the modem's radio `on` / `off` and `data` the cellular data link `connected` / `idle` / `off` (both empty without telephonyd); `set` with `{airplane?, mobile_data?}`. Airplane mode turns the radio (telephonyd), Wi-Fi and Bluetooth off, keeps the user's switches and restores them when it ends; in airplane mode the user may turn Wi-Fi and Bluetooth on again, and the next airplane mode leaves them on; dialling an emergency number makes the modem turn its radio on by itself and the system leaves airplane mode; airplane mode cannot be turned on during an emergency call. With mobile data off telephonyd disconnects the data link and the firewall closes the cellular interfaces it reports. Both switches hold across restarts | `system` |
| `settings.get` / `settings.set` | System settings (ringtone, media and alert-tone volumes, automatic keypad lock, `silent` silent mode, `ime_fuzzy` fuzzy pinyin, `large_text` large text, `developer_mode`, `device_unlocked` device unlock and `crash_send` sending one crash record to the developer (these two are confirmed through a system dialog); `theme` theme (the value is an index into `themes`), `wallpaper_clear` restore the default wallpaper (1 idle screen, 2 lock screen, 3 both); `sim_pin_lock` (1 on, 0 off) and `sim_pin_change`: the system shows its own PIN screen, the PIN goes only to the SIM card and the Settings app never sees it, and the reply comes once the user is done on that screen; `language`, `region`, `time_zone`: the 1-based index in `languages`, `regions`, `zones`, 0 for automatic; `auto_time` (1 network time, 0 manual), `time` (the manual time: the date and time in the zone in force, as "seconds since 1970-01-01 00:00 of that wall clock", so the app need not know the zone), `clock_24h` (1 for the 24-hour clock); `usb_mode` (what a trusted computer gets: 1 file transfer over MTP, 0 network sharing, the default; from the next computer on, [27](27-usb-typec.md) §3.3); security ([31](31-lock-and-biometrics.md) §6): `lock_pin` (1 set or change, 0 remove), `fingerprint_auth` (confirm the PIN before managing fingerprints), `fingerprint_enroll` (enrol): these three raise the system's own PIN or enrolment screen, the Settings app never gets the PIN or a fingerprint, and the reply comes once the user is done; `lock_grace` (how soon it locks after the screen goes off, in seconds), `lock_show_content`, `fingerprint_unlock`, `fingerprint_pay` (switches), `fingerprint_rename` (the fingerprint's ID as the value, the new name as `text`), `fingerprint_delete` (the fingerprint's ID); emergency alerts ([32](32-messaging.md)): `alert_extreme`, `alert_severe`, `alert_amber`, `alert_test` (1 shown, 0 not; the highest level has no switch, it is always shown)), `alerts` (each level's name, switch and whether it is locked), `usb` (`mode`: `mtp` or `network`; `sharing`: sharing with a computer now), `security` (whether a PIN is set, the grace period and its choices, the sensor, the fingerprints' IDs and names, the switches, why fingerprints cannot be used for now), the 「关于本机」 (About phone) information, the last 10 app crash records, `themes` and `theme_index`, `sim` (`state`, `pin_lock`), `languages` (the four languages, each name written in itself), `language` and `language_auto` (the one in force and the one automatic would pick), `regions` (region names sorted in the current language), `region` and `region_auto` (`code`, `from`: `sim` / `sku` / `default`), `zones` (each zone's city and its UTC offset now), `region_zones`, `time_zone` and `time_zone_auto` (`id`, `city`, `from`: `user` / `network` / `region`), `clock` (the current date and time as text, whether the time can be set by hand, what NTP runs through); the argument of `set` is `{key, value, text?}` | `system` |
| `system.update` | System update (A/B system slots): argument `{action}`. `status` returns the current version, the slot running now and the slot for the next boot, and the install state; `check` reads `latest.json` from the update server (staged rollout: `rollout` is the share (%) of devices the update has been offered to, `paused` pauses the rollout; each device decides by its own random bucket, and the bucket is never uploaded), and when it is not this device's turn yet, `held` states the reason; `install` streams the update package into the slot that is not running (packages with an untrusted signature, or not meant for this device, are refused; only data blocks present in neither slot are downloaded) and replies only after the install has finished; `reboot` reboots into the new system | `system` |
| `settings.apps` / `settings.app.set` | The settings each app declares in its manifest, with their current values; change one of them (`{app, key, value}`; type and options are checked by the system), and the app immediately gets a `settings` event | `system` |
| `pkg.install` | `{file, sha256?, auto?}`: a `.phoneapp` package in the caller's data directory, or a delta package based on the installed version (the system fills in the rest from the installed files). The system verifies the signature and checks the manifest (and, if `sha256` is given, the digest of the whole package), then shows a dialog listing the developer and the permissions, and installs or updates after the user confirms (§12). `auto: true` marks an update the store installs in the background: when it changes nothing the user agreed to (installed, same developer, higher version, no more permissions, the app neither in the foreground nor working in the background), it is installed without a dialog; otherwise the reply is `the update needs the user` | `install` |
| `pkg.revocations` | `{data}`: a takedown list (JSON) signed by the store. The system verifies the signature against the trusted store roots and ignores the list if it is older than the one it already has; installed apps on the list can no longer launch, and running ones are ended | `install` |
| `pkg.catalog` | `{data}`: a catalog (JSON) signed by the store → `{store, issued, apps}`. The system verifies the signature against the trusted store roots and refuses the catalog if it was issued more than 7 days ago or is older than the last one received from the same store; `apps` is the catalog's app list, in which each app in a staged rollout is replaced with the version this device should get, depending on whether the device is in this batch ([07](07-ecosystem-and-store.md) §4.1) | `install` |
| `pkg.uninstall` / `pkg.list` | `{id}` / —: uninstall (confirmed by the system; the data is deleted too); list installed packages | `install` |

### 4.6 Photos and video

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `media.list` | `{kind: "image"}` / `{kind: "video"}` | Photos / videos (videos have `len_ms` and `mime`: `video/webm`) | `photos` (runtime) |
| `video.open` | `{media, at_ms?}` | The system decodes and plays a video (WebM: VP8 / VP9 video, Opus / Vorbis audio) and returns `{player, stream, state, pos_ms, duration_ms, w, h, sound}`: write `stream` into a `media` node. The sound goes through system audio (category `media`, obeying audio focus), and the picture follows the sound. The app cannot get the file or the pixels | Must be able to see this video (same as for photos) |
| `video.control` | `{player, op, at_ms?}` | `pause` / `resume` / `seek` (to `at_ms`) / `state` / `close`; returns the same state as above (`state`: `playing` / `paused` / `ended`). When the stream is not on the app's foreground UI, the system pauses it automatically and resumes it when it comes back; when the last screen showing it closes, the system closes the player | — |
| `wallpaper.set` | `{media, target}` | Set a photo as the wallpaper of the idle screen (`idle`), the lock screen (`lock`) or both (`both`); the system keeps a copy, so the wallpaper stays if the photo is deleted. For apps outside the system image, the system asks the user first | Must be able to see this photo |
| `camera.capture` | — | System viewfinder; the user presses OK to take a photo. The photo is saved in the system gallery, and its entry is returned (`id` looks like `media:p4`) | `camera` (runtime) |
| `media.pick` | `{kind: "image"}` | System picker: the one image the user picks is handed to the app as a **read-only file** with the reply (§7), and its entry is returned too (including `mime`: `image/png` or `image/jpeg`) | — |
| `media.save` | `{data?, src?, name?}` | Save an image into the system gallery: `data` is a PNG / JPEG the app holds (base64, e.g. a downloaded image), `src` is an image the system displays on the app's behalf (a `gen:` image). The system first decodes and validates it, then shows a dialog stating the source and size, and saves only if the user agrees; returns the entry. Afterwards the app can see the one image it saved, but not the other photos in the gallery | — |
| `app.settings` | — | Read the current values of the settings the app declared in its manifest (default values for those never changed) | — |
| `widget.set` | `{text, arg}` | Set the app's own line on the idle screen; an empty string removes it. `arg` (at most 256 bytes) is the deep link used when the user selects the line and presses OK; the `start` / `open` the app receives carries it. The manifest must contain `"widget": true` | — |
| `media.delete` | `{id}` | Delete a photo: the system shows a confirmation dialog first, and the app cannot skip this step | `photos` (runtime) |

- Photos and videos belong to the system, not to the app that took them.
- An app can only see photos it took itself and those the user handed to it in the picker; apps holding the `photos` permission can see all of them.
- Image nodes use `src: "media:<id>"`, and the system decodes and displays them.

### 4.6.1 Web node

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `web.control` | `{screen, node, op, text?, value?}` | Control the app's own `web` node: `reload`, `back`, `forward`, `stop`, `zoom` (`value` 0.5–3), find in page `find` (`text`), `find_next`, `find_prev`, `find_end`, `clear_data` (clear this app's website data: cookies, cache, local storage), `sites` (a system screen lists the websites that have stored data, and the user picks one to delete; the app cannot get the list). The page's state arrives through `web` events | — |

Downloads from the page go into the system 「文件」 (Files) area (at most 16 MB), and the system shows a toast and posts a notification. `<input type=file>` gets one file through the system file picker, and `prompt` is shown as system UI. When the `content_blocking` setting in the app's manifest is turned on, the web process blocks ads and tracking scripts according to the system rules (`share/phone/web/blocklist.json`). Reference: the browser app (`apps/browser`).

### 4.7 Location and background jobs

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `location.get` | — | `{lat, lon, accuracy_m, at}`: coordinates only; converting them into place names is up to the app. Only available to the foreground app, and while it is in use the status bar shows 「● 位置」 (● Location). Location is currently simulated (Beijing); reading satellite positioning from the modem is **not implemented** | `location` (runtime) |
| `job.schedule` | `{id, every_min, charging?}` | Scheduled background job: every `every_min` minutes (at least 15), the system wakes the app or launches it in the background and sends a `job` event. Each run has a time window that opens when the job is due and closes half a period later (at most 30 minutes), its deadline. The system does not wake the phone for jobs that have not reached their deadline: when one job reaches its deadline, all jobs whose windows are open run in the same wake-up; they also run along when a push has just arrived (the radio is awake). Jobs with `charging: true` only run while on external power and never wake the phone on their own; they run after the charger is plugged in. They can do heavy work (the store's automatic updates): each run gets 1 minute before the app is frozen again, compared with 3 seconds for an ordinary wake-up. Jobs share the hourly wake-up budget with push; runs beyond the budget wait until the app returns to the foreground. Registering the same `id` again replaces the old job; jobs survive reboots | — |
| `job.cancel` | `{id}` | Cancel one of the app's own jobs | — |

Background jobs cannot get the location (location is foreground only). Reference: the weather app (`apps/weather`) gets the location in the foreground and refreshes in the background using the last known location.

### 4.8 Clipboard

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `clipboard.set` | `{text}` | Copy text (at most 16 KB); the system shows 「已复制」 (Copied) | — |

- The clipboard is **write-only; it cannot be read**, so apps cannot peek at the clipboard in the background.
- Pasting is done by the system: when an input field has focus, the first item of the system menu is 「粘贴「…」」 (Paste "…") (press `v`); the text goes straight into the input field, and the app only receives the result of the input.
- The clipboard is cleared automatically after 10 minutes.

### 4.9 Sharing

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `share.open` | `{kind: "text", text}`, `{kind: "image", media}`, `{kind: "link", url, text}` (`text` is the title) or `{kind: "file", file}` | The system 「分享到…」 (Share to…) sheet, listing the apps whose manifests declare that they can receive this kind of content. After the user picks one, returns `{shared: true}`; if the user cancels, reports `cancelled`. The receiving app is opened with the deep link `share:<kind>:<content>`; a shared photo or file is granted only to that one app | — |
| `file.save` | `{name, data}` (base64, at most 16 MB) | Save a file into the system 「文件」 (Files) area (documents, attachments); the system asks the user first. Returns the entry `{id, name, mime, size}` | — |
| `file.pick` | `{types?}` (extensions or MIME prefixes) | The system lists the files in the 「文件」 area, and the one the user picks is handed to the app as a read-only file with the reply | — |
| `file.open` | `{id}` | Open a file that has been handed to this app (shared with it, or picked or saved by it); the read-only file is handed over with the reply | — |

- The caller only learns whether the share succeeded, not which apps the user has installed or which one the user picked.
- A shared photo must be one the caller itself can see; when it is shared, the system grants the photo to the target app.
- The target app is opened (or gets an `open` event) with the deep link `share:text:<text>` or `share:image:media:<number>`; the text is at most 4 KB.
- Reference: the Photos app shares a photo to Hongyan (the user then picks the conversation); Weather shares text to Messages (the user fills in the recipient).

### 4.10 Inter-app intents

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `intent.invoke` | `{action, payload?, what?}`: `payload` is data for the handler (at most 4 KB), `what` is a one-sentence description (at most 64 characters) | If exactly one app (other than the caller itself) declares this `action` in its manifest, it is opened directly; if several do, the system lists them for the user to choose from (the chooser menu is labeled 「<发起方>请求<what>」 (<caller> requests <what>)). After the handler replies, returns `{app, ok, result}` and brings the caller back to the foreground. If the user cancels, the handler exits or there is no reply within 5 minutes, reports `cancelled`; if no app can handle the action, reports `no app handles this` | — |
| `intent.reply` | `{id, ok, result?}` (`result` at most 4 KB) | The handler's reply. Only the app the request was handed to can reply, and only once | — |

- The handler is opened (or gets an `open` event) with a deep link consisting of `intent:` followed by the JSON `{id, action, payload, from, from_name}`: `from` is the caller's app ID, filled in by the system, and cannot be forged.
- Whether to do it and how to confirm with the user is up to the handler: for things like payments, use the system's trusted confirmation (§4.1); the amount and payee the user sees come from the handler, not from the caller.
- The caller only learns which app replied and what it replied, not which other apps the user has installed that could handle the action.
- Reference: Hongyan's 「转账」 (Transfer) asks Feiqian to pay (`action: "pay"`, `payload: {to, amount, memo}`, the amount in fen, i.e. hundredths of a yuan). Feiqian shows 「<发起方>请求付款」 (<caller> requests payment), pays through trusted confirmation and replies `{bill, amount}`, and Hongyan records 「已转账」 (Transferred) in the conversation.

### 4.11 NFC card emulation and reading ([30](30-nfc.md))

| Capability | Arguments | Result | Permission |
|------|------|------|------|
| `nfc.reply` | `{session, apdu}`: the answer in hex, ending in its status word (`9000` success) | Answers the latest command of the `nfc.apdu` events; only the app the command went to can answer, and an answer after the reader left is an error | `nfc` |
| `nfc.state` / `nfc.set` | `set`'s `{key, on?, app?, card?, by?, name?, seconds?}`: `enabled` (`on`), `default_payment` (`app`), `foreground_payment` (`on`, pay with the open payment app), `express` (`card`, empty for none), `move` (`card`, `by` -1 or 1), `locked` (`card`, `on`: whether the card works with the screen off and the phone locked; not for payment cards and `requires_unlock` cards); the card pack's door cards: `read` (reader mode for `seconds`, at most 60), `cancel_read` (stops a `read` waiting for a card at once; it answers `none`), `add_door` (`name`, adds the card just read), `rename` (`card`, `name`), `delete` (`card`); `present` (`card`, the system presents it for a minute with its countdown, empty stops), `open_cards` (opens the card pack) | The controller, whether it is simulated, the cards in order (a door card's UID only as its last two digits, with its usual hours `usual`: `{weekend, from, to}`, minutes of the day, `weekend` meaning days off: weekends and the holidays of the region's holiday calendar, not the weekend days it makes workdays; each card's `locked` and `locked_fixed`: whether it works with the screen off and locked, and whether that can be changed), the door card a door reader gets first now, `door_first`, the default payment app and the apps with payment cards, the express transit card; the answer to `read` also has `read`: `{result, uid?, name?}`, `result` being `ok`, `none`, `encrypted`, `smart` (a CPU card) or `duplicate` (`name` is what the card pack calls it) | `system` (Cards, Settings) |
| `nfc.read` | `{seconds?}`: how long to wait for a card, 20 seconds by default, at most 60 | Reader mode (Android's `enableReaderMode`, iOS's Core NFC): the system shows its reading sheet ("hold the card to the back of the phone", saying which app reads; Back stops it) and answers with the card it reads, `{uid, atqa, sak, iso_dep, session?, until?}` (hex); an ISO-DEP card (a CPU card) has a `session` where the controller can keep it, for exchanging commands with `nfc.transceive` until `until` (Unix milliseconds), while the sheet says "keep the card on the phone". Without a card the error is `no card was held to the phone`, after Back or the app closing the session `cancelled`; only the app in the foreground reads, one read at a time | `nfc_reader` (runtime) |
| `nfc.transceive` | `{session, apdu?, close?}`: a command (hex, 4 to 4096 bytes) for the session's card; `close` ends the session (after the command, if there is one) | `{apdu}`: the card's answer, ending in its status word. A session lasts at most 30 seconds (`ui.NFCReaderHold`) and ends when the card is taken away, the user presses Back, or the app closes it or leaves the foreground, taking the sheet away; commands after that are errors | `nfc_reader` (runtime) |

- Once a reader has selected an AID of one of the app's cards, every command (the SELECT included) reaches it as an `nfc.apdu` event; a frozen app is thawed for a few seconds, one not running is started in the background, outside the push wake budget. A reader waits only so long for each command, so apps must answer at once.
- When the reader is done with the app's card (the field gone, a halt or DESELECT, or another AID selected), the app, if still running, gets an `nfc.deactivated` event (Android's `onDeactivated`) and can reset its command state there; an app that has ended is not started for it.
- Apps see neither the other cards in the card pack nor the door cards' UIDs; the system decides which card answers when ([30](30-nfc.md) §3, §4).
- Reader mode is apart from card emulation: what is read is the physical card held to the phone, and the UID the app gets is that card's (the UIDs of the door cards the card pack copied never go to apps). Only the simulated controller keeps an ISO-DEP card for a session; on an NCI controller a read gives the UID without a `session` (**not implemented**, [30](30-nfc.md) §8).
- Reference: Feiqian's transit card (the gate reads the balance and debits it) and payment card (the till selects the PPSE and pays); Feiqian reads a physical transit card's balance in reader mode (`SELECT` of the card's AID, then `80 5C 00 02 04`).

## 5. Events (`event`)

| Name | Value | When |
|------|----|----------|
| `open` | Deep link, same as the argument of `start` | The app is already running and is asked to open some place again (e.g. a digit is pressed on the idle screen and the Phone app gets `dial:<number>`) |
| `push` | `{id, kind, data, sent, notified, collapse_id}` | The data part of a push; for this the system may resume a frozen app for a few seconds, or launch it in the background. `notified` true means the system has already shown the notification for this push (the system does not show it when the app is in the foreground, or when the user has turned off the app's notifications), so the app should not post a notification of its own |
| `push.key_changed` | — | The device was unlocked or relocked, and the key service cleared all app keys: the push key is new, and data encrypted with the old public key cannot be decrypted. Apps that use end-to-end encrypted push must call `push.key` again and give the new public key to their server. Frozen apps are resumed for a few seconds for this; apps that are not running are not launched (they fetch the key at launch anyway) |
| `focus` | `lost` / `gained` | A system menu or dialog came over the app's screen, or the screen went off (`lost`), and the keys are the app's again (`gained`); Android's `onWindowFocusChanged`. The app is still in the foreground and its lifecycle does not change; a game pauses on `lost` (Snake does) |
| `audio` | Stream state `{id, state, pos_ms, …}` | One of the app's own streams changed (`ended` when playback finishes) |
| `media` | `next` / `previous` | The user pressed a media key (headset remote, keyboard); sent to the app that is playing. Play / pause is handled directly by the system |
| `settings` | `{key, value}` | The user changed a setting declared by this app under 「设置 › 应用设置」 (Settings › App settings) |
| `web` | `{screen, node, url, title, loading, can_back, can_forward, zoom}` | The state of one of the app's own web nodes changed (a new page opened, loading finished, zoom) |
| `alarm.fire` | Alarm ID | One of the app's own alarms went off (the system has already shown the full-screen alarm) |
| `job` | Job ID | A scheduled background job is due (§4.7); the app may have just been woken and will be frozen again a few seconds later |
| `sms` | SMS | An SMS arrived, or the state of a sent SMS changed (`state`: `sending` / `sent` / `delivered` / `failed`, same `id`), or messages were deleted or marked read (value `{}`) (`sms` permission) |
| `tel.call` / `tel.open` | Call / call ID | Phone app only |
| `call.resume` | Call ID | The user chose 「返回通话」 (Return to call) from the system menu |
| `nfc.apdu` | `{session, card, aid, apdu}` | A reader sent a command for one of the app's cards (hex); the app answers with `nfc.reply` (§4.11, `nfc` permission) |
| `nfc.deactivated` | `{session, card, reason}` | The reader is done with this card: `reason` is `link_loss` (the field gone, a halt or DESELECT) or `deselected` (another AID selected) (§4.11, `nfc` permission) |

## 6. Permissions and background

| Category | Permissions | Notes |
|------|------|------|
| Runtime (asked on first use; the user can revoke them in Settings) | `camera`, `microphone`, `contacts`, `photos`, `location`, `nfc_reader` (reading NFC cards held to the phone) | The grant dialog is drawn by the system, and apps cannot spoof it; when an app installed from the store has not been opened for 90 days, the system revokes these grants and asks again next time |
| Granted when declared | `keystore`, `attest`, `notifications`, `voip`, `net`, `phone`, `sms`, `nfc` | Automated store review only rejects `system`, `install` and pre-grants, and new apps and updates that request more permissions go into manual review; `phone` and `sms` still do not let store apps place calls or send SMS on their own: the system asks the user to confirm every time (§4.2). Runtime permissions need their purpose in the manifest's `purposes`, shown in the grant dialog and checked at the staff desk |
| System apps only | `system`, `install` (app store) | The installer rejects packages that declare them; installed apps do not get them even if they declare them |

Background rules ([17](17-app-sandbox.md) §5):
- The process is paused (frozen) 10 seconds after leaving the foreground.
- When memory is tight, the system may end apps in the background (this does not count as a crash). When the user comes back, the app is on its earlier page: the system first shows the earlier pages, and the new process receives at startup the `state` it handed up when it left the foreground (Go SDK: `OnSave` returns what to save, `Restore` reads it back) and rebuilds its pages from it; on pages whose screen ID stays the same, the system restores the unsent text in input fields and the focus.
- Exceptions: the `voip` background mode; the `audio` mode while playing; during a call.
- To receive messages in the background, use push: pushes with only a title and body are shown by the system without waking the app; pushes with data wake the app briefly within a budget (at most 20 times per app per hour). When the server sends a push with `collapse_id`, a new push with the same key replaces an older push that has not been delivered yet, along with its notification (for content where only the latest value matters, such as unread counts and scores).
- After a VoIP push wakes the app, the app must report the incoming call within 10 seconds (a notification with `kind: "call"`); after 3 failures to report within 24 hours, subsequent VoIP pushes are treated as ordinary data pushes.
- When a push has `priority: "normal"`, its data does not wake a frozen app immediately; it is delivered when the app wakes up for some other reason (returning to the foreground, another wake-up), and at the latest the system does one batched wake-up after 15 minutes.
- Audio streams and recordings an app opens during a call (VoIP or phone) count as the "call" category: they have priority during the call and are not stopped when the app leaves the foreground ([20](20-audio.md)).
- Notifications: each app gets at most 5 notifications per minute with a banner and an alert sound; the rest only go into the notification center. The notification center keeps at most 24 per app, deleting the oldest first. The user can turn off an app's notifications in 「设置 › 应用权限」 (Settings › App permissions) (`notifications`, like the network, has only two settings, 「允许」 (Allow) and 「关闭」 (Off)); once they are turned off, the app's notifications are dropped outright, and incoming calls are not affected.
- Actions on notifications (quick reply): when the user selects an app's action in the notification center, the system delivers it to the app in the background (a frozen app is resumed first, an app that is not running is launched in the background, and this does not count against the push wake-up budget), and then clears the notification.
- For periodic sync, use background jobs (§4.7); they share the same wake-up budget with push.
- Reference: how Hongyan does it ([21](21-push.md) §4): it is not resident; when it comes online, it gives its push token to its own server; messages from the other party come down as "notification + data" pushes carrying the conversation `thread`; incoming calls use VoIP push.

## 7. Files and shared memory (fd passing)

The replies of some capabilities come with open files attached; the message's `fds` field says how many:

| Capability | Attached file | What you can do |
|------|------------|------------|
| `audio.stream`, `audio.record` | A memfd holding a ring buffer of 48 kHz mono 16-bit audio, with its size sealed | Map it and read/write samples; for the format see `audio/ring.go`; in the Go SDK it is `app.GoAudio` |
| `media.pick` | Read-only file of the selected photo (PNG or JPEG, see the entry's `mime`) | Read it, upload it; you cannot write to it, and you cannot reach any other file in the gallery. In the Go SDK it is `app.CallFile` |

Rules:
- **There is no rule of one fd per message.** A message carries at most 8, which is only a cap against oversized messages. What really needs to be controlled is the following:
- **Direction**: files only flow from the system to apps. fds an app sends to the system are closed immediately, so an app cannot exhaust the system's file descriptor table by stuffing fds into it.
- **Count**: each app can have at most 4 audio streams open at the same time, of which only 1 can be a recording; system services have an overall cap as well. The number of open files of the app process itself is limited by the sandbox (1024).
- **Type**: the system only sends objects it has verified itself, such as size-sealed memfds and files opened read-only. The receiving side checks again too; for example, it refuses to map a ring buffer that is not sealed.

## 8. Sandbox restrictions

- Apps can only read and write `PHONE_DATA_DIR`. There is no shared `/tmp`; temporary files go in `$TMPDIR` (under the data directory).
- Files in the data directory cannot be executed, i.e. apps cannot download code and run it. Mini-programs should use the JS runtime provided by the host.
- Without the `net` permission there is no network; even with it, apps cannot listen on ports.
- Apps cannot see other processes or debug them, and dangerous system calls are blocked.
- The memory limit is 192 MB (no swap), and processes and threads combined are limited to 128. When the memory limit is exceeded, the system ends only your app and tells the user 「超出内存上限」 (Memory limit exceeded), so save data promptly. Go programs get `GOMEMLIMIT=144MiB`, so garbage collection works harder before the hard limit is reached.
- When system memory is tight, apps in the background are ended first, least recently used first; foreground apps, apps in a call and apps playing in the background are never picked. An app that was ended cold-starts the next time it is opened, so save data as soon as you get `lifecycle: background`.
- A page has at most 1000 nodes; the excess is cut off. The page stack is at most 32 deep; pushing beyond that replaces the top. For pages whose title contains the word for System in any of the four UI languages (「系统」, 「系統」, System, Sistema, as in Spanish "…del sistema"), the system prefixes the title with the app name, so apps cannot impersonate system dialogs.
- For apps with the `net` permission, the user can turn off their network in 「设置 › 应用权限 › 网络」 (Settings › App permissions › Network): the system ends the app, and the next time it starts without a network.
- When an app exits on its own with a non-zero exit code (crash, panic, killed by a signal), the system records the time, the reason and the last 20 lines of the log, which can be viewed under 「设置 › 关于本机 › 应用崩溃记录」 (Settings › About phone › App crash records). After 3 crashes within 10 minutes, the system temporarily stops launching the app in the background (pushes and background jobs are held until the user opens it).
- About 10 seconds after leaving the foreground, the whole app is frozen together with its child processes; in the background its CPU share is also lower than in the foreground.

## 9. SDKs and languages

| Language | Status | Notes |
|------|------|------|
| Go | ✓ `sdk` | `sdk.New`, `Run`; pages: `Reset` / `Push` / `Update` / `Pop` / `Toast` / `Notify`; capabilities: `Call` (blocking), `Go` (asynchronous; the result is decoded and the callback runs in the app's dispatch goroutine), `GoAudio`, `CallFile`; callbacks: `OnStart` / `OnAction` / `OnEvent` / `OnLifecycle` / `OnPopped` / `OnNotif` / `OnSave` (returns the state needed to rebuild the pages when the app leaves the foreground), and `Restore` in `OnStart` reads back the state saved before the system ended the app. A capability's result **replaces the whole** variable passed in; it is not merged with the old value. Capability names are in `ui/caps.go`; action IDs, lifecycle states, media kinds, playback states and operations, event names, settings keys and `ui.Cancelled(err)` (the user cancelled) are in `ui/names.go`: use the constants instead of writing the strings yourself |
| Python or any language | ✓ protocol directly | `apps/hello-py/hello.py` |
| Mini-programs (JS / TypeScript) | ✓ host `apps/miniapp`. Runtime grants are tracked separately per "host + mini-program": each mini-program needs the user's consent on first use, the host's grants and pre-grants are not passed on to mini-programs, and they can be revoked separately in Settings. Besides the built-in ones, the host also installs signed mini-program packages (same format as in §12, issued by the host vendor's mini-program store) and verifies the signature before installing and before every launch; each mini-program's storage lives in its own directory, at most 1 MB | `Page({data, render, onAction})`, `setData`; `phone.toast / navigateTo / navigateBack / getStorage / setStorage / scanCode / requestPayment` (and `phone.t`, the UI text, §9.1); `ui.text / section / doc / item / input / image / button / progress`; type declarations in `types/phone.d.ts` |
| TypeScript | ✓ developer machine (unit tests, selftest); QEMU still to run | The main language ([29](29-app-development.md)), §9.2: `kario new` / `build` / `package`; `import { reset, on, call, tr, storage } from "kario"`; types `@kario/sdk` (`sdk/ts`); sample `examples/hello-ts` |
| C / C++ | ✓ `sdk/c` (`phoneui.h`, C11, no dependencies) | `phoneui_open` / `phoneui_next` (answers `ping` automatically) / `phoneui_try_next` (for programs with their own event loop: does not block, returns 0 when no whole message is there, still answers `ping`) / `phoneui_pending` (messages the library already holds, which `poll` cannot see) / `phoneui_send` / `phoneui_call` (calls a capability synchronously; messages that arrive meanwhile are queued); JSON writer `phoneui_json_*`; `phoneui_get_string` gets the value of an input field. Example: `sdk/c/examples/hello-cpp` (C++20). The `phone-capi` package in the image ships `libphoneui.so`; the headers and the static library are in the `-dev` package |

### 9.1 UI text and translations

Code is written in English only (comments too; `scripts/no-chinese.py` checks it in `make test`); the UI's words are written as English messages, English being the source that needs no catalog, and their translations go next to them in `locale/<lang>.json` (`{"English": "translation"}`). The system offers four UI languages:

| Language | Tag | Where a missing message comes from |
|------|------|------|
| 简体中文 | `zh-Hans` | English |
| 繁體中文 | `zh-Hant` | Simplified Chinese, then English |
| English | `en` | (the source) |
| Español | `es` | English |

`PHONE_LANG` picks the language: the Shell hands it to every app (one of these four values; a process that gets none uses `zh-Hans`). A message a language's catalog lacks is looked up along the fallback chain in the table, ending with the English source, so a partly translated catalog still works. A message with arguments uses `%s` and `%d`, and its translation in every language keeps the same placeholders in the same order; for text whose word order differs between languages, such as dates, the Shell uses layout messages without placeholders (e.g. `{weekday}, {month} {day}`, which Spanish translates as `{weekday}, {day} de {month}`).

The user chooses in Settings › Language (or "Automatic": by region, CN Simplified Chinese; TW, HK, MO Traditional Chinese; the Spanish-speaking countries Spanish; everything else English; on a developer machine the Shell's own `PHONE_LANG` takes the place of automatic). On a change the Shell's own text switches at once (`i18n.SetLang`; every catalog made with `i18n.New` in a Go package follows), and apps restart as on iOS: the system ends them with their tombstones ([17](17-app-sandbox.md) §5.3), the app in the foreground comes back at once, on the page it was on, those in the background when the user opens them, and each new process gets the new `PHONE_LANG`; an app in a call or working in the background (playing music, VoIP) is not interrupted and switches when it next starts. The renderer and the web processes restart too. An app that wants to come back on its page hands over its page state in `OnSave` (that is how Settings comes back on its Language page).

`scripts/i18n-check.py` checks that every call has a Simplified Chinese translation, that every translation in every language is still used and keeps the placeholders, and reports for each catalog how much of Traditional Chinese and Spanish is translated and how much is missing (missing messages are not errors for now; `--strict zh-Hant` makes them so). The catalogs of the Shell, the Settings app, the terminal front end (`programs/shellcmd`) and the other system apps, the mini-program host and its built-in mini-programs, the samples, the renderers, the web view, the input method, the sandbox self-check, USB sharing, the compatibility test app, Feiqian (`apps/wallet`), the TypeScript calculator, samples and `kario` project template are fully translated; only the `jsrt` test app (test data) still lacks Traditional Chinese and Spanish, `i18n-check.py`'s report being the reference. The Traditional Chinese and Spanish translations are machine translations **not yet reviewed by native speakers**.

| Language | How | Catalog |
|------|------|------|
| Go | `var tr = i18n.New(localeFS)` (`//go:embed locale`), `tr.T("Saved %d photos", n)`; `tr.In("es")` for a fixed language | `locale/` next to the package |
| Mini-programs (JS / TS) | `phone.t("Order placed: %s", id)` | `locale/` in the mini-program's directory (or package), signed with it |
| Python | `T("Clicked %d times", n)` (a small function in `apps/hello-py/hello.py`) | `locale/` next to the script |
| C / C++ | `tr("Hello")` (`sdk/c/examples/hello-cpp`: reads `locale/` from the app directory) | `locale/` in the app directory |
| TypeScript | `tr("Saved %d notes", n)` (the `kario` module, along the fallback chain) | `src/locale/` beside the code, copied into the app directory's `locale/` by `kario build` |
| Manifest | The name, description, settings, permission purposes and the labels of commands and intents are data, written in the language `lang` names, other languages in `locales` (§1) | `manifest.json` |

The mini-program host (each mini-program's own catalogs), the Python and C/C++ samples, the renderers (`paint/`, `gui/`) and the web view (`webview/`) follow the same fallback chain when they read catalogs (unit tests ✓ for the mini-program host and the renderers; the Python and C++ samples and the web view checked on the developer machine with catalogs missing a few messages; none on a real device). A mini-program's name and description in `app.json` have one wording, which the host does not switch by language (**not implemented**).

### 9.2 TypeScript apps

TypeScript is the main language for apps ([29](29-app-development.md)). An app is written in TypeScript only: the `kario` command line (written in Go, with esbuild built in, so no Node needed) compiles it into one ES2023 module, and the system's `kario-jsrt` (QuickJS-ng plus the `kario` module, speaking the protocol of §2 through the C interface `sdk/c`) runs it in the app's sandbox. The code can `import` one module, `"kario"`: there is no QuickJS `std` or `os` module, no file, socket or process; capabilities still go through the broker (permissions, purposes, system UI), and the app's data is reached only through `storage`. Unit tests and the selftest pass on the developer machine; it has not run on QEMU yet ([29](29-app-development.md) §5).

```bash
kario new notes            # manifest.json, src/main.ts, src/locale/zh-Hans.json, tsconfig.json, types/kario/ (the types)
kario build notes          # → notes/build/: manifest.json, main.js, locale/, the icon; the manifest is checked first by the installer's rules
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # builds, then signs and packages; key and certificate as phone-pkg's (§12)
kario package -devstore DIR -o notes.phoneapp notes                 # signed by a development store, which phones in developer mode trust
```

| In the project | Description |
|----------|------|
| Manifest `"runtime": "js"`, `"main": "main.js"` | See §1; no `exec`, and the package carries no executable file |
| `src/main.ts` | The entry point. The local files it imports and pure-JS npm packages are bundled into `main.js`, `"kario"` is left to the runtime; a Node module such as `fs` is not found and the build fails |
| `src/locale/<language>.json` | The translations, beside the code (where `scripts/i18n-check.py` checks them); or in `locale/` at the top of the project, one or the other. `kario build` copies them into the app directory's `locale/` |
| `tsconfig.json`, `types/kario/` | Types for the editor: `"kario"` maps to `@kario/sdk` (`sdk/ts`), `lib` is ES2023 only, with no DOM or Node interfaces. `kario build` does not type-check (that needs Node); the editor reports type errors (VS Code's built-in TypeScript) |

The `kario` module (`sdk/ts/index.d.ts`):

| Interface | Description |
|------|------|
| `reset` / `push` / `replace` / `update` / `pop` | The page stack; the argument is a plain object of a `Screen` (§3). The runtime keeps the page stack and sends it again when a new Shell takes over the connection (`resync`) |
| `toast` / `notify` | Toast, notification |
| `call(cap, args)` | A capability call (§4), returning a Promise. A failure rejects with an `Error` whose `message` is the system's own text and `cap` the capability; `cancelled(err)` tells whether the user cancelled. Files handed over with the reply (`media.pick`, `audio.stream`) do not reach JavaScript (**not implemented**) |
| `on(name, handler)` | `start(arg, restored)`, `action(e)`, `key(key, e)` (keys a focused canvas receives), `input(e)` (every edit of a `live` input), `popped`, `foreground`, `background`, `save()` (returns, on leaving the foreground, the state needed to rebuild the pages, at most 64 KB; if the system ends the process, the new one gets it back in `start`'s `restored`), `event(name, value)` (§5), `notif(n)`, `resync`. One handler each, a new one replaces the old; without a `key` or `input` handler, keys and edits come to `action` as actions as usual |
| `tr(msg, ...args)` | UI text: looked up in `locale/` along the device language's fallback chain (§9.1), `%s`, `%v` and `%d` replaced in order |
| `storage.get` / `set` / `remove` / `keys` / `clear` | A key-value store of JSON values, at most 1 MB in all. The runtime keeps it in the app's data directory (a new file, then a rename), and JavaScript never sees the path; it outlives the process |
| `appId`, `lang` | The app's ID, the device's language |
| Globals | `setTimeout` / `setInterval` / `clearTimeout` / `clearInterval` (on the runtime's event loop, polled together with the protocol's socket), `console.log` / `info` / `warn` / `error` / `debug` (to stderr, which is the log the system keeps for the app), `queueMicrotask`, `atob` / `btoa`, `performance.now`; and all of ECMAScript itself (`Promise`, `JSON`, `Date`, `RegExp`, `Map`…) |

- The types of `@kario/sdk` — `Screen`, `Node`, `Action`, the node kinds and styles, the capability names with their arguments and results, the event names — are generated from `ui/` by `tools/tsgen` (`sdk/ts/ui.d.ts`, `make tsgen`), so the interface has one source; when the Go side changes and the file is not generated again, `make test` fails.
- Bytecode: `kario build -bytecode` also writes `main.jsc` (precompiled by `kario-jsrt --compile`, marked with the engine's version); the runtime takes it first and falls back to `main.js` when the version differs. For the system image only (`scripts/stage.sh`): QuickJS's bytecode format does not resist a hostile writer, so the installer and the store both refuse packages with bytecode; precompiling on the device after installation (like Android's dex2oat) is **not implemented**.
- Sample: `examples/hello-ts` (a list, an input, a soft key, the clipboard, storage, a tombstone, a timer); on the developer machine `stage.sh` compiles it into the system's app directory, and the selftest has a case for it.
- The declarative framework KarioUI (`@kario/ui`, bundled into `main.js` by `kario build`), running and debugging under Node (`kario run`), tests (`kario test`), publishing (`kario publish`): the tutorial [34](34-app-tutorial.md); the system's calculator (`apps/calc`) is written with it.

## 10. Testing

The Shell has a built-in headless script runner, and the same script runs in a terminal, on the pixel renderer (`--gui`) and in the device boot selftest:

```bash
make build
build/usr/bin/phone-shell --script my-app.script        # your test cases
build/usr/bin/phone-shell --selftest                    # the system's built-in test cases
build/usr/bin/phone-shell --selftest --only '视频' --junit report.xml   # only run cases whose names match ('视频' = video), write a JUnit report
```

Common commands:
- Keys and input: `key <key> [count]` (including `call` for the call key and `end` for the end key), `type <text>`;
- Waiting: `wait <text> [ms]`, `wait-re <regex> [ms]` (for text that keeps changing, such as the playback position), `gone <text>`, `at <screen ID>`;
- Screenshots: `snap <name>`;
- System state: `appstate <app> frozen|running|stopped`, `audio <category>`, `frames <frame count>`;
- Simulating external events: `push <app> <title> | <body> | <data>` (playing the role of your server), `tel ring <number>` (simulated incoming call), `timeshift <seconds>`.

The complete list is at the top of `shell/headless.go`.

## 11. Compatibility test suite (for device vendors)

```bash
make cts     # = phone-shell --cts build/cts-report.json
```

- **What runs**: the whole selftest, plus the compatibility test cases. The compatibility test app `com.phone.ctsprobe` (`cts/probe`) is only installed in this mode, and it checks that:
  - capabilities whose permission is not declared are always denied;
  - the interval of background jobs cannot be less than 15 minutes, and jobs can be registered and cancelled;
  - an app with the phone permission can answer and control calls itself;
  - every item of the sandbox self-check passes (the same `sandbox.SelfChecks` as in the Settings app);
  - when the memory limit is exceeded, only the app itself is ended, and the reason is shown;
  - when the app is stuck, the system shows 「没有响应」 (Not responding), and the user can wait for it to recover or close it;
  - after a crash, the system keeps a record that can be viewed in Settings;
  - without the corresponding permissions, the app cannot install packages, grant itself permissions, change system settings, see other apps' photos in the gallery, answer NFC readers (`nfc`), see and change the card pack (`nfc.state` / `nfc.set` need `system`), read an NFC card or send it commands (`nfc.read` / `nfc.transceive` need `nfc_reader`), or see and switch airplane mode and mobile data or send files over Bluetooth (`radio.state` / `radio.set` / `bt.send` need `system`);
  - the app cannot impersonate system UI (a title containing the word for System in any UI language gets the app name prefixed).
- **Provisioning**: before the compatibility test suite starts, it simulates the factory line provisioning a certificate onto the device: a CA representing the certifier (`phone-provision`) issues the device certificate, which is installed into the key service (`csr` / `install_cert`), so the report chains to the certifier's root rather than to the development root the device generated itself.
- **Report**: JSON, containing the device information, the result of each test case, the number of calls, successes and failures for each capability, and the capabilities that were never called.
- **Pass criteria**: all test cases pass, and every capability in §4 has been called at least once. If a capability is added without a test case for it, the compatibility test suite does not pass.
- **The report is signed**: the report's `attestation` field is a device attestation by the device key service over the report digest (the SHA-256 of the JSON with the signature removed), with a certificate chain leading to the root certificate. If the vendor changes any item in the report, the signature check fails. The certifier checks it like this:

  ```bash
  phone-cts-verify --root root.pub cts-report.json   # signature, certificate chain and results must all pass
  ```

  Before `make cts` starts, it simulates provisioning with a temporary CA (previous item), and `phone-shell --cts` saves that CA's root separately as `<report>.root`, for demonstration only; the device boot selftest does no provisioning, and its root is the one the device generated itself on first boot. In mass production the CA is in the certifier's hands (HSM), and the certifier checks with the root it holds itself, trusting nothing that comes with the report.
- **Certification is done once per model**: the vendor runs the compatibility test suite on a sample unit of the model, enters the company name, unified social credit code and model on the certifier's portal, and submits the signed report. The certifier's backend immediately checks that the signature chains to its root, the result is PASS and the device is not unlocked; after staff approve it, the model is added to the public list of certified models (this service is not open source).
- **Each device only needs provisioning**: in mass production, individual units no longer run the whole compatibility test suite. They run the vendor's own factory test at a factory station and, after passing it, go online to obtain a device certificate. The station reads the device's certificate signing request (the key service's `csr`), has the device produce a device attestation over a random nonce used only for this request (app name `system.provision`, proving that the device holds this key and is not unlocked), and passes it to the certifier's provisioning service; the service checks the station's token and quota and that the model is certified, issues the certificate with the CA, and the station installs it back onto the device (`install_cert`). The CA's private key exists only at the certifier, and stations cannot get it; every issuance is recorded. The open-source side is `ReadCSR`, `AttestFor`, `VerifyPossession`, `(*CA).Certify` and `InstallCerts` in the `keystore` package (`phone-provision` is the local version for when the CA is at hand, used for testing). On real devices, connecting the station to the device over USB and putting the keys into the TEE have to be done together with the hardware (**not implemented**). Staff see the requests at the review desk (the system version, number of test cases, capability coverage, renderer and test device ID from the report; the original report can be downloaded) and certify or reject them with a reason, which is recorded in the audit log; vendors check progress with the request number (`GET /v1/certifications/<number>`); certified models are public (`GET /v1/certified`: vendor, credit code, model, system version, certification time). A backend started without `--cert-root` does not accept requests.
- The device boot selftest also runs in this mode; the report is retrieved over the serial port (`build/cts-device.json`, with the root public key in `cts-device.json.root`), and at the end `make yocto-selftest` checks its signature on the host. For the certification plan see 59 (internal document) §2.

## 12. Packaging and release

Signed packages, the installer, delta updates and takedowns are implemented; the store backend is simulated ([07](07-ecosystem-and-store.md) §4.1: developer identity verification, manual review, delta packages, revocation lists, crash report authentication).

**Package format (`phoneapp/1`)**: a tar containing all files of the app directory, plus a `META/signature.json` that:
- lists each file's path, mode (0644 or 0755), size and SHA-256;
- includes the developer certificate: the developer's name and public key, issued by the store's root key;
- is signed as a whole by the developer key (Ed25519).

Packaging and verification both use `pkg`:

```go
dev, _ := pkg.DevDeveloper(keyDir, "我的工作室", store) // developer key + certificate issued by the store ("我的工作室" = "My Studio")
raw, _ := pkg.Build("notes/", dev)                      // notes/ contains manifest.json and the program
p, err := pkg.Open(raw, roots)                          // verification on the device before installing
```

**What the system checks at install time**:

| Check | If it fails |
|------|----------|
| The signature and certificate chain lead to a store root the device trusts; the files and the signature manifest match one to one, with nothing extra and nothing missing | Rejected |
| Only regular files; paths cannot escape the package | Rejected |
| Manifest: `id` is a reverse domain name (not starting with `system.`), `name` and `exec` are present, and `exec` is an executable file in the package root | Rejected |
| The manifest's `nfc`: cards need the `nfc` permission, valid card ids, categories and AIDs (`ui.CheckNFCCards`, which the store backend applies too) | Rejected |
| The manifest's `privacy`: valid kinds and uses; no data sent without `net`; with `net`, the data of every runtime permission listed under `collects` or `local` (`ui.CheckPrivacy`, which the store backend and `kario build` apply too; [33](33-app-guidelines.md) §3) | Rejected |
| A TypeScript app (`"runtime": "js"`): no `exec`, no executable file and no bytecode (`.jsc`) in the package, and `main` is a `.js` file in the package root; the store's review applies the same check (`jsrt.Check`) | Rejected |
| No `granted`, no system-only permissions (`system`, `install`), no unknown permissions | Rejected |
| The name must not be the same as that of an app in the system image | Rejected |
| Updates must be signed by the same developer key | Rejected |
| All of the above pass: the system shows a dialog listing the name, version, developer, issuing store and permissions | Not installed if the user cancels |

**After installation**:
- The code is placed in the system directory `packages/<id>`; the app itself can only read and execute it, not modify it;
- The app only runs the programs in its package and cannot borrow programs with the same name from system directories;
- The signature manifest is kept together with the code (`META/signature.json`). On the first launch after every boot, and after developer mode changes, the system re-checks it against the store roots trusted at that time: if files have been modified, unsigned files have been added, or the store is no longer trusted, the app is not allowed to start (`pkg.VerifyDir`);
- Uninstalling deletes the code and data, and clears the runtime grants and usage records, background jobs, alarms, photo grants and all keys in the key service (the app's own keys, its mini-programs' keys, the push key); the push token is invalidated. An app published later by a different developer under the same ID cannot get any of these.

Which stores the device trusts: `share/phone/trust/<store>.pub` in the image; when developer mode is turned on (「设置 › 开发者模式」 (Settings › Developer mode)), additionally `trust/` in the state directory. The prototype's selftest uses packages signed by the "development store" in the state directory (`pkg.DevStore`), which plays the role of the store backend. After developer mode is turned off, packages signed by the development store cannot be installed, and those already installed cannot start. The roots of the official stores come with the product image or are provisioned on the factory line, and developers register in the store backend ([07](07-ecosystem-and-store.md) §4.1).

Without packaging, you can also put the app directory directly into `share/phone/apps/`, which makes it equivalent to an app in the system image. Do this only on development machines.
