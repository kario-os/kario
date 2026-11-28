// Dates and times as a calendar keeps them: a day is a day number (days
// from 1970-01-01, no zone), a wall time a day and the minutes after its
// midnight on some zone's clock. An event's wall times are on its own zone
// (an IANA name), or float on the phone's; instants are Unix milliseconds.
// The runtime's Date knows only the phone's zone (TZ, no tz database in
// JavaScript), so another zone's offsets come from the system (time.offsets)
// into a table the conversions read.
import { dayNumber, fromDayNumber } from "./lunar";

export type Day = number;

export interface Wall {
  day: Day;
  min: number;
}

const pad = (n: number, w = 2) => String(n).padStart(w, "0");

/** "YYYY-MM-DD", or undefined for anything else (or a date that does not exist). */
export function parseDate(s: string): Day | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) return undefined;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  if (mo < 1 || mo > 12 || d < 1 || d > daysInMonth(y, mo)) return undefined;
  return dayNumber(y, mo, d);
}

export function fmtDate(d: Day): string {
  const [y, m, dd] = fromDayNumber(d);
  return `${pad(y, 4)}-${pad(m)}-${pad(dd)}`;
}

/** "YYYY-MM-DDTHH:MM". */
export function parseWall(s: string): Wall | undefined {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2})$/.exec(s);
  if (!m) return undefined;
  const day = parseDate(m[1]);
  const h = Number(m[2]);
  const mi = Number(m[3]);
  if (day === undefined || h > 23 || mi > 59) return undefined;
  return { day, min: h * 60 + mi };
}

export function fmtWall(w: Wall): string {
  return `${fmtDate(w.day)}T${fmtTime(w.min)}`;
}

/** "HH:MM" of minutes after midnight. */
export function fmtTime(min: number): string {
  return `${pad(Math.floor(min / 60))}:${pad(min % 60)}`;
}

export function parseTime(s: string): number | undefined {
  const m = /^(\d{2}):(\d{2})$/.exec(s);
  if (!m || Number(m[1]) > 23 || Number(m[2]) > 59) return undefined;
  return Number(m[1]) * 60 + Number(m[2]);
}

export function ymd(d: Day): [number, number, number] {
  return fromDayNumber(d);
}

export function day(y: number, m: number, d: number): Day {
  return dayNumber(y, m, d);
}

/** 0 Sunday to 6 Saturday. */
export function weekday(d: Day): number {
  return (((d + 4) % 7) + 7) % 7;
}

export function daysInMonth(y: number, m: number): number {
  return [31, (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0 ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][m - 1];
}

/** The wall time minutes later (or earlier), on the same clock. */
export function addMinutes(w: Wall, minutes: number): Wall {
  const t = w.day * 1440 + w.min + minutes;
  return { day: Math.floor(t / 1440), min: ((t % 1440) + 1440) % 1440 };
}

export function wallCompare(a: Wall, b: Wall): number {
  return a.day - b.day || a.min - b.min;
}

/** The first day of the month n months after d's (the same day when the month has it, else its last). */
export function addMonths(d: Day, n: number): Day {
  const [y, m, dd] = ymd(d);
  const t = y * 12 + (m - 1) + n;
  const yy = Math.floor(t / 12);
  const mm = (t % 12) + 1;
  return day(yy, mm, Math.min(dd, daysInMonth(yy, mm)));
}

// Zones: the phone's own (TZ; "" floats on it) goes through Date, the
// others through the spans the system gave.
let phoneZone = "";
const spans = new Map<string, { start: number; offset: number }[]>();

/** The phone's zone, as the runtime's TZ has it (locale.get, time.zone). */
export function setPhoneZone(zone: string): void {
  phoneZone = zone;
}

export function getPhoneZone(): string {
  return phoneZone;
}

/** A zone's spans of one UTC offset (time.offsets, Unix seconds). */
export function setZoneSpans(zone: string, list: { start: number; offset: number }[]): void {
  spans.set(zone, [...list].sort((a, b) => a.start - b.start));
}

/** A zone's spans as the table has them (undefined: none there). */
export function zoneSpans(zone: string): { start: number; offset: number }[] | undefined {
  return spans.get(zone);
}

export function hasZone(zone: string): boolean {
  return local(zone) || zone === "UTC" || zone === "Etc/UTC" || spans.has(zone);
}

/** The zones events name that the table lacks. */
export function missingZones(zones: Iterable<string>): string[] {
  return [...new Set(zones)].filter((z) => !hasZone(z));
}

function local(zone: string): boolean {
  return zone === "" || zone === phoneZone;
}

function offsetAt(zone: string, ms: number): number {
  if (zone === "UTC" || zone === "Etc/UTC") return 0;
  const list = spans.get(zone);
  if (!list?.length) return -new Date(ms).getTimezoneOffset() * 60;
  let off = list[0].offset;
  for (const s of list) {
    if (s.start * 1000 > ms) break;
    off = s.offset;
  }
  return off;
}

/** The instant zone's clock shows w (the first of two in an hour a change repeats; one it skips comes as far past the change). */
export function toInstant(w: Wall, zone: string): number {
  const [y, m, d] = ymd(w.day);
  if (local(zone)) return new Date(y, m - 1, d, Math.floor(w.min / 60), w.min % 60).getTime();
  const naive = Date.UTC(y, m - 1, d, Math.floor(w.min / 60), w.min % 60);
  const before = offsetAt(zone, naive - 86400000);
  const after = offsetAt(zone, naive + 86400000);
  const candidates = [naive - before * 1000, naive - after * 1000].sort((a, b) => a - b);
  for (const c of candidates) if (offsetAt(zone, c) * 1000 === naive - c) return c;
  return naive - before * 1000;
}

/** The wall time zone's clock shows at ms. */
export function toWall(ms: number, zone: string): Wall {
  if (local(zone)) {
    const t = new Date(ms);
    return { day: day(t.getFullYear(), t.getMonth() + 1, t.getDate()), min: t.getHours() * 60 + t.getMinutes() };
  }
  const t = ms + offsetAt(zone, ms) * 1000;
  const days = Math.floor(t / 86400000);
  return { day: days, min: Math.floor((t - days * 86400000) / 60000) };
}

/** Today on the phone's clock. */
export function today(now = Date.now()): Day {
  return toWall(now, "").day;
}
