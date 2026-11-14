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

test("packages: signed by the developer's key or a development store, named by id and version", () => {
  const m = { id: "com.example.notes", version: "1.2" };
  assert.equal(logic.packageFile("/w/a", m), path.join("/w/a", "build", "com.example.notes-1.2.phoneapp"));
  assert.deepEqual(logic.packageArgs("/w/a", m, s), [
    "package", "-o", "/w/a/build/com.example.notes-1.2.phoneapp", "-key", "/w/a/dev.key", "-cert", "/w/a/dev.cert", "/w/a",
  ]);
  assert.deepEqual(logic.packageArgs("/w/a", m, { ...s, devStore: "/d" }).slice(3, 5), ["-devstore", "/d"]);
  assert.throws(() => logic.publishArgs("/w/a.phoneapp", s), /storeUrl/);
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

test("the panel frames only the Shell's page", () => {
  const html = logic.previewHTML("http://127.0.0.1:8790/", "n0nce");
  assert.match(html, /frame-src http:\/\/127\.0\.0\.1:8790;/);
  assert.match(html, /<iframe src="http:\/\/127\.0\.0\.1:8790\/"/);
  assert.match(html, /default-src 'none'/);
  assert.equal(logic.escapeArg("/w/my app"), "'/w/my app'");
  assert.equal(logic.escapeArg("-o"), "-o");
});
