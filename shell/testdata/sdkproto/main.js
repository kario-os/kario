// An app of the TypeScript runtimes (kario-jsrt, Node.js) for
// TestSDKProtoMajors (shell/sdkproto_test.go): the title of its page says
// what it saw.
import { on, reset, update, toast } from "kario";

on("start", () => {
  reset({ id: "main", title: "started", body: [] });
  toast("hello from js");
});
on("action", (e) => update({ id: "main", title: "action on " + e.screen, body: [] }));
on("resync", () => update({ id: "main", title: "resynced", body: [] }));
