// The TypeScript fallback of native:imgproc (native/imgproc.c): the same box
// blur and checksum, pixel for pixel, for a phone where the module cannot
// load and to measure what the native code buys.

export const WIDTH = 320;
export const HEIGHT = 240;
export const RADIUS = 4;

/** testImage is a w×h RGBA pattern, the same on every run. */
export function testImage(w = WIDTH, h = HEIGHT): Uint8Array {
  const px = new Uint8Array(w * h * 4);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const i = (y * w + x) * 4;
      px[i] = (x * 7 + y * 3) & 255;
      px[i + 1] = (x ^ y) & 255;
      px[i + 2] = (x * y) & 255;
      px[i + 3] = 255;
    }
  }
  return px;
}

const clamp = (v: number, lo: number, hi: number) => (v < lo ? lo : v > hi ? hi : v);

function pass(src: Uint8Array, dst: Uint8Array, count: number, len: number, step: number, stride: number, r: number): void {
  const n = 2 * r + 1;
  for (let l = 0; l < count; l++) {
    const base = l * stride;
    for (let c = 0; c < 4; c++) {
      let sum = 0;
      for (let k = -r; k <= r; k++) sum += src[base + clamp(k, 0, len - 1) * step + c];
      for (let i = 0; i < len; i++) {
        dst[base + i * step + c] = Math.floor((sum * 2 + n) / (2 * n));
        sum += src[base + clamp(i + r + 1, 0, len - 1) * step + c];
        sum -= src[base + clamp(i - r, 0, len - 1) * step + c];
      }
    }
  }
}

/** blur blurs w×h RGBA pixels in place: rows, then columns. */
export function blur(px: Uint8Array, w: number, h: number, r: number): void {
  const tmp = new Uint8Array(w * h * 4);
  pass(px, tmp, h, w, 4, w * 4, r);
  pass(tmp, px, w, h, w * 4, 4, r);
}

/** checksum is FNV-1a over the bytes, as 8 hex digits. */
export function checksum(px: Uint8Array): string {
  let x = 2166136261;
  for (let i = 0; i < px.length; i++) x = Math.imul(x ^ px[i], 16777619) >>> 0;
  return x.toString(16).padStart(8, "0");
}
