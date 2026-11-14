// The calculator under a fake Shell (kario test): the selftest's keys, the
// grid, errors, numbers as the Go calculator printed them, a tombstone.
import { expectEqual, launch, test } from "@kario/test";
import { evaluate, format } from "../src/calc";

test("the selftest's case: * cycles the operators, OK on = calculates", async () => {
  const app = await launch();
  app.at("calc");
  app.expect("decimal point");
  expectEqual(app.focus, "k:=", "focus");
  await app.type("12");
  await app.type("*");
  await app.type("30");
  app.expect("12+30");
  await app.key("enter");
  app.expect("= 42");
  await app.type("6");
  await app.type("**");
  await app.type("2");
  await app.type("=");
  app.expect("6−2");
  app.expect("= 4");
});

test("the key grid: arrows and OK, backspace and clear", async () => {
  const app = await launch();
  app.focusOn("k:7");
  await app.key("enter");
  await app.key("right");
  await app.key("enter");
  expectEqual(app.focus, "k:8", "focus");
  await app.key("down");
  expectEqual(app.focus, "k:5", "focus a row down");
  await app.press("k:+");
  await app.press("k:9");
  app.expect("78+9");
  await app.press("k:←");
  app.expect("78+");
  await app.press("k:=");
  app.expect("Error: Incomplete expression");
  await app.type("5=");
  app.expect("= 83");
  await app.type("+1=");
  app.expect("= 84");
  await app.type("C");
  app.expect("0");
  expectEqual(app.menu(), ["=", "Clear"], "the options menu: the focused key, then the page's");
});

test("errors say what is wrong, and the expression stays to be fixed", async () => {
  const app = await launch();
  await app.type("1/0=");
  app.expect("Error: Division by zero");
  await app.key("left");
  await app.press("k:←");
  await app.type("5=");
  app.expect("= 0.2");
  await app.type("c(1+2=");
  app.expect("Error: Missing closing parenthesis");
  await app.type(")=");
  app.expect("= 3");
  await app.type("c1..2=");
  app.expect("Error: Not a number: 1..2");
  await app.type("c-(2+3)x4#5=");
  app.expect("= -22.5");
});

test("numbers read as the Go calculator printed them", () => {
  expectEqual(format(0.1 + 0.2), "0.3");
  expectEqual(format(1 / 3), "0.3333333333");
  expectEqual(format(2 / 3), "0.6666666667");
  expectEqual(format(1 / 2048), "0.0004882812", "a tie rounds to even");
  expectEqual(format(-0), "0");
  expectEqual(format(1e21), "1000000000000000000000");
  expectEqual(format(-1.5), "-1.5");
  expectEqual(format(1e308 * 10), "+Inf");
  expectEqual(evaluate("2+3×4"), 14);
  expectEqual(evaluate("−(1+1)÷4"), -0.5);
});

test("the expression comes back after the system ended the app (a tombstone)", async () => {
  const app = await launch();
  await app.type("7+8");
  await app.background();
  app.kill();
  const again = await app.relaunch();
  again.at("calc");
  again.expect("7+8");
  await again.type("=");
  again.expect("= 15");
});

test("a finished calculation starts afresh after a tombstone", async () => {
  const app = await launch();
  await app.type("6*2=");
  app.expect("= 8");
  await app.background();
  app.kill();
  const again = await app.relaunch();
  again.expect("decimal point");
  again.expectGone("= 8");
});

test("a new Shell taking over gets the page again", async () => {
  const app = await launch();
  await app.type("77");
  await app.resync();
  app.expect("77");
});

test("Spanish", async () => {
  const app = await launch({ lang: "es" });
  app.expect("Calculadora");
  expectEqual(app.menu(), ["=", "Borrar"], "the options menu");
  await app.type("1/0=");
  app.expect("Error: División entre cero");
});
