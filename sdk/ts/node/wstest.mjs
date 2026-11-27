// kario test's WebSocket server (@kario/test): the app's WebSocket connects
// to it as to wss:// or ws:// anywhere, over an in-memory pair of sockets
// (net.mjs speaks the same handshake and framing to it as to a real
// server), and the test plays the server: it decides the handshake, reads
// what the app sends and sends, pings, closes or drops.
import { createHash } from "node:crypto";
import { duplexPair } from "node:stream";

const GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
const STATUS = { 400: "Bad Request", 401: "Unauthorized", 403: "Forbidden", 404: "Not Found", 500: "Internal Server Error", 503: "Service Unavailable" };
const utf8 = new TextEncoder();
const asText = (b) => new TextDecoder("utf-8", { ignoreBOM: true }).decode(b);

// frame is a server's frame: unmasked, as the protocol wants.
function frame(op, payload) {
  const n = payload.byteLength;
  const h = n < 126 ? 2 : n < 65536 ? 4 : 10;
  const out = new Uint8Array(h + n);
  out[0] = 0x80 | op;
  if (n < 126) {
    out[1] = n;
  } else if (n < 65536) {
    out[1] = 126;
    out[2] = n >> 8;
    out[3] = n & 255;
  } else {
    out[1] = 127;
    new DataView(out.buffer).setBigUint64(2, BigInt(n));
  }
  out.set(payload, h);
  return out;
}

const bytes = (data) =>
  typeof data === "string"
    ? utf8.encode(data)
    : data instanceof ArrayBuffer
      ? new Uint8Array(data)
      : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);

/**
 * connect is the app's connection to the test's server: the socket the
 * runtime's WebSocket speaks on, and the server's side, the peer, handed
 * to onPeer once the app's handshake came. answer(request) decides the
 * handshake: { protocol } accepts with that protocol, { status } refuses
 * with that HTTP status, nothing accepts; it may be async. activity is
 * called whenever bytes go either way (the test's settle waits for it).
 */
export function connect(to, answer, onPeer, activity) {
  const [app, server] = duplexPair();
  const peer = new Peer(app, server, activity);
  let head = Buffer.alloc(0);
  const onHead = async (chunk) => {
    activity();
    head = Buffer.concat([head, chunk]);
    const end = head.indexOf("\r\n\r\n");
    if (end < 0) return;
    server.off("data", onHead);
    const [line, ...lines] = head.subarray(0, end).toString("latin1").split("\r\n");
    const rest = head.subarray(end + 4);
    const headers = {};
    for (const l of lines) {
      const i = l.indexOf(":");
      if (i > 0) headers[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
    }
    const path = line.split(" ")[1] ?? "/";
    peer.url = `${to.secure ? "wss" : "ws"}://${headers.host ?? to.host}${path}`;
    peer.headers = headers;
    peer.protocols = (headers["sec-websocket-protocol"] ?? "").split(",").map((p) => p.trim()).filter(Boolean);
    onPeer(peer);
    let a;
    try {
      a = (await answer?.(peer.request())) ?? {};
    } catch (e) {
      a = { status: 500 };
      peer.refused = e instanceof Error ? e.message : String(e);
    }
    if (a.status !== undefined && a.status !== 101) {
      server.end(`HTTP/1.1 ${a.status} ${STATUS[a.status] ?? "Refused"}\r\nContent-Length: 0\r\n\r\n`);
      peer.ended(1006, "refused", false);
      return;
    }
    const accept = createHash("sha1").update((headers["sec-websocket-key"] ?? "") + GUID).digest("base64");
    const out = ["HTTP/1.1 101 Switching Protocols", "Upgrade: websocket", "Connection: Upgrade", `Sec-WebSocket-Accept: ${accept}`];
    if (a.protocol) {
      out.push(`Sec-WebSocket-Protocol: ${a.protocol}`);
      peer.protocol = a.protocol;
    }
    for (const [k, v] of Object.entries(a.headers ?? {})) out.push(`${k}: ${v}`);
    server.write(out.join("\r\n") + "\r\n\r\n");
    peer.opened();
    if (rest.length) peer.feed(rest);
  };
  server.on("data", onHead);
  server.on("close", () => peer.ended(1006, "", false));
  server.on("error", () => {});
  // The runtime waits for the TLS (or TCP) connection before its handshake.
  setImmediate(() => app.emit(to.secure ? "secureConnect" : "connect"));
  return app;
}

class Peer {
  url = "";
  protocols = [];
  headers = {};
  protocol = "";
  /** What the app sent, in order: text as strings, binary as bytes. */
  messages = [];
  /** Why the test's answer threw, when it did. */
  refused = "";
  #app;
  #server;
  #activity;
  #taken = 0;
  #waiting = [];
  #open = false;
  #over = false;
  #closing = false;
  #chunks = Buffer.alloc(0);
  #parts = [];
  #op = 0;
  #closed;
  #resolveClosed;

  constructor(app, server, activity) {
    this.#app = app;
    this.#server = server;
    this.#activity = activity;
    this.#closed = new Promise((r) => {
      this.#resolveClosed = r;
    });
  }

  request() {
    return { url: this.url, protocols: this.protocols.slice(), headers: { ...this.headers } };
  }

  opened() {
    this.#open = true;
    this.#server.on("data", (chunk) => this.feed(chunk));
  }

  get open() {
    return this.#open && !this.#over;
  }

  /** Resolves with the close's code and reason once the connection is over (1006: no close frame). */
  get closed() {
    return this.#closed;
  }

  ended(code, reason, clean) {
    if (this.#over) return;
    this.#over = true;
    for (const w of this.#waiting.splice(0)) w.reject(new Error(`the connection closed (${code}) before the app sent a message`));
    this.#resolveClosed({ code, reason, clean });
    this.#activity();
  }

  feed(chunk) {
    this.#activity();
    this.#chunks = Buffer.concat([this.#chunks, chunk]);
    for (;;) {
      const b = this.#chunks;
      if (b.length < 2) return;
      let len = b[1] & 0x7f;
      let at = 2;
      if (len === 126) {
        if (b.length < 4) return;
        len = b.readUInt16BE(2);
        at = 4;
      } else if (len === 127) {
        if (b.length < 10) return;
        len = Number(b.readBigUInt64BE(2));
        at = 10;
      }
      const masked = b[1] & 0x80;
      const mask = masked ? b.subarray(at, at + 4) : null;
      at += masked ? 4 : 0;
      if (b.length < at + len) return;
      const payload = Buffer.from(b.subarray(at, at + len));
      if (mask) for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
      this.#chunks = b.subarray(at + len);
      const fin = b[0] & 0x80;
      const op = b[0] & 0x0f;
      if (op === 8) {
        const code = payload.length >= 2 ? payload.readUInt16BE(0) : 1005;
        const reason = asText(payload.subarray(2));
        if (!this.#closing) this.#write(frame(8, payload.subarray(0, 2)));
        this.#server.end();
        this.ended(code, reason, true);
        return;
      }
      if (op === 9) {
        this.#write(frame(10, payload));
        continue;
      }
      if (op === 10) continue;
      if (op !== 0) {
        this.#op = op;
        this.#parts = [];
      }
      this.#parts.push(payload);
      if (!fin) continue;
      const all = Buffer.concat(this.#parts);
      const m = this.#op === 1 ? asText(all) : new Uint8Array(all);
      this.messages.push(m);
      const w = this.#waiting.shift();
      if (w) {
        this.#taken++;
        w.resolve(m);
      }
    }
  }

  #write(b) {
    if (!this.#server.destroyed && !this.#server.writableEnded) {
      this.#activity();
      this.#server.write(b);
    }
  }

  /** The next message from the app not taken yet (waiting up to ms for it). */
  next(ms = 2000) {
    if (this.#taken < this.messages.length) return Promise.resolve(this.messages[this.#taken++]);
    if (this.#over) return Promise.reject(new Error("the connection is closed"));
    return new Promise((resolve, reject) => {
      const w = { resolve, reject };
      const t = setTimeout(() => {
        this.#waiting = this.#waiting.filter((x) => x !== w);
        reject(new Error(`no message from the app within ${ms} ms`));
      }, ms);
      w.resolve = (m) => {
        clearTimeout(t);
        resolve(m);
      };
      w.reject = (e) => {
        clearTimeout(t);
        reject(e);
      };
      this.#waiting.push(w);
    });
  }

  /** A text message (a string) or a binary one (bytes) to the app. */
  send(data) {
    if (!this.open) throw new Error("the connection is not open");
    this.#write(frame(typeof data === "string" ? 1 : 2, bytes(data)));
  }

  ping(data = "") {
    if (!this.open) throw new Error("the connection is not open");
    this.#write(frame(9, bytes(data)));
  }

  /** Bytes as they are, for a server that breaks the protocol. */
  sendRaw(data) {
    this.#write(bytes(data));
  }

  /** The server closes (the app answers with its close); resolves once the connection is over. */
  close(code = 1000, reason = "") {
    if (this.open && !this.#closing) {
      this.#closing = true;
      const why = utf8.encode(reason);
      const p = new Uint8Array(2 + why.byteLength);
      p[0] = code >> 8;
      p[1] = code & 255;
      p.set(why, 2);
      this.#write(frame(8, p));
    }
    return this.#closed.then(() => {});
  }

  /** The connection breaks without a close (the app sees 1006). */
  drop() {
    this.#server.destroy();
    this.#app.destroy();
    this.ended(1006, "", false);
  }
}
