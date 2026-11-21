// The Chinese calendar, computed: new moons from Meeus' series
// (Astronomical Algorithms, 2nd ed., ch. 49) and the solar terms from the
// Sun's apparent longitude (ch. 25, with the nutation in longitude of ch.
// 22), on Beijing time (UTC+8, GB/T 33661-2017). A month begins on the
// day of a new moon; month 11 holds the winter solstice; a year of 13
// months makes its first month without a major term (zhongqi) the leap
// month. No tables of dates: the series need a few hundred bytes, and the
// rules are the standard's. The truncated series are good to a minute or
// so; a term or a new moon that close to midnight could land on the wrong
// day, so the results were checked against an ephemeris for 1901-2099.

const RAD = Math.PI / 180;
const sin = (deg: number) => Math.sin(deg * RAD);
const cos = (deg: number) => Math.cos(deg * RAD);
const norm = (deg: number) => ((deg % 360) + 360) % 360;

/** Days from 1970-01-01 of a Gregorian date (proleptic). */
export function dayNumber(y: number, m: number, d: number): number {
  const a = Math.floor((14 - m) / 12);
  const yy = y + 4800 - a;
  const mm = m + 12 * a - 3;
  const jdn = d + Math.floor((153 * mm + 2) / 5) + 365 * yy + Math.floor(yy / 4) - Math.floor(yy / 100) + Math.floor(yy / 400) - 32045;
  return jdn - 2440588;
}

/** The Gregorian date of a day number. */
export function fromDayNumber(n: number): [number, number, number] {
  const a = n + 2440588 + 32044;
  const b = Math.floor((4 * a + 3) / 146097);
  const c = a - Math.floor((146097 * b) / 4);
  const d = Math.floor((4 * c + 3) / 1461);
  const e = c - Math.floor((1461 * d) / 4);
  const m = Math.floor((5 * e + 2) / 153);
  return [100 * b + d - 4800 + Math.floor(m / 10), m + 3 - 12 * Math.floor(m / 10), e - Math.floor((153 * m + 2) / 5) + 1];
}

// ΔT = TT − UT in seconds (Espenak and Meeus' polynomials, 1900-2150).
function deltaT(year: number): number {
  let t: number;
  if (year < 1920) {
    t = year - 1900;
    return -2.79 + 1.494119 * t - 0.0598939 * t * t + 0.0061966 * t ** 3 - 0.000197 * t ** 4;
  }
  if (year < 1941) {
    t = year - 1920;
    return 21.2 + 0.84493 * t - 0.0761 * t * t + 0.0020936 * t ** 3;
  }
  if (year < 1961) {
    t = year - 1950;
    return 29.07 + 0.407 * t - (t * t) / 233 + t ** 3 / 2547;
  }
  if (year < 1986) {
    t = year - 1975;
    return 45.45 + 1.067 * t - (t * t) / 260 - t ** 3 / 718;
  }
  if (year < 2005) {
    t = year - 2000;
    return 63.86 + 0.3345 * t - 0.060374 * t * t + 0.0017275 * t ** 3 + 0.000651814 * t ** 4 + 0.00002373599 * t ** 5;
  }
  if (year < 2050) {
    t = year - 2000;
    return 62.92 + 0.32217 * t + 0.005589 * t * t;
  }
  return -20 + 32 * ((year - 1820) / 100) ** 2 - 0.5628 * (2150 - year);
}

// Beijing's day number of an instant given as a Julian Ephemeris Day.
function beijingDay(jde: number): number {
  const year = 2000 + (jde - 2451545) / 365.25;
  const jdUT = jde - deltaT(year) / 86400;
  return Math.floor(jdUT + 0.5 + 8 / 24 - 2440588);
}

/** The new moon k lunations after that of 2000-01-06, as a JDE (Meeus 49.1, with table 49.A). */
export function newMoon(k: number): number {
  const T = k / 1236.85;
  let jde = 2451550.09766 + 29.530588861 * k + 0.00015437 * T * T - 0.00000015 * T ** 3 + 0.00000000073 * T ** 4;
  const E = 1 - 0.002516 * T - 0.0000074 * T * T;
  const M = 2.5534 + 29.1053567 * k - 0.0000014 * T * T - 0.00000011 * T ** 3;
  const Mp = 201.5643 + 385.81693528 * k + 0.0107582 * T * T + 0.00001238 * T ** 3 - 0.000000058 * T ** 4;
  const F = 160.7108 + 390.67050284 * k - 0.0016118 * T * T - 0.00000227 * T ** 3 + 0.000000011 * T ** 4;
  const O = 124.7746 - 1.56375588 * k + 0.0020672 * T * T + 0.00000215 * T ** 3;
  jde +=
    -0.4072 * sin(Mp) +
    0.17241 * E * sin(M) +
    0.01608 * sin(2 * Mp) +
    0.01039 * sin(2 * F) +
    0.00739 * E * sin(Mp - M) -
    0.00514 * E * sin(Mp + M) +
    0.00208 * E * E * sin(2 * M) -
    0.00111 * sin(Mp - 2 * F) -
    0.00057 * sin(Mp + 2 * F) +
    0.00056 * E * sin(2 * Mp + M) -
    0.00042 * sin(3 * Mp) +
    0.00042 * E * sin(M + 2 * F) +
    0.00038 * E * sin(M - 2 * F) -
    0.00024 * E * sin(2 * Mp - M) -
    0.00017 * sin(O) -
    0.00007 * sin(Mp + 2 * M) +
    0.00004 * sin(2 * Mp - 2 * F) +
    0.00004 * sin(3 * M) +
    0.00003 * sin(Mp + M - 2 * F) +
    0.00003 * sin(2 * Mp + 2 * F) -
    0.00003 * sin(Mp + M + 2 * F) +
    0.00003 * sin(Mp - M + 2 * F) -
    0.00002 * sin(Mp - M - 2 * F) -
    0.00002 * sin(3 * Mp + M) +
    0.00002 * sin(4 * Mp);
  const A = [
    [299.77 + 0.107408 * k - 0.009173 * T * T, 0.000325],
    [251.88 + 0.016321 * k, 0.000165],
    [251.83 + 26.651886 * k, 0.000164],
    [349.42 + 36.412478 * k, 0.000126],
    [84.66 + 18.206239 * k, 0.00011],
    [141.74 + 53.303771 * k, 0.000062],
    [207.14 + 2.453732 * k, 0.00006],
    [154.84 + 7.30686 * k, 0.000056],
    [34.52 + 27.261239 * k, 0.000047],
    [207.19 + 0.121824 * k, 0.000042],
    [291.34 + 1.844379 * k, 0.00004],
    [161.72 + 24.198154 * k, 0.000037],
    [239.56 + 25.513099 * k, 0.000035],
    [331.55 + 3.592518 * k, 0.000023],
  ];
  for (const [arg, c] of A) jde += c * sin(arg);
  return jde;
}

/** The Sun's apparent ecliptic longitude in degrees at a JDE. */
export function solarLongitude(jde: number): number {
  const T = (jde - 2451545) / 36525;
  const L0 = 280.46646 + 36000.76983 * T + 0.0003032 * T * T;
  const M = 357.52911 + 35999.05029 * T - 0.0001537 * T * T;
  const C = (1.914602 - 0.004817 * T - 0.000014 * T * T) * sin(M) + (0.019993 - 0.000101 * T) * sin(2 * M) + 0.000289 * sin(3 * M);
  const O = 125.04452 - 1934.136261 * T;
  const Ls = 280.4665 + 36000.7698 * T;
  const Lm = 218.3165 + 481267.8813 * T;
  const nutation = (-17.2 * sin(O) - 1.32 * sin(2 * Ls) - 0.23 * sin(2 * Lm) + 0.21 * sin(2 * O)) / 3600;
  const aberration = -20.4898 / 3600 / (1.000001018 * (1 - 0.016708634 ** 2) / (1 + 0.016708634 * cos(M + C)));
  return norm(L0 + C + nutation + aberration + perturbations(T));
}

// The main perturbations of the Sun's longitude the series above leaves
// out, in arc seconds: the Moon's pull on the Earth (the elongation D) and
// the planets' (mean longitudes of Venus, the Earth, Mars, Jupiter,
// Saturn), with the offset of the low-precision theory. They take the
// error of a solar term's time from about 12 minutes to about 4. The
// amplitudes were fitted by least squares to the solar-term times of
// sxtwl_cpp (BSD-3-Clause), the C++ port of the Shouxing almanac, 1901-2099.
function perturbations(T: number): number {
  const D = 297.8502 + 445267.1115 * T;
  const V = 181.9798 + 58517.8157 * T;
  const E = 100.4664 + 35999.373 * T;
  const Ma = 355.433 + 19140.2993 * T;
  const J = 34.3515 + 3034.9057 * T;
  const S = 50.0774 + 1222.1138 * T;
  return (
    (-6.971 +
      6.469 * sin(D) +
      4.816 * sin(V - E) -
      5.556 * sin(2 * (V - E)) -
      0.664 * sin(3 * (V - E)) -
      7.153 * sin(E - J) +
      2.738 * sin(2 * (E - J)) -
      0.277 * sin(E - Ma) -
      2.058 * sin(2 * (E - Ma)) -
      0.393 * sin(E - S) -
      0.872 * sin(3 * V - 5 * E) +
      2.543 * cos(2 * V - 3 * E) -
      2.604 * sin(J) +
      0.459 * cos(J)) /
    3600
  );
}

/** The JDE at which the Sun's apparent longitude is deg, the first after jde0 - 5 days. */
export function solarTermAt(deg: number, jde0: number): number {
  let jde = jde0;
  for (let i = 0; i < 50; i++) {
    let diff = deg - solarLongitude(jde);
    diff = ((diff + 540) % 360) - 180;
    if (Math.abs(diff) < 1e-7) break;
    jde += diff * (365.2422 / 360);
  }
  return jde;
}

/** The Beijing day of the solar term at longitude deg in the given year (0 is the spring equinox). */
export function solarTermDay(year: number, deg: number): number {
  const approx = 2451545 + ((year - 2000) * 365.2422) + ((norm(deg - 280) / 360) * 365.2422) - 1.5;
  return beijingDay(solarTermAt(deg, approx));
}

/**
 * The 24 terms in the order of a Gregorian year, from Minor Cold (285°)
 * to the Winter Solstice (270°); the even ones are the minor terms (jieqi).
 */
export const TERM_DEGREES = [285, 300, 315, 330, 345, 0, 15, 30, 45, 60, 75, 90, 105, 120, 135, 150, 165, 180, 195, 210, 225, 240, 255, 270];

const termCache = new Map<number, number[]>();

/** The day numbers of a Gregorian year's 24 terms, in TERM_DEGREES' order. */
export function yearTerms(year: number): number[] {
  let t = termCache.get(year);
  if (!t) {
    t = TERM_DEGREES.map((deg, i) => {
      const approx = 2451545 + (year - 2000) * 365.2422 + 5 + i * 15.2184 - 0.5;
      return beijingDay(solarTermAt(deg, approx));
    });
    termCache.set(year, t);
  }
  return t;
}

/** Which of the 24 terms (index into TERM_DEGREES) falls on day n, -1 for none. */
export function termOn(n: number): number {
  const [y] = fromDayNumber(n);
  return yearTerms(y).indexOf(n);
}

// The lunation index k of the new moon on or before Beijing day n.
function moonBefore(n: number): number {
  let k = Math.floor((n - dayNumber(2000, 1, 6)) / 29.530588861);
  while (beijingDay(newMoon(k + 1)) <= n) k++;
  while (beijingDay(newMoon(k)) > n) k--;
  return k;
}

/** A lunar month of a Chinese year: its number (1-12), leap or not, the day it begins and its length. */
export interface LunarMonth {
  month: number;
  leap: boolean;
  start: number;
  days: number;
}

/** One Chinese year (the one beginning in the Gregorian year): its months from month 1 to the last of month 12. */
export interface LunarYear {
  year: number;
  months: LunarMonth[];
}

// The months from the one holding the winter solstice of year y - 1 (month
// 11) to the one before the next such month, numbered by the rules.
const suiCache = new Map<number, LunarMonth[]>();

function sui(y: number): LunarMonth[] {
  let s = suiCache.get(y);
  if (s) return s;
  const ws0 = yearTerms(y - 1)[23];
  const ws1 = yearTerms(y)[23];
  const k0 = moonBefore(ws0);
  const k1 = moonBefore(ws1);
  const starts: number[] = [];
  for (let k = k0; k <= k1; k++) starts.push(beijingDay(newMoon(k)));
  const count = starts.length - 1;
  const zhongqi = (a: number, b: number) => {
    for (const yy of [fromDayNumber(a)[0], fromDayNumber(b - 1)[0]]) {
      const t = yearTerms(yy);
      for (let i = 1; i < 24; i += 2) if (t[i] >= a && t[i] < b) return true;
    }
    return false;
  };
  s = [];
  let num = 11;
  let leapDone = count !== 13;
  for (let i = 0; i < count; i++) {
    const a = starts[i];
    const b = starts[i + 1];
    let leap = false;
    if (!leapDone && i > 0 && !zhongqi(a, b)) {
      leap = true;
      leapDone = true;
    }
    if (!leap && i > 0) num = (num % 12) + 1;
    s.push({ month: num, leap, start: a, days: b - a });
  }
  suiCache.set(y, s);
  return s;
}

/** The Chinese year that begins in Gregorian year y (its New Year is in January or February of y). */
export function lunarYear(y: number): LunarYear {
  const a = sui(y);
  const b = sui(y + 1);
  const all = [...a, ...b];
  const first = all.findIndex((m) => m.month === 1 && !m.leap);
  const months: LunarMonth[] = [];
  for (let i = first; i < all.length; i++) {
    if (i > first && all[i].month === 1 && !all[i].leap) break;
    months.push(all[i]);
  }
  return { year: y, months };
}

/** A day of the Chinese calendar. */
export interface LunarDate {
  /** The Gregorian year its Chinese year begins in. */
  year: number;
  month: number;
  leap: boolean;
  day: number;
  /** The month's length: 29 or 30. */
  monthDays: number;
}

/** The Chinese date of Gregorian day n (day number), 1901-2099. */
export function lunarDate(n: number): LunarDate {
  const [y] = fromDayNumber(n);
  for (const yy of [y, y - 1]) {
    const ly = lunarYear(yy);
    for (const m of ly.months) {
      if (n >= m.start && n < m.start + m.days) return { year: yy, month: m.month, leap: m.leap, day: n - m.start + 1, monthDays: m.days };
    }
  }
  throw new Error("no lunar date for day " + n);
}

/** The Chinese New Year (day number) of the Chinese year beginning in y. */
export function newYear(y: number): number {
  return lunarYear(y).months[0].start;
}

/** The leap month of the Chinese year beginning in y (0 for none). */
export function leapMonth(y: number): number {
  return lunarYear(y).months.find((m) => m.leap)?.month ?? 0;
}

/** The sexagenary cycle of a Chinese year: its stem (0-9) and branch (0-11, also the zodiac animal). */
export function cycle(year: number): [number, number] {
  const i = (((year - 4) % 60) + 60) % 60;
  return [i % 10, i % 12];
}
