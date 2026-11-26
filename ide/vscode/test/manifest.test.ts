// The manifest editor's decisions, and its rules coming from kario manifest
// check -json (node --test).
import * as assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import * as manifest from "../src/manifest";

const schema: manifest.Schema = {
  runtime: "js",
  main: "main.js",
  permissions: [
    { name: "camera", label: "Camera", runtime: true, data: "photos" },
    { name: "location", label: "Location", runtime: true, data: "location" },
    { name: "net", label: "Network", runtime: false },
  ],
  max_purpose: 80,
  privacy_data: ["location", "photos", "usage"],
  privacy_uses: ["app", "analytics", "ads"],
  net_permission: "net",
  languages: ["zh-Hans", "zh-Hant", "en", "es"],
};

const original = `{
  "id": "com.example.notes",
  "name": "Notes",
  "runtime": "js",
  "main": "main.js",
  "permissions": ["location"],
  "purposes": {"location": "Nearby notes"},
  "locales": {"en": {"name": "Notes"}},
  "settings": [{"key": "sort", "choices": ["a", "b"]}]
}`;

test("the form reads a manifest and writes back only what it shows", () => {
  const f = manifest.formOf(original)!;
  assert.equal(f.id, "com.example.notes");
  assert.deepEqual(f.permissions, ["location"]);
  assert.equal(f.purposes.location, "Nearby notes");
  assert.equal(f.privacy, false);
  assert.equal(manifest.formOf("[1]"), undefined);
  assert.equal(manifest.formOf("{"), undefined);

  const out = JSON.parse(
    manifest.applyForm(
      original,
      {
        ...f,
        version: "1.1",
        permissions: ["camera", "net"],
        purposes: { camera: "Scan a note", location: "stale" },
        privacy: true,
        collects: [{ data: "photos", use: ["app"], linked: false, shared: true }],
        local: ["location"],
      },
      schema,
    ),
  );
  assert.deepEqual(Object.keys(out), ["id", "name", "runtime", "main", "permissions", "purposes", "locales", "settings", "version", "privacy"]);
  assert.equal(out.version, "1.1");
  assert.deepEqual(out.permissions, ["camera", "net"]);
  assert.deepEqual(out.purposes, { camera: "Scan a note" });
  assert.deepEqual(out.privacy, { collects: [{ data: "photos", use: ["app"], shared: true }], local: ["location"] });
  assert.deepEqual(out.settings, [{ key: "sort", choices: ["a", "b"] }]);
  assert.equal(out.description, undefined);

  const none = JSON.parse(manifest.applyForm(original, { ...f, permissions: [], privacy: false }, schema));
  assert.equal(none.purposes, undefined);
  assert.equal(none.privacy, undefined);
  assert.equal(manifest.applyForm(manifest.applyForm(original, f, schema), f, schema), manifest.applyForm(original, f, schema));
});

test("problems go beside their fields, the rest at the top", () => {
  const f = manifest.formOf(original)!;
  const by = manifest.problemsByField(
    [
      { field: "purposes.camera", message: "needs a purpose" },
      { field: "permissions.bogus", message: "not one an app can declare" },
      { field: "locales", message: "too long" },
      { field: "name", message: "the app needs a name" },
    ],
    { ...f, permissions: ["camera", "bogus"] },
    schema,
  );
  assert.deepEqual(by.get("purposes.camera"), ["needs a purpose"]);
  assert.deepEqual(by.get("permissions.bogus"), ["not one an app can declare"]);
  assert.deepEqual(by.get(""), ["too long"]);
  assert.deepEqual(by.get("name"), ["the app needs a name"]);
});

test("the page: one checkbox per permission, purposes for runtime ones, the label's table, scripts by nonce only", () => {
  const f = manifest.formOf(original)!;
  const html = manifest.editorHTML(
    f,
    { ok: false, problems: [{ field: "purposes.location", message: "the purpose <is> long" }], schema },
    "n0nce",
    "vscode-resource:",
  );
  for (const p of schema.permissions) assert.match(html, new RegExp(`data-perm="${p.name}"`));
  assert.match(html, /data-perm="location" checked/);
  assert.match(html, /data-purpose="location" maxlength="80"[^>]*value="Nearby notes"/);
  assert.match(html, /data-purpose="camera"[^>]*disabled/);
  assert.doesNotMatch(html, /data-purpose="net"/);
  assert.match(html, /data-for="purposes.location">the purpose &lt;is&gt; long/);
  assert.match(html, /data-collect="usage"/);
  assert.match(html, /data-use="photos:ads"/);
  assert.match(html, /script-src 'nonce-n0nce'/);
  assert.match(html, /<table id="label" hidden>/);
  assert.match(html, /<option value="zh-Hant">/);
});

// kario built from this tree (go build), when Go is at hand.
function karioCLI(): string | undefined {
  if (process.env.KARIO_CLI) return process.env.KARIO_CLI;
  const root = path.resolve(__dirname, "..", "..", "..", "..");
  if (!fs.existsSync(path.join(root, "go.mod"))) return undefined;
  const out = path.join(os.tmpdir(), `kario-studio-test-${process.pid}`, "kario");
  try {
    execFileSync("go", ["build", "-o", out, "./cmd/kario"], { cwd: root, stdio: "pipe" });
  } catch {
    return undefined;
  }
  return out;
}

test("the rules are kario's: kario manifest check -json on what the form wrote", (t) => {
  const cli = karioCLI();
  if (!cli) {
    t.skip("no kario (set KARIO_CLI, or have Go)");
    return;
  }
  const check = (text: string) => {
    try {
      return manifest.parseReport(execFileSync(cli, ["manifest", "check", "-json", "-"], { input: text, encoding: "utf8" }));
    } catch (e) {
      return manifest.parseReport((e as { stdout: string }).stdout);
    }
  };
  const good = check(original);
  assert.equal(good.ok, true, JSON.stringify(good.problems));
  assert.ok(good.schema.permissions.some((p) => p.name === "camera" && p.runtime));
  const f = manifest.formOf(original)!;
  const bad = check(manifest.applyForm(original, { ...f, permissions: ["camera", "net", "location"], purposes: { location: "Nearby notes" }, privacy: true, collects: [], local: ["location"] }, good.schema));
  const fields = bad.problems.map((p) => p.field);
  assert.ok(fields.includes("purposes.camera"), JSON.stringify(bad.problems));
  assert.ok(fields.includes("privacy"), JSON.stringify(bad.problems));
  const fixed = check(
    manifest.applyForm(
      original,
      { ...f, permissions: ["camera", "net", "location"], purposes: { location: "Nearby notes", camera: "Scan a note" }, privacy: true, collects: [], local: ["location", "photos"] },
      good.schema,
    ),
  );
  assert.equal(fixed.ok, true, JSON.stringify(fixed.problems));
});
