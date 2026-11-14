// node run.mjs APP/main.js: a TypeScript app under Node.js as the Shell
// starts it (kario run), the module "kario" answered by kario.mjs, so the
// app can be debugged with --inspect. As on kario-jsrt, main.js imports
// nothing else; a main module that throws or rejects ends the process.
import path from "node:path";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { appOnly, register } from "./hooks.mjs";
import { format } from "./runtime.mjs";

const main = process.argv[2];
if (!main) {
  process.stderr.write("usage: node run.mjs APP/main.js\n");
  process.exit(2);
}
const appDir = path.dirname(path.resolve(main));
process.env.KARIO_APP_DIR = appDir;
const kario = new URL("./kario.mjs", import.meta.url).href;
if (!register((specifier, parent) => (specifier === "kario" ? kario : appOnly(specifier, parent, appDir)))) {
  process.stderr.write(`kario-node: Node.js ${process.versions.node} has no module.registerHooks (22.15 or newer)\n`);
  process.exit(1);
}
process.on("unhandledRejection", (reason) => process.stderr.write(`kario-node: unhandled promise rejection: ${format(reason)}\n`));

try {
  await import("kario");
  await import(pathToFileURL(path.resolve(main)).href);
} catch (e) {
  process.stderr.write(`kario-node: the app failed to start: ${format(e)}\n`);
  process.exit(1);
}
