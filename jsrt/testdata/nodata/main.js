// Storage without a data directory fails with an error the app can catch.
import * as kario from "kario";

let note = "storage worked";
try {
  kario.storage.set("k", "v");
} catch (e) {
  note = "caught " + e.message;
}

kario.on("start", () => {
  kario.reset({ id: "main", title: "nodata", body: [{ kind: "text", text: note }] });
});
