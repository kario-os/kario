// Blob, File, FormData, Headers, Request and Response for an app's fetch
// (22-developer-api.md §9.4), subsets of the web's, and fetch taking them.
// One file for both engines: kario-jsrt compiles it the first time an app
// uses one of them or fetch (jsrt/src/net.c), Node.js at start (net.mjs).
// A script whose value is a function of the runtime's own fetch, which
// takes headers as an object of strings and a body as a string or bytes
// and gives a response whose headers are an object, whose body is a
// reader, with text(), json() and arrayBuffer().
(function (coreFetch) {
  "use strict";

  const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];
  const CREDENTIALS = ["omit", "same-origin", "include"];
  const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
  const CHUNK = 64 << 10;
  const utf8 = new TextEncoder();
  const text = (b) => new TextDecoder().decode(b);
  // NET marks a Response made from a fetch's (only fetch has it).
  const NET = {};

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

  // reader is a body read as it comes, in chunks of at most 64 KB, as
  // fetch's response body is (a subset of the web's ReadableStream).
  function reader(bytes) {
    let at = 0;
    let done = !bytes;
    let locked = false;
    const r = {
      async read() {
        locked = true;
        if (done || at >= bytes.byteLength) {
          done = true;
          return { done: true, value: undefined };
        }
        const value = bytes.slice(at, at + CHUNK);
        at += value.byteLength;
        return { done: false, value };
      },
      next() {
        return this.read();
      },
      async cancel() {
        locked = true;
        done = true;
      },
      async return() {
        await this.cancel();
        return { done: true, value: undefined };
      },
      getReader() {
        return this;
      },
      releaseLock() {},
      [Symbol.asyncIterator]() {
        return this;
      },
      get locked() {
        return locked;
      },
      get [Symbol.toStringTag]() {
        return "ReadableStream";
      },
    };
    return r;
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
      return reader(this.#bytes);
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

  // extract is a body as bytes with the type it implies (null for none).
  function extract(body, who) {
    if (body === undefined || body === null) return { bytes: null, type: null };
    if (typeof body === "string") return { bytes: utf8.encode(body), type: "text/plain;charset=UTF-8" };
    if (body instanceof ArrayBuffer || ArrayBuffer.isView(body)) return { bytes: copy(body), type: null };
    if (body instanceof Blob) return { bytes: bytesOf(body), type: body.type || null };
    if (body instanceof FormData) return multipart(body);
    throw new TypeError(`${who}: the body is a string, an ArrayBuffer, a typed array, a Blob or FormData`);
  }

  // Body is what Request and Response read their bodies with: whole as
  // often as asked (as fetch's), or once as a stream (then not whole).
  let bodyInit, bodyBytes;

  class Body {
    #bytes = null;
    #stream = null;

    static {
      bodyInit = (self, bytes) => {
        self.#bytes = bytes;
      };
      bodyBytes = (self) => self.#bytes;
    }

    get body() {
      if (!this.#bytes) return null;
      return (this.#stream ??= reader(this.#bytes));
    }

    get bodyUsed() {
      return !!this.#stream?.locked;
    }

    async #whole() {
      if (this.#stream?.locked) throw new TypeError("the body was read as a stream");
      return this.#bytes ?? new Uint8Array(0);
    }

    async text() {
      return text(await this.#whole());
    }

    async json() {
      return JSON.parse(text(await this.#whole()));
    }

    async arrayBuffer() {
      return (await this.#whole()).slice().buffer;
    }

    async bytes() {
      return (await this.#whole()).slice();
    }

    async blob() {
      return new Blob([await this.#whole()], { type: this.headers.get("content-type") ?? "" });
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
      if (init.body !== undefined && init.body !== null) {
        const b = extract(init.body, "Request");
        bytes = b.bytes;
        if (b.type && !this.#headers.has("content-type")) this.#headers.set("content-type", b.type);
      }
      if (bytes && (method === "GET" || method === "HEAD")) throw new TypeError("Request: a GET or HEAD request has no body");
      bodyInit(this, bytes);
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
        if (bytes) out.body = bytes;
        if (r.#signal) out.signal = r.#signal;
        if (r.#timeout !== undefined) out.timeout = r.#timeout;
        return out;
      };
    }

    clone() {
      if (this.bodyUsed) throw new TypeError("Request.clone: the body was read");
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
      if (b.bytes && [204, 205, 304].includes(status)) throw new TypeError(`Response: a ${status} response has no body`);
      if (b.type && !this.#headers.has("content-type")) this.#headers.set("content-type", b.type);
      bodyInit(this, b.bytes);
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
      return this.#net ? this.#net.body : super.body;
    }
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
      const bytes = bodyBytes(this);
      return new Response(bytes ? bytes.slice() : null, { status: this.#status, statusText: this.#statusText, headers: this.#headers });
    }

    get [Symbol.toStringTag]() {
      return "Response";
    }
  }
  // ---- fetch ----

  // fetch takes a URL or a Request, with options whose headers may be
  // Headers and whose body may also be a Blob or FormData; what the
  // runtime's fetch checks (the URL, method, headers, the body's size,
  // timeout, signal) it checks with its own words.
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
    return fromNetwork(await coreFetch(url, opts));
  }

  return { Blob, File, FormData, Headers, Request, Response, fetch };
});
