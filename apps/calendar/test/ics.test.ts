// iCalendar: what other calendars write, round trips, and input that is
// broken, hostile or huge (never a throw; what is left out says why).
import { expectEqual, test } from "@kario/test";
import { setPhoneZone, setZoneSpans } from "../src/dates";
import { decodeUTF8, encodeUTF8, formatDuration, ianaZone, parseDuration, parseICS, serialiseICS, zonesNamed } from "../src/ics";
import type { CalEvent } from "../src/model";

setPhoneZone("Asia/Shanghai");
setZoneSpans("America/New_York", [
  { start: Date.UTC(2025, 10, 2, 6) / 1000, offset: -5 * 3600 },
  { start: Date.UTC(2026, 2, 8, 7) / 1000, offset: -4 * 3600 },
  { start: Date.UTC(2026, 10, 1, 6) / 1000, offset: -5 * 3600 },
]);

let n = 0;
const id = () => "t" + ++n;

const GOOGLE = [
  "BEGIN:VCALENDAR",
  "PRODID:-//Google Inc//Google Calendar 70.9054//EN",
  "VERSION:2.0",
  "BEGIN:VTIMEZONE",
  "TZID:America/New_York",
  "BEGIN:DAYLIGHT",
  "DTSTART:19700308T020000",
  "END:DAYLIGHT",
  "END:VTIMEZONE",
  "BEGIN:VEVENT",
  "DTSTART;TZID=America/New_York:20261124T090000",
  "DTEND;TZID=America/New_York:20261124T100000",
  "RRULE:FREQ=WEEKLY;BYDAY=TU;UNTIL=20261222T140000Z",
  "EXDATE;TZID=America/New_York:20261208T090000",
  "UID:abc123@google.com",
  "SUMMARY:Stand-up\\, team A",
  "LOCATION:Room 3\\; floor 2",
  "DESCRIPTION:Line one\\nLine two with a long text that goes on and on so that it is fold",
  " ed over a line",
  "BEGIN:VALARM",
  "ACTION:DISPLAY",
  "TRIGGER:-PT15M",
  "END:VALARM",
  "BEGIN:VALARM",
  "ACTION:EMAIL",
  "TRIGGER:-P1D",
  "END:VALARM",
  "END:VEVENT",
  "BEGIN:VEVENT",
  "DTSTART;VALUE=DATE:20261001",
  "DTEND;VALUE=DATE:20261008",
  "SUMMARY:\u56fd\u5e86\u8282",
  "UID:holiday@example",
  "BEGIN:VALARM",
  "ACTION:DISPLAY",
  "TRIGGER;RELATED=START:-PT15H",
  "END:VALARM",
  "END:VEVENT",
  "BEGIN:VEVENT",
  "DTSTART:20261117T013000Z",
  "DURATION:PT1H30M",
  "SUMMARY:UTC call",
  "END:VEVENT",
  "BEGIN:VEVENT",
  "RECURRENCE-ID;TZID=America/New_York:20261201T090000",
  "DTSTART;TZID=America/New_York:20261201T110000",
  "UID:abc123@google.com",
  "SUMMARY:Stand-up moved",
  "END:VEVENT",
  "END:VCALENDAR",
].join("\r\n");

test("what Google writes: zones, dates, UTC, DURATION, RRULE, EXDATE, VALARM, escapes, folding", () => {
  const r = parseICS(GOOGLE, id);
  expectEqual(r.problems, [], "problems");
  expectEqual(r.events.length, 4, "events");
  const [a, b, c] = r.events;
  expectEqual([a.title, a.location, a.notes], ["Stand-up, team A", "Room 3; floor 2", "Line one\nLine two with a long text that goes on and on so that it is folded over a line"], "texts");
  expectEqual([a.start, a.end, a.zone, a.allDay], ["2026-11-24T09:00", "2026-11-24T10:00", "America/New_York", false], "zoned");
  expectEqual([a.rrule, a.exdates, a.alarms], ["FREQ=WEEKLY;BYDAY=TU;UNTIL=20261222T140000Z", ["2026-12-08T09:00"], [15]], "rule, exdate, the display alarm only");
  expectEqual([b.start, b.end, b.allDay, b.alarms], ["2026-10-01", "2026-10-08", true, [900]], "all day: 09:00 the day before");
  expectEqual([c.start, c.end, c.zone], ["2026-11-17T01:30", "2026-11-17T03:00", "UTC"], "UTC with a duration");
  expectEqual([r.events[3].recurrenceId, r.events[3].start], ["2026-12-01T09:00", "2026-12-01T11:00"], "a changed occurrence");
});

test("round trip: what the calendar writes it reads back the same", () => {
  const first = parseICS(GOOGLE, id).events;
  const text = serialiseICS(first, "20261117T000000Z");
  for (const line of text.split("\r\n")) if (encodeUTF8(line).length > 75) throw new Error("a line over 75 octets: " + line);
  const again = parseICS(text, id);
  expectEqual(again.problems, [], "problems");
  const strip = (e: CalEvent) => ({ ...e, id: "" });
  // An event without a UID gets one written (RFC 5545 wants it): the calendar's ID.
  expectEqual(again.events.map((e, i) => ({ ...strip(e), uid: first[i].uid ? e.uid : "" })), first.map(strip), "the same events");
  expectEqual(again.events[2].uid, first[2].id, "the UID written for one without");
  const floating: CalEvent = { id: "x", uid: "f@x", title: "\u65e9\u4e0a 8 \u70b9", allDay: false, start: "2026-11-17T08:00", end: "2026-11-17T08:30", zone: "", location: "", notes: "", alarms: [0, -10], rrule: "FREQ=DAILY", exdates: [], recurrenceId: "", src: "" };
  const back = parseICS(serialiseICS([floating], "20261117T000000Z"), id).events[0];
  expectEqual(strip(back), strip(floating), "floating time, at the start and after it");
});

test("other calendars' zones", () => {
  expectEqual(ianaZone("/mozilla.org/20050126_1/Asia/Shanghai"), "Asia/Shanghai", "Thunderbird");
  expectEqual(ianaZone("China Standard Time"), "Asia/Shanghai", "Outlook");
  expectEqual(ianaZone("America/Argentina/Buenos_Aires"), "America/Argentina/Buenos_Aires", "three parts");
  expectEqual(ianaZone("Mars/Olympus"), "", "unknown: floating");
  expectEqual(zonesNamed('DTSTART;TZID="America/New_York":1\nX;TZID=Asia/Tokyo:2\nDTSTART;TZID=UTC:3'), ["America/New_York", "Asia/Tokyo"], "zones to fetch");
});

test("durations", () => {
  expectEqual(parseDuration("PT1H30M"), 90, "PT1H30M");
  expectEqual(parseDuration("-P1DT9H"), -1980, "-P1DT9H");
  expectEqual(parseDuration("P2W"), 20160, "P2W");
  for (const bad of ["P", "PT", "1H", "P1H", "PT1X", ""]) expectEqual(parseDuration(bad), undefined, JSON.stringify(bad));
  for (const m of [0, 15, -540, 1440, 2880 + 75]) expectEqual(parseDuration(formatDuration(m)), m, `${m} minutes`);
});

test("broken input is left out with a reason, never thrown", () => {
  const cases: [string, number, string][] = [
    ["BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:x\r\nEND:VEVENT\r\nEND:VCALENDAR", 0, "an event without DTSTART"],
    ["BEGIN:VEVENT\nDTSTART:20261340T090000\nEND:VEVENT", 0, "DTSTART 20261340T090000 is not a date or time"],
    ["BEGIN:VEVENT\nDTSTART:20261117T090000\nRRULE:FREQ=SECONDLY\nEND:VEVENT", 0, "RRULE FREQ=SECONDLY: the calendar keeps times to the minute"],
    ["BEGIN:VEVENT\nDTSTART:20261117\nRRULE:FREQ=HOURLY\nEND:VEVENT", 0, "RRULE FREQ=HOURLY is not one this calendar repeats by"],
    ["BEGIN:VEVENT\nDTSTART:20261117\nRDATE:2026XX18\nEND:VEVENT", 0, "RDATE 2026XX18 is not a date or time"],
    ["BEGIN:VEVENT\nDTSTART:20261117\nDURATION:PXYZ\nEND:VEVENT", 0, "DURATION PXYZ is not a duration"],
    ["BEGIN:VEVENT\nDTSTART:20261117\n", 0, "an event without END:VEVENT"],
    ["BEGIN:VEVENT\nDTSTART:20261117\nEND:VTODO\nEND:VEVENT", 1, "END:VTODO without its BEGIN"],
    ["BEGIN:VEVENT\nDTSTART;TZID=\"broken:20261117T090000\nEND:VEVENT", 0, "a line that is not a property"],
    ["BEGIN:VEVENT\nDTSTART:20261117\nSTATUS:CANCELLED\nEND:VEVENT", 0, ""],
  ];
  for (const [text, events, why] of cases) {
    const r = parseICS(text, id);
    expectEqual(r.events.length, events, `events of ${JSON.stringify(text)}`);
    if (why) expectEqual(r.problems.map((p) => p.why).includes(why), true, `${JSON.stringify(text)} says ${why}: ${JSON.stringify(r.problems)}`);
  }
  expectEqual(parseICS("BEGIN:X\n".repeat(20), id).problems.at(-1)?.why, "components nested too deep", "nesting");
  expectEqual(parseICS("x".repeat((1 << 20) + 1), id).problems[0].why, "the file is larger than 1 MB", "size");
  const many = "BEGIN:VEVENT\nDTSTART:20261117\nEND:VEVENT\n".repeat(2100);
  const r = parseICS(many, id);
  expectEqual([r.events.length, r.problems.at(-1)?.why], [2000, "more than 2000 events"], "count");
  const long = parseICS(`BEGIN:VEVENT\nDTSTART:20261117\nSUMMARY:${"\u957f".repeat(5000)}\nEND:VEVENT`, id).events[0];
  expectEqual([...long.title].length, 200, "a title is bounded");
});

test("UTF-8", () => {
  const s = "\u65e5\u5386 Calendario ✓ 😀";
  expectEqual(decodeUTF8(encodeUTF8(s)), s, "round trip");
  expectEqual(decodeUTF8("\xff\xc3(\xe6\x97"), "\ufffd\ufffd(\ufffd\ufffd", "broken sequences");
  expectEqual(decodeUTF8("\xef\xbb\xbfBEGIN"), "\ufeffBEGIN", "a byte order mark stays for the parser to drop");
});

test("RDATE read in its forms and written back; an event the user organizes marks its attendees for a scheduling server; a message has no reminders", () => {
  const text = [
    "BEGIN:VCALENDAR",
    "BEGIN:VEVENT",
    "UID:rd@x",
    "DTSTART;TZID=America/New_York:20261124T090000",
    "DTEND;TZID=America/New_York:20261124T100000",
    "RRULE:FREQ=WEEKLY;COUNT=2",
    "RDATE;TZID=America/New_York:20261126T150000,20261127T150000",
    "RDATE:20261128T140000Z",
    "RDATE;VALUE=PERIOD:20261129T140000Z/20261129T160000Z",
    "RDATE;VALUE=DATE:20261130",
    "ORGANIZER:mailto:me@x.test",
    "ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@x.test",
    "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:a@x.test",
    "BEGIN:VALARM",
    "ACTION:DISPLAY",
    "TRIGGER:-PT10M",
    "END:VALARM",
    "END:VEVENT",
    "END:VCALENDAR",
  ].join("\r\n");
  const r = parseICS(text, id);
  expectEqual([r.problems, r.events[0].rdates], [[], ["2026-11-26T15:00", "2026-11-27T15:00", "2026-11-28T09:00", "2026-11-29T09:00", "2026-11-30T09:00"]], "on the event's clock: UTC, a PERIOD's start, a date at the event's time");
  const own = serialiseICS(r.events, "20261120T000000Z", {}, { me: ["ME@x.test"] });
  expectEqual(own.includes("RDATE;TZID=America/New_York:20261128T090000"), true, own);
  expectEqual(own.includes("ATTENDEE;PARTSTAT=NEEDS-ACTION;SCHEDULE-AGENT=CLIENT:mailto:a@x.test"), true, own);
  expectEqual(parseICS(own, id).events[0].rdates, r.events[0].rdates, "read back the same");
  const theirs = serialiseICS(r.events, "20261120T000000Z", {}, { me: ["other@x.test"] });
  expectEqual(theirs.includes("SCHEDULE-AGENT"), false, "an invitation from someone else");
  const msg = serialiseICS(r.events, "20261120T000000Z", {}, { method: "REQUEST", me: ["me@x.test"] });
  expectEqual([msg.includes("METHOD:REQUEST"), msg.includes("VALARM"), msg.includes("SCHEDULE-AGENT")], [true, false, false], msg);
});

test("where the account's server schedules: SCHEDULE-AGENT and SCHEDULE-STATUS read, the server's attendees left to it, the phone's marked, an answer by file marks the organizer, the first hand-over marks them all", () => {
  const text = [
    "BEGIN:VCALENDAR",
    "BEGIN:VEVENT",
    "UID:s1",
    "DTSTART:20301104T010000Z",
    "SUMMARY:sched",
    "ORGANIZER:mailto:me@kario.test",
    "ATTENDEE;PARTSTAT=NEEDS-ACTION;SCHEDULE-STATUS=1.2:mailto:bob@example.com",
    "ATTENDEE;PARTSTAT=NEEDS-ACTION;SCHEDULE-STATUS=\"5.2,2.0\":mailto:zoe@example.com",
    "ATTENDEE;SCHEDULE-AGENT=CLIENT;PARTSTAT=ACCEPTED:mailto:carol@example.com",
    "END:VEVENT",
    "END:VCALENDAR",
  ].join("\r\n");
  const [e] = parseICS(text, id).events;
  expectEqual(e.attendees?.map((a) => [a.delivery ?? "", a.agent ?? ""]), [["1.2", ""], ["5.2", ""], ["", "CLIENT"]], "read");
  const me = ["mailto:me@kario.test"];
  const server = serialiseICS([e], "20301101T000000Z", {}, { me, schedule: true });
  expectEqual([server.includes("PARTSTAT=NEEDS-ACTION:mailto:bob@example.com"), server.includes("SCHEDULE-AGENT=CLIENT:mailto:carol"), server.includes("SCHEDULE-STATUS")], [true, true, false], server);
  const plain = serialiseICS([e], "20301101T000000Z", {}, { me });
  expectEqual(plain.split("SCHEDULE-AGENT=CLIENT").length - 1, 3, plain);
  const handover = serialiseICS([e], "20301101T000000Z", {}, { me, schedule: true, byPhone: true });
  expectEqual(handover.split("SCHEDULE-AGENT=CLIENT").length - 1, 3, handover);
  const msg = serialiseICS([e], "20301101T000000Z", {}, { me, schedule: true, method: "REQUEST" });
  expectEqual(msg.includes("SCHEDULE-"), false, msg);
  const invited: CalEvent = { ...e, organizer: { addr: "mailto:bob@example.com", name: "" }, src: "" };
  expectEqual(serialiseICS([invited], "20301101T000000Z", {}, { me, schedule: true }).includes("ORGANIZER;SCHEDULE-AGENT=CLIENT:mailto:bob"), true, "answered by file");
  expectEqual(serialiseICS([{ ...invited, src: "account:a1" }], "20301101T000000Z", {}, { me, schedule: true }).includes("SCHEDULE-AGENT=CLIENT:mailto:bob"), false, "the server answers");
  expectEqual(serialiseICS([invited], "20301101T000000Z", {}, { me }).includes("ORGANIZER;SCHEDULE-AGENT"), false, "no scheduling server");
});
