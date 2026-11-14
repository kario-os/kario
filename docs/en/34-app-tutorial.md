> English translation of [the Chinese original](../34-app-tutorial.md). The Chinese text is authoritative; keep both in sync.

# Tutorial: an app with KarioUI

> A notes app step by step: a list, an input, a second page, actions and confirmation, storage and the tombstone, the clipboard capability, translations, tests, debugging, packaging and publishing. The finished app is `examples/karioui-notes`; the code here is taken from it, and its tests (`kario test`) pass.
> Status: KarioUI, `kario run`, `kario test`, `kario publish` and the Kario Studio extension work on the developer machine ([29](29-app-development.md) §5.2); **not run** on QEMU or a real phone. How to design the interface and what the store reviews: [33](33-app-guidelines.md); capabilities and the protocol: [22](22-developer-api.md).

## 0. What you need

| What | Where from |
|--------|--------|
| The `kario` command line and the developer machine's Shell | `make build` in the Kario source tree: `build/bin/kario`, `build/usr/bin/phone-shell`, `kario-jsrt`; put `build/bin` on your `PATH` |
| Node.js 22.15 or newer | For `kario run` (running and debugging under Node) and `kario test`; not for `kario build` and `kario package` |
| The pixel UI preview (optional) | `make paint` (phone-paint) |
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
```

`kario run` builds first (with a source map), then starts `phone-shell --dev-app`: the app comes after the system's apps (in place of one with the same ID) and opens at start. Under Node the Shell starts it outside the sandbox, so a debugger can reach it; this is for the developer machine only, the device's Shell refuses it ([29](29-app-development.md) §5.2). The app's log (`console.log`) is in the Shell's run directory (on the developer machine `~/.local/state/phone-sim/run/<app ID>.log`), and in the debug console while a debugger is attached. With `-watch`, saving a change restarts the app the way the system ends and reopens it (§6): the state handed to the tombstone and the text in inputs are kept; a change to `manifest.json` needs `kario run` again.

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
  expectEqual(app.calls, [{ cap: "clipboard.set", args: { text: "Milk" } }], "the calls");
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

| `@kario/test` | What it does |
|---------------|--------|
| `test(name, fn)` | One case (node:test) |
| `launch({ lang, arg, restored, storage, capabilities })` | Starts the app under the fake Shell; `capabilities` answers capabilities by name, throwing fails them (a message of `cancelled` is the user cancelling) |
| `app.key(key)`, `app.type(text)`, `app.fill(text)` | Keys: `enter`, `esc`, `f2` (the right soft key), the arrows, `backspace`, one character (typed into an input, else a shortcut) |
| `app.press(id or text, action?)`, `app.focusOn(…)`, `app.menu()`, `app.choose(label)` | OK on a node; move the focus; what the Options menu has; pick an entry (confirming) |
| `app.expect(text)`, `app.expectGone(…)`, `app.at(screen ID)`, `app.wait(text, ms)` | Assert on the screen; wait for a timer or a capability's answer |
| `app.screen`, `app.stack`, `app.focus`, `app.text()`, `app.toasts`, `app.calls`, `app.confirmed`, `app.storage`, `app.log` | What the app sent |
| `app.back()`, `app.background()`, `app.kill()`, `app.relaunch()`, `app.resync()`, `app.event(name, value)` | Back; leave the foreground (returns the state handed over); the system ends it; restart after a tombstone; a new Shell takes over; a system event |

The fake Shell's focus and keys simplify the real Shell's ([22](22-developer-api.md) §3): what holds on a phone is decided by the Shell's selftest ([22](22-developer-api.md) §10). The system app Calculator (`apps/calc`) has both: `kario test` cases, and its selftest in `shell/selftest.script`.

## 10. In VS Code / Cursor

Install `kario-studio-0.1.0.vsix` built from `ide/vscode` (`code --install-extension …` or `cursor --install-extension …`) and point `kario.cli` at `kario`:

| Command (`Kario: …`) | What it does |
|--------------------|--------|
| New App | `kario new`, then opens the folder |
| Build | Builds; errors go to the Problems view |
| Run in the Terminal / Run in the Preview Panel | The text UI in a terminal; the pixel UI in a panel beside the editor, whose keyboard is the phone's keys, reloaded on save (setting `kario.reloadOnSave`) |
| Debug (Node.js) | The preview panel plus debugging: VS Code's Node.js debugger attaches to the app, breakpoints in `src/*.ts` |
| Run Tests | `kario test` |
| Package / Publish to the Store | Sign and package; hand it to the store's review |
| Add a Debug Configuration | Adds configurations to `.vscode/launch.json` and `tasks.json`, for F5 from then on |

## 11. Packaging and publishing

```bash
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # the developer's key and the store's certificate (phone-pkg keygen / register)
kario package -devstore ~/devstore -o notes.phoneapp notes          # or signed by a development store, which phones in developer mode trust
kario publish -store https://store.example.com notes.phoneapp      # POST /v1/submissions: new apps and new permissions go to human review
kario publish -store https://store.example.com -rollout 10 notes.phoneapp   # an update to 10% of devices first
```

What the store reviews: [33](33-app-guidelines.md) §2. The notes app has no network, so the store's Details page says "No data leaves the phone" by itself; an app with network access adds a privacy label, `privacy`, to its manifest (33 §3), saying which data leaves the phone and what for, and `kario build` checks it against the permissions first.

## 12. KarioUI reference

| Name | What it is |
|------|--------|
| `Text(text, { style, id, actions })`, `Section(text)` | A paragraph (`style`: `dim`, `accent`, `title`, `ok`, `warn`, `big`, `display`); a group heading |
| `Item(title, { id, subtitle, trailing, badge, icon, src, style, onOk, actions })`, `Tile(…)` | A list item; a cell of the grid (the page's `columns`) |
| `Input(id, { placeholder, value, secret, keyboard, clear, onSubmit, onChange })` | An input; a `state` as `value` follows what the user types; `onChange` searches as you type |
| `Button(text, onOk)`, `Progress(fraction, { text, trailing })`, `Image(src, …)`, `Doc(markdown)`, `Media(stream)`, `Web(src, { id })`, `Drawing(canvas, { id, onKey })` | Button, progress bar, image, document, video stream, web page, canvas |
| `List(array, (item, index) => node)` | A group of nodes; a page's `body` takes arrays and `false` / `""` (`cond && Text(…)`) |
| `act(id, label, what, { key, group, confirm, hidden, primary })` | An action |
| `page(id, setup, { onPop, transient })` | A page: `setup(params)` returns the view (or a render function); a view has `title`, `body`, `actions`, `right`, `center`, `focus`, `bottom`, `columns`, `layout` |
| `nav.push`, `nav.replace`, `nav.reset`, `nav.pop`, `nav.depth`, `nav.top` | The page stack |
| `state(initial, { store, save })`: `value`, `set`, `update` | State; `update(f)` may change it in place and still redraws |
| `refresh()` | Redraws when something outside a state changed (a variable a timer set) |
| `app(rootPage)` or `app({ root, onStart, onOpen, onEvent, onForeground, onBackground, onNotif, save })` | Runs the app |
| `tr`, `call`, `cancelled`, `toast`, `notify`, `storage`, `lang`, `appId` | The interfaces of the same names in the `kario` module ([22](22-developer-api.md) §9.2) |

## 13. Two engines

On the developer machine `kario run` and `kario test` use Node.js (V8); on the device it is QuickJS-ng (`kario-jsrt`). The `kario` module behaves the same on both (the runtime tests of `jsrt/` run on both engines); the differences are yours to mind:

- Only standard ECMAScript and `kario`: under Node globals such as `process` and `Buffer` can be reached, on the device they are not there; the type definitions leave them out and `tsc` reports them. Under Node too, `main.js` can import only `kario`.
- Node is not a sandbox: on the developer machine an app is not held to anything but its permissions (files, network); on the device it is ([17](17-app-sandbox.md)).
- Speed: QuickJS is an interpreter, much slower than V8; heavy computation goes to the system services, measured on a phone.
