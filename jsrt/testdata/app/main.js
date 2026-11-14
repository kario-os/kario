// What runtime_test.go drives: every part of the kario module once.
import * as kario from "kario";

let count = kario.storage.get("count") ?? 0;
let note = "";

const screen = () => ({
  id: "main",
  title: kario.tr("Counter"),
  body: [
    { kind: "text", text: kario.tr("Count %d", count) },
    { kind: "text", text: note },
    { kind: "input", id: "q", live: true },
  ],
});

kario.on("start", (arg, restored) => {
  if (restored !== undefined) note = "restored " + JSON.stringify(restored);
  if (arg) note = "arg " + arg;
  kario.reset(screen());
});

kario.on("action", async (e) => {
  switch (e.action) {
    case "inc":
      count++;
      kario.storage.set("count", count);
      break;
    case "copy":
      try {
        const r = await kario.call("clipboard.set", { text: "x" });
        note = "ok " + JSON.stringify(r);
      } catch (err) {
        note = "err " + err.message + " " + err.cap + " " + kario.cancelled(err);
      }
      break;
    case "timer":
      setTimeout((a) => {
        note = "timer " + a;
        kario.update(screen());
      }, 30, "fired");
      return;
    case "cancel": {
      const id = setTimeout(() => {
        note = "cancelled timer ran";
        kario.update(screen());
      }, 20);
      clearTimeout(id);
      setTimeout(() => {
        note = "cancel kept";
        kario.update(screen());
      }, 60);
      return;
    }
    case "interval": {
      let n = 0;
      const id = setInterval(() => {
        if (++n === 3) {
          clearInterval(id);
          note = "interval " + n;
          kario.update(screen());
        }
      }, 10);
      return;
    }
    case "push":
      kario.push({ id: "second", title: "Second", body: [] });
      return;
    case "lang":
      note = kario.lang + " " + kario.tr("Hello") + " " + kario.tr("%d of %s", 3.7, "x") + " " + kario.tr("%s and %s", "a");
      break;
    case "big":
      try {
        kario.storage.set("big", "y".repeat(2 << 20));
      } catch (err) {
        note = "refused " + err.message + " " + kario.storage.keys().join(",");
      }
      break;
    case "throw":
      throw new Error("boom");
    case "fields":
      note = "fields " + JSON.stringify(e.fields) + " " + e.screen;
      break;
  }
  kario.update(screen());
});

kario.on("input", (e) => {
  note = "input " + e.node + "=" + e.value;
  kario.update(screen());
});

kario.on("key", (k) => {
  note = "key " + k;
  kario.update(screen());
});

kario.on("save", () => ({ count, note }));

kario.on("event", (name, value) => {
  note = "event " + name + " " + value;
  kario.update(screen());
});

kario.on("popped", (id) => {
  note = "popped " + id;
  kario.update(screen());
});

kario.on("resync", () => {
  note = "resynced";
  kario.update(screen());
});

kario.on("notif", (n) => {
  note = "notif " + n.id + " " + n.action + " " + n.text;
  kario.update(screen());
});

console.log("started", { count }, kario.appId);
