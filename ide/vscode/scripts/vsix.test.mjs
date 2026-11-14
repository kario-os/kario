// The .vsix the packager writes reads back whole, laid out as VS Code installs it.
import assert from "node:assert/strict";
import { test } from "node:test";
import { entries, unzip, zip } from "./vsix.mjs";

test("a zip that reads back, every CRC right", () => {
  const files = [
    { name: "a.txt", data: Buffer.from("hello") },
    { name: "dir/b.json", data: Buffer.from(JSON.stringify({ x: "y".repeat(5000) })) },
    { name: "empty", data: Buffer.alloc(0) },
  ];
  const back = unzip(zip(files));
  assert.deepEqual([...back.keys()], files.map((f) => f.name));
  for (const f of files) assert.deepEqual(back.get(f.name), f.data);
});

test("the extension's package: manifest, content types, the compiled extension, no tests", () => {
  const { pkg, entries: files } = entries();
  const names = files.map((f) => f.name);
  for (const n of ["extension.vsixmanifest", "[Content_Types].xml", "extension/package.json", "extension/README.md", "extension/LICENSE.txt", "extension/out/src/extension.js", "extension/out/src/logic.js"]) {
    assert.ok(names.includes(n), n);
  }
  assert.ok(!names.some((n) => n.includes("/test/") || n.endsWith(".map")));
  const manifest = files[0].data.toString();
  assert.match(manifest, new RegExp(`Id="${pkg.name}" Version="${pkg.version}" Publisher="${pkg.publisher}"`));
  assert.match(manifest, /Microsoft\.VisualStudio\.Code\.Engine" Value="\^1\.85\.0"/);
});
