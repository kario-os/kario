// Kario Studio's decisions, without VS Code (node --test).
import * as assert from "node:assert/strict";
import * as path from "node:path";
import { test } from "node:test";
import * as logic from "../src/logic";

function tree(entries: Record<string, string>): logic.Files {
  return {
    exists: (p) => p in entries,
    read: (p) => entries[p],
  };
}

const s: logic.Settings = { ...logic.defaults, cli: "/k/kario" };

test("the project is the nearest TypeScript app up from a file", () => {
  const fs = tree({
    "/w/notes/manifest.json": '{"id":"com.example.notes","runtime":"js"}',
    "/w/notes/src/main.ts": "",
    "/w/go/manifest.json": '{"id":"com.example.go","exec":"app"}',
    "/w/go/src/main.ts": "",
    "/w/broken/manifest.json": "{",
  });
  assert.equal(logic.findProject("/w/notes/src/pages/list.ts", fs), "/w/notes");
  assert.equal(logic.findProject("/w/notes", fs), "/w/notes");
  assert.equal(logic.findProject("/w/go/src/main.ts", fs), undefined);
  assert.equal(logic.findProject("/w/broken/x.ts", fs), undefined);
  assert.equal(logic.findProject("/elsewhere", fs), undefined);
});

test("the command lines", () => {
  assert.deepEqual(logic.buildArgs("/w/a"), ["build", "-minify=false", "-sourcemap", "/w/a"]);
  assert.deepEqual(logic.runArgs("/w/a", { ...s, language: "es" }), ["run", "-device", "bar-t9", "-lang", "es", "/w/a"]);
  assert.deepEqual(logic.previewArgs("/w/a", { ...s, shell: "/b/phone-shell", renderer: "/b/paint" }, false), [
    "run", "-shell", "/b/phone-shell", "-device", "bar-t9", "-preview", "127.0.0.1:8790", "-gui", "/b/paint", "-watch", "/w/a",
  ]);
  assert.deepEqual(logic.previewArgs("/w/a", s, true).slice(-5), ["-watch", "-inspect", "127.0.0.1:9229", "-brk", "/w/a"]);
  assert.ok(!logic.previewArgs("/w/a", { ...s, reloadOnSave: false }, false).includes("-watch"));
  assert.deepEqual(logic.testArgs("/w/a", { ...s, node: "/n/node" }, "grid"), ["test", "-node", "/n/node", "-run", "grid", "/w/a"]);
});

test("a SKU takes the place of the form factor; the picker lists SKUs, then form factors", () => {
  assert.deepEqual(logic.runArgs("/w/a", { ...s, sku: "qemu-t9-1g" }), ["run", "-sku", "qemu-t9-1g", "/w/a"]);
  assert.deepEqual(logic.previewArgs("/w/a", { ...s, sku: "qemu-qwerty" }, false).slice(0, 5), ["run", "-sku", "qemu-qwerty", "-preview", "127.0.0.1:8790"]);
  const skus = logic.parseSkus(
    JSON.stringify([
      { id: "qemu-t9-1g", device: "bar-t9", form: "t9", width: 480, height: 640, memory_mb: 1024, board: "qemuarm64", display: "qemu-480x640", keypad: "qemu-kbd", product: "kario-std", region: "cn", emulator: true },
      { id: "kario-qwerty-sd662-4g", device: "bar-qwerty", form: "qwerty", width: 640, height: 480, memory_mb: 4096, board: "sm6115-fxtec-pro1x", display: "dmt028", keypad: "qwerty-v1", product: "kario-pro", region: "cn", emulator: false },
    ]),
  );
  const picks = logic.devicePicks(skus, { ...s, sku: "kario-qwerty-sd662-4g" });
  assert.deepEqual(picks.map((p) => p.label), ["qemu-t9-1g", "kario-qwerty-sd662-4g", "bar-t9", "bar-qwerty", "communicator"]);
  assert.deepEqual(picks.filter((p) => p.picked).map((p) => p.label), ["kario-qwerty-sd662-4g"]);
  assert.match(picks[0].description, /bar-t9 480×640, t9 keypad, 1 GB/);
  assert.match(picks[0].detail!, /\(emulator\)/);
  assert.deepEqual([picks[2].sku, picks[2].device], ["", "bar-t9"]);
  assert.deepEqual(logic.devicePicks([], s).filter((p) => p.picked).map((p) => p.label), ["bar-t9"]);
  assert.equal(logic.deviceLabel({ ...s, sku: "qemu-t9" }), "qemu-t9");
  assert.equal(logic.deviceLabel(s), "bar-t9");
  assert.throws(() => logic.parseSkus('{"id":"x"}'), /not a list/);
});

test("view previews and profiling runs", () => {
  assert.deepEqual(logic.viewPreviewArgs("/w/a", { ...s, renderer: "/p/paint", language: "es" }, false), [
    "preview", "-json", "-gui", "/p/paint", "-device", "bar-t9", "-lang", "es", "/w/a",
  ]);
  assert.deepEqual(logic.viewPreviewArgs("/w/a", { ...s, sku: "qemu-t9-1g" }, false), ["preview", "-json", "-sku", "qemu-t9-1g", "/w/a"]);
  assert.deepEqual(logic.viewPreviewArgs("/w/a", { ...s, sku: "qemu-t9-1g" }, true), ["preview", "-json", "/w/a"]);
  const p = logic.profileArgs("/w/a", s);
  assert.deepEqual(p.slice(-3), ["-profile", path.join("/w/a", ".kario", "profile", "trace.json"), "/w/a"]);
  assert.ok(!p.includes("-watch") && p.includes("-preview"));
  const images = logic.parsePreviewImages(
    JSON.stringify([
      { name: "new", target: "bar-t9", file: "/w/a/.kario/preview/bar-t9/new.png", width: 480, height: 640 },
      { name: "a <b>", target: "qemu-qwerty", file: "/w/a/.kario/preview/qemu-qwerty/a-b.png", width: 640, height: 480 },
    ]),
  );
  const html = logic.viewPreviewHTML(images, (f) => "vscode-resource:" + f, "n0nce", "vscode-resource:");
  assert.match(html, /<h3>bar-t9<\/h3>.*<h3>qemu-qwerty<\/h3>/s);
  assert.match(html, /<img src="vscode-resource:\/w\/a\/.kario\/preview\/bar-t9\/new.png" width="240" height="320"/);
  assert.match(html, /<figcaption>a &lt;b&gt;<\/figcaption>/);
  assert.match(html, /img-src vscode-resource:;/);
  assert.throws(() => logic.parsePreviewImages("{}"), /not a list/);
});

test("packages: signed by the developer's key or a development store, named by id and version", () => {
  const m = { id: "com.example.notes", version: "1.2" };
  assert.equal(logic.packageFile("/w/a", m), path.join("/w/a", "build", "com.example.notes-1.2.phoneapp"));
  assert.deepEqual(logic.packageArgs("/w/a", m, s), [
    "package", "-o", "/w/a/build/com.example.notes-1.2.phoneapp", "-key", "/w/a/dev.key", "-cert", "/w/a/dev.cert", "/w/a",
  ]);
  assert.deepEqual(logic.packageArgs("/w/a", m, { ...s, devStore: "/d" }).slice(3, 5), ["-devstore", "/d"]);
  assert.throws(() => logic.publishArgs("/w/a.phoneapp", s), /storeUrl/);
  assert.deepEqual(logic.publishArgs("/w/a/build/a.phoneapp", { ...s, storeUrl: "http://store" }, "/w/a"), [
    "publish", "-store", "http://store", "-key", path.join("/w/a", "dev.key"), "/w/a/build/a.phoneapp",
  ]);
  assert.deepEqual(logic.publishArgs("/w/a.phoneapp", { ...s, storeUrl: "http://store", rollout: 10 }), [
    "publish", "-store", "http://store", "-rollout", "10", "/w/a.phoneapp",
  ]);
});

test("the debugger attaches to Node.js's inspector with the build's source maps", () => {
  const c = logic.attachConfig("/w/a", { ...s, inspectAddress: "127.0.0.1:9333" });
  assert.equal(c.type, "node");
  assert.equal(c.request, "attach");
  assert.equal(c.port, 9333);
  assert.equal(c.restart, true);
  assert.deepEqual(c.outFiles, [path.join("/w/a", "build", "**", "*.js")]);
  assert.throws(() => logic.hostPort("nowhere"), /host:port/);
});

test("the phone's debugger: kario dap, launch or attach, the project and the development store filled in", () => {
  assert.deepEqual(logic.dapArgs(), ["dap"]);
  const launch = logic.deviceDebugConfig("/w/a", { ...s, devStore: "/w/store" });
  assert.equal(launch.type, logic.DEVICE_DEBUG);
  assert.equal(launch.request, "launch");
  assert.equal(launch.project, "/w/a");
  assert.equal(launch.devstore, "/w/store");
  assert.equal(launch.stopOnEntry, false);
  const attach = logic.deviceDebugConfig("/w/a", s, "attach");
  assert.equal(attach.request, "attach");
  assert.ok(!("devstore" in attach));
  // F5 without a launch.json: a launch of the project in use.
  assert.deepEqual(logic.resolveDeviceConfig({}, "/w/a", s), logic.deviceDebugConfig("/w/a", s));
  assert.equal(logic.resolveDeviceConfig({}, undefined, s), undefined);
  const filled = logic.resolveDeviceConfig({ type: "kario", request: "launch", name: "x" }, "/w/b", { ...s, devStore: "/w/store" })!;
  assert.equal(filled.project, "/w/b");
  assert.equal(filled.devstore, "/w/store");
  const kept = logic.resolveDeviceConfig({ type: "kario", request: "attach", name: "x", project: "/w/c", devstore: "/mine" }, undefined, { ...s, devStore: "/w/store" })!;
  assert.equal(kept.project, "/w/c");
  assert.equal(kept.devstore, "/mine");
  assert.equal(logic.resolveDeviceConfig({ type: "kario", request: "launch", name: "x" }, undefined, s), undefined);
});

test("launch.json and tasks.json keep what they had, comments and all", () => {
  const before = `{
    // mine
    "version": "0.2.0",
    "configurations": [
      { "name": "Other", "type": "node", "request": "launch", "url": "http://x//y" }, /* trailing */
    ],
  }`;
  const launch = JSON.parse(logic.launchJSON(before, logic.attachConfig("/w/a", s)));
  assert.deepEqual(launch.configurations.map((c: { name: string }) => c.name), ["Other", "Kario: attach to the app"]);
  assert.equal(launch.configurations[0].url, "http://x//y");
  assert.equal(launch.configurations[1].preLaunchTask, "kario: debug");
  const again = JSON.parse(logic.launchJSON(JSON.stringify(launch), logic.attachConfig("/w/a", s)));
  assert.equal(again.configurations.length, 2);
  const tasks = JSON.parse(logic.tasksJSON(undefined, logic.debugTask("/w/a", s)));
  assert.equal(tasks.tasks[0].label, "kario: debug");
  assert.equal(tasks.tasks[0].command, "/k/kario");
  assert.ok(tasks.tasks[0].args.includes("-inspect"));
  assert.ok(new RegExp(tasks.tasks[0].problemMatcher.background.endsPattern).test("PHONE-PREVIEW: http://127.0.0.1:8790  (phone-paint)"));
});

test("what kario prints: the preview's address, build errors as problems", () => {
  assert.equal(logic.previewURL("PHONE-PREVIEW: http://127.0.0.1:8790  (phone-paint 0.1.0, bar-t9 480x640)"), "http://127.0.0.1:8790");
  assert.equal(logic.previewURL("pixel renderer: phone-paint"), undefined);
  const out = "kario: /w/a/src/main.ts:3:9: Could not resolve \"fs\"\nsrc/list.ts:10:1: Expected \";\"\nkario: manifest.json: the app needs a name\n";
  assert.deepEqual(logic.problems(out, "/w/a"), [
    { file: "/w/a/src/main.ts", line: 3, column: 9, message: 'Could not resolve "fs"' },
    { file: "/w/a/src/list.ts", line: 10, column: 1, message: 'Expected ";"' },
    { file: "/w/a/manifest.json", line: 1, column: 1, message: "the app needs a name" },
  ]);
});

test("a kario command that does not start says how to get one", () => {
  assert.equal(
    logic.cliProblem("kario", { code: "ENOENT", message: "spawn kario ENOENT" }),
    "no kario command (the setting kario.cli): run make install-kario in the Kario tree, or set kario.cli to its build/bin/kario",
  );
  assert.equal(logic.cliProblem("/k/kario", { code: "EACCES", message: "spawn /k/kario EACCES" }), "/k/kario: spawn /k/kario EACCES");
});

test("the panel frames only the Shell's page", () => {
  const html = logic.previewHTML("http://127.0.0.1:8790/", "n0nce");
  assert.match(html, /frame-src http:\/\/127\.0\.0\.1:8790;/);
  assert.match(html, /<iframe src="http:\/\/127\.0\.0\.1:8790\/"/);
  assert.match(html, /default-src 'none'/);
  assert.equal(logic.escapeArg("/w/my app"), "'/w/my app'");
  assert.equal(logic.escapeArg("-o"), "-o");
});
