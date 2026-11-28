// Invitations without e-mail (iTIP, RFC 5546): an invitation (METHOD:REQUEST)
// comes as a file, from Files or shared from another app; the user answers
// it (accept, tentative, decline), the answer goes into the event's
// PARTSTAT, and a METHOD:REPLY file goes back the way the user chooses
// (the system's share sheet). A REPLY from an attendee puts the answer
// into the organizer's copy; a CANCEL takes the event (or one occurrence)
// out, or marks it cancelled. Who of the attendees the user is: the one
// address they have answered as before, the only attendee, or the one they
// pick. The user organizes an event of their own: they add and take out
// attendees, and a REQUEST (the event as it is now) or a CANCEL (for those
// taken out, or for a deleted event or occurrence) goes the same way.
// Where the account's server schedules (RFC 6638, serverSchedules), it
// does all that itself from the events the sync hands it: no files, the
// user is the account's address, the attendees' answers and the server's
// delivery status come back with the event.
import { attendeeLine, type Message, occurrenceOf, organizerLine, sameAddress, serialiseICS } from "./ics";
import { type Attendee, clip, MAX_NAME, type CalEvent, type Person } from "./model";
import { type Calendar, shiftKey, span } from "./store";
import type { VZone } from "./vtz";

export type Answer = "ACCEPTED" | "TENTATIVE" | "DECLINED";

export { sameAddress };

/** Which attendee the user is: one of mine (addresses answered as before), else the only one; undefined to ask. */
export function whoAmI(attendees: Attendee[] | undefined, mine: string[]): Attendee | undefined {
  const list = attendees ?? [];
  return list.find((a) => mine.some((m) => sameAddress(m, a.addr))) ?? (list.length === 1 ? list[0] : undefined);
}

/** The events of an invitation with the user's answer in their PARTSTAT (and RSVP answered). */
export function answered(events: CalEvent[], me: string, answer: Answer): CalEvent[] {
  return events.map((e) => ({
    ...e,
    attendees: (e.attendees ?? []).map((a) => (sameAddress(a.addr, me) ? { ...a, partstat: answer, rsvp: undefined } : a)),
  }));
}

/** The METHOD:REPLY of an answer to an invitation's events (stamp: DTSTAMP). */
export function replyICS(events: CalEvent[], me: Attendee, answer: Answer, stamp: string): string {
  const out = ["BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Kario//Calendar//EN", "METHOD:REPLY"];
  for (const e of events) {
    out.push("BEGIN:VEVENT", "UID:" + e.uid, "DTSTAMP:" + stamp);
    if (e.recurrenceId) out.push(occurrenceLine(e));
    if (e.organizer) out.push(organizerLine(e.organizer));
    out.push(attendeeLine({ ...me, partstat: answer, rsvp: undefined }));
    out.push("SEQUENCE:" + (e.seq ?? 0));
    if (e.title) out.push("SUMMARY:" + e.title.replace(/[\\;,]/g, (c) => "\\" + c).replace(/\r?\n/g, "\\n"));
    out.push("END:VEVENT");
  }
  out.push("END:VCALENDAR");
  return out.join("\r\n") + "\r\n";
}

// An event's RECURRENCE-ID as a line, on its clock.
function occurrenceLine(e: CalEvent): string {
  const v = e.recurrenceId.replace(/[-:]/g, "");
  if (e.allDay) return `RECURRENCE-ID;VALUE=DATE:${v}`;
  if (!e.zone) return `RECURRENCE-ID:${v}00`;
  if (e.zone === "UTC") return `RECURRENCE-ID:${v}00Z`;
  return `RECURRENCE-ID;TZID=${/[;:,]/.test(e.zone) ? `"${e.zone}"` : e.zone}:${v}00`;
}

/** The events a message (REPLY, CANCEL) is about: the whole event's, or the one occurrence's (its override, or the repeating event). */
export function target(cal: Calendar, m: Message): { master?: CalEvent; override?: CalEvent; key: string } {
  const group = cal.group(m.uid);
  const master = group.find((e) => !e.recurrenceId);
  const any = master ?? group[0];
  const key = any ? occurrenceOf(m, any) : "";
  return { master, override: key ? group.find((e) => e.recurrenceId === key) : undefined, key };
}

/** Puts a REPLY's answers into the organizer's copy; how many attendees' answers changed. */
export function applyReply(cal: Calendar, m: Message): number {
  const t = target(cal, m);
  const e = t.key ? (t.override ?? t.master) : t.master;
  if (!e?.attendees) return 0;
  let n = 0;
  const list = e.attendees.map((a) => {
    const r = m.attendees.find((x) => sameAddress(x.addr, a.addr));
    if (!r || r.partstat === a.partstat) return a;
    n++;
    return { ...a, partstat: r.partstat, rsvp: undefined };
  });
  if (n) cal.put({ ...e, attendees: list });
  return n;
}

/** Applies a CANCEL: the event (or the occurrence) taken out, or kept marked cancelled; whether there was one. */
export function applyCancel(cal: Calendar, m: Message, remove: boolean): boolean {
  const t = target(cal, m);
  if (!cal.group(m.uid).length) return false;
  if (!t.key) {
    for (const e of cal.group(m.uid)) {
      if (remove) cal.remove(e.id);
      else cal.put({ ...e, status: "CANCELLED" });
    }
    return true;
  }
  if (t.override) {
    if (remove) cal.remove(t.override.id);
    else cal.put({ ...t.override, status: "CANCELLED" });
  }
  if (t.master && (remove || !t.override)) {
    if (remove && !t.master.exdates.includes(t.key)) cal.put({ ...t.master, exdates: [...t.master.exdates, t.key] });
    if (!remove) {
      const end = shiftKey(t.key, t.master.allDay, span(t.master)?.minutes ?? 0);
      cal.put({ ...t.master, id: cal.newId(), uid: t.master.uid, rrule: "", exdates: [], recurrenceId: t.key, start: t.key, end, status: "CANCELLED" });
    }
  }
  return true;
}

/** Whether the user organizes an event (one of their addresses is its organizer), or may start to (it has none). */
export function organizes(e: CalEvent, mine: string[]): boolean {
  return !e.organizer || mine.some((m) => sameAddress(m, e.organizer!.addr));
}

/** An event's attendees other than its organizer. */
export function invitees(e: CalEvent): Attendee[] {
  return (e.attendees ?? []).filter((a) => !e.organizer || !sameAddress(a.addr, e.organizer.addr));
}

/** A person as typed: "Name <a@b.c>" or "a@b.c", as a mailto: address; undefined for no e-mail address. */
export function personOf(text: string): Person | undefined {
  const m = /^\s*(?:(.*?)\s*<\s*([^<>\s]+)\s*>|([^<>\s]+))\s*$/.exec(text);
  const addr = (m?.[2] ?? m?.[3] ?? "").replace(/^mailto:/i, "");
  if (!/^[^\s@"]+@[^\s@"]+\.[^\s@"]+$/.test(addr)) return undefined;
  return { addr: "mailto:" + addr, name: clip((m?.[1] ?? "").replace(/"/g, "").trim(), MAX_NAME) };
}

/** The event with one more attendee (who): the user (me, an address) its organizer, and an attendee who accepted, as calendars write it; a new one marked as the others are when the phone sends them the invitation itself. */
export function withAttendee(e: CalEvent, me: string, who: Person): CalEvent {
  const organizer = e.organizer ?? { addr: "mailto:" + me.replace(/^mailto:/i, ""), name: "" };
  const agent = byFile(e) ? { agent: "CLIENT" } : {};
  let list = e.attendees ?? [];
  if (!list.some((a) => sameAddress(a.addr, organizer.addr))) list = [{ ...organizer, partstat: "ACCEPTED", role: "CHAIR", ...agent }, ...list];
  if (!list.some((a) => sameAddress(a.addr, who.addr))) list = [...list, { ...who, partstat: "NEEDS-ACTION", role: "REQ-PARTICIPANT", rsvp: true, ...agent }];
  return { ...e, organizer, attendees: list };
}

/** Whether the phone sends an event's invitations itself (files): an attendee is marked SCHEDULE-AGENT=CLIENT, as the account's server stored them. */
export function byFile(e: CalEvent): boolean {
  return (e.attendees ?? []).some((a) => a.agent === "CLIENT");
}

/**
 * Whether the account's server schedules an event (RFC 6638): the account
 * schedules (schedule), and the event is the user's own and not one whose
 * invitations went as files, or an invitation that came from the account.
 */
export function serverSchedules(e: CalEvent, schedule: boolean, mine: string[]): boolean {
  if (!schedule || !e.organizer) return false;
  return organizes(e, mine) ? !byFile(e) : e.src.startsWith("account:");
}

/** The event without the attendee of addr. */
export function withoutAttendee(e: CalEvent, addr: string): CalEvent {
  return { ...e, attendees: (e.attendees ?? []).filter((a) => !sameAddress(a.addr, addr)) };
}

/** An organizer's message (stamp: DTSTAMP): a REQUEST of the events as they are, or a CANCEL of them (only to: the attendees it goes to, the organizer kept). */
export function organizerICS(method: "REQUEST" | "CANCEL", events: CalEvent[], stamp: string, zones: Record<string, VZone>, to?: Attendee[]): string {
  const evs = events.map((e) => {
    const out: CalEvent = method === "CANCEL" ? { ...e, status: "CANCELLED" } : e;
    if (to) out.attendees = [...(e.attendees ?? []).filter((a) => e.organizer && sameAddress(a.addr, e.organizer.addr)), ...to];
    return out;
  });
  return serialiseICS(evs, stamp, zones, { method });
}

/** One occurrence (key, on its clock) of a repeating event as its own event: its override, or the master moved there. */
export function occurrenceEvent(master: CalEvent, override: CalEvent | undefined, key: string): CalEvent {
  if (override) return override;
  const end = shiftKey(key, master.allDay, span(master)?.minutes ?? 0);
  return { ...master, rrule: "", rdates: undefined, exdates: [], recurrenceId: key, start: key, end };
}
