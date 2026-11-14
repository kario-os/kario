// @kario/test: an app's tests under Node.js against a fake Shell (kario test,
// 29-app-development.md §4). launch() starts the app's built main.js as the
// Shell would, speaking the same protocol through the same runtime as kario
// run (sdk/ts/node), and the driver plays the user: keys, typing, the
// Options menu, Back, leaving the screen, the system ending the app. The
// focus and keys are a simple model of the Shell's (22-developer-api.md §3);
// the selftest on the real Shell stays the reference.
import type { Action, Node, Notification, Screen } from "kario";

/** What kario test's runner (sdk/ts/node/test.mjs) provides. */
interface Host {
  test(name: string, fn: () => void | Promise<void>): unknown;
  shells: Map<string, FakeShell>;
  appURL: string;
  /** The language apps start in unless launch says (kario test -lang). */
  lang: string;
  manifest(): { id?: string } | undefined;
  readFile(rel: string): string | undefined;
  immediate(fn: () => void): void;
  later(fn: () => void, ms: number): void;
}

const host = (globalThis as unknown as { __karioTest?: Host }).__karioTest;

function need(): Host {
  if (!host) throw new Error("@kario/test runs under kario test");
  return host;
}

/** test is node:test's test: a named case, failed by what it throws. */
export function test(name: string, fn: () => void | Promise<void>): void {
  need().test(name, fn);
}

/** A fake capability: what call(cap, args) resolves to; throw to fail it (the error's message is the platform's text, "cancelled" for a user's no). */
export type Capability = (args: any) => unknown;

export interface LaunchOptions {
  /** PHONE_LANG (default kario test's -lang, else "en"). */
  lang?: string;
  /** The deep link the app starts with. */
  arg?: string;
  /** What the previous process saved (a tombstone's state). */
  restored?: unknown;
  /** The app's storage as a previous process left it. */
  storage?: Record<string, string>;
  /** Answers to capability calls by name; without one a call fails. */
  capabilities?: Record<string, Capability>;
}

/** The protocol's messages, as the fake Shell reads and writes them. */
interface Msg {
  t: string;
  app?: string;
  proto?: string;
  screen?: Screen;
  screen_id?: string;
  node?: string;
  action?: string;
  value?: string;
  fields?: Record<string, string>;
  text?: string;
  notif?: Notification;
  state?: string;
  req?: number;
  cap?: string;
  args?: unknown;
  ok?: boolean;
  err?: string;
  data?: unknown;
  event?: string;
}

interface Page {
  screen: Screen;
  focus: string;
  inputs: Record<string, string>;
  sent: Record<string, string | undefined>;
}

let instances = 0;

function focusable(n: Node): boolean {
  switch (n.kind) {
    case "input":
    case "button":
    case "web":
      return true;
    case "item":
    case "media":
    case "image":
    case "doc":
      return !!n.actions?.length;
    case "canvas":
      return !!n.canvas?.keys || !!n.actions?.length;
  }
  return false;
}

function focusables(s: Screen): Node[] {
  return s.body.filter((n) => n.id && focusable(n));
}

function nodeText(n: Node, typed?: string): string[] {
  return [n.text, n.title, n.subtitle, n.trailing, n.badge, n.kind === "input" ? typed || n.placeholder : undefined].filter(
    (s): s is string => !!s,
  );
}

/** The fake Shell: one app process's other end. */
class FakeShell {
  readonly appId: string;
  readonly lang: string;
  readonly store: Record<string, string>;
  readonly storage: { load(): string | undefined; save(text: string): void };
  readonly stack: Page[] = [];
  readonly toasts: string[] = [];
  readonly notifications: Notification[] = [];
  readonly calls: { cap: string; args: unknown }[] = [];
  readonly logs: string[] = [];
  readonly confirmed: string[] = [];
  saved: unknown;
  hello = false;
  ended = false;
  activity = 0;
  private toApp?: (line: string) => void;
  private close?: () => void;

  constructor(
    readonly id: string,
    readonly o: LaunchOptions,
  ) {
    this.appId = need().manifest()?.id ?? "com.example.test";
    this.lang = o.lang ?? need().lang;
    this.store = { ...(o.storage ?? {}) };
    this.storage = {
      load: () => (Object.keys(this.store).length ? JSON.stringify(this.store) : undefined),
      save: (text: string) => {
        const next = JSON.parse(text) as Record<string, string>;
        for (const k of Object.keys(this.store)) delete this.store[k];
        Object.assign(this.store, next);
      },
    };
  }

  /** fake.mjs connects the app's runtime. */
  attach(toApp: (line: string) => void, close: () => void): void {
    this.toApp = toApp;
    this.close = close;
  }

  log(line: string): void {
    this.logs.push(line);
  }

  send(m: Msg): void {
    if (this.ended) return;
    const line = JSON.stringify(m);
    this.activity++;
    need().immediate(() => {
      if (!this.ended) this.toApp?.(line);
    });
  }

  /** A message from the app (its runtime's send). */
  fromApp(line: string): void {
    if (this.ended) return;
    this.activity++;
    const m = JSON.parse(line) as Msg;
    switch (m.t) {
      case "hello":
        this.hello = true;
        break;
      case "reset":
        this.stack.length = 0;
        this.stack.push(this.page(m.screen!));
        break;
      case "push":
        if (this.stack.length >= 32) this.stack[this.stack.length - 1] = this.page(m.screen!);
        else this.stack.push(this.page(m.screen!));
        break;
      case "replace":
        this.stack[Math.max(0, this.stack.length - 1)] = this.page(m.screen!);
        break;
      case "update":
        for (const p of this.stack) if (p.screen.id === m.screen!.id) this.replace(p, m.screen!);
        break;
      case "pop":
        if (this.stack.length > 1) this.send({ t: "popped", screen_id: this.stack.pop()!.screen.id });
        break;
      case "toast":
        this.toasts.push(m.text ?? "");
        break;
      case "notify":
        if (m.notif) this.notifications.push(m.notif);
        break;
      case "state":
        this.saved = m.data;
        break;
      case "req":
        this.request(m);
        break;
    }
  }

  private request(m: Msg): void {
    const cap = m.cap ?? "";
    this.calls.push({ cap, args: m.args });
    const fake = this.o.capabilities?.[cap];
    const answer = (r: Pick<Msg, "ok" | "err" | "data">) => this.send({ ...r, t: "res", req: m.req });
    if (!fake) {
      answer({ ok: false, err: `${cap}: no answer in this test (launch's capabilities)` });
      return;
    }
    Promise.resolve()
      .then(() => fake(m.args))
      .then(
        (data) => answer({ ok: true, data }),
        (e: unknown) => answer({ ok: false, err: e instanceof Error ? e.message : String(e) }),
      );
  }

  private page(s: Screen): Page {
    const fs = focusables(s);
    let focus = "";
    if (s.focus) focus = s.focus;
    else if (s.bottom && fs.length) focus = fs[fs.length - 1].id!;
    else if (fs.length) focus = fs[0].id!;
    const p: Page = { screen: s, focus, inputs: {}, sent: {} };
    this.syncInputs(p);
    return p;
  }

  // replace keeps the focus by node id, else the next focusable, as the Shell does.
  private replace(p: Page, s: Screen): void {
    const old = p.screen.body.findIndex((n) => n.id === p.focus);
    p.screen = s;
    this.syncInputs(p);
    const fs = focusables(s);
    if (!fs.some((n) => n.id === p.focus)) {
      const after = fs.find((n) => s.body.indexOf(n) >= old);
      p.focus = (after ?? fs.at(-1))?.id ?? "";
    }
  }

  private syncInputs(p: Page): void {
    for (const n of p.screen.body) {
      if (n.kind !== "input" || !n.id) continue;
      if (!(n.id in p.inputs) || n.value !== p.sent[n.id]) p.inputs[n.id] = n.value ?? "";
      p.sent[n.id] = n.value;
    }
  }

  end(): void {
    this.ended = true;
    this.close?.();
  }
}

/** The app under test, driven as a user would. */
export class App {
  constructor(private readonly sh: FakeShell) {}

  private get top(): Page {
    const p = this.sh.stack.at(-1);
    if (!p) throw new Error("the app has no page");
    return p;
  }

  /** The top page as the app last sent it. */
  get screen(): Screen {
    return this.top.screen;
  }
  /** Every page, the first one first. */
  get stack(): Screen[] {
    return this.sh.stack.map((p) => p.screen);
  }
  /** The focused node's id on the top page. */
  get focus(): string {
    return this.top.focus;
  }
  get toasts(): readonly string[] {
    return this.sh.toasts;
  }
  get notifications(): readonly Notification[] {
    return this.sh.notifications;
  }
  /** The capabilities the app called, in order. */
  get calls(): readonly { cap: string; args: unknown }[] {
    return this.sh.calls;
  }
  /** What the app last handed over for a tombstone. */
  get saved(): unknown {
    return this.sh.saved;
  }
  /** The app's storage (key: JSON text). */
  get storage(): Readonly<Record<string, string>> {
    return this.sh.store;
  }
  /** The app's log lines (console and the runtime's reports). */
  get log(): readonly string[] {
    return this.sh.logs;
  }
  /** The confirmations the user accepted (an action's confirm text). */
  get confirmed(): readonly string[] {
    return this.sh.confirmed;
  }

  /** What the top page shows: the title, then each node's text, an input's typed text. */
  text(): string {
    const p = this.top;
    const lines = [p.screen.title];
    for (const n of p.screen.body) lines.push(nodeText(n, n.id ? p.inputs[n.id] : undefined).join(" · "));
    const right = p.screen.actions?.find((a) => a.id === p.screen.right);
    if (right) lines.push(`[${right.label}]`);
    return lines.filter(Boolean).join("\n");
  }

  has(text: string): boolean {
    return this.sh.stack.length > 0 && this.text().includes(text);
  }

  /** expect fails unless the top page shows text. */
  expect(text: string): void {
    if (!this.has(text)) throw new Error(`the page does not show ${JSON.stringify(text)}:\n${this.dump()}`);
  }

  /** expectGone fails while the top page shows text. */
  expectGone(text: string): void {
    if (this.has(text)) throw new Error(`the page still shows ${JSON.stringify(text)}:\n${this.dump()}`);
  }

  /** at fails unless the top page is this screen id. */
  at(screen: string): void {
    if (this.screen.id !== screen) throw new Error(`at ${this.screen.id}, not ${screen}:\n${this.dump()}`);
  }

  /** wait until the top page shows text (a timer, a capability's answer). */
  async wait(text: string, ms = 2000): Promise<void> {
    const until = Date.now() + ms;
    while (!this.has(text)) {
      if (Date.now() > until) throw new Error(`no ${JSON.stringify(text)} within ${ms} ms:\n${this.dump()}`);
      await new Promise<void>((r) => need().later(r, 10));
    }
  }

  /** The node whose id is idOrText, else whose text is it, else the first (focusable first) whose text includes it. */
  find(idOrText: string): Node | undefined {
    const body = this.screen.body;
    const has = (n: Node) => nodeText(n).some((t) => t.includes(idOrText));
    return (
      body.find((n) => n.id === idOrText) ??
      body.find((n) => nodeText(n).includes(idOrText)) ??
      body.find((n) => focusable(n) && has(n)) ??
      body.find(has)
    );
  }

  private dump(): string {
    if (!this.sh.stack.length) return "(no page)";
    return `${this.screen.id} (focus ${this.focus || "none"})\n${this.text()}`;
  }

  private focused(): Node | undefined {
    return this.screen.body.find((n) => n.id === this.focus && focusable(n));
  }

  private fields(): Record<string, string> {
    return { ...this.top.inputs };
  }

  private async run(a: Action, node: Node | undefined, value = ""): Promise<void> {
    if (a.confirm) this.sh.confirmed.push(a.confirm);
    this.sh.send({ t: "action", screen_id: this.screen.id, node: node?.id ?? "", action: a.id, value, fields: this.fields() });
    await this.settle();
  }

  /** focusOn moves the focus to a node (by id or text), as the arrow keys would. */
  focusOn(idOrText: string): void {
    const n = this.find(idOrText);
    if (!n?.id || !focusable(n)) throw new Error(`no focusable ${JSON.stringify(idOrText)}:\n${this.dump()}`);
    this.top.focus = n.id;
  }

  /** press: OK on a node (by id or text), or the node's action with that id or label. */
  async press(idOrText: string, action?: string): Promise<void> {
    const n = this.find(idOrText);
    if (!n) throw new Error(`no node ${JSON.stringify(idOrText)}:\n${this.dump()}`);
    if (n.id && focusable(n)) this.top.focus = n.id;
    const a = action ? n.actions?.find((x) => x.id === action || x.label === action) : n.actions?.find((x) => x.primary);
    if (!a) throw new Error(`${JSON.stringify(idOrText)} has no ${action ? JSON.stringify(action) : "primary action"}`);
    await this.run(a, n);
  }

  /** The Options menu: the focused node's actions, then the page's (hidden ones left out). */
  menu(): string[] {
    const n = this.focused();
    return [...(n?.actions ?? []), ...(this.screen.actions ?? [])].filter((a) => !a.hidden).map((a) => a.label);
  }

  /** choose runs the Options menu's entry with this label (accepting its confirmation). */
  async choose(label: string): Promise<void> {
    const n = this.focused();
    const own = n?.actions?.find((a) => a.label === label && !a.hidden);
    const page = this.screen.actions?.find((a) => a.label === label && !a.hidden);
    const a = own ?? page;
    if (!a) throw new Error(`no ${JSON.stringify(label)} in the menu: ${this.menu().join(", ")}`);
    await this.run(a, n);
  }

  /**
   * key presses a key: "enter" (OK), "esc" (Back), "f2" (the right soft
   * key), "up", "down", "left", "right", "backspace", or one character
   * (typed into a focused input, else a shortcut key).
   */
  async key(name: string, count = 1): Promise<void> {
    for (let i = 0; i < count; i++) await this.key1(name);
  }

  private move(dir: number): void {
    const fs = focusables(this.screen);
    if (!fs.length) return;
    const i = fs.findIndex((n) => n.id === this.focus);
    const cur = fs[i];
    const grid = this.screen.columns && cur?.style === "tile" ? this.screen.columns : 1;
    const step = Math.abs(dir) === 2 ? 1 : grid;
    const j = Math.min(fs.length - 1, Math.max(0, (i < 0 ? 0 : i) + Math.sign(dir) * step));
    this.top.focus = fs[j].id!;
  }

  private async key1(name: string): Promise<void> {
    const n = this.focused();
    const s = this.screen;
    const actionOf = (id?: string) => s.actions?.find((a) => a.id === id);
    switch (name) {
      case "enter": {
        if (n?.kind === "input") {
          if (!n.submit) return;
          const value = this.top.inputs[n.id!] ?? "";
          const fields = this.fields();
          if (n.clear) this.top.inputs[n.id!] = "";
          this.sh.send({ t: "action", screen_id: s.id, node: n.id, action: n.submit, value, fields });
          await this.settle();
          return;
        }
        const a = n?.actions?.find((x) => x.primary) ?? (n ? undefined : actionOf(s.center));
        if (a) await this.run(a, n);
        return;
      }
      case "esc":
        await this.back();
        return;
      case "f2": {
        const a = actionOf(s.right);
        if (a) await this.run(a, n);
        return;
      }
      case "up":
      case "down":
        if (n?.kind === "canvas" && n.canvas?.keys) return this.canvasKey(n, name);
        this.move(name === "up" ? -1 : 1);
        return;
      case "left":
      case "right": {
        if (n?.kind === "canvas" && n.canvas?.keys) return this.canvasKey(n, name);
        const a = n?.actions?.find((x) => x.key === name) ?? s.actions?.find((x) => x.key === name);
        if (a) return this.run(a, n);
        if (n?.style === "tile") this.move(name === "left" ? -2 : 2);
        return;
      }
      case "backspace":
        if (n?.kind === "input") this.top.inputs[n.id!] = [...(this.top.inputs[n.id!] ?? "")].slice(0, -1).join("");
        return;
    }
    if ([...name].length !== 1) throw new Error(`no key ${JSON.stringify(name)}`);
    if (n?.kind === "input") {
      this.top.inputs[n.id!] = (this.top.inputs[n.id!] ?? "") + name;
      if (n.live) {
        this.sh.send({ t: "action", screen_id: s.id, node: n.id, action: "changed", value: this.top.inputs[n.id!], fields: this.fields() });
        await this.settle();
      }
      return;
    }
    if (n?.kind === "canvas" && n.canvas?.keys) return this.canvasKey(n, name);
    const a = n?.actions?.find((x) => x.key === name) ?? s.actions?.find((x) => x.key === name);
    if (a) await this.run(a, n);
  }

  private async canvasKey(n: Node, key: string): Promise<void> {
    this.sh.send({ t: "action", screen_id: this.screen.id, node: n.id, action: "key", value: key, fields: this.fields() });
    await this.settle();
  }

  /** type presses each character's key. */
  async type(text: string): Promise<void> {
    for (const c of text) await this.key1(c);
  }

  /** fill sets the focused input's text (past the input method). */
  async fill(text: string): Promise<void> {
    const n = this.focused();
    if (n?.kind !== "input") throw new Error(`no input has the focus:\n${this.dump()}`);
    this.top.inputs[n.id!] = text;
  }

  /** back is the Back key: the top page closes (the app hears popped); on the first page the app leaves the screen. */
  async back(): Promise<void> {
    if (this.sh.stack.length > 1) this.sh.send({ t: "popped", screen_id: this.sh.stack.pop()!.screen.id });
    else this.sh.send({ t: "lifecycle", state: "background" });
    await this.settle();
  }

  /** event sends a system event (22-developer-api.md §5). */
  async event(name: string, value = ""): Promise<void> {
    this.sh.send({ t: "event", event: name, value });
    await this.settle();
  }

  /** notif is the user's choice on one of the app's notifications. */
  async notif(id: string, action: string, text = ""): Promise<void> {
    this.sh.send({ t: "notif_action", value: id, action, text });
    await this.settle();
  }

  /** background: the app leaves the screen; it returns what the app saved. */
  async background(): Promise<unknown> {
    this.sh.send({ t: "lifecycle", state: "background" });
    await this.settle();
    return this.sh.saved;
  }

  async foreground(): Promise<void> {
    this.sh.send({ t: "lifecycle", state: "foreground" });
    await this.settle();
  }

  /** resync: a new Shell takes over; the app sends its pages again. */
  async resync(): Promise<void> {
    this.sh.stack.length = 0;
    this.sh.send({ t: "resync" });
    await this.settle();
  }

  /** kill ends the process as the system does when memory is short. */
  kill(): void {
    this.sh.end();
  }

  /** relaunch starts a new process as after a tombstone: the saved state, the same storage. */
  async relaunch(o: LaunchOptions = {}): Promise<App> {
    this.sh.end();
    return launch({ ...this.sh.o, restored: this.sh.saved, storage: { ...this.sh.store }, ...o });
  }

  /** settle waits until the app has answered: its handlers, promises and the capabilities it awaits. */
  async settle(): Promise<void> {
    let quiet = 0;
    for (let i = 0; i < 1000 && quiet < 3; i++) {
      const before = this.sh.activity;
      await new Promise<void>((r) => need().immediate(r));
      quiet = this.sh.activity === before ? quiet + 1 : 0;
    }
  }
}

/** launch starts the app (the build of main.js kario test made) under a fake Shell, in the foreground. */
export async function launch(o: LaunchOptions = {}): Promise<App> {
  const h = need();
  const id = String(++instances);
  const sh = new FakeShell(id, o);
  h.shells.set(id, sh);
  await import(`${h.appURL}?kario=${id}`);
  if (!sh.hello) throw new Error("the app sent no hello");
  sh.send({ t: "start", value: o.arg ?? "", data: o.restored });
  sh.send({ t: "lifecycle", state: "foreground" });
  const app = new App(sh);
  await app.settle();
  return app;
}

/** expectEqual fails unless the two are the same JSON. */
export function expectEqual(got: unknown, want: unknown, what = "value"): void {
  const g = JSON.stringify(got);
  const w = JSON.stringify(want);
  if (g !== w) throw new Error(`${what}: got ${g}, want ${w}`);
}
