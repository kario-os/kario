// Keepalive WebSocket Sample, the TypeScript side of examples/keepalive
// (21-push.md §8.3): a messenger's one connection of its own, a WebSocket
// with the keepalive option. The system dials the server for it and the
// runtime speaks WebSocket on that socket; the app sends a heartbeat only
// when the system says so (the keepalive event, beat), never on a timer:
// frozen in the background it is thawed for those heartbeats and for
// messages the server sends. A restarted system that could not keep the
// connection says so (lost): the app then connects again. HELLO on
// connecting, PING as the heartbeat, any other text message a message.
import { WebSocket, on, reset, storage, tr, update, type Node, type Screen } from "kario";

const EVERY_S = 600;

let server = storage.get<string>("server") ?? "";
let ws: WebSocket | undefined;
let connected = false;
let beats = 0;
let last = "";

function screen(): Screen {
  const body: Node[] = [
    { kind: "input", id: "server", placeholder: tr("Server (wss://…)"), keyboard: "url", value: server, submit: "connect" },
  ];
  if (!connected) {
    body.push({ kind: "text", style: "dim", text: tr("Not connected") });
    return { id: "main", title: tr("Keepalive WebSocket Sample"), focus: "server", body };
  }
  body.push(
    { kind: "text", style: "accent", text: tr("Connected to %s · heartbeat every %d s", server, EVERY_S) },
    { kind: "text", text: tr("Heartbeats sent: %d", beats) },
  );
  if (last) body.push({ kind: "text", text: tr("Last message: %s", last) });
  return {
    id: "main",
    title: tr("Keepalive WebSocket Sample"),
    focus: "server",
    body,
    actions: [{ id: "close", label: tr("Disconnect"), key: "x" }],
  };
}

function connect(): void {
  // The one before closes once this one has its connection ("replaced").
  let c: WebSocket;
  try {
    c = new WebSocket(server, [], { keepalive: EVERY_S });
  } catch (err) {
    console.log(`keepalive: ${String(err)}`);
    last = tr("Not connected: %s", (err as Error).message);
    update(screen());
    return;
  }
  ws = c;
  c.onopen = () => {
    connected = true;
    beats = 0;
    last = "";
    c.send("HELLO");
    console.log(`keepalive connected: ${server} every ${EVERY_S} s`);
    update(screen());
  };
  c.onmessage = (e) => {
    last = typeof e.data === "string" ? e.data : tr("(%d bytes)", e.data.byteLength);
    console.log(`keepalive got: ${last}`);
    update(screen());
  };
  c.onclose = (e) => {
    console.log(`keepalive closed: ${e.code} ${e.reason}`);
    if (ws !== c) return;
    ws = undefined;
    connected = false;
    update(screen());
  };
}

on("start", () => reset(screen()));

on("event", (name, value) => {
  if (name !== "keepalive") return;
  if (value === "beat" && ws?.readyState === WebSocket.OPEN) {
    ws.send("PING");
    beats++;
    console.log(`keepalive beat ${beats}`);
    update(screen());
  } else if (value === "lost") {
    console.log("keepalive lost: connecting again");
    connect();
  }
});

on("action", (e) => {
  if (e.action === "connect") {
    server = e.value.trim();
    storage.set("server", server);
    connect();
  } else if (e.action === "close") {
    ws?.close(1000, "disconnected");
  }
});
