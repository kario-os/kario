> English translation of [the Chinese original](../32-messaging.md). The Chinese text is authoritative; keep both in sync.

# Messaging: phone-smsd, the hand-off with telephonyd, emergency alerts

> Status: **prototype ✓ (simulated baseband)**. The system service `phone-smsd` (`programs/smsd`: the SMS store, conversations, read and unread, deleting messages and conversations, search, the block list and the blocked folder, delivery states, verification-code detection, the record of emergency alerts), telephonyd's queue of unacknowledged events and `sms.ack`, the simulated network's delivery reports and cell broadcasts (`sim.cb`), the one-time migration of old messages, the system's emergency-alert screen and sound, Settings › Emergency alerts and the Messages app (conversations, delivery, part counter, search, deleting, blocking, copying verification codes, the alert record) are implemented; unit tests ✓, 8 host selftest cases ✓ (§10). The ModemManager side (a received message deleted from the modem only after smsd acknowledged it, delivery reports, ModemManager 1.24's `CellBroadcast` interface) is written and `telephonyd/test/mm_test.py` updated with it, but it was **not run** this time (it needs `make mm-selftest`), and it is **not verified on hardware**. MMS, class 0 (flash) SMS and backups are **not implemented** (§11).

## 0. Summary

- **SMS storage moves out of telephonyd into phone-smsd.** telephonyd only does the radio side: sending, receiving, delivery reports, cell broadcasts; smsd owns everything once a message is stored. This is Android's layering: the RIL / telephony framework receives messages, reassembles long ones and acknowledges them to the network, then hands them to TelephonyProvider (`mmssms.db`) to store.
- **Not one is lost.** Every message telephonyd receives (and every cell broadcast, every outgoing message's outcome) first goes into its own queue of unacknowledged events (on disk); once smsd has stored it and synced it to disk it acknowledges it with `sms.ack`, and only then does telephonyd let go of it; with ModemManager the copy in the modem's storage is deleted only then too. After a restart of either side, what was not acknowledged is handed over again, and smsd recognises what it already stored (§3).
- **No database.** smsd's store is an append-only log over a snapshot: each change is one line, synced; at 256 lines they are compacted into a new snapshot; a last line a crash cut short is dropped when read. Files 0600, directory 0700 (§4).
- **Only the default SMS app deletes and blocks.** As Android lets only the default SMS app write the SMS store, `sms.delete`, `sms.block` and `sms.blocked` are only for the Messages app in the system image; other apps with the `sms` permission can only read and send, and each message they send needs the user's confirmation (§8).
- **Emergency alerts come over everything.** Cell broadcasts are graded by channel (national, extreme, severe, AMBER, test), raise a system screen even over the lock screen, sound in audiod's own `alert` category (silent mode or not) and vibrate; the national level cannot be switched off, the others can in Settings (§7).

## 1. Mapping to Android

| Android | What it does | Kario | Status |
|---------|--------------|-------|--------|
| RIL / telephony framework (`InboundSmsHandler`) | Receives SMS, reassembles long ones, acknowledges them to the network (RP-ACK) once stored | telephonyd: the simulated baseband, or ModemManager (which reassembles and acknowledges); a queue of unacknowledged events it lets go of only once smsd acknowledges | ✓ simulated; ModemManager **tests not run, not verified on hardware** |
| TelephonyProvider (`mmssms.db`: `sms`, `threads`) | Messages and conversations | phone-smsd: log plus snapshot, conversations by normalised number | ✓ |
| Default SMS app (`Telephony.Sms.getDefaultSmsPackage`) | Only it writes the SMS store | The system image's Messages (`com.phone.messages`); cannot be changed | ✓ (no other app can become it) |
| `SmsManager.sendTextMessage` + sent / delivered `PendingIntent` | Sending, the outcome, the delivery report | `sms.send`; `state`: `sending` → `sent` → `delivered` / `failed` | ✓ simulated; ModemManager **tests not run** |
| BlockedNumberProvider | The block list; the default SMS and phone apps read and write it | smsd's block list; a blocked number's messages go to "Blocked", without a notification | ✓ (SMS only; calls are not blocked) |
| CellBroadcastService + CellBroadcastReceiver | Receive cell broadcasts, deduplicate, grade, full-screen alert, alert history, settings | telephonyd receives (`cb` event), smsd deduplicates, grades and stores, the shell raises the system screen, Messages shows the record, Settings has the switches | ✓ simulated; ModemManager **tests not run, not verified on hardware** |
| Class 0 SMS (flash) | Shown at once, not stored by default | Not told apart: stored as an ordinary SMS | **not implemented** |
| MMS | Sent and received over the data connection | — | **not implemented** |
| SMS verification codes (`SmsRetriever`, keyboard suggestions) | Apps read the code by themselves | smsd detects the code; "Copy code" on the notification, also in the conversation; not handed to other apps | ✓ (copy only) |

## 2. Architecture

```
 network ──► modem ──► telephonyd (C++, the radio side)
                          │ unacknowledged queue (telephony.json's pending, on disk;
                          │ with ModemManager: the copy in the modem's storage)
                          │ events sms / cb (with a key)   ▲ sms.ack {keys}
                          ▼                               │
                        phone-smsd (Go, the storage side)─┘
                          │ log + snapshot (state/smsd/)
                          │ events message / changed / alert; unnotified + notified
                          ▼
                        shell: notifications (copy code), emergency-alert screen, broker (sms.*)
                          │
                          ▼
                        Messages (the default SMS app), other apps with the sms permission
```

smsd is telephonyd's second client (the other is the shell); apps reach smsd only through the broker.

## 3. The hand-off: not one message lost

1. telephonyd receives a message, makes the event `{"event":"sms","key":…,"sms":{…}}`, puts it into the unacknowledged queue, writes it to disk (a temporary file, `fsync`, then a rename) and broadcasts it. With ModemManager the id is computed from the number, the SMSC timestamp and the text (FNV-1a), so the same message reported again after ModemManager restarts keeps its id and is queued once.
2. smsd subscribes to telephonyd and, each time it connects, first asks for `sms.pending`: what came while smsd was down and what it had no time to acknowledge.
3. smsd sends `sms.ack` only once it has stored the message (the log synced to disk). The ids it stored are kept in a list of the last 2000; one handed over again is acknowledged, not stored again.
4. telephonyd deletes it from the queue only on the acknowledgement; with ModemManager this is also when it calls `Messaging.Delete` on the copy in the modem's storage (a SIM has a few dozen slots, and a full one takes no new messages).
5. Outgoing messages' outcomes (`sent`, `delivered`, `failed`) and cell broadcasts go through the same acknowledgement; `sending` is smsd's own record and is not queued.
6. The messages telephonyd used to keep itself (`sms` in `telephony.json`) are taken by smsd each time it connects, with `sms.legacy`, stored, and then telephonyd is told to delete them with `sms.legacy_drop`; a crash half way is harmless: the next try skips the ids it has.

| Who restarts | Outcome |
|--------------|---------|
| smsd crashes, a message arrives meanwhile | It stays in telephonyd's queue; when smsd is back it gets it from `sms.pending`, stores and acknowledges it (selftest §10) |
| smsd crashes after storing, before acknowledging | telephonyd hands it over again; smsd recognises the id and only acknowledges |
| telephonyd restarts (simulated baseband) | The queue is read back from `telephony.json`; smsd gets it from `sms.pending` when it reconnects (selftest §10: the test hook `sim.sms` with `held` only queues the message, without an event, as if telephonyd died right after storing it; then telephonyd is killed) |
| telephonyd restarts (ModemManager) | What was not acknowledged is still in the modem's storage, listed again, with the same ids |
| The shell is not there (or not yet reconnected) | smsd stores as usual; new messages and alerts not shown yet stay on smsd's "unnotified" list, which the shell takes with `unnotified` as soon as it subscribes and confirms with `notified` once shown |

## 4. The store

- Directory `<state>/smsd/` (0700): `snapshot.json` (0600) and `log` (0600, one change a line, with its sequence number). Reading takes the snapshot, then replays the newer lines of the log by sequence number; an unfinished last line is cut off.
- Compaction: at 256 lines in the log a new snapshot is written (temporary file, `fsync`, rename, directory sync), then the log is emptied; a crash between the two is harmless: the replay skips by sequence number what the snapshot already holds.
- Limits: 10000 messages (the oldest go), 500 alerts, 2000 received ids remembered, 200 unnotified.
- Conversations go by the normalised number: spaces, dashes, dots and brackets dropped, and a Chinese number's `+86` / `0086` (the contacts store has numbers without the country code); a sender with letters (a company's name) is compared case-insensitively. Other countries' codes are not dropped: **not implemented**.
- Search runs in memory: the text (case-insensitive) and the number; contact names are matched by the Messages app itself (smsd does not know the contacts).

## 5. Sending and delivery reports

- `sms.send` first stores the message as `sending`, then hands it to telephonyd (with smsd's id, which its later events carry); without telephonyd it is recorded as `failed` at once.
- Delivery reports: the simulated network reports `delivered` 0.5 s after sending (numbers ending in 999 fail, numbers ending in 998 are sent but never get a report); ModemManager is asked for a report when the message is created (`delivery-report-request`), and on its `DeliveryState` telephonyd reports `delivered` or `failed` by its class, nothing while the network still retries; at most 64 messages wait for a report, the oldest giving way.
- States only move forward (`sending` < `sent` < `delivered` / `failed`): late or repeated reports do not take one back; sending again (the default SMS app only, `sms.send {id}`) takes it from `failed` back to `sending`.
- A message still `sending` after 3 minutes (telephonyd went away meanwhile; ModemManager gives up after 2) is recorded as `failed` by smsd.
- The part counter (`ui.SMSSegments`, shared by the Messages app and smsd): the GSM 7-bit alphabet takes 160 characters a part, 153 when split; any character outside it (Chinese, emoji) means UCS-2, 70 / 67; smsd does not send more than 10 parts.

## 6. Blocking and verification codes

- The block list holds normalised numbers; a blocked number's messages go straight to the "Blocked" folder, marked read, with no notification and not on the unnotified list. Messages after unblocking are as usual; the earlier ones stay in "Blocked".
- Verification codes: 4–8 digits (or two runs split by one space or dash, as `123 456`) near a word that announces a code, the one nearest the word winning, at most 40 characters away. Amounts (a currency sign before, 元 or decimals after), years, times (a colon), quantities and numbers longer than 8 digits are not codes. The words and marks are Chinese, English and Spanish data in `programs/smsd/otp.json`, not in the code.
- A detected code is stored with the message (`code`): the notification offers "Copy code" (the shell copies it itself, Messages need not start), and in the conversation `c` on that message copies it too; the clipboard stays as before: only the user pastes, in an input field.

## 7. Emergency alerts (cell broadcast)

| Channel (3GPP TS 23.041) | Level | Can be switched off |
|--------------------------|-------|---------------------|
| 4370, 4383 | National (`presidential`) | No |
| 4371–4372, 4384–4385; ETWS 4352–4354, 4356 | Extreme (`extreme`) | Yes, on by default |
| 4373–4378, 4386–4391, 4396–4397 | Severe (`severe`) | Yes, on by default |
| 4379, 4392 | AMBER (`amber`) | Yes, on by default |
| Others (monthly tests, exercises, operator messages, ETWS tests and so on) | Test (`test`) | Yes, off by default |

- telephonyd: the simulated network sends one with the test hook `sim.cb {channel, serial, text}`; ModemManager 1.24 (the Yocto layer's meta-openembedded wrynose has `modemmanager_1.24.2`) has `org.freedesktop.ModemManager1.Modem.CellBroadcast`: telephonyd sets the channels 4352–4359 and 4370–4400 (`SetChannels`), takes `Added` and the `Cbm` objects (`State`, `Text`, `Channel`, `MessageCode`, `Update`) and calls `Delete` once smsd acknowledged. The ModemManager side's **tests were not run, not verified on hardware**; modems differ in their cell broadcast support.
- smsd deduplicates: the same channel and serial number (message code + update number) within 24 hours is one alert; a new serial number (an update) is a new one. Every alert is stored, whether its level is on or not.
- The shell: when the level is on it raises the system screen `alertOverlay`: on top even over the lock screen (it stays when the lock closes dialogs), the end key does not close it, only OK or Back; the screen lights up; audiod's new `alert` category (highest priority, volume 1, sounds in silent mode too) plays the 853 Hz + 960 Hz attention signal (2 s, half a second, half a second, looping until closed); three vibrations. With the level off it is only recorded.
- Settings › Emergency alerts: one line per level, the national level reading "Always shown"; the `settings.set` keys `alert_extreme`, `alert_severe`, `alert_amber`, `alert_test`.
- Messages › Emergency alerts: the alerts received (level, text, time, channel).

## 8. Capabilities and the default SMS app

| Capability | Who may use it |
|------------|----------------|
| `sms.send` | `sms` permission; for a store app the system states every message's recipient and text and sends it only once the user confirms; only the default SMS app may send again with `{id}` |
| `sms.list`, `sms.read`, `sms.threads`, `sms.alerts` | `sms` permission |
| `sms.delete`, `sms.block`, `sms.blocked` | `sms` permission, and only the default SMS app (`com.phone.messages` in the system image) |

Arguments and results are in [22](22-developer-api.md) §4.2; apps without the `sms` permission are refused all of them (the compatibility probe checks each).

## 9. The Messages app

- Conversation list: the contact's name or the number, the last message ("Me:" for sent ones, "Not sent:" for failed ones), the time, the unread count; each conversation's menu has "Delete conversation" (confirmed first) and "Block number" (confirmed first). The right soft key writes a new message; the page menu: Search (`s`), Blocked (`k`), Emergency alerts (`e`).
- Conversation: both sides' bubbles, sent ones marked "Sending…", "Sent", "Delivered", "Not sent"; on a message: Resend (when it was not sent), Copy code (`c`), Copy text (`y`), Delete (`d`, confirmed first); the page menu: Call, Delete conversation, Block / Unblock. Under the input, the part counter ("68 left · 1 SMS").
- New message: recipient, text, part counter, Send; shared text comes filled in.
- Search: text, numbers and contact names, as you type.
- Blocked: the blocked numbers (OK unblocks), "Block a number", the conversations from blocked numbers.
- Emergency alerts: the record and each alert's details.

## 10. Verification

| Test | Result |
|------|--------|
| `go test ./programs/smsd/` (the store: restarts, a torn last line, compaction, permissions, deduplication, blocking, unnotified; verification codes; alert levels; the hand-off with a fake telephonyd: handed over again after a restart, no duplicates, migrating old messages) | ✓ (with `-race`) |
| `go test ./ui -run TestSMSSegments`, the default SMS app, alert levels and copying codes in `go test ./shell` | ✓ |
| telephonyd's own tests (the unacknowledged queue, kept across a restart, handing over the old store, `sim.cb`, delivery reports, `delivery()`, `stable_id()`) | ✓ |
| Selftest: a message while Messages is closed → notification → conversation, reply, part counter, delivered; copying a code; deleting a message and a conversation; blocking; a message that arrives while smsd is down; a message telephonyd stored but crashed before handing to smsd | ✓ (host) |
| Selftest: a national alert over the lock screen (sound, vibration), no alert once extreme is off in Settings, both in Messages | ✓ (host) |
| `make mm-selftest` (the ModemManager side: deleted only once acknowledged, kept across a restart, delivery reports, cell broadcasts) | Updated, **not run** |
| Device selftest (`make yocto-selftest` and the like) | **Not run**; **not verified on hardware** |

## 11. Not implemented

- MMS, group messages, scheduled sending, saved drafts, SMS backup and export.
- Showing class 0 (flash) SMS apart; managing the messages on the SIM (they are only taken in, then deleted).
- Blocking calls (BlockedNumberProvider covers incoming calls too); blocking by keyword, spam detection.
- Normalising other countries' numbers (only `+86` is dropped).
- No other app can become the default SMS app.
- Geo-fenced alerts (4400), choosing among an alert's languages, repeated reminders, reading alerts aloud.
