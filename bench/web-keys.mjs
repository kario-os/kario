// Key-only end-to-end check of web/chat, mirroring the shell selftest's chat
// steps, plus screenshots for the comparison doc. The page is served over
// HTTP (as bench.py does) so it can load its translations.
//   PLAYWRIGHT_DIR=<dir with node_modules/playwright> CHROME=<chrome> node web-keys.mjs <outdir>
import fs from "node:fs";
import http from "node:http";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.env.PLAYWRIGHT_DIR, "package.json"));
const { chromium } = require("playwright");
const here = path.dirname(fileURLToPath(import.meta.url));
const out = process.argv[2] || ".";
const root = path.join(here, "../web");

// The text the page shows, from its translations (web/chat/zh-Hans.json),
// with the page's own t().
const messages = JSON.parse(fs.readFileSync(path.join(root, "chat/zh-Hans.json"), "utf8"));
function t(msg, ...args) {
  const s = Object.prototype.hasOwnProperty.call(messages, msg) ? messages[msg] : msg;
  return args.length ? s.replace(/%[sd]/g, () => String(args.shift())) : s;
}

const types = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css", ".json": "application/json" };
const server = http.createServer((req, res) => {
  const file = path.join(root, path.normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)));
  fs.readFile(file, (err, body) => {
    if (err) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream" }).end(body);
  });
});
await new Promise((ok) => server.listen(0, "127.0.0.1", ok));

const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ["--no-proxy-server"] });
const page = await browser.newPage({ viewport: { width: 480, height: 640 } });
await page.goto(`http://127.0.0.1:${server.address().port}/chat/index.html`);

const steps = [];
async function step(name, fn) {
  try {
    await fn();
    steps.push(["PASS", name]);
  } catch (e) {
    steps.push(["FAIL", name + ": " + e.message.split("\n")[0]]);
  }
}
const see = (text) => page.getByText(text, { exact: false }).first().waitFor({ timeout: 3000 });
const key = async (k, n = 1) => { for (let i = 0; i < n; i++) await page.keyboard.press(k); };
const typed = "Hello";
const reply = t("Got it: %s", typed);

await step("conversation list shows", () => see(t("Product group (%d)", 5)));
await page.screenshot({ path: path.join(out, "web-01-list.png") });
await step("arrow keys pick Zhang San and open the chat", async () => { await key("ArrowDown"); await key("Enter"); await see(t("Going hiking this weekend?")); });
await step("type, send and get the reply", async () => { await page.keyboard.type(typed); await key("Enter"); await see(reply); });
await page.screenshot({ path: path.join(out, "web-02-chat.png") });
await step("message menu (same as a long press)", async () => { await key("ArrowUp"); await key("Tab"); await see(t("Copy")); await see(t("Forward")); await see(t("Quote")); });
await page.screenshot({ path: path.join(out, "web-03-menu.png") });
await step("quote fills the input", async () => {
  await key("Escape"); await key("q");
  const v = await page.inputValue("#compose");
  if (!v.includes(t("\"%s\" ", reply).trim())) throw new Error("input is " + JSON.stringify(v));
});
await step("forward to another conversation", async () => { await key("ArrowUp"); await key("f"); await see(t("Forward to…")); await key("ArrowDown"); await key("Enter"); await see(t("Forwarded to %s", t("Li Si"))); });
await step("back to the conversation list", async () => { await key("Escape"); await see(t("File Transfer")); });

for (const [s, n] of steps) console.log(s + "  " + n);
console.log(`WEB-KEYS-RESULT: ${steps.every(([s]) => s === "PASS") ? "PASS" : "FAIL"} (${steps.filter(([s]) => s === "PASS").length}/${steps.length})`);
await browser.close();
server.close();
