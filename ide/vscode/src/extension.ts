// Kario Studio (29-app-development.md §3): the kario command line behind
// the editor's commands. New project, build (errors in the Problems view),
// run in a terminal or in the preview panel (the Shell's pixel UI), debug
// under Node.js (VS Code's debugger attaches to the app the Shell started)
// or on a phone in developer mode (the "kario" debug type, whose adapter is
// kario dap), tests against a fake Shell, a signed package and its
// submission to the store.
import { ChildProcess, spawn } from "node:child_process";
import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import * as logic from "./logic";
import * as manifest from "./manifest";

const files: logic.Files = {
  exists: (p) => fs.existsSync(p),
  read: (p) => {
    try {
      return fs.readFileSync(p, "utf8");
    } catch {
      return undefined;
    }
  },
};

let output: vscode.OutputChannel;
let diagnostics: vscode.DiagnosticCollection;
let running: { proc: ChildProcess; panel?: vscode.WebviewPanel } | undefined;

function settings(): logic.Settings {
  const c = vscode.workspace.getConfiguration("kario");
  const d = logic.defaults;
  return {
    cli: c.get("cli", d.cli),
    shell: c.get("shell", d.shell),
    renderer: c.get("renderer", d.renderer),
    node: c.get("node", d.node),
    device: c.get("device", d.device),
    sku: c.get("sku", d.sku),
    language: c.get("language", d.language),
    previewAddress: c.get("previewAddress", d.previewAddress),
    inspectAddress: c.get("inspectAddress", d.inspectAddress),
    devStore: c.get("signing.devStore", d.devStore),
    key: c.get("signing.key", d.key),
    cert: c.get("signing.cert", d.cert),
    storeUrl: c.get("storeUrl", d.storeUrl),
    rollout: c.get("rollout", d.rollout),
    reloadOnSave: c.get("reloadOnSave", d.reloadOnSave),
  };
}

let lastProject: string | undefined;

/**
 * project is the Kario project of the active file, else the one last used
 * (a panel of it has the focus), else of the first workspace folder that is one.
 */
function project(): string | undefined {
  const starts = [vscode.window.activeTextEditor?.document.uri.fsPath, lastProject, ...(vscode.workspace.workspaceFolders ?? []).map((f) => f.uri.fsPath)];
  for (const s of starts) {
    const dir = s && logic.findProject(s, files);
    if (dir) return (lastProject = dir);
  }
  void vscode.window.showErrorMessage("No Kario app here: a folder with manifest.json (\"runtime\": \"js\") and src/main.ts. Kario: New App makes one.");
  return undefined;
}

/** kario runs the command line, its output in the Kario channel; resolves with what it printed. */
function kario(args: string[], cwd: string): Promise<{ code: number; text: string }> {
  const s = settings();
  output.appendLine(`$ ${[s.cli, ...args].map(logic.escapeArg).join(" ")}`);
  return new Promise((resolve) => {
    const p = spawn(s.cli, args, { cwd });
    let text = "";
    const take = (b: Buffer) => {
      text += b.toString();
      output.append(b.toString());
    };
    p.stdout.on("data", take);
    p.stderr.on("data", take);
    p.on("error", (e) => {
      const why = logic.cliProblem(s.cli, e);
      output.appendLine(why);
      void vscode.window.showErrorMessage(`Kario: ${why}`);
      resolve({ code: -1, text: why });
    });
    p.on("close", (code) => resolve({ code: code ?? -1, text }));
  });
}

/** capture runs the command line for its output alone: stdin given, stdout and stderr apart, nothing in the channel. */
function capture(args: string[], cwd: string, stdin = ""): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve) => {
    const p = spawn(settings().cli, args, { cwd });
    let stdout = "";
    let stderr = "";
    p.stdout.on("data", (b: Buffer) => (stdout += b.toString()));
    p.stderr.on("data", (b: Buffer) => (stderr += b.toString()));
    p.on("error", (e) => resolve({ code: -1, stdout, stderr: logic.cliProblem(settings().cli, e) }));
    p.on("close", (code) => resolve({ code: code ?? -1, stdout, stderr }));
    p.stdin.on("error", () => undefined);
    p.stdin.end(stdin);
  });
}

function report(dir: string, text: string): void {
  diagnostics.clear();
  const byFile = new Map<string, vscode.Diagnostic[]>();
  for (const p of logic.problems(text, dir)) {
    const at = new vscode.Position(Math.max(0, p.line - 1), Math.max(0, p.column - 1));
    const list = byFile.get(p.file) ?? [];
    list.push(new vscode.Diagnostic(new vscode.Range(at, at), p.message, vscode.DiagnosticSeverity.Error));
    byFile.set(p.file, list);
  }
  for (const [file, list] of byFile) diagnostics.set(vscode.Uri.file(file), list);
}

async function build(dir: string): Promise<boolean> {
  const r = await kario(logic.buildArgs(dir), dir);
  report(dir, r.text);
  if (r.code !== 0) {
    output.show(true);
    if (r.code !== -1) void vscode.window.showErrorMessage("Kario: the build failed (see Problems)");
  }
  return r.code === 0;
}

function terminal(name: string, args: string[], cwd: string): void {
  const t = vscode.window.createTerminal({ name, cwd });
  t.show();
  t.sendText([settings().cli, ...args].map(logic.escapeArg).join(" "));
}

function stop(): void {
  if (!running) return;
  running.proc.kill("SIGTERM");
  running.panel?.dispose();
  running = undefined;
}

/** start runs the app in the Shell's preview mode (kario run args) and resolves with its page's address; ended runs onEnd. */
function start(dir: string, args: string[], onEnd?: () => void): Promise<string> {
  stop();
  const s = settings();
  output.appendLine(`$ ${[s.cli, ...args].map(logic.escapeArg).join(" ")}`);
  const proc = spawn(s.cli, args, { cwd: dir, stdio: ["ignore", "pipe", "pipe"] });
  running = { proc };
  return new Promise((resolve, reject) => {
    let seen = "";
    const take = (b: Buffer) => {
      output.append(b.toString());
      seen += b.toString();
      for (const line of seen.split("\n")) {
        const url = logic.previewURL(line);
        if (url) {
          seen = "";
          resolve(url);
        }
      }
    };
    proc.stdout!.on("data", take);
    proc.stderr!.on("data", take);
    proc.on("error", (e) => reject(new Error(logic.cliProblem(s.cli, e))));
    proc.on("close", (code) => {
      if (running?.proc === proc) running = undefined;
      report(dir, seen);
      onEnd?.();
      reject(new Error(`kario run ended (${code})`));
    });
  });
}

async function panel(url: string, title: string): Promise<void> {
  const external = await vscode.env.asExternalUri(vscode.Uri.parse(url));
  const p = vscode.window.createWebviewPanel("karioPreview", title, vscode.ViewColumn.Beside, { enableScripts: false, retainContextWhenHidden: true });
  p.webview.html = logic.previewHTML(external.toString(), crypto.randomBytes(16).toString("base64"));
  p.onDidDispose(() => {
    if (running?.panel === p) stop();
  });
  if (running) running.panel = p;
}

async function preview(debug: boolean, profile = false): Promise<void> {
  const dir = project();
  if (!dir || !(await build(dir))) return;
  const m = logic.readManifest(dir, files);
  const s = settings();
  const args = profile ? logic.profileArgs(dir, s) : logic.previewArgs(dir, s, debug);
  try {
    const url = await start(dir, args, profile ? () => void profiled(dir) : undefined);
    await panel(url, `Kario: ${m?.name ?? path.basename(dir)}`);
    if (debug) {
      const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(dir));
      await vscode.debug.startDebugging(folder, logic.attachConfig(dir, settings()) as vscode.DebugConfiguration);
    }
  } catch (e) {
    output.show(true);
    void vscode.window.showErrorMessage(`Kario: ${(e as Error).message}`);
  }
}

/** profiled offers the timeline a profiling run wrote: Perfetto, the file, the JavaScript CPU profile. */
async function profiled(dir: string): Promise<void> {
  const trace = logic.profileFile(dir);
  if (!fs.existsSync(trace)) return;
  const cpuDir = trace.replace(/\.json$/, ".cpuprofiles");
  const cpu = fs.existsSync(cpuDir) ? fs.readdirSync(cpuDir).filter((f) => f.endsWith(".cpuprofile")).sort().map((f) => path.join(cpuDir, f)) : [];
  const choice = await vscode.window.showInformationMessage(
    `Kario: the timeline is in ${path.relative(dir, trace)}`,
    "Open Perfetto",
    "Reveal the Trace",
    ...(cpu.length ? ["Open the CPU Profile"] : []),
  );
  if (choice === "Open Perfetto") void vscode.env.openExternal(vscode.Uri.parse("https://ui.perfetto.dev"));
  if (choice === "Reveal the Trace") void vscode.commands.executeCommand("revealFileInOS", vscode.Uri.file(trace));
  if (choice === "Open the CPU Profile") void vscode.commands.executeCommand("vscode.open", vscode.Uri.file(cpu[cpu.length - 1]));
}

let views: { panel: vscode.WebviewPanel; dir: string; every: boolean; busy: boolean; again: boolean } | undefined;

/** viewPreview draws the previews (kario preview) and shows them; saving a source draws them again. */
async function viewPreview(every: boolean): Promise<void> {
  const dir = project();
  if (!dir) return;
  if (!views || views.dir !== dir) {
    views?.panel.dispose();
    const panel = vscode.window.createWebviewPanel("karioViews", "Kario: previews", vscode.ViewColumn.Beside, {
      enableScripts: false,
      localResourceRoots: [vscode.Uri.file(path.join(dir, ".kario"))],
    });
    views = { panel, dir, every, busy: false, again: false };
    panel.onDidDispose(() => {
      if (views?.panel === panel) views = undefined;
    });
  }
  views.every = every;
  views.panel.reveal(undefined, true);
  await drawViews();
}

async function drawViews(): Promise<void> {
  const v = views;
  if (!v) return;
  if (v.busy) {
    v.again = true;
    return;
  }
  v.busy = true;
  try {
    const r = await capture(logic.viewPreviewArgs(v.dir, settings(), v.every), v.dir);
    if (r.code !== 0) {
      output.appendLine(r.stderr);
      report(v.dir, r.stderr);
      void vscode.window.showErrorMessage("Kario: the previews could not be drawn (see the Kario output)");
      return;
    }
    const images = logic.parsePreviewImages(r.stdout);
    const stamp = Date.now();
    v.panel.webview.html = logic.viewPreviewHTML(images, (f) => `${v.panel.webview.asWebviewUri(vscode.Uri.file(f))}?v=${stamp}`, crypto.randomBytes(16).toString("base64"), v.panel.webview.cspSource);
  } finally {
    v.busy = false;
    if (v.again) {
      v.again = false;
      void drawViews();
    }
  }
}

async function newApp(): Promise<void> {
  const parent = await vscode.window.showOpenDialog({ canSelectFolders: true, canSelectFiles: false, openLabel: "Create the app in this folder" });
  if (!parent?.length) return;
  const name = await vscode.window.showInputBox({ prompt: "The app's directory name", value: "my-app", validateInput: (v) => (/^[\w.-]+$/.test(v) ? undefined : "Letters, digits, . _ -") });
  if (!name) return;
  const dir = path.join(parent[0].fsPath, name);
  const r = await kario(["new", dir], parent[0].fsPath);
  if (r.code !== 0) {
    output.show(true);
    return;
  }
  await vscode.commands.executeCommand("vscode.openFolder", vscode.Uri.file(dir), { forceNewWindow: !!vscode.workspace.workspaceFolders?.length });
}

async function packageApp(): Promise<string | undefined> {
  const dir = project();
  const m = dir && logic.readManifest(dir, files);
  if (!dir || !m) return undefined;
  const r = await kario(logic.packageArgs(dir, m, settings()), dir);
  report(dir, r.text);
  if (r.code !== 0) {
    output.show(true);
    if (r.code !== -1) void vscode.window.showErrorMessage("Kario: packaging failed (signing: kario.signing.*)");
    return undefined;
  }
  const file = logic.packageFile(dir, m);
  void vscode.window.showInformationMessage(`Kario: ${path.relative(dir, file)}`);
  return file;
}

async function publish(): Promise<void> {
  const file = await packageApp();
  if (!file) return;
  try {
    const dir = project();
    const args = logic.publishArgs(file, settings(), dir);
    const r = await kario(args, path.dirname(file));
    output.show(true);
    if (r.code === 0) void vscode.window.showInformationMessage(`Kario: ${r.text.trim().split("\n").slice(-2).join(" · ")}`);
    else void vscode.window.showErrorMessage(`Kario: ${r.text.trim().split("\n").pop()}`);
  } catch (e) {
    void vscode.window.showErrorMessage(`Kario: ${(e as Error).message}`);
  }
}

async function launchConfig(): Promise<void> {
  const dir = project();
  const folder = dir && vscode.workspace.getWorkspaceFolder(vscode.Uri.file(dir));
  if (!dir || !folder) return;
  const vs = path.join(folder.uri.fsPath, ".vscode");
  fs.mkdirSync(vs, { recursive: true });
  const s = settings();
  const launch = path.join(vs, "launch.json");
  const tasks = path.join(vs, "tasks.json");
  fs.writeFileSync(launch, logic.launchJSON(files.read(launch), logic.attachConfig(dir, s)));
  fs.writeFileSync(tasks, logic.tasksJSON(files.read(tasks), logic.debugTask(dir, s)));
  await vscode.window.showTextDocument(vscode.Uri.file(launch));
}

let deviceItem: vscode.StatusBarItem;

function showDevice(): void {
  deviceItem.text = `$(device-mobile) ${logic.deviceLabel(settings())}`;
}

/** pickDevice: a SKU (kario skus -json) or a bare form factor, kept in the workspace's settings. */
async function pickDevice(): Promise<void> {
  const s = settings();
  const cwd = project() ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? process.cwd();
  const r = await capture(["skus", "-json"], cwd);
  let skus: logic.Sku[] = [];
  try {
    skus = logic.parseSkus(r.stdout);
  } catch {
    output.appendLine(r.stderr || r.stdout);
    void vscode.window.showWarningMessage("Kario: kario skus failed (kario.cli); only the form factors are offered");
  }
  const picks = logic.devicePicks(skus, s).map((p) => ({ ...p, label: (p.picked ? "$(check) " : "") + p.label }));
  const choice = await vscode.window.showQuickPick(picks, { title: "Kario: the device the app runs on", matchOnDescription: true, matchOnDetail: true });
  if (!choice) return;
  const c = vscode.workspace.getConfiguration("kario");
  const target = vscode.workspace.workspaceFolders?.length ? vscode.ConfigurationTarget.Workspace : vscode.ConfigurationTarget.Global;
  await c.update("sku", choice.sku, target);
  await c.update("device", choice.device, target);
  showDevice();
}

/** The manifest editor: a form over manifest.json, checked by kario manifest check. */
class ManifestEditor implements vscode.CustomTextEditorProvider {
  async resolveCustomTextEditor(doc: vscode.TextDocument, panel: vscode.WebviewPanel): Promise<void> {
    panel.webview.options = { enableScripts: true };
    const dir = path.dirname(doc.uri.fsPath);
    let last: manifest.Report | undefined;
    let ours = "";
    const check = async (): Promise<manifest.Report | undefined> => {
      const r = await capture(["manifest", "check", "-json", "-"], dir, doc.getText());
      try {
        last = manifest.parseReport(r.stdout);
      } catch {
        output.appendLine(r.stderr || r.stdout);
        return undefined;
      }
      return last;
    };
    const render = async () => {
      const rep = await check();
      const form = manifest.formOf(doc.getText());
      if (!rep || !form) {
        panel.webview.html = `<!DOCTYPE html><body><p>${!rep ? "kario manifest check did not answer: set kario.cli to the kario program (see the Kario output)." : "manifest.json is not a JSON object: fix it in the text editor."}</p></body>`;
        return;
      }
      panel.webview.html = manifest.editorHTML(form, rep, crypto.randomBytes(16).toString("base64"), panel.webview.cspSource);
    };
    const sub = vscode.workspace.onDidChangeTextDocument(async (e) => {
      if (e.document.uri.toString() !== doc.uri.toString() || e.contentChanges.length === 0) return;
      if (doc.getText() === ours) {
        const rep = await check();
        if (rep) void panel.webview.postMessage({ type: "problems", problems: rep.problems });
        return;
      }
      await render();
    });
    panel.onDidDispose(() => sub.dispose());
    panel.webview.onDidReceiveMessage(async (m: { type: string; form: manifest.Form }) => {
      if (m.type !== "edit" || !last) return;
      const next = manifest.applyForm(doc.getText(), m.form, last.schema);
      if (next === doc.getText()) return;
      ours = next;
      const edit = new vscode.WorkspaceEdit();
      edit.replace(doc.uri, new vscode.Range(0, 0, doc.lineCount, 0), next);
      await vscode.workspace.applyEdit(edit);
    });
    await render();
  }
}

/** debugDevice debugs the project on the phone (kario dap). */
async function debugDevice(request: "launch" | "attach"): Promise<void> {
  const dir = project();
  if (!dir) return;
  const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(dir));
  await vscode.debug.startDebugging(folder, logic.deviceDebugConfig(dir, settings(), request) as vscode.DebugConfiguration);
}

async function editManifest(): Promise<void> {
  const dir = project();
  if (dir) await vscode.commands.executeCommand("vscode.openWith", vscode.Uri.file(path.join(dir, "manifest.json")), "kario.manifestEditor");
}

export function activate(ctx: vscode.ExtensionContext): void {
  output = vscode.window.createOutputChannel("Kario");
  diagnostics = vscode.languages.createDiagnosticCollection("kario");
  const cmd = (id: string, fn: () => unknown) => ctx.subscriptions.push(vscode.commands.registerCommand(id, fn));
  deviceItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 10);
  deviceItem.command = "kario.pickDevice";
  deviceItem.tooltip = "Kario: the device the app runs on (SKU or form factor)";
  showDevice();
  deviceItem.show();
  ctx.subscriptions.push(
    deviceItem,
    vscode.workspace.onDidChangeConfiguration((e) => e.affectsConfiguration("kario") && showDevice()),
    vscode.window.registerCustomEditorProvider("kario.manifestEditor", new ManifestEditor(), { webviewOptions: { retainContextWhenHidden: true } }),
  );
  ctx.subscriptions.push(
    vscode.workspace.onDidSaveTextDocument((d) => {
      if (views && d.uri.fsPath.startsWith(path.join(views.dir, "src") + path.sep)) void drawViews();
    }),
  );
  cmd("kario.viewPreview", () => viewPreview(false));
  cmd("kario.viewPreviewAll", () => viewPreview(true));
  cmd("kario.profile", () => preview(false, true));
  cmd("kario.pickDevice", pickDevice);
  cmd("kario.editManifest", editManifest);
  cmd("kario.new", newApp);
  cmd("kario.build", async () => {
    const dir = project();
    if (dir && (await build(dir))) void vscode.window.setStatusBarMessage("Kario: built", 3000);
  });
  cmd("kario.run", async () => {
    const dir = project();
    if (dir) terminal("Kario", logic.runArgs(dir, settings()), dir);
  });
  cmd("kario.preview", () => preview(false));
  cmd("kario.debug", () => preview(true));
  cmd("kario.stop", stop);
  cmd("kario.test", async () => {
    const dir = project();
    if (dir) terminal("Kario tests", logic.testArgs(dir, settings()), dir);
  });
  cmd("kario.package", packageApp);
  cmd("kario.publish", publish);
  cmd("kario.launchConfig", launchConfig);
  cmd("kario.debugDevice", () => debugDevice("launch"));
  cmd("kario.attachDevice", () => debugDevice("attach"));
  ctx.subscriptions.push(
    vscode.debug.registerDebugAdapterDescriptorFactory(logic.DEVICE_DEBUG, {
      createDebugAdapterDescriptor: () => new vscode.DebugAdapterExecutable(settings().cli, logic.dapArgs()),
    }),
    vscode.debug.registerDebugConfigurationProvider(logic.DEVICE_DEBUG, {
      resolveDebugConfiguration: (_folder, cfg) => {
        const c = cfg as Record<string, unknown>;
        const out = logic.resolveDeviceConfig(c, c.project ? undefined : project(), settings());
        return out as vscode.DebugConfiguration | undefined;
      },
    }),
  );
  ctx.subscriptions.push(output, diagnostics, { dispose: stop });
}

export function deactivate(): void {
  stop();
}
