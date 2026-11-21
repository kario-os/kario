// What debug_test.go drives: breakpoints, steps, frames, variables,
// debugger statements and exceptions. Lines are found by their markers.
import * as kario from "kario";

const greeting = "hi";
let presses = 0;

function total(a, b) {
  const sum = a + b; // @sum
  return sum * 2; // @return
}

function press(e) {
  presses++; // @inc
  const t = total(presses, 10); // @call
  const info = { t, list: [1, 2, 3], greeting }; // @info
  kario.update(screen(info.t)); // @update
}

const screen = (n) => ({ id: "main", title: greeting, body: [{ kind: "text", text: "n " + n }] }); // @screen

// Its first statement reads no variable and calls nothing: it has a
// position only in code compiled for a debugger.
function assign(v) {
  let copy = 0; // @assign
  copy += v;
  presses = copy;
  kario.update(screen(copy));
}

kario.on("start", () => kario.reset(screen(0)));

kario.on("action", (e) => {
  if (e.action === "press") press(e); // @dispatch
  if (e.action === "stop") {
    debugger; // @debugger
  }
  if (e.action === "throw") throw new Error("boom " + presses); // @throw
  if (e.action === "caught") {
    try {
      JSON.parse("{");
    } catch (err) {
      kario.update(screen("caught"));
    }
  }
  if (e.action === "log") console.log("presses", presses);
  if (e.action === "assign") assign(Number(e.value));
});
