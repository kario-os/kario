// The module "kario" for an app the Shell started under Node.js (kario run):
// the protocol over the socket on PHONE_UI_FD, storage in PHONE_DATA_DIR,
// translations from locale/ beside main.js, the log on stderr (and the
// debugger's console when one is attached).
import fs from "node:fs";
import inspector from "node:inspector";
import { createRequire } from "node:module";
import net from "node:net";
import path from "node:path";
import process from "node:process";
import tls from "node:tls";
import { fileURLToPath } from "node:url";
import { nodeFetch } from "./net.mjs";
import { createKario, format } from "./runtime.mjs";

const STORAGE_FILE = "kario-storage.json";
const COOKIE_FILE = "kario-cookies.txt";
const fd = Number(process.env.PHONE_UI_FD);
if (!process.env.PHONE_UI_FD || !Number.isInteger(fd)) {
  process.stderr.write("kario-node: not started by phone-shell (no PHONE_UI_FD)\n");
  process.exit(1);
}
const appDir = process.env.KARIO_APP_DIR ?? "";
const dataDir = process.env.PHONE_DATA_DIR ?? "";
const log = (line) => process.stderr.write(line + "\n");

// console as kario-jsrt has it: one line on stderr, which the Shell keeps as
// the app's log; the debugger still sees the values themselves.
for (const level of ["log", "info", "warn", "error", "debug"]) {
  const shown = inspector.console?.[level]?.bind(inspector.console);
  console[level] = (...args) => {
    log(args.map(format).join(" "));
    shown?.(...args);
  };
}

// manifestHas: the app's manifest asks for the permission, as kario-jsrt
// reads it (fetch needs "net").
function manifestHas(permission) {
  try {
    const m = JSON.parse(fs.readFileSync(path.join(appDir, "manifest.json"), "utf8"));
    return Array.isArray(m.permissions) && m.permissions.includes(permission);
  } catch {
    return false;
  }
}

// The socket's reader: kario-native.node's (kario builds it when this
// machine has a C compiler) also takes the files a reply brings, which
// Node's own sockets drop.
let addon;
try {
  addon = createRequire(import.meta.url)(fileURLToPath(new URL("./kario-native.node", import.meta.url)));
} catch {
  addon = undefined;
}
const sock = addon?.uiRead ? undefined : new net.Socket({ fd, readable: true, writable: true });

// fdFile is a received file as the runtime reads it (a KarioFile, an
// AudioRing).
const fdFile = (n) => ({
  stat: () => {
    const st = fs.fstatSync(n);
    return { size: st.size, regular: st.isFile() };
  },
  read: (buf, position) => fs.readSync(n, buf, 0, buf.byteLength, position),
  write: (buf, position) => fs.writeSync(n, buf, 0, buf.byteLength, position),
  close: () => fs.closeSync(n),
  // Only a memfd can be sealed; Node.js cannot read the seals themselves.
  sealable: () => fs.readlinkSync(`/proc/self/fd/${n}`).startsWith("/memfd:"),
});

function sendLine(line) {
  if (sock) {
    sock.write(line + "\n");
    return;
  }
  const b = Buffer.from(line + "\n");
  for (let off = 0; off < b.length; ) {
    try {
      off += fs.writeSync(fd, b, off);
    } catch (e) {
      if (e.code !== "EAGAIN") throw e;
    }
  }
}

// writeWhole writes a file whole (a new file, then a rename): a process killed
// meanwhile leaves the old one, never half of the new.
function writeWhole(file, text) {
  const tmp = file + ".tmp";
  try {
    const f = fs.openSync(tmp, "w", 0o600);
    try {
      fs.writeSync(f, text);
      fs.fsyncSync(f);
    } finally {
      fs.closeSync(f);
    }
    fs.renameSync(tmp, file);
  } catch (e) {
    fs.rmSync(tmp, { force: true });
    throw e;
  }
}

// replaceWhole is writeWhole on libuv's threads, as kario-jsrt writes the
// storage on a thread of its own: the app's loop goes on meanwhile.
async function replaceWhole(file, text) {
  const tmp = file + ".tmp";
  try {
    const f = await fs.promises.open(tmp, "w", 0o600);
    try {
      await f.writeFile(text);
      await f.sync();
    } finally {
      await f.close();
    }
    await fs.promises.rename(tmp, file);
  } catch (e) {
    await fs.promises.rm(tmp, { force: true }).catch(() => {});
    throw e;
  }
}

const k = createKario(
  {
    send: sendLine,
    catalog: (lang) => {
      try {
        return fs.readFileSync(path.join(appDir, "locale", lang + ".json"), "utf8");
      } catch {
        return undefined;
      }
    },
    storage: dataDir
      ? {
          load: () => {
            try {
              return fs.readFileSync(path.join(dataDir, STORAGE_FILE), "utf8");
            } catch {
              return undefined;
            }
          },
          save: (text) => replaceWhole(path.join(dataDir, STORAGE_FILE), text),
        }
      : undefined,
    log,
    fetch: nodeFetch,
    // A WebSocket's connection: TLS with Node's CAs (and
    // NODE_EXTRA_CA_CERTS), as fetch has them.
    connect: ({ secure, host, port }) =>
      secure ? tls.connect({ host, port, servername: net.isIP(host) ? undefined : host }) : net.connect({ host, port }),
    cookies: dataDir
      ? {
          load: () => {
            try {
              return fs.readFileSync(path.join(dataDir, COOKIE_FILE), "utf8");
            } catch {
              return undefined;
            }
          },
          save: (text) => writeWhole(path.join(dataDir, COOKIE_FILE), text),
        }
      : undefined,
  },
  { appId: process.env.PHONE_APP_ID, lang: process.env.PHONE_LANG, net: manifestHas("net"), forms: process.env.PHONE_UI_TEST_FORMS },
);

// One message a turn, as kario-jsrt runs the jobs a message started before
// the next: what an awaited call or a rejection does comes first.
const lines = [];
let turn = false;
function next() {
  turn = false;
  const m = lines.shift();
  if (m === undefined) return;
  k.receive(m.line, m.files);
  if (lines.length) schedule();
}
function schedule() {
  if (!turn) {
    turn = true;
    setImmediate(next);
  }
}
// Files arrive with their message's first bytes, so the files received so
// far, oldest first, are the ones a whole line counts in its "fds" (as
// libphoneui and the Go SDK take them).
const MAX_UNCLAIMED = 32;
let pending = Buffer.alloc(0);
const unclaimed = [];
function take(chunk) {
  pending = pending.length ? Buffer.concat([pending, chunk]) : chunk;
  for (let nl = pending.indexOf(10); nl >= 0; nl = pending.indexOf(10)) {
    const line = pending.subarray(0, nl).toString("utf8");
    pending = pending.subarray(nl + 1);
    if (!line) continue;
    let files = [];
    if (unclaimed.length) {
      let n = 0;
      try {
        n = Math.trunc(Number(JSON.parse(line).fds)) || 0;
      } catch {
        // Not JSON: receive reports it; it claims no file.
      }
      files = unclaimed.splice(0, Math.min(Math.max(n, 0), 8)).map(fdFile);
    }
    lines.push({ line, files });
  }
  schedule();
}
// written: what the app stored so far is written (or failed to be).
const written = () => k.exports.storage.flush().catch(() => {});
// The Shell closed the connection: the app ends, its timers with it, once
// what it stored is on disk.
function gone() {
  k.close();
  written().then(() => process.exit(0));
}
// SIGTERM (the Shell stopping) ends the app as the signal would have, once
// what it stored is on disk; under --cpu-prof with an exit instead, as V8
// writes the profile when the process exits, not when a signal ends it
// (kario run -profile).
const profiling = process.execArgv.some((a) => a.startsWith("--cpu-prof"));
process.on("SIGTERM", () => {
  written().then(() => {
    if (profiling) process.exit(0);
    process.removeAllListeners("SIGTERM");
    process.kill(process.pid, "SIGTERM");
  });
});
if (sock) {
  sock.on("data", take);
  sock.on("close", gone);
  sock.on("error", () => k.close());
} else {
  addon.uiRead(fd, (bytes, fds) => {
    for (const n of fds) {
      if (unclaimed.length < MAX_UNCLAIMED) unclaimed.push(n);
      else fs.closeSync(n);
    }
    if (bytes === undefined) gone();
    else take(Buffer.from(bytes));
  });
}

export const { appId, lang, reset, push, replace, update, pop, toast, notify, call, on, tr, storage, cancelled, fetch, WebSocket, Blob, File, FormData, Headers, Request, Response } = k.exports;
