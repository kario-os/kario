// node --import test.mjs --test ...: what kario test sets up before an app's
// test files run. KARIO_TEST_APP is the app built for the test; its "kario"
// is fake.mjs, a test's fake Shell (@kario/test), and as on kario-jsrt its
// main.js imports nothing else.
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { test } from "node:test";
import { pathToFileURL } from "node:url";
import { appOnly, register } from "./hooks.mjs";

const appDir = process.env.KARIO_TEST_APP;
if (!appDir) {
  process.stderr.write("kario test: no KARIO_TEST_APP\n");
  process.exit(2);
}
const read = (rel) => {
  try {
    return fs.readFileSync(path.join(appDir, rel), "utf8");
  } catch {
    return undefined;
  }
};
let manifest;
globalThis.__karioTest = {
  test,
  shells: new Map(),
  appURL: pathToFileURL(path.join(appDir, "main.js")).href,
  lang: process.env.KARIO_TEST_LANG || "en",
  manifest: () => (manifest ??= JSON.parse(read("manifest.json") ?? "{}")),
  readFile: read,
  immediate: (fn) => setImmediate(fn),
  later: (fn, ms) => setTimeout(fn, ms),
};
const fake = new URL("./fake.mjs", import.meta.url).href;
if (
  !register((specifier, parent) => {
    if (specifier !== "kario") return appOnly(specifier, parent, appDir);
    const n = parent ? new URL(parent).searchParams.get("kario") : null;
    return `${fake}?kario=${n ?? "0"}`;
  })
) {
  process.stderr.write(`kario test: Node.js ${process.versions.node} has no module.registerHooks (22.15 or newer)\n`);
  process.exit(1);
}
