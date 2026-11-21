// What fetch_test.go and net_test.go drive: one fetch or WebSocket an
// action, the outcome as one line.
import * as kario from "kario";

const show = (text) => kario.reset({ id: "main", title: "fetch", body: [{ kind: "text", text }] });

// Taken before any of them is used: on kario-jsrt the stand-ins, which
// must still construct, subclass, test instances and reach statics.
const EarlyHeaders = kario.Headers;
const EarlyResponse = kario.Response;
class TaggedHeaders extends kario.Headers {
  get tag() {
    return "tagged " + this.get("x");
  }
}

kario.on("start", () => show("ready"));

// signalOf makes the plan's AbortSignal: aborted before, after ms, or by
// AbortSignal.timeout.
function signalOf(plan, c) {
  if (plan.abortAfter || plan.abortOpen) return c.signal;
  if (plan.abort === undefined && plan.timeoutSignal === undefined) return undefined;
  if (plan.timeoutSignal !== undefined) return AbortSignal.timeout(plan.timeoutSignal);
  const why = plan.abortReason ? new Error(plan.abortReason) : undefined;
  if (plan.abort === "before") c.abort(why);
  else if (typeof plan.abort === "number") setTimeout(() => c.abort(why), plan.abort);
  return c.signal;
}

async function read(res, plan, c) {
  switch (plan.read) {
    case "json":
      return JSON.stringify(await res.json());
    case "bytes":
      return String((await res.arrayBuffer()).byteLength);
    case "stream":
    case "iterate": {
      let n = 0;
      let chunks = 0;
      let max = 0;
      const stop = async () => {
        if (plan.abortAfter && chunks === plan.abortAfter) c.abort();
        return plan.stop && chunks === plan.stop;
      };
      if (plan.read === "iterate") {
        for await (const value of res.body) {
          n += value.byteLength;
          chunks++;
          max = Math.max(max, value.byteLength);
          if (await stop()) break;
        }
      } else {
        const reader = res.body.getReader();
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          n += value.byteLength;
          chunks++;
          max = Math.max(max, value.byteLength);
          if (await stop()) {
            await reader.cancel();
            break;
          }
        }
        const after = await reader.read();
        if (!after.done) return "read after the end";
      }
      return `${n} ${chunks > 0} ${max <= 65536} ${res.body.locked}`;
    }
    case "mixed": {
      const text = await res.text();
      let second;
      try {
        await res.body.getReader().read();
        second = "-";
      } catch (err) {
        second = `${err.name}: ${err.message}`;
      }
      return `${text} ${second} ${await res.text()}`;
    }
    case "mixed2": {
      await res.body.getReader().read();
      try {
        await res.text();
        return "-";
      } catch (err) {
        return `${err.name}: ${err.message}`;
      }
    }
    default:
      return await res.text();
  }
}

function coding() {
  const enc = new TextEncoder();
  const bytes = enc.encode("h\u00e9llo \u20ac \ud800");
  const d = new TextDecoder();
  const split = d.decode(Uint8Array.of(0xe2, 0x82), { stream: true }) + d.decode(Uint8Array.of(0xac));
  const bom = new TextDecoder().decode(Uint8Array.of(0xef, 0xbb, 0xbf, 0x61));
  const bad = new TextDecoder().decode(Uint8Array.of(0x61, 0xff, 0x62));
  let fatal;
  try {
    new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.of(0xc3));
    fatal = "-";
  } catch (err) {
    fatal = err.name;
  }
  let label;
  try {
    new TextDecoder("x-unknown");
    label = "-";
  } catch (err) {
    label = err.name;
  }
  return `coding ${bytes.length} ${new TextDecoder().decode(bytes) === "h\u00e9llo \u20ac \ufffd"} ${split === "\u20ac"} ${bom} ${bad === "a\ufffdb"} ${fatal} ${label}`;
}

function signals() {
  const c = new AbortController();
  const seen = [];
  c.signal.onabort = (e) => seen.push(`on ${e.type}`);
  c.signal.addEventListener("abort", () => seen.push("listener"), { once: true });
  const before = c.signal.aborted;
  c.abort();
  c.abort();
  let thrown;
  try {
    c.signal.throwIfAborted();
  } catch (err) {
    thrown = `${err.name}: ${err.message}`;
  }
  const pre = AbortSignal.abort("why");
  return `signals ${before} ${c.signal.aborted} ${seen.join(",")} ${thrown} ${pre.aborted} ${pre.reason}`;
}

// thrown is what f throws, as "Name: message".
function thrown(f) {
  try {
    f();
    return "-";
  } catch (err) {
    return `${err.name}: ${err.message}`;
  }
}

// classes: the web's Blob, File, FormData, Headers, Request and Response
// without the network, one line per class.
async function classes() {
  const { Blob, File, FormData, Headers, Request, Response } = kario;
  const out = [];
  const h = new Headers({ "Content-Type": "text/plain" });
  h.append("X-A", "1");
  h.append("x-a", " 2 ");
  h.append("Set-Cookie", "a=1");
  h.append("Set-Cookie", "b=2");
  out.push(`headers ${h.get("x-A")} ${h.has("CONTENT-TYPE")} ${[...h.keys()].join(",")} ${h.getSetCookie().length} ${h.get("nope")} ${new Headers([["k", "v"]]).get("k")} ${thrown(() => h.set("bad name", "x"))} ${thrown(() => h.set("x", "a\nb"))}`);
  const b = new Blob(["h\u00e9", new Uint8Array([33]), new Blob(["?"])], { type: "Text/Plain" });
  out.push(`blob ${b.size} ${b.type} ${await b.text()} ${(await b.slice(1, -1).arrayBuffer()).byteLength} ${await b.slice(-2).text()} ${b instanceof Blob} ${thrown(() => new Blob("x"))}`);
  const f = new File(["abc"], "a.txt", { type: "text/plain", lastModified: 5 });
  out.push(`file ${f.name} ${f.size} ${f.type} ${f.lastModified} ${f instanceof Blob}`);
  const fd = new FormData();
  fd.append("a", "1");
  fd.append("a", 2);
  fd.append("f", new Blob(["xyz"]));
  fd.set("s", "x");
  fd.set("s", "y");
  const file = fd.get("f");
  out.push(`form ${fd.getAll("a").join("+")} ${fd.has("s")} ${fd.get("s")} ${file.name} ${file instanceof File} ${[...fd.keys()].join(",")} ${thrown(() => fd.append("x", "v", "name"))}`);
  fd.delete("a");
  out.push(`form2 ${[...fd].length} ${fd.get("a")}`);
  const q = new Request("https://example.com/x", { method: "post", headers: { "X-B": "2" }, body: "hi" });
  out.push(`request ${q.method} ${q.url} ${q.headers.get("content-type")} ${await q.text()} ${await q.clone().text()} ${q.credentials} ${q.signal.aborted} ${thrown(() => new Request("https://e/", { body: "x" }))} ${thrown(() => new Request("https://e/", { method: "TRACE" }))}`);
  const r = new Response("ok", { status: 201, statusText: "Made", headers: { "X-C": "3" } });
  out.push(`response ${r.status} ${r.ok} ${r.statusText} ${r.headers.get("content-type")} ${r.headers.get("x-c")} ${await r.text()} ${await r.text()} ${r.type} ${r.url === ""} ${(await r.clone().blob()).size}`);
  const j = EarlyResponse.json({ n: 1 }, { status: 202 });
  out.push(`json ${j.status} ${j.headers.get("content-type")} ${JSON.stringify(await j.json())} ${kario.Response.redirect("https://e/", 303).headers.get("location")} ${thrown(() => new Response("x", { status: 99 }))} ${thrown(() => new Response("x", { status: 204 }))} ${new Response(null).body}`);
  const s = new Response(new Uint8Array(70000));
  let chunks = 0;
  for await (const c of s.body) chunks += c.byteLength > 0 ? 1 : 0;
  out.push(`stream ${chunks} ${s.bodyUsed} ${await s.text().catch((e) => e.message)}`);
  const early = new EarlyHeaders({ y: "1" });
  out.push(`early ${early instanceof kario.Headers} ${new kario.Headers() instanceof EarlyHeaders} ${new TaggedHeaders({ x: "t" }).tag} ${thrown(() => kario.Headers()).split(":")[0]}`);
  return out.join(" / ");
}

// web: a request made with the web's classes, and fetch's Response.
async function web(plan) {
  const { Blob, FormData, Headers, Request, Response } = kario;
  const url = plan.url;
  let res;
  switch (plan.web) {
    case "classes":
      return classes();
    case "form": {
      const fd = new FormData();
      fd.append("a", "1\n2");
      fd.append("q", 'say "hi"');
      fd.append("f", new Blob(["xyz"], { type: "text/plain" }), "x.txt");
      res = await kario.fetch(url, { method: "POST", body: fd });
      break;
    }
    case "blob":
      res = await kario.fetch(url, { method: "POST", body: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }) });
      break;
    case "request":
      res = await kario.fetch(new Request(url, { method: "PUT", headers: new Headers({ "X-App": "v" }), body: "x" }));
      break;
    case "override":
      res = await kario.fetch(new Request(url, { method: "PUT", body: "x" }), { method: "POST", headers: [["X-App", "w"]] });
      break;
    case "response": {
      res = await kario.fetch(url);
      const blob = await res.blob();
      return `response ${res instanceof Response} ${res.headers.get("X-Test")} ${res.headers["x-test"]} ${Object.keys(res.headers).includes("x-test")} ${typeof res.headers.get} ${res.type} ${JSON.stringify(res.statusText)} ${blob.size} ${blob.type.startsWith("text/plain")} ${await res.text()} ${thrown(() => res.headers.set("a", "b"))} ${thrown(() => res.clone())}`;
    }
  }
  return `${res.status} ${res.headers.get("x-test") ?? "-"} ${await res.text()}`;
}

function websocket(plan, c) {
  const log = [];
  let n = 0;
  const ws = new kario.WebSocket(plan.ws, plan.protocols, { ...(plan.options ?? {}), signal: signalOf(plan, c) });
  log.push(`state ${ws.readyState} ${ws.binaryType}`);
  ws.onopen = () => {
    log.push(`open ${ws.protocol} ${ws.readyState}`);
    if (plan.busy) {
      const until = Date.now() + plan.busy;
      while (Date.now() < until);
    }
    for (const m of plan.send ?? []) ws.send(typeof m === "string" ? m : Uint8Array.from(m.bin, (ch) => ch.charCodeAt(0)));
    if (plan.close) ws.close(...plan.close);
    if (plan.abortOpen) c.abort();
  };
  ws.addEventListener("message", (e) => {
    n++;
    if (!plan.quiet) log.push(typeof e.data === "string" ? `text ${e.data}` : `binary ${e.data.byteLength}`);
    if (plan.count && n === plan.count) {
      log.push(`got ${n}`);
      ws.close(1000, "enough");
    }
  });
  ws.addEventListener("error", (e) => log.push(`error ${e.message}`));
  ws.onclose = (e) => {
    log.push(`close ${e.code} ${e.reason} ${e.wasClean} ${ws.readyState}`);
    show(log.join(" | "));
  };
}

kario.on("action", async (e) => {
  if (e.action !== "fetch") return;
  const plan = JSON.parse(e.value);
  const c = new AbortController();
  if (plan.coding) return show(coding());
  if (plan.signals) return show(signals());
  if (plan.web) {
    try {
      return show(await web(plan));
    } catch (err) {
      return show(`err ${err.name}: ${err.message}`);
    }
  }
  if (plan.ws !== undefined) {
    try {
      websocket(plan, c);
    } catch (err) {
      show(`throw ${err.name}: ${err.message}`);
    }
    return;
  }
  const { url, init = {} } = plan;
  if (init.bytes) {
    init.body = Uint8Array.from(init.bytes, (ch) => ch.charCodeAt(0));
    delete init.bytes;
  }
  const signal = signalOf(plan, c);
  if (signal) init.signal = signal;
  let res;
  try {
    res = await kario.fetch(url, init);
    const body = await read(res, plan, c);
    show(`ok ${res.status} ${res.ok} ${res.redirected} ${res.headers["x-test"] ?? "-"} ${body}`);
  } catch (err) {
    show(`err ${err.name}: ${err.message}${res ? " after " + res.status : ""}`);
  }
});
