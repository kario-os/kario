// The tutorial's app (34-app-tutorial.md), written with KarioUI: notes in a
// list, an input that adds one, a page per note, the clipboard capability,
// storage that outlives the process and a page that comes back after the
// system ended the app.
import { Button, Input, Item, List, Text, act, app, call, cancelled, nav, page, state, tr } from "@kario/ui";

interface Note {
  text: string;
  at: number;
}

const notes = state<Note[]>([], { store: "notes" });
const status = state("", { save: "status" });

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
  right: act("clear", tr("Clear all"), () => notes.set([]), { group: "danger", confirm: tr("Delete all notes?") }),
}));

app(Main);
