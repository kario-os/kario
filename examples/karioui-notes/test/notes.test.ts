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
  expectEqual(app.calls.at(-1), { cap: "clipboard.set", args: { text: "Milk" } }, "the last call");
  await app.key("esc");
  app.at("main");
});

test("push data adds a note once; the app says so only when the system did not", async () => {
  const app = await launch({ capabilities: { "push.register": () => ({ token: "tok-1" }) } });
  expectEqual(app.calls[0], { cap: "push.register" }, "the first call");
  const push = (id: string, note: string, notified = false) =>
    app.event("push", JSON.stringify({ id, data: JSON.stringify({ note }), sent: 1, notified }));
  await push("p1", "Milk");
  app.expect("Received “Milk”");
  app.expect("1 notes");
  await push("p1", "Milk");
  app.expect("1 notes");
  await app.event("push", JSON.stringify({ id: "p2", data: "not JSON", sent: 1 }));
  await push("p3", "Bread", true);
  app.expect("2 notes");
  app.expectGone("Received “Bread”");
  await app.choose("Push and reminders");
  app.at("push");
  app.expect("tok-1");
});

test("the push token goes to the app's own server with fetch; a refusal says what the server answered", async () => {
  let status = 200;
  const app = await launch({
    capabilities: { "push.register": () => ({ token: "tok-1" }) },
    fetch: () => ({ status }),
  });
  await app.choose("Push and reminders");
  await app.key("down");
  await app.key("down");
  await app.type("https://notes.example/");
  await app.key("enter");
  app.expect("Sent the token to https://notes.example");
  const req = app.fetches.at(-1)!;
  expectEqual(
    { url: req.url, method: req.method, type: req.headers["Content-Type"], body: JSON.parse(String.fromCharCode(...req.body!)) },
    { url: "https://notes.example/register", method: "POST", type: "application/json", body: { app: "com.example.kariouinotes", token: "tok-1" } },
    "the request",
  );
  status = 403;
  await app.key("enter");
  app.expect("The server answered 403");
});

test("a note a push brought in the background is kept for the next process", async () => {
  const app = await launch();
  await app.background();
  await app.event("push", JSON.stringify({ id: "p1", data: JSON.stringify({ note: "Milk" }), sent: 1, notified: true }));
  app.kill();
  const again = await launch({ storage: { ...app.storage } });
  again.expect("Milk");
  again.expect("1 notes");
});

test("the daily reminder is a background job: it notifies, and turning it off cancels it", async () => {
  const app = await launch({ capabilities: { "job.schedule": () => ({ ok: true }), "job.cancel": () => ({ ok: true }) } });
  await app.type("Milk");
  await app.key("enter");
  await app.event("job", "digest");
  expectEqual(app.notifications, [], "the notifications while it is off");
  await app.choose("Push and reminders");
  app.expect("Daily reminder");
  app.expect("Off");
  await app.key("enter");
  app.expect("On");
  expectEqual(app.calls.at(-1), { cap: "job.schedule", args: { id: "digest", every_min: 1440 } }, "the call");
  await app.background();
  await app.event("job", "digest");
  expectEqual(app.notifications, [{ id: "digest", title: "1 notes", body: "Milk" }], "the notifications");
  const again = await app.relaunch();
  again.at("push");
  again.expect("On");
  await again.key("enter");
  again.expect("Off");
  expectEqual(again.calls.at(-1), { cap: "job.cancel", args: { id: "digest" } }, "the call");
});

test("a job the system refuses leaves the reminder off and says why", async () => {
  const app = await launch({
    capabilities: {
      "job.schedule": () => {
        throw new Error("a job needs an id and at least 15 minutes between runs");
      },
    },
  });
  await app.choose("Push and reminders");
  await app.key("enter");
  app.expect("Reminder not changed: a job needs an id and at least 15 minutes between runs");
  app.expect("Off");
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
