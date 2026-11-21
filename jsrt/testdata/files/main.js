// What files_test.go drives: a capability an action, what came with its
// reply as one line.
import * as kario from "kario";

const show = (text) => kario.reset({ id: "main", title: "files", body: [{ kind: "text", text }] });
const text = (bytes) => String.fromCharCode(...bytes);
const fail = (f) => {
  try {
    f();
    return "-";
  } catch (err) {
    return `${err.name}: ${err.message}`;
  }
};

kario.on("start", () => show("ready"));

kario.on("action", async (e) => {
  try {
    switch (e.action) {
      case "pick": {
        const it = await kario.call("media.pick", { kind: "image" });
        const f = it.files[0];
        const head = text(f.read(4));
        const rest = f.read().length;
        const all = text(f.readAll());
        const size = f.size;
        f.close();
        f.close();
        show(`picked ${it.files.length} ${size} ${head} ${rest} ${all} ${JSON.stringify(it)} ${f.closed} ${fail(() => f.read())}`);
        break;
      }
      case "many": {
        const it = await kario.call("file.pick", {});
        show(`many ${it.files.map((f) => text(f.readAll())).join(",")}`);
        for (const f of it.files) f.close();
        break;
      }
      case "big": {
        const it = await kario.call("file.open", { id: "f1" });
        const f = it.files[0];
        show(`big ${f.size} ${fail(() => f.readAll())} ${f.read(1 << 20).length} ${fail(() => f.read(2 << 20))} ${fail(() => f.read(0))}`);
        f.close();
        break;
      }
      case "ignore":
        await kario.call("file.pick", {});
        show("ignored");
        break;
      case "keep": {
        const it = await kario.call("file.pick", {});
        globalThis.kept = (globalThis.kept ?? []).concat(it.files);
        show(`kept ${globalThis.kept.length}`);
        break;
      }
      case "release":
        for (const f of globalThis.kept ?? []) f.close();
        globalThis.kept = [];
        show("released");
        break;
      case "play": {
        const s = await kario.call("audio.stream", { name: "beep" });
        const samples = Int16Array.from({ length: 480 }, (_, i) => i - 240);
        const n = s.ring.write(samples);
        const more = s.ring.write(new Int16Array(100000));
        s.ring.finish();
        show(`play ${s.id} ${s.ring.rate} ${s.ring.capacity} ${n} ${more} ${s.ring.buffered} ${s.ring.finished} ${JSON.stringify(s)}`);
        s.ring.close();
        show(`play closed ${s.ring.closed} ${fail(() => s.ring.write(samples))}`);
        break;
      }
      case "record": {
        const s = await kario.call("audio.record", { name: "memo" });
        const buf = new Int16Array(1000);
        const n = s.ring.read(buf);
        show(`record ${n} ${buf[0]} ${buf[n - 1]} ${s.ring.buffered} ${fail(() => s.ring.read([1, 2]))}`);
        s.ring.close();
        break;
      }
    }
  } catch (err) {
    show(`err ${err.message}`);
  }
});
