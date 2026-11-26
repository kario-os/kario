> English translation of [the Chinese original](../36-device-switch.md). The Chinese text is authoritative; keep both in sync.

# Switching protocol kario-switch/1

> First version, settled on 2026-11-16; the details filled in on 2026-11-17 from the app author's clarifications (§1, §2), and the same day the path where both phones are on the same Wi-Fi made a proper one (the QR code's `wifi`, §1; stopping when the Wi-Fi changes, §7). The receiver on the phone is implemented (`switchrecv/`, `shell/switch.go`, Settings › My device › Move from an old phone, §7): unit tests and the test vectors ✓, selftest on a developer machine (simulated hotspot, simulated Wi-Fi) ✓, the boot selftest of both images on QEMU ✓ (iwd really starts the hotspot, a simulated old phone joins from a network namespace of its own and reaches port 47800 through the firewall, sends everything, and the import is undone); the QEMU case for the same Wi-Fi (the simulated old phone joining the test access point the phone is on) ✓ on both images, **not tried on a phone**. The "Kario Switch" app on the old phone (Expo, one code base for Android and iOS) is written in kario-internal and **not tried on a phone**. On 2026-11-18 a path without the app was added, reading the old phone over Bluetooth (§8: Kario as the client of PBAP and MAP): unit tests, replayed recorded sessions and the developer machine's selftest ✓, the QEMU case **not passing yet** (skipped for now, see [13](13-prototype.md)), **not tried on a phone**. Why it is done this way: kario-internal's `strategy/64-accounts-and-migration.md`.

## 0. What it does

The user installs "Kario Switch" on the old phone (Android or iPhone), scans the QR code on the Kario phone with it, and moves the contacts, call log, SMS, calendar, photos and videos across. Point to point, with no cloud in between; when it is done the receiver closes and the token is void.

| Data | Android | iOS |
|------|---------|-----|
| Contacts | ✓ (`expo-contacts`) | ✓ |
| Calendar | ✓ (`expo-calendar`) | ✓ |
| Photos, videos | ✓ (`expo-media-library`) | ✓ |
| Call log | ✓ (a native module reads `CallLog`; needs `READ_CALL_LOG`) | The system lets no app read it; read over Bluetooth PBAP instead (§8, Kario being the phone book's client as a car kit is), **not tried on a phone** |
| SMS | ✓ (a native module reads `Telephony.Sms`; needs `READ_SMS`) | The system lets no app read it; the iPhone's MAP only notifies a car kit of new messages and gives none of the earlier ones (§8) |

Without the app on the old phone, Kario can also read its contacts and call log (Android, iPhone) and its messages (Android) over Bluetooth, §8.

Google Play allows `READ_SMS` and `READ_CALL_LOG` only to the default SMS / phone app and a few exceptions, one of which is "backup and restore for users", subject to review. Most users in China install from the vendor stores or straight from a package: the Kario receiver's page can offer the package itself (§6).

## 1. Connecting and pairing

1. On the Kario phone: Settings › My device › Move from an old phone. The receiver comes up: with the phone on Wi-Fi (with an IPv4 address) it listens on the Wi-Fi address (**the Wi-Fi path**: the old phone joins the same network; the user can switch to the hotspot, §7), otherwise on the personal hotspot's (turning the hotspot on if it is off, and off again at the end; **the hotspot path**); TCP 47800, HTTP; it makes this session's X25519 key pair and a random 32-byte token.
2. The screen shows a QR code holding (the hotspot path, the Wi-Fi path):

   ```
   kario-switch://pair?v=1&host=192.168.43.1&port=47800&ssid=Kario-3F2A&psk=...&token=<base64url>&key=<base64url>&name=<device name>
   kario-switch://pair?v=1&host=192.168.1.23&port=47800&wifi=<network name>&token=<base64url>&key=<base64url>&name=<device name>
   ```

   `key` is the receiver's X25519 public key (32 bytes), `token` the token, `ssid` and `psk` the hotspot's: both together, or neither (the Wi-Fi path). `wifi` is the name (SSID) of the network the receiver is on in the Wi-Fi path, for the app to check and to tell the user which network to join; no password: it comes only in the Wi-Fi path, in the place of `ssid` and `psk` and never beside them (a code with both is broken); it is 1 to 32 bytes of UTF-8 text, and a name that is not (not UTF-8, too long) is left out, so the Wi-Fi path may come without it (an earlier receiver leaves it out too) and the app then only says "join the Wi-Fi the Kario phone is on". `host` is a local IPv4 address (not a host name). The query is encoded as Go's `url.Values.Encode` does (a space is `+`, the rest percent-encoded), the fields in the order above; `token` and `key`, here and everywhere below, are base64url without padding (RFC 4648 §5). On a phone with the app installed, scanning it opens the app (a deep link).
3. The hotspot path: the app joins the hotspot (Android and iOS both ask the user to confirm; failing that the user joins by hand, with the name and password the Kario page shows). The Wi-Fi path: the app joins no network; it checks that the phone is on Wi-Fi and, where the platform tells, on the `wifi` network (or that `host` is in the phone's Wi-Fi subnet), and otherwise asks the user to join that network; when it cannot reach the receiver or times out it says what the likely causes are (not the same network; public, hotel and office Wi-Fi often keep devices apart, so the two phones cannot reach each other) and suggests 「改用热点」 (Use the hotspot instead) on Kario and scanning the new code. On both paths the app reaches the receiver at `host:port`.
4. The app makes a one-time X25519 key pair and derives the shared keys:

   ```
   shared   = X25519(app private key, key)
   k        = HKDF-SHA256(ikm=shared, salt=token, info="kario-switch/1", 64 bytes)
   upstream = k[0:32] (app → Kario), downstream = k[32:64]
   ```

   The `salt` is the token's 32 raw bytes, not its base64url text. An all-zero shared secret (the other side's key a low-order point) is refused.

Only someone who scanned this QR code knows `key` and `token`; on top of the hotspot's WPA2 (in the Wi-Fi path the network is shared with others and there is no such layer) the content is encrypted end to end with these keys, and plain HTTP carries nothing but envelopes, so the receiver needs no HTTPS certificate and the app no certificate pinning (React Native's `fetch` cannot pin).

## 2. Messages

The body of every request and reply is an **envelope**:

```
envelope = nonce (12 bytes) || AES-256-GCM(key, nonce, plaintext, aad) || tag (16 bytes)
nonce    = direction (4 bytes: upstream 0x00000001, downstream 0x00000002) || counter (8 bytes, big-endian, from 0, +1 per message)
aad      = HTTP method + " " + the request target as sent (the path, and with a query "?" and the query; no host), e.g. "POST /v1/items/contacts", "PUT /v1/files/7f3c?offset=0"
```

- The query is in the aad, so `?offset=N` is protected too.
- Each app public key (§2.1) has its own upstream and downstream counters, each only growing. The receiver takes an envelope only when its counter is above the last one that opened under that key; one that is not is a replay, answered with 409 (`replay`), and does not count as a request (the 30 minutes do not start again).
- A reply is sealed with the downstream key and the aad of the request it answers, at that key's next downstream counter; every sealed reply takes one, an error's too. The app only asks a downstream counter to be above the last one's.
- Requests go one after another, and the receiver handles one at a time. A retry is a new envelope (a new counter), not the old bytes sent again.
- The first request, `POST /v1/hello`, carries the app's public key in the `X-Kario-Key` header; its body is an envelope too. Later requests do not carry it: the receiver tries the keys it has in turn.
- Header `Content-Type: application/octet-stream`; the plaintext is UTF-8 JSON, a file chunk (§2.3) raw bytes.
- A request's body (the whole envelope) is at most `max_body` bytes (4 MiB). A larger one is not read and gets 413 (`too_large`), sealed under the public key of the last request that opened; in plain JSON before any did. On a 413 the app splits the batch in two and sends the halves. The receiver replies without reading the oversized body and closes the connection, so a phone's HTTP library (OkHttp, NSURLSession) may see the connection reset before it can read the 413: the app always sizes batches and chunks by `max_body`, and the 413 is only the fallback.
- Errors: the reply body is an envelope whose plaintext is `{"error": "...", "code": "..."}` (`bad_offset` adds `offset`). An envelope that does not open (no hello yet, the wrong public key or token, a session that ended, an aad that does not match) gets 401 with `Content-Type: application/json` and the plain body `{"error":"the envelope does not open","code":"bad_key"}`.

### 2.1 hello

```
POST /v1/hello
→ {"app": "kario-switch", "version": "1.0.0", "device": {"brand": "Xiaomi", "model": "...", "os": "android", "os_version": "14"},
   "counts": {"contacts": 812, "call_log": 2000, "sms": 5231, "calendar": 40, "photos": 3120, "videos": 88}}
← {"session": "<id>", "accept": ["contacts", "call_log", "sms", "calendar", "photos", "videos"], "max_batch": 500, "chunk": 1048576, "max_body": 4194304}
```

`accept` are the kinds Kario takes (those the user ticked on the Kario phone, less those it has no place for, §7); the app sends only these. An `app` other than `kario-switch` gets 400 (`bad_hello`). `counts` may be partial (the app asks for permissions kind by kind after `accept`); a later hello fills in the kinds it names.

After a restart the app says hello again with a new key pair under the same token: the receiver takes it and the session goes on (the de-duplication and the files' progress are still there). The receiver remembers each app public key it has seen under this token with that key's last counter, so an old hello sent again is a replay under its own counter (409) and cannot switch the session back to the old keys. Until a key's hello has gone through, its other requests get 409 (`no_hello`).

### 2.2 Items

```
POST /v1/items/<kind>
→ {"items": [...], "last": false}
← {"stored": 500, "skipped": 0}
```

At most `max_batch` items per batch (more get 400 `too_many`); the last batch has `last: true`. The kinds are `contacts`, `call_log`, `sms` and `calendar`; any other gets 404 (`not_found`; photos and videos go by §2.3), one not in `accept` gets 403 (`not_accepted`).

Sending a batch again imports nothing twice: for each kind the receiver keeps the SHA-256 of every item the session stored (taken over the fields it reads, so their order and white space do not matter), and the same item again (in the same batch or a later one) is skipped; so is what Kario has already (§7). `stored` counts what was stored, `skipped` what was skipped; an item that cannot be read is in neither and appears in `failed` at the end. When the destination cannot take the batch for now (a service is not there) the answer is 503 (`unavailable`), nothing of the batch is stored, and the app sends it again a little later.

An item may carry the app's own `id` (as a file's, `[A-Za-z0-9._-]{1,64}`), which `failed` then uses. The items of each kind:

| Kind | Item |
|------|------|
| `contacts` | `{"vcard": "BEGIN:VCARD\r\nVERSION:3.0\r\n..."}`, vCard 3.0 or 4.0 (RFC 6350), the photo inline (`PHOTO`, at most 256 KB); at most 512 KB in all |
| `call_log` | `{"number", "name", "type": "in"/"out"/"missed"/"rejected", "at": Unix seconds, "duration": seconds}`; the number at most 64 bytes, the name at most 128 characters |
| `sms` | `{"address", "body", "type": "in"/"out", "at": Unix milliseconds, "read": true}`; for MMS only the text; the body not empty, at most 64 KB |
| `calendar` | `{"ics": "BEGIN:VEVENT\r\n...END:VEVENT"}` (RFC 5545, one event per item, recurrence rules as they are): from `BEGIN:VEVENT` to `END:VEVENT`, with or without a CRLF after it, its components (`VALARM`) paired, no `VEVENT` or `VCALENDAR` inside, at most 64 KB; a `TZID` is an IANA zone name (`Asia/Shanghai`) |

### 2.3 Files

```
POST /v1/files              → {"id": "<a stable id the app gives>", "kind": "photo"/"video", "name", "size", "mime", "taken_at": Unix seconds, "sha256": "<hex>"}
                            ← {"offset": 0}              the bytes already received when part of it came before; size when it is complete
PUT  /v1/files/<id>?offset=N  body: one envelope whose plaintext is at most chunk bytes from N on
                            ← {"offset": N + chunk length}
```

- `id` is `[A-Za-z0-9._-]{1,64}`; a file's `kind` is the singular `photo` / `video` and needs the plural `photos` / `videos` in `accept`, or it gets 403 (`not_accepted`). `name` is the path's last part without control characters, at most 128 characters; `size` is above 0; `sha256` is 64 hex digits. An offer that is not so gets 400 (`bad_offer`). A `mime` that does not read as `type/subtype` is not used (Kario goes by the name).
- One file is at most 4 GiB, a larger one gets 507 (`too_big`); the session's files together may not take more than the disk's free space when it started less 256 MiB, and the one that would gets 507 (`no_space`). Both go into `failed`.
- The same `id` offered again: with the same kind, size and `sha256` the answer is the bytes received (`size` once complete); with any of them different (it changed on the old phone) the file starts again from 0.
- A `PUT` whose `offset` is not the bytes received gets 400, `{"code": "bad_offset", "offset": <received>}` (`size` for a complete file); a chunk is 1 to `chunk` bytes and may not go past `size`, or it gets 400 (`bad_chunk`); an `id` not offered gets 404 (`not_found`).

After a break the app asks `POST /v1/files` for the `offset` again and goes on from there. Once a file is complete the receiver checks its `sha256`; on a mismatch it answers 422 (`bad_hash`) and drops the file (offered again it starts from 0), with `sha256` in `failed`; a file it cannot store gets 500 (`store_failed`).

v1 keeps the `sha256` up front as specified (the app reads a file once before sending it, to hash it); v2 may move it to the end, hashed while sending.

### 2.4 Done

```
POST /v1/done → {}
← {"contacts": 812, "call_log": 2000, "sms": 5231, "calendar": 40, "photos": 3120, "videos": 88, "failed": []}
```

The counts are what the session stored of each kind. `failed` is a list of `{"kind", "id", "reason"}` (at most 1000 entries; past that they are only counted): an item's `kind` is its kind, a file's `photo` / `video`; `id` is the file's, or the item's own (empty without one); `reason` is a short English phrase without content (`bad number`, `not a vcard`, `sha256`, `no space`…). What came in later leaves the list.

Then the receiver closes and the token is void (anything after gets 401), the keys are dropped and half-received files deleted; it also closes after 30 minutes without a request that opens (replays do not count). The Kario phone lists what this session imported and can undo it in one go (deleting the items and files it brought in, §7).

### 2.5 Errors

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_json` | The plaintext is not the JSON it should be |
| 400 | `bad_hello` | `app` is not `kario-switch` |
| 400 | `too_many` | A batch over `max_batch` items |
| 400 | `bad_offer` | A file offer that is not as §2.3 asks |
| 400 | `bad_offset` | `offset` is not the bytes received; carries `offset` |
| 400 | `bad_chunk` | An empty chunk, one over `chunk`, or past `size` |
| 401 | `bad_key` | The envelope does not open (plain JSON) |
| 403 | `not_accepted` | A kind not in `accept` |
| 404 | `not_found` | No such path, kind or file |
| 409 | `replay` | The counter is not above this key's last one |
| 409 | `no_hello` | This key's hello has not gone through |
| 413 | `too_large` | The body is over `max_body` |
| 422 | `bad_hash` | A complete file's `sha256` is wrong; it was dropped |
| 500 | `store_failed` | The file could not be stored |
| 503 | `unavailable` | The destination cannot take the batch for now; send it again later |
| 507 | `too_big`, `no_space` | The file is too large, or there is no room left |

## 3. Security (along the lines of T/TAF 214-2024)

- Before: on the Kario phone the user confirms which kinds to take; on the old phone the system's permission dialogs ask kind by kind.
- During: only between the two phones; the content is encrypted end to end with keys found only in the QR code; the token is good for one session. The QR code is drawn on a secure surface (hidden from screenshots, recordings, casting and the selftest's snapshots).
- After: the receiver closes and drops the keys (they are only ever in memory, never on disk); the app on the old phone keeps no copy (it only reads, caches nothing locally, and clears its memory on exit).
- The receiver takes requests only on the hotspot's or the current Wi-Fi's address, on no other interface: it binds that one IPv4 address, and a connection from outside that interface's subnet is closed at once. In the Wi-Fi path other devices on the same network can reach the port too, but without the QR code they cannot open an envelope (401); when the Wi-Fi disconnects, moves to another network or changes the address, the receiver stops (§7).
- The log has kinds and counts, never keys, the token, the QR code or content; the record undo uses holds IDs only (§7).

## 4. Versions

`v=1`. Incompatible changes take `v=2` and `/v2/` paths; new fields keep the version, and unknown fields are ignored. The QR code's `wifi` was added so: an app that does not know it takes a code without `ssid` for the Wi-Fi path as before.

## 5. Tests

The app's tests include a mock receiver written from this protocol, and the encryption has fixed test vectors (`switch/test/vectors.json`, in kario-internal, made with Node's OpenSSL: fixed keys, token, derived keys and envelopes). The receiver on the phone uses the same vectors (copied to `switchrecv/testdata/switch-vectors.json`), byte for byte in both directions:

- both public keys, the shared secret and HKDF's output, the same from either side; the token's base64url text as the salt derives other keys.
- the pairing URL byte for byte, and the old phone's side reads the same fields back. The vectors' code is the hotspot path's, without `wifi`; a Wi-Fi path code (the vectors' token and key, `wifi` a name with a space, a `+` and Chinese) is written out in both sides' tests as the same text (`switchrecv/vectors_test.go`'s `wifiPairingURL`, the app's `switch/test/pairing.test.ts`) and compared byte for byte; with `ssid` there is no `wifi`, a name that is not 1 to 32 bytes of UTF-8 is left out, and the old phone's side refuses a code with both.
- every envelope seals to the vector's bytes and opens to its plaintext; a flipped tag, ciphertext or nonce, an aad without the query, the downstream key in place of the upstream one, and one shorter than nonce and tag do not open.
- the vectors' whole session against the receiver: with the random source giving the vectors' session id, the replies to hello, the items, the file offer, the chunk, the replay (409) and done equal the vectors byte for byte; hello after done gets the vectors' 401.

`switchrecv/oldphone` is the old phone's side written separately from this document (none of the receiver's code), and the receiver's tests use it over HTTP: one run through every kind, de-duplication of a batch sent again and of what Kario has, the 503 of a destination that is not there, resuming a file and a second hello from an app that started again, an old hello replayed not switching back to the old key, a chunk with one byte changed (422), a replay (409), the wrong token and public key (401), a kind not ticked (403), a body over `max_body` (413, sent again in halves), a wrong `offset` (400), the file limits (507) and offers that are not as they should be, and the session closing after 30 minutes (shortened in the test) without a request. Fuzz target `switchrecv`: `FuzzEnvelope` ([17](17-app-sandbox.md) §9). The Shell's unit tests (`shell/switch_test.go`) run an import and its undo against a fake phone-smsd and telephonyd; the Wi-Fi path: the code carries the network's name and no hotspot comes up, switching to the hotspot while waiting gives the hotspot's code, a session the old phone reached does not switch; the Wi-Fi moving to another network, changing the address or disconnecting, and the hotspot turned off, all end the session and say why (Wi-Fi down for one look does not count, two do). The developer machine's selftest cases 「换机」 (moving from an old phone) and 「换机（同一 Wi-Fi）」 (on the same Wi-Fi), and QEMU's 「换机（模拟器）」 and 「换机（模拟器，同一 Wi-Fi）」, are in [13](13-prototype.md).

## 6. Phones without the app

Contacts have one more way that needs no app: CardDAV. The old phone adds the Kario account (iPhone has "Add CardDAV Account" in its settings, Android takes DAVx⁵), or its contacts are on iCloud, Nextcloud or Fastmail already; Kario signs in to the same account in Settings › Accounts, and the contacts sync down ([19](19-core-apps.md) §1.1; with real servers **not verified**). The call log, messages and photos still come over this protocol or the page below.

The receiver's page `http://host:port/` offers two things: the "Kario Switch" package to download (Android), and a page for uploading photos with the browser alone (pick files and upload; no end-to-end encryption, only the hotspot's WPA2 and the token). **Not implemented** (`GET /` answers 404 for now).

## 7. The receiver on Kario

`switchrecv/` is the protocol itself (the Go standard library only: `crypto/ecdh`, `crypto/hkdf`, AES-GCM, `net/http`) and hands what arrives over through its `Sink` interface; `shell/switch.go` puts it into the Shell, and the Settings page is `apps/settings/switch.go`, through the system-only `switch.state` / `switch.start` / `switch.stop` / `switch.undo` ([22](22-developer-api.md) §4.5).

- **Start**: the user ticks the kinds (all of them at first) and starts. The Shell leaves out the kinds Kario has no place for (messages without phone-smsd, the call log without telephonyd). With Wi-Fi connected (with an IPv4 address) and the hotspot off it takes the Wi-Fi path: it listens on the Wi-Fi address, and the QR code carries the network's name (`wifi`); otherwise the hotspot path: if the hotspot is on, as it is; if it is off, turned on and off again at the end (a Shell that ends during the session leaves a mark, and the next one turns the hotspot off at start, deleting half-received files as well). It binds port 47800 of that one IPv4 address only, and closes connections from outside its subnet. On a developer machine Wi-Fi and the hotspot are simulated and the receiver listens on `127.0.0.1`.
- **Use the hotspot instead**: in the Wi-Fi path, while the old phone has not reached it yet, the page's 「改用热点」 (Use the hotspot instead; `h`, also under Options; `switch.start` with `hotspot`) ends this session and starts again on the hotspot with the hotspot's code (turning the hotspot on takes the phone off the Wi-Fi, which comes back at the end); a session the old phone has reached stays. Public, hotel and office Wi-Fi often keeps devices apart, so that the old phone cannot reach Kario's address; that is when to use it.
- **The network changes**: every 2 seconds a session looks at the network it listens on (for Wi-Fi only which network it is on and the address, no scan). When the Wi-Fi disconnects (down on two looks in a row: one, during a roam, does not count), moves to another network or changes the address, or the hotspot is turned off (airplane mode), the session ends: the listener closes, the token is void, a hotspot it turned on goes off, rather than waiting for the 30 minutes; the top of the page says why (「接收已停止：手机从 Wi-Fi「A」换到了「B」」, Receiving stopped: the phone moved from Wi-Fi "A" to "B", and the like), what came in stays (the import can be undone; it ended as `network`), and in the Wi-Fi path 「改用热点接收」 (Receive over the hotspot) starts again on the hotspot. On the old phone the connection breaks, and the new code is scanned.
- **QR code**: the Settings page opens on the QR code (right under the instruction; an image node `qr:`, marked a secure surface, the same path as the wallet's payment code: the pixel renderer draws a frame image, the text renderer half blocks), then 「等待旧手机…」 (Waiting for the old phone…) and how to connect: in the hotspot path 「经 Kario 的热点接收」 (Receiving over Kario's hotspot) and the hotspot's name and password (for joining by hand); in the Wi-Fi path 「经 Wi-Fi 接收」 (Receiving over Wi-Fi) with the network's name, 「旧手机要连同一个 Wi-Fi 网络」 (The old phone has to be on the same Wi-Fi network), and why the old phone may not connect and what to do then (use the hotspot); last the secure surface's note. When the page moves to another state (waiting, receiving, ended) it opens afresh at its top, as a new page would. The pairing URL is about 210 bytes, a code 53 modules wide, which does not fit a 40-column screen of the text renderer (it is cut off and cannot be scanned); only the pixel renderer's can be scanned.
- **Progress**: each kind's items stored, skipped and failed, and how many the old phone said it has; the old phone's brand, model and system. At the end, what the import added of each kind and how many failed.
- **Destinations**:

  | Kind | Where it goes |
  |------|---------------|
  | Contacts | The Shell's contacts (the `contacts` package reads the vCard: the name from FN / N / ORG, the numbers and their types from TEL, the e-mail addresses from EMAIL, the postal addresses from ADR, an inline photo from PHOTO (a JPEG, PNG or GIF of at most 1 MB, kept as it came), NOTE, NICKNAME, the organization from ORG and the job title from TITLE, the birthday from BDAY and the anniversary from ANNIVERSARY / X-ANNIVERSARY / the X-ABDATE iPhone labels so (dates without a year too, iPhone's X-APPLE-OMIT-YEAR as well), web addresses from URL; not `CATEGORIES`); one with neither a name nor a number fails; one of the same name with all its numbers there already is skipped; the contacts that came in go to the default book (Settings › Accounts › New contacts go to; on the phone alone without an account), and those in an account's book go to the server with the sync 20 seconds later ([19](19-core-apps.md) §1.1) |
  | Call log | telephonyd's `log.import` ([04](04-ui-language-telephonyd.md)): the number without spaces, dashes, brackets and dots, 3–20 digits, `*` and `#` (`+` first), or it fails; `in` is logged as answered, `out` dialled, `missed` missed, `rejected` declined; the old phone's name is not kept (Kario names calls from the contacts). Put into the log by time, its cap of 200 holding as before, so calls older than the newest 200 do not stay (counted as skipped); one with the same number, direction and time there already is skipped |
  | SMS | phone-smsd's `import` ([32](32-messaging.md)): read or unread as on the old phone, sent ones as sent, marked imported, without notifications; put into their conversations by time; one with the same conversation, direction, time and text there already is skipped |
  | Calendar | The session's events first go into one `.ics` file in Files (「日历 <date>.ics」, Calendar <date>.ics: a `VCALENDAR` around the `VEVENT`s, without `VTIMEZONE`, written anew with each batch, at most 16 MB), which the calendar app ([19](19-core-apps.md) §1) imports in the background when the session ends, reporting the count in a notification; undo first has the calendar take that import out, then deletes the file. Without a calendar app the file stays in Files |
  | Photos, videos | Files: staged inside Files' own directory while they arrive and renamed into it when complete (never read into memory), at most 4 GiB each; named by Files' rule, at most 64 characters, a name Files has already getting a number like " (2)"; dated when they were taken. Bluetooth sending and web pages' file fields read a file whole into memory, so files over 16 MB are not listed there |

- **Undo**: the session's record (`<state dir>/shell/switch.json`) holds only the IDs of the contacts, messages, calls and files it added, so an import can be undone after a restart too; only the last import that added anything is kept. 「撤销这次导入」 (Undo this import, confirmed first) deletes the contacts and files by ID, and has phone-smsd's `delete_imported` and telephonyd's `log.delete` delete the imported messages and calls (only those marked imported); a side that cannot delete for now (its service not there) keeps its IDs, and the undo can be tried again. A contact edited since the import is deleted all the same.
- **Not done**: §6's web page; the iPhone's messages (its MAP does not give them, §8); the Wi-Fi path on real routers (client isolation, one name on two bands, IPv6) is **not tried on a phone**; choosing the hotspot before starting (for now it starts on Wi-Fi, then 「改用热点」); a QR code the text renderer can draw whole.
## 8. Moving over Bluetooth (nothing installed on the old phone)
Settings › My device › Move from an old phone › 「经蓝牙迁移」 (Move over Bluetooth; `b`): nothing goes on the old phone; Kario reads it as a Bluetooth **client**. The phone book and the call log over PBAP (Kario is the PCE; Android phones and iPhones both have a PBAP server, for car kits), messages over MAP (Kario is the MCE; Android phones have a MAP server, the iPhone's only notifies a car kit of new messages and gives none of the earlier ones). What it reads goes to the same destinations as §7's receiver (`shell/switch_bt.go` uses `switchSink`): the same check for what Kario has, the same record, the same "Undo this import". Photos, videos and the calendar do not come this way. The OBEX is Kario's own (package `obex`): the image has no obexd (it needs libical and ICU, 39 MB; `yocto/meta-phone`'s bluez5 bbappend removes `obex-profiles`), so obexd's `org.bluez.obex.Client1`, `PhonebookAccess1` and `MessageAccess1` are not used; the clients are in package `obex` too and, like the servers, run on the sockets BlueZ hands over (Profile1).
1. **Pairing**: pair with the old phone in Settings › Bluetooth first (comparing the code). Kario publishes the service record of a PBAP client (PCE; a BlueZ external profile with `Role` `server`: the record alone, no channel), so the old phone sees that Kario is a phone book client; the MAP client's profile is `client`, Kario publishes no message service.
2. **Choosing the phone**: the 「经蓝牙迁移」 page lists the paired devices; pick one. Of the ticked kinds only contacts, the call log and messages are taken (those Kario has no place for are left out as before). `switch.start` has `bluetooth` (the device's address), see [22](22-developer-api.md) §4.5. When the services BlueZ read at pairing (`UUIDs`) have no phone book or message service, those kinds are noted "The old phone does not give these over Bluetooth".
3. **Allowing it on the old phone**: the Shell connects through BlueZ (`Device1.ConnectProfile`, the PBAP server 0x112F, the MAP server 0x1132) and sends OBEX's CONNECT (Target: PBAP's, MAP's UUID). The old phone then asks its user (Android shows a request to read the contacts and call log, and one to read the messages; an iPhone, as for a car kit, needs syncing contacts turned on for this device in its Bluetooth settings, **not verified**), and the CONNECT's answer only comes once the user has decided: Kario waits 90 seconds, the page says 「请在「…」上允许」 (Allow it on "…"), with no QR code. A refusal (Forbidden and the like) or no answer notes those kinds "Not allowed on the old phone". After that each answer is waited for at most 30 seconds; without one the link goes, and what came stays.
4. **Reading the phone book**: first how many entries each object has (`MaxListCount` 0, `PhonebookSize` in the answer; the page shows "x of y"), then 100 a page (`ListStartOffset`, `MaxListCount`). It asks for vCard 2.1 (car kits all ask for 2.1, and phones write 2.1 most reliably; a 3.0 answer is read as well), with a property selector of only VERSION, FN, N, TEL, ORG, NOTE and X-IRMC-CALL-DATETIME (no photo). Entry 0 of `telecom/pb.vcf` is the phone's own card (PBAP says so): the listing in handle order (`x-bt/vcard-listing`, 1 entry) tells whether the first card is `0.vcf`, and then it is not imported; a phone that does not list is taken to have it, as the profile says. The call log is read from `ich` (received), `och` (dialled) and `mch` (missed), from `cch` when none of the three is there; the kind is `X-IRMC-CALL-DATETIME`'s parameter (2.1's `;MISSED`, 3.0's `;TYPE=MISSED`), else the folder's; a time without a zone is in Kario's zone, one with `Z` or a UTC offset is read so. PBAP has no call durations (0), and Android (AOSP's code) counts a rejected call as received. Only a server of PBAP 1.2 or later (the version in its service record, which BlueZ gives in NewConnection) gets `PbapSupportedFeatures` (download, browsing) in the CONNECT.
5. **Reading the messages**: into `telecom/msg`, the listings of `inbox` and `sent` (`x-bt/MAP-msg-listing`, 100 a page, `FilterMessageType` leaving out e-mail), then GetMessage for each (`x-bt/message`, `Attachment` 0 and `Charset` UTF-8, both mandatory in MAP). The number, time and read state come from the listing (the sender of a received message, the recipient of a sent one, the first of several recipients; the bMessage's vCards when the listing has no number), the text is what lies between the bMessage's `BEGIN:MSG` and `END:MSG` (`LENGTH` is not trusted: phones count it differently; a text in several parts is joined), an MMS's is the `text/plain` parts of its MIME. Each is checked by §2.2's rules (a number of at most 64 bytes, a text not empty and at most 64 KB, a time); one that fails them fails. The iPhone's message server lists nothing and has no `sent`: the page says 「经蓝牙只过来了一部分」 (Only part of them came over Bluetooth).
6. **To the destinations**: each page goes as one batch to `switchSink` (contacts, call log, messages), which tells what Kario has already (skipped) as in §7; the same item again in the same move (a phone ignoring `ListStartOffset` gives everything on every page) is skipped by its content's SHA-256 and not counted twice. When a destination is away for a moment (phone-smsd or telephonyd restarting) the batch goes again a second later, and after 3 tries it fails.
7. **End and undo**: once read (or when the user presses Stop: the link closes at once) the import is recorded in `switch.json` as §7's are (IDs only, plus why a kind did not all come), the page lists each kind's count, and "Undo this import" is there. When nothing came, the page says why (not allowed, the phone does not give it over Bluetooth, Kario has it all). The log gets kinds, counts and the device's address.
### 8.1 OBEX interoperability
| Item | How |
|------|-----|
| OBEX over L2CAP (GOEP 2.0) | When BlueZ hands over L2CAP (`SOCK_SEQPACKET`), each read and write is one OBEX packet, no larger than the link's send MTU (`BT_SNDMTU`) and what the peer's CONNECT said; RFCOMM is a byte stream |
| Single Response Mode (SRM) | Over L2CAP only. The client puts `SRM: enable` in an operation's first packet; when the peer's first answer has it too, a GET no longer takes a request per answer and a PUT only waits for the last packet's answer; `SRMP: wait` in the peer's answer makes a PUT wait for the next answer. The server (a car kit reading Kario's phone book, pushing a file) does the same: a GET's answers go one after the other (`SRMP: wait` in a request makes it wait for the next request), a PUT gets answers to its first and last packets only; refused midway (too large), it answers once with the error and not the operation's packets after it |
| Connection ID, Target, Who | The client's CONNECT has a Target, and every request after it starts with the Connection ID the peer gave (none when it gave none); the answer's Who is not checked (some phones leave it out). The server gives a session with a Target a Connection ID, takes requests without one (some car kits leave it out) and answers one with another with Service Unavailable (0xD3) |
| PBAP feature bits | A server of 1.2 or later gets `PbapSupportedFeatures` = 0x00000003 (download, browsing) in the CONNECT; an earlier one no application parameters |
| vCard property selector | The client asks for VERSION, FN, N, TEL, ORG, NOTE and X-IRMC-CALL-DATETIME (bits 0, 1, 2, 7, 16, 17, 28). Kario's server writes what a car kit's selector asks for: in 2.1 the selected properties and N and TEL, in 3.0 always FN, the call time only when selected; all of them without a selector or with 0 |
| Phones' habits | Not taking `MaxListCount` 0 (sending everything), not taking the offset (everything, or the first page, on every page), no `PhonebookSize`, 3.0 for 2.1, no card 0, folded vCard lines and quoted-printable soft breaks, a card inside 2.1's `AGENT`, loose listing XML (case, HTML entities, unclosed elements), MAP times with or without a UTC offset, a wrong bMessage `LENGTH`, a text in parts: all read |
| Not done | OBEX authentication (a peer asking for it answers Unauthorized, taken as not allowed); whether GetMessage marks the old phone's messages read is **not verified**; with several message server instances (Android has others for e-mail) BlueZ connects the first record, **not tried on a phone** |
### 8.2 Tests
- Package `obex`: the clients against a scripted peer written from the protocols, checked byte for byte: the CONNECT (Target; the feature bits only to a 1.2 server), the Connection ID first, a pull's type, name and application parameters (the property selector by its bits), MAP's SETPATHs and GetMessage's mandatory parameters, SRM asked for over L2CAP only; SRM's GET (all of it for one request), back to a request per answer with a peer that does not turn SRM on, a PUT's SRMP wait; Kario's server in SRM (a PUT with two answers, a GET for one request, the session still in step after a refusal midway, a request's SRMP wait), a wrong Connection ID answered with 0xD3, the property selector; an old phone not answering the CONNECT in time, refusing it, an object over the bound. Parsers: vCards written the way Android and the iPhone write them (`obex/btphone/fixtures/`) cut into cards, call history entries, time formats, message listings, bMessages (received, sent, MMS, in parts, `native` encoding, broken). Fuzzing: `FuzzOldPhone` (whatever the old phone answers, the pulls end within their bounds) and `FuzzParsers` ([17](17-app-sandbox.md) §9).
- `obex/btphone`: the simulated old phones (Android: 2.1, quoted-printable, card 0, two SMS and an MMS in the inbox, one sent; iPhone: 3.0, no card 0, an empty message listing, no `sent`; optionally ignoring the offset), whose user may allow, refuse or not answer. The clients read everything from them over a byte stream and over an L2CAP-like packet link. **Recorded sessions** (`obex/btphone/testdata/*.session`: a PBAP session over L2CAP and a MAP session over RFCOMM, packet by packet in hex, recorded anew with `-update`) are played back, and every packet the client sends must be the recorded one; a session captured from a real phone (btmon) in this form plays the same way. These two were recorded against the simulated phone, not a real one.
- The Shell (`shell/switch_bt_test.go`, with fake phone-smsd and telephonyd): a whole move from the Android phone (without its own card, a withheld number failing, an MMS's text, unread, a time with a UTC offset, no content in the log), undo, a move again after it and once more with everything there; the iPhone (only part of the messages); the old phone refusing the phone book and allowing messages; Stop while it asks; a headset without the services, a device not paired, no phone-smsd.
- The developer machine's selftest 「换机（蓝牙）」 (Moving from an old phone, Bluetooth): the simulated Android phone and iPhone (`bt-oldphone`; their user's answer `bt-oldphone-allow`), pairing in Settings, moving over Bluetooth, checking contacts, messages and recent calls, undo ✓. QEMU's 「换机（模拟器，蓝牙）」 (Moving from an old phone, emulator, Bluetooth): btvirt's second controller plays the old phone, really through BlueZ's pairing, SDP, ConnectProfile and RFCOMM (BlueZ handed over RFCOMM connections only; Single Response Mode over L2CAP is not reached on QEMU), passes on QEMU ✓ (2026-11-19; on 2026-11-20 it ran for a while only where `KARIO_SELFTEST_BT_OLDPHONE` was set: btvirt sometimes died of SIGBUS, its main loop calling the callback of a timer already removed, and with BlueZ's fixes in the layer it runs everywhere again, see [13](13-prototype.md)); what made it fail before were out-of-date refreshes of the Settings › Bluetooth page, see [13](13-prototype.md).
- **Not tried on a phone**: not with a real Android phone or iPhone; their consent prompts, whether an iPhone lets a phone (not a car kit) read its phone book, and real amounts of data and speed are not verified.
- **Undo**: the session's record (`<state dir>/shell/switch.json`) holds only the IDs of the contacts, messages, calls and files it added, so an import can be undone after a restart too; only the last import that added anything is kept. 「撤销这次导入」 (Undo this import, confirmed first) deletes the contacts and files by ID, and has phone-smsd's `delete_imported` and telephonyd's `log.delete` delete the imported messages and calls (only those marked imported); a side that cannot delete for now (its service not there) keeps its IDs, and the undo can be tried again. A contact edited since the import is deleted all the same. With a contacts account signed in, those of them already on the server are deleted there too by the next sync (unless someone changed them there; a changed one comes back to the phone, "an edit beats a delete", [19](19-core-apps.md) §1.1).
- **Not done**: §6's web page; iOS's call log and messages (Bluetooth PBAP, MAP); the Wi-Fi path on real routers (client isolation, one name on two bands, IPv6) is **not tried on a phone**; choosing the hotspot before starting (for now it starts on Wi-Fi, then 「改用热点」); a QR code the text renderer can draw whole.
