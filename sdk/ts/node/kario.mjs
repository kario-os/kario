// The module "kario" for an app the Shell started under Node.js (kario run):
// the protocol over the socket on PHONE_UI_FD, storage in PHONE_DATA_DIR,
// translations from locale/ beside main.js, the log on stderr (and the
// debugger's console when one is attached).
import fs from "node:fs";
import inspector from "node:inspector";
import net from "node:net";
import path from "node:path";
import process from "node:process";
import { createKario, format } from "./runtime.mjs";

const STORAGE_FILE = "kario-storage.json";
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

const sock = new net.Socket({ fd, readable: true, writable: true });
const k = createKario(
  {
    send: (line) => sock.write(line + "\n"),
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
          // A new file, then a rename: a process killed meanwhile leaves the
          // old one, never half of the new.
          save: (text) => {
            const file = path.join(dataDir, STORAGE_FILE);
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
              throw new Error(`storage: ${e.message}`);
            }
          },
        }
      : undefined,
    log,
  },
  { appId: process.env.PHONE_APP_ID, lang: process.env.PHONE_LANG },
);

// One message a turn, as kario-jsrt runs the jobs a message started before
// the next: what an awaited call or a rejection does comes first.
const lines = [];
let turn = false;
function next() {
  turn = false;
  const line = lines.shift();
  if (line === undefined) return;
  k.receive(line);
  if (lines.length) schedule();
}
function schedule() {
  if (!turn) {
    turn = true;
    setImmediate(next);
  }
}
let pending = "";
sock.setEncoding("utf8");
sock.on("data", (chunk) => {
  pending += chunk;
  for (let nl = pending.indexOf("\n"); nl >= 0; nl = pending.indexOf("\n")) {
    const line = pending.slice(0, nl);
    pending = pending.slice(nl + 1);
    if (line) lines.push(line);
  }
  schedule();
});
// The Shell closed the connection: the app ends, its timers with it.
sock.on("close", () => {
  k.close();
  process.exit(0);
});
sock.on("error", () => k.close());

export const { appId, lang, reset, push, replace, update, pop, toast, notify, call, on, tr, storage, cancelled } = k.exports;
