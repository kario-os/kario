// A TypeScript app with a native module (22-developer-api.md §9.3): the
// interface in KarioUI, the box blur in C (native:imgproc), with the same
// blur in TypeScript as the fallback and for the comparison. "Compare"
// measures both on this phone and writes the numbers to the app's log.
import { Button, Text, act, app, page, refresh, state, tr } from "@kario/ui";
import * as ts from "./blur";
import { compare, pick, type Engine } from "./engine";

const W = ts.WIDTH;
const H = ts.HEIGHT;
const R = ts.RADIUS;

let engine: Engine | undefined;
const result = state("");
const detail = state("");

const ms = (x: number) => (x < 10 ? x.toFixed(2) : x.toFixed(0));

// unavailable says why a native action does nothing here.
function unavailable(): boolean {
  if (engine?.native) return false;
  result.set(engine ? tr("TypeScript only: %s", engine.why ?? "") : tr("Loading the native module…"));
  detail.set("");
  return true;
}

function blurOnce(how: "native" | "typescript"): void {
  if (how === "native" && unavailable()) return;
  const px = ts.testImage();
  const t0 = Date.now();
  if (how === "native") engine!.native!.blur(px, W, H, R);
  else ts.blur(px, W, H, R);
  const took = Date.now() - t0;
  const name = how === "native" ? tr("native code") : tr("TypeScript");
  result.set(tr("Blurred %d×%d (radius %d) with %s in %s ms", W, H, R, name, ms(took)));
  detail.set(tr("Checksum %s", ts.checksum(px)));
}

async function blurOnWorker(): Promise<void> {
  if (unavailable()) return;
  const native = engine!.native!;
  result.set(tr("Blurring on a worker thread…"));
  detail.set("");
  const t0 = Date.now();
  const out = new Uint8Array(await native.blurAsync(ts.testImage(), W, H, R));
  result.set(tr("Blurred on a worker thread in %s ms", ms(Date.now() - t0)));
  detail.set(tr("Checksum %s", ts.checksum(out)));
}

function runCompare(): void {
  if (unavailable()) return;
  const c = compare(engine!.native!);
  const times = c.typescript / Math.max(c.native, 0.001);
  console.log(
    `imgproc bench ${W}x${H} r${R}: native ${ms(c.native)} ms (${c.runs[0]} runs), typescript ${ms(c.typescript)} ms (${c.runs[1]} runs), ${times.toFixed(0)}x, same ${c.same}`,
  );
  result.set(tr("Native %s ms, TypeScript %s ms: %s× faster", ms(c.native), ms(c.typescript), times.toFixed(0)));
  detail.set(c.same ? tr("Same pixels both ways") : tr("The two results differ"));
}

// The same buttons with or without the module, so the focus does not move
// when the import settles after the first page.
const Main = page("main", () => {
  const native = engine?.native;
  return {
    title: tr("Image filters"),
    body: [
      Text(
        !engine ? tr("Loading the native module…") : native ? tr("Native code: %s", native.version) : tr("TypeScript only: %s", engine.why ?? ""),
        { style: "dim" },
      ),
      Button(tr("Blur with native code"), act("native", tr("Blur with native code"), () => blurOnce("native"))),
      Button(tr("Blur in TypeScript"), act("typescript", tr("Blur in TypeScript"), () => blurOnce("typescript"))),
      Button(tr("Blur on a worker thread"), act("worker", tr("Blur on a worker thread"), blurOnWorker)),
      Button(tr("Compare"), act("compare", tr("Compare"), runCompare)),
      result.value && Text(result.value, { style: "accent" }),
      detail.value && Text(detail.value),
    ],
  };
});

app({
  root: Main,
  // Not a top-level await: the page has to be there when the system starts
  // the app; it says which engine it uses once the import settled.
  onStart() {
    void pick(() => import("native:imgproc")).then((e) => {
      engine = e;
      refresh();
    });
  },
});
