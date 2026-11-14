// The app under a fake Shell (kario test): keys as the user presses them,
// the page as the app sends it.
import { launch, test } from "@kario/test";

test("OK counts, the right soft key starts again", async () => {
  const app = await launch();
  app.expect("Tapped 0 times");
  await app.key("enter");
  await app.key("enter");
  app.expect("Tapped 2 times");
  await app.key("f2");
  app.expect("Tapped 0 times");
});

test("the count outlives the process", async () => {
  const app = await launch();
  await app.press("Tap me");
  const again = await app.relaunch();
  again.expect("Tapped 1 times");
});
