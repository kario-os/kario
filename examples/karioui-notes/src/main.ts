// The tutorial's app (34-app-tutorial.md), written with KarioUI: notes in a
// list, an input that adds one, a page per note, the clipboard capability,
// storage that outlives the process, a page that comes back after the
// system ended the app, notes a server sends by push, the push token sent
// to that server with fetch, and a daily reminder that runs as a background
// job.
import { Button, Input, Item, List, Text, act, app, appId, call, cancelled, fetch, nav, notify, page, state, tr } from "@kario/ui";
import type { PushEvent } from "kario";

interface Note {
  text: string;
  at: number;
  /** The push it came in, so that a push delivered twice adds it once. */
  push?: string;
}

const notes = state<Note[]>([], { store: "notes" });
const status = state("", { save: "status" });
const token = state("");
const reminder = state(false, { store: "reminder" });
const server = state("", { store: "server" });

// The background job's id: scheduling it again replaces it, and the "job"
// event carries it.
const DIGEST = "digest";
const DAY_MIN = 24 * 60;

function say(text: string): void {
  status.set(text);
  setTimeout(() => {
    if (status.value === text) status.set("");
  }, 3000);
}

async function copy(text: string): Promise<void> {
  try {
    await call("clipboard.set", { text });
    say(tr("Copied “%s”", text));
  } catch (err) {
    say(cancelled(err) ? tr("Not copied") : tr("Copy failed: %s", (err as Error).message));
  }
}

async function register(): Promise<void> {
  try {
    token.set((await call("push.register")).token);
  } catch (err) {
    console.warn("push.register:", (err as Error).message);
  }
}

// sendToken tells the app's own server where to push: a POST of the token
// to <server>/register (the manifest asks for "net").
async function sendToken(): Promise<void> {
  const base = server.value.trim().replace(/\/+$/, "");
  if (!base) return;
  if (!token.value) {
    say(tr("Not registered for push yet"));
    return;
  }
  try {
    const res = await fetch(base + "/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app: appId, token: token.value }),
    });
    say(res.ok ? tr("Sent the token to %s", base) : tr("The server answered %d", res.status));
  } catch (err) {
    say(tr("Not sent: %s", (err as Error).message));
  }
}

// received adds the note a push's data carries ({"note": "..."}); the
// system may have thawed the app or started it in the background for it.
function received(p: PushEvent): void {
  let text: unknown;
  try {
    text = JSON.parse(p.data ?? "").note;
  } catch {
    return;
  }
  if (typeof text !== "string" || !text.trim() || notes.value.some((n) => n.push === p.id)) return;
  const t = text.trim();
  notes.update((list) => [...list, { text: t, at: Date.now(), push: p.id }]);
  if (!p.notified) say(tr("Received “%s”", t));
}

async function setReminder(on: boolean): Promise<void> {
  try {
    if (on) await call("job.schedule", { id: DIGEST, every_min: DAY_MIN });
    else await call("job.cancel", { id: DIGEST });
    reminder.set(on);
  } catch (err) {
    say(tr("Reminder not changed: %s", (err as Error).message));
  }
}

// digest is the background job's work: the app has a few seconds before it
// is frozen again, enough for one notification.
function digest(): void {
  const last = notes.value.at(-1);
  if (!reminder.value || !last) return;
  notify({ id: DIGEST, title: tr("%d notes", notes.value.length), body: last.text });
}

const PushPage = page("push", () => ({
  title: tr("Push and reminders"),
  body: [
    Item(tr("Daily reminder"), {
      id: "reminder",
      trailing: reminder.value ? tr("On") : tr("Off"),
      onOk: act("reminder", reminder.value ? tr("Turn off") : tr("Turn on"), () => setReminder(!reminder.value)),
    }),
    Item(tr("Push token"), {
      id: "token",
      subtitle: token.value || tr("Not registered"),
      actions: token.value ? [act("copy", tr("Copy"), () => copy(token.value), { key: "c" })] : [],
    }),
    Input("server", { placeholder: tr("Your server (https://…), OK sends the token"), keyboard: "url", value: server, onSubmit: () => void sendToken() }),
    status.value && Text(status.value, { style: "accent" }),
  ],
}));

const NotePage = page("note", (p: { index: number }) => {
  const n = notes.value[p.index];
  if (!n) return { title: tr("Note"), body: [Text(tr("This note was deleted"), { style: "dim" })] };
  return {
    title: tr("Note"),
    body: [
      Text(n.text, { style: "big" }),
      Text(tr("%d characters", [...n.text].length), { style: "dim" }),
      Button(tr("Copy"), act("copy", tr("Copy"), () => copy(n.text))),
      status.value && Text(status.value, { style: "accent" }),
    ],
  };
});

const Main = page("main", () => ({
  title: tr("Notes"),
  focus: "new",
  body: [
    Input("new", {
      placeholder: tr("Write a note"),
      clear: true,
      onSubmit: (text) => {
        const t = text.trim();
        if (!t) return;
        notes.update((list) => [...list, { text: t, at: Date.now() }]);
        say(tr("Added “%s”", t));
      },
    }),
    status.value && Text(status.value, { style: "accent" }),
    List(notes.value, (n, i) =>
      Item(n.text, {
        id: `n${n.at}`,
        onOk: act("open", tr("Open"), () => nav.push(NotePage, { index: i })),
        actions: [
          act("copy", tr("Copy"), () => copy(n.text), { key: "c" }),
          act("delete", tr("Delete"), () => notes.update((list) => list.filter((x) => x !== n)), {
            group: "danger",
            confirm: tr("Delete this note?"),
          }),
        ],
      }),
    ).reverse(),
    Text(notes.value.length ? tr("%d notes", notes.value.length) : tr("No notes yet"), { style: "dim" }),
  ],
  actions: [act("push", tr("Push and reminders"), () => nav.push(PushPage, undefined), { key: "p" })],
  right: act("clear", tr("Clear all"), () => notes.set([]), { group: "danger", confirm: tr("Delete all notes?") }),
}));

app({
  root: Main,
  onStart: () => void register(),
  onEvent(name, value) {
    if (name === "push") received(JSON.parse(value) as PushEvent);
    else if (name === "job" && value === DIGEST) digest();
  },
});
