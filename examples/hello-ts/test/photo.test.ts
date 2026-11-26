// The sample's photo under a fake Shell (kario test): the picker's answer
// brings the file, which the app reads and closes.
import { expectEqual, launch, test, withFiles } from "@kario/test";

const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13]);

test("a picked photo is read as a file and noted, the file closed", async () => {
  const app = await launch({
    storage: {},
    capabilities: { "media.pick": () => withFiles({ id: "media:p1", kind: "image", title: "Sunset", mime: "image/png" }, png) },
  });
  await app.choose("Attach a photo");
  app.expect("Attached a photo");
  app.expect("PNG photo “Sunset”: 12 bytes");
  expectEqual(app.calls.at(-1), { cap: "media.pick", args: { kind: "image" } }, "the call");
  expectEqual(app.openFiles, 0, "files left open");
});

test("a cancelled picker adds nothing", async () => {
  const app = await launch({
    storage: {},
    capabilities: {
      "media.pick": () => {
        throw new Error("cancelled");
      },
    },
  });
  await app.choose("Attach a photo");
  app.expect("No photo attached");
  app.expect("No notes yet");
});
