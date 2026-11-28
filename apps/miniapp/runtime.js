// A mini-program's runtime: the module kario-jsrt runs for one program, in
// a process of its own. The host writes it with PROGRAM in front (the
// program's id and name, its JavaScript, its translations, prelude.js and
// whether it keeps storage) and is this process's Shell: pages go to it as
// push, update and pop, platform capabilities and errors as calls it
// answers. The program's code shares this context and can reach "kario"
// itself, so nothing here is a boundary: the host checks every page, call
// and pop against the program's declared permissions and its own pages.
import { on, push, update, pop, toast, call, storage } from "kario";

const P = PROGRAM;
const defs = Object.create(null);
const live = Object.create(null);
let seq = 0;

function describe(e) {
  if (e instanceof Error) {
    return String(e) + (e.stack ? "\n" + e.stack : "");
  }
  return String(e);
}

// tell reports to the host; it shows the user the first line.
function tell(kind, message) {
  call("error", { kind: kind, message: message }).catch(function () {});
}

function invoke(self, fn, args) {
  if (typeof fn !== "function") {
    return undefined;
  }
  try {
    return fn.apply(self, args);
  } catch (e) {
    tell("callback", describe(e));
    return undefined;
  }
}

function copy(v) {
  try {
    const c = JSON.parse(JSON.stringify(v));
    return c !== null && typeof c === "object" && !Array.isArray(c) ? c : {};
  } catch (e) {
    return {};
  }
}

// integer is %d: Number(x) truncated, written out in full like Go's
// strconv.FormatFloat(x, 'f', -1, 64) (1e21 is 1000000000000000000000).
function integer(v) {
  const f = Number(v);
  if (!isFinite(f)) {
    return String(f);
  }
  const t = Math.trunc(f);
  if (t === 0) {
    return "0";
  }
  const s = String(t);
  const e = s.indexOf("e+");
  if (e < 0) {
    return s;
  }
  const neg = s[0] === "-";
  const digits = s.slice(neg ? 1 : 0, e).replace(".", "");
  return (neg ? "-" : "") + digits + "0".repeat(Number(s.slice(e + 2)) + 1 - digits.length);
}

// translate is phone.t (types/phone.d.ts): msg's translation, then %s, %d
// and %% in it; a verb past the last argument stays as written, and so does
// the whole text when there are no arguments.
function translate(msg, args) {
  msg = String(msg);
  if (Object.prototype.hasOwnProperty.call(P.msgs, msg)) {
    msg = P.msgs[msg];
  }
  if (args.length === 0) {
    return msg;
  }
  let out = "";
  for (let i = 0; i < msg.length; i++) {
    if (msg[i] !== "%" || i + 1 === msg.length) {
      out += msg[i];
      continue;
    }
    const verb = msg[i + 1];
    if (verb === "%") {
      out += "%";
    } else if ((verb === "s" || verb === "d") && args.length > 0) {
      out += verb === "s" ? String(args[0]) : integer(args[0]);
      args = args.slice(1);
    } else {
      out += msg.slice(i, i + 2);
    }
    i++;
  }
  return out;
}

function utf8Length(s) {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    n += c < 0x80 ? 1 : c < 0x800 ? 2 : c >= 0xd800 && c < 0xdc00 ? 4 : c >= 0xdc00 && c < 0xe000 ? 0 : 3;
  }
  return n;
}

function quotaError() {
  return new TypeError("storage quota exceeded (" + (P.quota >> 10) + " KB per mini-program)");
}

// A program without "storage" has no store: its runtime has no data
// directory, and kario-jsrt's error for that is one no catch sees.
function getStorage(key) {
  const v = P.storage ? storage.get(String(key)) : undefined;
  return v === undefined ? null : v;
}

function setStorage(key, v) {
  if (!P.storage) {
    throw new TypeError("permission not declared by mini-program: storage");
  }
  key = String(key);
  const text = JSON.stringify(v === undefined ? null : v);
  const parts = [JSON.stringify(key) + ":" + (text === undefined ? "null" : text)];
  for (const k of storage.keys()) {
    if (k !== key) {
      parts.push(JSON.stringify(k) + ":" + JSON.stringify(storage.get(k)));
    }
  }
  if (utf8Length("{" + parts.join(",") + "}") > P.quota) {
    throw quotaError();
  }
  try {
    storage.set(key, v === undefined ? null : v);
  } catch (e) {
    throw e instanceof RangeError ? quotaError() : e;
  }
}

function screenOf(pg) {
  const o = pg.obj;
  const s = { id: pg.screen, title: o.title === undefined ? "" : String(o.title) };
  const body = invoke(o, o.render, []);
  if (body !== undefined) {
    s.body = Array.isArray(body) ? body : [body];
  }
  if (o.actions !== undefined) {
    s.actions = o.actions;
  }
  return s;
}

function send(f, pg) {
  const s = screenOf(pg);
  try {
    f(s);
  } catch (e) {
    f({ id: s.id, title: s.title, body: "not JSON: " + String(e) });
  }
}

// top is the page on top of the program's own, the one the user sees.
function top() {
  let t;
  for (const id of Object.keys(live)) {
    if (t === undefined || live[id].seq > t.seq) {
      t = live[id];
    }
  }
  return t;
}

function hook(pg, name) {
  if (pg !== undefined) {
    invoke(pg.obj, pg.obj[name], []);
  }
}

function navigate(name, query) {
  const def = defs[name];
  if (def === undefined) {
    call("error", { kind: "page", message: name }).catch(function () {});
    return;
  }
  hook(top(), "onHide");
  seq++;
  const pg = { screen: "mp:" + P.id + ":" + name + ":" + seq, seq: seq, obj: {}, shown: false };
  const obj = pg.obj;
  for (const k of Object.keys(def)) {
    obj[k] = def[k];
  }
  obj.data = def.data !== null && typeof def.data === "object" && !Array.isArray(def.data) ? copy(def.data) : {};
  obj.setData = function (patch) {
    const cur = obj.data;
    for (const k of Object.keys(patch)) {
      cur[k] = patch[k];
    }
    if (pg.shown && live[pg.screen] === pg) {
      send(update, pg);
    }
  };
  live[pg.screen] = pg;
  invoke(obj, obj.onLoad, [query]);
  pg.shown = true;
  send(push, pg);
  hook(pg, "onShow");
}

function Page(name, def) {
  if (def === null || def === undefined) {
    throw new TypeError("Page " + String(name) + ": the page is an object");
  }
  defs[String(name)] = Object(def);
}

function asked(opts, name) {
  return opts !== null && opts !== undefined ? opts[name] : undefined;
}

const phone = {
  t: function (msg) {
    return translate(msg, Array.prototype.slice.call(arguments, 1));
  },
  toast: function (text) {
    toast(String(text));
  },
  navigateTo: function (name, query) {
    name = String(name);
    const q = query !== null && typeof query === "object" ? copy(query) : {};
    Promise.resolve().then(function () {
      navigate(name, q);
    });
  },
  navigateBack: function (delta) {
    let n = 1;
    if (delta !== undefined) {
      n = Math.max(1, Math.trunc(Number(delta)) || 0);
    }
    n = Math.min(n, Object.keys(live).length);
    for (let i = 0; i < n; i++) {
      pop();
    }
  },
  getStorage: getStorage,
  setStorage: setStorage,
  scanCode: function (opts) {
    call("scanCode").then(
      function (r) {
        invoke(undefined, asked(opts, "success"), [{ result: r.result }]);
      },
      function (e) {
        invoke(undefined, asked(opts, "fail"), [{ errMsg: e.message }]);
      },
    );
  },
  requestPayment: function (opts) {
    const amount = Math.trunc(Number(asked(opts, "amount")));
    call("requestPayment", { merchant: String(asked(opts, "merchant")), amount: isFinite(amount) ? amount : 0 }).then(
      function (r) {
        invoke(undefined, asked(opts, "success"), [{ orderId: r.orderId, verified: r.verified }]);
      },
      function (e) {
        invoke(undefined, asked(opts, "fail"), [{ errMsg: e.message }]);
      },
    );
  },
};

globalThis.Page = Page;
globalThis.phone = phone;

on("start", function (page) {
  try {
    (0, eval)(P.prelude);
    (0, eval)(P.source);
  } catch (e) {
    tell("start", P.id + ": " + describe(e));
    return;
  }
  if (defs[page] === undefined) {
    tell("start", P.id + " has no " + page + " page");
    return;
  }
  navigate(page, {});
});

on("action", function (e) {
  const pg = live[e.screen];
  if (pg === undefined) {
    return;
  }
  const fields = {};
  for (const k of Object.keys(e.fields || {})) {
    fields[k] = e.fields[k];
  }
  invoke(pg.obj, pg.obj.onAction, [{ action: e.action, node: e.node, value: e.value, fields: fields }]);
});

on("popped", function (id) {
  const pg = live[id];
  if (pg === undefined) {
    return;
  }
  delete live[id];
  hook(pg, "onUnload");
  hook(top(), "onShow");
});

// The host closed the program and keeps it warm, or opened it again.
on("background", function () {
  hook(top(), "onHide");
});

on("foreground", function () {
  hook(top(), "onShow");
});
