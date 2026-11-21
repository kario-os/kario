// Invitations without e-mail (iTIP): what an invitation, an answer and a
// cancellation read as, the answer written back as a REPLY, and REPLY and
// CANCEL put into the calendar.
import { expectEqual, test } from "@kario/test";
import { setPhoneZone } from "../src/dates";
import { parseICS, serialiseICS } from "../src/ics";
import { answered, applyCancel, applyReply, invitees, occurrenceEvent, organizerICS, organizes, personOf, replyICS, sameAddress, whoAmI, withAttendee, withoutAttendee } from "../src/itip";
import type { CalEvent } from "../src/model";
import { Calendar } from "../src/store";

setPhoneZone("Asia/Shanghai");

let n = 0;
const id = () => "i" + ++n;

export const REQUEST = [
  "BEGIN:VCALENDAR",
  "PRODID:-//Example//Mail//EN",
  "VERSION:2.0",
  "METHOD:REQUEST",
  "BEGIN:VEVENT",
  "UID:team-sync@example.com",
  "SEQUENCE:2",
  "DTSTAMP:20261120T010000Z",
  "DTSTART;TZID=Asia/Shanghai:20301104T100000",
  "DTEND;TZID=Asia/Shanghai:20301104T110000",
  "RRULE:FREQ=WEEKLY;COUNT=4",
  "SUMMARY:team sync",
  'ORGANIZER;CN="Wang, Fang":mailto:fang@example.com',
  'ATTENDEE;CN="Wang, Fang";ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:fang@example.com',
  "ATTENDEE;CN=Li Lei;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:lei@example.com",
  "ATTENDEE;RSVP=TRUE:mailto:mei@example.com",
  "END:VEVENT",
  "END:VCALENDAR",
].join("\r\n");

test("an invitation: organizer, attendees and their answers, SEQUENCE; written back the same", () => {
  const r = parseICS(REQUEST, id);
  expectEqual([r.method, r.problems, r.events.length], ["REQUEST", [], 1], "read");
  const e = r.events[0];
  expectEqual(e.organizer, { addr: "mailto:fang@example.com", name: "Wang, Fang" }, "the organizer, the comma in a quoted name");
  expectEqual(e.attendees, [
    { addr: "mailto:fang@example.com", name: "Wang, Fang", partstat: "ACCEPTED", role: "CHAIR" },
    { addr: "mailto:lei@example.com", name: "Li Lei", partstat: "NEEDS-ACTION", role: "REQ-PARTICIPANT", rsvp: true },
    { addr: "mailto:mei@example.com", name: "", partstat: "NEEDS-ACTION", rsvp: true },
  ], "the attendees, NEEDS-ACTION by default");
  expectEqual(e.seq, 2, "SEQUENCE");
  const again = parseICS(serialiseICS(r.events, "20261120T000000Z"), id).events[0];
  expectEqual({ ...again, id: "" }, { ...e, id: "" }, "round trip");
  const cancelled = parseICS(REQUEST.replace("SEQUENCE:2", "SEQUENCE:3\r\nSTATUS:CANCELLED"), id).events[0];
  expectEqual(cancelled.status, "CANCELLED", "an invitation's cancellation is kept, marked");
});

test("who the user is, the answer, the REPLY", () => {
  const e = parseICS(REQUEST, id).events[0];
  expectEqual(whoAmI(e.attendees, []), undefined, "three attendees: ask");
  expectEqual(whoAmI(e.attendees, ["LEI@example.com"])?.name, "Li Lei", "an address answered as before, case and mailto: aside");
  expectEqual(whoAmI([e.attendees![1]], [])?.name, "Li Lei", "the only attendee");
  expectEqual(sameAddress("mailto:A@x", "a@X"), true, "addresses");
  const [ok] = answered([e], "mailto:lei@example.com", "ACCEPTED");
  expectEqual([ok.attendees![1].partstat, ok.attendees![1].rsvp, ok.attendees![2].partstat], ["ACCEPTED", undefined, "NEEDS-ACTION"], "only the user's PARTSTAT");
  const reply = replyICS([ok], ok.attendees![1], "ACCEPTED", "20261120T020000Z");
  for (const want of ["METHOD:REPLY", "UID:team-sync@example.com", "SEQUENCE:2", 'ORGANIZER;CN="Wang, Fang":mailto:fang@example.com', 'ATTENDEE;CN="Li Lei";PARTSTAT=ACCEPTED;ROLE=REQ-PARTICIPANT:mailto:lei@example.com']) {
    expectEqual(reply.includes(want), true, `${want} in\n${reply}`);
  }
  expectEqual(reply.includes("mei@example.com"), false, "only the user answers");
  const back = parseICS(reply, id);
  expectEqual([back.method, back.messages[0].uid, back.messages[0].attendees[0].partstat], ["REPLY", "team-sync@example.com", "ACCEPTED"], "the REPLY reads as one");
  const one = replyICS([{ ...ok, recurrenceId: "2030-11-11T10:00" }], ok.attendees![1], "DECLINED", "20261120T020000Z");
  expectEqual(one.includes("RECURRENCE-ID;TZID=Asia/Shanghai:20301111T100000"), true, "an answer for one occurrence");
});

test("a REPLY into the organizer's copy; a CANCEL of the event or of one occurrence", () => {
  const cal = new Calendar();
  const e = { ...parseICS(REQUEST, id).events[0], id: "m1" };
  cal.events = [e];
  const reply = (partstat: string, rid = "") =>
    parseICS(`BEGIN:VCALENDAR\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:team-sync@example.com\r\n${rid}ATTENDEE;PARTSTAT=${partstat}:mailto:MEI@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n`, id).messages[0];
  expectEqual(applyReply(cal, reply("TENTATIVE")), 1, "one answer");
  expectEqual(cal.get("m1")!.attendees![2].partstat, "TENTATIVE", "Mei is tentative");
  expectEqual(applyReply(cal, reply("TENTATIVE")), 0, "the same again changes nothing");
  expectEqual(applyReply(cal, { ...reply("ACCEPTED"), uid: "other" }), 0, "an event not here");
  const cancel = (rid = "") => parseICS(`BEGIN:VCALENDAR\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:team-sync@example.com\r\n${rid}SEQUENCE:3\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n`, id).messages[0];
  expectEqual(applyCancel(cal, cancel("RECURRENCE-ID;TZID=Asia/Shanghai:20301111T100000\r\n"), true), true, "one occurrence");
  expectEqual(cal.get("m1")!.exdates, ["2030-11-11T10:00"], "taken out with EXDATE");
  applyCancel(cal, cancel("RECURRENCE-ID:20301118T020000Z\r\n"), false);
  const marked = cal.events.find((x) => x.recurrenceId === "2030-11-18T10:00");
  expectEqual([marked?.status, marked?.start, marked?.end, marked?.uid], ["CANCELLED", "2030-11-18T10:00", "2030-11-18T11:00", e.uid], "kept, marked: an occurrence of its own (a UTC RECURRENCE-ID on the event's clock)");
  applyCancel(cal, cancel(), false);
  expectEqual(cal.events.every((x) => x.status === "CANCELLED"), true, "the whole event marked");
  applyCancel(cal, cancel(), true);
  expectEqual(cal.events.length, 0, "and removed");
});

test("the user's own invitation: attendees added (the user its organizer) and taken out; the REQUEST and the CANCELs", () => {
  const plain: CalEvent = { id: "e1", uid: "kick@kario", title: "kickoff", allDay: false, start: "2030-11-04T10:00", end: "2030-11-04T11:00", zone: "Asia/Shanghai",
    location: "", notes: "", alarms: [10], rrule: "FREQ=WEEKLY;COUNT=3", exdates: [], recurrenceId: "", src: "" };
  expectEqual([organizes(plain, []), invitees(plain)], [true, []], "an event without an organizer: the user may invite");
  expectEqual(personOf("Li Lei <LEI@example.com>"), { addr: "mailto:LEI@example.com", name: "Li Lei" }, "a name and an address");
  expectEqual(personOf("mailto:mei@example.com")?.addr, "mailto:mei@example.com", "an address");
  expectEqual([personOf("lei"), personOf("a b@x.y"), personOf("<x@y>")], [undefined, undefined, undefined], "no address");
  let e = withAttendee(plain, "me@kario.test", personOf("Li Lei <lei@example.com>")!);
  e = withAttendee(e, "me@kario.test", personOf("mei@example.com")!);
  expectEqual(e.organizer, { addr: "mailto:me@kario.test", name: "" }, "the user organizes it");
  expectEqual(e.attendees!.map((a) => `${a.addr} ${a.partstat} ${a.role}`), ["mailto:me@kario.test ACCEPTED CHAIR", "mailto:lei@example.com NEEDS-ACTION REQ-PARTICIPANT", "mailto:mei@example.com NEEDS-ACTION REQ-PARTICIPANT"], "the organizer an attendee who accepted");
  expectEqual([organizes(e, ["ME@kario.test"]), organizes(e, ["other@x"]), invitees(e).length], [true, false, 2], "whose it is");
  expectEqual(withAttendee(e, "me@kario.test", personOf("LEI@example.com")!).attendees!.length, 3, "once each");
  const out = withoutAttendee(e, "mailto:Mei@example.com");
  expectEqual(invitees(out).map((a) => a.addr), ["mailto:lei@example.com"], "taken out");
  const req = organizerICS("REQUEST", [{ ...out, seq: 1 }], "20301101T000000Z", {}).replace(/\r\n /g, "");
  for (const want of ["METHOD:REQUEST", "ORGANIZER:mailto:me@kario.test", "ATTENDEE;CN=\"Li Lei\";PARTSTAT=NEEDS-ACTION;ROLE=REQ-PARTICIPANT;RSVP=TRUE:mailto:lei@example.com", "SEQUENCE:1", "RRULE:FREQ=WEEKLY;COUNT=3"]) {
    expectEqual(req.includes(want), true, `${want} in\n${req}`);
  }
  expectEqual(req.includes("VALARM"), false, "no reminders in an invitation");
  const toMei = organizerICS("CANCEL", [out], "20301101T000000Z", {}, [e.attendees![2]]).replace(/\r\n /g, "");
  expectEqual([toMei.includes("METHOD:CANCEL"), toMei.includes("STATUS:CANCELLED"), toMei.includes("mei@example.com"), toMei.includes("lei@example.com"), toMei.includes("ATTENDEE;PARTSTAT=ACCEPTED;ROLE=CHAIR:mailto:me@kario.test")], [true, true, true, false, true], toMei);
  const back = parseICS(toMei, id);
  expectEqual([back.method, back.messages[0].uid, back.messages[0].attendees.map((a) => a.addr)], ["CANCEL", "kick@kario", ["mailto:me@kario.test", "mailto:mei@example.com"]], "reads as a cancellation");
  const one = occurrenceEvent(e, undefined, "2030-11-11T10:00");
  expectEqual([one.recurrenceId, one.start, one.end, one.rrule], ["2030-11-11T10:00", "2030-11-11T10:00", "2030-11-11T11:00", ""], "one occurrence as its own event");
  expectEqual(organizerICS("CANCEL", [one], "20301101T000000Z", {}).includes("RECURRENCE-ID;TZID=Asia/Shanghai:20301111T100000"), true, "a CANCEL of one occurrence");
});
