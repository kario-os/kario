// The calendar's events: kept in the app's storage (the runtime writes a
// new file and renames it over the old, so a crash leaves the last whole
// save), bounded in number and size, and expanded into occurrences on the
// phone's clock for the views and the reminders.
import { storage } from "kario";
import { addMinutes, type Day, fmtDate, fmtWall, parseDate, parseWall, setZoneSpans, toInstant, toWall, type Wall } from "./dates";
import { ruleOf, untilText } from "./ics";
import { clip, MAX_EVENTS, MAX_TITLE, type CalEvent } from "./model";
import { expand, formatRule, MAX_COUNT } from "./recur";
import { ownZone, type VZone, vzoneSpans } from "./vtz";

/** The most the events may take in storage (its limit is 1 MB in all). */
export const MAX_STORED = 900_000;

const KEY = "events";
const SEQ = "seq";
const ZONES = "zones";

/** One occurrence of an event, on the phone's clock. */
export interface Occ {
  ev: CalEvent;
  /** Its start on the event's own clock, as EXDATE and RECURRENCE-ID name it. */
  key: string;
  /** Its start and end on the phone's clock (an all-day one: its days, end the day after its last). */
  start: Wall;
  end: Wall;
  /** The instant it starts (an all-day one: its first midnight on the phone's clock). */
  ms: number;
}

/** An event's start and end on its clock, and its length in minutes. */
export function span(e: CalEvent): { start: Wall; end: Wall; minutes: number } | undefined {
  const s = e.allDay ? dayWall(e.start) : parseWall(e.start);
  const t = e.allDay ? dayWall(e.end) : parseWall(e.end);
  if (!s || !t) return undefined;
  return { start: s, end: t, minutes: Math.max(0, (t.day - s.day) * 1440 + t.min - s.min) };
}

function dayWall(s: string): Wall | undefined {
  const d = parseDate(s);
  return d === undefined ? undefined : { day: d, min: 0 };
}

function wallKey(w: Wall): number {
  return w.day * 1440 + w.min;
}

/** An event's RDATE starts on its clock. */
export function rdateWalls(e: CalEvent): Wall[] {
  if (e.recurrenceId) return [];
  return (e.rdates ?? []).map((x) => (e.allDay ? dayWall(x) : parseWall(x))).filter((w): w is Wall => !!w);
}

/** Whether an event has more than one occurrence (RRULE or RDATE): not a changed occurrence. */
export function repeats(e: CalEvent): boolean {
  return !e.recurrenceId && (!!e.rrule || !!e.rdates?.length);
}

/** The key of a start as written (the event's start format). */
export function startKey(w: Wall, allDay: boolean): string {
  return allDay ? fmtDate(w.day) : fmtWall(w);
}

/** A start key as minutes (day * 1440 + minute). */
export function keyMinutes(key: string, allDay: boolean): number | undefined {
  const w = allDay ? dayWall(key) : parseWall(key);
  return w && wallKey(w);
}

/** A start key moved by minutes (an all-day one by whole days). */
export function shiftKey(key: string, allDay: boolean, minutes: number): string {
  const w = allDay ? dayWall(key) : parseWall(key);
  return w ? startKey(addMinutes(w, allDay ? Math.round(minutes / 1440) * 1440 : minutes), allDay) : key;
}

/** A new event's UID. */
export function newUid(id: string): string {
  return `${id}-${Date.now().toString(36)}@kario`;
}

export class Calendar {
  events: CalEvent[] = [];
  /** Events shown but not kept, synced or exported: the contacts' birthdays and anniversaries (birthdays.ts). */
  virtual: CalEvent[] = [];
  /** The zones of the calendar's own (from files' VTIMEZONEs) the events are on, by key. */
  zones: Record<string, VZone> = {};
  private seq = 0;
  private touched = new Set<string>();

  load(): void {
    this.touched.clear();
    const list = storage.get<CalEvent[]>(KEY);
    this.events = Array.isArray(list) ? list.filter((e) => e && typeof e.id === "string" && span(e)) : [];
    this.seq = Number(storage.get<number>(SEQ)) || this.events.length;
    const z = storage.get<Record<string, VZone>>(ZONES);
    this.zones = z && typeof z === "object" ? z : {};
    for (const [k, v] of Object.entries(this.zones)) if (Array.isArray(v?.obs)) setZoneSpans(k, vzoneSpans(v));
  }

  /** Keeps a file's zones (those the calendar lacks). */
  addZones(zones: Record<string, VZone>): void {
    for (const [k, v] of Object.entries(zones)) this.zones[k] ??= v;
  }

  /** A zone's name as the user reads it: a zone of the calendar's own by its TZID. */
  zoneName(zone: string): string {
    return (ownZone(zone) && this.zones[zone]?.tzid) || zone;
  }

  /** Saves the events (and the zones of their own they are on); an error's text when they do not fit. */
  save(): string | undefined {
    const used = new Set(this.events.map((e) => e.zone));
    for (const k of Object.keys(this.zones)) if (!used.has(k)) delete this.zones[k];
    const text = JSON.stringify(this.events);
    if (text.length + JSON.stringify(this.zones).length > MAX_STORED) return "full";
    try {
      storage.set(KEY, this.events);
      storage.set(SEQ, this.seq);
      storage.set(ZONES, this.zones);
    } catch (e) {
      return e instanceof Error ? e.message : String(e);
    }
    return undefined;
  }

  newId(): string {
    return "e" + ++this.seq;
  }

  get(id: string): CalEvent | undefined {
    return this.events.find((e) => e.id === id) ?? this.virtual.find((e) => e.id === id);
  }

  /** Room for n more events. */
  room(n = 1): boolean {
    return this.events.length + n <= MAX_EVENTS;
  }

  put(e: CalEvent): void {
    const i = this.events.findIndex((x) => x.id === e.id);
    if (!e.uid) e.uid = newUid(e.id);
    e.title = clip(e.title, MAX_TITLE);
    if (i >= 0) {
      if (this.events[i].uid !== e.uid) this.touched.add(this.events[i].uid);
      this.events[i] = e;
    } else this.events.push(e);
    this.touched.add(e.uid);
  }

  /** Takes an event out; a repeating one with the occurrences it changed. */
  remove(id: string): void {
    const e = this.get(id);
    if (!e) return;
    this.events = this.events.filter((x) => x.id !== id && !(repeats(e) && x.uid === e.uid && x.recurrenceId));
    this.touched.add(e.uid);
  }

  /** The UIDs of the events changed on the phone since the last look (for the account's sync). */
  takeTouched(): string[] {
    const out = [...this.touched];
    this.touched.clear();
    return out;
  }

  /** An event with the occurrences it changed (one UID), the repeating one first. */
  group(uid: string): CalEvent[] {
    return this.events.filter((e) => e.uid === uid).sort((a, b) => Number(!!a.recurrenceId) - Number(!!b.recurrenceId) || a.recurrenceId.localeCompare(b.recurrenceId));
  }

  /** The UIDs of all events. */
  uids(): string[] {
    return [...new Set(this.events.map((e) => e.uid))];
  }

  /**
   * Puts the server's version of an event (list: it and its changed
   * occurrences, one UID) in place of the phone's, keeping the phone's IDs
   * for the same occurrences; a UID new to the phone is marked src. Not a
   * change on the phone (nothing goes back). False when it does not fit
   * (bytes: what the events take in storage now, kept up to date).
   */
  replaceUID(uid: string, list: CalEvent[], src: string, bytes: { n: number }): boolean {
    const old = this.events.filter((e) => e.uid === uid);
    const fresh = list.filter((e) => e.uid === uid);
    const size = (l: CalEvent[]) => JSON.stringify(l).length + l.length;
    const delta = size(fresh) - (old.length ? size(old) : 0);
    if (this.events.length - old.length + fresh.length > MAX_EVENTS || bytes.n + delta > MAX_STORED - 20_000) return false;
    const keep = old[0]?.src ?? src;
    const made = fresh.map((e) => ({ ...e, id: old.find((x) => x.recurrenceId === e.recurrenceId)?.id ?? this.newId(), src: keep }));
    this.events = [...this.events.filter((e) => e.uid !== uid), ...made];
    bytes.n += delta;
    return true;
  }

  /** Takes out the events of a UID the server deleted (not a change on the phone); whether there were any. */
  dropUID(uid: string): boolean {
    const n = this.events.length;
    this.events = this.events.filter((e) => e.uid !== uid);
    return this.events.length < n;
  }

  /** What the events take in storage. */
  bytes(): number {
    return JSON.stringify(this.events).length + JSON.stringify(this.zones).length;
  }

  /** The occurrence that replaces one of a repeating event's (RECURRENCE-ID), if any. */
  override(master: CalEvent, key: string): CalEvent | undefined {
    return this.events.find((x) => x.uid === master.uid && x.recurrenceId === key && x !== master);
  }

  /** The repeating event an override belongs to. */
  master(e: CalEvent): CalEvent | undefined {
    if (!e.recurrenceId) return undefined;
    return this.events.find((x) => x.uid === e.uid && repeats(x));
  }

  /**
   * A repeating event split at one of its occurrences (key, on its clock),
   * for "this and following": old is the event ending before it (UNTIL a
   * minute before, or the day before for an all-day one; a COUNT becomes
   * that UNTIL), rest the occurrences from it on as a new event with a new
   * ID and UID starting there (what COUNT has left), with the EXDATEs from
   * key on; moved are the changed occurrences from key on, now rest's.
   * Nothing is changed yet; rest's ID is id ("" to only look). Undefined
   * when key is the first occurrence (that is the whole series) or not one
   * the rule makes (an RDATE's: the rule would start over from it). The
   * RDATEs after key go with rest.
   */
  split(master: CalEvent, key: string, id = ""): { old: CalEvent; rest: CalEvent; moved: CalEvent[] } | undefined {
    const sp = span(master);
    const rule = master.recurrenceId ? undefined : ruleOf(master.rrule, master.zone);
    const k = master.allDay ? dayWall(key) : parseWall(key);
    if (!sp || !rule || !k || wallKey(k) <= wallKey(sp.start)) return undefined;
    if (!expand(sp.start, rule, k.day, k.day, new Set(), 50).some((w) => wallKey(w) === wallKey(k))) return undefined;
    const before = expand(sp.start, rule, sp.start.day, k.day, new Set(), MAX_COUNT + 1).filter((w) => wallKey(w) < wallKey(k)).length;
    const fmt = (w: Wall) => untilText(w, master.allDay, master.zone);
    const until = master.allDay ? { day: k.day - 1, min: 1439 } : addMinutes(k, -1);
    const old: CalEvent = { ...master, rrule: formatRule({ ...rule, count: undefined, until }, fmt), exdates: master.exdates.filter((x) => x < key) };
    const rdates = master.rdates ?? [];
    if (master.rdates) old.rdates = rdates.filter((x) => x < key);
    const rest: CalEvent = {
      ...master,
      id,
      uid: id && newUid(id),
      start: startKey(k, master.allDay),
      end: startKey(addMinutes(k, sp.minutes), master.allDay),
      rrule: formatRule({ ...rule, count: rule.count ? Math.max(1, rule.count - before) : undefined }, fmt),
      exdates: master.exdates.filter((x) => x >= key),
    };
    if (master.rdates) rest.rdates = rdates.filter((x) => x > key);
    const moved = this.events.filter((x) => x.uid === master.uid && x.recurrenceId && x.recurrenceId >= key).map((x) => ({ ...x, uid: rest.uid }));
    return { old, rest, moved };
  }

  /** The occurrences that touch the phone's days from..to, in order (all-day ones first on a day). */
  occurrences(from: Day, to: Day): Occ[] {
    const out: Occ[] = [];
    const overridden = new Map<string, Set<number>>();
    for (const e of this.events) {
      if (!e.recurrenceId) continue;
      const w = e.allDay ? dayWall(e.recurrenceId) : parseWall(e.recurrenceId);
      if (!w) continue;
      if (!overridden.has(e.uid)) overridden.set(e.uid, new Set());
      overridden.get(e.uid)!.add(wallKey(w));
    }
    for (const e of [...this.events, ...this.virtual]) {
      const sp = span(e);
      if (!sp) continue;
      const rule = e.recurrenceId ? undefined : ruleOf(e.rrule, e.zone);
      const excluded = new Set<number>();
      for (const x of e.exdates) {
        const w = e.allDay ? dayWall(x) : parseWall(x);
        if (w) excluded.add(wallKey(w));
      }
      if (repeats(e)) for (const k of overridden.get(e.uid) ?? []) excluded.add(k);
      const days = Math.ceil(sp.minutes / 1440);
      for (const w of expand(sp.start, rule, from - days - 1, to + 1, excluded, 400, rdateWalls(e))) {
        const occ = this.occ(e, w, sp.minutes);
        if (occ.start.day <= to && (wallKey(occ.end) > from * 1440 || occ.start.day >= from)) out.push(occ);
      }
    }
    return out.sort((a, b) => a.start.day - b.start.day || Number(!a.ev.allDay) - Number(!b.ev.allDay) || a.ms - b.ms || a.ev.title.localeCompare(b.ev.title));
  }

  private occ(e: CalEvent, w: Wall, minutes: number): Occ {
    const key = startKey(w, e.allDay);
    if (e.allDay) {
      return { ev: e, key, start: w, end: { day: w.day + Math.max(1, Math.ceil(minutes / 1440)), min: 0 }, ms: toInstant(w, "") };
    }
    const ms = toInstant(w, e.zone);
    return { ev: e, key, start: toWall(ms, ""), end: toWall(ms + minutes * 60000, ""), ms };
  }

  /** The occurrences on one of the phone's days. */
  onDay(d: Day): Occ[] {
    return this.occurrences(d, d).filter((o) => o.start.day <= d && (o.end.day > d || (o.end.day === d && o.end.min > 0) || (o.start.day === d && !o.ev.allDay)));
  }

  /** The occurrence of e starting at key (on its clock), if the rule has it. */
  occAt(e: CalEvent, key: string): Occ | undefined {
    const sp = span(e);
    const w = e.allDay ? dayWall(key) : parseWall(key);
    if (!sp || !w) return undefined;
    if (!repeats(e)) return wallKey(w) === wallKey(sp.start) ? this.occ(e, sp.start, sp.minutes) : undefined;
    const hit = expand(sp.start, ruleOf(e.rrule, e.zone), w.day, w.day, new Set(), 50, rdateWalls(e)).find((x) => wallKey(x) === wallKey(w));
    return hit && this.occ(e, hit, sp.minutes);
  }

  /** The next occurrence of e from day d on (else its last before). */
  next(e: CalEvent, d: Day): Occ | undefined {
    const sp = span(e);
    if (!sp) return undefined;
    const rule = e.recurrenceId ? undefined : ruleOf(e.rrule, e.zone);
    const later = expand(sp.start, rule, d, d + 366 * 5, new Set(e.exdates.map((x) => (e.allDay ? dayWall(x) : parseWall(x))).filter((w): w is Wall => !!w).map(wallKey)), 1, rdateWalls(e));
    if (later.length) return this.occ(e, later[0], sp.minutes);
    return this.occ(e, sp.start, sp.minutes);
  }

  /** Events whose title, place or notes hold q (case aside), the soonest next occurrence first. */
  search(q: string, d: Day): Occ[] {
    const s = q.trim().toLowerCase();
    if (!s) return [];
    const out: Occ[] = [];
    for (const e of [...this.events, ...this.virtual]) {
      if (![e.title, e.location, e.notes].some((t) => t.toLowerCase().includes(s))) continue;
      const o = this.next(e, d);
      if (o) out.push(o);
      if (out.length >= 50) break;
    }
    return out.sort((a, b) => Number(a.start.day < d) - Number(b.start.day < d) || a.ms - b.ms);
  }

  /** Adds what the calendar lacks (by UID and the occurrence it replaces); src marks them. */
  merge(list: CalEvent[], src: string): { added: number; skipped: number; full: boolean } {
    let added = 0;
    let skipped = 0;
    for (const e of list) {
      const dup = this.events.some((x) =>
        e.uid ? x.uid === e.uid && x.recurrenceId === e.recurrenceId : !x.uid && x.title === e.title && x.start === e.start && x.end === e.end,
      );
      if (dup) {
        skipped++;
        continue;
      }
      if (!this.room()) return { added, skipped, full: true };
      this.put({ ...e, id: this.newId(), src });
      added++;
    }
    return { added, skipped, full: false };
  }

  /** Takes out what the system imported from the file src; how many. */
  unmerge(src: string): number {
    const before = this.events.length;
    for (const e of this.events) if (e.src === src) this.touched.add(e.uid);
    this.events = this.events.filter((e) => e.src !== src);
    return before - this.events.length;
  }
}

/** A reminder as notify.schedule takes it, with its instant. */
export interface Reminder {
  id: string;
  at: string;
  zone: string;
  title: string;
  body: string;
  arg: string;
  ms: number;
}

// FNV-1a: a short, stable ID for a reminder (an event's UID may be long).
function hash(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(16).padStart(8, "0");
}

/**
 * The reminders due from now over the next days, the soonest first, at
 * most max (the system keeps 64 an app's); body says what the
 * notification shows under the title.
 */
export function reminders(cal: Calendar, now: number, days: number, max: number, body: (o: Occ) => string): Reminder[] {
  const d0 = toWall(now, "").day;
  const out: Reminder[] = [];
  for (const o of cal.occurrences(d0 - 2, d0 + days)) {
    for (const a of o.ev.alarms) {
      const w = o.ev.allDay ? o.start : parseWall(o.key);
      if (!w) continue;
      const zone = o.ev.allDay ? "" : o.ev.zone;
      const at = addMinutes(w, -a);
      const ms = toInstant(at, zone);
      if (ms <= now) continue;
      // The system knows IANA zones only: a zone of the calendar's own goes as UTC.
      const own = ownZone(zone);
      out.push({
        id: "r" + hash(`${o.ev.id}|${o.key}|${a}`),
        at: fmtWall(own ? toWall(ms, "UTC") : at),
        zone: own ? "UTC" : zone,
        title: o.ev.title,
        body: body(o),
        arg: `event:${o.ev.id}:${o.key}`,
        ms,
      });
    }
  }
  return out.sort((a, b) => a.ms - b.ms).slice(0, max);
}
