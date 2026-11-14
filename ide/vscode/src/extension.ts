// Kario Studio (29-app-development.md §3): the kario command line behind
// the editor's commands. New project, build (errors in the Problems view),
// run in a terminal or in the preview panel (the Shell's pixel UI), debug
// under Node.js (VS Code's debugger attaches to the app the Shell started),
// tests against a fake Shell, a signed package and its submission to the
// store.
import { ChildProcess, spawn } from "node:child_process";
import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import * as logic from "./logic";

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

/** project is the Kario project of the active file, else of the first workspace folder that is one. */
function project(): string | undefined {
  const starts = [vscode.window.activeTextEditor?.document.uri.fsPath, ...(vscode.workspace.workspaceFolders ?? []).map((f) => f.uri.fsPath)];
  for (const s of starts) {
    const dir = s && logic.findProject(s, files);
    if (dir) return dir;
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
      output.appendLine(String(e));
      resolve({ code: -1, text: String(e) });
    });
    p.on("close", (code) => resolve({ code: code ?? -1, text }));
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
    void vscode.window.showErrorMessage("Kario: the build failed (see Problems)");
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

/** start runs the app in the Shell's preview mode and resolves with its page's address. */
function start(dir: string, debug: boolean): Promise<string> {
  stop();
  const s = settings();
  const args = logic.previewArgs(dir, s, debug);
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
    proc.on("error", reject);
    proc.on("close", (code) => {
      if (running?.proc === proc) running = undefined;
      report(dir, seen);
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

async function preview(debug: boolean): Promise<void> {
  const dir = project();
  if (!dir || !(await build(dir))) return;
  const m = logic.readManifest(dir, files);
  try {
    const url = await start(dir, debug);
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
    void vscode.window.showErrorMessage("Kario: packaging failed (signing: kario.signing.*)");
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
    const args = logic.publishArgs(file, settings());
    const r = await kario(args, path.dirname(file));
    output.show(true);
    if (r.code === 0) void vscode.window.showInformationMessage(`Kario: ${r.text.trim().split("\n").pop()}`);
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

export function activate(ctx: vscode.ExtensionContext): void {
  output = vscode.window.createOutputChannel("Kario");
  diagnostics = vscode.languages.createDiagnosticCollection("kario");
  const cmd = (id: string, fn: () => unknown) => ctx.subscriptions.push(vscode.commands.registerCommand(id, fn));
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
  ctx.subscriptions.push(output, diagnostics, { dispose: stop });
}

export function deactivate(): void {
  stop();
}
