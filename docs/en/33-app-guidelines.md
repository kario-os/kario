> English translation of [the Chinese original](../33-app-guidelines.md). The Chinese text is authoritative; keep both in sync.

# App guidelines: the interface, review and privacy labels

> For those who write Kario apps: how to design the interface (gathered from [10](10-keyboard-interaction-model.md) and [26](26-design-system.md)), what the store reviews (the implemented checks of [07](07-ecosystem-and-store.md) §4.1 and [22](22-developer-api.md) §12, plus the standards of the human review), how privacy is shown.
> Status: the interface guidelines and the review guidelines are written conventions; what the system enforces is implemented and marked so; screenshots of what to do and what not to **not done**; privacy labels implemented (§3: the manifest, the checks, the store app's Details page, the staff desk), not shown on the web catalogue yet (**not implemented**); these documents are not on the developer portal yet (**not implemented**). The steps of writing an app are in [34](34-app-tutorial.md).

## 0. Conclusion first

- An app only says "what content there is and what can be done"; focus, menus, soft keys, Back, the input method, confirmations, permission dialogs, colours and font sizes are the system's. So the guidelines are shorter than iOS's: the rules the system enforces (10 §3) need not be remembered and cannot be broken; what is written here is what the system cannot enforce and review looks at.
- One page, one thing; OK does the main action, everything else goes into Options, the right soft key holds the page's most frequent action; every action can be found in the menu.
- Review first runs the same automatic checks as installing on the device; new apps and updates that ask for new permissions then go to a human review, which looks at whether the purposes are true, the permissions the fewest, the keys enough, and whether the app impersonates the system.

## 1. Interface guidelines

### 1.1 What the system does for the app

| Aspect | What the system does | What the app should do |
|------|------------|------------|
| Focus and the arrow keys | Moves in reading order, in two dimensions in a grid (`columns`), the focus always visible | Give actionable nodes a stable `id` (the focus goes by id, so it does not jump when a list changes) |
| Options menu, command palette | The left soft key, Options, lists the focused node's actions and the page's, with their keys; `/` searches all actions | Write clear labels for the actions, give the frequent ones a `key` |
| Back | The Back key goes up one level, keeping the scroll position and unsent input | Stack pages with push; do not make a "Back" button of your own |
| Confirmation | With `group: "danger"` or `confirm` the system's confirmation dialog comes first | Mark deleting, clearing, paying with them |
| Input method | The T9 pad, the full keyboard, pinyin, strokes, the symbol table are the system's; so is paste | Give inputs a `placeholder`, choose the `keyboard` by content (`number`, `phone`, `email`) |
| Permissions | A system dialog at first use, showing the manifest's purpose | Write true purposes (§2.2); degrade gracefully when refused |
| Appearance | Four themes, ten font sizes, large text, the pixel UI and the text UI | Use only the nodes' `style` (`dim`, `accent`, `title`, `ok`, `warn`, `big`, `display`); write no colours, do not align with spaces |
| Coming back after the system ended the app | Shows the earlier pages first; unsent text in inputs and the focus come back | Hand over, on leaving, the state needed to rebuild the pages (KarioUI: `state(..., { save })` and page parameters, 34 §6) |

### 1.2 How to lay out a page

- The title says where this is, in one line (about 20 Chinese characters per line in portrait, 10 §6). No "System" in the title: the system puts the app's name in front (22 §8).
- The main action goes to OK (`primary`), one per node; secondary actions go into Options. The right soft key (`right`) holds the page's most frequent action: new, send, delete all.
- OK on a list item opens its details; delete, share, copy are its actions in the menu, not button nodes after every item.
- One input with its `submit`, OK submits it; no "OK" button under the input.
- Long content goes in `doc` (Markdown, links go into the menu by themselves); long lists get a search (a `live` input) or pages: at most 1000 nodes a page, 32 pages a stack (22 §8).
- Long waits show a `progress` or a line of `dim` text; the loop that handles actions must answer `ping` at all times, the system says "Not responding" after 5 seconds.
- On T9 devices the digit keys are shortcuts outside inputs: do not rely on letter keys alone for a feature. Hidden actions (`hidden`) are only for when the screen already shows a matching button (the calculator's digits).
- Translate whole sentences: `tr("Added “%s”", name)` goes into the catalogs as one sentence; do not stitch words together; the order of dates and numbers differs between languages (22 §9.1).
- The canvas (`canvas`) is for games, maps and charts only; text to be read goes in ordinary nodes, none inside the canvas (22 §3).

### 1.3 Do this, not that

| Do this | Not that |
|--------|------------|
| Every feature can be found in the Options menu | A feature only a shortcut key reaches |
| OK opens a list item, delete is in the menu marked `danger` | A "Delete" button node after every item |
| An input with a `submit` | Another "OK" button under the input |
| States as text in the `ok` and `warn` styles | Emoji, coloured characters, ASCII boxes for states |
| Payments and signatures through the system's trusted confirmation (`auth.confirm`) | A PIN pad of your own, a "System" dialog of your own |
| Ask for the camera when it is first needed, offer a way without it when refused | Ask for every permission at start |
| Receive messages in the background through push and background jobs (22 §6) | Stay alive with the `voip` background mode |

## 2. Review guidelines

### 2.1 Automatic checks

The store runs these checks on every package it receives, and the device runs them again when installing (the same code); a package that fails is refused:

| Check | Where it is implemented |
|------|------------|
| The signature and the certificate chain lead to this store's root; the files and the signed list match one to one | `pkg.Open`, `appstore` |
| The developer is verified; the app ID is a reverse domain name, does not start with `system.`, was never taken down; an update comes from the same developer with a higher version | `appstore`, `shell/install.go` |
| No `system`, no `install`, no `granted`, no unknown permissions | `appstore`, `shell/install.go`, `kario build` |
| Every runtime permission has a purpose (1–80 characters), purposes only for declared permissions | `appstore`, `kario build` |
| `locales` has only the four languages, the same length limits as the manifest, no translation of settings, permissions, commands, intents or notification actions the manifest lacks | `i18n.CheckAppText` |
| TypeScript apps: no `exec`, no executable file, no bytecode, `main` a `.js` file at the package root | `jsrt.Check` |
| Contactless cards: the `nfc` permission, valid card IDs, categories and AIDs | `ui.CheckNFCCards` |
| Privacy label: only the defined kinds of data and uses; no data sent without the `net` permission; with `net`, the data of every runtime permission accounted for (§3) | `ui.CheckPrivacy` (`appstore`, `shell/install.go`, `kario build`) |
| Public stores in China: a filing number in the right format | The official store backend (not open source) |

`kario build` already runs the manifest part of these on the developer's machine: a package that passes `kario build` is not refused for these.

### 2.2 What the human review looks at

New apps and updates that ask for new permissions go to the human review queue (07 §4.1); the reviewers go by this table:

| What | What passing looks like |
|--------|------------|
| The purposes are true | What a purpose says is really in the app, and only then: the camera "to scan and pay" is asked for on the scanning page only |
| The fewest permissions | Every declared permission is used; where a system screen such as `media.pick` or `scan.qr` is enough, no whole `photos` or `camera` |
| Enough with keys | Everything can be done with the arrow keys, OK, the soft keys and the menu; canvas apps get their keyboard reach reviewed (07 §0) |
| No impersonation of the system | No imitation of the system's dialogs, PIN entry or payment confirmation; payments go through `auth.confirm` |
| Background by the rules | Messages through push, periodic sync through background jobs; `voip` only for apps with real calls |
| No charges, no harassment | The system has the user confirm every call and SMS (22 §4.2); review also looks at whether the app pushes the user to confirm |
| Lawful content | The laws of the regions it is listed in; a filing number in public stores in China |
| Matches the privacy label | The data declared as sent matches the permissions, the purposes and the features: an app with accounts lists `account`, one with an analytics or ad library `usage` and `identifiers`; the staff desk's review queue has a "Privacy label" column (§3) |
| Native code | An app in TypeScript only gets its JS and manifest reviewed; an app with native modules ([29](29-app-development.md) P5, **not implemented**) is reviewed as a native app |

A reviewer who sends an app back writes the reason, which the developer sees on the portal; a listed app can be reported, and staff can take it down after handling the report (07 §4.1).

## 3. Privacy labels

What the store shows:

| What | Where it comes from | Where it is seen |
|----------|----------|------------|
| Permissions | The manifest's `permissions` | The system's install confirmation; the store app's Details page (in the phone's language); `permissions` in the catalogue |
| Each runtime permission's purpose | The manifest's `purposes`, other languages in `locales` | The system's grant dialog; the Details page (after the permission); `purposes` in the catalogue (by language) |
| Privacy label | The manifest's `privacy` | "Privacy" on the Details page; the staff desk's review queue; `privacy` in the catalogue |
| Developer, unified social credit code, filing number | The developer's registration, the manifest's `filing` | The Details page; the catalogue |

A privacy label is the developer's declaration of "which data leaves the phone, what for, whether it is linked to the user, whether third parties get it", like the App Store's "App Privacy" and Google Play's "Data safety". In the manifest (a weather app):

```json
"permissions": ["net", "location", "contacts"],
"purposes": { "location": "Show the weather where you are", "contacts": "Send the forecast to a friend" },
"privacy": {
  "collects": [
    { "data": "location", "use": ["app"] },
    { "data": "usage", "use": ["analytics"], "shared": true }
  ],
  "local": ["contacts"]
}
```

| Field | Meaning |
|------|------|
| `collects` | The data that leaves the phone, one entry per kind: `data` is the kind; `use` what for, at least one of `app` (app features), `analytics`, `ads` (advertising); `linked`: tied to who the user is (an account, a phone number); `shared`: given to third parties (an analytics or advertising company) |
| `local` | Kinds the app can read but uses on the phone only, never sending them |

The kinds (`ui/privacy.go`): `account` name, phone number and email, `contacts`, `location`, `photos` photos and videos, `audio` recordings, `messages` the content of communications, `payment` payment and card details, `identifiers`, `usage` usage data, `diagnostics`. The Details page lists them in this order, in wording of the system's in the four languages; developers fill in kinds only.

The system checks what it can (`ui.CheckPrivacy`, the same code in `kario build`, on installing and in the store backend):

- Only the kinds and uses above, each kind once.
- An app without the `net` permission cannot reach the network, so `collects` must be empty; the Details page says "No data leaves the phone: the app has no network access (checked by the system)" whatever the developer wrote.
- With `net`, the data of every runtime permission must be listed in `collects` or `local`: `camera` and `photos` are photos and videos, `microphone` recordings, `contacts` contacts, `location` location, `nfc.reader` payment and card details. A missing one is refused.
- An update of a listed app that sends a kind of data more goes to the human review like one asking for a new permission ("collects new data: …"). During a staged rollout, phones still offered the old version see the old version's label.

An app without `privacy` can still be listed for now: the Details page says "The developer has not provided privacy details", and `kario build` says so for apps with `net`; making it mandatory is a store rule, **not implemented**. Data an app gets without a permission, such as `usage` and `identifiers`, the system cannot check: the developer answers for the label, reviewers check it as in §2.2, and a label that does not match the app is a violation. The web catalogue (the official store's `/apps`) does not show labels yet (**not implemented**).

## 4. Not done

| Item | Notes |
|------|------|
| Screenshots of what to do and what not to | Planned from `make pixel-selftest`, one per row of §1.3 |
| Mandatory privacy labels, labels on the web catalogue | For now an app without one can be listed; the web catalogue has the name, the developer and the filing number only (§3) |
| The documents on the developer portal | The portal (not open source) has registration, submission and review status only |
