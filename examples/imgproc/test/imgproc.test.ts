// The image filter app under a fake Shell (kario test): the TypeScript blur
// on its own, the fallback when the module does not load, and, with the
// native module built for this machine, native and TypeScript agreeing.
import { expectEqual, launch, test } from "@kario/test";
import { blur, checksum, testImage } from "../src/blur";
import { pick } from "../src/engine";

test("the TypeScript blur: edges clamped, rounded half up", () => {
  // One row of three pixels, radius 1: (a+a+b)/3, (a+b+c)/3, (b+c+c)/3.
  const px = new Uint8Array([0, 10, 255, 255, 3, 20, 255, 255, 9, 40, 0, 255]);
  blur(px, 3, 1, 1);
  expectEqual([...px], [1, 13, 255, 255, 4, 23, 170, 255, 7, 33, 85, 255], "pixels");
  expectEqual(checksum(new Uint8Array([97])), "e40c292c", "FNV-1a of 'a'");
  expectEqual(testImage(4, 2).length, 32, "the test image's size");
});

test("without its native module the app falls back to TypeScript, saying why", async () => {
  const e = await pick(() => Promise.reject(new Error("native:imgproc: no lib/arm64/imgproc.so in the app's package")));
  expectEqual(e.native, undefined, "native");
  expectEqual(e.why, "native:imgproc: no lib/arm64/imgproc.so in the app's package", "why");
});

test("native code and TypeScript blur to the same pixels", async () => {
  const app = await launch();
  await app.wait("Native code: imgproc 1.0");
  await app.press("native");
  app.expect("with native code in");
  const native = app.text().match(/Checksum (\w+)/)?.[1];
  await app.press("typescript");
  app.expect("with TypeScript in");
  expectEqual(app.text().match(/Checksum (\w+)/)?.[1], native, "the checksums");
  await app.press("compare");
  app.expect("× faster");
  app.expect("Same pixels both ways");
});

test("an asynchronous call runs on a worker thread and gives the same pixels", async () => {
  const app = await launch();
  await app.wait("Native code: imgproc 1.0");
  await app.press("typescript");
  const want = app.text().match(/Checksum (\w+)/)?.[1];
  await app.press("worker");
  await app.wait("Blurred on a worker thread in");
  expectEqual(app.text().match(/Checksum (\w+)/)?.[1], want, "the checksums");
});
