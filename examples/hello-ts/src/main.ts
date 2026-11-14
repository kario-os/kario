// A TypeScript app on kario-jsrt (29-app-development.md): notes in a list,
// an input that adds one, a page per note, a soft key, the clipboard
// capability, storage that outlives the process, the open page kept for a
// tombstone, and a timer that clears the status line.
import {
  call,
  cancelled,
  on,
  push,
  reset,
  storage,
  tr,
  update,
  type ActionEvent,
  type Node,
  type Screen,
} from "kario";

interface Note {
  text: string;
  at: number;
}

/** What the app hands over on leaving the screen (on "save"). */
interface Saved {
  open?: number;
}

let notes: Note[] = storage.get<Note[]>("notes") ?? [];
let open: number | undefined;
let status = "";
let statusTimer: number | undefined;

function keep(): void {
  storage.set("notes", notes);
}

/** say shows text under the notes for a few seconds. */
function say(text: string): void {
  status = text;
  clearTimeout(statusTimer);
  statusTimer = setTimeout(() => {
    status = "";
    redraw();
  }, 3000);
}

function statusLine(): Node[] {
  return status ? [{ kind: "text", style: "accent", text: status }] : [];
}

function mainScreen(): Screen {
  const body: Node[] = [
    { kind: "input", id: "new", placeholder: tr("Write a note"), submit: "add", clear: true },
    ...statusLine(),
  ];
  for (let i = notes.length - 1; i >= 0; i--) {
    body.push({
      kind: "item",
      id: `n${i}`,
      title: notes[i].text,
      actions: [
        { id: "open", label: tr("Open"), primary: true },
        { id: "copy", label: tr("Copy"), key: "c" },
        { id: "delete", label: tr("Delete"), group: "danger", confirm: tr("Delete this note?") },
      ],
    });
  }
  body.push(
    notes.length
      ? { kind: "text", style: "dim", text: tr("%d notes", notes.length) }
      : { kind: "text", style: "dim", text: tr("No notes yet") },
  );
  return {
    id: "main",
    title: tr("TypeScript notes"),
    focus: "new",
    body,
    actions: [{ id: "clear", label: tr("Clear all"), group: "danger", confirm: tr("Delete all notes?") }],
    right: "clear",
  };
}

function notePage(i: number): Screen {
  const n = notes[i];
  return {
    id: "note",
    title: tr("Note"),
    body: [
      { kind: "text", style: "big", text: n.text },
      { kind: "text", style: "dim", text: tr("%d characters", [...n.text].length) },
      { kind: "button", id: "copy", text: tr("Copy"), actions: [{ id: "copy", label: tr("Copy"), primary: true }] },
      ...statusLine(),
    ],
  };
}

function redraw(): void {
  update(mainScreen());
  if (open !== undefined && notes[open]) update(notePage(open));
}

async function copy(text: string): Promise<void> {
  try {
    await call("clipboard.set", { text });
    say(tr("Copied “%s” to the clipboard", text));
  } catch (err) {
    say(cancelled(err) ? tr("Not copied") : tr("Copy failed: %s", (err as Error).message));
  }
}

on("start", (_arg: string, restored: unknown) => {
  reset(mainScreen());
  const saved = restored as Saved | undefined;
  if (saved?.open !== undefined && notes[saved.open]) {
    open = saved.open;
    push(notePage(open));
  }
});

on("action", async (e: ActionEvent) => {
  const i = e.node.startsWith("n") ? Number(e.node.slice(1)) : open;
  switch (e.action) {
    case "add": {
      const text = e.value.trim();
      if (!text) return;
      notes.push({ text, at: Date.now() });
      keep();
      say(tr("Added “%s”", text));
      break;
    }
    case "open":
      if (i === undefined || !notes[i]) return;
      open = i;
      push(notePage(i));
      return;
    case "copy":
      if (i !== undefined && notes[i]) await copy(notes[i].text);
      break;
    case "delete":
      if (i !== undefined) notes.splice(i, 1);
      keep();
      break;
    case "clear":
      notes = [];
      keep();
      break;
  }
  redraw();
});

on("popped", (screen: string) => {
  if (screen === "note") open = undefined;
});

on("save", (): Saved => ({ open }));
