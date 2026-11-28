// A TypeScript app's own network against a test server: the selftest's,
// on the device's loopback (shell/fetch_headless.go). OK on the server's
// address runs the checks in turn: a response read as a stream as it
// comes, a request aborted while the server holds it, cookies kept in the
// app's own jar and sent back only with "include", and a WebSocket echo.
// Each says how it went on the page and, in English, on the app's log.
import { WebSocket, fetch, on, reset, storage, tr, update, type FetchInit, type Node, type Screen } from "kario";

interface Result {
  ok: boolean;
  text: string;
}

let server = storage.get<string>("server") ?? "";
let lines: string[] = [];
let running = false;

function screen(): Screen {
  const body: Node[] = [
    {
      kind: "input",
      id: "server",
      placeholder: tr("Test server (http://127.0.0.1:…), OK runs the checks"),
      keyboard: "url",
      value: server,
      submit: "run",
    },
  ];
  for (const text of lines) body.push({ kind: "text", text });
  return { id: "main", title: tr("Network check"), focus: "server", body };
}

/** stream reads /stream as it comes: the server sends half of it, waits a second, then the rest. */
async function stream(base: string): Promise<Result> {
  const res = await fetch(base + "/stream");
  if (!res.body) return { ok: false, text: tr("Stream: no body") };
  const reader = res.body.getReader();
  let bytes = 0;
  let chunks = 0;
  let first = 0;
  let last = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    last = Date.now();
    if (!chunks) first = last;
    bytes += value.byteLength;
    chunks++;
  }
  console.log(`netcheck stream: ${bytes} bytes in ${chunks} chunks, the first ${last - first} ms before the last`);
  const asTheyCame = last - first >= 500;
  return {
    ok: res.ok && asTheyCame,
    text: asTheyCame
      ? tr("Stream: %d bytes in %d chunks, read as they came", bytes, chunks)
      : tr("Stream: %d bytes in %d chunks, all at once", bytes, chunks),
  };
}

/** abort aborts a request to /hold, which never answers, after 300 ms. */
async function abort(base: string): Promise<Result> {
  const c = new AbortController();
  const t0 = Date.now();
  setTimeout(() => c.abort(), 300);
  try {
    await fetch(base + "/hold", { signal: c.signal });
    return { ok: false, text: tr("Abort: the request was answered") };
  } catch (err) {
    const name = (err as Error).name;
    console.log(`netcheck abort: ${name} after ${Date.now() - t0} ms`);
    return { ok: name === "AbortError", text: tr("Abort: %s", name) };
  }
}

/** cookies: what the jar sends before /cookie/set sets two, then without and with "include". */
async function cookies(base: string): Promise<Result> {
  const include: FetchInit = { credentials: "include" };
  const sent = async (init?: FetchInit) => (await (await fetch(base + "/cookie/get", init)).text()) || "-";
  const before = await sent(include);
  await (await fetch(base + "/cookie/set", include)).text();
  const without = await sent();
  const back = await sent(include);
  console.log(`netcheck cookies: before ${before}, sent back ${back}, without include ${without}`);
  return {
    ok: back.includes("sid=netcheck") && without === "-",
    text: tr("Cookies: before %s, sent back %s, without “include” %s", before, back, without),
  };
}

/** echo sends a text message to /ws and closes once the server echoes it. */
function echo(base: string): Promise<Result> {
  const text = "hello kario";
  return new Promise((resolve) => {
    let got = "";
    const ws = new WebSocket(base.replace(/^http/, "ws") + "/ws");
    ws.onopen = () => ws.send(text);
    ws.onmessage = (e) => {
      got = typeof e.data === "string" ? e.data : "(binary)";
      ws.close(1000, "done");
    };
    ws.onclose = (e) => {
      console.log(`netcheck websocket: echoed "${got}", closed ${e.code} clean ${e.wasClean}`);
      resolve({ ok: got === text && e.code === 1000 && e.wasClean, text: tr("WebSocket: echoed “%s”, closed %d", got, e.code) });
    };
  });
}

const checks: [string, (base: string) => Promise<Result>][] = [
  ["stream", stream],
  ["abort", abort],
  ["cookies", cookies],
  ["websocket", echo],
];

async function run(value: string): Promise<void> {
  if (running) return;
  running = true;
  server = value.trim().replace(/\/+$/, "");
  storage.set("server", server);
  lines = [];
  update(screen());
  let passed = 0;
  for (const [name, check] of checks) {
    let r: Result;
    try {
      r = await check(server);
    } catch (err) {
      console.log(`netcheck ${name}: ${String(err)}`);
      r = { ok: false, text: tr("%s failed: %s", name, (err as Error).message) };
    }
    if (r.ok) passed++;
    lines.push(r.text);
    update(screen());
  }
  console.log(`netcheck: ${passed} of ${checks.length} passed`);
  lines.push(
    passed === checks.length
      ? tr("All %d checks passed", passed)
      : tr("%d of %d checks failed", checks.length - passed, checks.length),
  );
  update(screen());
  running = false;
}

on("start", () => reset(screen()));

on("action", (e) => {
  if (e.action === "run") void run(e.value);
});
