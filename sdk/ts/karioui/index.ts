// KarioUI (@kario/ui): the declarative way to write a Kario app
// (29-app-development.md §2). Components are functions returning the
// semantic nodes the system draws; a page is a function of the app's state;
// when the state changes the pages are drawn again, and only the pages whose
// screens changed are sent (update), the Shell and the renderer doing the
// rest. There is no layout here: focus, menus, soft keys, Back and the input
// method stay the system's. kario build bundles this file into main.js.
import * as kario from "kario";
import type { Action, ActionEvent, ActionGroup, Canvas, EventName, Layout, Node, NodeStyle, NotifEvent, Screen } from "kario";

export { appId, call, cancelled, lang, notify, storage, toast, tr } from "kario";
export type { ActionEvent, Node, Screen } from "kario";

/** What an action runs; the pages are drawn again when it returns, and again when its promise settles. */
export type Handler = (e: ActionEvent) => void | Promise<void>;

/** An action and what it runs (act). */
export interface Act {
  readonly id: string;
  readonly label: string;
  readonly run: Handler;
  /** A shortcut key, shown in the Options menu. */
  readonly key?: string;
  readonly group?: ActionGroup;
  /** The system asks this first (with "danger", a confirmation dialog). */
  readonly confirm?: string;
  /** Only its key runs it; the menus leave it out (a calculator's digits). */
  readonly hidden?: boolean;
  /** OK runs it. */
  readonly primary?: boolean;
}

export type ActOptions = Pick<Act, "key" | "group" | "confirm" | "hidden" | "primary">;

/** act is an action: an id the app keeps stable, the label the menus show, what it does. */
export function act(id: string, label: string, run: Handler, o: ActOptions = {}): Act {
  return { id, label, run, ...o };
}

const BIND = Symbol("karioui");

interface Binding {
  acts?: Act[];
  submit?: (value: string, e: ActionEvent) => void | Promise<void>;
  change?: (value: string, e: ActionEvent) => void | Promise<void>;
  key?: (key: string, e: ActionEvent) => void | Promise<void>;
  bind?: State<string>;
}

type Bound = Node & { [BIND]?: Binding };

/** What a page's body holds: nodes, lists of them, and false, null, undefined or "" for nothing (cond && Text(…)). */
export type Child = Node | readonly Child[] | false | null | undefined | "";

function action(a: Act): Action {
  const out: Action = { id: a.id, label: a.label };
  if (a.key) out.key = a.key;
  if (a.primary) out.primary = true;
  if (a.group) out.group = a.group;
  if (a.confirm) out.confirm = a.confirm;
  if (a.hidden) out.hidden = true;
  return out;
}

function bound(n: Node, b: Binding): Node {
  const acts = b.acts?.filter(Boolean) ?? [];
  if (acts.length) n.actions = acts.map(action);
  (n as Bound)[BIND] = { ...b, acts };
  return n;
}

function ok(onOk: Handler | Act | undefined, label: string): Act | undefined {
  if (!onOk) return undefined;
  return typeof onOk === "function" ? act("ok", label, onOk, { primary: true }) : { ...onOk, primary: true };
}

/** Text: a paragraph, or the calculator's result (style "display"). */
export function Text(text: string, o: { id?: string; style?: NodeStyle; actions?: Act[] } = {}): Node {
  return bound({ kind: "text", id: o.id, text, style: o.style }, { acts: o.actions });
}

/** Section: a group's heading. */
export function Section(text: string): Node {
  return { kind: "section", text };
}

export interface ItemOptions {
  id?: string;
  subtitle?: string;
  trailing?: string;
  badge?: string;
  /** A one-character icon for the text UI. */
  icon?: string;
  /** An image the system shows with it (media:<id>). */
  src?: string;
  style?: NodeStyle;
  /** What OK does: a handler (its menu label is the title) or an action. */
  onOk?: Handler | Act;
  /** More actions: the Options menu while the item has the focus. */
  actions?: Act[];
}

/** Item: one entry of a list. Give it an id: the focus and the actions go by it. */
export function Item(title: string, o: ItemOptions = {}): Node {
  const n: Node = { kind: "item", id: o.id, title, subtitle: o.subtitle, trailing: o.trailing, badge: o.badge, icon: o.icon, src: o.src, style: o.style };
  return bound(n, { acts: [ok(o.onOk, title), ...(o.actions ?? [])].filter((a): a is Act => !!a) });
}

/** Tile: an item laid out in the page's grid (columns). */
export function Tile(title: string, o: ItemOptions = {}): Node {
  return Item(title, { ...o, style: "tile" });
}

export interface InputOptions {
  placeholder?: string;
  /** The text; a State is kept up to date with what the user types. */
  value?: string | State<string>;
  secret?: boolean;
  /** "number", "phone" or "email": the input method starts there. */
  keyboard?: "" | "number" | "phone" | "email";
  /** The Shell empties the input once it is submitted. */
  clear?: boolean;
  /** OK in the input; the action's id is submitId ("submit"). */
  onSubmit?: (value: string, e: ActionEvent) => void | Promise<void>;
  submitId?: string;
  /** Every edit (search as you type). */
  onChange?: (value: string, e: ActionEvent) => void | Promise<void>;
}

/** Input: one line of text the system's input method types into. */
export function Input(id: string, o: InputOptions = {}): Node {
  const st = typeof o.value === "object" ? o.value : undefined;
  const n: Node = {
    kind: "input",
    id,
    placeholder: o.placeholder,
    value: st ? st.value : (o.value as string | undefined),
    secret: o.secret,
    keyboard: o.keyboard,
    clear: o.clear,
    submit: o.onSubmit ? (o.submitId ?? "submit") : undefined,
    live: o.onChange ? true : undefined,
  };
  return bound(n, { submit: o.onSubmit, change: o.onChange, bind: st });
}

/** Button: a page's one obvious thing to do. */
export function Button(text: string, onOk: Handler | Act, o: { id?: string; actions?: Act[] } = {}): Node {
  const a = ok(onOk, text)!;
  return bound({ kind: "button", id: o.id ?? (typeof onOk === "function" ? text : onOk.id), text }, { acts: [a, ...(o.actions ?? [])] });
}

/** Progress: a bar from 0 to 1. */
export function Progress(fraction: number, o: { id?: string; text?: string; trailing?: string } = {}): Node {
  return { kind: "progress", id: o.id, fraction, text: o.text, trailing: o.trailing };
}

/** Image: media:<id> (a photo the app may see) or qr:<text>. */
export function Image(src: string, o: { id?: string; rows?: number; onOk?: Act; actions?: Act[] } = {}): Node {
  return bound({ kind: "image", id: o.id, src, rows: o.rows }, { acts: [ok(o.onOk, ""), ...(o.actions ?? [])].filter((a): a is Act => !!a) });
}

/** Doc: Markdown the system lays out; its links become menu actions. */
export function Doc(markdown: string, o: { id?: string } = {}): Node {
  return { kind: "doc", id: o.id, text: markdown };
}

/** Media: a video stream one of the app's calls or players handed out. */
export function Media(stream: string, o: { id?: string; pip?: string } = {}): Node {
  return { kind: "media", id: o.id, stream, pip: o.pip };
}

/** Web: an http(s) page in the system's web view. */
export function Web(src: string, o: { id: string }): Node {
  return { kind: "web", id: o.id, src };
}

/** Drawing: graphics the system draws; with onKey a focused canvas gets the keys (games). */
export function Drawing(canvas: Canvas, o: { id: string; onKey?: (key: string, e: ActionEvent) => void | Promise<void>; actions?: Act[] }): Node {
  return bound({ kind: "canvas", id: o.id, canvas: o.onKey ? { ...canvas, keys: true } : canvas }, { key: o.onKey, acts: o.actions });
}

/** List: one or more nodes per element. */
export function List<T>(items: readonly T[], render: (item: T, index: number) => Child): Child[] {
  return items.map(render);
}

/** A page as its function returns it. */
export interface View {
  title: string;
  body: readonly Child[];
  /** The page's actions: the Options menu and the command palette. */
  actions?: readonly Act[];
  /** The right soft key (one of actions, or added to them). */
  right?: Act;
  /** What OK does while nothing has the focus. */
  center?: Act;
  /** Where the focus starts. */
  focus?: string;
  /** The focus starts at the bottom (a chat). */
  bottom?: boolean;
  /** Tiles in a grid of this many columns. */
  columns?: number;
  layout?: Layout;
}

export interface Page<P> {
  readonly id: string;
  readonly setup: (params: P) => View | (() => View);
  readonly options: PageOptions;
}

export interface PageOptions {
  /** Back closed the page (or the app popped it). */
  onPop?: () => void;
  /** Left out of the state kept for a tombstone, with the pages above it. */
  transient?: boolean;
}

const pages = new Map<string, Page<any>>();

/**
 * page defines a page by its screen id. setup gets the page's parameters
 * and returns its view; or, to keep state of its own, it returns the
 * function that does (it then runs once each time the page opens). The
 * parameters are JSON: they are what brings the page back after the system
 * ended the app in the background.
 */
export function page<P = void>(id: string, setup: (params: P) => View | (() => View), options: PageOptions = {}): Page<P> {
  if (pages.has(id)) throw new Error(`page "${id}" is defined twice`);
  const p: Page<P> = { id, setup, options };
  pages.set(id, p);
  return p;
}

/** A value the pages read; setting it draws them again. */
export interface State<T> {
  value: T;
  set(value: T): void;
  /** f may change the value in place: the pages are drawn again either way. */
  update(f: (value: T) => T | void): void;
}

export interface StateOptions {
  /** Kept for a tombstone under this key: the system ended the app in the background, and it comes back as it was. */
  save?: string;
  /** Kept in storage under this key: it outlives the process. */
  store?: string;
}

const saved = new Map<string, Cell<any>>();

class Cell<T> implements State<T> {
  constructor(
    private v: T,
    private readonly o: StateOptions,
  ) {}
  get value(): T {
    return this.v;
  }
  set value(v: T) {
    this.set(v);
  }
  set(v: T): void {
    if (Object.is(v, this.v)) return;
    this.put(v);
  }
  update(f: (value: T) => T | void): void {
    const r = f(this.v);
    this.put(r === undefined ? this.v : r);
  }
  restore(v: T): void {
    this.v = v;
  }
  private put(v: T): void {
    this.v = v;
    if (this.o.store) kario.storage.set(this.o.store, v);
    refresh();
  }
}

/** state is a value the pages depend on: module-level for the app, or in a page's setup for that page. */
export function state<T>(initial: T, o: StateOptions = {}): State<T> {
  let v = initial;
  if (o.store) {
    const s = kario.storage.get<T>(o.store);
    if (s !== undefined) v = s;
  }
  const c = new Cell(v, o);
  if (o.save) {
    if (saved.has(o.save)) throw new Error(`state "${o.save}" is saved twice`);
    saved.set(o.save, c);
  }
  return c;
}

interface Built {
  screen: Screen;
  json: string;
  focus?: string;
  handlers: Map<string, Handler>;
  binds: Map<string, State<string>>;
}

interface Route {
  page: Page<any>;
  params: unknown;
  screen: string;
  render: () => View;
  built?: Built;
}

// The Shell keeps at most this many pages; a push beyond replaces the top.
const MAX_STACK = 32;

let routes: Route[] = [];
let popping: string[] = [];
let starting = false;
let dirty = false;
let config: AppConfig | undefined;

const slot = (node: string, action: string) => node + "\u0000" + action;

function flatten(children: readonly Child[], out: Node[]): Node[] {
  for (const c of children) {
    if (Array.isArray(c)) flatten(c, out);
    else if (c) out.push(c as Node);
  }
  return out;
}

function build(r: Route): Built {
  const v = r.render();
  const handlers = new Map<string, Handler>();
  const binds = new Map<string, State<string>>();
  const body = flatten(v.body, []);
  body.forEach((n, i) => {
    const b = (n as Bound)[BIND];
    if (!b) return;
    if (!n.id) n.id = `_${i}`;
    const id = n.id;
    for (const a of b.acts ?? []) handlers.set(slot(id, a.id), a.run);
    if (b.submit && n.submit) {
      const submit = b.submit;
      handlers.set(slot(id, n.submit), (e) => {
        if (b.bind && n.clear) b.bind.set("");
        return submit(e.value, e);
      });
    }
    if (b.change) {
      const change = b.change;
      handlers.set(slot(id, "changed"), (e) => change(e.value, e));
    }
    if (b.key) {
      const key = b.key;
      handlers.set(slot(id, "key"), (e) => key(e.value, e));
    }
    if (b.bind) binds.set(id, b.bind);
  });
  const acts = [...(v.actions ?? [])];
  for (const a of [v.right, v.center]) if (a && !acts.some((x) => x.id === a.id)) acts.push(a);
  for (const a of acts) handlers.set(slot("", a.id), a.run);
  const screen: Screen = { id: r.screen, title: v.title, body };
  if (acts.length) screen.actions = acts.map(action);
  if (v.right) screen.right = v.right.id;
  if (v.center) screen.center = v.center.id;
  if (v.bottom) screen.bottom = true;
  if (v.columns) screen.columns = v.columns;
  if (v.layout) screen.layout = v.layout;
  return { screen, json: JSON.stringify(screen), focus: v.focus, handlers, binds };
}

function open(page: Page<any>, params: unknown, below: Route[]): Route {
  let screen = page.id;
  for (let n = 2; below.some((r) => r.screen === screen); n++) screen = `${page.id}~${n}`;
  const made = page.setup(params);
  if (typeof made === "function") return { page, params, screen, render: made };
  let ready: View | undefined = made;
  const render = () => {
    const v = ready ?? (page.setup(params) as View);
    ready = undefined;
    return v;
  };
  return { page, params, screen, render };
}

function first(r: Route): Screen {
  r.built = build(r);
  return r.built.focus ? { ...r.built.screen, focus: r.built.focus } : r.built.screen;
}

/** refresh draws the pages again: for what changed outside a State (a timer's variable). */
export function refresh(): void {
  if (dirty || starting || !config) return;
  dirty = true;
  void Promise.resolve().then(flush);
}

function flush(): void {
  dirty = false;
  for (const r of routes) {
    const b = build(r);
    const changed = b.json !== r.built?.json;
    r.built = b;
    if (changed) kario.update(b.screen);
  }
}

// settle draws the pages again after a handler, and once more when its
// promise settles; a rejection stays unhandled, so the log shows it.
function settle(out: void | Promise<void>): void | Promise<void> {
  refresh();
  if (out && typeof out.then === "function") return out.finally(refresh);
}

/** The page stack. Back is the system's: the user goes back without the app, which hears of it (onPop). */
export const nav = {
  /** Opens a page on top. */
  push<P>(page: Page<P>, params: P): void {
    const r = open(page, params, routes);
    if (routes.length >= MAX_STACK) routes[routes.length - 1] = r;
    else routes.push(r);
    if (!starting) kario.push(first(r));
    refresh();
  },
  /** Replaces the top page. */
  replace<P>(page: Page<P>, params: P): void {
    const r = open(page, params, routes.slice(0, -1));
    routes[Math.max(0, routes.length - 1)] = r;
    if (!starting) kario.replace(first(r));
    refresh();
  },
  /** Starts the stack again with one page. */
  reset<P>(page: Page<P>, params: P): void {
    const r = open(page, params, []);
    routes = [r];
    popping = [];
    if (!starting) kario.reset(first(r));
    refresh();
  },
  /** Closes the top page (not the first). */
  pop(): void {
    if (routes.length < 2) return;
    const r = routes.pop()!;
    if (!starting) {
      popping.push(r.screen);
      kario.pop();
    }
    r.page.options.onPop?.();
    refresh();
  },
  /** How many pages are open. */
  get depth(): number {
    return routes.length;
  },
  /** The top page's screen id. */
  get top(): string {
    return routes.at(-1)?.screen ?? "";
  },
};

export interface AppConfig {
  /** The first page. */
  root: Page<void>;
  /** The app starts; arg is the deep link, restored what save() returned before the system ended it. */
  onStart?(arg: string, restored: unknown): void;
  /** A deep link: at start (arg) and while running (the "open" event). */
  onOpen?(link: string): void;
  /** A system event (22-developer-api.md §5). */
  onEvent?(name: EventName, value: string): void;
  onForeground?(): void;
  onBackground?(): void;
  onNotif?(n: NotifEvent): void;
  /** More than the saved states and the pages, for a tombstone (JSON). */
  save?(): unknown;
}

interface Tomb {
  karioui: 1;
  routes: { page: string; params: unknown }[];
  states: Record<string, unknown>;
  app?: unknown;
}

function tomb(): Tomb {
  const rs: Tomb["routes"] = [];
  for (const r of routes) {
    if (r.page.options.transient) break;
    rs.push({ page: r.page.id, params: r.params });
  }
  const states: Record<string, unknown> = {};
  for (const [k, c] of saved) states[k] = c.value;
  return { karioui: 1, routes: rs, states, app: config?.save?.() };
}

function isTomb(v: unknown): v is Tomb {
  return !!v && typeof v === "object" && (v as Tomb).karioui === 1 && Array.isArray((v as Tomb).routes);
}

// dispatch runs what an action on one of the pages does; an action on a page
// no longer in the stack does nothing (its handlers are gone with it).
function dispatch(e: ActionEvent): void | Promise<void> {
  const r = routes.find((x) => x.screen === e.screen);
  if (!r?.built) return;
  for (const [id, st] of r.built.binds) if (Object.hasOwn(e.fields, id)) st.set(e.fields[id]);
  const h = r.built.handlers.get(slot(e.node, e.action)) ?? r.built.handlers.get(slot("", e.action));
  return settle(h?.(e));
}

/**
 * app runs the app: root is its first page (or the configuration's). It
 * takes the module "kario"'s handlers; with KarioUI an app hears of the
 * system through the configuration instead.
 */
export function app(root: Page<void> | AppConfig): void {
  config = "setup" in root ? { root } : root;
  const c = config;
  kario.on("start", (arg, restored) => {
    starting = true;
    try {
      routes = [];
      let appState: unknown = restored;
      if (isTomb(restored)) {
        appState = restored.app;
        for (const [k, v] of Object.entries(restored.states ?? {})) saved.get(k)?.restore(v);
        for (const s of restored.routes) {
          const p = pages.get(s.page);
          if (!p || (routes.length === 0 && p !== c.root)) break;
          routes.push(open(p, s.params, routes));
        }
      }
      if (routes.length === 0) routes.push(open(c.root, undefined, []));
      c.onStart?.(arg, appState);
      if (arg) c.onOpen?.(arg);
    } finally {
      starting = false;
    }
    routes.forEach((r, i) => (i ? kario.push : kario.reset)(first(r)));
  });
  kario.on("action", dispatch);
  kario.on("popped", (screen) => {
    if (popping[0] === screen) {
      popping.shift();
      return;
    }
    if (routes.length > 1 && routes.at(-1)!.screen === screen) {
      routes.pop()!.page.options.onPop?.();
      refresh();
    }
  });
  kario.on("save", tomb);
  kario.on("event", (name, value) => {
    if (name === "open") c.onOpen?.(value);
    settle(c.onEvent?.(name, value));
  });
  kario.on("foreground", () => settle(c.onForeground?.()));
  kario.on("background", () => settle(c.onBackground?.()));
  kario.on("notif", (n) => settle(c.onNotif?.(n)));
}
