// Blob, File, FormData, Headers, Request, Response and ReadableStream for
// an app's fetch (22-developer-api.md §9.4), subsets of the web's, and
// fetch taking them. One file for both engines: kario-jsrt compiles it the
// first time an app uses one of them or fetch (jsrt/src/net.c), Node.js at
// start (net.mjs). A script whose value is a function of the runtime's own
// fetch, which takes headers as an object of strings and a body as a
// string, bytes or a stream's reader, and gives a response whose headers
// are an object, whose body is a reader, with text(), json() and
// arrayBuffer(); and of the engine's own ReadableStream, if it has one
// (Node.js), which then takes the place of the one here.
(function (coreFetch, EngineStream) {
  "use strict";

  const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];
  const CREDENTIALS = ["omit", "same-origin", "include"];
  const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
  const CHUNK = 64 << 10;
  const utf8 = new TextEncoder();
  const text = (b) => new TextDecoder().decode(b);
  // NET marks a Response made from a fetch's (only fetch has it); MINE the
  // calls only this file makes.
  const NET = {};
  const MINE = {};

  const copy = (v) =>
    v instanceof ArrayBuffer ? new Uint8Array(v.slice(0)) : new Uint8Array(v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength));

  function concat(parts, size) {
    const all = new Uint8Array(size);
    let at = 0;
    for (const p of parts) {
      all.set(p, at);
      at += p.byteLength;
    }
    return all;
  }

  // ---- ReadableStream ----

  // The web's ReadableStream, a subset: streams of any values with a
  // source's start, pull and cancel and a strategy's highWaterMark and
  // size; a default reader; async iteration; ReadableStream.from. Not
  // there: byte streams and their readers (type "bytes", mode "byob"),
  // tee, pipeTo and pipeThrough (there are no WritableStream and
  // TransformStream).
  const desired = (st) => (st.state === "errored" ? null : st.state === "closed" ? 0 : st.hwm - st.queued);

  function pullIfNeeded(st) {
    if (!st.started || st.closing || st.state !== "readable") return;
    if (!st.reader?.requests.length && desired(st) <= 0) return;
    if (st.pulling) {
      st.again = true;
      return;
    }
    st.pulling = true;
    callSource(st.source, st.pull, st.controller).then(
      () => {
        st.pulling = false;
        if (st.again) {
          st.again = false;
          pullIfNeeded(st);
        }
      },
      (e) => streamError(st, e),
    );
  }

  function callSource(source, fn, arg) {
    try {
      return Promise.resolve(fn === undefined ? undefined : fn.call(source, arg));
    } catch (e) {
      return Promise.reject(e);
    }
  }

  function streamClosed(st) {
    st.state = "closed";
    const r = st.reader;
    if (!r) return;
    for (const q of r.requests.splice(0)) q.resolve({ value: undefined, done: true });
    r.closed.resolve();
  }

  function streamError(st, e) {
    if (st.state !== "readable") return;
    st.state = "errored";
    st.error = e;
    st.queue = [];
    st.queued = 0;
    const r = st.reader;
    if (!r) return;
    for (const q of r.requests.splice(0)) q.reject(e);
    r.closed.reject(e);
  }

  function streamCancel(st, reason) {
    st.disturbed = true;
    if (st.state === "closed") return Promise.resolve();
    if (st.state === "errored") return Promise.reject(st.error);
    st.queue = [];
    st.queued = 0;
    streamClosed(st);
    return callSource(st.source, st.cancel, reason).then(() => undefined);
  }

  // settled is a promise with its resolve and reject, marked handled.
  function settled() {
    const s = {};
    s.promise = new Promise((resolve, reject) => {
      s.resolve = resolve;
      s.reject = reject;
    });
    s.promise.catch(() => {});
    return s;
  }

  let stateOf;

  class ReadableStreamDefaultController {
    #st;

    constructor(key, st) {
      if (key !== MINE) throw new TypeError("ReadableStreamDefaultController: made by a ReadableStream");
      this.#st = st;
    }

    get desiredSize() {
      return desired(this.#st);
    }

    enqueue(chunk) {
      const st = this.#st;
      if (st.closing || st.state !== "readable") throw new TypeError("ReadableStreamDefaultController.enqueue: the stream is closed");
      const r = st.reader;
      if (r?.requests.length) {
        r.requests.shift().resolve({ value: chunk, done: false });
      } else {
        let n = 1;
        try {
          if (st.size) n = Number(st.size(chunk));
          if (!(n >= 0) || n === Infinity) throw new RangeError("ReadableStream: a chunk's size is a number from 0 up");
        } catch (e) {
          streamError(st, e);
          throw e;
        }
        st.queue.push([chunk, n]);
        st.queued += n;
      }
      pullIfNeeded(st);
    }

    close() {
      const st = this.#st;
      if (st.closing || st.state !== "readable") throw new TypeError("ReadableStreamDefaultController.close: the stream is closed");
      st.closing = true;
      if (!st.queue.length) streamClosed(st);
    }

    error(e) {
      streamError(this.#st, e);
    }

    get [Symbol.toStringTag]() {
      return "ReadableStreamDefaultController";
    }
  }

  class ReadableStreamDefaultReader {
    #st;
    #r;

    constructor(stream) {
      const st = stateOf(stream);
      if (!st) throw new TypeError("ReadableStreamDefaultReader: the argument is a ReadableStream");
      if (st.reader) throw new TypeError("ReadableStreamDefaultReader: the stream is locked to a reader");
      this.#st = st;
      this.#r = st.reader = { requests: [], closed: settled() };
      if (st.state === "closed") this.#r.closed.resolve();
      else if (st.state === "errored") this.#r.closed.reject(st.error);
    }

    get closed() {
      return this.#r.closed.promise;
    }

    read() {
      const st = this.#st;
      if (!st) return Promise.reject(new TypeError("ReadableStreamDefaultReader.read: the reader was released"));
      st.disturbed = true;
      if (st.state === "closed") return Promise.resolve({ value: undefined, done: true });
      if (st.state === "errored") return Promise.reject(st.error);
      if (st.queue.length) {
        const [chunk, n] = st.queue.shift();
        st.queued -= n;
        if (st.closing && !st.queue.length) streamClosed(st);
        else pullIfNeeded(st);
        return Promise.resolve({ value: chunk, done: false });
      }
      return new Promise((resolve, reject) => {
        this.#r.requests.push({ resolve, reject });
        pullIfNeeded(st);
      });
    }

    releaseLock() {
      const st = this.#st;
      if (!st) return;
      const e = new TypeError("ReadableStreamDefaultReader: the reader was released");
      for (const q of this.#r.requests.splice(0)) q.reject(e);
      if (st.state === "readable") this.#r.closed.reject(e);
      else (this.#r.closed = settled()).reject(e);
      st.reader = null;
      this.#st = null;
    }

    cancel(reason) {
      if (!this.#st) return Promise.reject(new TypeError("ReadableStreamDefaultReader.cancel: the reader was released"));
      return streamCancel(this.#st, reason);
    }

    get [Symbol.toStringTag]() {
      return "ReadableStreamDefaultReader";
    }
  }

  class ReadableStream {
    #st;

    static {
      stateOf = (s) => (s !== null && typeof s === "object" && #st in s ? s.#st : undefined);
    }

    constructor(source, strategy) {
      if (source === undefined) source = {};
      if (source === null || (typeof source !== "object" && typeof source !== "function")) throw new TypeError("ReadableStream: the source is an object");
      if (source.type !== undefined) throw new TypeError('ReadableStream: only streams of values (no type "bytes")');
      for (const k of ["start", "pull", "cancel"])
        if (source[k] !== undefined && typeof source[k] !== "function") throw new TypeError(`ReadableStream: the source's ${k} is a function`);
      const hwm = strategy?.highWaterMark === undefined ? 1 : Number(strategy.highWaterMark);
      if (!(hwm >= 0)) throw new RangeError("ReadableStream: highWaterMark is a number from 0 up");
      const size = strategy?.size;
      if (size !== undefined && typeof size !== "function") throw new TypeError("ReadableStream: the strategy's size is a function");
      const st = (this.#st = {
        state: "readable",
        queue: [],
        queued: 0,
        reader: null,
        error: undefined,
        disturbed: false,
        source,
        pull: source.pull,
        cancel: source.cancel,
        hwm,
        size,
        started: false,
        pulling: false,
        again: false,
        closing: false,
      });
      st.controller = new ReadableStreamDefaultController(MINE, st);
      const started = source.start === undefined ? undefined : source.start.call(source, st.controller);
      Promise.resolve(started).then(
        () => {
          st.started = true;
          pullIfNeeded(st);
        },
        (e) => streamError(st, e),
      );
    }

    get locked() {
      return !!this.#st.reader;
    }

    cancel(reason) {
      if (this.#st.reader) return Promise.reject(new TypeError("ReadableStream.cancel: the stream is locked to a reader"));
      return streamCancel(this.#st, reason);
    }

    getReader(options) {
      const mode = options?.mode;
      if (mode !== undefined) {
        if (String(mode) === "byob") throw new TypeError("ReadableStream.getReader: no BYOB readers (only streams of values)");
        throw new RangeError('ReadableStream.getReader: mode is "byob" or none');
      }
      return new ReadableStreamDefaultReader(this);
    }

    values(options) {
      const reader = this.getReader();
      const preventCancel = !!options?.preventCancel;
      let done = false;
      return {
        async next() {
          if (done) return { value: undefined, done: true };
          let r;
          try {
            r = await reader.read();
          } catch (e) {
            done = true;
            reader.releaseLock();
            throw e;
          }
          if (r.done) {
            done = true;
            reader.releaseLock();
          }
          return r;
        },
        async return(value) {
          if (!done) {
            done = true;
            const p = preventCancel ? undefined : reader.cancel(value);
            reader.releaseLock();
            await p;
          }
          return { value, done: true };
        },
        [Symbol.asyncIterator]() {
          return this;
        },
      };
    }

    [Symbol.asyncIterator](options) {
      return this.values(options);
    }

    static from(items) {
      const get = items?.[Symbol.asyncIterator] ?? items?.[Symbol.iterator];
      if (typeof get !== "function") throw new TypeError("ReadableStream.from: the argument is iterable");
      const it = get.call(items);
      return new ReadableStream(
        {
          async pull(c) {
            const r = await it.next();
            if (r.done) c.close();
            else c.enqueue(await r.value);
          },
          async cancel(reason) {
            await it.return?.(reason);
          },
        },
        { highWaterMark: 0 },
      );
    }

    get [Symbol.toStringTag]() {
      return "ReadableStream";
    }
  }

  // Stream is the engine's ReadableStream, which the classes here make
  // their bodies of.
  const Stream = EngineStream ?? ReadableStream;
  const isStream = (v) => v instanceof Stream || (v !== null && typeof v === "object" && typeof v.getReader === "function");

  // streamOf is bytes read as a stream, in chunks of at most 64 KB, which
  // calls used() once read or cancelled.
  function streamOf(bytes, used) {
    let at = 0;
    return new Stream(
      {
        pull(c) {
          used();
          const value = bytes.slice(at, at + CHUNK);
          at += value.byteLength;
          if (value.byteLength) c.enqueue(value);
          if (at >= bytes.byteLength) c.close();
        },
        cancel() {
          used();
        },
      },
      { highWaterMark: 0 },
    );
  }

  // readAll is a stream's bytes, whole.
  async function readAll(stream) {
    const reader = stream.getReader();
    const parts = [];
    let size = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      const b = value instanceof Uint8Array ? value : ArrayBuffer.isView(value) || value instanceof ArrayBuffer ? copy(value) : null;
      if (!b) throw new TypeError("the body's stream gave a chunk that is not bytes (a Uint8Array)");
      parts.push(b);
      size += b.byteLength;
    }
    return concat(parts, size);
  }

  const why = (e) => (e instanceof Error ? e.message : String(e));

  // pullOf is the runtime's side of a body sent from a stream: its chunks
  // as bytes, the stream's failure as fetch's TypeError (with the stream's
  // error as its cause).
  function pullOf(reader) {
    return {
      async read() {
        let r;
        try {
          r = await reader.read();
        } catch (e) {
          throw new TypeError("fetch: the body's stream failed: " + why(e), { cause: e });
        }
        if (r.done) return { done: true, value: undefined };
        const v = r.value;
        if (!(v instanceof ArrayBuffer || ArrayBuffer.isView(v)))
          throw new TypeError("fetch: the body's stream gave a chunk that is not bytes (a Uint8Array; text goes through TextEncoder)");
        return { done: false, value: v instanceof Uint8Array ? v : copy(v) };
      },
      cancel(reason) {
        try {
          return Promise.resolve(reader.cancel(reason)).catch(() => {});
        } catch {
          return Promise.resolve();
        }
      },
    };
  }

  // netStream is a fetch's body (the runtime's reader) as a stream, read
  // only as the app reads, so the runtime's back-pressure holds.
  function netStream(raw) {
    return new Stream(
      {
        async pull(c) {
          const r = await raw.read();
          if (r.done) c.close();
          else c.enqueue(r.value);
        },
        cancel: () => raw.cancel(),
      },
      { highWaterMark: 0 },
    );
  }

  // ---- Headers ----

  function headerName(n, who) {
    n = String(n);
    if (!TOKEN.test(n)) throw new TypeError(`${who}: ${JSON.stringify(n)} is not a header name`);
    return n.toLowerCase();
  }

  function headerValue(v, who) {
    v = String(v).replace(/^[\t\n\r ]+|[\t\n\r ]+$/g, "");
    if (/[\0\r\n]/.test(v)) throw new TypeError(`${who}: a header value with a line break or NUL`);
    return v;
  }

  // Response headers from the network: read only.
  const locked = new WeakSet();

  class Headers {
    #map = new Map();

    constructor(init) {
      if (init === undefined || init === null) return;
      if (init instanceof Headers) {
        for (const [k, v] of init.#map) this.#map.set(k, v.slice());
        return;
      }
      if (typeof init !== "object" && typeof init !== "function") throw new TypeError("Headers: init is Headers, an object of strings or a list of pairs");
      if (typeof init[Symbol.iterator] === "function") {
        for (const pair of init) {
          const p = typeof pair === "object" && pair !== null && typeof pair[Symbol.iterator] === "function" ? [...pair] : [];
          if (p.length !== 2) throw new TypeError("Headers: each pair is [name, value]");
          this.append(p[0], p[1]);
        }
        return;
      }
      for (const k of Object.keys(init)) this.append(k, init[k]);
    }

    #writable() {
      if (locked.has(this)) throw new TypeError("Headers: the headers of a response from the network are read-only");
    }

    append(name, value) {
      this.#writable();
      const n = headerName(name, "Headers.append");
      const v = headerValue(value, "Headers.append");
      const list = this.#map.get(n);
      if (list) list.push(v);
      else this.#map.set(n, [v]);
    }

    set(name, value) {
      this.#writable();
      this.#map.set(headerName(name, "Headers.set"), [headerValue(value, "Headers.set")]);
    }

    delete(name) {
      this.#writable();
      this.#map.delete(headerName(name, "Headers.delete"));
    }

    get(name) {
      const list = this.#map.get(headerName(name, "Headers.get"));
      return list ? list.join(", ") : null;
    }

    has(name) {
      return this.#map.has(headerName(name, "Headers.has"));
    }

    getSetCookie() {
      return (this.#map.get("set-cookie") ?? []).slice();
    }

    *entries() {
      for (const k of [...this.#map.keys()].sort()) {
        if (k === "set-cookie") for (const v of this.#map.get(k)) yield [k, v];
        else yield [k, this.get(k)];
      }
    }

    *keys() {
      for (const [k] of this.entries()) yield k;
    }

    *values() {
      for (const [, v] of this.entries()) yield v;
    }

    forEach(fn, self) {
      for (const [k, v] of this.entries()) fn.call(self, v, k, this);
    }

    [Symbol.iterator]() {
      return this.entries();
    }

    get [Symbol.toStringTag]() {
      return "Headers";
    }
  }

  // record is headers as fetch takes them: an object of strings.
  function record(h) {
    const out = {};
    for (const [k, v] of h) out[k] = k in out ? out[k] + ", " + v : v;
    return out;
  }

  // netHeaders are a fetch's response headers: read only, and each also a
  // property by its lower-case name, as fetch's headers always were
  // (res.headers["content-type"]), unless the name is one of Headers'.
  function netHeaders(rec) {
    const h = new Headers(rec);
    for (const [k, v] of Object.entries(rec)) {
      const name = k.toLowerCase();
      if (!(name in h)) Object.defineProperty(h, name, { value: String(v), enumerable: true });
    }
    locked.add(h);
    return h;
  }

  // ---- Blob and File ----

  let bytesOf;

  function partBytes(p) {
    if (typeof p === "string") return utf8.encode(p);
    if (p instanceof ArrayBuffer || ArrayBuffer.isView(p)) return copy(p);
    if (p instanceof Blob) return bytesOf(p);
    return utf8.encode(String(p));
  }

  function blobType(t) {
    t = t === undefined ? "" : String(t);
    return /^[\x20-\x7e]*$/.test(t) ? t.toLowerCase() : "";
  }

  class Blob {
    #bytes;
    #type;

    static {
      bytesOf = (b) => b.#bytes;
    }

    constructor(parts, options) {
      if (parts === undefined) parts = [];
      if (parts === null || typeof parts !== "object" || typeof parts[Symbol.iterator] !== "function")
        throw new TypeError("Blob: the parts are a list of strings, ArrayBuffers, typed arrays and Blobs");
      const list = [];
      let size = 0;
      for (const p of parts) {
        const b = partBytes(p);
        list.push(b);
        size += b.byteLength;
      }
      this.#bytes = list.length === 1 ? list[0] : concat(list, size);
      this.#type = blobType(options?.type);
    }

    get size() {
      return this.#bytes.byteLength;
    }

    get type() {
      return this.#type;
    }

    slice(start, end, type) {
      const n = this.#bytes.byteLength;
      const at = (v, d) => {
        if (v === undefined) return d;
        v = Math.trunc(Number(v)) || 0;
        return v < 0 ? Math.max(n + v, 0) : Math.min(v, n);
      };
      const from = at(start, 0);
      const to = Math.max(at(end, n), from);
      return new Blob([this.#bytes.subarray(from, to)], { type });
    }

    async text() {
      return text(this.#bytes);
    }

    async arrayBuffer() {
      return this.#bytes.slice().buffer;
    }

    async bytes() {
      return this.#bytes.slice();
    }

    stream() {
      return streamOf(this.#bytes, () => {});
    }

    get [Symbol.toStringTag]() {
      return "Blob";
    }
  }

  class File extends Blob {
    #name;
    #lastModified;

    constructor(parts, name, options) {
      if (arguments.length < 2) throw new TypeError("File: the parts and a name");
      super(parts, options);
      this.#name = String(name);
      const t = options?.lastModified;
      this.#lastModified = t === undefined ? Date.now() : Math.trunc(Number(t)) || 0;
    }

    get name() {
      return this.#name;
    }

    get lastModified() {
      return this.#lastModified;
    }

    get [Symbol.toStringTag]() {
      return "File";
    }
  }

  // ---- FormData ----

  function formEntry(name, value, filename, files) {
    name = String(name);
    if (value instanceof Blob) {
      if (!(value instanceof File) || filename !== undefined)
        value = new File([value], filename !== undefined ? String(filename) : value instanceof File ? value.name : "blob", {
          type: value.type,
          lastModified: value instanceof File ? value.lastModified : undefined,
        });
      return [name, value];
    }
    if (files) throw new TypeError("FormData: a file name goes with a Blob or File");
    return [name, String(value)];
  }

  let entriesOf;

  class FormData {
    #entries = [];

    static {
      entriesOf = (f) => f.#entries;
    }

    constructor(form) {
      if (form !== undefined) throw new TypeError("FormData: there are no form elements; append the fields");
    }

    append(name, value, filename) {
      this.#entries.push(formEntry(name, value, filename, arguments.length > 2));
    }

    set(name, value, filename) {
      const e = formEntry(name, value, filename, arguments.length > 2);
      const i = this.#entries.findIndex(([k]) => k === e[0]);
      if (i < 0) {
        this.#entries.push(e);
        return;
      }
      this.#entries[i] = e;
      this.#entries = this.#entries.filter(([k], j) => j <= i || k !== e[0]);
    }

    get(name) {
      name = String(name);
      return this.#entries.find(([k]) => k === name)?.[1] ?? null;
    }

    getAll(name) {
      name = String(name);
      return this.#entries.filter(([k]) => k === name).map(([, v]) => v);
    }

    has(name) {
      name = String(name);
      return this.#entries.some(([k]) => k === name);
    }

    delete(name) {
      name = String(name);
      this.#entries = this.#entries.filter(([k]) => k !== name);
    }

    *entries() {
      for (const [k, v] of this.#entries.slice()) yield [k, v];
    }

    *keys() {
      for (const [k] of this.entries()) yield k;
    }

    *values() {
      for (const [, v] of this.entries()) yield v;
    }

    forEach(fn, self) {
      for (const [k, v] of this.entries()) fn.call(self, v, k, this);
    }

    [Symbol.iterator]() {
      return this.entries();
    }

    get [Symbol.toStringTag]() {
      return "FormData";
    }
  }

  // multipart is FormData as multipart/form-data (RFC 7578, as browsers
  // send it): its bytes and the type with the boundary.
  function multipart(form) {
    const lines = (s) => s.replace(/\r\n|\r|\n/g, "\r\n");
    const quoted = (s) => lines(s).replace(/"/g, "%22").replace(/\r/g, "%0D").replace(/\n/g, "%0A");
    const parts = [];
    for (const [name, value] of entriesOf(form)) {
      let head = `Content-Disposition: form-data; name="${quoted(name)}"`;
      let body;
      if (typeof value === "string") {
        body = utf8.encode(lines(value));
      } else {
        head += `; filename="${quoted(value.name)}"\r\nContent-Type: ${value.type || "application/octet-stream"}`;
        body = bytesOf(value);
      }
      parts.push([utf8.encode(head + "\r\n\r\n"), body]);
    }
    // 16 random characters, as browsers: a part holding them is not
    // looked for.
    const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
    let boundary = "----KarioFormBoundary";
    for (let i = 0; i < 16; i++) boundary += alphabet[Math.floor(Math.random() * alphabet.length)];
    const out = [];
    for (const [h, b] of parts) out.push(utf8.encode(`--${boundary}\r\n`), h, b, utf8.encode("\r\n"));
    out.push(utf8.encode(`--${boundary}--\r\n`));
    return { bytes: concat(out, out.reduce((n, p) => n + p.byteLength, 0)), type: `multipart/form-data; boundary=${boundary}` };
  }

  // extract is a body as bytes with the type it implies (null for none),
  // or a stream as it is.
  function extract(body, who) {
    if (body === undefined || body === null) return { bytes: null, type: null };
    if (typeof body === "string") return { bytes: utf8.encode(body), type: "text/plain;charset=UTF-8" };
    if (body instanceof ArrayBuffer || ArrayBuffer.isView(body)) return { bytes: copy(body), type: null };
    if (body instanceof Blob) return { bytes: bytesOf(body), type: body.type || null };
    if (body instanceof FormData) return multipart(body);
    if (isStream(body)) {
      if (body.locked) throw new TypeError(`${who}: the body's stream is locked (read already, or by another reader)`);
      return { bytes: null, type: null, stream: body };
    }
    throw new TypeError(`${who}: the body is a string, an ArrayBuffer, a typed array, a Blob, FormData or a ReadableStream`);
  }

  // Body is what Request and Response read their bodies with: whole as
  // often as asked (as fetch's), or once as a stream (then not whole). A
  // body given as a stream is read whole once, its bytes kept.
  let bodyInit, bodyBytes, bodySource;

  class Body {
    #bytes = null;
    #source = null;
    #whole = null;
    #stream = null;
    #used = false;

    static {
      bodyInit = (self, bytes, source) => {
        self.#bytes = bytes;
        self.#source = source ?? null;
      };
      bodyBytes = (self) => self.#bytes;
      bodySource = (self) => self.#source;
    }

    get body() {
      if (this.#source) return this.#source;
      if (!this.#bytes) return null;
      return (this.#stream ??= streamOf(this.#bytes, () => (this.#used = true)));
    }

    get bodyUsed() {
      return this.#used || !!this.#source?.locked;
    }

    async #all() {
      if (this.#whole) return this.#whole;
      if (this.bodyUsed) throw new TypeError("the body was read as a stream");
      if (this.#source) return (this.#whole = readAll(this.#source));
      return this.#bytes ?? new Uint8Array(0);
    }

    async text() {
      return text(await this.#all());
    }

    async json() {
      return JSON.parse(text(await this.#all()));
    }

    async arrayBuffer() {
      return (await this.#all()).slice().buffer;
    }

    async bytes() {
      return (await this.#all()).slice();
    }

    async blob() {
      return new Blob([await this.#all()], { type: this.headers.get("content-type") ?? "" });
    }
  }

  // ---- Request ----

  let requestInit;

  class Request extends Body {
    #url;
    #method;
    #headers;
    #signal;
    #credentials;
    #timeout;

    constructor(input, init) {
      super();
      const base = input instanceof Request ? input : null;
      if (init !== undefined && init !== null && typeof init !== "object") throw new TypeError("Request: the options are an object");
      init ??= {};
      this.#url = base ? base.url : String(input);
      let method = init.method ?? base?.method ?? "GET";
      method = typeof method === "string" ? METHODS.find((m) => m === method.toUpperCase()) : undefined;
      if (!method) throw new TypeError("Request: method is one of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS");
      this.#method = method;
      this.#headers = new Headers(init.headers ?? base?.headers);
      let bytes = base ? bodyBytes(base) : null;
      let source = base ? bodySource(base) : null;
      if (init.body !== undefined && init.body !== null) {
        const b = extract(init.body, "Request");
        bytes = b.bytes;
        source = b.stream ?? null;
        if (b.type && !this.#headers.has("content-type")) this.#headers.set("content-type", b.type);
      }
      if ((bytes || source) && (method === "GET" || method === "HEAD")) throw new TypeError("Request: a GET or HEAD request has no body");
      if (source && init.body && init.duplex !== "half") throw new TypeError('Request: a body that is a stream needs duplex: "half"');
      bodyInit(this, bytes, source);
      const signal = init.signal !== undefined ? init.signal : base?.signal;
      if (signal !== undefined && signal !== null && !(signal instanceof AbortSignal)) throw new TypeError("Request: signal is an AbortSignal");
      this.#signal = signal ?? undefined;
      const credentials = init.credentials ?? base?.credentials ?? "same-origin";
      if (!CREDENTIALS.includes(credentials)) throw new TypeError('Request: credentials is "omit", "same-origin" or "include"');
      this.#credentials = credentials;
      this.#timeout = init.timeout ?? base?.timeout;
    }

    get url() {
      return this.#url;
    }
    get method() {
      return this.#method;
    }
    get headers() {
      return this.#headers;
    }
    get signal() {
      return (this.#signal ??= new AbortController().signal);
    }
    get credentials() {
      return this.#credentials;
    }
    /** Kario's: fetch's timeout in ms (undefined: fetch's default). */
    get timeout() {
      return this.#timeout;
    }

    // requestInit is what fetch hands the runtime for a request.
    static {
      requestInit = (r) => {
        const out = { method: r.#method, headers: record(r.#headers), credentials: r.#credentials };
        const bytes = bodyBytes(r);
        const source = bodySource(r);
        if (bytes) out.body = bytes;
        if (source) {
          out.body = source;
          out.duplex = "half";
        }
        if (r.#signal) out.signal = r.#signal;
        if (r.#timeout !== undefined) out.timeout = r.#timeout;
        return out;
      };
    }

    get duplex() {
      return "half";
    }

    clone() {
      if (this.bodyUsed) throw new TypeError("Request.clone: the body was read");
      if (bodySource(this)) throw new TypeError("Request.clone: a body that is a stream is not cloned");
      return new Request(this);
    }

    get [Symbol.toStringTag]() {
      return "Request";
    }
  }

  // ---- Response ----

  let fromNetwork;

  class Response extends Body {
    #status;
    #statusText;
    #headers;
    #url = "";
    #redirected = false;
    #type = "default";
    #net = null;

    constructor(body, init) {
      super();
      if (body === NET) {
        this.#net = init;
        this.#status = init.status;
        this.#statusText = "";
        this.#headers = netHeaders(init.headers);
        this.#url = init.url;
        this.#redirected = !!init.redirected;
        this.#type = "basic";
        return;
      }
      if (init !== undefined && init !== null && typeof init !== "object") throw new TypeError("Response: the options are an object");
      init ??= {};
      const status = init.status === undefined ? 200 : Number(init.status);
      if (!Number.isInteger(status) || status < 200 || status > 599) throw new RangeError("Response: status is from 200 to 599");
      const statusText = init.statusText === undefined ? "" : String(init.statusText);
      if (/[\0\r\n]/.test(statusText)) throw new TypeError("Response: statusText has a line break or NUL");
      this.#status = status;
      this.#statusText = statusText;
      this.#headers = new Headers(init.headers);
      const b = extract(body, "Response");
      if ((b.bytes || b.stream) && [204, 205, 304].includes(status)) throw new TypeError(`Response: a ${status} response has no body`);
      if (b.type && !this.#headers.has("content-type")) this.#headers.set("content-type", b.type);
      bodyInit(this, b.bytes, b.stream);
    }

    static json(data, init) {
      const body = JSON.stringify(data);
      if (body === undefined) throw new TypeError("Response.json: the data has no JSON");
      const headers = new Headers(init?.headers);
      if (!headers.has("content-type")) headers.set("content-type", "application/json");
      return new Response(body, { ...init, headers });
    }

    static redirect(url, status = 302) {
      if (![301, 302, 303, 307, 308].includes(status)) throw new RangeError("Response.redirect: status is 301, 302, 303, 307 or 308");
      return new Response(null, { status, headers: { location: String(url) } });
    }

    static {
      fromNetwork = (res) => new Response(NET, res);
    }

    get ok() {
      return this.#status >= 200 && this.#status <= 299;
    }
    get status() {
      return this.#status;
    }
    get statusText() {
      return this.#statusText;
    }
    get headers() {
      return this.#headers;
    }
    get url() {
      return this.#url;
    }
    get redirected() {
      return this.#redirected;
    }
    get type() {
      return this.#type;
    }
    get body() {
      return this.#net ? (this.#netBody ??= netStream(this.#net.body)) : super.body;
    }
    #netBody = null;
    get bodyUsed() {
      return this.#net ? this.#net.body.locked : super.bodyUsed;
    }
    text() {
      return this.#net ? this.#net.text() : super.text();
    }
    json() {
      return this.#net ? this.#net.json() : super.json();
    }
    async arrayBuffer() {
      return this.#net ? this.#net.arrayBuffer() : super.arrayBuffer();
    }
    async bytes() {
      return this.#net ? new Uint8Array(await this.#net.arrayBuffer()) : super.bytes();
    }
    async blob() {
      return this.#net ? new Blob([await this.#net.arrayBuffer()], { type: this.#headers.get("content-type") ?? "" }) : super.blob();
    }

    clone() {
      if (this.#net) throw new TypeError("Response.clone: a response from the network is not cloned (text() and the like can be read again)");
      if (this.bodyUsed) throw new TypeError("Response.clone: the body was read");
      if (bodySource(this)) throw new TypeError("Response.clone: a body that is a stream is not cloned");
      const bytes = bodyBytes(this);
      return new Response(bytes ? bytes.slice() : null, { status: this.#status, statusText: this.#statusText, headers: this.#headers });
    }

    get [Symbol.toStringTag]() {
      return "Response";
    }
  }
  // ---- fetch ----

  // fetch takes a URL or a Request, with options whose headers may be
  // Headers and whose body may also be a Blob, FormData or a stream (with
  // duplex: "half", as the web's fetch wants); what the runtime's fetch
  // checks (the URL, method, headers, the body's size, timeout, signal) it
  // checks with its own words. A stream is read by the runtime as it
  // sends, and cancelled when the request fails or is aborted.
  async function fetch(input, init) {
    let url = input;
    let opts = {};
    if (input instanceof Request) {
      url = input.url;
      opts = requestInit(input);
    }
    if (init !== undefined && init !== null) {
      if (typeof init !== "object") return fromNetwork(await coreFetch(url, init));
      opts = { ...opts, ...init };
      const h = init.headers;
      if (h instanceof Headers || (h !== null && typeof h === "object" && typeof h[Symbol.iterator] === "function")) opts.headers = record(new Headers(h));
      const b = init.body;
      if (b instanceof Blob || b instanceof FormData) {
        const { bytes, type } = extract(b, "fetch");
        opts.body = bytes;
        if (type && !Object.keys(opts.headers ?? {}).some((k) => k.toLowerCase() === "content-type")) opts.headers = { ...opts.headers, "Content-Type": type };
      }
    }
    let pull;
    if (isStream(opts.body)) {
      const method = typeof opts.method === "string" ? opts.method.toUpperCase() : "GET";
      if (method === "GET" || method === "HEAD") throw new TypeError("fetch: a GET or HEAD request has no body");
      if (opts.duplex !== "half") throw new TypeError('fetch: a body that is a stream needs duplex: "half"');
      if (opts.body.locked) throw new TypeError("fetch: the body's stream is locked (read already, or by another reader)");
      opts.body = pull = pullOf(opts.body.getReader());
    }
    delete opts.duplex;
    let res;
    try {
      res = await coreFetch(url, opts);
    } catch (e) {
      pull?.cancel(e);
      throw e;
    }
    return fromNetwork(res);
  }

  return { Blob, File, FormData, Headers, Request, Response, ReadableStream: Stream, fetch };
});
