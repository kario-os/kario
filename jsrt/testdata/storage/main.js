// What storage_test.go drives: saves the runtime writes in the background.
import * as kario from "kario";

kario.on("start", () => kario.reset({ id: "main", title: "storage", body: [] }));

kario.on("action", (e) => {
  switch (e.action) {
    case "save":
      kario.storage.set("v", e.value);
      kario.toast("saved " + kario.storage.get("v"));
      break;
    case "many": {
      const n = Number(e.value);
      for (let i = 1; i <= n; i++) kario.storage.set("n", i);
      kario.toast("set " + kario.storage.get("n"));
      break;
    }
    case "flush":
      kario.storage.set("v", e.value);
      kario.storage.flush().then(
        () => kario.toast("flushed " + kario.storage.get("v")),
        (err) => kario.toast("flush failed " + kario.storage.get("v") + " " + err.message),
      );
      break;
  }
});

kario.on("save", () => ({ v: kario.storage.get("v") }));
