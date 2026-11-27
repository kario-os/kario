// What keepalive_test.go drives: keepalive WebSockets by name, what each
// does as a line on the screen (the last few).
import * as kario from "kario";

const lines = [];
const sockets = {};
const aborts = {};

function say(line) {
  lines.push(line);
  lines.splice(0, lines.length - 16);
  kario.reset({ id: "main", title: "keepalive", body: [{ kind: "text", text: lines.join(" | ") }] });
}

// digest is a message as the test checks it: short text as it is, other
// data its length and checksum.
function digest(data) {
  if (typeof data === "string" && data.length <= 32) return `text ${data}`;
  const bytes = typeof data === "string" ? new TextEncoder().encode(data) : new Uint8Array(data);
  let h = 0;
  for (const b of bytes) h = (h * 31 + b) >>> 0;
  return `${typeof data === "string" ? "text" : "binary"} ${bytes.length} ${h}`;
}

kario.on("start", () => say("ready"));

kario.on("event", (name, value) => {
  if (name !== "keepalive") return;
  say(`event ${value}`);
  if (value !== "beat") return;
  for (const [n, ws] of Object.entries(sockets)) {
    if (ws.readyState === 1) {
      ws.send("PING");
      say(`${n} sent PING`);
    }
  }
});

kario.on("action", (e) => {
  const p = e.value ? JSON.parse(e.value) : {};
  switch (e.action) {
    case "open": {
      const options = { keepalive: p.every };
      if (p.abortable) options.signal = (aborts[p.name] = new AbortController()).signal;
      let ws;
      try {
        ws = new kario.WebSocket(p.url, [], options);
      } catch (err) {
        say(`${p.name} throw ${err.name}: ${err.message}`);
        return;
      }
      sockets[p.name] = ws;
      say(`${p.name} state ${ws.readyState}`);
      ws.onopen = () => {
        say(`${p.name} open`);
        if (p.hello) ws.send(p.hello);
      };
      ws.onmessage = (m) => say(`${p.name} ${digest(m.data)}`);
      ws.onerror = (err) => say(`${p.name} error ${err.message}`);
      ws.onclose = (c) => say(`${p.name} close ${c.code} ${c.reason} ${c.wasClean}`);
      break;
    }
    case "close":
      sockets[p.name].close(...(p.args ?? []));
      break;
    case "abort":
      aborts[p.name].abort();
      break;
    case "call":
      kario.call("net.keepalive", { host: "127.0.0.1", port: 1, every_s: 600 }).then(
        () => say("call ok"),
        (err) => say(`call ${err.message}`),
      );
      break;
  }
});
