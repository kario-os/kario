// The module "kario" on Node.js (29-app-development.md): what kario-jsrt
// does in C (jsrt/src/kario.c and dispatch.c) over a transport, so the same
// main.js runs on the developer machine as on the device. kario.mjs gives it
// the Shell's socket on fd 3 (kario run), fake.mjs a test's fake Shell
// (kario test). Only what the protocol carries is here: no file, socket or
// process for the app (Node itself is not a sandbox; the device's engine is).
import { defaultLang, languages } from "./languages.mjs";

export const PROTO = "phone.ui/0";
// At most this much JSON in an app's storage (as the mini-program host).
const MAX_STORAGE = 1 << 20;
// The Shell keeps at most this many pages; a push beyond replaces the top.
const MAX_STACK = 32;
// The largest state a Shell keeps for a tombstone (ui.MaxSavedState).
const MAX_SAVED_STATE = 64 << 10;
const EVENTS = new Set(["start", "action", "key", "input", "popped", "foreground", "background", "save", "event", "notif", "resync"]);
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

function callError(message, cap) {
  const e = new Error(message);
  if (cap !== undefined) e.cap = cap;
  return e;
}

/**
 * createKario is one app process's module "kario". io:
 *   send(line)        a message to the Shell (one line of JSON)
 *   catalog(lang)     the app's locale/<lang>.json as text, or undefined
 *   storage           load(): the store's text or undefined; save(text), throwing on failure;
 *                     undefined when the app has no data directory
 *   log(line)         a line on the app's log
 * env: appId and lang (PHONE_APP_ID, PHONE_LANG). It returns the module's
 * exports and receive(line), what the transport hands it from the Shell.
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

  function sendLine(line) {
    if (closed) return false;
    try {
      io.send(line);
      return true;
    } catch {
      closed = true;
      return false;
    }
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

  function write(s) {
    const json = JSON.stringify(s);
    if (utf8.encode(json).length > MAX_STORAGE) throw new RangeError(`storage: over ${MAX_STORAGE >> 10} KB`);
    io.storage.save(json);
  }

  // put sets key to json (undefined removes it) and writes the file; on
  // failure the key keeps what it had.
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
  };

  function call(cap, args) {
    if (typeof cap !== "string") throw new TypeError("call: the capability's name is a string");
    const argsJSON = args === undefined ? undefined : toJSON(args);
    return new Promise((resolve, reject) => {
      const id = ++nextReq;
      const line = `{"t":"req","req":${id},"cap":${JSON.stringify(cap)}${argsJSON === undefined ? "" : `,"args":${argsJSON}`}}`;
      if (!sendLine(line)) {
        reject(callError("connection lost", cap));
        return;
      }
      calls.set(id, { cap, resolve, reject });
    });
  }

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
      if (saved !== undefined) {
        let json;
        try {
          json = toJSON(saved);
        } catch (e) {
          report(`on("save") returned what JSON cannot hold`, e);
        }
        const n = json === undefined ? 0 : utf8.encode(json).length;
        if (n > MAX_SAVED_STATE) io.log(`kario-node: saved state not sent: ${n} bytes (at most ${MAX_SAVED_STATE})`);
        else if (json !== undefined) sendLine(`{"t":"state","data":${json}}`);
      }
      emit("background", []);
    } else if (msg.state === "foreground") {
      emit("foreground", []);
    }
  }

  function result(msg) {
    const id = Number(msg.req);
    const c = calls.get(id);
    if (!c) return;
    calls.delete(id);
    if (msg.ok) c.resolve(msg.data);
    else c.reject(callError(typeof msg.err === "string" ? msg.err : "failed", c.cap));
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

  function receive(line) {
    let msg;
    try {
      msg = JSON.parse(line);
    } catch (e) {
      report("a message from the shell", e);
      return;
    }
    if (msg === null || typeof msg !== "object" || typeof msg.t !== "string") return;
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
        result(msg);
        break;
      case "popped":
        popped(msg);
        break;
      case "event":
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

  sendLine(JSON.stringify({ t: "hello", app: appId, proto: PROTO }));

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
    },
    receive,
    close() {
      closed = true;
    },
  };
}
