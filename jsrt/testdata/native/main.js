// What native_test.go drives: native:probe (probe.c) from JavaScript.
import * as kario from "kario";

let note = "ready";
const page = () => ({ id: "main", title: "Native", body: [{ kind: "text", text: note }] });
const say = (text) => {
  note = text;
  kario.update(page());
};

kario.on("start", () => kario.reset(page()));

kario.on("action", async (e) => {
  try {
    if (e.action === "load") {
      await import(e.value);
      say("loaded " + e.value);
      return;
    }
    const p = await import("native:probe");
    switch (e.action) {
      case "sync": {
        const bytes = new Uint8Array([1, 2, 250]);
        const n = p.invert(bytes.subarray(1));
        const rev = new Uint8Array(p.reversed(new Uint8Array([1, 2, 3]).buffer));
        const echoed = JSON.stringify(p.echo({ a: [1, "x\n"], b: null }));
        const t = p.types(undefined, null, true, 1.5, "s", new Uint8Array(1), {}, [1]);
        say(`sync ${p.add(2, 3)} ${p.upper("abc")} ${n} ${bytes.join(",")} ${rev.join(",")} ${echoed} ${t} ${p.engine.length > 0} ${p.abi}`);
        break;
      }
      case "throw": {
        const out = [];
        for (const f of [() => p.fail("nope"), () => p.badJson(), () => p.upper(3)]) {
          try {
            f();
            out.push("returned");
          } catch (err) {
            // Engines word their syntax errors differently: the name only.
            out.push(err.name === "SyntaxError" ? err.name : `${err instanceof Error ? err.name : "?"}:${err.message.split(" ")[0]}`);
          }
        }
        say("threw " + out.join(" "));
        break;
      }
      case "async": {
        const t0 = Date.now();
        const all = await Promise.all([p.slow(150), p.slow(150), p.slow(150)]);
        const ms = Date.now() - t0;
        const bytes = new Uint8Array([4, 5, 6]);
        const rev = new Uint8Array(await p.reversedAsync(bytes));
        const echoed = JSON.stringify(await p.echoAsync("q\"\t"));
        let rejected = "";
        try {
          await p.failAsync("later");
        } catch (err) {
          rejected = err.message;
        }
        say(`async ${all.every((x) => x.worker)} ${ms < 400} ${rev.join(",")} ${bytes.join(",")} ${echoed} ${rejected}`);
        break;
      }
      case "events": {
        const got = [];
        p.on("tick", (v) => got.push(v.i));
        p.on("done", (v) => say(`events ${got.join(",")} ${v === undefined}`));
        p.start(50);
        break;
      }
      case "exec":
        say(`exec ${p.exec(e.value)} thread ${p.thread()}`);
        break;
      case "extra": {
        const out = [];
        for (const f of [() => p.typed(21), () => p.typed("x"), () => p.typed(1.5), () => p.thrown(), () => p.unknown()]) {
          try {
            out.push(String(f()));
          } catch (err) {
            out.push(`${err instanceof Error ? err.name : "?"}:${err.message}`);
          }
        }
        try {
          await p.thrownAsync();
        } catch (err) {
          out.push(`rejected:${err.message}`);
        }
        say("extra " + out.join(" | "));
        break;
      }
      case "crash":
        p.crash();
        break;
      case "crashAsync":
        await p.crashAsync();
        break;
      case "abort":
        p.abort();
        break;
    }
  } catch (err) {
    say("refused " + err.message);
  }
});
