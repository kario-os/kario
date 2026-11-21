> English translation of [the Chinese original](../31-lock-and-biometrics.md). The Chinese text is authoritative; keep both in sync.

# Lock screen and biometrics: the lock-screen PIN, fingerprint unlock, fingerprint payments

> Status: **prototype ✓ (developer machine)**. The lock-screen PIN, locking when the screen goes off, emergency calls and hidden notification contents on the lock screen, enrolling a fingerprint and unlocking with it, disabling after failures in a row, fingerprint payments and the payment key voided when a new finger is enrolled pass in 9 selftest cases on the developer machine (run with both the text and the pixel renderer); `keystore` and `hal/fingerprint` unit tests ✓. The fingerprint sensor is **simulated**; real sensors are **not implemented** (they depend on the hardware and the vendor's TEE, see §4.1); templates, matching and tokens in the TEE are **not implemented** (R15): phone-keystored stands in for it. QEMU selftest ✓ (the 9 lock-screen cases), not on a real phone. The factory reset and the anti-theft lock (§11) were implemented on 2026-11-21: unit tests ✓, 2 developer-machine selftest cases ✓; the wipe at boot on a device has only host tests of its script and its QEMU case is **not run yet**, **not tried on a phone**.

## 0. Conclusions

- Without a lock-screen PIN everything is as before: the keypad lock is Nokia's, the left soft key then `*` undoes it, no secret involved.
- Once a PIN (4 to 16 digits) is set in 「设置 › 安全」 (Settings › Security), the keypad lock becomes a secure lock, as on Android and iOS: the phone locks when the screen goes off (a grace period can be set) and starts locked; the lock screen hides the app that was in front, and notifications show only the app's name and a count (their contents can be allowed in Settings); only the PIN or an enrolled fingerprint undoes it.
- What works locked: dialling emergency numbers, answering a call, stopping an alarm; NFC door cards and the express transit card work with the screen off and the phone locked without a key, and a double press of the power key opens the quick card view ([30](30-nfc.md) §5). Other features can add their own lock-screen shortcuts (§2.3).
- The PIN is typed only on system screens and checked by phone-keystored, which keeps a salted, slow hash (PBKDF2 over an HMAC with a key derived from the device key); the PIN itself is never written to disk or logs, and the Shell does not keep it. 5 wrong in a row cost a 30-second wait, more cost longer, and a restart does not reset the count.
- Fingerprints go through `hal/fingerprint`: the developer machine and QEMU have a simulated sensor, which the selftest touches with `finger <name>`. Templates and matching are in phone-keystored (the TEE's stand-in); a match yields only an authentication token (after Android's HardwareAuthToken), which the Shell passes on.
- Minor mode's parental PIN ([19](19-core-apps.md) §1.2) is in phone-keystored too, kept and counted apart from the lock-screen PIN, checked the same way with the same waits after wrong tries; when it is forgotten, a phone with a parent's account linked resets it once the parent confirms on their page, one without needs reflashing (§10).
- The factory reset is in 「设置 › 更多设置」 (Settings › More settings): it takes the lock-screen PIN (the parental PIN in minor mode) and the system's own last confirmation, and the phone restarts and erases its data before the services start; the system and its updates, and the phone's identity from the factory (device keys and certificates), stay. A phone with a Kario account signed in has an anti-theft lock (as Android's FRP): erased other than by its owner with the PIN (a forgotten PIN on the lock screen, a flashed data partition), it needs a code from that account before it can be used (§11).
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
| Factory reset | System › Reset options, takes the screen lock; recovery takes no credential | General › Transfer or Reset iPhone, takes the passcode; "Erase iPhone" on the lockout screen | More settings, takes the lock-screen PIN (the parental PIN in minor mode); erasing without the PIN on the lock screen after 5 wrong; no recovery (§11) ✓ (developer machine) |
| Protection after an erase | FRP: the previous Google account after an untrusted erase | Activation Lock: the Apple ID, kept by Apple's servers | The anti-theft lock: the Kario account, a record in the persistent area and the account server's binding, a release the server signs (§11.4) ✓ (developer machine, simulated account server) |

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
| Incoming call | The system's answering screen as always; once answered only the call screen is usable: menus, the task switcher and Back cannot leave it; after hanging up the lock screen is back. When the screen goes off and the phone locks during a call, the call screen in front stays rather than being hidden, and lighting the screen shows it (as on Android and iOS), again with only the call screen usable; its right soft key 「最小化」 (Minimize) does nothing while locked. When the call goes on but the lock screen is back (the call was put in the background before the lock, or the call screen was replaced by another app), the lock screen says 「通话键 回到通话」 (Call key: back to the call), and the call key brings the call screen back without unlocking; the same for cellular and VoIP calls (unit tests `TestLockReturnsToCall`, `TestLockDuringACall`, the developer-machine selftest 「锁屏：通话中息屏上锁……」, **not run on a real phone**; a call in front and in the background: [19](19-core-apps.md) §5) |
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
| Storage | phone-keystored's `keystore/lock.json`: a random salt, PBKDF2-SHA256 (100,000 rounds) over an HMAC with a key derived from the device key (the file is no use on another device; the derivation takes the data area's random salt too, a new one after a factory reset, §11.3; in production the TEE's hardware key); plus the "secure user ID", made when the PIN is set and carried by tokens |
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
| `parent.status` / `parent.set_pin` / `parent.clear_pin` / `parent.verify` | The parental PIN (§10): whether one is set, any wait; set or change it (given the one in force), remove it (given it), check it; a right one is only "right", no token |
| `parent.reset_pin` | Replaces the parental PIN without the one in force (the wrong tries forgotten); the Shell calls it only after checking the account server's signed reset (§10) |

## 8. Tests

| Environment | Result |
|------|------|
| Unit tests (`keystore`) | PIN format; the slow hash holds no plaintext; changing needs the old PIN; 30 seconds after 5 wrong, 1 minute after 10; a restart does not shorten a wait; a right PIN resets the count; a tampered token and one from before a restart are refused; enrolling needs a fresh PIN token (an expired one fails); another finger and an enrolled one are refused; at most 5; rename; deleting needs a token; matching: disabled after 5 failures, the PIN brings it back, 48 hours, the PIN after a restart, templates usable across a restart; removing the PIN removes the fingers; cancelling a wait; biometric-bound keys: none before the PIN confirmed, its signature passes `VerifyConfirm`, a token works once, an unlock's token cannot sign, the payment PIN cannot sign with it, it fails as a PIN key, enrolling a new finger voids it and it can be made again, a token expires after 30 seconds ✓ |
| Unit tests (`keystore`, the parental PIN) | Apart from the lock-screen PIN: each its own file with only a slow hash and its own count; the lock-screen PIN as the parental one counts as wrong, the parental PIN does not unlock; changing and removing it take the one in force; 30 seconds after 5 wrong, a restart does not shorten the wait, a right one resets the count; a reset needs no old PIN, the old one stops working, the wait is forgotten, there is nothing to reset with minor mode off, the lock-screen PIN is not touched ✓; the Shell's side (`shell`): setting a parental PIN turns minor mode on, no PIN in the state directory, changes and developer mode take the PIN, entered once with Settings in front it covers the next changes, turning off takes the PIN, phone-keystored's word decides ✓ |
| Unit tests (`hal/fingerprint`) | The enrolled finger matches 200 times out of 200, another finger 0 out of 200; enrolling refuses another finger; waiting for a touch; a touch just before the wait counts, a stale one does not ✓ |
| Developer-machine selftest (`--only '^锁屏'`, text and pixel renderers) | 10 cases ✓: setting the PIN; the fingerprint page asks for the PIN first, enrolling with 5 touches (another finger does not count), turning on fingerprint payments; locking when the screen goes off, the lock screen hiding the calculator, a touch unlocking back to the calculator; the lock screen showing only 「飞钱：1 条通知」; an unknown finger disabled after 5, a wrong PIN explained, a right one unlocking; dialling 112 locked; the screen going off during a call locks with the call screen still in front, Back and the right soft key cannot leave it and hanging up returns to the lock screen, and with the call in the background the call key brings it back without unlocking; Feiqian set up with the payment PIN the first time, then paying by fingerprint and passing the merchant's check; after another finger is enrolled, back to the payment PIN, and once confirmed the new finger pays too; removing the PIN brings back the keypad lock |
| QEMU, real phone | QEMU selftest ✓ (the 9 lock-screen cases); not run on a real phone (no fingerprint driver) |

## 9. Not done

| Item | Notes |
|----|------|
| In the TEE (R15) | PIN checks, templates, matching and the token key belong in an OP-TEE trusted application, the PIN reaching it through the trusted input path; now it is the phone-keystored process |
| Real fingerprint sensors | See §4.1: FPC and Goodix need the vendor's TEE; USB sensors through libfprint in a separate process; all **not implemented** |
| The sensor waking the phone from suspend | The sensor's interrupt as a wakeup source on a phone, **not implemented** |
| IFAA / SOTER | The Alipay and WeChat biometric standards, **not implemented** (they need the vendor's TEE and certification) |
| Face, pattern, password | Only a numeric PIN and fingerprints |
| One PIN for the lock screen and payments | See §3, **not implemented** |

## 10. The parental PIN (minor mode)

Minor mode ([19](19-core-apps.md) §1.2) is guarded by a parental PIN: setting it turns minor mode on and turning it off takes it; so do changes to minor mode and screen time, installing from the store, developer mode, device unlock, the date and time, and giving more time.

| Step | How |
|------|------|
| Typing it | Only on the system's own screen (`parentPadOverlay` in `shell/minor.go`, drawn as the lock-screen PIN's screens are); Settings calls `settings.set minor_mode` / `parent_pin`, or changes a guarded setting, and the answer waits for the user, so Settings never gets the PIN. Setting it asks twice; changing it asks for the one in force first |
| Keeping it | phone-keystored's `keystore/parent.json`, apart from `lock.json`: a random salt, PBKDF2-SHA256 (100,000 rounds) over an HMAC with another key derived from the device key (`kario-parental-pin`). The PIN itself is never written to disk or logs; the Shell holds it only while it is being typed and drops it once sent; neither Settings' files nor the Shell's `minor.json` or `settings.json` hold it |
| Wrong tries | The lock-screen PIN's table (§3): 1–4 say how many are left, 30 seconds after 5, longer the more, the count kept in `parent.json` so a restart does not shorten it; each PIN counts its own |
| What a check gives | A right one is only "right", no token: the Shell itself enforces minor mode; it keeps no credential of a right PIN either, only letting the next changes go without it for 10 minutes while Settings stays in front |
| On or off | A parental PIN in phone-keystored is minor mode on; the Shell starts by what it noted and takes `parent.status`'s answer once it comes |
| And the lock-screen PIN | Two things: the lock-screen PIN is the child's (the lock works as ever), the parental PIN the parent's; the screen advises against using the same one |
| When it is forgotten | With a parent's account linked ([19](19-core-apps.md) §1.2, "A forgotten parental PIN"): the phone asks the account server, the parent confirms on the page after signing in with an e-mail code within 10 minutes, and the server signs a reset for this phone, this request only, good for 10 minutes (`family.Reset`); the Shell checks it, has the new PIN typed twice on the system's own screen, checks once more that the reset has not expired before handing it on, and phone-keystored's `parent.reset_pin` replaces the hash in `parent.json` (a new salt, the wrong tries and the wait cleared); the old one stops working, and a reset is used once. phone-keystored does not check the signature itself: the Shell is its only caller and is what enforces minor mode anyway, so checking in the Shell gives it nothing more. Without a parent's account linked only reflashing helps, which wipes the phone's data with it (a factory reset takes the parental PIN too, §11.1) |

Not done: in the TEE (as R15 in §9); recovering a forgotten parental PIN without a parent's account linked (only reflashing) is **not implemented**; a parent's remote management was implemented on 2026-11-20 ([19](19-core-apps.md) §1.2, "A parent's remote management": remotely the rules can be tightened and loosened, but minor mode cannot be turned off and the PIN cannot be changed); resetting the PIN through the parent's account was implemented in a second round the same day (the table's "When it is forgotten"), **not tried on a phone**, the account server **not deployed**, the parent's page in WeChat's built-in browser **not verified**.

## 11. Factory reset and the anti-theft lock

> Implemented 2026-11-21. Unit tests (`keystore`, `wipe`, `antitheft`, `shell`, and 4 of the account server `kario-accountsrv`) ✓; the developer-machine selftest cases 「恢复出厂设置：……」 (Factory reset) and 「防盗锁：……」 (Anti-theft lock) ✓ (text renderer; the restart is `phone-shell` stopping the apps and the services and starting itself over in place, what is erased is the selftest's state directory; the account server is simulated in the Shell). `go test ./shell`'s parts cannot restart in place, and the two cases are skipped there. The wipe at boot on a device (the initramfs making the data partition anew, `phone-wipe.service`) has only the host's tests of the script (package `wipe` runs yocto's `data-wipe`, with stand-ins for mount and mkfs on the formatting path) and the QEMU-only case 「恢复出厂设置（设备）」 (Factory reset on the device), **not run on QEMU yet**; a QEMU test over several boots (as `make yocto-ota-test`) is **not implemented**; **not tried on a phone**; the account server is **not deployed**.

Here rather than in [19](19-core-apps.md): the factory reset is guarded by the lock-screen PIN (§3) and the parental PIN (§10), the way out of a forgotten PIN starts on the lock screen (§2), and the anti-theft lock and the device keys ([11](11-platform-architecture.md) §7) are one chain of trust; [19](19-core-apps.md) §1.2 says only that minor mode guards it.

### 11.1 Factory reset

| Step | How |
|------|------|
| Where | 「设置 › 更多设置 › 恢复出厂设置」 (Settings › More settings › Factory reset) (Android has it in System › Reset options › Erase all data, Xiaomi MIUI 12 in More settings › Backup and reset, HyperOS moved it to My device); the palette's 「设置 · 恢复出厂设置」. The code is in `apps/settings/reset.go` and `shell/reset.go` |
| What the page says | Erased: every app's data and settings, the apps from the store; the accounts and the passwords the system keeps sealed for them (CardDAV app passwords, the device token of a parent's remote management); contacts, messages, the call log; photos, videos, music and Files; Wi-Fi networks and Bluetooth pairings; the lock-screen PIN and the fingerprints; minor mode and the parental PIN. Kept: the system and the updates installed (both system slots are untouched); the phone's identity from the factory (the device keys and certificates, §11.3). With a Kario account signed in, a switch 「保留防盗锁」 (Keep the anti-theft lock), off by default; last, what will be asked for |
| Confirming | In minor mode the parental PIN, every time (the 10 minutes after entering it in Settings during which further changes need no PIN do not count here); otherwise the lock-screen PIN, if one is set; both typed on the system's own screens. Then the system's own last confirmation 「清除这台手机上的全部数据？」 (Erase everything on this phone?), saying what goes, what stays and what becomes of the anti-theft lock. Keys a computer injects over the developer link cannot answer either ([27](27-usb-typec.md) §4). A phone without a lock-screen PIN has only the last confirmation: whoever holds it unlocked can erase it, and the page says so |
| Erasing | The Shell writes the request in the persistent area (`wipe.Request`: `/var/lib/slots/wipe/request`, a temporary file renamed, fsync'd), then sets the anti-theft lock's record (§11.4), gives the account server at most 5 seconds (ending the anti-theft binding and a parent's remote management, as best it can), shows 「正在清除手机…」 (Erasing the phone…) and restarts. The next boot erases before the services start (table below). Settings: `settings.set factory_reset` (1; 2 keeps the anti-theft lock), `settings.get`'s `reset` |
| After the restart | As a new phone: the idle screen, no account, PIN or contacts (the developer machine and the selftest have the demo contacts), every setting at its default, the product's system apps still there. Kario has no setup wizard, **not implemented** |

Where the erasing happens:

| Environment | How it erases |
|------|----------|
| A/B disk (QEMU's disk image; production follows this layout, [25](25-system-update.md) §1) | The initramfs (`slot-init`, with `phone-wipe`'s `data-wipe`), after opening the system slot and before mounting the data partition, looks for the request on the state partition: it first copies the device's identity (`phone/factory/`, `phone/keystore/device.key`, `device-sm2.key`) to the state partition (`wipe/keep/`, then `kept`), makes the data partition anew with `mkfs.ext4` (mke2fs discards the partition's blocks first, an erase where the storage supports it), mounts it, copies the identity back, writes a new data salt, and removes the request last. A power loss half way starts over from the copied identity at the next boot. If the data partition does not mount, the identity is lost, and the log says so. The initramfs gains e2fsprogs' mke2fs for it (the initramfs's size **not verified**) |
| Boards whose root file system holds the data (OnePlus 6T) | `phone-wipe.service` (`DefaultDependencies=no`, before `sysinit.target` and `systemd-tmpfiles-setup.service`, run only with a request): it keeps the identity the same way, empties `/var/lib/phone`, `/var/lib/iwd` (Wi-Fi networks) and `/var/lib/bluetooth` (pairings), copies the identity back, writes the data salt and removes the request. It is also the fallback when the initramfs could not. Emptying is not erasing: the blocks deleted wait for the storage to reclaim them |
| Developer machine | `phone-shell` calls `wipe.Boot` before starting its services: the persistent area is `persist/` in its base directory (beside `state/` and `run/`), the steps as above; the restart is phone-shell stopping the apps and services and starting itself over in place (`Config.Reboot`), and a selftest goes on from the line it stood at (the `rebooted` step) |

The two implementations (Go's package `wipe`, yocto's `data-wipe` script) take the same steps and keep the same paths; package `wipe`'s tests run both paths of the script against the same checks.

### 11.2 A forgotten lock-screen PIN

Kario has no recovery as Android has ([27](27-usb-typec.md) §4.1); flashing goes through the board's tools. So the way out of a forgotten lock-screen PIN is on the lock screen, after iOS's "Erase iPhone" on its lockout screen (iOS 15.2 on): after 5 wrong PINs in a row (the first wait of 30 seconds), the PIN screen gains a line 「忘了 PIN 码？按 # 清除这台手机和上面的一切」 (Forgot the PIN? # erases this phone and everything on it); # brings the system's own confirmation 「不用 PIN 码清除这台手机？」 (Erase this phone without its PIN?), which says the Kario account signed in will be asked for afterwards. In minor mode the parental PIN comes first (else a child could wipe minor mode away with a few wrong PINs). Such an erase lacks the owner's lock-screen PIN, and the anti-theft lock holds.

| Ways to erase without the lock-screen PIN | State |
|------|------|
| 「忘了 PIN 码」 (Forgot the PIN) on the lock screen | ✓ (above); the anti-theft lock holds |
| A wipe screen entered with keys at boot (Android recovery's Wipe data) | **Not implemented**: it needs reading keys and drawing in the initramfs; when done it counts as an untrusted erase, as the lock screen's does |
| Erasing after too many wrong PINs (iOS's "Erase data" option) | **Not implemented**; now the waits grow, to a day at most (§3) |
| A flashing tool rewriting the data partition (fastboot, Qualcomm EDL) | The erase cannot be stopped; with the state partition not rewritten the anti-theft lock holds (§11.4: the data area lacks the record's token) |

### 11.3 Keys: the factory identity stays, the data area's keys change

phone-keystored's device keys (Ed25519 and SM2) are what the device certificate vouches for: on the factory line the certifier's CA certifies the two public keys ([11](11-platform-architecture.md) §7, [22](22-developer-api.md) §11), and registering with the push gateway, the update server's reports and payments' trusted confirmations rely on that chain to know "this is a genuine, certified phone". New device keys would void the certificate, and only the factory station can issue another: the owner has no way to. So a factory reset **keeps** the device keys, the device certificates and the roots (`keystore/device.key`, `device-sm2.key`, `factory/`); the device ID stays, and `wipe.Keep` lists them. Android does the same: the attestation keys provisioned in Keymaster at the factory survive a factory reset; the keys of the user's data go.

Gone: every app key (`keystore/apps/`, mini-programs' too), the push keys, the payment PIN (back to the default), the lock-screen and parental PINs, the fingerprint templates, the secrets the system sealed; the device-unlock state goes too (the components the owner built and swapped in live in the data area and go with it, and device attestation reports a normal boot again).

Deleting the files is not enough: keys derived from the same device key (the PINs' peppers, the templates' key, the sealed secrets' key) would stay the same, and data copied off before the erase (a sealed app password, say) would still open on this phone. So the derivation also takes a random salt of the data area (`keystore/data.salt`, `keystore.DataSaltPath`): written when a new key store is made, deleted with the data area by the erase, and written anew as the erase's last step; what was sealed before the erase no longer opens and the PINs' hashes no longer match (`TestDataSaltAfterAReset`). Phones from before this version have no salt and derive as before (`TestStoreWithoutADataSalt`) until their first factory reset. In production the salt belongs in the TEE's secure storage, destroyed by the erase (**depends on hardware**); now it is on the data partition, where deleting is not erasing (§11.1).

### 11.4 The anti-theft lock

After Android's FRP (Factory Reset Protection) and the iPhone's Activation Lock: a phone with a Kario account signed in, erased other than by its owner with the PIN, needs that account before it can be used again. The code is in `shell/antitheft.go`, the protocol is package `antitheft` (kario-antitheft/1).

| Step | How |
|------|------|
| When it comes on | With a Kario account signed in in 「设置 › 账号」 (Settings › Accounts) (the product's `PHONE_ACCOUNT_SERVER`): the Shell binds the phone on the account server (`POST /v1/antitheft/bind`: the account's e-mail address and app password, and the device's attestation over the server's one-time challenge), and the server records that this phone (by its device key) is the account's; then the Shell writes the record in the persistent area (`/var/lib/slots/antitheft.json`: the account's ID, its address masked as `l***i@example.com`, the device ID, a random token) and the same token in the data area (`<state dir>/shell/antitheft.json`, written first). Failing, it tries again after a minute, doubling, at most 6 hours. The factory reset page shows it |
| When it goes off | Signing out of the Kario account (with a lock-screen PIN set, that first: else whoever holds the phone unlocked could sign out, then erase it from the lock screen); a factory reset from Settings with the PIN without 「保留防盗锁」 (Keep the anti-theft lock); the server's release while locked. Each deletes the record and tells the server the binding ended (as best it can: offline, the server keeps the binding, which the owner sees and removes on the account's page) |
| When it locks | At start (`startTheft`): a record in the persistent area that says it is locked (an erase from the lock screen, the owner kept the lock), or whose token the data area lacks (the data area was replaced: flashing, an erase the Shell did not ask for). So an erase from the lock screen locks as flashing the data partition does |
| While locked | The system's own screen over everything: 「这台手机是在没有机主 PIN 码的情况下清除的。它原来登录着 Kario 账号 l***i@example.com」 (This phone was erased without its owner's PIN. It was signed in to the Kario account l***i@example.com); the left soft key has the server send a 6-digit code to that address, typed and then OK; OK without a code asks the server whether the owner removed the phone on the account's page; the call key dials emergency numbers only (the lock screen's path, no case of its own), and during a call the end key hangs up. No other key does anything, injected keys neither |
| Release | The server, having checked the code (10 minutes, 5 tries) or found that the owner removed the phone, signs a release `{device ID, account, the phone's nonce for this attempt, issued, expires}` with the account server's signing key over `kario-antitheft-release/1\n` and the JSON (`antitheft.Release`). The release carries the ID of the key that signed it (`key`, the rotation of [11](11-platform-architecture.md) §15.1). The phone takes it only when the signature matches the product's public key (`PHONE_FAMILY_KEY`: the same key signs minor mode's policies, each under its own prefix), the device ID is its own, the account is the record's and the nonce the one it made for this attempt (`antitheft.Verify`; the phone's clock is not asked: after an erase it cannot be trusted, and a nonce used once is enough against replay). Then the record goes, and the phone is as new |

The protocol (package `antitheft`; each request takes a one-time challenge first; the device attestation's app is `system.antitheft`, over `antitheft.Nonce(purpose, account, challenge, the phone's nonce)`):

| Request | Credentials | Answer |
|------|------|------|
| `POST /v1/antitheft/challenge` | — | `{challenge, expires_in}` (5 minutes, once) |
| `POST /v1/antitheft/bind` | Basic: the e-mail address, an app password; the device attestation | `{account, email}`; bound to another account: 409 |
| `POST /v1/antitheft/unbind` | The same | 204 |
| `POST /v1/antitheft/status` | The device attestation (the account's ID, the phone's nonce) | `{bound, email}`; with `release` when the owner removed it |
| `POST /v1/antitheft/code` | The same | 202 `{email}`; 5 an hour per phone and 10 per account, then 429 |
| `POST /v1/antitheft/release` | The same, with the code | The signed release; a wrong code 401 |
| `GET /v1/antitheft/devices`, `DELETE /v1/antitheft/devices/{device ID}` | The account page's session | The phones bound; removing one, which gets a release when it asks next |

The server's side (kario-internal's `kario-accountsrv`) is in internal documents 63 §9 and 64 §3.2.

Threat model:

| Threat | What stops it |
|------|--------|
| A stolen locked phone, erased from the lock screen | It is locked after the erase and needs the code sent to the account's address; the server's binding and the phone's record are both still there |
| A stolen unlocked phone | Erasing from Settings takes the lock-screen PIN, so does signing out of the Kario account; a phone without a lock-screen PIN is not protected (Android's FRP too works only with a screen lock set) |
| Flashing the data partition | The record on the state partition stays and the data area lacks its token, so it locks. The prototype's device keys are on the data partition, so the device ID changes: a code still releases it (the release names the device ID now and the record's account), but the factory certificate is gone too (production's device keys are in the TEE, **depends on hardware**) |
| Flashing the state partition too, or editing the record | Not stopped: without a record nothing locks. It takes a locked bootloader refusing to flash, and in production the record in RPMB or the TEE's secure storage (**depends on hardware**); the server keeps the binding, but asking it at boot whether the phone is still bound is **not implemented** |
| Binding someone else's phone to one's own account, asking a release for another's phone | Every request carries a device attestation by that phone's device key over the purpose, the account and a one-time challenge; the server keeps bindings by the device's public key, so another key claiming the same device ID finds nothing of the phone; the phone takes only a release with its own nonce for this attempt. In production the server has the certifier's root (`--device-root`) and takes only provisioned phones; the trial's phones are not provisioned and prove only that they hold the key |
| A man in the middle (a TLS-terminating reverse proxy included) forging a release | A release carries the account server's signature, and the phone trusts only the public key in the product image |
| Replaying an old release | A release names the phone's nonce for this attempt, and only that one is taken |
| Flooding with codes, guessing codes | 5 an hour per phone and 10 per account; each code holds 10 minutes and 5 tries, for this phone only; codes go only to the account's address |
| A stolen account mailbox | It can release the account's phones and remove them on the page; a stronger sign-in (a phone number, two-step) is **not implemented** |
| The account server's signing key leaking | It can sign a release for any phone; changing the key means a new public key with a system update, a procedure **not implemented**; a key apart from minor mode's is **not implemented** (one key now, under separate prefixes) |
| Deleting the account | The phones bound to it count as removed by the owner and are released when they next ask, never locked for good |

### 11.5 Tests

| Environment | Result |
|------|------|
| Unit tests (`keystore`) | After an erase the device ID, keys and certificate are the same, a secret sealed before does not open, the lock-screen and parental PINs are gone; an old key store without a data salt derives as before and gets none; a broken salt is refused ✓ |
| Unit tests (`wipe`) | The request, the erase, only the identity and a new salt left, modes kept, the request gone, the next boot does nothing; a power loss after the identity was kept (the state directory half deleted) is finished at the next boot; yocto's `data-wipe` on the emptying path (Wi-Fi and Bluetooth emptied, the rest of `/var/lib` untouched, nothing without a request) and on the formatting path (mount and mkfs stand-ins) ends as Go's does ✓ |
| Unit tests (`antitheft`) | Binding takes the account's app password and a device attestation for that login, and another account cannot take the phone; a challenge holds once; a wrong code does not release; a release holds only for this phone, this account and this nonce, and one signed with another key is refused; after the owner's removal no code is needed, and a phone the account never had gets nothing; the simulated server's state file opens again; another key claiming the device ID, another app, another nonce and other roots all fail ✓ |
| Unit tests (`shell`) | From Settings: the lock-screen PIN first (a wrong one goes no further), the last confirmation refuses injected keys, cancelling erases nothing, confirming writes the request, shows the erasing screen and restarts; in minor mode the parental PIN, even with Settings in front and the PIN entered within 10 minutes (this test fails with `parentGate` in its place); without a PIN only the last confirmation; no persistent area, refused; on the lock screen # does nothing after 4 wrong PINs and erases after 5, locking; at the next start the anti-theft screen, checking leaves it locked, a wrong code leaves it locked, the right one releases it with the record and the binding gone; a release signed with another key is refused; the owner's removal on the page releases it at the check; a reset from Settings with the PIN deletes the record and the server ends the binding, and with the lock kept it starts locked; signing out of the Kario account takes the lock-screen PIN, and cancelling keeps the lock ✓ |
| Account server (kario-internal) | 4 tests: binding, release, removal, the root, deletion, the code limit ✓ (`make server-test`) |
| Developer-machine selftest | 「恢复出厂设置：……」 (binding after signing in the Kario account in Settings, the lock-screen PIN and the last confirmation, after the restart the data gone, the device identity the same, not locked, the server's binding gone) and 「防盗锁：……」 (# erases after 5 wrong PINs on the lock screen, locked after the restart, still bound when checking, a wrong code does not release, the right one does) ✓ |
| QEMU | 「恢复出厂设置（设备）」 (the image's `data-wipe` emptying a temporary tree, `phone-wipe.service` before `sysinit.target` and enabled) runs only on the device, **not run yet**; the two cases above run on QEMU with phone-shell's own private services and temporary directory too, **not run yet**; a test over several boots for the initramfs making the data partition anew is **not implemented** |

Not done: a wipe screen at boot (recovery); erasing after too many wrong PINs; asking the server at boot whether the phone is still bound (when there is no record); a button on the account's page to remove a phone (the API exists; the page is in kario-internal's portal, **not implemented**); in production the record and the data salt in RPMB / the TEE (**depends on hardware**); a locked bootloader (**depends on hardware**: Kario does not make the bootloader, [27](27-usb-typec.md) §4.1; anyone can `fastboot oem unlock` a OnePlus 6T, whose root file system is the whole userdata, the persistent area included, so flashing takes the record too); signing in with the device certificate and an account token (the phone binds with an app password now); a setup wizard.
