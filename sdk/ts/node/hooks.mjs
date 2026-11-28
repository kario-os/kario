// Module resolution for apps under Node.js: "kario" is the runtime's module
// (kario.mjs for kario run, fake.mjs for kario test), "native:<name>" one of
// the app's native modules (native.mjs), and an app's main.js imports
// nothing else, as on kario-jsrt.
import module from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { SCHEME, URL_SCHEME, source } from "./native.mjs";

/**
 * register makes map(specifier, parentURL) answer imports first: a URL, or
 * undefined for Node's own resolution; native imports load from appDir.
 * False when this Node.js has no synchronous hooks (module.registerHooks:
 * 22.15, 23.5 and newer).
 */
export function register(map, appDir) {
  if (typeof module.registerHooks !== "function") return false;
  module.registerHooks({
    resolve(specifier, context, next) {
      if (specifier.startsWith(SCHEME)) return { url: URL_SCHEME + specifier.slice(SCHEME.length), shortCircuit: true };
      const url = map(specifier, context.parentURL);
      return url ? { url, shortCircuit: true } : next(specifier, context);
    },
    load(url, context, next) {
      if (!url.startsWith(URL_SCHEME)) return next(url, context);
      return { format: "module", source: source(SCHEME + url.slice(URL_SCHEME.length), appDir), shortCircuit: true };
    },
  });
  return true;
}

/** appOnly refuses an import from the app's own code (anything in appDir). */
export function appOnly(specifier, parentURL, appDir) {
  if (parentURL && parentURL.startsWith(pathToFileURL(appDir + path.sep).href)) {
    throw new Error(`could not load module '${specifier}': an app imports only "kario" and its native modules ("native:<name>")`);
  }
  return undefined;
}
