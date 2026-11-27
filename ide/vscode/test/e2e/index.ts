// Kario Studio clicked through in a real editor (scripts/e2e.mjs starts it
// with @vscode/test-electron): new project, build, the manifest editor, the
// preview panel with keys, the view previews, a profiling run, debugging on
// a (fake) phone and a package, each through the extension's own commands. The workspace's
// settings (written by e2e.mjs) name the kario, phone-shell and renderer to
// use. The dialogs New App opens are answered here.
import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as http from "node:http";
import * as path from "node:path";
import * as vscode from "vscode";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function until<T>(what: string, f: () => T | undefined | false | Promise<T | undefined | false>, ms = 30000): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await f();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(200);
  }
}

function get(url: string): Promise<{ status: number; body: Buffer }> {
  return new Promise((resolve, reject) => {
    http
      .get(url, (res) => {
        const parts: Buffer[] = [];
        res.on("data", (b: Buffer) => parts.push(b));
        res.on("end", () => resolve({ status: res.statusCode ?? 0, body: Buffer.concat(parts) }));
      })
      .on("error", reject);
  });
}

function post(url: string, form: string): Promise<number> {
  return new Promise((resolve, reject) => {
    const req = http.request(url, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" } }, (res) => {
      res.resume();
      res.on("end", () => resolve(res.statusCode ?? 0));
    });
    req.on("error", reject);
    req.end(form);
  });
}

function tabs(): vscode.Tab[] {
  return vscode.window.tabGroups.all.flatMap((g) => g.tabs);
}

export async function run(): Promise<void> {
  const ws = vscode.workspace.workspaceFolders![0].uri.fsPath;
  const app = path.join(ws, "e2e-app");
  const cfg = vscode.workspace.getConfiguration("kario");
  const addr = cfg.get<string>("previewAddress")!;
  const steps: [string, () => Promise<void>][] = [
    [
      "Kario: New App",
      async () => {
        const w = vscode.window as { showOpenDialog: unknown; showInputBox: unknown };
        const c = vscode.commands as { executeCommand: (id: string, ...a: unknown[]) => Thenable<unknown> };
        const saved = { open: w.showOpenDialog, input: w.showInputBox, exec: c.executeCommand };
        let opened: vscode.Uri | undefined;
        w.showOpenDialog = async () => [vscode.Uri.file(ws)];
        w.showInputBox = async () => "e2e-app";
        c.executeCommand = (id: string, ...a: unknown[]) => {
          if (id === "vscode.openFolder") {
            opened = a[0] as vscode.Uri;
            return Promise.resolve();
          }
          return saved.exec.call(vscode.commands, id, ...a);
        };
        try {
          await vscode.commands.executeCommand("kario.new");
        } finally {
          w.showOpenDialog = saved.open;
          w.showInputBox = saved.input;
          c.executeCommand = saved.exec;
        }
        assert.ok(fs.existsSync(path.join(app, "manifest.json")), "no manifest.json");
        assert.equal(opened?.fsPath, app);
      },
    ],
    [
      "Kario: Build",
      async () => {
        await vscode.window.showTextDocument(vscode.Uri.file(path.join(app, "src", "main.ts")));
        await vscode.commands.executeCommand("kario.build");
        assert.ok(fs.existsSync(path.join(app, "build", "main.js")));
        assert.ok(fs.existsSync(path.join(app, "build", "main.js.map")));
      },
    ],
    [
      "Kario: Edit the Manifest",
      async () => {
        await vscode.commands.executeCommand("kario.editManifest");
        const tab = await until("the manifest editor", () =>
          tabs().find((t) => t.input instanceof vscode.TabInputCustom && t.input.viewType === "kario.manifestEditor"),
        );
        await vscode.window.tabGroups.close(tab);
        await vscode.window.showTextDocument(vscode.Uri.file(path.join(app, "src", "main.ts")));
      },
    ],
    [
      "Kario: Run in the Preview Panel",
      async () => {
        await vscode.commands.executeCommand("kario.preview");
        await until("the preview panel", () => tabs().find((t) => t.input instanceof vscode.TabInputWebview && t.label.startsWith("Kario:")));
        const first = await until("a frame", async () => (await get(`http://${addr}/frame.png`)).body.length > 0 && true, 30000);
        assert.ok(first);
        await sleep(2500);
        const before = (await get(`http://${addr}/frame.png`)).body;
        assert.equal(before.subarray(1, 4).toString(), "PNG");
        assert.equal(await post(`http://${addr}/key`, "key=enter"), 200);
        let after = before;
        await until("the frame after OK", async () => {
          after = (await get(`http://${addr}/frame.png`)).body;
          return !after.equals(before);
        });
        await vscode.commands.executeCommand("kario.stop");
      },
    ],
    [
      "Kario: Preview the Views",
      async () => {
        await vscode.commands.executeCommand("kario.viewPreview");
        const dev = cfg.get<string>("device")!;
        for (const name of ["new", "tapped"]) {
          const png = path.join(app, ".kario", "preview", dev, `${name}.png`);
          assert.ok(fs.statSync(png).size > 1000, png);
        }
        await until("the previews panel", () => tabs().find((t) => t.label === "Kario: previews"));
      },
    ],
    [
      "Kario: Run with the Performance Timeline",
      async () => {
        await vscode.commands.executeCommand("kario.profile");
        await sleep(2500);
        await post(`http://${addr}/key`, "key=enter");
        await sleep(1000);
        await vscode.commands.executeCommand("kario.stop");
        const trace = path.join(app, ".kario", "profile", "trace.json");
        await until("the timeline", () => fs.existsSync(trace), 20000);
        const t = JSON.parse(fs.readFileSync(trace, "utf8"));
        assert.ok(t.traceEvents.some((e: { ph: string }) => e.ph === "C"), "no counters");
        assert.ok(t.traceEvents.some((e: { cat: string }) => e.cat === "page"), "no pages");
      },
    ],
    [
      "Kario: Debug on the Phone",
      async () => {
        // The fake phone (e2e.mjs) runs the app on kario-jsrt: a breakpoint
        // where the app starts, the stop at it in src/main.ts, an expression
        // in TypeScript evaluated in its frame, then the app runs on.
        const main = path.join(app, "src", "main.ts");
        const line = fs.readFileSync(main, "utf8").split("\n").findIndex((l) => l.startsWith("app(Main)"));
        assert.ok(line >= 0, "no line to break on");
        const bp = new vscode.SourceBreakpoint(new vscode.Location(vscode.Uri.file(main), new vscode.Position(line, 0)));
        vscode.debug.addBreakpoints([bp]);
        let stops = 0;
        const tracker = vscode.debug.registerDebugAdapterTrackerFactory("kario", {
          createDebugAdapterTracker: () => ({
            onDidSendMessage: (m: { type?: string; event?: string }) => {
              if (m.type === "event" && m.event === "stopped") stops++;
            },
          }),
        });
        try {
          await vscode.commands.executeCommand("kario.debugDevice");
          await until("the stop at the breakpoint", () => stops > 0, 120000);
          const session = vscode.debug.activeDebugSession;
          assert.ok(session, "no debug session");
          const st = await session.customRequest("stackTrace", { threadId: 1 });
          const top = st.stackFrames[0];
          assert.equal(top.source?.path, main);
          assert.equal(top.line, line + 1);
          const r = await session.customRequest("evaluate", { expression: "(count.value as number) + 1", frameId: top.id, context: "watch" });
          assert.equal(r.result, "1");
          await session.customRequest("continue", { threadId: 1 });
          const pages = process.env.KARIO_E2E_PAGES!;
          await until("the page after continue", () => fs.existsSync(pages) && fs.readFileSync(pages, "utf8").includes("Tapped 0 times"));
          await vscode.debug.stopDebugging(session);
        } finally {
          tracker.dispose();
          vscode.debug.removeBreakpoints([bp]);
        }
      },
    ],
    [
      "Kario: Package",
      async () => {
        await vscode.commands.executeCommand("kario.package");
        const out = fs.readdirSync(path.join(app, "build")).filter((f) => f.endsWith(".phoneapp"));
        assert.equal(out.length, 1, String(out));
      },
    ],
  ];
  const failed: string[] = [];
  for (const [name, step] of steps) {
    try {
      await step();
      console.log(`KARIO-E2E ok   ${name}`);
    } catch (e) {
      failed.push(name);
      console.log(`KARIO-E2E FAIL ${name}: ${(e as Error).stack ?? e}`);
    }
  }
  if (failed.length) throw new Error(`failed: ${failed.join(", ")}`);
}
