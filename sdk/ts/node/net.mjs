// fetch and WebSocket under Node.js (kario run, kario test), kario-jsrt's
// (jsrt/src/net.c, ws.c, wsframe.c) rules, limits and messages to the
// letter: the request goes out through io (Node.js's own fetch and sockets
// under kario run, the test's answers under kario test). Blob, File,
// FormData, Headers, Request and Response are web.js, the same file
// kario-jsrt runs.
import { createHash, randomBytes } from "node:crypto";
import fs from "node:fs";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const webJS = new URL("./web.js", import.meta.url);
const webAPI = vm.runInThisContext(fs.readFileSync(webJS, "utf8"), { filename: fileURLToPath(webJS) });

const FETCH_MAX_URL = 8192;
const FETCH_MAX_SEND = 4 << 20;
const FETCH_MAX_BODY = 8 << 20;
const FETCH_MAX_HEAD = 64 << 10;
const FETCH_MAX_HEADERS = 64;
const FETCH_MAX_RUNNING = 8;
const FETCH_MAX_REDIRECTS = 5;
const FETCH_TIMEOUT_MS = 30000;
const FETCH_TIMEOUT_MAX_MS = 120000;
const FETCH_CHUNK = 64 << 10;
const FETCH_METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"];
const FETCH_FORBIDDEN = new Set(["host", "content-length", "transfer-encoding", "connection", "expect", "upgrade", "te", "trailer", "keep-alive"]);
const FETCH_FOLLOWS = new Set(["GET", "HEAD", "POST"]);
const CREDENTIALS = new Set(["omit", "same-origin", "include"]);
// An address of 127.0.0.0/8 in dotted decimal, the one form no resolver
// takes for a name.
const LOOPBACK4 = /^127(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

const WS_MAX_OPEN = 4;
const WS_MAX_MESSAGE = 4 << 20;
const WS_MAX_SEND = 4 << 20;
const WS_MAX_BUFFERED = 16 << 20;
const WS_HANDSHAKE_MS = 30000;
const WS_CLOSING_MS = 5000;
const WS_GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
// The heartbeat intervals net.keepalive takes (ui.KeepaliveMinSec, MaxSec).
const KEEPALIVE_MIN_S = 60;
const KEEPALIVE_MAX_S = 3600;

const fetchError = (why) => new TypeError("fetch: " + why);
// define sets o's own property k, also when k is __proto__.
const define = (o, k, v) => Object.defineProperty(o, k, { value: v, enumerable: true, writable: true, configurable: true });
// Node.js's parser refused a NUL in the response's head (its error holds
// the bytes from the one it refused): kario-jsrt's words for it, libcurl's.
const nulByte = (e) => e?.name === "HTTPParserError" && String(e.data ?? "").charCodeAt(0) === 0;
const utf8 = new TextEncoder();
// A message's text as it came, a leading U+FEFF included.
const asText = (b) => new TextDecoder("utf-8", { ignoreBOM: true }).decode(b);

function urlCheck(url, ws) {
  if (url.length > FETCH_MAX_URL) return "the URL is too long";
  if (/[\x00-\x20\x7f]/.test(url)) return "the URL has spaces or control characters";
  const [s, p] = ws ? ["wss://", "ws://"] : ["https://", "http://"];
  const lower = url.slice(0, s.length).toLowerCase();
  const secure = lower.startsWith(s);
  if (!secure && !lower.startsWith(p)) return ws ? "only wss:// URLs, and ws:// to the device's own loopback" : "only https:// URLs, and http:// to the device's own loopback";
  if (ws && url.includes("#")) return "a WebSocket URL has no fragment";
  const rest = url.slice(secure ? s.length : p.length);
  const auth = rest.slice(0, rest.search(/[/?#]|$/));
  if (auth.includes("@")) return "a URL with a user name or password";
  if (!auth) return "a URL without a host";
  if (secure) return "";
  let host;
  if (auth.startsWith("[")) {
    const end = auth.indexOf("]");
    if (end < 0) return "a URL with a bad host";
    host = auth.slice(1, end);
  } else {
    host = auth.split(":")[0];
  }
  host = host.toLowerCase();
  if (!host || host.length >= 256) return "a URL with a bad host";
  if (host === "localhost" || LOOPBACK4.test(host) || host === "::1") return "";
  return ws
    ? "ws:// only to the device's own loopback (localhost, 127.0.0.1, [::1]); use wss://"
    : "http:// only to the device's own loopback (localhost, 127.0.0.1, [::1]); use https://";
}

// headersOf checks an app's headers as kario-jsrt does; the reason, or "".
function headersOf(obj, out, ws) {
  if (obj === undefined || obj === null) return "";
  if (typeof obj !== "object" || Array.isArray(obj)) return "headers is an object of strings";
  const entries = Object.entries(obj);
  if (entries.length > FETCH_MAX_HEADERS) return "more than 64 headers";
  for (const [name, value] of entries) {
    if (typeof value !== "string") return "headers is an object of strings";
    if (!/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(name)) return "a header name that is not a token";
    if (/[\r\n]/.test(value) || name.length + value.length > 8192) return "a header value with a line break, or over 8 KB";
    if (ws && name.toLowerCase().startsWith("sec-websocket-")) return "a header of the handshake itself (Sec-WebSocket-…; protocols go in the second argument)";
    if (FETCH_FORBIDDEN.has(name.toLowerCase())) return "a header the network sets itself (Host, Content-Length, Connection…)";
    define(out, name, value);
  }
  return "";
}

// fetchRequest checks fetch's arguments as kario-jsrt does, in the same
// order, and gives what io.fetch sends; a TypeError when they may not go.
function fetchRequest(url, init) {
  if (typeof url !== "string") throw fetchError("the URL is a string");
  const bad = urlCheck(url, false);
  if (bad) throw fetchError(bad);
  if (init !== undefined && init !== null && typeof init !== "object") throw fetchError("the options are an object");
  init ??= {};
  let method = "GET";
  if (init.method !== undefined) {
    method = typeof init.method === "string" ? FETCH_METHODS.find((m) => m === init.method.toUpperCase()) : undefined;
    if (!method) throw fetchError("method is one of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS");
  }
  let timeout = FETCH_TIMEOUT_MS;
  if (init.timeout !== undefined) {
    const t = Number(init.timeout);
    if (!(t >= 1 && t <= FETCH_TIMEOUT_MAX_MS)) throw fetchError("timeout is from 1 to 120000 ms");
    timeout = Math.trunc(t);
  }
  const headers = {};
  const hbad = headersOf(init.headers, headers, false);
  if (hbad) throw fetchError(hbad);
  const typed = Object.keys(headers).some((n) => n.toLowerCase() === "content-type");
  let body;
  let stream;
  if (init.body !== undefined && init.body !== null) {
    let text = false;
    if (typeof init.body === "string") {
      body = utf8.encode(init.body);
      text = true;
    } else if (init.body instanceof ArrayBuffer) {
      body = new Uint8Array(init.body.slice(0));
    } else if (ArrayBuffer.isView(init.body)) {
      body = new Uint8Array(init.body.buffer.slice(init.body.byteOffset, init.body.byteOffset + init.body.byteLength));
    } else if (typeof init.body === "object" && !Array.isArray(init.body) && typeof init.body.read === "function") {
      stream = init.body;
    } else {
      throw fetchError("body is a string, an ArrayBuffer or a typed array");
    }
    if (body?.byteLength > FETCH_MAX_SEND) throw fetchError("the body is over 4 MB");
    if (text && !typed) headers["Content-Type"] = "text/plain;charset=UTF-8";
  }
  if (init.credentials !== undefined && !CREDENTIALS.has(init.credentials)) throw fetchError('credentials is "omit", "same-origin" or "include"');
  let signal;
  if (init.signal !== undefined && init.signal !== null) {
    if (!(init.signal instanceof AbortSignal)) throw fetchError("signal is an AbortSignal");
    signal = init.signal;
  }
  if ((body || stream) && (method === "GET" || method === "HEAD")) throw fetchError("a GET or HEAD request has no body");
  return { url, method, headers, body, stream, timeout, cookies: init.credentials === "include", signal };
}

// ---- cookies ----

// cookieJar is the app's cookies for credentials: "include" (RFC 6265 as an
// app needs it: Domain, Path, Secure, Expires and Max-Age, the __Secure-
// and __Host- prefixes), as the image's libcurl (8.19) keeps them, in the
// Netscape format it writes, so kario-jsrt and Node.js read each other's
// jar. A session cookie lives as long as the process.
export function cookieJar(store) {
  let jar;
  let made = 0;
  const now = () => Math.floor(Date.now() / 1000);
  const load = () => {
    if (jar) return jar;
    jar = [];
    for (let line of (store?.load() ?? "").split("\n")) {
      let httpOnly = false;
      if (line.startsWith("#HttpOnly_")) {
        httpOnly = true;
        line = line.slice(10);
      } else if (line.startsWith("#") || !line.trim()) {
        continue;
      }
      const f = line.split("\t");
      if (f.length < 7) continue;
      const expires = Number(f[4]);
      if (!expires || expires < now()) continue;
      jar.push({
        domain: f[0].replace(/^\./, "").toLowerCase(),
        hostOnly: f[1] !== "TRUE",
        path: f[2],
        secure: f[3] === "TRUE",
        expires,
        name: f[5],
        value: f.slice(6).join("\t"),
        httpOnly,
        made: ++made,
      });
    }
    return jar;
  };
  const save = () => {
    if (!store) return;
    const lines = ["# Netscape HTTP Cookie File", ""];
    for (const c of jar) {
      if (!c.expires) continue;
      lines.push(`${c.httpOnly ? "#HttpOnly_" : ""}${c.hostOnly ? c.domain : "." + c.domain}\t${c.hostOnly ? "FALSE" : "TRUE"}\t${c.path}\t${c.secure ? "TRUE" : "FALSE"}\t${c.expires}\t${c.name}\t${c.value}`);
    }
    try {
      store.save(lines.join("\n") + "\n");
    } catch {
      // A jar that cannot be written: the cookies stay in memory.
    }
  };
  const domainMatch = (host, c) => (c.hostOnly ? host === c.domain : host === c.domain || host.endsWith("." + c.domain));
  const pathMatch = (path, cp) => path === cp || (path.startsWith(cp) && (cp.endsWith("/") || path[cp.length] === "/"));
  const hostOf = (u) => u.hostname.replace(/^\[|\]$/g, "").toLowerCase();
  // libcurl's secure context: https://, or the loopback by its usual names.
  const secureContext = (u) => u.protocol === "https:" || ["localhost", "127.0.0.1", "::1"].includes(hostOf(u));
  return {
    header(url) {
      const u = new URL(url);
      const host = hostOf(u);
      const t = now();
      const secure = secureContext(u);
      const got = load().filter((c) => (!c.expires || c.expires > t) && domainMatch(host, c) && pathMatch(u.pathname || "/", c.path) && (!c.secure || secure));
      // libcurl's order: the longer path, domain and name, then the newer.
      got.sort((a, b) => b.path.length - a.path.length || b.domain.length - a.domain.length || b.name.length - a.name.length || b.made - a.made);
      return got.map((c) => `${c.name}=${c.value}`).join("; ");
    },
    store(url, lines) {
      if (!lines?.length) return;
      const u = new URL(url);
      const host = hostOf(u);
      const secure = secureContext(u);
      const dir = u.pathname.lastIndexOf("/") > 0 ? u.pathname.slice(0, u.pathname.lastIndexOf("/")) : "/";
      let changed = false;
      for (const line of lines) {
        const [pair, ...attrs] = line.split(";");
        const eq = pair.indexOf("=");
        if (eq <= 0) continue;
        const c = { name: pair.slice(0, eq).trim(), value: pair.slice(eq + 1).trim(), domain: host, hostOnly: true, path: dir, secure: false, expires: 0, httpOnly: false };
        let gone = c.value.includes("\t");
        let maxAge;
        for (const a of attrs) {
          const i = a.indexOf("=");
          const k = (i < 0 ? a : a.slice(0, i)).trim().toLowerCase();
          const v = i < 0 ? "" : a.slice(i + 1).trim();
          if (k === "domain" && v) {
            const d = v.replace(/^\./, "").toLowerCase();
            if (!(host === d || host.endsWith("." + d)) || (!d.includes(".") && d !== host)) gone = true;
            c.domain = d;
            c.hostOnly = false;
          } else if (k === "path" && v.startsWith("/")) {
            c.path = v;
          } else if (k === "secure") {
            c.secure = true;
            gone ||= !secure;
          } else if (k === "httponly") {
            c.httpOnly = true;
          } else if (k === "max-age" && /^-?\d+$/.test(v)) {
            maxAge = Number(v);
          } else if (k === "expires" && maxAge === undefined) {
            const ms = Date.parse(v);
            if (!Number.isNaN(ms)) c.expires = Math.max(1, Math.floor(ms / 1000));
          }
        }
        if (c.name.startsWith("__Secure-")) gone ||= !c.secure;
        if (c.name.startsWith("__Host-")) gone ||= !c.secure || c.path !== "/" || !c.hostOnly;
        if (gone || !c.name) continue;
        if (maxAge !== undefined) c.expires = maxAge <= 0 ? 1 : now() + maxAge;
        const j = load();
        // A cookie from a non-secure origin may not overlay a secure one
        // (the same name and domain, a path under the first segment of its).
        const top = (p) => p.slice(0, p.indexOf("/", 1) < 0 ? p.length : p.indexOf("/", 1));
        if (!secure && !c.secure && j.some((x) => x.secure && x.name === c.name && x.domain === c.domain && c.path.startsWith(top(x.path)))) continue;
        const i = j.findIndex((x) => x.name === c.name && x.domain === c.domain && x.path === c.path);
        c.made = i >= 0 ? j[i].made : ++made;
        if (i >= 0) j.splice(i, 1);
        if (!c.expires || c.expires > now()) j.push(c);
        changed = true;
      }
      if (changed) save();
    },
  };
}

// ---- fetch ----

/**
 * nodeFetch is io.fetch under kario run: Node.js's own fetch with
 * kario-jsrt's redirects (GET, HEAD and POST follow at most 5, only to
 * https://; POST becomes GET on 301, 302 and 303; a body from a stream
 * goes once, its 3xx the app's) and cookies. It settles at the response's
 * headers; next() reads the body, cancel() stops it.
 */
export async function nodeFetch(req) {
  let { url, method, body } = req;
  const ac = new AbortController();
  req.onCancel?.(() => ac.abort());
  const failed = (e) => fetchError(nulByte(e?.cause) ? "Nul byte in header" : (e?.cause?.message ?? e?.message ?? String(e)));
  const streamed = !!body && typeof body.read === "function";
  if (streamed) {
    const src = body;
    body = new ReadableStream(
      {
        async pull(c) {
          const r = await src.read();
          if (r.done) c.close();
          else c.enqueue(r.value);
        },
      },
      { highWaterMark: 0 },
    );
  }
  for (let redirects = 0; ; redirects++) {
    const headers = { ...req.headers };
    const cookie = req.jar?.header(url);
    if (cookie) headers.Cookie = cookie;
    let res;
    try {
      res = await globalThis.fetch(url, { method, headers, body, redirect: "manual", signal: ac.signal, ...(streamed ? { duplex: "half" } : {}) });
    } catch (e) {
      throw failed(e);
    }
    req.jar?.store(url, res.headers.getSetCookie?.() ?? []);
    const location = res.headers.get("location");
    if (!streamed && FETCH_FOLLOWS.has(method) && [301, 302, 303, 307, 308].includes(res.status) && location) {
      await res.body?.cancel();
      if (redirects >= FETCH_MAX_REDIRECTS) throw fetchError("more than 5 redirects");
      const next = new URL(location, url);
      if (next.protocol !== "https:") throw fetchError("a redirect away from https:// (only https:// redirects are followed)");
      if (method === "POST" && [301, 302, 303].includes(res.status)) {
        method = "GET";
        body = undefined;
      }
      url = next.href;
      continue;
    }
    const hs = {};
    let size = 0;
    for (const [k, v] of res.headers) {
      define(hs, k, Object.hasOwn(hs, k) ? hs[k] + ", " + v : v);
      size += k.length + v.length + 4;
    }
    if (size > FETCH_MAX_HEAD) {
      ac.abort();
      throw fetchError("the response's headers are over 64 KB");
    }
    const reader = res.body?.getReader();
    return {
      status: res.status,
      url,
      redirected: redirects > 0,
      headers: hs,
      async next() {
        try {
          return reader ? await reader.read() : { done: true };
        } catch (e) {
          throw failed(e);
        }
      },
      cancel: () => ac.abort(),
    };
  }
}

// transferOf is what io.fetch gave as a transfer: a whole answer (kario
// test's) read as one chunk, or a stream of them.
function transferOf(got) {
  if (typeof got.next === "function") return got;
  const b = got.body;
  let it;
  if (b !== undefined && b !== null && typeof b !== "string" && !(b instanceof Uint8Array) && typeof b[Symbol.asyncIterator] === "function") it = b[Symbol.asyncIterator]();
  let sent = false;
  return {
    status: got.status ?? 200,
    url: got.url,
    redirected: !!got.redirected,
    headers: got.headers ?? {},
    async next() {
      if (it) {
        const r = await it.next();
        return r.done ? r : { done: false, value: typeof r.value === "string" ? utf8.encode(r.value) : new Uint8Array(r.value) };
      }
      if (sent || b === undefined || b === null) return { done: true };
      sent = true;
      return { done: false, value: typeof b === "string" ? utf8.encode(b) : new Uint8Array(b) };
    },
    cancel() {
      it?.return?.();
    },
  };
}

// deadline is a timeout that starts over with restart(): its promise
// rejects with fetch's TypeError for why() once ms pass without one.
function deadline(ms, why, onTimeout) {
  let t;
  let reject;
  const promise = new Promise((_, r) => {
    reject = r;
  });
  promise.catch(() => {});
  const restart = () => {
    clearTimeout(t);
    t = setTimeout(() => {
      onTimeout?.();
      reject(fetchError(why()));
    }, ms);
  };
  restart();
  return { promise, restart, clear: () => clearTimeout(t) };
}

// timed races p against ms: why (a TypeError) when it ran out.
function timed(p, ms, why, onTimeout) {
  let t;
  return Promise.race([
    p,
    new Promise((_, reject) => {
      t = setTimeout(() => {
        onTimeout?.();
        reject(fetchError(why));
      }, ms);
    }),
  ]).finally(() => clearTimeout(t));
}

// createNet is fetch and WebSocket on io (runtime.mjs); own(cap, args) is a
// call of the runtime's own, resolving with the reply's data and the
// handles of its files ({ data, hs }).
export function createNet(io, env, own) {
  const jar = cookieJar(io.cookies);
  let fetching = 0;
  // A response nobody holds any more stops what it was fetching.
  const dropped = new FinalizationRegistry((stop) => stop());

  async function fetch(url, init) {
    if (!env.net) throw fetchError('the app\'s manifest does not ask for the "net" permission');
    const req = fetchRequest(url, init);
    if (req.signal?.aborted) throw req.signal.reason;
    if (fetching >= FETCH_MAX_RUNNING) throw fetchError("at most 8 requests at a time");
    fetching++;
    // What the timeout counts from: when the request went, or the last
    // bytes of a body sent from a stream.
    let start = Date.now();
    let reason;
    let ended = false;
    let t;
    const cancels = [];
    // A body from a stream: whether it has all gone, whether its next
    // chunk is awaited from the app.
    let sent = !req.stream;
    let awaited = false;
    const end = (why) => {
      if (ended) return;
      ended = true;
      fetching--;
      req.signal?.removeEventListener("abort", onAbort);
      t?.cancel();
      for (const c of cancels) c();
      if (!sent) req.stream.cancel(reason ?? why);
    };
    let rejectPending = () => {};
    function failWith(why) {
      if (reason !== undefined) return;
      reason = why;
      rejectPending(reason);
      end();
    }
    function onAbort() {
      failWith(req.signal.reason);
    }
    req.signal?.addEventListener("abort", onAbort);
    const aborted = new Promise((_, reject) => {
      rejectPending = reject;
    });
    aborted.catch(() => {});
    const out = { ...req, jar: req.cookies ? jar : undefined, onCancel: (f) => cancels.push(f) };
    delete out.signal;
    delete out.cookies;
    delete out.stream;
    const d = deadline(
      req.timeout,
      () => (awaited ? "no data from the body's stream for the timeout" : "timed out"),
      () => cancels.forEach((c) => c()),
    );
    let headed = false;
    const progress = () => {
      start = Date.now();
      if (!headed) d.restart();
    };
    if (req.stream) {
      // Read only as the network takes it, so no more of it waits than
      // Node.js buffers.
      out.body = {
        async read() {
          progress();
          awaited = true;
          let r;
          try {
            r = await req.stream.read();
          } catch (e) {
            failWith(e);
            throw e;
          } finally {
            awaited = false;
          }
          if (r.done) {
            sent = true;
            progress();
          }
          return r;
        },
      };
    }
    try {
      t = transferOf(await Promise.race([Promise.resolve(io.fetch(out)), d.promise, aborted]));
    } catch (e) {
      end(e);
      throw reason ?? e;
    } finally {
      headed = true;
      d.clear();
    }
    if (ended) {
      t.cancel();
      throw reason;
    }
    return response(t);

    function response(t) {
      let mode = 0;
      let whole;
      let waiting = false;
      let done = false;
      let carry;
      const status = t.status;
      const headers = {};
      for (const [k, v] of Object.entries(t.headers)) define(headers, k.toLowerCase(), String(v));
      const finish = () => {
        done = true;
        end();
      };
      // chunk is the next piece of the body, at most 64 KB.
      async function chunk(idle) {
        if (carry?.byteLength) {
          const c = carry.subarray(0, FETCH_CHUNK);
          carry = carry.subarray(c.byteLength);
          return { done: false, value: c.slice() };
        }
        for (;;) {
          const next = Promise.race([t.next(), aborted]);
          const r = idle ? await timed(next, req.timeout, "no data for the timeout while the body was read", () => t.cancel()) : await next;
          if (r.done) return r;
          const v = r.value instanceof Uint8Array ? r.value : new Uint8Array(r.value);
          if (!v.byteLength) continue;
          carry = v.subarray(Math.min(v.byteLength, FETCH_CHUNK));
          return { done: false, value: v.slice(0, FETCH_CHUNK) };
        }
      }
      const body = {
        async read() {
          if (mode === 1) throw fetchError("the body was read whole (text(), json(), arrayBuffer())");
          if (waiting) throw fetchError("a read of the body is waiting already");
          if (reason !== undefined) throw reason;
          if (done) return { done: true, value: undefined };
          mode = 2;
          waiting = true;
          try {
            const r = await chunk(true);
            if (r.done) finish();
            return r.done ? { done: true, value: undefined } : r;
          } catch (e) {
            finish();
            throw reason ?? e;
          } finally {
            waiting = false;
          }
        },
        next() {
          return this.read();
        },
        async cancel() {
          if (mode !== 1) {
            mode = 2;
            finish();
          }
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
          return mode !== 0;
        },
        get [Symbol.toStringTag]() {
          return "ReadableStream";
        },
      };
      async function collect() {
        const parts = [];
        let size = 0;
        const left = () => Math.max(1, req.timeout - (Date.now() - start));
        try {
          if (Number(headers["content-length"]) > FETCH_MAX_BODY) throw fetchError("the response is over 8 MB");
          for (;;) {
            const r = await timed(chunk(false), left(), "timed out", () => t.cancel());
            if (r.done) break;
            size += r.value.byteLength;
            if (size > FETCH_MAX_BODY) throw fetchError("the response is over 8 MB");
            parts.push(r.value);
          }
        } catch (e) {
          finish();
          throw reason ?? e;
        }
        finish();
        const all = new Uint8Array(size);
        let at = 0;
        for (const p of parts) {
          all.set(p, at);
          at += p.byteLength;
        }
        return all;
      }
      const readWhole = () => {
        if (mode === 2) return Promise.reject(fetchError("the body was read as a stream"));
        if (reason !== undefined && !whole) return Promise.reject(reason);
        mode = 1;
        whole ??= collect();
        return whole;
      };
      const res = {
        ok: status >= 200 && status <= 299,
        status,
        url: t.url ?? req.url,
        redirected: !!t.redirected,
        headers,
        body,
        text: async () => new TextDecoder().decode(await readWhole()),
        json: async () => JSON.parse(new TextDecoder().decode(await readWhole())),
        arrayBuffer: async () => (await readWhole()).slice().buffer,
      };
      // The body, not res: web.js's Response gives the app a stream that
      // holds only the body.
      dropped.register(body, () => {
        if (!waiting && !(whole && !done)) end();
      });
      return res;
    }
  }

  // ---- WebSocket ----

  let wsOpen = 0;

  function token(s) {
    return typeof s === "string" && /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(s);
  }

  function frame(op, payload) {
    const n = payload.byteLength;
    const h = n < 126 ? 2 : n < 65536 ? 4 : 10;
    const out = new Uint8Array(h + 4 + n);
    out[0] = 0x80 | op;
    if (n < 126) {
      out[1] = 0x80 | n;
    } else if (n < 65536) {
      out[1] = 0x80 | 126;
      out[2] = n >> 8;
      out[3] = n & 255;
    } else {
      out[1] = 0x80 | 127;
      new DataView(out.buffer).setBigUint64(2, BigInt(n));
    }
    const mask = randomBytes(4);
    out.set(mask, h);
    for (let i = 0; i < n; i++) out[h + 4 + i] = payload[i] ^ mask[i & 3];
    return out;
  }

  const closeCodeOK = (c) => (c >= 1000 && c <= 1003) || (c >= 1007 && c <= 1014) || (c >= 3000 && c <= 4999);
  const fatal = new TextDecoder("utf-8", { fatal: true });
  const validUTF8 = (b) => {
    try {
      fatal.decode(b);
      return true;
    } catch {
      return false;
    }
  };

  // reader is wsframe.c's ws_reader: feed() takes what came and gives the
  // events in it, an error ending it.
  function reader() {
    let chunks = [];
    let have = 0;
    let msg = 0;
    let parts = [];
    let size = 0;
    let over = false;
    // first is the first n bytes that came (n <= have), take removes them.
    const first = (n) => {
      if (chunks[0].length < n) chunks = [Buffer.concat(chunks)];
      return chunks[0].subarray(0, n);
    };
    const take = (n) => {
      const out = first(n);
      chunks[0] = chunks[0].subarray(n);
      if (!chunks[0].length) chunks.shift();
      have -= n;
      return out;
    };
    return function feed(bytes) {
      const evs = [];
      if (over) return evs;
      chunks.push(bytes);
      have += bytes.length;
      const fail = (code, why) => {
        over = true;
        evs.push({ kind: "error", code, why });
        return evs;
      };
      for (;;) {
        if (have < 2) return evs;
        let buf = first(2);
        let need = 2;
        const len7 = buf[1] & 0x7f;
        need += len7 === 126 ? 2 : len7 === 127 ? 8 : 0;
        if (buf[1] & 0x80) need += 4;
        if (have < need) return evs;
        buf = first(need);
        const fin = buf[0] >> 7;
        const op = buf[0] & 0x0f;
        if (buf[0] & 0x70) return fail(1002, "a frame with reserved bits set (no extension was agreed)");
        if (buf[1] & 0x80) return fail(1002, "a masked frame from the server");
        let len = len7;
        if (len7 === 126) len = (buf[2] << 8) | buf[3];
        else if (len7 === 127) {
          const big = new DataView(buf.buffer, buf.byteOffset + 2, 8).getBigUint64(0);
          if (big >> 63n) return fail(1002, "a frame length over 2^63");
          len = big > BigInt(Number.MAX_SAFE_INTEGER) ? Number.MAX_SAFE_INTEGER : Number(big);
        }
        if (op & 8) {
          if (op !== 8 && op !== 9 && op !== 10) return fail(1002, "a frame with an unknown opcode");
          if (!fin) return fail(1002, "a fragmented control frame");
          if (len > 125) return fail(1002, "a control frame over 125 bytes");
        } else {
          if (op === 0 && !msg) return fail(1002, "a continuation frame without a message");
          if ((op === 1 || op === 2) && msg) return fail(1002, "a new message before the last one ended");
          if (op !== 0 && op !== 1 && op !== 2) return fail(1002, "a frame with an unknown opcode");
          if (len > WS_MAX_MESSAGE - (op === 0 ? size : 0)) return fail(1009, "a message over the limit");
        }
        if (have < need + len) return evs;
        take(need);
        const payload = take(len).slice();
        if (op === 9 || op === 10) {
          evs.push({ kind: op === 9 ? "ping" : "pong", data: payload });
          continue;
        }
        if (op === 8) {
          if (len === 1) return fail(1002, "a close frame of one byte");
          let code = 1005;
          let why = new Uint8Array(0);
          if (len >= 2) {
            code = (payload[0] << 8) | payload[1];
            why = payload.subarray(2);
            if (!closeCodeOK(code)) return fail(1002, "a close code no endpoint sends");
            if (!validUTF8(why)) return fail(1007, "a close reason that is not UTF-8");
          }
          over = true;
          evs.push({ kind: "close", code, reason: asText(why) });
          return evs;
        }
        if (op !== 0) {
          msg = op;
          parts = [];
          size = 0;
        }
        parts.push(payload);
        size += len;
        if (!fin) continue;
        const all = parts.length === 1 ? parts[0] : Buffer.concat(parts);
        if (msg === 1 && !validUTF8(all)) return fail(1007, "a text message that is not UTF-8");
        evs.push({ kind: msg === 1 ? "text" : "binary", data: all });
        msg = 0;
        parts = [];
        size = 0;
      }
    };
  }

  const closeHandle = (h) => {
    try {
      h.close();
    } catch {
      // Already gone.
    }
  };
  let keepEvent = () => {};

  class WebSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;
    #state = 0;
    #url;
    #protocol = "";
    #listeners = [];
    #sock;
    #closing;
    #signal;
    #onAbort;
    // A keepalive WebSocket's heartbeat interval (0: another), and where the
    // Shell dials for it.
    #keepalive = 0;
    #keepHost;
    #keepPort;
    #secure = false;
    #attach;
    onopen = null;
    onmessage = null;
    onerror = null;
    onclose = null;

    constructor(url, protocols, options) {
      if (!env.net) throw new TypeError('WebSocket: the app\'s manifest does not ask for the "net" permission');
      if (typeof url !== "string") throw new TypeError("WebSocket: the URL is a string");
      const bad = urlCheck(url, true);
      if (bad) throw new SyntaxError("WebSocket: " + bad);
      const offered = [];
      let why = "";
      if (protocols !== undefined) {
        const list = Array.isArray(protocols) ? protocols : [protocols];
        if (list.length > 16) why = "at most 16 protocols";
        for (const p of list) {
          if (why) break;
          if (!token(p)) why = "a protocol that is not a token";
          else if (offered.some((o) => o.toLowerCase() === p.toLowerCase())) why = "a protocol given twice";
          else offered.push(p);
        }
      }
      const headers = {};
      if (!why && options !== undefined && options !== null && typeof options !== "object") why = "the options are an object";
      if (!why && options && typeof options === "object") {
        why = headersOf(options.headers, headers, true);
        if (!why && options.signal !== undefined && options.signal !== null) {
          if (!(options.signal instanceof AbortSignal)) why = "signal is an AbortSignal";
          else this.#signal = options.signal;
        }
        const k = options.keepalive;
        if (!why && k !== undefined && k !== null) {
          if (!env.keepalive) why = 'the app\'s manifest does not ask for the "keepalive" permission';
          else if (!Number.isInteger(k) || k < KEEPALIVE_MIN_S || k > KEEPALIVE_MAX_S) why = "keepalive is the heartbeat's interval in whole seconds, 60 to 3600";
          else this.#keepalive = k;
        }
      }
      if (!why && wsOpen >= WS_MAX_OPEN) why = "at most 4 WebSockets at a time";
      if (why) throw new TypeError("WebSocket: " + why);
      if (this.#signal?.aborted) throw this.#signal.reason;
      this.#url = url;
      wsOpen++;
      if (this.#signal) {
        this.#onAbort = () => this.#drop();
        this.#signal.addEventListener("abort", this.#onAbort);
      }
      this.#connect(offered, headers);
    }

    #fire(type, extra) {
      const ev = { type, target: this, ...extra };
      const h = this["on" + type];
      if (typeof h === "function") {
        try {
          h.call(this, ev);
        } catch (e) {
          io.log(`kario-node: uncaught exception in an event handler: ${e?.stack ?? e}`);
        }
      }
      for (const l of this.#listeners.filter((l) => l.type === type)) {
        if (l.once) this.removeEventListener(type, l.fn);
        try {
          l.fn.call(this, ev);
        } catch (e) {
          io.log(`kario-node: uncaught exception in an event listener: ${e?.stack ?? e}`);
        }
      }
    }

    #ended = false;
    #end(code, reason, clean, error) {
      if (this.#ended) return;
      this.#ended = true;
      clearTimeout(this.#closing);
      this.#signal?.removeEventListener("abort", this.#onAbort);
      this.#sock?.destroy();
      wsOpen--;
      if (this.#keepalive) WebSocket.#closed(this);
      if (error) this.#fire("error", { message: error });
      this.#state = 3;
      this.#fire("close", { code, reason, wasClean: clean });
    }

    // drop ends the connection without the closing handshake: at once for a
    // keepalive WebSocket still waiting for its socket.
    #drop() {
      if (this.#state === 3) return;
      this.#state = 2;
      WebSocket.#unqueue(this);
      setImmediate(() => this.#end(1006, "aborted", false));
    }

    #connect(offered, headers) {
      const u = new URL(this.#url);
      const secure = u.protocol === "wss:";
      const auth = this.#url.slice(secure ? 6 : 5).split(/[/?]/)[0];
      const rest = this.#url.slice((secure ? 6 : 5) + auth.length);
      const path = rest.startsWith("/") ? rest : "/" + rest;
      const key = randomBytes(16).toString("base64");
      const want = createHash("sha1").update(key + WS_GUID).digest("base64");
      const host = u.hostname.replace(/^\[|\]$/g, "");
      const port = Number(u.port) || (secure ? 443 : 80);
      // attach speaks the handshake and the framing on sock (connected: no
      // connect event to wait for).
      this.#attach = (sock, connected) => this.#speak(sock, connected, { secure, auth, path, key, want, offered, headers });
      if (this.#keepalive && io.adopt) {
        this.#secure = secure;
        this.#keepHost = host;
        this.#keepPort = port;
        WebSocket.#keepWait.push(this);
        WebSocket.#ask();
        return;
      }
      if (!io.connect) {
        setImmediate(() => this.#end(1006, "no WebSocket connection in this test", false, "no WebSocket connection in this test"));
        return;
      }
      this.#attach(io.connect({ secure, host, port }), false);
    }

    #speak(sock, connected, { secure, auth, path, key, want, offered, headers }) {
      this.#sock = sock;
      const timer = setTimeout(() => this.#end(1006, "the opening handshake timed out", false, "the opening handshake timed out"), WS_HANDSHAKE_MS);
      let head = Buffer.alloc(0);
      let feed;
      let peerClosed = false;
      const send = (b) => {
        if (!sock.destroyed) sock.write(b);
      };
      sock.on("error", (e) => {
        clearTimeout(timer);
        this.#end(1006, e.message, false, e.message);
      });
      sock.on("close", () => {
        clearTimeout(timer);
        if (peerClosed) return;
        const why = feed ? "the server closed the connection without a close frame" : "the server closed the connection during the handshake";
        this.#end(1006, why, false, why);
      });
      const handshake = () => {
        const lines = [`GET ${path} HTTP/1.1`, `Host: ${auth}`, "Upgrade: websocket", "Connection: Upgrade", "Sec-WebSocket-Version: 13", `Sec-WebSocket-Key: ${key}`, "User-Agent: kario-jsrt"];
        if (offered.length) lines.push(`Sec-WebSocket-Protocol: ${offered.join(", ")}`);
        for (const [k, v] of Object.entries(headers)) lines.push(`${k}: ${v}`);
        send(lines.join("\r\n") + "\r\n\r\n");
      };
      if (connected) handshake();
      else sock.on(secure ? "secureConnect" : "connect", handshake);
      const events = (evs) => {
        for (const e of evs) {
          if (this.#state === 3) return;
          if (e.kind === "text") this.#fire("message", { data: asText(e.data) });
          else if (e.kind === "binary") this.#fire("message", { data: e.data.buffer.slice(e.data.byteOffset, e.data.byteOffset + e.data.byteLength) });
          else if (e.kind === "ping") {
            if (this.#state === 1) send(frame(10, e.data));
          } else if (e.kind === "close") {
            peerClosed = true;
            if (this.#state === 1) {
              const p = e.code === 1005 ? new Uint8Array(0) : Uint8Array.of(e.code >> 8, e.code & 255);
              send(frame(8, p));
            }
            sock.end();
            this.#end(e.code, e.reason, true);
          } else if (e.kind === "error") {
            peerClosed = true;
            if (this.#state === 1) send(frame(8, Buffer.concat([Uint8Array.of(e.code >> 8, e.code & 255), utf8.encode(e.why).subarray(0, 123)])));
            sock.end();
            this.#end(e.code, e.why, false, e.why);
          }
        }
      };
      sock.on("data", (chunk) => {
        if (this.#state === 3) return;
        if (feed) {
          events(feed(chunk));
          return;
        }
        head = Buffer.concat([head, chunk]);
        const end = head.indexOf("\r\n\r\n");
        if (end < 0) {
          if (head.length > FETCH_MAX_HEAD) this.#end(1006, "the server's answer to the handshake is over 64 KB", false, "the server's answer to the handshake is over 64 KB");
          return;
        }
        clearTimeout(timer);
        const text = head.subarray(0, end).toString("latin1");
        const [status, ...hl] = text.split("\r\n");
        const hs = {};
        for (const l of hl) {
          const i = l.indexOf(":");
          if (i > 0) hs[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
        }
        const has = (v, t) => (v ?? "").toLowerCase().split(/[ ,]+/).includes(t);
        let why = "";
        if (!/^HTTP\/1\.\d 101\b/.test(status)) why = "the server did not switch to WebSocket (no 101 answer)";
        else if (!has(hs.upgrade, "websocket")) why = "the server's answer has no Upgrade: websocket";
        else if (!has(hs.connection, "upgrade")) why = "the server's answer has no Connection: Upgrade";
        else if (hs["sec-websocket-accept"] !== want) why = "the server's Sec-WebSocket-Accept is wrong";
        else if ("sec-websocket-extensions" in hs) why = "the server chose an extension the client did not offer";
        else if ("sec-websocket-protocol" in hs) {
          const p = hs["sec-websocket-protocol"];
          if (!offered.includes(p)) why = "the server chose a protocol the client did not offer";
          else this.#protocol = p;
        }
        if (why) {
          this.#end(1006, why, false, why);
          return;
        }
        if (this.#state === 2) {
          this.#end(1006, "aborted", false);
          return;
        }
        this.#state = 1;
        feed = reader();
        this.#fire("open", {});
        const restBytes = head.subarray(end + 4);
        if (restBytes.length) events(feed(restBytes));
      });
      this.#sendFrame = (op, p) => send(frame(op, p));
    }

    #sendFrame = () => {};

    send(data) {
      if (this.#state === 0) throw new TypeError("WebSocket.send: the connection is not open yet");
      if (this.#state !== 1) return;
      let p;
      let op = 2;
      if (typeof data === "string") {
        p = utf8.encode(data);
        op = 1;
      } else if (data instanceof ArrayBuffer) {
        p = new Uint8Array(data);
      } else if (ArrayBuffer.isView(data)) {
        p = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
      } else {
        throw new TypeError("WebSocket.send: the data is a string, an ArrayBuffer or a view on one");
      }
      if (p.byteLength > WS_MAX_SEND) throw new RangeError(`WebSocket.send: a message over ${WS_MAX_SEND >> 20} MB`);
      if (this.bufferedAmount + p.byteLength > WS_MAX_BUFFERED) throw new RangeError(`WebSocket.send: over ${WS_MAX_BUFFERED >> 20} MB waits to be sent (bufferedAmount)`);
      this.#sendFrame(op, p);
    }

    close(code, reason) {
      if (code !== undefined) {
        code = Math.trunc(Number(code)) | 0;
        if (code !== 1000 && (code < 3000 || code > 4999)) throw new TypeError("WebSocket.close: the code is 1000, or from 3000 to 4999");
      }
      let why = new Uint8Array(0);
      if (reason !== undefined) {
        why = utf8.encode(String(reason));
        if (why.byteLength > 123) throw new SyntaxError("WebSocket.close: the reason is over 123 bytes");
        if (why.byteLength && code === undefined) code = 1000;
      }
      if (this.#state === 0) {
        this.#drop();
      } else if (this.#state === 1) {
        this.#closeWith(code, why);
      }
    }

    #closeWith(code, why) {
      this.#state = 2;
      const p = code === undefined ? new Uint8Array(0) : Buffer.concat([Uint8Array.of(code >> 8, code & 255), why]);
      this.#sendFrame(8, p);
      this.#closing = setTimeout(() => this.#end(1006, "the server did not answer the close", false, "the server did not answer the close"), WS_CLOSING_MS);
    }

    // ---- keepalive WebSockets (21-push.md §8.3), as ws.c keeps them ----
    // Those waiting for their socket, oldest first; the one whose
    // net.keepalive is out (one at a time, so that the replies come in the
    // order the Shell replaces its connections); the one whose socket the
    // Shell holds and whether it has ended; those that had their socket.
    static #keepWait = [];
    static #keepOut;
    static #keepCur;
    static #keepEnded = false;
    static #keepLive = new Set();
    static {
      keepEvent = (value) => {
        if (value === "closed" || value === "lost") {
          WebSocket.#keepCur = undefined;
          WebSocket.#keepEnded = false;
        }
      };
    }

    static #unqueue(ws) {
      const i = WebSocket.#keepWait.indexOf(ws);
      if (i >= 0) WebSocket.#keepWait.splice(i, 1);
    }

    // release has the Shell let go of the socket it holds for a keepalive
    // WebSocket that has ended.
    static #release() {
      own("net.keepalive", { close: true }).catch(() => {});
      WebSocket.#keepCur = undefined;
      WebSocket.#keepEnded = false;
    }

    static #ask() {
      const ws = WebSocket.#keepWait[0];
      if (!ws || WebSocket.#keepOut) return;
      WebSocket.#keepOut = ws;
      own("net.keepalive", { host: ws.#keepHost, port: ws.#keepPort, every_s: ws.#keepalive }).then(
        (r) => WebSocket.#reply(ws, undefined, r.hs),
        (e) => WebSocket.#reply(ws, e.message, []),
      );
    }

    // reply is the Shell's answer for ws: on success the Shell holds the
    // socket it hands over, and none of the one before, also when ws was
    // cancelled meanwhile.
    static #reply(ws, err, hs) {
      WebSocket.#keepOut = undefined;
      let h;
      if (!err) {
        let socket = false;
        try {
          socket = hs.length === 1 && hs[0].stat().socket === true;
        } catch {
          socket = false;
        }
        if (socket) h = hs.pop();
        else err = "net.keepalive: the reply has no socket";
      }
      for (const x of hs) closeHandle(x);
      const c = WebSocket.#keepWait[0] === ws ? WebSocket.#keepWait.shift() : undefined;
      if (h) {
        WebSocket.#keepCur = ws;
        WebSocket.#keepEnded = !c;
        WebSocket.#replace(c);
        if (c) c.#start(h);
        else closeHandle(h);
      } else if (c) {
        c.#told(err);
      }
      if (WebSocket.#keepWait.length) WebSocket.#ask();
      else if (WebSocket.#keepCur && WebSocket.#keepEnded) WebSocket.#release();
    }

    // replace closes the keepalive WebSockets besides c (undefined: all)
    // that have had their socket: the Shell holds another now.
    static #replace(c) {
      for (const w of WebSocket.#keepLive) {
        if (w === c || w.#state >= 2) continue;
        if (w.#state === 1) {
          w.#closeWith(1001, utf8.encode("replaced"));
        } else {
          w.#state = 2;
          setImmediate(() => w.#end(1006, "replaced", false));
        }
      }
    }

    static #closed(ws) {
      WebSocket.#keepLive.delete(ws);
      WebSocket.#unqueue(ws);
      if (WebSocket.#keepCur !== ws) return;
      WebSocket.#keepEnded = true;
      if (!WebSocket.#keepOut) WebSocket.#release();
    }

    #start(h) {
      WebSocket.#keepLive.add(this);
      let sock;
      try {
        sock = io.adopt(h, { secure: this.#secure, host: this.#keepHost });
      } catch (e) {
        closeHandle(h);
        this.#told(e.message);
        return;
      }
      this.#attach(sock, !this.#secure);
    }

    #told(why) {
      this.#state = 2;
      setImmediate(() => this.#end(1006, why, false, why));
    }

    addEventListener(type, fn, opts) {
      if (typeof fn !== "function") return;
      type = String(type);
      if (!this.#listeners.some((l) => l.type === type && l.fn === fn)) this.#listeners.push({ type, fn, once: !!opts?.once });
    }

    removeEventListener(type, fn) {
      type = String(type);
      this.#listeners = this.#listeners.filter((l) => !(l.type === type && l.fn === fn));
    }

    get readyState() {
      return this.#state;
    }
    get url() {
      return this.#url;
    }
    get protocol() {
      return this.#protocol;
    }
    get extensions() {
      return "";
    }
    get bufferedAmount() {
      return this.#sock?.writableLength ?? 0;
    }
    get binaryType() {
      return "arraybuffer";
    }
    set binaryType(v) {
      if (v !== "arraybuffer") throw new TypeError('WebSocket.binaryType: only "arraybuffer" (messages do not come as Blobs)');
    }
    get [Symbol.toStringTag]() {
      return "WebSocket";
    }
  }
  for (const [k, v] of Object.entries({ CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 })) Object.defineProperty(WebSocket.prototype, k, { value: v });

  const { ReadableStream: _, ...web } = webAPI(fetch, globalThis.ReadableStream);
  return { ...web, WebSocket, keepaliveEvent: (value) => keepEvent(value) };
}
