// Calculator, with KarioUI: a 4-column key grid. Every key also answers its
// own character, and the 9-key pad works the Nokia way: * steps through
// + − × ÷, # is the decimal point, OK on "=" evaluates. An expression being
// typed is kept for a tombstone; a finished calculation starts afresh.
import { Text, Tile, act, app, page, state, tr } from "@kario/ui";
import { empty, press, type Calc } from "./calc";

const KEYS = ["7", "8", "9", "÷", "4", "5", "6", "×", "1", "2", "3", "−", "C", "0", ".", "+", "(", ")", "←", "="];
const TYPED = ["0", "1", "2", "3", "4", "5", "6", "7", "8", "9", ".", "+", "-", "x", "/", "(", ")", "=", "c", "*", "#"];
const AS: Record<string, string> = { "-": "−", x: "×", "/": "÷", "#": ".", c: "C" };

const calc = state<Calc>(empty);
const key = (k: string) => calc.set(press(calc.value, k));

function status() {
  const c = calc.value;
  if (c.err) return Text(c.result, { style: "warn" });
  if (c.result) return Text("= " + c.result, { style: "accent" });
  return Text(tr("* cycles + − × ÷   # decimal point   OK calculates"), { style: "dim" });
}

const Main = page("calc", () => ({
  title: tr("Calculator"),
  columns: 4,
  layout: "bottom",
  focus: "k:=",
  body: [
    Text(calc.value.expr || "0", { style: "display" }),
    status(),
    KEYS.map((k) => Tile(k, { id: "k:" + k, onOk: act("press", k, () => key(k)) })),
  ],
  actions: [
    // Typed characters press keys too; hidden so the options menu stays short.
    ...TYPED.map((k) => act("key:" + k, k, () => key(AS[k] ?? k), { key: k, hidden: true })),
    act("clear", tr("Clear"), () => key("C"), { key: "C" }),
  ],
}));

app({
  root: Main,
  save: () => (calc.value.result === "" ? calc.value.expr : undefined),
  onStart: (_arg, restored) => {
    if (typeof restored === "string") calc.set({ ...empty, expr: restored });
  },
});
