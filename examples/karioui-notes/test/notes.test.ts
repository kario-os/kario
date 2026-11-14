// The tutorial's app under a fake Shell (kario test).
import { expectEqual, launch, test } from "@kario/test";

test("a note is added from the input, shown newest first, counted", async () => {
  const app = await launch();
  app.expect("No notes yet");
  await app.type("Milk");
  await app.key("enter");
  await app.type("Bread");
  await app.key("enter");
  app.expect("Added “Bread”");
  app.expect("2 notes");
  expectEqual(app.screen.body.filter((n) => n.kind === "item").map((n) => n.title), ["Bread", "Milk"], "the list");
  await app.wait("2 notes");
  await app.wait("Notes\nWrite a note\nBread", 4000);
});

test("OK opens a note; the clipboard is a capability; Back returns", async () => {
  const app = await launch({ capabilities: { "clipboard.set": () => ({}) } });
  await app.type("Milk");
  await app.key("enter");
  await app.key("down");
  await app.key("enter");
  app.at("note");
  app.expect("4 characters");
  await app.key("enter");
  app.expect("Copied “Milk”");
  expectEqual(app.calls, [{ cap: "clipboard.set", args: { text: "Milk" } }], "the calls");
  await app.key("esc");
  app.at("main");
});

test("a refused capability says why; cancelling says so", async () => {
  let answer = "the system said no";
  const app = await launch({
    capabilities: {
      "clipboard.set": () => {
        throw new Error(answer);
      },
    },
  });
  await app.type("Milk");
  await app.key("enter");
  await app.focusOn("Milk");
  await app.type("c");
  app.expect("Copy failed: the system said no");
  answer = "cancelled";
  await app.choose("Copy");
  app.expect("Not copied");
});

test("delete asks first, the right soft key clears all", async () => {
  const app = await launch();
  for (const t of ["a", "b", "c"]) {
    await app.type(t);
    await app.key("enter");
  }
  app.focusOn("b");
  await app.choose("Delete");
  expectEqual(app.confirmed, ["Delete this note?"], "the confirmations");
  app.expect("2 notes");
  await app.key("f2");
  app.expect("No notes yet");
});

test("notes outlive the process; the open note comes back after a tombstone", async () => {
  const app = await launch();
  await app.type("Milk");
  await app.key("enter");
  await app.press("Milk");
  app.at("note");
  await app.background();
  const again = await app.relaunch();
  again.at("note");
  again.expect("Milk");
  await again.key("esc");
  again.expect("1 notes");
  const fresh = await again.relaunch({ restored: undefined });
  fresh.at("main");
  fresh.expect("Milk");
});

test("Spanish", async () => {
  const app = await launch({ lang: "es" });
  app.expect("Todavía no hay notas");
});
