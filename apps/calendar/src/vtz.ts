// VTIMEZONE (RFC 5545 §3.6.5): a zone a file defines itself, for a TZID
// that is neither an IANA name nor a spelling ianaZone knows (Outlook's
// "Customized Time Zone", Lotus Notes' "GMT +0800"). Its observances
// (STANDARD, DAYLIGHT) each start at DTSTART, a wall time on the offset
// before (TZOFFSETFROM), again at each RRULE and RDATE onset, and set
// TZOFFSETTO from then on. A zone whose onsets over the years around now
// are an IANA zone's is that zone (matchZone, with the system's offsets);
// otherwise the calendar keeps the zone as "vtz:<hash>" and turns its
// onsets into the spans its conversions read (dates.ts), on the clock of
// 1970 to 2100. Everything is bounded: zones a file defines, observances,
// dates, onsets.
import { day, fmtWall, parseWall, type Wall } from "./dates";
import { expand, parseRule } from "./recur";

export interface Observance {
  dst: boolean;
  /** DTSTART: "YYYY-MM-DDTHH:MM" on the offset before. */
  start: string;
  /** TZOFFSETFROM and TZOFFSETTO, minutes east of UTC. */
  from: number;
  to: number;
  rrule: string;
  rdates: string[];
}

export interface VZone {
  tzid: string;
  obs: Observance[];
}

export const MAX_VZONES = 16;
const MAX_OBSERVANCES = 32;
const MAX_RDATES = 64;
const MAX_ONSETS = 1000;
const FIRST = day(1970, 1, 1);
const LAST = day(2100, 1, 1);

/** The prefix of a zone the calendar keeps from a file. */
export const OWN = "vtz:";

export function ownZone(zone: string): boolean {
  return zone.startsWith(OWN);
}

// FNV-1a, 32 bits.
function fnv(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(16).padStart(8, "0");
}

/** A zone's key: the same TZID and observances are the same key in any file. */
export function zoneKey(z: VZone): string {
  return OWN + fnv(JSON.stringify(z));
}

/** "+0800", "-0430", "+053000" as minutes (seconds dropped), undefined for anything else. */
export function parseOffset(v: string): number | undefined {
  const m = /^([+-])(\d{2})(\d{2})(\d{2})?$/.exec(v.trim());
  if (!m || Number(m[2]) > 18 || Number(m[3]) > 59) return undefined;
  const n = Number(m[2]) * 60 + Number(m[3]);
  return m[1] === "-" ? -n : n;
}

export function formatOffset(min: number): string {
  const a = Math.abs(min);
  return (min < 0 ? "-" : "+") + String(Math.floor(a / 60)).padStart(2, "0") + String(a % 60).padStart(2, "0");
}

// A local date-time "YYYYMMDDTHHMMSS" (or a date, at midnight) as a wall time.
function localTime(v: string): Wall | undefined {
  const m = /^(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2})?)?$/.exec(v.trim());
  if (!m) return undefined;
  return parseWall(`${m[1]}-${m[2]}-${m[3]}T${m[4] ?? "00"}:${m[5] ?? "00"}`);
}

/** An observance from its properties (name, value), or undefined when it does not make one. */
export function observance(dst: boolean, props: { name: string; value: string }[]): Observance | undefined {
  const get = (n: string) => props.find((p) => p.name === n)?.value.trim();
  const start = localTime(get("DTSTART") ?? "");
  const from = parseOffset(get("TZOFFSETFROM") ?? "");
  const to = parseOffset(get("TZOFFSETTO") ?? "");
  if (!start || from === undefined || to === undefined) return undefined;
  const rrule = get("RRULE") ?? "";
  if (rrule && !ruleOfObservance(rrule, from)) return undefined;
  const rdates: string[] = [];
  for (const p of props.filter((x) => x.name === "RDATE")) {
    for (const v of p.value.split(",")) {
      const w = localTime(v.replace(/Z$/, ""));
      if (w && rdates.length < MAX_RDATES) rdates.push(fmtWall(v.trim().endsWith("Z") ? shift(w, from) : w));
    }
  }
  return { dst, start: fmtWall(start), from, to, rrule, rdates };
}

function shift(w: Wall, min: number): Wall {
  const t = w.day * 1440 + w.min + min;
  return { day: Math.floor(t / 1440), min: ((t % 1440) + 1440) % 1440 };
}

// An observance's rule: UNTIL is UTC (RFC 5545 has it so in a VTIMEZONE),
// on the observance's clock it is UNTIL plus the offset before the onset.
function ruleOfObservance(rrule: string, from: number) {
  return parseRule(rrule, (v) => {
    const w = localTime(v.replace(/Z$/, ""));
    if (!w) return undefined;
    if (!/T/.test(v)) return { day: w.day, min: 1439 };
    return v.endsWith("Z") ? shift(w, from) : w;
  });
}

/** Adds an observance to a zone, within the bounds. */
export function addObservance(z: VZone, o: Observance | undefined): void {
  if (o && z.obs.length < MAX_OBSERVANCES) z.obs.push(o);
}

/**
 * The zone's spans of one offset (as dates.ts's setZoneSpans takes them:
 * Unix seconds, offsets in seconds), in order; before the first onset the
 * first one's TZOFFSETFROM.
 */
export function vzoneSpans(z: VZone): { start: number; offset: number }[] {
  const onsets: { at: number; to: number; from: number }[] = [];
  const add = (w: Wall, o: Observance) => {
    onsets.push({ at: (w.day * 1440 + w.min - o.from) * 60, to: o.to, from: o.from });
  };
  for (const o of z.obs) {
    const start = parseWall(o.start);
    if (!start) continue;
    add(start, o);
    const rule = o.rrule ? ruleOfObservance(o.rrule, o.from) : undefined;
    if (rule) for (const w of expand(start, rule, Math.max(start.day, FIRST), LAST, new Set(), 400)) add(w, o);
    for (const r of o.rdates) {
      const w = parseWall(r);
      if (w) add(w, o);
    }
  }
  onsets.sort((a, b) => a.at - b.at);
  const kept = onsets.filter((o, i) => i === 0 || o.at !== onsets[i - 1].at).slice(-MAX_ONSETS);
  if (!kept.length) return [];
  return [{ start: -(2 ** 40), offset: kept[0].from * 60 }, ...kept.map((o) => ({ start: o.at, offset: o.to * 60 }))];
}

/** The offset (minutes) a zone's spans give at a Unix time (seconds). */
export function offsetIn(spans: { start: number; offset: number }[], t: number): number {
  let off = spans[0]?.offset ?? 0;
  for (const s of spans) {
    if (s.start > t) break;
    off = s.offset;
  }
  return off / 60;
}

/** The changes of offset within from..to (Unix seconds): [when, the offset after], and the offset at from. */
export function changes(spans: { start: number; offset: number }[], from: number, to: number): string {
  const out = [String(offsetIn(spans, from))];
  let last = offsetIn(spans, from);
  for (const s of spans) {
    if (s.start <= from || s.start > to) continue;
    if (s.offset / 60 !== last) out.push(`${s.start}:${s.offset / 60}`);
    last = s.offset / 60;
  }
  return out.join(",");
}

// IANA zones by their offsets in January and July (minutes east, today's
// rules), the likelier first: the candidates a file's zone is compared with.
const CANDIDATES: [string, number, number][] = [
  ["Asia/Shanghai", 480, 480], ["Asia/Hong_Kong", 480, 480], ["Asia/Taipei", 480, 480], ["Asia/Singapore", 480, 480],
  ["Asia/Kuala_Lumpur", 480, 480], ["Asia/Manila", 480, 480], ["Australia/Perth", 480, 480],
  ["Asia/Tokyo", 540, 540], ["Asia/Seoul", 540, 540], ["Asia/Kolkata", 330, 330], ["Asia/Kathmandu", 345, 345],
  ["Asia/Dhaka", 360, 360], ["Asia/Yangon", 390, 390], ["Asia/Bangkok", 420, 420], ["Asia/Jakarta", 420, 420],
  ["Asia/Ho_Chi_Minh", 420, 420], ["Asia/Karachi", 300, 300], ["Asia/Tashkent", 300, 300], ["Asia/Dubai", 240, 240],
  ["Asia/Tehran", 210, 210], ["Asia/Riyadh", 180, 180], ["Europe/Istanbul", 180, 180], ["Europe/Moscow", 180, 180],
  ["Asia/Jerusalem", 120, 180], ["Africa/Cairo", 120, 180], ["Africa/Johannesburg", 120, 120], ["Africa/Nairobi", 180, 180],
  ["Africa/Lagos", 60, 60], ["Europe/Berlin", 60, 120], ["Europe/Paris", 60, 120], ["Europe/Madrid", 60, 120],
  ["Europe/Rome", 60, 120], ["Europe/Amsterdam", 60, 120], ["Europe/Warsaw", 60, 120], ["Europe/Athens", 120, 180],
  ["Europe/Helsinki", 120, 180], ["Europe/Kyiv", 120, 180], ["Europe/London", 0, 60], ["Europe/Dublin", 0, 60],
  ["Europe/Lisbon", 0, 60], ["Atlantic/Reykjavik", 0, 0], ["UTC", 0, 0], ["America/Sao_Paulo", -180, -180],
  ["America/Argentina/Buenos_Aires", -180, -180], ["America/Santiago", -180, -240], ["America/Halifax", -240, -180],
  ["America/St_Johns", -210, -150], ["America/Caracas", -240, -240], ["America/New_York", -300, -240],
  ["America/Toronto", -300, -240], ["America/Bogota", -300, -300], ["America/Lima", -300, -300],
  ["America/Chicago", -360, -300], ["America/Mexico_City", -360, -360], ["America/Denver", -420, -360],
  ["America/Phoenix", -420, -420], ["America/Los_Angeles", -480, -420], ["America/Vancouver", -480, -420],
  ["America/Anchorage", -540, -480], ["Pacific/Honolulu", -600, -600], ["Australia/Darwin", 570, 570],
  ["Australia/Adelaide", 630, 570], ["Australia/Brisbane", 600, 600], ["Australia/Sydney", 660, 600],
  ["Australia/Melbourne", 660, 600], ["Pacific/Guam", 600, 600], ["Pacific/Noumea", 660, 660],
  ["Pacific/Auckland", 780, 720], ["Pacific/Fiji", 720, 720], ["Pacific/Tongatapu", 780, 780],
];

/** The IANA zones to compare a zone with: the phone's, then those with its offsets in January and July of year y. */
export function candidates(spans: { start: number; offset: number }[], y: number, phone: string): string[] {
  const jan = offsetIn(spans, Date.UTC(y, 0, 15) / 1000);
  const jul = offsetIn(spans, Date.UTC(y, 6, 15) / 1000);
  const out = CANDIDATES.filter(([, a, b]) => a === jan && b === jul).map(([z]) => z);
  return phone && !out.includes(phone) ? [phone, ...out] : out;
}

/**
 * The IANA zone a file's zone is, if one has the same offsets and the same
 * changes from two years before now to eight after (fetch: an IANA zone's
 * spans, from the system); "" for none.
 */
export async function matchZone(z: VZone, now: number, phone: string, fetch: (zone: string) => Promise<{ start: number; offset: number }[] | undefined>): Promise<string> {
  const own = vzoneSpans(z);
  if (!own.length) return "";
  const y = new Date(now).getUTCFullYear();
  const from = Date.UTC(y - 2, 0, 1) / 1000;
  const to = Date.UTC(y + 8, 0, 1) / 1000;
  const want = changes(own, from, to);
  for (const c of candidates(own, y, phone)) {
    const s = c === "UTC" ? [{ start: 0, offset: 0 }] : await fetch(c);
    if (s?.length && changes(s, from, to) === want) return c;
  }
  return "";
}
