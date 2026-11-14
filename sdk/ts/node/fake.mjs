// The module "kario" under kario test: the same runtime as kario run, its
// other end a test's fake Shell (@kario/test) instead of the socket. Each
// launch of the app imports main.js?kario=<n>, whose "kario" is
// fake.mjs?kario=<n>, that launch's Shell; "kario" imported by a test
// itself (a pure module that translates) is ?kario=0, which has no Shell.
import { createKario } from "./runtime.mjs";

const host = globalThis.__karioTest;
const id = new URL(import.meta.url).searchParams.get("kario") ?? "0";
const store = {};
const shell = host.shells.get(id) ?? {
  appId: host.manifest()?.id ?? "",
  lang: host.lang,
  storage: {
    load: () => store.text,
    save: (text) => {
      store.text = text;
    },
  },
  fromApp() {},
  attach() {},
  log: (line) => process.stderr.write(line + "\n"),
};

const k = createKario(
  {
    send: (line) => shell.fromApp(line),
    catalog: (lang) => host.readFile(`locale/${lang}.json`),
    storage: shell.storage,
    log: (line) => shell.log(line),
  },
  { appId: shell.appId, lang: shell.lang },
);
shell.attach(
  (line) => k.receive(line),
  () => k.close(),
);

export const { appId, lang, reset, push, replace, update, pop, toast, notify, call, on, tr, storage, cancelled } = k.exports;
