// What Kario Studio decides without VS Code: which project a file belongs
// to, the kario command lines, the debugger's configuration, what kario's
// output says. extension.ts wires these to the editor; test/ checks them
// with node:test alone.
import * as path from "node:path";

export interface Settings {
  cli: string;
  shell: string;
  renderer: string;
  node: string;
  device: string;
  /** A SKU to simulate (kario run -sku); it takes the place of device. */
  sku: string;
  language: string;
  previewAddress: string;
  inspectAddress: string;
  devStore: string;
  key: string;
  cert: string;
  storeUrl: string;
  rollout: number;
  reloadOnSave: boolean;
}

export const defaults: Settings = {
  cli: "kario",
  shell: "",
  renderer: "",
  node: "",
  device: "bar-t9",
  sku: "",
  language: "",
  previewAddress: "127.0.0.1:8790",
  inspectAddress: "127.0.0.1:9229",
  devStore: "",
  key: "dev.key",
  cert: "dev.cert",
  storeUrl: "",
  rollout: 100,
  reloadOnSave: true,
};

export interface Manifest {
  id?: string;
  name?: string;
  version?: string;
  runtime?: string;
}

/** The file system findProject looks at. */
export interface Files {
  exists(p: string): boolean;
  read(p: string): string | undefined;
}

/**
 * findProject is the Kario TypeScript project around a file or directory:
 * the nearest directory up with a manifest.json saying "runtime": "js" and
 * src/main.ts.
 */
export function findProject(start: string, fs: Files): string | undefined {
  for (let dir = path.resolve(start); ; dir = path.dirname(dir)) {
    const m = readManifest(dir, fs);
    if (m?.runtime === "js" && fs.exists(path.join(dir, "src", "main.ts"))) return dir;
    if (path.dirname(dir) === dir) return undefined;
  }
}

export function readManifest(dir: string, fs: Files): Manifest | undefined {
  const raw = fs.read(path.join(dir, "manifest.json"));
  if (raw === undefined) return undefined;
  try {
    const m = JSON.parse(raw);
    return m && typeof m === "object" ? (m as Manifest) : undefined;
  } catch {
    return undefined;
  }
}

/** packageFile is where Package writes a project's .phoneapp: build/<id>-<version>.phoneapp. */
export function packageFile(dir: string, m: Manifest): string {
  const name = `${m.id ?? "app"}${m.version ? "-" + m.version : ""}.phoneapp`;
  return path.join(dir, "build", name.replace(/[^A-Za-z0-9._-]/g, "_"));
}

function common(s: Settings): string[] {
  const args: string[] = [];
  if (s.shell) args.push("-shell", s.shell);
  if (s.node) args.push("-node", s.node);
  if (s.sku) args.push("-sku", s.sku);
  else if (s.device) args.push("-device", s.device);
  if (s.language) args.push("-lang", s.language);
  return args;
}

/** A SKU as kario skus -json lists it. */
export interface Sku {
  id: string;
  device: string;
  form: string;
  width: number;
  height: number;
  memory_mb: number;
  board: string;
  display: string;
  keypad: string;
  product: string;
  region: string;
  emulator: boolean;
}

/** parseSkus reads kario skus -json. */
export function parseSkus(text: string): Sku[] {
  const v = JSON.parse(text);
  if (!Array.isArray(v) || !v.every((s) => s && typeof s.id === "string" && typeof s.device === "string")) {
    throw new Error("kario skus -json: not a list of SKUs");
  }
  return v as Sku[];
}

export const DEVICES = ["bar-t9", "bar-qwerty", "communicator"];

export interface DevicePick {
  label: string;
  description: string;
  detail?: string;
  /** What the pick sets: a SKU, or only a form factor (sku ""). */
  sku: string;
  device: string;
  picked: boolean;
}

/** devicePicks: the SKUs, then the bare form factors, the one in force marked. */
export function devicePicks(skus: Sku[], s: Settings): DevicePick[] {
  const out: DevicePick[] = skus.map((k) => ({
    label: k.id,
    description: `${k.device} ${k.width}×${k.height}, ${k.form} keypad, ${k.memory_mb / 1024} GB`,
    detail: `${k.board}${k.emulator ? " (emulator)" : ""} · ${k.display} · ${k.keypad} · ${k.product}`,
    sku: k.id,
    device: k.device,
    picked: s.sku === k.id,
  }));
  for (const d of DEVICES) out.push({ label: d, description: "form factor only (no SKU)", sku: "", device: d, picked: !s.sku && s.device === d });
  return out;
}

/** deviceLabel is the status bar's word for the device in force. */
export function deviceLabel(s: Settings): string {
  return s.sku || s.device || "bar-qwerty";
}

export function buildArgs(dir: string): string[] {
  return ["build", "-minify=false", "-sourcemap", dir];
}

/** runArgs: the app in the terminal's text UI. */
export function runArgs(dir: string, s: Settings): string[] {
  return ["run", ...common(s), dir];
}

/**
 * previewArgs: the pixel UI on a local page the panel shows, built again and
 * restarted on its pages when a source is saved; debug adds Node.js's inspector.
 */
export function previewArgs(dir: string, s: Settings, debug: boolean): string[] {
  const args = ["run", ...common(s), "-preview", s.previewAddress];
  if (s.renderer) args.push("-gui", s.renderer);
  if (s.reloadOnSave) args.push("-watch");
  if (debug) args.push("-inspect", s.inspectAddress, "-brk");
  return [...args, dir];
}

/** profileFile is where a profiling run's timeline goes: outside build/, which every build empties. */
export function profileFile(dir: string): string {
  return path.join(dir, ".kario", "profile", "trace.json");
}

/** profileArgs: the preview with the performance timeline (kario run -profile), not rebuilt on save. */
export function profileArgs(dir: string, s: Settings): string[] {
  const args = previewArgs(dir, { ...s, reloadOnSave: false }, false);
  return [...args.slice(0, -1), "-profile", profileFile(dir), dir];
}

/** viewPreviewArgs: kario preview of the views, on the device in force or (every) on all form factors. */
export function viewPreviewArgs(dir: string, s: Settings, every: boolean): string[] {
  const args = ["preview", "-json"];
  if (s.shell) args.push("-shell", s.shell);
  if (s.node) args.push("-node", s.node);
  if (s.renderer) args.push("-gui", s.renderer);
  if (!every) {
    if (s.sku) args.push("-sku", s.sku);
    else if (s.device) args.push("-device", s.device);
  }
  if (s.language) args.push("-lang", s.language);
  return [...args, dir];
}

/** A picture kario preview -json lists. */
export interface PreviewImage {
  name: string;
  target: string;
  file: string;
  width: number;
  height: number;
}

export function parsePreviewImages(text: string): PreviewImage[] {
  const v = JSON.parse(text);
  if (!Array.isArray(v) || !v.every((i) => i && typeof i.file === "string" && typeof i.target === "string")) {
    throw new Error("kario preview -json: not a list of pictures");
  }
  return v as PreviewImage[];
}

/** viewPreviewHTML is the views panel: the pictures by device, src turning a file into the webview's URI. */
export function viewPreviewHTML(images: PreviewImage[], src: (file: string) => string, nonce: string, cspSource: string): string {
  const esc = (t: string) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  const targets = [...new Set(images.map((i) => i.target))];
  const body = targets
    .map((t) => {
      const figs = images
        .filter((i) => i.target === t)
        .map((i) => `<figure><img src="${esc(src(i.file))}" width="${i.width / 2}" height="${i.height / 2}" alt="${esc(i.name)}"><figcaption>${esc(i.name)}</figcaption></figure>`)
        .join("");
      return `<h3>${esc(t)}</h3><div class="row">${figs}</div>`;
    })
    .join("\n");
  return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${cspSource}; style-src 'nonce-${nonce}';">
<style nonce="${nonce}">body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); } .row { display: flex; flex-wrap: wrap; gap: 1em; } figure { margin: 0; } figcaption { text-align: center; opacity: .8; }</style>
</head>
<body>${body || "<p>No previews.</p>"}</body>
</html>`;
}

export function testArgs(dir: string, s: Settings, only?: string): string[] {
  const args = ["test"];
  if (s.node) args.push("-node", s.node);
  if (only) args.push("-run", only);
  return [...args, dir];
}

export function packageArgs(dir: string, m: Manifest, s: Settings): string[] {
  const args = ["package", "-o", packageFile(dir, m)];
  if (s.devStore) args.push("-devstore", s.devStore);
  else args.push("-key", path.resolve(dir, s.key), "-cert", path.resolve(dir, s.cert));
  return [...args, dir];
}

/**
 * publishArgs: the package to the store, then its symbol file (kario
 * package writes it beside), signed with the developer's key of the
 * project dir.
 */
export function publishArgs(file: string, s: Settings, dir?: string): string[] {
  if (!s.storeUrl) throw new Error("kario.storeUrl is not set: the store backend's URL");
  const args = ["publish", "-store", s.storeUrl];
  if (s.rollout < 100) args.push("-rollout", String(s.rollout));
  if (dir && !s.devStore) args.push("-key", path.resolve(dir, s.key));
  return [...args, file];
}

/** hostPort splits "127.0.0.1:9229". */
export function hostPort(addr: string): { host: string; port: number } {
  const i = addr.lastIndexOf(":");
  const port = Number(addr.slice(i + 1));
  if (i < 0 || !Number.isInteger(port) || port <= 0 || port > 65535) throw new Error(`not host:port: ${addr}`);
  return { host: addr.slice(0, i) || "127.0.0.1", port };
}

/**
 * attachConfig is the debugger's configuration: VS Code's Node.js debugger
 * attaching to the app, and again to each new build -watch restarts.
 */
export function attachConfig(dir: string, s: Settings): Record<string, unknown> {
  const { host, port } = hostPort(s.inspectAddress);
  return {
    type: "node",
    request: "attach",
    name: "Kario: attach to the app",
    address: host,
    port,
    restart: true,
    sourceMaps: true,
    outFiles: [path.join(dir, "build", "**", "*.js")],
    skipFiles: ["<node_internals>/**"],
    continueOnAttach: true,
    timeout: 30000,
  };
}

/** DEVICE_DEBUG is the debug type of the phone's debugger: kario dap is its adapter. */
export const DEVICE_DEBUG = "kario";

/** dapArgs: the debug adapter's command line (the Debug Adapter Protocol on its standard input and output). */
export function dapArgs(): string[] {
  return ["dap"];
}

/**
 * deviceDebugConfig is the phone's debugger: launch builds the project
 * unminified, installs it with the development store and restarts it under
 * kario-jsrt's debugger over the trusted cable; attach joins the app as it
 * runs on the phone (one not running starts under the debugger).
 */
export function deviceDebugConfig(dir: string, s: Settings, request: "launch" | "attach" = "launch"): Record<string, unknown> {
  const c: Record<string, unknown> = {
    type: DEVICE_DEBUG,
    request,
    name: request === "launch" ? "Kario: debug on the phone" : "Kario: attach on the phone",
    project: dir,
    stopOnEntry: false,
  };
  if (s.devStore) c.devstore = s.devStore;
  return c;
}

/**
 * resolveDeviceConfig fills what a "kario" configuration leaves out: the
 * project (the one in use), the development store (kario.signing.devStore);
 * an empty configuration (F5 without launch.json) becomes a launch.
 */
export function resolveDeviceConfig(cfg: Record<string, unknown>, dir: string | undefined, s: Settings): Record<string, unknown> | undefined {
  if (!cfg.type && !cfg.request && !cfg.name) {
    if (!dir) return undefined;
    return deviceDebugConfig(dir, s);
  }
  const out = { ...cfg };
  if (!out.project) {
    if (!dir) return undefined;
    out.project = dir;
  }
  if (!out.devstore && s.devStore) out.devstore = s.devStore;
  return out;
}

/** launchJSON adds the attach configuration (and the task that starts the app) to a launch.json. */
export function launchJSON(existing: string | undefined, cfg: Record<string, unknown>): string {
  let doc: { version?: string; configurations?: Record<string, unknown>[] } = { version: "0.2.0", configurations: [] };
  if (existing && existing.trim()) {
    doc = JSON.parse(stripComments(existing));
    doc.configurations ??= [];
  }
  const next = { ...cfg, preLaunchTask: "kario: debug" };
  const i = doc.configurations!.findIndex((c) => c.name === cfg.name);
  if (i >= 0) doc.configurations![i] = next;
  else doc.configurations!.push(next);
  return JSON.stringify(doc, null, 2) + "\n";
}

/**
 * debugTask is the background task launch.json's configuration starts first:
 * kario run with the inspector; VS Code waits for the preview's line.
 */
export function debugTask(dir: string, s: Settings): Record<string, unknown> {
  return {
    label: "kario: debug",
    type: "process",
    command: s.cli,
    args: previewArgs(dir, s, true),
    isBackground: true,
    problemMatcher: {
      owner: "kario",
      pattern: { regexp: "^(.+):(\\d+):(\\d+): (.*)$", file: 1, line: 2, column: 3, message: 4 },
      background: { activesBegin: "^kario run: .* under Node\\.js", endsPattern: "^PHONE-PREVIEW: " },
    },
  };
}

/** tasksJSON adds debugTask to a tasks.json. */
export function tasksJSON(existing: string | undefined, task: Record<string, unknown>): string {
  let doc: { version?: string; tasks?: Record<string, unknown>[] } = { version: "2.0.0", tasks: [] };
  if (existing && existing.trim()) {
    doc = JSON.parse(stripComments(existing));
    doc.tasks ??= [];
  }
  const i = doc.tasks!.findIndex((t) => t.label === task.label);
  if (i >= 0) doc.tasks![i] = task;
  else doc.tasks!.push(task);
  return JSON.stringify(doc, null, 2) + "\n";
}

// stripComments drops the // and /* */ comments VS Code allows in its JSON
// files (not inside strings).
export function stripComments(text: string): string {
  let out = "";
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') {
      let j = i + 1;
      while (j < text.length && text[j] !== '"') j += text[j] === "\\" ? 2 : 1;
      out += text.slice(i, j + 1);
      i = j;
    } else if (c === "/" && text[i + 1] === "/") {
      while (i < text.length && text[i] !== "\n") i++;
      out += "\n";
    } else if (c === "/" && text[i + 1] === "*") {
      i = text.indexOf("*/", i + 2);
      if (i < 0) break;
      i++;
    } else {
      out += c;
    }
  }
  return out.replace(/,(\s*[}\]])/g, "$1");
}

/** previewURL is the page kario run's Shell serves, from its "PHONE-PREVIEW: http://…" line. */
export function previewURL(line: string): string | undefined {
  return /^PHONE-PREVIEW: (http:\/\/\S+)/.exec(line)?.[1];
}

export interface Problem {
  file: string;
  line: number;
  column: number;
  message: string;
}

/** problems are the errors in kario's output: esbuild's "file:line:col: message", the manifest's. */
export function problems(output: string, dir: string): Problem[] {
  const out: Problem[] = [];
  for (const raw of output.split("\n")) {
    const line = raw.replace(/^kario: /, "");
    const m = /^(.+?):(\d+):(\d+): (.+)$/.exec(line);
    if (m) {
      out.push({ file: path.resolve(dir, m[1]), line: Number(m[2]), column: Number(m[3]), message: m[4] });
      continue;
    }
    const mf = /^manifest\.json: (.+)$/.exec(line);
    if (mf) out.push({ file: path.join(dir, "manifest.json"), line: 1, column: 1, message: mf[1] });
  }
  return out;
}

/** previewHTML is the panel: the Shell's preview page in a frame. */
export function previewHTML(url: string, nonce: string): string {
  const origin = new URL(url).origin;
  return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; frame-src ${origin}; style-src 'nonce-${nonce}';">
<style nonce="${nonce}">html, body, iframe { margin: 0; padding: 0; border: 0; width: 100%; height: 100%; background: #101218; }</style>
</head>
<body><iframe src="${url}" title="Kario"></iframe></body>
</html>`;
}

/** cliProblem says why the kario command (kario.cli) did not start, from spawn's error. */
export function cliProblem(cli: string, err: { code?: string; message: string }): string {
  if (err.code === "ENOENT") {
    return `no ${cli} command (the setting kario.cli): run make install-kario in the Kario tree, or set kario.cli to its build/bin/kario`;
  }
  return `${cli}: ${err.message}`;
}

/** escapeArg quotes an argument for a terminal (POSIX shells). */
export function escapeArg(a: string): string {
  return /^[A-Za-z0-9_./:=@+-]+$/.test(a) ? a : `'${a.replace(/'/g, `'\\''`)}'`;
}
