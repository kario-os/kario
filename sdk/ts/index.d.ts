// @kario/sdk: what a TypeScript app sees on Kario (29-app-development.md).
// This file is the module "kario", which kario-jsrt provides at run time;
// ui.d.ts, generated from the Go sources, holds the protocol's types. There
// is nothing else: no Node.js and no browser API, no file, socket or process.
// A project maps "kario" to this file (tsconfig "paths"); kario build leaves
// the import for the runtime.

import type {
  CapArgs,
  CapName,
  CapResult,
  EventName,
  Language,
  Notification,
  Screen,
} from "./ui";

export * from "./ui";

/** The app's ID (the manifest's "id"). */
export const appId: string;

/**
 * The device's language. tr() follows it; a change of language restarts the
 * app with its state (on "save").
 */
export const lang: Language;

/** Replaces the page stack with one page. */
export function reset(screen: Screen): void;
/** Opens a page on top; Back closes it (then "popped"). */
export function push(screen: Screen): void;
/** Replaces the top page. */
export function replace(screen: Screen): void;
/** Updates the pages with this screen's id in place. */
export function update(screen: Screen): void;
/** Closes the top page. */
export function pop(): void;
/** A short message at the bottom of the screen (only while in the foreground). */
export function toast(text: string): void;
/** A notification in the notification centre. */
export function notify(notification: Notification): void;

/**
 * A capability's call (22-developer-api.md §4): the system checks the
 * permission and may ask the user first. A failure rejects with a CallError
 * whose message is the platform's text; cancelled(err) tells a user's "no".
 * Files a capability hands over (media.pick, audio.stream) do not reach
 * JavaScript.
 */
export function call<C extends CapName>(cap: C, args?: CapArgs[C]): Promise<CapResult[C]>;

/** How a capability's call failed. */
export interface CallError extends Error {
  /** The capability. */
  cap: string;
}

/** The user declined (a system dialog, a picker): err's message ends in "cancelled". */
export function cancelled(err: unknown): boolean;

/** An action the user took on a page. */
export interface ActionEvent {
  /** The page's screen id. */
  screen: string;
  /** The node it was taken on ("" for a page action). */
  node: string;
  /** The action's id. */
  action: string;
  /** An input's text, a canvas's key, a document link's address. */
  value: string;
  /** Every input of the page by id. */
  fields: Record<string, string>;
}

/** The user's choice on a notification. */
export interface NotifEvent {
  id: string;
  action: string;
  /** What the user typed (an action with input). */
  text: string;
}

/** What on() subscribes to; one handler each, a new one replaces it. */
export interface Handlers {
  /**
   * The app starts: arg is the deep link ("" for none); restored is what
   * "save" returned in the process the system ended in the background
   * (a tombstone), undefined when the app starts afresh.
   */
  start(arg: string, restored: unknown): void;
  action(e: ActionEvent): void | Promise<void>;
  /** A focused canvas with keys: the key's name ("up", "5", "enter"). Without this handler keys come as actions. */
  key(key: string, e: ActionEvent): void | Promise<void>;
  /** An edit of a live input. Without this handler edits come as "changed" actions. */
  input(e: ActionEvent): void | Promise<void>;
  /** Back closed a page. */
  popped(screen: string): void;
  foreground(): void;
  /** The app left the screen; the system may freeze it, or end it when memory is short. */
  background(): void;
  /**
   * Called on leaving the screen: what the app needs to rebuild its pages
   * (JSON, at most 64 KB) should the system end it; start gets it back.
   * Text typed into a page comes back by itself when the rebuilt page has
   * the same screen id.
   */
  save(): unknown;
  /** A system event (22-developer-api.md §5). */
  event(name: EventName, value: string): void;
  notif(n: NotifEvent): void;
  /** A new Shell took over; the pages were sent again by the runtime. */
  resync(): void;
}

export function on<K extends keyof Handlers>(name: K, handler: Handlers[K] | null): void;

/**
 * msg in the device's language from locale/<language>.json beside the code,
 * along the platform's fallback chain (zh-Hant, then zh-Hans, then the English
 * source); args go into %s, %v and %d in order.
 */
export function tr(msg: string, ...args: unknown[]): string;

/**
 * A small key-value store of JSON values (1 MB in all) that the runtime keeps
 * in the app's data directory; it outlives the process. set() throws when the
 * store would grow past its size.
 */
export const storage: {
  get<T = unknown>(key: string): T | undefined;
  set(key: string, value: unknown): void;
  remove(key: string): void;
  keys(): string[];
  clear(): void;
};

declare global {
  function setTimeout<A extends unknown[]>(fn: (...args: A) => void, ms?: number, ...args: A): number;
  function setInterval<A extends unknown[]>(fn: (...args: A) => void, ms?: number, ...args: A): number;
  function clearTimeout(id: number | undefined): void;
  function clearInterval(id: number | undefined): void;
  function queueMicrotask(fn: () => void): void;
  function atob(data: string): string;
  function btoa(data: string): string;
  /** Lines on the app's log (stderr), which the system keeps. */
  const console: {
    log(...data: unknown[]): void;
    info(...data: unknown[]): void;
    warn(...data: unknown[]): void;
    error(...data: unknown[]): void;
    debug(...data: unknown[]): void;
  };
  const performance: { now(): number };
}
