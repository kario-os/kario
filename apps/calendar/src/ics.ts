// iCalendar (RFC 5545), the subset calendars exchange: VEVENTs with
// DTSTART, DTEND or DURATION (dates, floating times, UTC times, TZID with
// an IANA name, or another TZID with the file's VTIMEZONE: vtz.ts),
// SUMMARY, LOCATION, DESCRIPTION, UID, RRULE, RDATE (a PERIOD's start: the
// occurrence lasts as the event does), EXDATE, RECURRENCE-ID and VALARMs
// that display (or sound) a reminder. Other components and
// properties are passed over. The parser never throws on its input: a
// broken event is left out with the line it broke on, and everything is
// bounded (the file, the events, each text, the zones).
import { addMinutes, fmtDate, fmtWall, hasZone, parseDate, setZoneSpans, toInstant, toWall, type Wall } from "./dates";
import { type Attendee, clip, MAX_ADDRESS, MAX_ALARMS, MAX_ATTENDEES, MAX_EVENTS, MAX_EXDATES, MAX_LOCATION, MAX_NAME, MAX_NOTES, MAX_RDATES, MAX_TITLE, type CalEvent, type Person } from "./model";
import { parseRule, type Rule } from "./recur";
import { addObservance, formatOffset, MAX_VZONES, observance, ownZone, type VZone, vzoneSpans, zoneKey } from "./vtz";

/** The largest file the parser reads (what file.open reads whole). */
export const MAX_ICS_BYTES = 1 << 20;

export interface ParseResult {
  events: CalEvent[];
  /** What was left out and why, with the line (1-based) it was on. */
  problems: { line: number; why: string }[];
  /** The file's own zones the events are on, by key ("vtz:..."). */
  zones: Record<string, VZone>;
  /** iTIP's METHOD (RFC 5546): REQUEST, REPLY, CANCEL, PUBLISH; "" for none. */
  method: string;
  /** A REPLY's or a CANCEL's events, which need no DTSTART: what they answer or cancel. */
  messages: Message[];
}

/** An event of a REPLY or a CANCEL: its UID, the occurrence (RECURRENCE-ID, on its own clock), SEQUENCE, who. */
export interface Message {
  uid: string;
  rid?: { wall: Wall; date: boolean; zone: string };
  seq: number;
  title: string;
  organizer?: Person;
  attendees: Attendee[];
}

const MAX_MESSAGES = 200;

interface Prop {
  name: string;
  params: Map<string, string>;
  value: string;
  line: number;
}

// Windows' zone names that Outlook writes as TZID, to IANA's.
const WINDOWS_ZONES: Record<string, string> = {
  "China Standard Time": "Asia/Shanghai",
  "Taipei Standard Time": "Asia/Taipei",
  "Tokyo Standard Time": "Asia/Tokyo",
  "Korea Standard Time": "Asia/Seoul",
  "Singapore Standard Time": "Asia/Singapore",
  "India Standard Time": "Asia/Kolkata",
  "GMT Standard Time": "Europe/London",
  "W. Europe Standard Time": "Europe/Berlin",
  "Central Europe Standard Time": "Europe/Budapest",
  "Romance Standard Time": "Europe/Paris",
  "Eastern Standard Time": "America/New_York",
  "Central Standard Time": "America/Chicago",
  "Mountain Standard Time": "America/Denver",
  "Pacific Standard Time": "America/Los_Angeles",
  "Central Standard Time (Mexico)": "America/Mexico_City",
  "UTC": "UTC",
};

/** An IANA zone from a TZID ("" when it names none this knows: the time then floats). */
export function ianaZone(tzid: string): string {
  const t = tzid.trim();
  if (WINDOWS_ZONES[t]) return WINDOWS_ZONES[t];
  // Thunderbird's "/mozilla.org/20050126_1/Asia/Shanghai" and the like.
  const m = /([A-Za-z]+(?:\/[A-Za-z0-9_+-]+){1,2})$/.exec(t);
  if (m && /^(Africa|America|Antarctica|Arctic|Asia|Atlantic|Australia|Europe|Indian|Pacific|Etc)\//.test(m[1])) return m[1];
  return t === "UTC" || t === "GMT" ? "UTC" : "";
}

function unescapeText(v: string): string {
  return v.replace(/\\([\\;,nN])/g, (_, c: string) => (c === "n" || c === "N" ? "\n" : c));
}

function escapeText(v: string): string {
  return v.replace(/[\\;,]/g, (c) => "\\" + c).replace(/\r?\n/g, "\\n");
}

function lines(text: string): { text: string; line: number }[] {
  const out: { text: string; line: number }[] = [];
  const raw = text.replace(/^\uFEFF/, "").split(/\r\n|\n|\r/);
  raw.forEach((l, i) => {
    if ((l.startsWith(" ") || l.startsWith("\t")) && out.length) out[out.length - 1].text += l.slice(1);
    else if (l !== "") out.push({ text: l, line: i + 1 });
  });
  return out;
}

function parseLine(text: string, line: number): Prop | undefined {
  let i = 0;
  const name = /^[A-Za-z0-9-]+/.exec(text)?.[0];
  if (!name) return undefined;
  i = name.length;
  const params = new Map<string, string>();
  while (text[i] === ";") {
    const eq = text.indexOf("=", i);
    if (eq < 0) return undefined;
    const key = text.slice(i + 1, eq).toUpperCase();
    i = eq + 1;
    let val = "";
    if (text[i] === '"') {
      const close = text.indexOf('"', i + 1);
      if (close < 0) return undefined;
      val = text.slice(i + 1, close);
      i = close + 1;
    } else {
      const m = /^[^;:]*/.exec(text.slice(i))![0];
      val = m;
      i += m.length;
    }
    params.set(key, val);
  }
  if (text[i] !== ":") return undefined;
  return { name: name.toUpperCase(), params, value: text.slice(i + 1), line };
}

/** A DATE or DATE-TIME value: its wall time (min -1 for a date) and zone ("UTC" for Z; zoneOf reads TZID). */
function parseTimeValue(p: Prop, zoneOf: (tzid: string) => string = ianaZone): { wall: Wall; date: boolean; zone: string } | undefined {
  const v = p.value.trim();
  const m = /^(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2})?(Z)?)?$/.exec(v);
  if (!m) return undefined;
  const d = parseDate(`${m[1]}-${m[2]}-${m[3]}`);
  if (d === undefined) return undefined;
  if (m[4] === undefined) return { wall: { day: d, min: 0 }, date: true, zone: "" };
  const h = Number(m[4]);
  const mi = Number(m[5]);
  if (h > 23 || mi > 59) return undefined;
  const zone = m[7] ? "UTC" : zoneOf(p.params.get("TZID") ?? "");
  return { wall: { day: d, min: h * 60 + mi }, date: p.params.get("VALUE")?.toUpperCase() === "DATE", zone };
}

/** The VTIMEZONEs of a file, by TZID (at most MAX_VZONES; those without an observance left out). */
export function vtimezones(text: string): Map<string, VZone> {
  const out = new Map<string, VZone>();
  if (text.length > MAX_ICS_BYTES) return out;
  let zone: VZone | undefined;
  let obs: { dst: boolean; props: Prop[] } | undefined;
  let depth = 0;
  for (const { text: l, line } of lines(text)) {
    const p = parseLine(l, line);
    if (!p) continue;
    const what = p.value.trim().toUpperCase();
    if (p.name === "BEGIN") {
      depth++;
      if (what === "VTIMEZONE" && depth <= 2 && !zone) zone = { tzid: "", obs: [] };
      else if (zone && (what === "STANDARD" || what === "DAYLIGHT")) obs = { dst: what === "DAYLIGHT", props: [] };
      if (depth > 8) break;
    } else if (p.name === "END") {
      depth = Math.max(0, depth - 1);
      if (obs && (what === "STANDARD" || what === "DAYLIGHT")) {
        if (zone) addObservance(zone, observance(obs.dst, obs.props));
        obs = undefined;
      } else if (zone && what === "VTIMEZONE") {
        if (zone.tzid && zone.obs.length && !out.has(zone.tzid) && out.size < MAX_VZONES) out.set(zone.tzid, zone);
        zone = obs = undefined;
      }
    } else if (obs) obs.props.push(p);
    else if (zone && p.name === "TZID") zone.tzid = clip(p.value.trim(), 100);
  }
  return out;
}

/** An RFC 5545 DURATION in minutes (seconds dropped), or undefined. */
export function parseDuration(v: string): number | undefined {
  const m = /^([+-])?P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$/.exec(v.trim());
  if (!m || v.trim().length <= 2 || /T$/.test(v.trim())) return undefined;
  const n = (s?: string) => (s ? Number(s) : 0);
  const min = n(m[2]) * 7 * 1440 + n(m[3]) * 1440 + n(m[4]) * 60 + n(m[5]) + Math.floor(n(m[6]) / 60);
  if (min > 3660 * 1440) return undefined;
  return m[1] === "-" ? -min : min;
}

export function formatDuration(min: number): string {
  const sign = min < 0 ? "-" : "";
  let m = Math.abs(min);
  const d = Math.floor(m / 1440);
  m -= d * 1440;
  const h = Math.floor(m / 60);
  m -= h * 60;
  let s = sign + "P";
  if (d) s += d + "D";
  if (h || m || !d) s += "T" + (h ? h + "H" : "") + (m || (!h && !d) ? m + "M" : "");
  return s;
}

// A time of another zone (EXDATE in UTC for a zoned event) on the event's clock.
function onClock(t: { wall: Wall; zone: string }, zone: string): Wall {
  if (t.zone === zone) return t.wall;
  return toWall(toInstant(t.wall, t.zone), zone);
}

function wallText(w: Wall, allDay: boolean): string {
  return allDay ? fmtDate(w.day) : fmtWall(w);
}

/**
 * The events of an iCalendar text. newId makes the calendar's IDs; matched
 * names the IANA zone a TZID's VTIMEZONE was found to be (matchZones).
 * Another TZID with a VTIMEZONE is that zone itself (its spans set for the
 * conversions), one with neither floats.
 */
export function parseICS(text: string, newId: () => string, matched: Record<string, string> = {}): ParseResult {
  const res: ParseResult = { events: [], problems: [], zones: {}, method: "", messages: [] };
  if (text.length > MAX_ICS_BYTES) {
    res.problems.push({ line: 0, why: "the file is larger than 1 MB" });
    return res;
  }
  const own = vtimezones(text);
  const keys = new Map<string, string>();
  const zoneOf = (tzid: string): string => {
    const t = tzid.trim();
    const iana = ianaZone(t);
    // An IANA name the system has no offsets for is the file's own zone, when it defines one.
    if (iana && (hasZone(iana) || !own.has(t))) return iana;
    if (matched[t]) return matched[t];
    const z = own.get(t);
    if (!z) return iana;
    if (!keys.has(t)) {
      const key = zoneKey(z);
      keys.set(t, key);
      res.zones[key] = z;
      setZoneSpans(key, vzoneSpans(z));
    }
    return keys.get(t)!;
  };
  const stack: string[] = [];
  let ev: Prop[] | undefined;
  let alarm: Prop[] | undefined;
  let alarms: Prop[][] = [];
  let evLine = 0;
  for (const { text: l, line } of lines(text)) {
    const p = parseLine(l, line);
    if (!p) {
      if (ev) res.problems.push({ line, why: "a line that is not a property" });
      continue;
    }
    if (p.name === "BEGIN") {
      const what = p.value.trim().toUpperCase();
      stack.push(what);
      if (what === "VEVENT" && stack.length <= 2) {
        ev = [];
        alarms = [];
        evLine = line;
      } else if (what === "VALARM" && ev) alarm = [];
      if (stack.length > 8) {
        res.problems.push({ line, why: "components nested too deep" });
        return res;
      }
      continue;
    }
    if (p.name === "END") {
      const what = p.value.trim().toUpperCase();
      if (stack.at(-1) !== what) {
        res.problems.push({ line, why: `END:${what} without its BEGIN` });
        if (ev && what === "VEVENT") ev = undefined;
        continue;
      }
      stack.pop();
      if (what === "VALARM" && alarm) {
        alarms.push(alarm);
        alarm = undefined;
      } else if (what === "VEVENT" && ev) {
        if (res.events.length >= MAX_EVENTS) {
          res.problems.push({ line: evLine, why: `more than ${MAX_EVENTS} events` });
          return res;
        }
        if (res.method === "REPLY" || res.method === "CANCEL") {
          const m = makeMessage(ev, zoneOf);
          if (typeof m === "string") res.problems.push({ line: evLine, why: m });
          else if (res.messages.length < MAX_MESSAGES) res.messages.push(m);
          ev = undefined;
          continue;
        }
        const made = makeEvent(ev, alarms, newId, zoneOf);
        if (typeof made === "string") res.problems.push({ line: evLine, why: made });
        else if (made) res.events.push(made);
        ev = undefined;
      }
      continue;
    }
    if (alarm) alarm.push(p);
    else if (ev && stack.at(-1) === "VEVENT") ev.push(p);
    else if (!ev && p.name === "METHOD" && stack.length === 1 && stack[0] === "VCALENDAR") res.method = clip(p.value.trim().toUpperCase(), 20);
  }
  if (ev) res.problems.push({ line: evLine, why: "an event without END:VEVENT" });
  return res;
}

// A CAL-ADDRESS property (ORGANIZER, ATTENDEE) as a person; quotes, which
// a parameter cannot hold, are dropped from the name.
function person(p: Prop): Person {
  return { addr: clip(p.value.trim(), MAX_ADDRESS), name: clip((p.params.get("CN") ?? "").replace(/"/g, "").trim(), MAX_NAME) };
}

// A parameter's token (PARTSTAT, ROLE): letters and "-", upper case, bounded.
const token = (v: string | undefined) => (v ?? "").toUpperCase().replace(/[^A-Z-]/g, "").slice(0, 20);

function attendees(props: Prop[]): Attendee[] {
  const out: Attendee[] = [];
  for (const p of props.filter((x) => x.name === "ATTENDEE").slice(0, MAX_ATTENDEES)) {
    const a: Attendee = { ...person(p), partstat: token(p.params.get("PARTSTAT")) || "NEEDS-ACTION" };
    const role = token(p.params.get("ROLE"));
    if (role) a.role = role;
    if (p.params.get("RSVP")?.trim().toUpperCase() === "TRUE") a.rsvp = true;
    const agent = token(p.params.get("SCHEDULE-AGENT"));
    if (agent) a.agent = agent;
    const delivery = /^\d\.\d{1,2}/.exec(p.params.get("SCHEDULE-STATUS")?.trim() ?? "")?.[0];
    if (delivery) a.delivery = delivery;
    out.push(a);
  }
  return out;
}

function sequence(props: Prop[]): number | undefined {
  const v = props.find((p) => p.name === "SEQUENCE")?.value.trim();
  return v && /^\d{1,9}$/.test(v) ? Number(v) : undefined;
}

// One VEVENT of a REPLY or a CANCEL, or why it is left out.
function makeMessage(props: Prop[], zoneOf: (tzid: string) => string): Message | string {
  const get = (n: string) => props.find((p) => p.name === n);
  const uid = clip(get("UID")?.value.trim() || "", 255);
  if (!uid) return "an answer or a cancellation without UID";
  const m: Message = { uid, seq: sequence(props) ?? 0, title: clip(unescapeText(get("SUMMARY")?.value ?? "").trim(), MAX_TITLE), attendees: attendees(props) };
  const rid = get("RECURRENCE-ID");
  if (rid) {
    const t = parseTimeValue(rid, zoneOf);
    if (!t) return `RECURRENCE-ID ${rid.value} is not a date or time`;
    m.rid = t;
  }
  const org = get("ORGANIZER");
  if (org) m.organizer = person(org);
  return m;
}

/** A REPLY's or CANCEL's occurrence on an event's clock, as the event's RECURRENCE-ID and EXDATE are written ("" for the whole event). */
export function occurrenceOf(m: Message, e: CalEvent): string {
  if (!m.rid) return "";
  if (e.allDay || m.rid.date) return fmtDate(m.rid.wall.day);
  return fmtWall(onClock(m.rid, e.zone));
}

// One VEVENT, or why it is left out (undefined: a cancelled one, unless an
// invitation's: the organizer's cancellation the user keeps, marked).
function makeEvent(props: Prop[], alarms: Prop[][], newId: () => string, zoneOf: (tzid: string) => string): CalEvent | string | undefined {
  const get = (n: string) => props.find((p) => p.name === n);
  const status = get("STATUS")?.value.trim().toUpperCase();
  if (status === "CANCELLED" && !get("ORGANIZER")) return undefined;
  const ds = get("DTSTART");
  if (!ds) return "an event without DTSTART";
  const start = parseTimeValue(ds, zoneOf);
  if (!start) return `DTSTART ${ds.value} is not a date or time`;
  const allDay = start.date;
  const zone = allDay ? "" : start.zone;
  let end: Wall;
  const de = get("DTEND");
  const du = get("DURATION");
  if (de) {
    const e = parseTimeValue(de, zoneOf);
    if (!e) return `DTEND ${de.value} is not a date or time`;
    end = allDay ? e.wall : onClock(e, zone);
  } else if (du) {
    const d = parseDuration(du.value);
    if (d === undefined || d < 0) return `DURATION ${du.value} is not a duration`;
    end = addMinutes(start.wall, d);
  } else {
    end = allDay ? { day: start.wall.day + 1, min: 0 } : start.wall;
  }
  if (allDay) end = { day: Math.max(end.day, start.wall.day + 1), min: 0 };
  else if (end.day * 1440 + end.min < start.wall.day * 1440 + start.wall.min) end = start.wall;
  let rrule = "";
  const rr = get("RRULE");
  if (rr) {
    const rule = ruleOf(rr.value.trim(), zone);
    if (/(^|;)FREQ=SECONDLY(;|$)/i.test(rr.value.trim())) return "RRULE FREQ=SECONDLY: the calendar keeps times to the minute";
    if (!rule || (allDay && (rule.byHour || rule.byMinute || rule.freq === "HOURLY" || rule.freq === "MINUTELY"))) return `RRULE ${clip(rr.value, 60)} is not one this calendar repeats by`;
    rrule = rr.value.trim();
  }
  const rdates: string[] = [];
  for (const p of props.filter((x) => x.name === "RDATE")) {
    for (const v of p.value.split(",")) {
      const t = parseTimeValue({ ...p, value: v.split("/")[0] }, zoneOf);
      if (!t) return `RDATE ${clip(v, 40)} is not a date or time`;
      const w = allDay ? t.wall : t.date ? { day: t.wall.day, min: start.wall.min } : onClock(t, zone);
      const k = wallText(w, allDay);
      if (rdates.length < MAX_RDATES && !rdates.includes(k)) rdates.push(k);
    }
  }
  const exdates: string[] = [];
  for (const p of props.filter((x) => x.name === "EXDATE")) {
    for (const v of p.value.split(",")) {
      const t = parseTimeValue({ ...p, value: v }, zoneOf);
      if (!t) return `EXDATE ${clip(v, 40)} is not a date or time`;
      if (exdates.length < MAX_EXDATES) exdates.push(wallText(allDay ? t.wall : onClock(t, zone), allDay));
    }
  }
  let recurrenceId = "";
  const rid = get("RECURRENCE-ID");
  if (rid) {
    const t = parseTimeValue(rid, zoneOf);
    if (!t) return `RECURRENCE-ID ${rid.value} is not a date or time`;
    recurrenceId = wallText(allDay ? t.wall : onClock(t, zone), allDay);
  }
  const reminders: number[] = [];
  for (const a of alarms) {
    const action = a.find((p) => p.name === "ACTION")?.value.trim().toUpperCase();
    if (action !== "DISPLAY" && action !== "AUDIO") continue;
    const tr = a.find((p) => p.name === "TRIGGER");
    if (!tr) continue;
    let before: number | undefined;
    if (tr.params.get("VALUE")?.toUpperCase() === "DATE-TIME") {
      const t = parseTimeValue(tr, zoneOf);
      if (t) before = Math.round((toInstant(start.wall, zone) - toInstant(t.wall, t.zone)) / 60000);
    } else {
      const d = parseDuration(tr.value);
      if (d !== undefined) {
        const len = (end.day - start.wall.day) * 1440 + end.min - start.wall.min;
        before = tr.params.get("RELATED")?.toUpperCase() === "END" ? -(len + d) : -d;
      }
    }
    if (before !== undefined && Math.abs(before) <= 366 * 1440 && !reminders.includes(before) && reminders.length < MAX_ALARMS) reminders.push(before);
  }
  const text = (n: string, max: number) => clip(unescapeText(get(n)?.value ?? "").trim(), max);
  const e: CalEvent = {
    id: newId(),
    uid: clip(get("UID")?.value.trim() || "", 255),
    title: text("SUMMARY", MAX_TITLE),
    allDay,
    start: wallText(start.wall, allDay),
    end: wallText(end, allDay),
    zone,
    location: text("LOCATION", MAX_LOCATION),
    notes: text("DESCRIPTION", MAX_NOTES),
    alarms: reminders,
    rrule,
    exdates,
    recurrenceId,
    src: "",
  };
  if (rdates.length && !recurrenceId) e.rdates = rdates;
  const org = get("ORGANIZER");
  if (org) {
    e.organizer = person(org);
    const who = attendees(props);
    if (who.length) e.attendees = who;
    if (status === "CANCELLED") e.status = "CANCELLED";
  }
  const seq = sequence(props);
  if (seq !== undefined) e.seq = seq;
  return e;
}

// A CAL-ADDRESS property's line.
function addressProp(name: string, p: Person, params: string[] = []): string {
  const all = [...(p.name ? [`CN="${p.name}"`] : []), ...params];
  return name + (all.length ? ";" + all.join(";") : "") + ":" + p.addr;
}

/** An attendee as an ATTENDEE line (more: other parameters); the server's SCHEDULE-STATUS is not written back. */
export function attendeeLine(a: Attendee, more: string[] = []): string {
  return addressProp("ATTENDEE", a, [`PARTSTAT=${a.partstat}`, ...(a.role ? [`ROLE=${a.role}`] : []), ...(a.rsvp ? ["RSVP=TRUE"] : []), ...more]);
}

/**
 * How serialiseICS writes: an iTIP message (METHOD, RFC 5546: no
 * reminders), or the calendar's own copy, where the user's addresses (me)
 * mark the events they organize, schedule says the account's server sends
 * the invitations (RFC 6638), and byPhone marks every invitation as the
 * phone's (the first hand-over: what was sent went as files).
 */
export interface WriteOptions {
  method?: string;
  me?: string[];
  schedule?: boolean;
  byPhone?: boolean;
}

/** Addresses compared as calendars do: "mailto:" and case aside. */
export function sameAddress(a: string, b: string): boolean {
  const n = (s: string) => s.trim().toLowerCase().replace(/^mailto:/, "");
  return n(a) === n(b);
}

/** An organizer as an ORGANIZER line. */
export function organizerLine(p: Person): string {
  return addressProp("ORGANIZER", p);
}

const utf8Length = (s: string) => {
  let n = 0;
  for (const ch of s) {
    const c = ch.codePointAt(0)!;
    n += c < 0x80 ? 1 : c < 0x800 ? 2 : c < 0x10000 ? 3 : 4;
  }
  return n;
};

// A content line folded at 75 octets, never inside a character.
function fold(line: string): string {
  const out: string[] = [];
  let cur = "";
  let len = 0;
  for (const ch of line) {
    const n = utf8Length(ch);
    if (len + n > 75) {
      out.push(cur);
      cur = " ";
      len = 1;
    }
    cur += ch;
    len += n;
  }
  out.push(cur);
  return out.join("\r\n");
}

function timeProp(name: string, value: string, allDay: boolean, zone: string): string {
  const v = value.replace(/[-:]/g, "");
  if (allDay) return `${name};VALUE=DATE:${v.slice(0, 8)}`;
  if (zone === "UTC") return `${name}:${v}00Z`;
  if (zone) return `${name};TZID=${/[;:,]/.test(zone) ? `"${zone}"` : zone}:${v}00`;
  return `${name}:${v}00`;
}

// A zone of the calendar's own as a VTIMEZONE.
function vtimezone(tzid: string, z: VZone): string[] {
  const out = ["BEGIN:VTIMEZONE", "TZID:" + tzid];
  for (const o of z.obs) {
    const kind = o.dst ? "DAYLIGHT" : "STANDARD";
    out.push("BEGIN:" + kind, "DTSTART:" + o.start.replace(/[-:]/g, "") + "00", "TZOFFSETFROM:" + formatOffset(o.from), "TZOFFSETTO:" + formatOffset(o.to));
    if (o.rrule) out.push("RRULE:" + o.rrule);
    for (const r of o.rdates) out.push("RDATE:" + r.replace(/[-:]/g, "") + "00");
    out.push("END:" + kind);
  }
  out.push("END:VTIMEZONE");
  return out;
}

/**
 * An iCalendar text of the events (stamp: DTSTAMP, "YYYYMMDDTHHMMSSZ"); an
 * event on a zone of the calendar's own ("vtz:...", zones has it) is
 * written with that zone's TZID and VTIMEZONE. An event the user organizes
 * (o.me) has its attendees marked SCHEDULE-AGENT=CLIENT (RFC 6638 §7.1):
 * the phone sends the invitations, as files, so a server that schedules
 * does not send them again; where the account's server schedules
 * (o.schedule) the mark stays only on the attendees that have it (their
 * invitations went as files), and the organizer of an invitation that
 * did not come from the account is marked instead (the answer goes as a
 * file); o.byPhone marks them all. A message carries none of it.
 */
export function serialiseICS(events: CalEvent[], stamp: string, zones: Record<string, VZone> = {}, o: WriteOptions = {}): string {
  const out = ["BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Kario//Calendar//EN", "CALSCALE:GREGORIAN"];
  if (o.method) out.push("METHOD:" + o.method);
  const tzids = new Map<string, string>();
  for (const e of events) {
    const z = zones[e.zone];
    if (!ownZone(e.zone) || !z || tzids.has(e.zone)) continue;
    let tzid = z.tzid.replace(/"/g, "");
    if ([...tzids.values()].includes(tzid)) tzid += " " + e.zone.slice(4);
    tzids.set(e.zone, tzid);
    out.push(...vtimezone(tzid, z));
  }
  for (const e of events) {
    const zone = tzids.get(e.zone) ?? e.zone;
    out.push("BEGIN:VEVENT", "UID:" + (e.uid || e.id), "DTSTAMP:" + stamp);
    out.push(timeProp("DTSTART", e.start, e.allDay, zone), timeProp("DTEND", e.end, e.allDay, zone));
    if (e.recurrenceId) out.push(timeProp("RECURRENCE-ID", e.recurrenceId, e.allDay, zone));
    out.push("SUMMARY:" + escapeText(e.title));
    if (e.location) out.push("LOCATION:" + escapeText(e.location));
    if (e.notes) out.push("DESCRIPTION:" + escapeText(e.notes));
    if (e.rrule) out.push("RRULE:" + e.rrule);
    for (const x of e.rdates ?? []) out.push(timeProp("RDATE", x, e.allDay, zone));
    for (const x of e.exdates) out.push(timeProp("EXDATE", x, e.allDay, zone));
    const organizes = !!e.organizer && (o.me ?? []).some((m) => sameAddress(m, e.organizer!.addr));
    const answersByFile = !o.method && !!e.organizer && !organizes && (!!o.byPhone || (!!o.schedule && !e.src.startsWith("account:")));
    if (e.organizer) out.push(addressProp("ORGANIZER", e.organizer, answersByFile ? ["SCHEDULE-AGENT=CLIENT"] : []));
    const client = !o.method && organizes && (!o.schedule || !!o.byPhone);
    for (const a of e.attendees ?? []) {
      const agent = o.method ? "" : client ? "CLIENT" : (a.agent ?? "");
      out.push(attendeeLine(a, agent ? ["SCHEDULE-AGENT=" + agent] : []));
    }
    if (e.seq !== undefined) out.push("SEQUENCE:" + e.seq);
    if (e.status === "CANCELLED" && e.organizer) out.push("STATUS:CANCELLED");
    for (const a of o.method ? [] : e.alarms) {
      out.push("BEGIN:VALARM", "ACTION:DISPLAY", "DESCRIPTION:" + escapeText(e.title || "Reminder"), "TRIGGER:" + formatDuration(-a), "END:VALARM");
    }
    out.push("END:VEVENT");
  }
  out.push("END:VCALENDAR");
  return out.map(fold).join("\r\n") + "\r\n";
}

/** The TZIDs a file names (to fetch their zones before parsing). */
export function zonesNamed(text: string): string[] {
  const out = new Set<string>();
  for (const m of text.slice(0, MAX_ICS_BYTES).matchAll(/TZID=("[^"]*"|[^;:\r\n]*)/g)) {
    const z = ianaZone(m[1].replace(/"/g, ""));
    if (z && z !== "UTC") out.add(z);
    if (out.size >= 16) break;
  }
  return [...out];
}

/** UTF-8 bytes (a binary string, as atob gives them) to text; bad sequences become U+FFFD. */
export function decodeUTF8(bin: string): string {
  let out = "";
  for (let i = 0; i < bin.length; ) {
    const b = bin.charCodeAt(i);
    let n = 0;
    let c = 0xfffd;
    if (b < 0x80) {
      c = b;
      n = 1;
    } else if (b >= 0xc2 && b < 0xe0) n = 2;
    else if (b >= 0xe0 && b < 0xf0) n = 3;
    else if (b >= 0xf0 && b < 0xf5) n = 4;
    if (n > 1) {
      let v = b & (0xff >> (n + 1));
      let ok = i + n <= bin.length;
      for (let k = 1; ok && k < n; k++) {
        const x = bin.charCodeAt(i + k);
        if ((x & 0xc0) !== 0x80) ok = false;
        v = (v << 6) | (x & 0x3f);
      }
      if (ok && !(n === 3 && v < 0x800) && !(n === 4 && (v < 0x10000 || v > 0x10ffff)) && !(v >= 0xd800 && v < 0xe000)) c = v;
      else n = 1;
    }
    if (n === 0) n = 1;
    out += String.fromCodePoint(c);
    i += n;
  }
  return out;
}

/** Text to UTF-8 bytes as a binary string (for btoa). */
export function encodeUTF8(s: string): string {
  let out = "";
  for (const ch of s) {
    const c = ch.codePointAt(0)!;
    if (c < 0x80) out += ch;
    else if (c < 0x800) out += String.fromCharCode(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
    else if (c < 0x10000) out += String.fromCharCode(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
    else out += String.fromCharCode(0xf0 | (c >> 18), 0x80 | ((c >> 12) & 0x3f), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
  }
  return out;
}

/** An event's rule (RRULE's value), its UNTIL read onto the event's clock. */
export function ruleOf(rrule: string, zone: string): Rule | undefined {
  if (!rrule) return undefined;
  return parseRule(rrule, (v) => {
    const t = parseTimeValue({ name: "UNTIL", params: new Map(), value: v, line: 0 });
    if (!t) return undefined;
    if (t.date) return { day: t.wall.day, min: 1439 };
    return t.zone === "UTC" && zone !== "UTC" ? onClock(t, zone) : t.wall;
  });
}

/** The UNTIL of a rule written on an event's clock, as RRULE wants it. */
export function untilText(w: Wall, allDay: boolean, zone: string): string {
  if (allDay) return fmtDate(w.day).replace(/-/g, "");
  if (!zone) return fmtWall(w).replace(/[-:]/g, "") + "00";
  const u = toWall(toInstant(w, zone), "UTC");
  return fmtWall(u).replace(/[-:]/g, "") + "00Z";
}
