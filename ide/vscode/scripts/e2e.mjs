// node scripts/e2e.mjs: Kario Studio clicked through in a real editor
// (test/e2e/index.ts) with @vscode/test-electron (MIT, a dev dependency).
// The editor is VSCodium (MIT-licensed builds of VS Code's sources), not
// Microsoft's VS Code, whose binaries are not under an open licence: it is
// downloaded once into ~/.cache/kario/vscodium (KARIO_E2E_EDITOR names
// another), used for the test only and never shipped. Without a display it
// runs itself again under xvfb-run. It needs a kario tree's programs:
// KARIO_CLI (default build/bin/kario), which finds the Shell and phone-paint
// as it does for a user unless KARIO_SHELL and KARIO_RENDERER name them,
// and Node.js. Debugging on a phone talks to the fake phone of kario's own
// tests (TestE2EPhone in cmd/kario, started here with go test), which runs
// the app on this tree's kario-jsrt.
import { execFileSync, spawn, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const here = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const tree = path.resolve(here, "..", "..");
const VSCODIUM = "1.143.17232";
const ARCHIVE = `VSCodium-linux-x64-${VSCODIUM}.tar.gz`;
const URL_BASE = `https://github.com/VSCodium/vscodium/releases/download/${VSCODIUM}/`;

if (!process.env.DISPLAY && !process.env.KARIO_E2E_UNDER_XVFB) {
  const r = spawnSync("xvfb-run", ["-a", "-s", "-screen 0 1280x1024x24", process.execPath, ...process.argv.slice(1)], {
    stdio: "inherit",
    env: { ...process.env, KARIO_E2E_UNDER_XVFB: "1" },
  });
  if (r.error) {
    console.error("kario e2e: no display and no xvfb-run:", r.error.message);
    process.exit(2);
  }
  process.exit(r.status ?? 1);
}

/** editor is the VSCodium to test in: KARIO_E2E_EDITOR, or the cached download (verified by its published SHA-256). */
function editor() {
  if (process.env.KARIO_E2E_EDITOR) return process.env.KARIO_E2E_EDITOR;
  const cache = path.join(os.homedir(), ".cache", "kario", "vscodium");
  const dir = path.join(cache, VSCODIUM);
  const bin = path.join(dir, "codium");
  if (fs.existsSync(bin)) return bin;
  fs.mkdirSync(cache, { recursive: true });
  const archive = path.join(cache, ARCHIVE);
  if (!fs.existsSync(archive)) execFileSync("curl", ["-fsSL", "-o", archive, URL_BASE + ARCHIVE], { stdio: "inherit" });
  const want = execFileSync("curl", ["-fsSL", URL_BASE + ARCHIVE + ".sha256"], { encoding: "utf8" }).trim().split(/\s+/)[0];
  const got = crypto.createHash("sha256").update(fs.readFileSync(archive)).digest("hex");
  if (got !== want) {
    fs.rmSync(archive);
    throw new Error(`${ARCHIVE}: SHA-256 ${got}, the release says ${want}`);
  }
  fs.mkdirSync(dir, { recursive: true });
  execFileSync("tar", ["xzf", archive, "-C", dir]);
  return bin;
}

function program(env, def = "") {
  const p = process.env[env] || path.join(tree, def);
  if (!fs.existsSync(p)) throw new Error(`no ${p} (${env})`);
  return p;
}

function freePort() {
  return new Promise((resolve) => {
    const s = net.createServer().listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });
}

/** phone starts TestE2EPhone and resolves to the environment kario dap needs to reach it. */
async function phone(work) {
  const file = path.join(work, "phone.json");
  const child = spawn("go", ["test", "-count=1", "-run", "^TestE2EPhone$", "./cmd/kario"], {
    cwd: tree,
    stdio: ["ignore", "inherit", "inherit"],
    env: { ...process.env, KARIO_E2E_PHONE: file, KARIO_E2E_DEVSTORE: path.join(work, "devstore") },
  });
  const stop = () => {
    fs.writeFileSync(file + ".stop", "");
    const kill = setTimeout(() => child.kill(), 10000);
    return new Promise((resolve) => child.once("exit", () => resolve(clearTimeout(kill))));
  };
  for (const end = Date.now() + 300000; !fs.existsSync(file); ) {
    if (child.exitCode !== null || Date.now() > end) throw new Error("the fake phone (go test -run TestE2EPhone ./cmd/kario) did not come up");
    await new Promise((r) => setTimeout(r, 200));
  }
  return { env: { ...JSON.parse(fs.readFileSync(file, "utf8")), KARIO_E2E_PAGES: file + ".pages" }, stop };
}

const { runTests } = await import("@vscode/test-electron");
const work = fs.mkdtempSync(path.join(os.tmpdir(), "kario-e2e-"));
const ws = path.join(work, "workspace");
const fake = await phone(work);
fs.mkdirSync(path.join(ws, ".vscode"), { recursive: true });
fs.writeFileSync(
  path.join(ws, ".vscode", "settings.json"),
  JSON.stringify(
    {
      "kario.cli": program("KARIO_CLI", "build/bin/kario"),
      ...(process.env.KARIO_SHELL ? { "kario.shell": program("KARIO_SHELL") } : {}),
      ...(process.env.KARIO_RENDERER ? { "kario.renderer": program("KARIO_RENDERER") } : {}),
      "kario.node": process.execPath,
      "kario.device": "bar-t9",
      "kario.previewAddress": `127.0.0.1:${await freePort()}`,
      "kario.reloadOnSave": false,
      "kario.signing.devStore": path.join(work, "devstore"),
      "http.proxySupport": "off",
    },
    null,
    2,
  ),
);
try {
  await runTests({
    vscodeExecutablePath: editor(),
    extensionDevelopmentPath: here,
    extensionTestsPath: path.join(here, "out", "test", "e2e", "index.js"),
    launchArgs: [ws, "--disable-extensions", "--disable-workspace-trust", "--disable-gpu", "--no-sandbox", "--user-data-dir", path.join(work, "user"), "--extensions-dir", path.join(work, "extensions")],
    extensionTestsEnv: { NO_PROXY: "127.0.0.1,localhost", no_proxy: "127.0.0.1,localhost", ...fake.env },
  });
  console.log("kario e2e: PASS");
} catch (e) {
  console.error("kario e2e: FAIL:", e.message ?? e);
  process.exitCode = 1;
} finally {
  await fake.stop();
  if (!process.env.KARIO_E2E_KEEP) fs.rmSync(work, { recursive: true, force: true });
}
