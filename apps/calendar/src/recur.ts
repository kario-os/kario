// Recurrence (RFC 5545 §3.3.10): FREQ MINUTELY, HOURLY, DAILY, WEEKLY,
// MONTHLY or YEARLY with INTERVAL, COUNT or UNTIL, BYDAY (with an ordinal in
// a month or a year: 2MO, -1FR), BYMONTHDAY, BYMONTH, BYYEARDAY (a yearly,
// hourly or minutely rule's), BYWEEKNO (a yearly rule's), BYHOUR and
// BYMINUTE, BYSETPOS and WKST; BYSECOND only as 0, and no SECONDLY (times
// are minutes). Each BY part expands or limits a period's set as RFC
// 5545's table has it, BYSETPOS picks from the whole set of a period's
// starts. Occurrences are expanded on the event's own clock (wall times),
// so a weekly 09:00 stays at 09:00 over a daylight-saving change, and an
// hourly one keeps to the clock's hours; COUNT counts the occurrences
// before EXDATE takes any out, and RDATE's starts come on top of them.
import { addMonths, day, daysInMonth, type Day, type Wall, weekday, ymd } from "./dates";

export type Freq = "MINUTELY" | "HOURLY" | "DAILY" | "WEEKLY" | "MONTHLY" | "YEARLY";

const FREQS: Freq[] = ["MINUTELY", "HOURLY", "DAILY", "WEEKLY", "MONTHLY", "YEARLY"];

export interface ByDay {
  /** 0 Sunday to 6 Saturday. */
  wd: number;
  /** 0 every such day of the period; 1 the first, -1 the last, ... */
  n: number;
}

export interface Rule {
  freq: Freq;
  interval: number;
  count?: number;
  /** The last day an occurrence may start (inclusive), and its time when the rule gave one. */
  until?: Wall;
  byDay?: ByDay[];
  byMonthDay?: number[];
  byMonth?: number[];
  byYearDay?: number[];
  byWeekNo?: number[];
  byHour?: number[];
  byMinute?: number[];
  bySetPos?: number[];
  wkst: number;
}

/** The most occurrences a rule may count: what an import keeps. */
export const MAX_COUNT = 1000;

const WD = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];

function ints(s: string, lo: number, hi: number): number[] | undefined {
  const out: number[] = [];
  for (const p of s.split(",")) {
    if (!/^[+-]?\d{1,3}$/.test(p)) return undefined;
    const n = Number(p);
    if (n === 0 || Math.abs(n) < lo || Math.abs(n) > hi) return undefined;
    out.push(n);
  }
  return out.length ? out : undefined;
}

// A list of hours or minutes: 0 to hi, no sign.
function clock(s: string, hi: number): number[] | undefined {
  const out: number[] = [];
  for (const p of s.split(",")) {
    if (!/^\d{1,2}$/.test(p) || Number(p) > hi) return undefined;
    out.push(Number(p));
  }
  return out.length ? out : undefined;
}

/** The rule of an RRULE value, or undefined when it is not one this reads (the event then does not repeat). */
export function parseRule(value: string, until?: (s: string) => Wall | undefined): Rule | undefined {
  const parts = new Map<string, string>();
  for (const p of value.split(";")) {
    const i = p.indexOf("=");
    if (i <= 0) return undefined;
    parts.set(p.slice(0, i).toUpperCase(), p.slice(i + 1).toUpperCase());
  }
  const freq = FREQS.find((f) => f === parts.get("FREQ"));
  if (!freq) return undefined;
  const r: Rule = { freq, interval: 1, wkst: 1 };
  for (const [k, v] of parts) {
    switch (k) {
      case "FREQ":
        break;
      case "INTERVAL":
        if (!/^\d{1,4}$/.test(v) || Number(v) < 1) return undefined;
        r.interval = Number(v);
        break;
      case "COUNT":
        if (!/^\d{1,6}$/.test(v) || Number(v) < 1) return undefined;
        r.count = Math.min(Number(v), MAX_COUNT);
        break;
      case "UNTIL": {
        const u = until?.(v);
        if (!u) return undefined;
        r.until = u;
        break;
      }
      case "BYDAY": {
        const list: ByDay[] = [];
        for (const p of v.split(",")) {
          const m = /^([+-]?\d{1,2})?(SU|MO|TU|WE|TH|FR|SA)$/.exec(p);
          if (!m) return undefined;
          const n = m[1] ? Number(m[1]) : 0;
          if (Math.abs(n) > 53) return undefined;
          list.push({ wd: WD.indexOf(m[2]), n });
        }
        r.byDay = list;
        break;
      }
      case "BYMONTHDAY":
        if (!(r.byMonthDay = ints(v, 1, 31))) return undefined;
        break;
      case "BYMONTH":
        if (!(r.byMonth = ints(v, 1, 12)) || r.byMonth.some((n) => n < 0)) return undefined;
        break;
      case "BYYEARDAY":
        if (!(r.byYearDay = ints(v, 1, 366))) return undefined;
        break;
      case "BYWEEKNO":
        if (!(r.byWeekNo = ints(v, 1, 53))) return undefined;
        break;
      case "BYHOUR":
        if (!(r.byHour = clock(v, 23))) return undefined;
        break;
      case "BYMINUTE":
        if (!(r.byMinute = clock(v, 59))) return undefined;
        break;
      case "BYSECOND":
        if (!/^0{1,2}$/.test(v)) return undefined;
        break;
      case "BYSETPOS":
        if (!(r.bySetPos = ints(v, 1, 366))) return undefined;
        break;
      case "WKST":
        if (!WD.includes(v)) return undefined;
        r.wkst = WD.indexOf(v);
        break;
      default:
        return undefined;
    }
  }
  if (r.count && r.until) return undefined;
  // What RFC 5545 says MUST NOT be: BYWEEKNO outside a yearly rule,
  // BYYEARDAY in a daily, weekly or monthly one, BYMONTHDAY in a weekly
  // one, an ordinal BYDAY with BYWEEKNO.
  if (r.byWeekNo && r.freq !== "YEARLY") return undefined;
  if (r.byYearDay && (r.freq === "DAILY" || r.freq === "WEEKLY" || r.freq === "MONTHLY")) return undefined;
  if (r.byMonthDay && r.freq === "WEEKLY") return undefined;
  if (r.byWeekNo && r.byDay?.some((b) => b.n !== 0)) return undefined;
  return r;
}

/** The RRULE value of a rule (UNTIL written by the caller, who knows its zone). */
export function formatRule(r: Rule, until?: (w: Wall) => string): string {
  const p = [`FREQ=${r.freq}`];
  if (r.interval > 1) p.push(`INTERVAL=${r.interval}`);
  if (r.count) p.push(`COUNT=${r.count}`);
  if (r.until && until) p.push(`UNTIL=${until(r.until)}`);
  if (r.byDay?.length) p.push("BYDAY=" + r.byDay.map((b) => (b.n ? String(b.n) : "") + WD[b.wd]).join(","));
  if (r.byMonthDay?.length) p.push("BYMONTHDAY=" + r.byMonthDay.join(","));
  if (r.byMonth?.length) p.push("BYMONTH=" + r.byMonth.join(","));
  if (r.byYearDay?.length) p.push("BYYEARDAY=" + r.byYearDay.join(","));
  if (r.byWeekNo?.length) p.push("BYWEEKNO=" + r.byWeekNo.join(","));
  if (r.byHour?.length) p.push("BYHOUR=" + r.byHour.join(","));
  if (r.byMinute?.length) p.push("BYMINUTE=" + r.byMinute.join(","));
  if (r.bySetPos?.length) p.push("BYSETPOS=" + r.bySetPos.join(","));
  if (r.wkst !== 1) p.push("WKST=" + WD[r.wkst]);
  return p.join(";");
}

/** Whether the rule has parts the event editor has no choice for (it keeps them as they are). */
export function beyondEditor(r: Rule | undefined): boolean {
  return !!(r && (r.byHour || r.byMinute || r.byWeekNo || r.byYearDay || r.freq === "HOURLY" || r.freq === "MINUTELY"));
}

// Whether a day passes the BY parts that limit the days of an hourly or
// minutely rule (BYMONTH, BYYEARDAY, BYMONTHDAY, BYDAY's weekdays).
function dayAllowed(r: Rule, d: Day): boolean {
  const [y, m, dd] = ymd(d);
  if (r.byMonth && !r.byMonth.includes(m)) return false;
  if (r.byYearDay) {
    const len = day(y + 1, 1, 1) - day(y, 1, 1);
    const k = d - day(y, 1, 1) + 1;
    if (!r.byYearDay.some((n) => (n > 0 ? n : len + n + 1) === k)) return false;
  }
  if (r.byMonthDay && !monthDays(y, m, r.byMonthDay).includes(d)) return false;
  return !r.byDay || r.byDay.some((b) => b.wd === weekday(d));
}

// The minute (day * 1440 + minute) an hourly or minutely rule's period i
// starts at: the start's hour or minute, i * INTERVAL on.
function periodStart(r: Rule, start: Wall, i: number): number {
  const base = start.day * 1440 + (r.freq === "HOURLY" ? Math.floor(start.min / 60) * 60 : start.min);
  return base + i * r.interval * SPAN[r.freq];
}

// The n-th day of the weekday wd from first on, over len days (n < 0 counts from the end).
function nth(first: Day, len: number, wd: number, n: number): Day | undefined {
  const all: Day[] = [];
  for (let d = 0; d < len; d++) if (weekday(first + d) === wd) all.push(first + d);
  return n > 0 ? all[n - 1] : all[all.length + n];
}

// The days of a month BYDAY picks (n counts within the month).
function monthByDay(y: number, m: number, bd: ByDay[]): Day[] {
  const first = day(y, m, 1);
  const len = daysInMonth(y, m);
  const out: Day[] = [];
  for (const b of bd) {
    if (b.n === 0) {
      for (let d = 0; d < len; d++) if (weekday(first + d) === b.wd) out.push(first + d);
    } else {
      const d = nth(first, len, b.wd, b.n);
      if (d !== undefined) out.push(d);
    }
  }
  return out;
}

function monthDays(y: number, m: number, list: number[]): Day[] {
  const len = daysInMonth(y, m);
  const out: Day[] = [];
  for (const n of list) {
    const d = n > 0 ? n : len + n + 1;
    if (d >= 1 && d <= len) out.push(day(y, m, d));
  }
  return out;
}

// The candidate days of one month for a monthly rule.
function inMonth(r: Rule, y: number, m: number, startDay: number): Day[] {
  if (r.byMonthDay && r.byDay) {
    const allowed = new Set(r.byDay.map((b) => b.wd));
    return monthDays(y, m, r.byMonthDay).filter((d) => allowed.has(weekday(d)));
  }
  if (r.byMonthDay) return monthDays(y, m, r.byMonthDay);
  if (r.byDay) return monthByDay(y, m, r.byDay);
  return startDay <= daysInMonth(y, m) ? [day(y, m, startDay)] : [];
}

// The first day of week 1 of year y: weeks start on wkst, and week 1 is
// the first with at least four days in the year (the one with 4 January).
function week1(y: number, wkst: number): Day {
  const j4 = day(y, 1, 4);
  return j4 - ((weekday(j4) - wkst + 7) % 7);
}

/** A day's week number in its own week-numbering year, and how many weeks that year has. */
export function weekNo(d: Day, wkst: number): { n: number; weeks: number } {
  let y = ymd(d)[0];
  let w1 = week1(y, wkst);
  if (d < w1) w1 = week1(--y, wkst);
  else if (d >= week1(y + 1, wkst)) w1 = week1(++y, wkst);
  return { n: Math.floor((d - w1) / 7) + 1, weeks: (week1(y + 1, wkst) - w1) / 7 };
}

// The days of year y a yearly rule picks. Every BY part of a day narrows
// the year's days (an expansion of the year that the others then limit:
// the same set RFC 5545's table makes); without any, the start's month
// and day. BYDAY's ordinals count within each BYMONTH month, else within
// the year. A day of week 1 of the next year in late December, or of the
// last week of the year before in early January, is in year y's set.
function inYear(r: Rule, y: number, startDay: number): Day[] {
  const [, sm, sd] = ymd(startDay);
  const dayParts = r.byWeekNo || r.byYearDay || r.byMonthDay || r.byDay;
  const months = r.byMonth ?? (dayParts ? undefined : [sm]);
  const mdays = r.byMonthDay ?? (dayParts ? undefined : [sd]);
  const first = day(y, 1, 1);
  const len = day(y + 1, 1, 1) - first;
  let any: Set<number> | undefined;
  let nths: Set<Day> | undefined;
  if (r.byDay) {
    any = new Set(r.byDay.filter((b) => b.n === 0).map((b) => b.wd));
    nths = new Set();
    for (const b of r.byDay) {
      if (b.n === 0) continue;
      if (months) {
        for (const m of months) {
          const d = nth(day(y, m, 1), daysInMonth(y, m), b.wd, b.n);
          if (d !== undefined) nths.add(d);
        }
      } else {
        const d = nth(first, len, b.wd, b.n);
        if (d !== undefined) nths.add(d);
      }
    }
  }
  const out: Day[] = [];
  for (let k = 0; k < len; k++) {
    const d = first + k;
    const [, m, dd] = ymd(d);
    if (months && !months.includes(m)) continue;
    if (r.byYearDay && !r.byYearDay.some((n) => (n > 0 ? n : len + n + 1) === k + 1)) continue;
    if (mdays) {
      const ml = daysInMonth(y, m);
      if (!mdays.some((n) => (n > 0 ? n : ml + n + 1) === dd)) continue;
    }
    if (r.byWeekNo) {
      const w = weekNo(d, r.wkst);
      if (!r.byWeekNo.some((n) => (n > 0 ? n : w.weeks + n + 1) === w.n)) continue;
    }
    if (any && nths && !any.has(weekday(d)) && !nths.has(d)) continue;
    out.push(d);
  }
  return out;
}

// The times of day an occurrence starts at (BYHOUR and BYMINUTE expand the
// start's), as minutes after midnight.
function times(r: Rule, startMin: number): number[] {
  const hs = r.byHour ?? [Math.floor(startMin / 60)];
  const ms = r.byMinute ?? [startMin % 60];
  const out: number[] = [];
  for (const h of hs) for (const m of ms) out.push(h * 60 + m);
  return out;
}

// A period's starts (as minutes: day * 1440 + minute), in order, BYSETPOS
// picking from them.
function starts(days: Day[], tm: number[], pos?: number[]): number[] {
  const all = new Set<number>();
  for (const d of days) for (const t of tm) all.add(d * 1440 + t);
  const sorted = [...all].sort((a, b) => a - b);
  if (!pos) return sorted;
  const out: number[] = [];
  for (const p of pos) {
    const k = p > 0 ? sorted[p - 1] : sorted[sorted.length + p];
    if (k !== undefined) out.push(k);
  }
  return [...new Set(out)].sort((a, b) => a - b);
}

// The candidate starts of period i (the i-th minute, hour, day, week, month or year after the start's).
function period(r: Rule, start: Wall, i: number): number[] {
  const [y, m, d] = ymd(start.day);
  const tm = times(r, start.min);
  switch (r.freq) {
    case "MINUTELY":
    case "HOURLY": {
      const at = periodStart(r, start, i);
      const dd = Math.floor(at / 1440);
      if (!dayAllowed(r, dd)) return [];
      const hour = Math.floor((at % 1440) / 60);
      if (r.byHour && !r.byHour.includes(hour)) return [];
      if (r.freq === "MINUTELY") return r.byMinute && !r.byMinute.includes(at % 60) ? [] : starts([dd], [at % 1440], r.bySetPos);
      return starts([dd], (r.byMinute ?? [start.min % 60]).map((mi) => hour * 60 + mi), r.bySetPos);
    }
    case "DAILY": {
      const t = start.day + i * r.interval;
      const [ty, tmo] = ymd(t);
      if (r.byMonth && !r.byMonth.includes(tmo)) return [];
      if (r.byMonthDay && !monthDays(ty, tmo, r.byMonthDay).includes(t)) return [];
      if (r.byDay && !r.byDay.some((b) => b.wd === weekday(t))) return [];
      return starts([t], tm, r.bySetPos);
    }
    case "WEEKLY": {
      const weekStart = start.day - ((weekday(start.day) - r.wkst + 7) % 7) + i * 7 * r.interval;
      const wds = r.byDay?.length ? r.byDay.map((b) => b.wd) : [weekday(start.day)];
      let days = wds.map((wd) => weekStart + ((wd - r.wkst + 7) % 7));
      if (r.byMonth) days = days.filter((t) => r.byMonth!.includes(ymd(t)[1]));
      return starts(days, tm, r.bySetPos);
    }
    case "MONTHLY": {
      const first = addMonths(day(y, m, 1), i * r.interval);
      const [fy, fm] = ymd(first);
      if (r.byMonth && !r.byMonth.includes(fm)) return [];
      return starts(inMonth(r, fy, fm, d), tm, r.bySetPos);
    }
    case "YEARLY":
      return starts(inYear(r, y + i * r.interval, start.day), tm, r.bySetPos);
  }
}

// The most minutes one period takes, for stepping over periods (a jump
// never passes the window).
const SPAN: Record<Freq, number> = { MINUTELY: 1, HOURLY: 60, DAILY: 1440, WEEKLY: 7 * 1440, MONTHLY: 31 * 1440, YEARLY: 366 * 1440 };

// The first period of an hourly or minutely rule from minute k on.
function periodFrom(r: Rule, start: Wall, k: number): number {
  return Math.max(0, Math.ceil((k - periodStart(r, start, 0)) / (r.interval * SPAN[r.freq])));
}

/**
 * The starts of the occurrences of an event starting at start (its first
 * occurrence, whatever the rule says) whose day is from..to, at most
 * limit, in order; excluded holds the starts EXDATE takes out (as minutes:
 * day * 1440 + minute), extra RDATE's starts.
 */
export function expand(start: Wall, rule: Rule | undefined, from: Day, to: Day, excluded: Set<number> = new Set(), limit = 1000, extra: Wall[] = []): Wall[] {
  const out = expandRule(start, rule, from, to, excluded, limit);
  if (!extra.length) return out;
  const seen = new Set(out.map((w) => w.day * 1440 + w.min));
  for (const w of extra) {
    const k = w.day * 1440 + w.min;
    if (w.day >= from && w.day <= to && !excluded.has(k) && !seen.has(k)) {
      seen.add(k);
      out.push(w);
    }
  }
  return out.sort((a, b) => a.day * 1440 + a.min - (b.day * 1440 + b.min)).slice(0, limit);
}

function expandRule(start: Wall, rule: Rule | undefined, from: Day, to: Day, excluded: Set<number>, limit: number): Wall[] {
  const out: Wall[] = [];
  const keep = (w: Wall) => {
    if (w.day >= from && w.day <= to && !excluded.has(w.day * 1440 + w.min)) out.push(w);
  };
  if (!rule) {
    keep(start);
    return out;
  }
  keep(start);
  let count = 1;
  const startKey = start.day * 1440 + start.min;
  const untilKey = rule.until ? rule.until.day * 1440 + rule.until.min : Infinity;
  const fine = rule.freq === "HOURLY" || rule.freq === "MINUTELY";
  // Without COUNT, periods before the window can be stepped over.
  let i = 0;
  if (!rule.count && from > start.day) {
    i = Math.max(0, Math.floor(((from - start.day) * 1440) / (SPAN[rule.freq] * rule.interval)) - 2);
  }
  for (let guard = 0; guard < 200000 && out.length < limit; i++, guard++) {
    const keys = period(rule, start, i);
    if (keys.length === 0 && fine) {
      // Step over the rest of a day, or an hour, the BY parts leave out.
      const at = periodStart(rule, start, i);
      const d = Math.floor(at / 1440);
      if (d > Math.min(to, rule.until?.day ?? Infinity)) break;
      let next = at + 1;
      if (!dayAllowed(rule, d)) next = d * 1440 + 1440;
      else if (rule.byHour && !rule.byHour.includes(Math.floor((at % 1440) / 60))) next = Math.floor(at / 60) * 60 + 60;
      i = Math.max(i, periodFrom(rule, start, next) - 1);
      continue;
    }
    if (keys.length === 0 && (rule.freq === "DAILY" || rule.freq === "WEEKLY")) {
      if (start.day + (i * SPAN[rule.freq] * rule.interval) / 1440 > Math.min(to, rule.until?.day ?? Infinity)) break;
      continue;
    }
    let past = false;
    for (const k of keys) {
      if (k <= startKey) continue;
      if (k > untilKey) {
        past = true;
        break;
      }
      const w = { day: Math.floor(k / 1440), min: k % 1440 };
      if (w.day > to) {
        past = true;
        break;
      }
      count++;
      if (rule.count && count > rule.count) {
        past = true;
        break;
      }
      keep(w);
    }
    if (past) break;
    // A period far past the window with nothing in it (Feb 30th): stop.
    if (keys.length === 0 && (i * SPAN[rule.freq] * rule.interval) / 1440 > to - start.day + 4 * 366) break;
  }
  return out;
}

/** The start of the last occurrence a rule makes (COUNT or UNTIL), undefined when it goes on. */
export function lastStart(start: Wall, rule: Rule | undefined): Wall | undefined {
  if (!rule) return start;
  if (!rule.count && !rule.until) return undefined;
  const all = expandRule(start, rule, start.day, rule.until?.day ?? start.day + 366 * 200, new Set(), MAX_COUNT + 1);
  return all.at(-1) ?? start;
}
