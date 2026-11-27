// The module "kario" on Node.js (29-app-development.md): what kario-jsrt
// does in C (jsrt/src/kario.c, dispatch.c, store.c) over a transport, so the same
// main.js runs on the developer machine as on the device. kario.mjs gives it
// the Shell's socket on fd 3 (kario run), fake.mjs a test's fake Shell
// (kario test). Only what the protocol carries is here: no file, socket or
// process for the app (Node itself is not a sandbox; the device's engine is).
import { replyFiles } from "./files.mjs";
import { defaultLang, languages } from "./languages.mjs";
import { createNet } from "./net.mjs";

export const PROTO = "phone.ui/0";
// At most this much JSON in an app's storage (as the mini-program host).
const MAX_STORAGE = 1 << 20;
// The Shell keeps at most this many pages; a push beyond replaces the top.
const MAX_STACK = 32;
// The largest state a Shell keeps for a tombstone (ui.MaxSavedState).
const MAX_SAVED_STATE = 64 << 10;
const EVENTS = new Set(["start", "action", "key", "input", "popped", "foreground", "background", "save", "event", "notif", "resync"]);
const KEEPALIVE_REFUSED = "net.keepalive: a TypeScript app keeps its own connection as a WebSocket (new WebSocket(url, protocols, { keepalive }))";
const utf8 = new TextEncoder();

/** canonical is the language a PHONE_LANG tag names, as package i18n reads it. */
export function canonical(tag) {
  const t = String(tag ?? "").trim().replaceAll("_", "-").toLowerCase();
  if (!t) return defaultLang;
  const [base, ...rest] = t.split("-");
  if (base === "zh") return rest.some((p) => ["hant", "tw", "hk", "mo"].includes(p)) ? "zh-Hant" : "zh-Hans";
  const l = languages.find((l) => l.id.toLowerCase() === base || l.id.toLowerCase() === t);
  return l ? l.id : "en";
}

/** chain is where a message comes from: the language, its fallbacks, English. */
export function chain(lang) {
  const l = languages.find((l) => l.id === canonical(lang));
  const out = l ? [l.id, ...l.fallbacks] : [];
  if (out.at(-1) !== "en") out.push("en");
  return out;
}

/** format is how the log shows a value: text as it is, an error with its stack, data as JSON. */
export function format(v) {
  if (typeof v === "string") return v;
  if (v instanceof Error) return v.stack && !v.stack.startsWith(String(v)) ? `${v}\n${v.stack}` : String(v.stack ?? v);
  if (v !== null && typeof v === "object") {
    try {
      const j = JSON.stringify(v);
      if (j !== undefined) return j;
    } catch {
      // Not JSON (a cycle, a BigInt): shown as text below.
    }
  }
  try {
    return String(v);
  } catch {
    return "?";
  }
}

function toJSON(v) {
  const j = JSON.stringify(v);
  if (j === undefined) throw new TypeError("not representable as JSON");
  return j;
}

function text(v) {
  try {
    return String(v);
  } catch {
    return "?";
  }
}

// %d as Go prints an integer: truncated, -0 kept, no exponent.
function integer(x) {
  if (!Number.isFinite(x)) return "0";
  const t = Math.trunc(x);
  return Object.is(t, -0) ? "-0" : BigInt(t).toString();
}

// Other majors (22-developer-api.md §2.1): each a form (ui.ProtoForm) of
// renames from this runtime's names to the major's; a rule with t renames
// the type t to as and the fields of messages of that type, one without t
// the fields of every message.
const PROTO_BASE = "phone.ui/";
const OWN_MAJOR = Number(PROTO.slice(PROTO_BASE.length));

function readForms(json) {
  if (!json) return [];
  try {
    const forms = JSON.parse(json);
    return Array.isArray(forms) ? forms.filter((f) => Number.isInteger(f?.major) && f.major >= 0 && f.major !== OWN_MAJOR) : [];
  } catch {
    return [];
  }
}

function renamed(m, from, to) {
  if (Object.hasOwn(m, from)) {
    m[to] = m[from];
    delete m[from];
  }
}

function toMajor(f, m) {
  const t = m.t;
  for (const r of f.rules ?? []) {
    if (r.t && r.t !== t) continue;
    for (const [own, theirs] of Object.entries(r.fields ?? {})) renamed(m, own, theirs);
    if (r.t && r.as) m.t = r.as;
  }
  return m;
}

// fromMajor turns m back; false for a type the major does not have.
function fromMajor(f, m) {
  const t = m.t;
  const rules = f.rules ?? [];
  const theirs = (r) => r.as || r.t;
  if (!rules.some((r) => r.t && theirs(r) === t) && rules.some((r) => r.t === t && r.as)) return false;
  for (const r of rules) {
    if (r.t && theirs(r) !== t) continue;
    for (const [own, their] of Object.entries(r.fields ?? {})) renamed(m, their, own);
    if (r.t) m.t = r.t;
  }
  return true;
}

function callError(message, cap) {
  const e = new Error(message);
  if (cap !== undefined) e.cap = cap;
  return e;
}

/**
 * createKario is one app process's module "kario". io:
 *   send(line)        a message to the Shell (one line of JSON)
 *   catalog(lang)     the app's locale/<lang>.json as text, or undefined
 *   storage           load(): the store's text or undefined; save(text): writes it whole,
 *                     returning or resolving once it is on disk, throwing or rejecting on
 *                     failure (one save at a time); undefined when the app has no data directory
 *   log(line)         a line on the app's log
 *   fetch(req)        a request (net.mjs); connect(o) a WebSocket's connection
 *   adopt(h, o)       a keepalive WebSocket's connection on the socket the Shell
 *                     dialled (a reply's handle), or undefined: it connects as others do
 *   cookies           the cookie jar's load() and save(text), or undefined
 *   clock(ms)         the system clock's new offset (time.clock), before the app hears of it;
 *                     optional
 * env: appId, lang, net and keepalive (PHONE_APP_ID, PHONE_LANG, the "net" and
 * "keepalive" permissions), forms: the other majors spoken (PHONE_UI_TEST_FORMS,
 * for tests).
 * It returns the module's exports and receive(line, files), what the
 * transport hands it from the Shell.
 */
export function createKario(io, env) {
  const appId = env.appId ?? "";
  const lang = canonical(env.lang);
  const handlers = Object.create(null);
  const calls = new Map();
  let nextReq = 0;
  let stack = [];
  let closed = false;
  let store;
  let messages;

  const report = (what, e) => io.log(`kario-node: ${what}: ${format(e)}`);
  const held = replyFiles((line) => io.log(line));
  // With other majors, lines wait in unstarted until start names the major
  // (form: undefined for this runtime's own).
  const forms = readForms(env.forms);
  const majors = [...new Set([OWN_MAJOR, ...forms.map((f) => f.major)])].sort((a, b) => b - a);
  let form;
  let started = forms.length === 0;
  const unstarted = [];

  function writeLine(line) {
    if (closed) return false;
    try {
      io.send(line);
      return true;
    } catch {
      closed = true;
      return false;
    }
  }

  function sendLine(line) {
    if (closed) return false;
    if (!started) {
      unstarted.push(line);
      return true;
    }
    return writeLine(form ? JSON.stringify(toMajor(form, JSON.parse(line))) : line);
  }

  // choose takes the major start names (none: a system that does not
  // negotiate, so hello's proto, the oldest) and sends what waited.
  function choose(proto) {
    const major = typeof proto === "string" && proto.startsWith(PROTO_BASE) ? Number(proto.slice(PROTO_BASE.length)) : majors.at(-1);
    form = forms.find((f) => f.major === major);
    started = true;
    for (const line of unstarted.splice(0)) sendLine(line);
  }

  function mirror(t, id, json) {
    if (t === "update") {
      for (const p of stack) if (p.id === id) p.json = json;
      return;
    }
    if (t === "reset") stack = [];
    if (stack.length && (t === "replace" || (t === "push" && stack.length >= MAX_STACK))) {
      stack[stack.length - 1] = { id, json };
      return;
    }
    stack.push({ id, json });
  }

  const page = (t) => (screen) => {
    if (screen === null || typeof screen !== "object" || Array.isArray(screen)) throw new TypeError(`${t}: the argument is a screen object`);
    if (typeof screen.id !== "string") throw new TypeError(`${t}: the screen needs an id`);
    const json = toJSON(screen);
    mirror(t, screen.id, json);
    sendLine(`{"t":"${t}","screen":${json}}`);
  };

  function emit(name, args) {
    const fn = handlers[name];
    if (!fn) return undefined;
    try {
      return fn(...args);
    } catch (e) {
      report(`uncaught exception in on("${name}")`, e);
      return undefined;
    }
  }

  function catalog() {
    if (messages) return messages;
    messages = Object.create(null);
    for (const l of chain(lang).reverse()) {
      const raw = io.catalog(l);
      if (raw === undefined) continue;
      try {
        const obj = JSON.parse(raw);
        if (obj && typeof obj === "object") for (const [k, v] of Object.entries(obj)) if (typeof v === "string") messages[k] = v;
      } catch (e) {
        report(`locale/${l}.json`, e);
      }
    }
    return messages;
  }

  function tr(...a) {
    if (a.length === 0) return "";
    const [msg, ...args] = a;
    const m = text(msg);
    const f = catalog()[m] ?? m;
    let out = "";
    let next = 0;
    for (let i = 0; i < f.length; i++) {
      const c = f[i];
      if (c !== "%" || i + 1 >= f.length) {
        out += c;
        continue;
      }
      const verb = f[++i];
      if (verb === "%") {
        out += "%";
      } else if (verb !== "s" && verb !== "v" && verb !== "d") {
        out += "%" + verb;
      } else if (next >= args.length) {
        out += `%!${verb}(MISSING)`;
      } else {
        const a = args[next++];
        out += verb === "d" && typeof a === "number" ? integer(a) : text(a);
      }
    }
    return out;
  }

  function open() {
    if (store) return store;
    if (!io.storage) throw new Error("storage: the app has no data directory");
    store = Object.create(null);
    const raw = io.storage.load();
    if (raw !== undefined) {
      try {
        const obj = JSON.parse(raw);
        if (obj && typeof obj === "object") for (const [k, v] of Object.entries(obj)) if (typeof v === "string") store[k] = v;
      } catch (e) {
        report("storage", e);
      }
    }
    return store;
  }

  // The store's writes, as kario-jsrt's writer thread does them (store.c):
  // one at a time, saves made meanwhile coalescing into the next, which
  // carries the newest content. Saves are numbered; a write carries the
  // newest save's number, waiting lists the flushes and the exit waiting
  // for a number, unsent is the state waiting to be sent.
  let next;
  let writing = false;
  let queued = 0;
  let done = 0;
  let ok = 0;
  let failure;
  const waiting = [];
  let unsent;

  const storageError = (e) => new Error(`storage: ${e?.message ?? e}`);

  async function pump() {
    writing = true;
    while (next !== undefined) {
      const text = next;
      const seq = queued;
      next = undefined;
      try {
        await io.storage.save(text);
        ok = seq;
      } catch (e) {
        failure = e;
        io.log(`kario-node: storage not saved: ${e?.message ?? e}`);
      }
      done = seq;
      while (waiting.length && waiting[0].seq <= done) {
        const w = waiting.shift();
        w.settle(ok >= w.seq);
      }
      if (unsent && done >= unsent.seq) {
        sendLine(unsent.line);
        unsent = undefined;
      }
    }
    writing = false;
  }

  // after calls settle(stored) once the saves made so far are written:
  // stored says whether what they carried is on disk.
  function after(settle) {
    const seq = queued;
    if (done >= seq) settle(ok >= seq);
    else waiting.push({ seq, settle });
  }

  // sendAfterWrites sends line once the saves made so far are written; a
  // later one replaces a line still waiting.
  function sendAfterWrites(line) {
    if (done >= queued) sendLine(line);
    else unsent = { line, seq: queued };
  }

  function write(s) {
    const json = JSON.stringify(s);
    if (utf8.encode(json).length > MAX_STORAGE) throw new RangeError(`storage: over ${MAX_STORAGE >> 10} KB`);
    next = json;
    queued++;
    if (!writing) pump();
  }

  // put sets key to json (undefined removes it) and saves the store; over
  // its size the key keeps what it had.
  function put(key, json) {
    const s = open();
    const k = typeof key === "symbol" ? key : String(key);
    const had = Object.hasOwn(s, k);
    const old = s[k];
    if (json === undefined) delete s[k];
    else s[k] = json;
    try {
      write(s);
    } catch (e) {
      if (had) s[k] = old;
      else delete s[k];
      throw e;
    }
  }

  const storage = {
    get(key) {
      const v = open()[typeof key === "symbol" ? key : String(key)];
      return typeof v === "string" ? JSON.parse(v) : undefined;
    },
    set(key, value) {
      put(key, value === undefined ? undefined : toJSON(value));
    },
    remove(key) {
      put(key, undefined);
    },
    keys() {
      return Object.keys(open());
    },
    clear() {
      const old = open();
      store = Object.create(null);
      try {
        write(store);
      } catch (e) {
        store = old;
        throw e;
      }
    },
    flush() {
      return new Promise((resolve, reject) => after((stored) => (stored ? resolve() : reject(storageError(failure)))));
    },
  };

  function request(cap, args, own) {
    const argsJSON = args === undefined ? undefined : toJSON(args);
    return new Promise((resolve, reject) => {
      const id = ++nextReq;
      const line = `{"t":"req","req":${id},"cap":${JSON.stringify(cap)}${argsJSON === undefined ? "" : `,"args":${argsJSON}`}}`;
      if (!sendLine(line)) {
        reject(callError("connection lost", cap));
        return;
      }
      calls.set(id, { cap, resolve, reject, own });
    });
  }

  function call(cap, args) {
    if (typeof cap !== "string") throw new TypeError("call: the capability's name is a string");
    // The socket comes only to the runtime's own call (net.mjs).
    if (cap === "net.keepalive") {
      if (args !== undefined) toJSON(args);
      return Promise.reject(callError(KEEPALIVE_REFUSED, cap));
    }
    return request(cap, args, false);
  }

  // own is a call of the runtime's own: it resolves with the reply's data
  // and the handles of its files, which no JavaScript of the app sees.
  const own = (cap, args) => request(cap, args, true);

  function on(name, fn) {
    const n = text(name);
    if (!EVENTS.has(n)) throw new TypeError(`on: no event "${n}"`);
    if (typeof fn !== "function" && fn !== undefined && fn !== null) throw new TypeError("on: the handler is a function");
    if (typeof fn === "function") handlers[n] = fn;
    else delete handlers[n];
  }

  function cancelled(err) {
    return err !== null && typeof err === "object" && typeof err.message === "string" && err.message.endsWith("cancelled");
  }

  const prop = (msg, name, dflt) => (msg[name] === undefined || msg[name] === null ? dflt : msg[name]);

  function action(msg) {
    const e = {
      screen: prop(msg, "screen_id", ""),
      node: prop(msg, "node", ""),
      action: prop(msg, "action", ""),
      value: prop(msg, "value", ""),
      fields: prop(msg, "fields", {}),
    };
    // A focused canvas's keys and a live input's edits have handlers of
    // their own; without one they are actions like any other.
    if (msg.action === "key" && handlers.key) emit("key", [e.value, e]);
    else if (msg.action === "changed" && handlers.input) emit("input", [e]);
    else emit("action", [e]);
  }

  function lifecycle(msg) {
    if (msg.state === "background") {
      const saved = emit("save", []);
      let line;
      if (saved !== undefined) {
        let json;
        try {
          json = toJSON(saved);
        } catch (e) {
          report(`on("save") returned what JSON cannot hold`, e);
        }
        const n = json === undefined ? 0 : utf8.encode(json).length;
        if (n > MAX_SAVED_STATE) io.log(`kario-node: saved state not sent: ${n} bytes (at most ${MAX_SAVED_STATE})`);
        else if (json !== undefined) line = `{"t":"state","data":${json}}`;
      }
      emit("background", []);
      // The Shell may end the app as soon as it has the state: what the
      // handlers stored, their microtasks included, is on disk first.
      if (line !== undefined) setImmediate(() => sendAfterWrites(line));
    } else if (msg.state === "foreground") {
      emit("foreground", []);
    }
  }

  function result(msg, hs) {
    const id = Number(msg.req);
    const c = calls.get(id);
    if (!c) {
      held.closeAll(hs);
      return;
    }
    calls.delete(id);
    if (!msg.ok) {
      held.closeAll(hs);
      c.reject(callError(typeof msg.err === "string" ? msg.err : "failed", c.cap));
      return;
    }
    let why;
    if (Number(msg.fds) > hs.length) {
      held.closeAll(hs);
      why = "files from replies reach Node.js through kario-native.node, which kario builds with a C compiler; it is not there (kario run -engine jsrt runs the app on kario-jsrt instead)";
    } else if (c.own) {
      c.resolve({ data: msg.data, hs });
      return;
    } else if (hs.length) {
      why = held.attach(c.cap, msg.data, hs);
    }
    if (why) c.reject(callError(why, c.cap));
    else c.resolve(msg.data);
  }

  // resync answers a Shell that took over the connection: calls the
  // previous one never answered fail, and the pages it held are sent again.
  function resync() {
    const pending = [...calls.values()];
    calls.clear();
    for (const c of pending) c.reject(callError("the shell restarted", c.cap));
    stack.forEach((p, i) => sendLine(`{"t":"${i ? "push" : "reset"}","screen":${p.json}}`));
    emit("resync", []);
  }

  function popped(msg) {
    const id = typeof msg.screen_id === "string" ? msg.screen_id : undefined;
    if (id !== undefined && stack.length > 1 && stack.at(-1).id === id) stack.pop();
    emit("popped", [id ?? ""]);
  }

  // receive takes a line from the Shell and the files (io's handles) that
  // came with it, the runtime's from then on.
  function receive(line, hs = []) {
    let msg;
    try {
      msg = JSON.parse(line);
    } catch (e) {
      held.closeAll(hs);
      report("a message from the shell", e);
      return;
    }
    if (msg === null || typeof msg !== "object" || typeof msg.t !== "string") {
      held.closeAll(hs);
      return;
    }
    if (!started && msg.t === "start") choose(msg.proto);
    if (form && !fromMajor(form, msg)) {
      held.closeAll(hs);
      report("a message from the shell", `${PROTO_BASE}${form.major} has no ${msg.t}`);
      return;
    }
    if (msg.t !== "res") held.closeAll(hs);
    switch (msg.t) {
      case "ping":
        sendLine(JSON.stringify({ t: "pong", value: typeof msg.value === "string" ? msg.value : "" }));
        break;
      case "start":
        emit("start", [prop(msg, "value", ""), msg.data]);
        break;
      case "action":
        action(msg);
        break;
      case "lifecycle":
        lifecycle(msg);
        break;
      case "res":
        result(msg, hs);
        break;
      case "popped":
        popped(msg);
        break;
      case "event":
        // Node's Date follows TZ once it is assigned.
        if (prop(msg, "event", "") === "time.zone" && prop(msg, "value", "")) process.env.TZ = prop(msg, "value", "");
        if (prop(msg, "event", "") === "keepalive") net.keepaliveEvent(prop(msg, "value", ""));
        if (prop(msg, "event", "") === "time.clock") io.clock?.(prop(msg, "value", ""));
        emit("event", [prop(msg, "event", ""), prop(msg, "value", "")]);
        break;
      case "notif_action":
        emit("notif", [{ id: prop(msg, "value", ""), action: prop(msg, "action", ""), text: prop(msg, "text", "") }]);
        break;
      case "resync":
        resync();
        break;
    }
  }

  // fetch and WebSocket: kario-jsrt's rules, the network itself io's.
  const net = createNet(io, env, own);

  const hello = { t: "hello", app: appId, proto: forms.length ? PROTO_BASE + majors.at(-1) : PROTO };
  if (forms.length) hello.protos = majors.map((m) => PROTO_BASE + m);
  writeLine(JSON.stringify(hello));

  return {
    exports: {
      appId,
      lang,
      reset: page("reset"),
      push: page("push"),
      replace: page("replace"),
      update: page("update"),
      pop: () => {
        sendLine('{"t":"pop"}');
      },
      toast: (t) => {
        sendLine(JSON.stringify({ t: "toast", text: text(t) }));
      },
      notify: (n) => {
        if (n === null || typeof n !== "object") throw new TypeError("notify: the argument is a notification object");
        sendLine(`{"t":"notify","notif":${toJSON(n)}}`);
      },
      call,
      on,
      tr,
      storage,
      cancelled,
      fetch: net.fetch,
      WebSocket: net.WebSocket,
      Blob: net.Blob,
      File: net.File,
      FormData: net.FormData,
      Headers: net.Headers,
      Request: net.Request,
      Response: net.Response,
    },
    receive,
    close() {
      closed = true;
    },
  };
}
