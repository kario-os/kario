// The calculator's arithmetic and keys: + − × ÷, parentheses, decimals; the
// 9-key pad's * steps through the operators in place (the Nokia way).
import { tr } from "kario";

export const OPERATORS = ["+", "−", "×", "÷"];

export interface Calc {
  expr: string;
  result: string;
  err: boolean;
}

export const empty: Calc = { expr: "", result: "", err: false };

function lastOperator(expr: string): string | undefined {
  return OPERATORS.find((op) => expr.endsWith(op));
}

/** press is the calculator after key k. */
export function press(c: Calc, k: string): Calc {
  let { expr, result } = c;
  if (c.err) {
    result = "";
  } else if (result !== "") {
    // Continue from the result with an operator, start over with a digit.
    if ([...OPERATORS, "*"].includes(k)) expr = result;
    else if (k !== "=") expr = "";
    result = "";
  }
  switch (k) {
    case "C":
      return empty;
    case "←":
      return { expr: [...expr].slice(0, -1).join(""), result, err: false };
    case "=":
      try {
        return { expr, result: format(evaluate(expr)), err: false };
      } catch (e) {
        return { expr, result: tr("Error: %s", (e as Error).message), err: true };
      }
    case "*": {
      const op = lastOperator(expr);
      if (op) expr = expr.slice(0, -op.length) + OPERATORS[(OPERATORS.indexOf(op) + 1) % OPERATORS.length];
      else if (expr !== "") expr += OPERATORS[0];
      return { expr, result, err: false };
    }
    case "+":
    case "−":
    case "×":
    case "÷": {
      const op = lastOperator(expr);
      if (op) expr = expr.slice(0, -op.length);
      if (expr !== "" || k === "−") expr += k;
      return { expr, result, err: false };
    }
  }
  return { expr: expr + k, result, err: false };
}

/**
 * format is v with up to 10 decimals, as Go's strconv.FormatFloat(v, 'f',
 * 10, 64) without trailing zeros: the exact binary value rounded half to
 * even, so a number shows as the Go calculator showed it.
 */
export function format(v: number): string {
  if (!Number.isFinite(v)) return Number.isNaN(v) ? "NaN" : v > 0 ? "+Inf" : "-Inf";
  const bits = new DataView(new ArrayBuffer(8));
  bits.setFloat64(0, v);
  const hi = bits.getUint32(0);
  const biased = (hi >>> 20) & 0x7ff;
  let mant = (BigInt(hi & 0xfffff) << 32n) | BigInt(bits.getUint32(4));
  let exp = -1074;
  if (biased !== 0) {
    mant |= 1n << 52n;
    exp = biased - 1075;
  }
  let num = mant * 10n ** 10n;
  let den = 1n;
  if (exp >= 0) num <<= BigInt(exp);
  else den <<= BigInt(-exp);
  let q = num / den;
  const twice = (num % den) * 2n;
  if (twice > den || (twice === den && (q & 1n) === 1n)) q++;
  const digits = q.toString().padStart(11, "0");
  let s = `${hi >>> 31 ? "-" : ""}${digits.slice(0, -10)}.${digits.slice(-10)}`;
  s = s.replace(/0+$/, "").replace(/\.$/, "");
  return s === "-0" ? "0" : s;
}

/** evaluate is a small recursive-descent parser: + − × ÷, parentheses, decimals. */
export function evaluate(expr: string): number {
  const s = [...expr];
  let i = 0;
  const peek = () => s[i] ?? "";

  function sum(): number {
    let v = product();
    while (peek() === "+" || peek() === "−") {
      const op = s[i++];
      const r = product();
      v = op === "+" ? v + r : v - r;
    }
    return v;
  }

  function product(): number {
    let v = unary();
    while (peek() === "×" || peek() === "÷") {
      const op = s[i++];
      const r = unary();
      if (op === "×") v *= r;
      else if (r === 0) throw new Error(tr("Division by zero"));
      else v /= r;
    }
    return v;
  }

  function unary(): number {
    if (peek() === "−") {
      i++;
      return -unary();
    }
    if (peek() === "(") {
      i++;
      const v = sum();
      if (peek() !== ")") throw new Error(tr("Missing closing parenthesis"));
      i++;
      return v;
    }
    const start = i;
    while (/[0-9.]/.test(peek())) i++;
    if (start === i) throw new Error(tr("Incomplete expression"));
    const text = s.slice(start, i).join("");
    if (!/^(\d+\.?\d*|\.\d+)$/.test(text)) throw new Error(tr("Not a number: %s", text));
    const v = Number(text);
    if (!Number.isFinite(v)) throw new Error(tr("Number out of range: %s", text));
    return v;
  }

  const v = sum();
  if (i !== s.length) throw new Error(tr("Incomplete expression"));
  return v;
}
