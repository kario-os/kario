// The app, written with KarioUI (@kario/ui): a page is a function of the
// app's state; the system draws it, and it is drawn again when the state
// changes. Types are in types/; kario test runs test/*.test.ts.
import { Button, Text, act, app, page, state, tr } from "@kario/ui";

const count = state(0, { store: "count" });

const Main = page("main", () => ({
  title: tr("Counter"),
  body: [
    Text(tr("Tapped %d times", count.value), { style: "big" }),
    Button(tr("Tap me"), act("tap", tr("Tap me"), () => count.update((n) => n + 1))),
  ],
  right: act("reset", tr("Reset"), () => count.set(0)),
}));

app(Main);
