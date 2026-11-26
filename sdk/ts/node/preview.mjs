// node preview.mjs PREVIEW.mjs APPDIR: what kario preview runs to evaluate
// an app's previews (KarioUI's preview()): PREVIEW.mjs is the app's code
// with its src/previews.ts, bundled by kario; APPDIR the app as kario build
// wrote it (manifest, catalogs). "kario" is a fake Shell's (fake.mjs) that
// nothing drives: the app does not start, its pages are only evaluated. It
// prints the previews' screens as one line of JSON.
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { register } from "./hooks.mjs";
import { format } from "./runtime.mjs";

const [bundle, appDir] = process.argv.slice(2);
if (!bundle || !appDir) {
  process.stderr.write("usage: node preview.mjs PREVIEW.mjs APPDIR\n");
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
  shells: new Map(),
  lang: process.env.KARIO_PREVIEW_LANG || "en",
  manifest: () => (manifest ??= JSON.parse(read("manifest.json") ?? "{}")),
  readFile: read,
};
const fake = new URL("./fake.mjs", import.meta.url).href;
if (!register((specifier) => (specifier === "kario" ? `${fake}?kario=0` : undefined), appDir)) {
  process.stderr.write(`kario preview: Node.js ${process.versions.node} has no module.registerHooks (22.15 or newer)\n`);
  process.exit(1);
}
try {
  const m = await import(pathToFileURL(path.resolve(bundle)).href);
  process.stdout.write(JSON.stringify(m.renderPreviews()) + "\n");
} catch (e) {
  process.stderr.write(`kario preview: the app's code failed: ${format(e)}\n`);
  process.exit(1);
}
