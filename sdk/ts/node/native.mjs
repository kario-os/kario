// Native modules under Node.js (kario run, kario test): "native:<name>" is
// lib/<arch>/<name>.so of the app's directory, checked as kario-jsrt checks
// it (jsrt/src/native.c, with the same messages) and loaded through
// kario-native.node (native.c), which kario builds beside this file.
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import process from "node:process";

export const SCHEME = "native:";
// The URL a native import resolves to, for the load hook.
export const URL_SCHEME = "kario-native:";
const ARCH = { x64: "x86_64", arm64: "arm64" }[process.arch] ?? "";
const MACHINES = { 62: "x86_64", 183: "arm64", 40: "32-bit arm", 3: "32-bit x86", 243: "riscv" };
const NAME = /^[a-z][a-z0-9_]{0,31}$/;
const loaded = new Map();
let addon;

function declared(appDir, name) {
  try {
    const m = JSON.parse(fs.readFileSync(path.join(appDir, "manifest.json"), "utf8"));
    return Array.isArray(m.native) && m.native.includes(name);
  } catch {
    return false;
  }
}

function checkELF(file) {
  const h = Buffer.alloc(20);
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  let got;
  try {
    got = fs.readSync(fd, h, 0, 20, 0);
  } finally {
    fs.closeSync(fd);
  }
  if (got !== 20 || h.readUInt32BE(0) !== 0x7f454c46) return "not an ELF library";
  if (h[5] !== 1) return "a big-endian library";
  const machine = h.readUInt16LE(18);
  if (h[4] !== 2 || MACHINES[machine] !== ARCH) return `a library for ${MACHINES[machine] ?? "another machine"}, not ${ARCH}`;
  if (h.readUInt16LE(16) !== 3) return "not a shared library (link it with -shared)";
  return undefined;
}

/** loadNative is the exports of the app's module spec ("native:<name>"), or throws why not. */
export function loadNative(spec, appDir) {
  if (loaded.has(spec)) return loaded.get(spec);
  const name = spec.slice(SCHEME.length);
  if (!NAME.test(name)) throw new Error(`could not load module '${spec}': a native module's name is [a-z][a-z0-9_]*, at most 32 characters`);
  if (!ARCH) throw new Error(`${spec}: native modules are built for arm64 and x86_64, not ${process.arch}`);
  if (!declared(appDir, name)) throw new Error(`${spec}: the manifest does not declare it ("native": ["${name}"])`);
  const rel = `lib/${ARCH}/${name}.so`;
  const file = path.join(fs.realpathSync(appDir), rel);
  let st;
  try {
    st = fs.lstatSync(file);
  } catch (e) {
    throw new Error(`${spec}: no ${rel} in the app's package (${e.code})`);
  }
  let real;
  try {
    real = fs.realpathSync(file);
  } catch {}
  if (!st.isFile() || real !== file) throw new Error(`${spec}: ${rel} is not a file of the app's package`);
  const bad = checkELF(file);
  if (bad) throw new Error(`${spec}: ${rel} is ${bad}`);
  if (!addon) {
    const at = new URL("./kario-native.node", import.meta.url);
    if (!fs.existsSync(at)) {
      throw new Error(`${spec}: native modules load under Node.js through kario-native.node, which kario builds with a C compiler; it is not there (kario run -engine jsrt runs the app on kario-jsrt instead)`);
    }
    addon = createRequire(import.meta.url)(at.pathname);
  }
  const exports = addon.load(file, spec, rel);
  loaded.set(spec, exports);
  return exports;
}

/** source is the ES module the load hook gives for a native import. */
export function source(spec, appDir) {
  const names = Object.keys(loadNative(spec, appDir));
  globalThis.__karioNative ??= new Map();
  globalThis.__karioNative.set(spec, loaded.get(spec));
  return [
    `const m = globalThis.__karioNative.get(${JSON.stringify(spec)});`,
    ...names.map((n, i) => `const v${i} = m[${JSON.stringify(n)}];`),
    `export { ${names.map((n, i) => `v${i} as ${n}`).join(", ")} };`,
  ].join("\n");
}
