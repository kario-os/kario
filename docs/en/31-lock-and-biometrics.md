> English translation of [the Chinese original](../31-lock-and-biometrics.md). The Chinese text is authoritative; keep both in sync.

# Lock screen and biometrics: the lock-screen PIN, fingerprint unlock, fingerprint payments

> Status: **prototype ✓ (developer machine)**. The lock-screen PIN, locking when the screen goes off, emergency calls and hidden notification contents on the lock screen, enrolling a fingerprint and unlocking with it, disabling after failures in a row, fingerprint payments and the payment key voided when a new finger is enrolled pass in 9 selftest cases on the developer machine (run with both the text and the pixel renderer); `keystore` and `hal/fingerprint` unit tests ✓. The fingerprint sensor is **simulated**; real sensors are **not implemented** (they depend on the hardware and the vendor's TEE, see §4.1); templates, matching and tokens in the TEE are **not implemented** (R15): phone-keystored stands in for it. Not run on QEMU (the lead runs that), not on a real phone.

## 0. Conclusions

- Without a lock-screen PIN everything is as before: the keypad lock is Nokia's, the left soft key then `*` undoes it, no secret involved.
- Once a PIN (4 to 16 digits) is set in 「设置 › 安全」 (Settings › Security), the keypad lock becomes a secure lock, as on Android and iOS: the phone locks when the screen goes off (a grace period can be set) and starts locked; the lock screen hides the app that was in front, and notifications show only the app's name and a count (their contents can be allowed in Settings); only the PIN or an enrolled fingerprint undoes it.
- What works locked: dialling emergency numbers, answering a call, stopping an alarm; NFC door cards and the express transit card work with the screen off and the phone locked without a key, and a double press of the power key opens the quick card view ([30](30-nfc.md) §5). Other features can add their own lock-screen shortcuts (§2.3).
- The PIN is typed only on system screens and checked by phone-keystored, which keeps a salted, slow hash (PBKDF2 over an HMAC with a key derived from the device key); the PIN itself is never written to disk or logs, and the Shell does not keep it. 5 wrong in a row cost a 30-second wait, more cost longer, and a restart does not reset the count.
- Fingerprints go through `hal/fingerprint`: the developer machine and QEMU have a simulated sensor, which the selftest touches with `finger <name>`. Templates and matching are in phone-keystored (the TEE's stand-in); a match yields only an authentication token (after Android's HardwareAuthToken), which the Shell passes on.
- Fingerprint payments: `auth.confirm` gains `bio_key`; a biometric-bound key signs only after a fingerprint match and is voided when a finger is enrolled or removed (iOS's biometryCurrentSet, Android's setInvalidatedByBiometricEnrollment). The system's confirmation dialog says 「触碰指纹传感器确认，或输入支付密码」 ("touch the fingerprint sensor to confirm, or enter the payment PIN"), the signature still covers the text shown, and the server still checks it with `keystore.VerifyConfirm`.

## 1. Compared with Android and iOS

| Aspect | Android | iOS | This platform |
|------|---------|-----|--------|
| Lock-screen credential | PIN, pattern or password, checked by Gatekeeper (TEE), with backoff by the count of failures | Passcode, checked by the Secure Enclave, with growing waits after failures | A 4–16 digit PIN, checked by phone-keystored (TEE **not implemented**, R15); after 5 failures 30 seconds, then longer, not reset by a restart ✓ |
| When it locks | When the screen goes off (with a delay setting), at start | When the screen goes off (the "require passcode" time), at start | When the screen goes off (immediately, 5 s, 30 s, 1 min, 5 min), at start ✓ |
| What works locked | Emergency calls, answering calls, alarms, lock-screen shortcuts | The same | The same; lock-screen shortcuts are for system features (§2.3) ✓ |
| Notifications on the lock screen | Sensitive content can be hidden | "Show previews when unlocked" by default | By default only the app's name and a count; contents can be allowed in Settings ✓ |
| How many fingerprints | Usually at most 5 | Touch ID: at most 5 | At most 5 ✓ |
| Where templates live | The fingerprint TA in the TEE | The Secure Enclave | In phone-keystored, encrypted on disk with a key derived from the device key (TEE **not implemented**) |
| Failures | 30 seconds after 5, the credential after 20 | The passcode after 5 | Disabled after 5 in a row until the PIN ✓ |
| When the credential is required | After a restart, after 72 hours without it, and more | After a restart, after 48 hours without unlocking, and more | After a restart, after 48 hours without the PIN ✓ |
| Removing the credential | The fingerprints go too | The same | The same ✓ |
| Biometrics in payments | BiometricPrompt + CryptoObject; a key can be set to be invalidated by new enrolments | LocalAuthentication + SecAccessControl (biometryCurrentSet) | `auth.confirm{bio_key}`, a biometric-bound key voided when the set of fingers changes ✓ |
| Authentication tokens | HardwareAuthToken (an HMAC, its key new at each boot) | Inside the Secure Enclave | Android's layout and rules (§4.3) ✓ |

## 2. The lock screen

### 2.1 When it locks

`c.keylock` is the one lock state and `c.locked()` its predicate; the PIN only decides how it is undone (`c.secureLocked()`).

| Situation | No PIN | With a PIN |
|------|-------------|-------------|
| Left soft key then `*` on the idle screen | Keypad lock | Secure lock |
| Automatic keypad lock (Settings › Keypad lock) | Keypad lock | Secure lock |
| The screen goes off | No lock (as before) | Locks once the grace period is over (immediately by default): "immediately" locks in the same housekeeping round that turns the screen off; a grace period is counted by the housekeeping, every 5 seconds while the screen is off, and checked once more before a key wakes the screen |
| Start, Shell restart | No lock | Locked; the Shell locks by the state it remembered, then follows phone-keystored |

When a secure lock engages, the Shell closes the menus and dialogs on the screen (a call, an alarm and the SIM PIN screen stay), hides the app in front and the system pages above the idle screen, and leaves only the idle screen; unlocking gives them back as they were.

### 2.2 What works locked

| Key or event | Secure lock |
|------------|--------|
| Digit keys, OK, the left soft key | Bring up the PIN screen (a digit counts as the first one) |
| Call key | If what was typed on the PIN screen is an emergency number (112, 110, 119, 120, 122, 911, 999), it is dialled at once, no unlocking; the digits are never shown, so a PIN starting like an emergency number gives nothing away. The call key on the lock screen also brings this screen up |
| Fingerprint | An enrolled finger unlocks (with the screen off, the touch lights it first); an unknown one shows 「未能识别指纹」 ("fingerprint not recognized") |
| Incoming call | The system's answering screen as always; once answered only the call screen is usable: menus, the task switcher and Back cannot leave it; after hanging up the lock screen is back |
| Alarm | Rings and stops as always |
| Notifications | The idle screen shows only the app's name and a count, like 「飞钱：1 条通知」 ("Feiqian: 1 notification"); a banner shows only the app's name; with 「锁定时显示通知内容」 (notification contents when locked) on in Settings they show as usual |
| End key | Back to the lock screen (after an emergency call); on boards without a power key, two presses on the lock screen after a pause: the quick card view ([18](18-nokia-form-factors.md) §5.1) |
| Power key | Screen off, screen on (screen off locks by the rules of §2.1); two presses open the quick card view over the lock screen without unlocking, the chosen card presented for 60 seconds, a payment card needing unlocking first ([30](30-nfc.md) §5); held, the power menu |

### 2.3 Lock-screen shortcuts (for other features)

The lock screen can offer system features that work without unlocking (such as a torch). They are listed in `lockShortcuts` in `shell/lock.go`, one line per feature, which can sit in an `init` of the feature's own file. None is registered now: NFC's old → shortcut is gone, as cards work without a key and the power key opens the quick card view ([30](30-nfc.md) §5).

```go
lockShortcuts = append(lockShortcuts, lockShortcut{Key: KeyF2, Label: "Torch", Run: (*Core).torch})
```

Rules: when `Run` is called the phone is still locked, so it may only raise a system overlay; everything else stays behind the lock. `Label` is English and is translated where the lock screen's hint shows it. Keys the lock screen keeps for itself: the digits, OK, the left soft key, `*`, the call key, the end key and Back (the power key does not pass through the lock screen); free are the right soft key and the arrows.

## 3. The PIN

| Step | How |
|------|------|
| Set, change, remove | Settings › Security; the PIN is typed on the system's own screen (the Settings app calls `settings.set lock_pin` and the reply waits for the user to finish), and the Settings app never sees it; changing asks for the current PIN first and the new one twice; removing asks for the current PIN and removes the fingerprints too |
| Storage | phone-keystored's `keystore/lock.json`: a random salt, PBKDF2-SHA256 (100,000 rounds) over an HMAC with a key derived from the device key (the file is no use on another device; in production the TEE's hardware key); plus the "secure user ID", made when the PIN is set and carried by tokens |
| No plaintext | The PIN is in the Shell's memory only while half typed on a system screen, and dropped once sent; never written to disk or logs |
| Wrong attempts | 1–4 in a row: the screen says how many more are free; 5–9: a 30-second wait after each; 10–14: 1 minute; 15–19: 5 minutes; 20–29: 30 minutes; 30–39: 1 hour; then 1 day. The count is in `lock.json`; after a restart the current wait starts over (a restart never shortens it) |
| The payment PIN | The lock-screen PIN and the payment PIN of the trusted confirmation are two things (like Alipay's payment password and the phone's lock-screen passcode); merging them is a later choice, **not implemented** |

## 4. Fingerprints

### 4.1 The HAL and sensors

`hal/fingerprint` defines `Sensor`: `Capture` waits for a touch and reads its features, `Enroll` starts an enrolment (`Add` takes touch after touch and reports progress, then gives the template), `Match` compares features with a template. The package is used only inside phone-keystored: readings and templates never leave that process.

| Backend | Status |
|------|------|
| Simulated (`Sim`, developer machine and QEMU) | ✓: each name is a different finger, each touch a slightly different reading; an enrolment takes 5 touches and another finger does not count; the selftest command `finger <name>` goes through the Shell to phone-keystored (`fp.sim_touch`, a test hook like the simulated baseband's `sim.ring`) |
| A phone's SPI sensor (FPC, Goodix) | **Not implemented**, depends on the hardware: there is no mainline driver; the vendor drives it from a trusted application in the TEE, which also enrols and matches, so a backend is a client of that trusted application and needs the vendor's TEE. The test phone, a Redmi Note 4X, has an FPC sensor with no mainline driver |
| A USB sensor (one libfprint supports) | **Not implemented**; libfprint is LGPL, so it could only run as a separate process, the backend talking to it across processes, never linked into the platform |
| Phones without a usable sensor | The board configuration leaves out `fingerprint` ([24](24-sku-framework.md)), so phone-keystored has no sensor; Settings says 「没有指纹传感器」 (no fingerprint sensor) |

phone-keystored picks its backend from the board configuration (`fingerprint` in `sku.json`): the emulator's board says `sim`, a real phone's board leaves it out until its sensor has a driver, so it has no fingerprints; the developer machine, with no `sku.json`, uses the simulated backend; `--fingerprint` overrides it. On a phone the sensor's interrupt should be a wakeup source, so a touch wakes it from suspend (**not implemented**); on the developer machine the `finger` command lights the screen first.

### 4.2 Templates and matching (keystored standing in for the TEE)

| Step | How |
|------|------|
| Enrolling | First the PIN on the system screen: phone-keystored issues a challenge, a correct PIN yields a token for that challenge, and enrolling takes that token for 10 minutes (Android's way); then the system's enrolment screen lights its progress step by step, and a finger already enrolled is refused |
| Storage | `keystore/fingerprints.json`: names and templates, the templates encrypted (AES-GCM) with a key derived from the device key; plus the "fingerprint set ID", new at each finger added or removed |
| Matching | Only inside phone-keystored; a match yields a token, never the template or the reading |
| Rename, delete | Settings › Security › Fingerprints (the PIN first to get in); deleting takes that token too |

### 4.3 Authentication tokens

After Android's HardwareAuthToken: the PIN check and the fingerprint matcher each make one, and operations that need the user (enrolling a finger, signing with a biometric-bound key) take it. The MAC key is made at random at each start of phone-keystored, so only it can make and check tokens and none survives a restart; the Shell only passes them on.

```
version(1) | challenge(8) | secure user ID(8) | authenticator ID(8) | type(4: 1 PIN, 2 fingerprint) | time(8: ms since start) | HMAC-SHA256(32)
```

### 4.4 Rules

| Rule | How |
|------|------|
| At most 5 fingerprints | A sixth cannot be enrolled |
| 5 unrecognized in a row | Fingerprints are disabled and the lock screen asks for the PIN; a correct PIN brings them back (unlocking and payments share the count) |
| After a restart | The first unlock needs the PIN (phone-keystored refuses fingerprints until it has seen the PIN since it started) |
| 48 hours without the PIN | The same |
| Removing the PIN | All fingerprints go, and the biometric-bound keys are voided with them |
| What they are used for | Settings › Security › Fingerprints: 「解锁手机」 (unlocking the phone, on by default) and 「支付」 (payments, off by default) |

## 5. Fingerprint payments

1. The app calls `auth.confirm{key, bio_key, prompt, payload}` (Feiqian: `key: "pay"`, `bio_key: "pay-bio"`). If the user turned on fingerprints for payments and has one enrolled, the system's confirmation dialog says 「触碰指纹传感器确认，或输入支付密码」 and waits for both a finger and the payment PIN.
2. A fingerprint match: phone-keystored first checks that the `bio_key` key exists and is bound to the current set of fingers, then waits for the touch; a match yields a token valid for one signature with this very key (within 30 seconds); the Shell hands the token to `confirm_bio`, and the key signs "the text shown + the payload". The proof has `key_alias` `pay-bio` and `bio: true`.
3. The payment PIN: signs with `key` as before; afterwards phone-keystored makes the `bio_key` key (if missing or voided), bound to the current set of fingers. So a biometric-bound key is made **only after the payment PIN confirmed**: someone who learnt the lock-screen PIN and enrolled their own finger voids the old key and still cannot pay.
4. After a finger is enrolled or removed: the dialog explains 「开通指纹支付后指纹有变动，请用支付密码确认，指纹支付随之重新开通」 ("the fingerprints changed since fingerprint payment was set up: confirm with the payment PIN to set it up again") and takes only the payment PIN; once confirmed the new key is made and the next payment can use a finger again. The first use goes the same way (「先用支付密码确认一次，之后就能用指纹支付」, "confirm with the payment PIN once to start paying with a fingerprint").
5. The server (the simulated merchant inside Feiqian) checks as before with `keystore.VerifyConfirm`, step by step: manufacturer root → device certificate → app key certificate → signature → content. A biometric-bound key's certificate says it is fingerprint-bound (`keystore.AppKeyCertMsgBio`), and `VerifyConfirm` picks the certificate form by the proof's `bio`, so the server knows a fingerprint confirmed it; Feiqian accepts both `pay` and `pay-bio`.

| Key | Confirmed by | What its certificate says |
|------|----------|-----------|
| `key` (such as `pay`) | The payment PIN | `user_auth` |
| `bio_key` (such as `pay-bio`) | A fingerprint, within 30 seconds of the match, once | Fingerprint-bound; voided once the set of fingers changes |

Mini-program hosts still pass only `key` and are not affected.

## 6. Settings › Security

| Item | How |
|----|------|
| Lock-screen PIN | Set, change, remove, all typed on the system screen (`settings.set` `lock_pin`: 1 set or change, 0 remove) |
| Lock after the screen goes off | Immediately, 5 s, 30 s, 1 min, 5 min (`lock_grace`, seconds) |
| Notification contents when locked | A switch (`lock_show_content`) |
| Fingerprints | The PIN on the system screen first (`fingerprint_auth`); the list of fingerprints, rename (`fingerprint_rename`, the ID as the value and the name as `text`), delete (`fingerprint_delete`); add (`fingerprint_enroll`, the system's enrolment screen); the two switches 「解锁手机」 and 「支付」 (`fingerprint_unlock`, `fingerprint_pay`) |

`settings.get` has `security`: whether a PIN is set, the grace period and its choices, whether notification contents show, the sensor, the fingerprints (IDs and names only), the maximum, the two switches, and why fingerprints cannot be used for now (`restart`, `timeout`, `lockout`). The Settings app's code is `apps/settings/security.go`; the Shell's side is `shell/lock.go`, `shell/lockpad.go` and `shell/biometric.go`.

## 7. Interfaces

phone-keystored's methods (only the Shell calls them; `keystore/methods.go`):

| Method | What it does |
|------|--------|
| `lock.status` | The PIN, any wait, the sensor, the fingerprints, why the PIN is needed |
| `lock.set_pin` / `lock.clear_pin` / `lock.verify` | Set or change, remove, check the PIN; checking takes a challenge and a correct PIN yields a token |
| `fp.challenge` / `fp.enroll` / `fp.enroll_next` | Enrolling: the challenge, the start (with a PIN token), waiting for the next touch and reporting progress |
| `fp.auth` | Waits for a touch and matches it; with `app` and `key` the match is for a signature with that biometric-bound key |
| `fp.cancel` | Ends the wait (and an enrolment) |
| `fp.rename` / `fp.remove` | Rename, delete (deleting takes a PIN token) |
| `confirm`'s `bio_key` / `confirm_bio` | Makes the biometric-bound key after the payment PIN confirmed; signs with it given a fingerprint token |
| `fp.sim_touch` | The simulated sensor's test hook |

## 8. Tests

| Environment | Result |
|------|------|
| Unit tests (`keystore`) | PIN format; the slow hash holds no plaintext; changing needs the old PIN; 30 seconds after 5 wrong, 1 minute after 10; a restart does not shorten a wait; a right PIN resets the count; a tampered token and one from before a restart are refused; enrolling needs a fresh PIN token (an expired one fails); another finger and an enrolled one are refused; at most 5; rename; deleting needs a token; matching: disabled after 5 failures, the PIN brings it back, 48 hours, the PIN after a restart, templates usable across a restart; removing the PIN removes the fingers; cancelling a wait; biometric-bound keys: none before the PIN confirmed, its signature passes `VerifyConfirm`, a token works once, an unlock's token cannot sign, the payment PIN cannot sign with it, it fails as a PIN key, enrolling a new finger voids it and it can be made again, a token expires after 30 seconds ✓ |
| Unit tests (`hal/fingerprint`) | The enrolled finger matches 200 times out of 200, another finger 0 out of 200; enrolling refuses another finger; waiting for a touch; a touch just before the wait counts, a stale one does not ✓ |
| Developer-machine selftest (`--only '^锁屏'`, text and pixel renderers) | 9 cases ✓: setting the PIN; the fingerprint page asks for the PIN first, enrolling with 5 touches (another finger does not count), turning on fingerprint payments; locking when the screen goes off, the lock screen hiding the calculator, a touch unlocking back to the calculator; the lock screen showing only 「飞钱：1 条通知」; an unknown finger disabled after 5, a wrong PIN explained, a right one unlocking; dialling 112 locked; Feiqian set up with the payment PIN the first time, then paying by fingerprint and passing the merchant's check; after another finger is enrolled, back to the payment PIN, and once confirmed the new finger pays too; removing the PIN brings back the keypad lock |
| QEMU, real phone | Not run (the lead runs QEMU; real phones have no fingerprint driver) |

## 9. Not done

| Item | Notes |
|----|------|
| In the TEE (R15) | PIN checks, templates, matching and the token key belong in an OP-TEE trusted application, the PIN reaching it through the trusted input path; now it is the phone-keystored process |
| Real fingerprint sensors | See §4.1: FPC and Goodix need the vendor's TEE; USB sensors through libfprint in a separate process; all **not implemented** |
| The sensor waking the phone from suspend | The sensor's interrupt as a wakeup source on a phone, **not implemented** |
| Back to the call from the lock screen | Under a secure lock, once the call screen was left by some way other than the end key, the lock screen offers no way back to it without unlocking, **not implemented** |
| IFAA / SOTER | The Alipay and WeChat biometric standards, **not implemented** (they need the vendor's TEE and certification) |
| Face, pattern, password | Only a numeric PIN and fingerprints |
| One PIN for the lock screen and payments | See §3, **not implemented** |
