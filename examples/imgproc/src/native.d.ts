// The types of the app's native module (native/imgproc.c), written by hand:
// the runtime knows a module's names, not its types.
declare module "native:imgproc" {
  /** A box blur of w×h RGBA pixels, in place. */
  export function blur(pixels: Uint8Array, width: number, height: number, radius: number): void;
  /** The same on a worker thread: a blurred copy. */
  export function blurAsync(pixels: Uint8Array, width: number, height: number, radius: number): Promise<ArrayBuffer>;
  /** FNV-1a over the bytes, as 8 hex digits. */
  export function checksum(bytes: Uint8Array): string;
  export const version: string;
  /** The module's events (none here). */
  export function on(event: string, handler?: (value: unknown) => void): void;
}
