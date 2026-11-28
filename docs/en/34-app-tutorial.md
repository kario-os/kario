> English translation of [the Chinese original](../34-app-tutorial.md). The Chinese text is authoritative; keep both in sync.

# Tutorial: an app with KarioUI

> A notes app step by step: a list, an input, a second page, actions and confirmation, storage and the tombstone, the clipboard capability, push, background jobs, translations, tests, debugging, packaging and publishing. The finished app is `examples/karioui-notes`; the code here is taken from it, and its tests (`kario test`, 10 cases) pass; push and background jobs also have a selftest case on the developer machine (§7.1, §7.2).
> Status: KarioUI, `kario run`, `kario preview`, `kario test`, `kario publish` and the Kario Studio extension work on the developer machine ([29](29-app-development.md) §5.2), and these tools run only there; KarioUI apps (the calculator, the tutorial's sample) pass the QEMU selftest; **not run on a real phone**. How to design the interface and what the store reviews: [33](33-app-guidelines.md); capabilities and the protocol: [22](22-developer-api.md). To get something running first (apps in C++ or Go and native modules too): [35](35-quickstart.md).

## 0. What you need

| What | Where from |
|--------|--------|
| The `kario` command line and the developer machine's Shell | `make install-kario` in the Kario source tree: `make build` (`build/bin/kario`, `build/usr/bin/phone-shell`, `kario-jsrt`), `make paint`, then a link from `~/.local/bin/kario` to `build/bin/kario` ([35](35-quickstart.md) §0) |
| Node.js 22.15 or newer | For `kario run` (running and debugging under Node), `kario test` and `kario preview`; not for `kario build` and `kario package`. CI tests on Node 22 and 24 |
| The pixel UI preview (optional) | phone-paint (`make paint`, included in `make install-kario`) |
| An editor (optional) | VS Code or Cursor with Kario Studio (`ide/vscode`, §10) |

## 1. A new project

```bash
kario new notes
cd notes
```

| File | What it is |
|------|--------|
| `manifest.json` | The app's ID, name, `"runtime": "js"`, permissions ([22](22-developer-api.md) §1) |
| `src/main.ts` | The entry point: pages written with KarioUI |
| `src/locale/<language>.json` | The catalogs: English source to translation |
| `src/previews.ts` | Previews: pages with sample state, which `kario preview` draws without running the app (§3); `kario build` leaves it out |
| `test/main.test.ts` | Tests, run by `kario test` |
| `tsconfig.json`, `types/` | Types for the editor: `kario` (the system's interfaces), `@kario/ui` (KarioUI), `@kario/test` (the test library); `kario build` uses its own copy, no npm packages to install |

## 2. The first page: state and components

In KarioUI a page is a function that returns the page's view: a title, nodes (`Text`, `Item`, `Input`…), actions. When the state the function reads (`state`) changes, the page is drawn again, and only the pages that changed are sent to the system. The whole file is `examples/karioui-notes/src/main.ts`.

```ts
import { Input, Item, List, Text, act, app, nav, page, state, tr } from "@kario/ui";

interface Note {
  text: string;
  at: number;
}

const notes = state<Note[]>([], { store: "notes" });

const Main = page("main", () => ({
  title: tr("Notes"),
  focus: "new",
  body: [
    Input("new", {
      placeholder: tr("Write a note"),
      clear: true,
      onSubmit: (text) => {
        const t = text.trim();
        if (!t) return;
        notes.update((list) => [...list, { text: t, at: Date.now() }]);
      },
    }),
    List(notes.value, (n, i) =>
      Item(n.text, { id: `n${n.at}`, onOk: act("open", tr("Open"), () => nav.push(NotePage, { index: i })) }),
    ).reverse(),
    Text(notes.value.length ? tr("%d notes", notes.value.length) : tr("No notes yet"), { style: "dim" }),
  ],
}));

app(Main);
```

- Nodes only say content and actions: focus, menus, soft keys, Back, the input method and layout are the system's. Give actionable nodes a stable `id`; the focus goes by it.
- `onOk` is what the OK key does; `act(id, label, what)` is an action whose `id` stays the same and whose label the menu shows.
- `state(initial, { store })` is kept in the app's storage and outlives the process; `{ save }` goes to the tombstone (§6).

## 3. Running it

```bash
kario run notes                               # the developer machine's Shell, the text UI in the terminal
kario run -preview 127.0.0.1:8790 notes       # the pixel UI (phone-paint) in a browser; the page's keyboard is the phone's keys
kario run -preview 127.0.0.1:8790 -watch notes   # builds again on save; the app comes back on its pages, its state kept
kario run -inspect 127.0.0.1:9229 -brk notes  # waits for a debugger first: Chrome's chrome://inspect or VS Code
kario run -engine jsrt notes                  # the device's engine, kario-jsrt, instead
kario run -sku qemu-t9-1g notes               # as a SKU: its screen, keyboard, memory tier (kario skus lists them all)
kario run -preview 127.0.0.1:8790 -profile out/trace.json notes   # records the performance timeline, written when it stops
kario preview notes                           # without running the app: the pages in src/previews.ts, a picture on each form factor (below)
kario run -preview 127.0.0.1:8790 hello-cpp   # an app in C, C++ or Go: exec in the manifest, the program built beside it (35 §3)
```

`kario run` builds first (with a source map), then starts `phone-shell --dev-app`: the app comes after the system's apps (in place of one with the same ID) and opens at start. Under Node the Shell starts it outside the sandbox, so a debugger can reach it; this is for the developer machine only, the device's Shell refuses it ([29](29-app-development.md) §5.2). The app's log (`console.log`) is in the Shell's run directory (on the developer machine `~/.local/state/phone-sim/run/<app ID>.log`), and in the debug console while a debugger is attached. With `-watch`, saving a change restarts the app the way the system ends and reopens it (§6): the state handed to the tombstone and the text in inputs are kept; a change to `manifest.json` needs `kario run` again.

**The model**: `-device` picks only the form factor (`bar-t9`, `bar-qwerty`, `communicator`); `-sku` picks a particular phone (`kario skus` lists every one in `sku/`), and the Shell runs with its form factor, keyboard (a full keyboard's Fn layer), memory tier (the web view's memory limit) and region. The board's hardware (lights, modem, NFC, fingerprint) is not simulated on the developer machine.

**Previews without running the app**: as SwiftUI's previews, pages get sample state, `kario preview` evaluates them under Node (the app does not start) and has the developer machine's Shell and phone-paint draw them. Previews go in `src/previews.ts`, and `main.ts` exports the pages and the state they read. The `kario new` template (the counter) has one (the notes sample does not yet; to add one there, export `Main` and `notes`):

```ts
import { preview } from "@kario/ui";
import { Main, count } from "./main";

preview("new", Main, { state: () => count.set(0) });
preview("tapped", Main, { state: () => count.set(42) });
```

The pictures go to `<project>/.kario/preview/<form factor or SKU>/<preview>.png`; `-device bar-t9,bar-qwerty` and `-sku qemu-t9-1g` pick the models to draw on, `-skin` adds pictures inside the phone body, `-lang` changes the language. A page with parameters: `preview("one", NotePage, { params: { index: 0 }, state: … })`. Previews run one after another and state is not reset between them: each sets all the state its page reads.

**Performance timeline**: with `-profile FILE` the Shell records the app's CPU and memory (every 250 ms), each page it sends (and the Shell's time handling it), its capability calls (request to answer), the latency from a key to the page update and the ANR probes; when `kario run` stops it writes FILE (Chrome Trace Event JSON): drop it into [ui.perfetto.dev](https://ui.perfetto.dev) or `chrome://tracing`. Under Node, `trace.cpuprofiles/` beside it also holds the JavaScript CPU profile, which VS Code or Chrome DevTools open. On the developer machine only; for the device's speed use `-engine jsrt`, real phones come later.

## 4. A second page: the navigation stack

A page can take parameters; `nav.push(page, params)` opens it, the Back key closes it (the system does; the app only hears `onPop`). The parameters are JSON: when the system ended the app in the background, they bring the page back.

```ts
const NotePage = page("note", (p: { index: number }) => {
  const n = notes.value[p.index];
  if (!n) return { title: tr("Note"), body: [Text(tr("This note was deleted"), { style: "dim" })] };
  return {
    title: tr("Note"),
    body: [
      Text(n.text, { style: "big" }),
      Text(tr("%d characters", [...n.text].length), { style: "dim" }),
      Button(tr("Copy"), act("copy", tr("Copy"), () => copy(n.text))),
    ],
  };
});
```

For a page with state of its own, `page`'s function returns the render function: the outer one runs once when the page opens, the inner one on every redraw.

## 5. Actions, soft keys and confirmation

A list item's other actions go into the Options menu, the frequent ones with a shortcut key; delete is marked `danger` with a `confirm`, and the system asks first. The right soft key holds the page's most frequent action.

```ts
Item(n.text, {
  id: `n${n.at}`,
  onOk: act("open", tr("Open"), () => nav.push(NotePage, { index: i })),
  actions: [
    act("copy", tr("Copy"), () => copy(n.text), { key: "c" }),
    act("delete", tr("Delete"), () => notes.update((list) => list.filter((x) => x !== n)), {
      group: "danger",
      confirm: tr("Delete this note?"),
    }),
  ],
}),
```

```ts
right: act("clear", tr("Clear all"), () => notes.set([]), { group: "danger", confirm: tr("Delete all notes?") }),
```

## 6. Storage and the tombstone

| What to keep | How | For how long |
|------------|--------|--------|
| The user's data | `state(initial, { store: "key" })`, or `storage` itself | Outlives the process (in the app's data directory, at most 1 MB) |
| The open pages | The pages and their parameters, handed over by KarioUI | When the system ended the app in the background and the user comes back (the tombstone, [17](17-app-sandbox.md) §5.3) |
| Temporary state of the interface | `state(initial, { save: "key" })` | As above |
| Anything else | `app({ root, save() { … }, onStart(arg, restored) { … } })` | As above, at most 64 KB |

Unsent text in inputs and the focus are restored by the system (as long as the page's ID stays the same).

Storage changes the memory at once and the runtime writes the file in the background, so a save never waits for the disk; a failed write (the disk full) throws nothing, and to know of one await `storage.flush()`: it resolves once what was stored before it is on disk and rejects when the write failed ([22](22-developer-api.md) §9.2).

## 7. Capabilities and permissions

Every capability is `call(name, args)`, returning a Promise; a failure's error is the system's own text, and `cancelled(err)` is true when the user cancelled.

```ts
async function copy(text: string): Promise<void> {
  try {
    await call("clipboard.set", { text });
    say(tr("Copied “%s”", text));
  } catch (err) {
    say(cancelled(err) ? tr("Not copied") : tr("Copy failed: %s", (err as Error).message));
  }
}
```

The clipboard needs no permission. For capabilities with a runtime permission (camera, location…) declare the permission in the manifest with its purpose (1–80 characters, shown in the grant dialog):

```json
{ "permissions": ["camera"], "purposes": { "camera": "Scan a code to add a note" } }
```

`kario manifest check notes` checks the manifest by the installer's and the store's rules (`kario build` checks it too, reporting only the first problem) and lists every problem; Kario Studio's "Edit the Manifest" is a form on the same rules (§10).

### 7.1 Push: notes a server sends

Apps do not stay running: one is frozen 10 seconds after it leaves the screen and ended when memory is short. A server that has something for it sends a push through the push gateway ([21](21-push.md)); the system decides whether to show a notification, wake the app briefly or start it in the background, and hands it the data as a `push` event ([22](22-developer-api.md) §5, §6). This is how the notes app receives notes sent from elsewhere:

1. At every start, get a token with `push.register` and give it to your server (a reinstall voids the old token; the gateway answers 410).
2. The server posts to `/v1/push` with `token`, `title`, `body` and `data` ([21](21-push.md) §2): with a title and a body the system shows a notification without waking the app (the manifest must declare `notifications`); with `data` the system hands that to the app.
3. The app gets `push` in `app({ onEvent })`; the value is JSON: `{id, kind, data, sent, notified, collapse_id}`.

```ts
async function register(): Promise<void> {
  try {
    token.set((await call("push.register")).token);
  } catch (err) {
    console.warn("push.register:", (err as Error).message);
  }
}

// received adds the note a push's data carries ({"note": "..."}); the
// system may have thawed the app or started it in the background for it.
function received(p: PushEvent): void {
  let text: unknown;
  try {
    text = JSON.parse(p.data ?? "").note;
  } catch {
    return;
  }
  if (typeof text !== "string" || !text.trim() || notes.value.some((n) => n.push === p.id)) return;
  const t = text.trim();
  notes.update((list) => [...list, { text: t, at: Date.now(), push: p.id }]);
  if (!p.notified) say(tr("Received “%s”", t));
}

app({
  root: Main,
  onStart: () => void register(),
  onEvent(name, value) {
    if (name === "push") received(JSON.parse(value) as PushEvent);
    else if (name === "job" && value === DIGEST) digest();
  },
});
```

- Be quick: a frozen app is woken for about 3 seconds, one not running is started in the background (its lifecycle is `background`, with no page in front of the user), and it is frozen again when done; an app gets at most 20 wakes an hour, data past that waits until it comes to the foreground. So store what arrives in a `state(…, { store })` at once rather than waiting for the user.
- The same push may arrive twice (the system acknowledges it to the gateway only after handing it over): drop duplicates by `id`.
- `notified` true means the system already showed the push's notification, so the app should not announce it again; while the app is in the foreground the system shows none, `notified` is false and the app says so itself (here a line "Received “…”").
- Encrypt sensitive data end to end: `push.key` gives the public key for the server, the server seals with `push.Seal`, the app opens what arrives with `push.open` ([22](22-developer-api.md) §4.2, [21](21-push.md) §4.1); the sample does not encrypt.
- The token goes to the app's own server with `fetch` ([22](22-developer-api.md) §9.4): the manifest needs `net`, and the privacy label declares `identifiers` (the token identifies this app on this device); URLs are `https://` only, with `http://` on the device's own loopback for development. The sample takes the server's address on its "Push and reminders" page (the Options menu, `p`; `keyboard: "url"`, so the input method starts in English) and on OK POSTs to `<address>/register`:

```ts
async function sendToken(): Promise<void> {
  const base = server.value.trim().replace(/\/+$/, "");
  if (!base) return;
  if (!token.value) {
    say(tr("Not registered for push yet"));
    return;
  }
  try {
    const res = await fetch(base + "/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app: appId, token: token.value }),
    });
    say(res.ok ? tr("Sent the token to %s", base) : tr("The server answered %d", res.status));
  } catch (err) {
    say(tr("Not sent: %s", (err as Error).message));
  }
}
```

  Every failure (no connection, a timeout, a bad certificate) rejects with a `TypeError` whose message starts with `fetch: `; a 4xx or 5xx from the server is no failure, look at `res.ok`. `kario test` does not reach the network: `launch({ fetch: () => ({ status: 200 }) })` gives the answer and `app.fetches` shows what the app sent (the token case of `test/notes.test.ts`).

Try it on the developer machine: in `kario test`, `app.event("push", JSON.stringify({ id, data, sent, notified }))` plays the system (the push cases of `test/notes.test.ts`); in the Shell's selftest scripts, `push <app> <title> | <body> | <data>` plays the server, through the real gateway and phone-pushd ([22](22-developer-api.md) §10). The selftest case "教程的示例（KarioUI 便签）" (the tutorial's sample) installs the app from the store, pushes a note while it is frozen, and sees the system's notification, the app woken and the note there once it is opened:

```bash
make selftest ONLY='教程的示例'
```

### 7.2 Background jobs: a daily reminder

For something to do regularly (syncing, cleaning up, reminding), use a background job: `job.schedule` registers `{id, every_min, charging?}`, and when it is due the system wakes the app or starts it in the background with a `job` event whose value is the job's `id` ([22](22-developer-api.md) §4.7). The interval is 15 minutes at least; each run has a window (from when it is due, half a period, at most 30 minutes), the system runs the jobs whose windows are open in one wake, and they share the hourly wake budget with push; `charging: true` jobs run only on external power, for heavy work. Registering the same `id` again replaces the job, and it survives a restart; `job.cancel` cancels it. The notes app's "Daily reminder" registers a daily job when it is turned on and posts one notification when it runs:

```ts
async function setReminder(on: boolean): Promise<void> {
  try {
    if (on) await call("job.schedule", { id: DIGEST, every_min: DAY_MIN });
    else await call("job.cancel", { id: DIGEST });
    reminder.set(on);
  } catch (err) {
    say(tr("Reminder not changed: %s", (err as Error).message));
  }
}

// digest is the background job's work: the app has a few seconds before it
// is frozen again, enough for one notification.
function digest(): void {
  const last = notes.value.at(-1);
  if (!reminder.value || !last) return;
  notify({ id: DIGEST, title: tr("%d notes", notes.value.length), body: last.text });
}
```

```ts
Item(tr("Daily reminder"), {
  id: "reminder",
  trailing: reminder.value ? tr("On") : tr("Off"),
  onOk: act("reminder", reminder.value ? tr("Turn off") : tr("Turn on"), () => setReminder(!reminder.value)),
}),
```

- A job has a few seconds (as a push's wake does): do one small thing and store the result; there is no location in the background (it is for the foreground only).
- Keep the switch in storage (`state(false, { store: "reminder" })`) so that it matches the job the system holds: turning it off calls `job.cancel`, and the event checks the switch first. When the system refuses (an interval too short, no `id`), the error is the system's text and the switch stays as it was.
- Notifications need `notifications` in the manifest; a notification with the same `id` replaces the earlier one, so there is one a day.
- Sync regularly with background jobs, receive messages through push, and do not stay alive with the `voip` background mode ([33](33-app-guidelines.md) §1.3, §2.2).

Tests: in `kario test`, `app.event("job", "digest")` plays the job falling due, and `app.notifications` holds what the app posted:

```ts
await app.choose("Push and reminders");
await app.key("enter");
expectEqual(app.calls.at(-1), { cap: "job.schedule", args: { id: "digest", every_min: 1440 } }, "the call");
await app.background();
await app.event("job", "digest");
expectEqual(app.notifications, [{ id: "digest", title: "1 notes", body: "Milk" }], "the notifications");
```

The selftest case above then moves the Shell's clock a day ahead (`timeshift 88200`): the job ran once in the background (`job com.example.kariouinotes digest runs 1`) and the system showed the app's "1 notes" notification (in Chinese, as the selftest runs); finally it turns the reminder off (`job.cancel`) and uninstalls the app through the store.

## 8. Translations

Interface text is written as English source in `tr()`, translations in `src/locale/<language>.json` (Simplified Chinese is required; what Traditional Chinese and Spanish lack falls back); translate whole sentences, with the same `%s` and `%d` in the same order in every language.

```json
{
  "%d notes": "%d 条便签",
  "Added “%s”": "已添加「%s」",
  "Notes": "便签"
}
```

In the Kario source tree, `python3 scripts/i18n-check.py` checks that every `tr("…")` has Simplified Chinese and every translation is still used.

## 9. Tests: kario test

`kario test` builds the app and runs `test/*.test.ts` in Node against a fake Shell: the app sends and receives over the protocol as on the phone (the Node version of the same `kario` module), and the test plays the user pressing keys and reading the screen.

```ts
import { expectEqual, launch, test } from "@kario/test";

test("OK opens a note; the clipboard is a capability; Back returns", async () => {
  const app = await launch({ capabilities: { "clipboard.set": () => ({}) } });
  await app.type("Milk");
  await app.key("enter");
  await app.key("down");
  await app.key("enter");
  app.at("note");
  app.expect("4 characters");
  await app.key("enter");
  app.expect("Copied “Milk”");
  expectEqual(app.calls.at(-1), { cap: "clipboard.set", args: { text: "Milk" } }, "the last call");
  await app.key("esc");
  app.at("main");
});
```

```bash
kario test notes                 # all of them
kario test -run tombstone notes  # those whose name matches
kario test -lang es notes        # the app starts in Spanish
kario test -inspect notes        # waits for a debugger
```

Cases may be split over several files in `test/`: each file runs in its own Node process, several files at once, reported in their names' order. Every `launch` in a file is a new app with a new fake Shell; nothing reaches it from another file, neither the apps it launched, the globals it set, nor its failing cases; when a file's cases are done, the apps it launched end. Under `-inspect` one process runs every file, one after another, so that the debugger attaches once.

| `@kario/test` | What it does |
|---------------|--------|
| `test(name, fn)` | One case (node:test) |
| `phoneZone(zone)` | The phone's time zone (an IANA name such as `Asia/Shanghai`): from there on the file's code and its cases see `Date`'s local time in it, and an app starts in it (as the Shell starts apps, [22](22-developer-api.md) §2); each case starts in its own file's zone; without it, UTC, whatever the developer machine's zone |
| `launch({ lang, arg, restored, storage, capabilities })` | Starts the app under the fake Shell; `capabilities` answers capabilities by name, throwing fails them (a message of `cancelled` is the user cancelling) |
| `app.key(key)`, `app.type(text)`, `app.fill(text)` | Keys: `enter`, `esc`, `f2` (the right soft key), the arrows, `backspace`, one character (typed into an input, else a shortcut) |
| `app.press(id or text, action?)`, `app.focusOn(…)`, `app.menu()`, `app.choose(label)` | OK on a node; move the focus; what the Options menu has; pick an entry (confirming) |
| `app.expect(text)`, `app.expectGone(…)`, `app.at(screen ID)`, `app.wait(text, ms)` | Assert on the screen; wait for a timer or a capability's answer |
| `app.screen`, `app.stack`, `app.focus`, `app.text()`, `app.toasts`, `app.notifications`, `app.calls`, `app.confirmed`, `app.storage`, `app.log` | What the app sent |
| `app.back()`, `app.background()`, `app.kill()`, `app.relaunch()`, `app.resync()`, `app.event(name, value)` | Back; leave the foreground (returns the state handed over); the system ends it; restart after a tombstone; a new Shell takes over; a system event |

The fake Shell's focus and keys simplify the real Shell's ([22](22-developer-api.md) §3): what holds on a phone is decided by the Shell's selftest ([22](22-developer-api.md) §10). The system app Calculator (`apps/calc`) has both: `kario test` cases, and its selftest in `shell/selftest.script`.

## 10. In VS Code / Cursor

Build `kario-studio-0.1.0.vsix` with `npm ci` and `npm run vsix` in `ide/vscode`, then `code --install-extension …` or `cursor --install-extension …` (in a Cursor remote session over SSH, `cursor` in Cursor's terminal installs it on the remote machine; or "Install from VSIX…" in the Extensions view). After `make install-kario` the setting `kario.cli` can stay the default `kario`; otherwise point it at `build/bin/kario` ([35](35-quickstart.md) §2):

| Command (`Kario: …`) | What it does |
|--------------------|--------|
| New App | `kario new`, then opens the folder |
| Build | Builds; errors go to the Problems view |
| Run in the Terminal / Run in the Preview Panel | The text UI in a terminal; the pixel UI in a panel beside the editor, whose keyboard is the phone's keys, reloaded on save (setting `kario.reloadOnSave`) |
| Debug (Node.js) | The preview panel plus debugging: VS Code's Node.js debugger attaches to the app, breakpoints in `src/*.ts` |
| Debug on the Phone (Developer Mode) | On a phone in developer mode over a trusted cable: builds, installs and restarts the app under kario-jsrt's debugger, breakpoints again in `src/*.ts` (the debug type `kario`, [29](29-app-development.md) §5.5) |
| Run Tests | `kario test` |
| Choose the Device | Pick a SKU (`kario skus`) or only a form factor, shown in the status bar; runs and previews use it |
| Edit the Manifest | A form over `manifest.json`: permissions and purposes, the privacy label, name, version and the like; each change is written to the file, and `kario manifest check -json`'s problems show beside the fields |
| Preview the Views / … on Every Form Factor | `kario preview`: the pages in `src/previews.ts` drawn without running the app, shown in a panel, drawn again on save |
| Run with the Performance Timeline | The preview panel with `-profile`; when it stops, open Perfetto, the timeline file or the CPU profile |
| Package / Publish to the Store | Sign and package; hand it to the store's review |
| Add a Debug Configuration | Adds configurations to `.vscode/launch.json` and `tasks.json`, for F5 from then on |

## 11. Packaging and publishing

```bash
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # the developer's key and the store's certificate (phone-pkg keygen / register)
kario package -devstore ~/devstore -o notes.phoneapp notes          # or signed by a development store, which phones in developer mode trust
kario publish -store https://store.example.com notes.phoneapp      # POST /v1/submissions: new apps and new permissions go to human review
kario publish -store https://store.example.com -rollout 10 notes.phoneapp   # an update to 10% of devices first
```

What the store reviews: [33](33-app-guidelines.md) §2. The notes app has no network, so the store's Details page says "No data leaves the phone" by itself; an app with network access adds a privacy label, `privacy`, to its manifest (33 §3), saying which data leaves the phone and what for (the official store refuses it without one), and `kario build` checks it against the permissions first, or says what to add when there is none.

## 12. KarioUI reference

| Name | What it is |
|------|--------|
| `Text(text, { style, id, actions })`, `Section(text)` | A paragraph (`style`: `dim`, `accent`, `title`, `ok`, `warn`, `big`, `display`); a group heading |
| `Item(title, { id, subtitle, trailing, badge, icon, src, style, onOk, actions })`, `Tile(…)` | A list item; a cell of the grid (the page's `columns`) |
| `Input(id, { placeholder, value, secret, keyboard, clear, onSubmit, onChange })` | An input; a `state` as `value` follows what the user types; `onChange` searches as you type |
| `DateInput(id, { value, … })`, `TimeInput(id, { value, … })` | The system's date and time fields (`keyboard: "date"` / `"time"`): the value is `YYYY-MM-DD` or `HH:MM`, the user sees the region's order with the weekday and their own 12- or 24-hour clock, ← → pick the year, month or day or the hour or minute, ↑ ↓ step it, digits type it ([22](22-developer-api.md) §3); `kario test`'s fake Shell does not model these keys: `app.fill("2026-11-20")` sets the value |
| `Button(text, onOk)`, `Progress(fraction, { text, trailing })`, `Image(src, …)`, `Doc(markdown)`, `Media(stream)`, `Web(src, { id })`, `Drawing(canvas, { id, onKey })` | Button, progress bar, image, document, video stream, web page, canvas |
| `List(array, (item, index) => node)` | A group of nodes; a page's `body` takes arrays and `false` / `""` (`cond && Text(…)`) |
| `act(id, label, what, { key, group, confirm, hidden, primary })` | An action |
| `page(id, setup, { onPop, transient })` | A page: `setup(params)` returns the view (or a render function); a view has `title`, `body`, `actions`, `right`, `center`, `focus`, `bottom`, `columns`, `layout` |
| `nav.push`, `nav.replace`, `nav.reset`, `nav.pop`, `nav.depth`, `nav.top` | The page stack |
| `state(initial, { store, save })`: `value`, `set`, `update` | State; `update(f)` may change it in place and still redraws |
| `refresh()` | Redraws when something outside a state changed (a variable a timer set) |
| `preview(name, page, { params, state })` | A page `kario preview` draws: `state` sets the sample state, `params` are the page's parameters (in `src/previews.ts`) |
| `app(rootPage)` or `app({ root, onStart, onOpen, onEvent, onForeground, onBackground, onNotif, save })` | Runs the app |
| `tr`, `call`, `cancelled`, `toast`, `notify`, `storage`, `lang`, `appId` | The interfaces of the same names in the `kario` module ([22](22-developer-api.md) §9.2) |

## 13. Two engines

On the developer machine `kario run` and `kario test` use Node.js (V8); on the device it is QuickJS-ng (`kario-jsrt`). The `kario` module behaves the same on both (the runtime tests of `jsrt/` run on both engines); the differences are yours to mind:

- Only standard ECMAScript and `kario`: under Node globals such as `process` and `Buffer` can be reached, on the device they are not there; the type definitions leave them out and `tsc` reports them. Under Node too, `main.js` can import only `kario`.
- Node is not a sandbox: on the developer machine an app is not held to anything but its permissions (files, network); on the device it is ([17](17-app-sandbox.md)).
- Speed: QuickJS is an interpreter, much slower than V8; heavy computation goes to the system services, measured on a phone.
