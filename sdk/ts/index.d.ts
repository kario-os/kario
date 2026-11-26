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
 * Files a capability hands over come on the result (CapFiles): media.pick's
 * photo and file.pick's file as KarioFile objects in "files", an audio
 * stream's shared ring as "ring".
 */
export function call<C extends CapName>(cap: C, args?: CapArgs[C]): Promise<CallResult<C>>;

/**
 * What a reply brings beside its JSON (22-developer-api.md §7). The members
 * are not enumerable: JSON.stringify, storage and a saved state leave them
 * out.
 */
export interface CapFiles {
  "media.pick": { files: [KarioFile] };
  "file.pick": { files: [KarioFile] };
  "file.open": { files: [KarioFile] };
  "audio.stream": { id: string; ring: AudioRing };
  "audio.record": { id: string; ring: AudioRing };
}

/** A call's result: the capability's JSON, with what came along (CapFiles). */
export type CallResult<C extends CapName> = C extends keyof CapFiles ? CapResult[C] & CapFiles[C] : CapResult[C];

/**
 * A read-only file a reply handed over (a picked photo, a picked file). The
 * app owns it: close() it once done. The runtime closes one the app drops
 * when it is collected, and while 64 are open a reply's files are closed
 * and its call fails.
 */
export interface KarioFile {
  /** Its size in bytes, now. */
  readonly size: number;
  readonly closed: boolean;
  /** The next bytes from where the last read() ended: at most max (default 64 KB, 1 B to 1 MB); empty at the end. */
  read(max?: number): Uint8Array;
  /** The whole file from its start, whatever read() took; a RangeError for a file over 16 MB (read() it in parts). */
  readAll(): Uint8Array;
  close(): void;
}

/**
 * An audio stream's ring shared with the system (audio.stream: the app
 * writes and the system plays; audio.record: the microphone writes and the
 * app reads): 48 kHz mono signed 16-bit samples, at most 10 s of them. Write
 * or read it often (a timer every 20 ms or so): a gap plays as silence, and
 * a recording overwrites what was not read.
 */
export interface AudioRing {
  readonly rate: 48000;
  /** Samples it holds. */
  readonly capacity: number;
  /** Samples waiting for the reader. */
  readonly buffered: number;
  /** The writer finished (finish()). */
  readonly finished: boolean;
  readonly closed: boolean;
  /** Copies as many samples as fit; how many did. */
  write(samples: Int16Array): number;
  /** Copies up to samples.length waiting samples into samples; how many. */
  read(samples: Int16Array): number;
  /** The end of a playback: the system plays what was written, then ends the stream. */
  finish(): void;
  /** Lets go of the ring (a recording goes on until audio.control's "stop"). */
  close(): void;
}

/** How a capability's call failed. */
export interface CallError extends Error {
  /** The capability. */
  cap: string;
}

/** The user declined (a system dialog, a picker): err's message ends in "cancelled". */
export function cancelled(err: unknown): boolean;

/**
 * An HTTP request from the app itself (22-developer-api.md §9.4), for an app
 * whose manifest asks for "net": https:// (and http:// to the device's own
 * loopback), the system's CAs, its own uid, firewall rules and traffic, as a
 * native app's connections. A subset of the web's fetch with its limits: at
 * most 8 at a time, 4 MB sent; GET, HEAD and POST follow at most 5
 * redirects, only to https:// (POST becomes GET on 301, 302 and 303), the
 * other methods get the 3xx. It settles once the response's headers are in;
 * the body is then read whole (at most 8 MB) or as a stream (any length).
 * Any failure, the request's or the network's, rejects with a TypeError
 * whose message starts "fetch: "; a 404 is a response; an aborted signal
 * rejects with its reason (an AbortError unless the app gave one). The
 * input may be a Request, whose fields init's override.
 */
export function fetch(input: string | Request, init?: FetchInit): Promise<FetchResponse>;

/**
 * What a body may be: text goes as UTF-8, a FormData as multipart/form-data,
 * a Blob with its type, a stream of Uint8Arrays (duplex: "half") chunked as
 * it comes.
 */
export type BodyInit = string | ArrayBuffer | ArrayBufferView | Blob | FormData | ReadableStream<Uint8Array>;

/** Headers, an object of strings, or [name, value] pairs. */
export type HeadersInit = Headers | Record<string, string> | Iterable<readonly [string, string]>;

export interface FetchInit {
  /** GET (the default), HEAD, POST, PUT, PATCH, DELETE or OPTIONS. */
  method?: string;
  /** Not Host, Content-Length, Transfer-Encoding, Connection and the like, which the network sets. */
  headers?: HeadersInit;
  /**
   * A string goes as UTF-8, text/plain unless headers say otherwise; bytes
   * as they are; a Blob with its type; a FormData as multipart/form-data
   * (at most 4 MB of these). A ReadableStream of Uint8Arrays goes chunked
   * as the network takes it (any length; read ahead only while less than
   * 256 KB of it waits), once: its request is not redirected, and fails if
   * the stream does.
   */
  body?: BodyInit;
  /** "half", as the web's fetch wants it, with a body that is a stream. */
  duplex?: "half";
  /**
   * Milliseconds, 1 to 120000 (default 30000): until the response's headers
   * and, read whole, its body; a body sent from a stream counts it from its
   * last bytes (so the stream may go quiet that long); a body read as a
   * stream may then go quiet this long while the app waits on it.
   */
  timeout?: number;
  /** Cancels the request at any point, the body's reading included. */
  signal?: AbortSignal;
  /**
   * "include" sends and keeps cookies, in a jar of the app's own (its data
   * directory; a session cookie lasts as long as the process). The default,
   * as in a browser, is "same-origin", and an app is no web origin: no
   * cookies, as with "omit".
   */
  credentials?: "omit" | "same-origin" | "include";
}

/**
 * What fetch answers: a Response whose headers are read-only and, as they
 * were before Response came, also properties by their lower-case names
 * (res.headers["content-type"]; a header named like one of Headers'
 * members only through get()). The body is read whole (at most 8 MB, as
 * often as asked) or as a stream; it is not cloned.
 */
export type FetchResponse = Response & { readonly headers: { readonly [name: string]: string } };

/**
 * A response's body read as it comes: getReader().read() or for await, in
 * chunks of at most 64 KB. The runtime reads at most 256 KB ahead of the
 * app, so a download of any length holds that much; cancel() stops the
 * transfer.
 */
export type ResponseBody = ReadableStream<Uint8Array>;

/** Bytes with a type, in memory (the web's, without URLs for them). */
export class Blob {
  /** Strings go as UTF-8; bytes and Blobs as they are. */
  constructor(parts?: Iterable<string | ArrayBuffer | ArrayBufferView | Blob>, options?: { type?: string });
  readonly size: number;
  /** Lower case; "" when the type given was not printable ASCII. */
  readonly type: string;
  /** Like Array's slice: from start up to end, negative from the end. */
  slice(start?: number, end?: number, type?: string): Blob;
  text(): Promise<string>;
  arrayBuffer(): Promise<ArrayBuffer>;
  bytes(): Promise<Uint8Array>;
  /** Its bytes in chunks of at most 64 KB. */
  stream(): ReadableStream<Uint8Array>;
}

/** A Blob with a name, as a FormData's files are. */
export class File extends Blob {
  constructor(parts: Iterable<string | ArrayBuffer | ArrayBufferView | Blob>, name: string, options?: { type?: string; lastModified?: number });
  readonly name: string;
  readonly lastModified: number;
}

/** Fields and files for a multipart/form-data body (no form element to read). */
export class FormData implements Iterable<[string, string | File]> {
  constructor();
  /** A Blob becomes a File named filename, else its own name or "blob". */
  append(name: string, value: string | Blob, filename?: string): void;
  set(name: string, value: string | Blob, filename?: string): void;
  get(name: string): string | File | null;
  getAll(name: string): (string | File)[];
  has(name: string): boolean;
  delete(name: string): void;
  entries(): IterableIterator<[string, string | File]>;
  keys(): IterableIterator<string>;
  values(): IterableIterator<string | File>;
  forEach(fn: (value: string | File, name: string, form: FormData) => void, self?: unknown): void;
  [Symbol.iterator](): IterableIterator<[string, string | File]>;
}

/** HTTP headers by name, any case; a name given twice joins its values with ", " (Set-Cookie apart). */
export class Headers implements Iterable<[string, string]> {
  constructor(init?: HeadersInit);
  append(name: string, value: string): void;
  set(name: string, value: string): void;
  delete(name: string): void;
  get(name: string): string | null;
  has(name: string): boolean;
  getSetCookie(): string[];
  /** Lower-case names, sorted. */
  entries(): IterableIterator<[string, string]>;
  keys(): IterableIterator<string>;
  values(): IterableIterator<string>;
  forEach(fn: (value: string, name: string, headers: Headers) => void, self?: unknown): void;
  [Symbol.iterator](): IterableIterator<[string, string]>;
}

/** A body read whole (as often as asked) or once as a stream (then not whole); one given as a stream is read whole once, and kept. */
interface BodyMixin {
  /** null for no body. */
  readonly body: ResponseBody | null;
  /** It was read as a stream (one given as a stream: read at all). */
  readonly bodyUsed: boolean;
  text(): Promise<string>;
  json(): Promise<unknown>;
  arrayBuffer(): Promise<ArrayBuffer>;
  bytes(): Promise<Uint8Array>;
  /** Typed by its Content-Type. */
  blob(): Promise<Blob>;
}

/** A request to hand fetch (the web's, with fetch's timeout); the URL is checked by fetch. */
export class Request implements BodyMixin {
  constructor(input: string | Request, init?: FetchInit);
  readonly url: string;
  /** Upper case: one of fetch's methods. */
  readonly method: string;
  readonly headers: Headers;
  readonly signal: AbortSignal;
  readonly credentials: "omit" | "same-origin" | "include";
  /** fetch's timeout (undefined: its default). */
  readonly timeout: number | undefined;
  readonly duplex: "half";
  readonly body: ResponseBody | null;
  readonly bodyUsed: boolean;
  text(): Promise<string>;
  json(): Promise<unknown>;
  arrayBuffer(): Promise<ArrayBuffer>;
  bytes(): Promise<Uint8Array>;
  blob(): Promise<Blob>;
  /** Not one whose body is a stream. */
  clone(): Request;
}

/** A response: fetch's (FetchResponse), or one the app makes (a cache, a test, reading a body as text). */
export class Response implements BodyMixin {
  /** status 200 to 599 (default 200); a 204, 205 or 304 has no body. */
  constructor(body?: BodyInit | null, init?: { status?: number; statusText?: string; headers?: HeadersInit });
  /** JSON of data, Content-Type application/json. */
  static json(data: unknown, init?: { status?: number; statusText?: string; headers?: HeadersInit }): Response;
  /** status 301, 302, 303, 307 or 308 (default 302), Location url. */
  static redirect(url: string, status?: number): Response;
  /** status is 200 to 299. */
  readonly ok: boolean;
  readonly status: number;
  /** "" for fetch's. */
  readonly statusText: string;
  readonly headers: Headers;
  /** fetch's: where it ended up, after redirects; "" otherwise. */
  readonly url: string;
  readonly redirected: boolean;
  /** "basic" for fetch's, "default" otherwise. */
  readonly type: "basic" | "default";
  readonly body: ResponseBody | null;
  readonly bodyUsed: boolean;
  text(): Promise<string>;
  json(): Promise<unknown>;
  arrayBuffer(): Promise<ArrayBuffer>;
  bytes(): Promise<Uint8Array>;
  blob(): Promise<Blob>;
  /** Not fetch's (read it whole again instead), nor one whose body is a stream. */
  clone(): Response;
}

/** A WebSocket's events (the web's, with only the members here). */
export interface WebSocketEvent {
  type: "open" | "message" | "error" | "close";
  target: WebSocket;
}
export interface MessageEvent extends WebSocketEvent {
  /** A text message, or a binary one's bytes. */
  data: string | ArrayBuffer;
}
export interface CloseEvent extends WebSocketEvent {
  /** 1000 and up from the server's close frame; 1006 when the connection went without one; 1002, 1007, 1009 when the server broke the protocol. */
  code: number;
  reason: string;
  /** Both sides closed with close frames. */
  wasClean: boolean;
}
export interface ErrorEvent extends WebSocketEvent {
  message: string;
}
export interface WebSocketEvents {
  open: WebSocketEvent;
  message: MessageEvent;
  error: ErrorEvent;
  close: CloseEvent;
}

export interface WebSocketOptions {
  /** Headers for the opening handshake (fetch's rules; not Sec-WebSocket-*). */
  headers?: Record<string, string>;
  /** Drops the connection at once (close 1006, "aborted"). */
  signal?: AbortSignal;
}

/**
 * A WebSocket from the app itself (22-developer-api.md §9.4), the browser's
 * interface, for an app whose manifest asks for "net": wss:// (and ws:// to
 * the device's own loopback), fetch's network rules and CAs. At most 4 at a
 * time; a message received or sent is at most 4 MB, at most 16 MB wait to be
 * sent; messages are text or ArrayBuffer (binaryType is "arraybuffer"). A
 * server that breaks the protocol (a frame too big, text that is not UTF-8)
 * gets the connection failed: error, then close with 1009, 1007 or 1002.
 * Cookies are not sent. A wrong URL, protocol or header throws.
 */
export class WebSocket {
  constructor(url: string, protocols?: string | string[], options?: WebSocketOptions);
  static readonly CONNECTING: 0;
  static readonly OPEN: 1;
  static readonly CLOSING: 2;
  static readonly CLOSED: 3;
  readonly CONNECTING: 0;
  readonly OPEN: 1;
  readonly CLOSING: 2;
  readonly CLOSED: 3;
  readonly url: string;
  /** The protocol the server chose ("" for none). */
  readonly protocol: string;
  readonly extensions: "";
  readonly readyState: 0 | 1 | 2 | 3;
  /** Bytes waiting to be sent. */
  readonly bufferedAmount: number;
  binaryType: "arraybuffer";
  onopen: ((e: WebSocketEvent) => void) | null;
  onmessage: ((e: MessageEvent) => void) | null;
  onerror: ((e: ErrorEvent) => void) | null;
  onclose: ((e: CloseEvent) => void) | null;
  /** A string goes as a text message, bytes as a binary one; throws while connecting. */
  send(data: string | ArrayBuffer | ArrayBufferView): void;
  /** code is 1000 or 3000 to 4999; reason at most 123 bytes of UTF-8. */
  close(code?: number, reason?: string): void;
  addEventListener<K extends keyof WebSocketEvents>(type: K, listener: (e: WebSocketEvents[K]) => void, options?: { once?: boolean }): void;
  removeEventListener<K extends keyof WebSocketEvents>(type: K, listener: (e: WebSocketEvents[K]) => void): void;
}

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
   * the same screen id. It goes to the system after "background", once
   * what the app stored is on disk.
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
 * in the app's data directory; it outlives the process. set(), remove() and
 * clear() change the store at once (get() reads the new value) and the file
 * is written in the background, in the order of the saves and the newest
 * content next, so a save never waits for the disk: a save that returned is
 * in memory, and on disk once flush() resolves. set() throws at once when
 * the store would grow past its size. The state on "save" goes out, and the
 * process ends (the Shell closing, SIGTERM, a failed main module), only once
 * what was stored is on disk.
 */
export const storage: {
  get<T = unknown>(key: string): T | undefined;
  set(key: string, value: unknown): void;
  remove(key: string): void;
  keys(): string[];
  clear(): void;
  /**
   * Resolves once what was stored before it is on disk. A write that fails
   * (the disk full) rejects it with an Error whose message starts with
   * "storage: " and says so on the app's log; the values stay in memory and
   * the next change writes them all again.
   */
  flush(): Promise<void>;
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

  /** Cancels what takes its signal (fetch, a WebSocket); the web's. */
  class AbortController {
    constructor();
    readonly signal: AbortSignal;
    /** reason defaults to an Error named "AbortError". */
    abort(reason?: unknown): void;
  }
  interface AbortSignal {
    readonly aborted: boolean;
    readonly reason: unknown;
    onabort: ((e: { type: "abort"; target: AbortSignal }) => void) | null;
    throwIfAborted(): void;
    addEventListener(type: "abort", listener: (e: { type: "abort"; target: AbortSignal }) => void, options?: { once?: boolean }): void;
    removeEventListener(type: "abort", listener: (e: { type: "abort"; target: AbortSignal }) => void): void;
  }
  const AbortSignal: {
    /** An aborted signal. */
    abort(reason?: unknown): AbortSignal;
    /** A signal aborted after ms with an Error named "TimeoutError". */
    timeout(ms: number): AbortSignal;
    /** A signal aborted when the first of signals is, with its reason. */
    any(signals: Iterable<AbortSignal>): AbortSignal;
  };

  /** What a ReadableStream's chunks come from (the web's, without type "bytes"). */
  interface UnderlyingSource<R = unknown> {
    /** Called at once; the stream pulls once its promise settles. */
    start?(controller: ReadableStreamDefaultController<R>): unknown;
    /** Called while the queue is below the high-water mark or a read waits; one at a time. */
    pull?(controller: ReadableStreamDefaultController<R>): unknown;
    /** The reader cancelled (reason as given). */
    cancel?(reason?: unknown): unknown;
  }
  interface ReadableStreamDefaultController<R = unknown> {
    /** The high-water mark less what is queued; null once errored. */
    readonly desiredSize: number | null;
    enqueue(chunk: R): void;
    close(): void;
    error(e?: unknown): void;
  }
  class ReadableStreamDefaultReader<R = unknown> {
    constructor(stream: ReadableStream<R>);
    /** Settles once the stream is closed (rejects when it errors or the reader is released). */
    readonly closed: Promise<void>;
    read(): Promise<{ done: false; value: R } | { done: true; value: undefined }>;
    releaseLock(): void;
    cancel(reason?: unknown): Promise<void>;
  }
  /**
   * The web's ReadableStream, a subset on the phone (kario-jsrt; Node.js's
   * is its own, complete one): a source's start, pull and cancel, a
   * strategy's highWaterMark and size, a default reader, for await,
   * ReadableStream.from. Not there: byte streams and BYOB readers, tee,
   * pipeTo, pipeThrough.
   */
  class ReadableStream<R = unknown> implements AsyncIterable<R> {
    constructor(source?: UnderlyingSource<R>, strategy?: { highWaterMark?: number; size?(chunk: R): number });
    /** A stream of an iterable's (or async iterable's) values, pulled as read. */
    static from<R>(items: Iterable<R | PromiseLike<R>> | AsyncIterable<R>): ReadableStream<R>;
    /** It has a reader. */
    readonly locked: boolean;
    cancel(reason?: unknown): Promise<void>;
    getReader(): ReadableStreamDefaultReader<R>;
    /** Locks the stream; leaving the loop early cancels it, unless preventCancel. */
    values(options?: { preventCancel?: boolean }): AsyncIterableIterator<R>;
    [Symbol.asyncIterator](options?: { preventCancel?: boolean }): AsyncIterableIterator<R>;
  }
  /** UTF-8 only on the phone (Node.js knows more encodings). */
  class TextEncoder {
    constructor();
    readonly encoding: "utf-8";
    encode(input?: string): Uint8Array;
  }
  class TextDecoder {
    constructor(label?: "utf-8" | "utf8", options?: { fatal?: boolean; ignoreBOM?: boolean });
    readonly encoding: "utf-8";
    readonly fatal: boolean;
    readonly ignoreBOM: boolean;
    /** With stream: true a sequence cut at the end waits for the next call. */
    decode(input?: ArrayBuffer | ArrayBufferView, options?: { stream?: boolean }): string;
  }
}
