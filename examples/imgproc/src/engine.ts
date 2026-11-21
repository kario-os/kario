// Which implementation the app uses: native:imgproc when it loads (the
// phone, kario run, kario test), else the TypeScript one, with the reason.
import * as ts from "./blur";

export type Native = typeof import("native:imgproc");

export interface Engine {
  native?: Native;
  /** Why there is no native module. */
  why?: string;
}

/** pick loads the native module, or falls back to TypeScript. */
export async function pick(load: () => Promise<Native>): Promise<Engine> {
  try {
    return { native: await load() };
  } catch (err) {
    return { why: err instanceof Error ? err.message : String(err) };
  }
}

export interface Comparison {
  native: number;
  typescript: number;
  runs: [number, number];
  same: boolean;
}

/** timed runs f on fresh copies of img, at least runs times and for at least ms: ms a run. */
function timed(img: Uint8Array, f: (px: Uint8Array) => void, runs: number, ms: number): { each: number; runs: number; out: Uint8Array } {
  let out = img;
  let n = 0;
  const t0 = Date.now();
  while (n < runs || Date.now() - t0 < ms) {
    out = img.slice();
    f(out);
    n++;
    if (n >= 1000) break;
  }
  return { each: (Date.now() - t0) / n, runs: n, out };
}

/** compare blurs the test image both ways and says how long each took and whether they agree. */
export function compare(native: Native, w = ts.WIDTH, h = ts.HEIGHT, r = ts.RADIUS): Comparison {
  const img = ts.testImage(w, h);
  const a = timed(img, (px) => native.blur(px, w, h, r), 5, 200);
  const b = timed(img, (px) => ts.blur(px, w, h, r), 2, 0);
  return { native: a.each, typescript: b.each, runs: [a.runs, b.runs], same: native.checksum(a.out) === ts.checksum(b.out) };
}
