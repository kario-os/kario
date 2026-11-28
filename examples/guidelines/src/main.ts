// The do and don't pages of the app guidelines (33-app-guidelines.md §1.3),
// which scripts/guideline-shots.sh draws for the document: the first page
// opens one by its name (the script fills it in) or from its list. The
// "dont" pages do what the guidelines advise against, on purpose.
import { Button, Input, Item, List, Text, Tile, act, app, call, nav, page, tr, type Act, type Page, type View } from "@kario/ui";

const noop = () => {};

const titles = () => [tr("Milk"), tr("Bread"), tr("Call the plumber")];

function itemActions(hidden: boolean): Act[] {
  return [
    act("copy", tr("Copy"), noop, { key: "c", hidden }),
    act("delete", tr("Delete"), noop, { key: "5", hidden, group: "danger", confirm: tr("Delete this note?") }),
  ];
}

const notes = (hidden: boolean) =>
  List(titles(), (t, i) => Item(t, { id: `n${i}`, onOk: act("open", tr("Open"), noop), actions: itemActions(hidden) }));

const PAY = () => tr("Pay ¥18.00 to “Morning Coffee”");
const BORDER = "+------------------+";

const EXAMPLES: Record<string, () => View> = {
  // The script opens the Options menu on these two.
  "menu-do": () => ({ title: tr("Notes"), body: [notes(false)] }),
  "menu-dont": () => ({ title: tr("Notes"), body: [notes(true), Text(tr("Press 5 to delete, C to copy"), { style: "dim" })] }),
  "list-do": () => ({ title: tr("Notes"), body: [notes(false), Text(tr("%d notes", titles().length), { style: "dim" })] }),
  "list-dont": () => ({
    title: tr("Notes"),
    body: [titles().map((t, i) => [Item(t, { id: `n${i}`, onOk: act("open", tr("Open"), noop) }), Button(tr("Delete"), noop, { id: `d${i}` })])],
  }),
  "input-do": () => ({
    title: tr("New note"),
    body: [Input("text", { placeholder: tr("Write a note"), onSubmit: noop }), Text(tr("OK adds it"), { style: "dim" })],
  }),
  "input-dont": () => ({
    title: tr("New note"),
    body: [Input("text", { placeholder: tr("Write a note") }), Button(tr("OK"), noop, { id: "ok" })],
  }),
  "status-do": () => ({
    title: tr("Sync"),
    body: [
      Text(tr("Synced 2 minutes ago"), { style: "ok" }),
      Text(tr("2 notes could not be sent"), { style: "warn" }),
      Item(tr("Send again"), { id: "retry", onOk: noop }),
    ],
  }),
  "status-dont": () => ({
    title: tr("Sync"),
    body: [Text(tr("✅✅ SYNCED ✅✅")), Text(BORDER), Text(tr("| [!!] 2 FAILED !!! |")), Text(BORDER), Item(tr(">> Send again <<"), { id: "retry", onOk: noop })],
  }),
  // The script presses OK: the system's trusted confirmation comes up.
  "pay-do": () => ({
    title: tr("Pay"),
    body: [
      Text(tr("Morning Coffee"), { style: "title" }),
      Text("¥18.00", { style: "big" }),
      Button(tr("Pay"), () => void call("auth.confirm", { key: "pay", prompt: PAY(), payload: "order:1001" }).catch(noop), { id: "pay" }),
    ],
  }),
  "pay-dont": () => ({
    title: tr("System payment"),
    columns: 3,
    body: [
      Text(PAY()),
      Text(tr("Payment password: ○○○○○○")),
      ["1", "2", "3", "4", "5", "6", "7", "8", "9"].map((k) => Tile(k, { id: `k${k}`, onOk: noop })),
    ],
  }),
};

const pages = new Map<string, Page<void>>(Object.entries(EXAMPLES).map(([name, view]) => [name, page(name, view)]));

function open(name: string): void {
  const p = pages.get(name.trim());
  if (p) nav.push(p, undefined);
}

const Main = page("main", () => ({
  title: tr("Guideline examples"),
  focus: "go",
  body: [
    Input("go", { placeholder: tr("Page name"), clear: true, onSubmit: open }),
    List([...pages.keys()], (name) => Item(name, { id: name, onOk: act("open", tr("Open"), () => open(name)) })),
  ],
}));

app(Main);
