// A file's own zones (VTIMEZONE): Outlook's "Customized Time Zone" with
// yearly rules from 1601, RDATE onsets, matching an IANA zone with the
// system's offsets, and writing the zone back so the file reads the same.
import { expectEqual, phoneZone, test } from "@kario/test";
import { fmtWall, setPhoneZone, toInstant, toWall, type Wall } from "../src/dates";
import { parseICS, serialiseICS, vtimezones } from "../src/ics";
import { matchZone, parseOffset, vzoneSpans } from "../src/vtz";

// The phone's zone, and the app's word for it (locale.get).
phoneZone("Asia/Shanghai");
setPhoneZone("Asia/Shanghai");

const OUTLOOK_ZONE = [
  "BEGIN:VTIMEZONE",
  "TZID:Customized Time Zone",
  "BEGIN:STANDARD",
  "DTSTART:16010101T020000",
  "TZOFFSETFROM:-0400",
  "TZOFFSETTO:-0500",
  "RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=1SU;BYMONTH=11",
  "END:STANDARD",
  "BEGIN:DAYLIGHT",
  "DTSTART:16010101T020000",
  "TZOFFSETFROM:-0500",
  "TZOFFSETTO:-0400",
  "RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=2SU;BYMONTH=3",
  "END:DAYLIGHT",
  "END:VTIMEZONE",
];

const file = (zone: string[], ...event: string[]) =>
  ["BEGIN:VCALENDAR", "VERSION:2.0", "METHOD:PUBLISH", ...zone, "BEGIN:VEVENT", "UID:z1@outlook", "SUMMARY:review", ...event, "END:VEVENT", "END:VCALENDAR"].join("\r\n");

const utc = (w: Wall, zone: string) => fmtWall(toWall(toInstant(w, zone), "UTC"));

let n = 0;
const id = () => "z" + ++n;

test("Outlook's customized zone: its own rules give the times, over the change to daylight saving", () => {
  const r = parseICS(file(OUTLOOK_ZONE, 'DTSTART;TZID="Customized Time Zone":20260303T090000', 'DTEND;TZID="Customized Time Zone":20260303T100000', "RRULE:FREQ=WEEKLY;COUNT=3"), id);
  expectEqual(r.problems, [], "problems");
  const e = r.events[0];
  expectEqual([e.zone.startsWith("vtz:"), r.zones[e.zone]?.tzid, e.start], [true, "Customized Time Zone", "2026-03-03T09:00"], "the zone kept as the file's");
  const day = (s: string): Wall => ({ day: toWall(Date.parse(s + "T12:00:00Z"), "UTC").day, min: 9 * 60 });
  expectEqual(["2026-03-03", "2026-03-10", "2026-11-03"].map((d) => utc(day(d), e.zone)), ["2026-03-03T14:00", "2026-03-10T13:00", "2026-11-03T14:00"], "EST, then EDT from 8 March, EST again");
  expectEqual(utc({ day: day("2026-03-08").day, min: 2 * 60 + 30 }, e.zone), "2026-03-08T07:30", "02:30 on the change does not exist: as 03:30 EDT");
});

test("RDATE onsets, offsets with seconds, and what is not a VTIMEZONE", () => {
  const zone = [
    "BEGIN:VTIMEZONE",
    "TZID:GMT +0800 (Standard) / GMT +0900 (Daylight)",
    "BEGIN:STANDARD",
    "DTSTART:19700101T000000",
    "TZOFFSETFROM:+0800",
    "TZOFFSETTO:+0800",
    "RDATE:20260901T030000",
    "END:STANDARD",
    "BEGIN:DAYLIGHT",
    "DTSTART:20260601T020000",
    "TZOFFSETFROM:+080000",
    "TZOFFSETTO:+0900",
    "END:DAYLIGHT",
    "END:VTIMEZONE",
  ];
  const r = parseICS(file(zone, 'DTSTART;TZID="GMT +0800 (Standard) / GMT +0900 (Daylight)":20260701T090000'), id);
  const e = r.events[0];
  expectEqual(utc({ day: toWall(Date.UTC(2026, 6, 1, 12), "UTC").day, min: 540 }, e.zone), "2026-07-01T00:00", "+09:00 between the onsets");
  expectEqual(utc({ day: toWall(Date.UTC(2026, 9, 1, 12), "UTC").day, min: 540 }, e.zone), "2026-10-01T01:00", "+08:00 after");
  expectEqual([parseOffset("+0530"), parseOffset("-001500"), parseOffset("+2500"), parseOffset("0800")], [330, -15, undefined, undefined], "offsets");
  const broken = ["BEGIN:VTIMEZONE", "TZID:Nowhere", "BEGIN:STANDARD", "DTSTART:2026", "TZOFFSETTO:+0100", "END:STANDARD", "END:VTIMEZONE"];
  const f = parseICS(file(broken, "DTSTART;TZID=Nowhere:20260701T090000"), id);
  expectEqual([vtimezones(file(broken)).size, f.events[0].zone, f.problems], [0, "", []], "no observance: the time floats, as before");
  const many = Array.from({ length: 40 }, (_, i) => [...OUTLOOK_ZONE.slice(0, 1), "TZID:z" + i, ...OUTLOOK_ZONE.slice(2)]).flat();
  expectEqual(vtimezones(file(many)).size, 16, "at most 16 zones a file");
});

test("a zone with an IANA zone's rules is that zone; one with other rules stays the file's", async () => {
  const z = vtimezones(file(OUTLOOK_ZONE)).get("Customized Time Zone")!;
  const own = vzoneSpans(z);
  const asked: string[] = [];
  const fetch = async (zone: string) => {
    asked.push(zone);
    if (zone === "America/New_York") return own;
    return [{ start: 0, offset: zone === "Asia/Shanghai" ? 8 * 3600 : -5 * 3600 }];
  };
  const now = Date.UTC(2026, 10, 20);
  expectEqual(await matchZone(z, now, "Asia/Shanghai", fetch), "America/New_York", "the US rules");
  expectEqual(asked, ["Asia/Shanghai", "America/New_York"], "the phone's zone first, then those with -05:00 and -04:00");
  // The European rules (last Sunday of March and October) at the same offsets are not New York's.
  const eu = vzoneSpans({ tzid: "x", obs: z.obs.map((o) => ({ ...o, rrule: o.rrule.replace(/BYDAY=\dSU;BYMONTH=11/, "BYDAY=-1SU;BYMONTH=10").replace(/BYDAY=\dSU;BYMONTH=3/, "BYDAY=-1SU;BYMONTH=3") })) });
  expectEqual(await matchZone(z, now, "", async () => eu), "", "other change dates");
  const r = parseICS(file(OUTLOOK_ZONE, 'DTSTART;TZID="Customized Time Zone":20260303T090000'), id, { "Customized Time Zone": "America/New_York" });
  expectEqual([r.events[0].zone, Object.keys(r.zones)], ["America/New_York", []], "matched: the IANA zone, no zone of its own");
});

test("written back with its VTIMEZONE, the file reads the same", () => {
  const first = parseICS(file(OUTLOOK_ZONE, 'DTSTART;TZID="Customized Time Zone":20261117T090000', "DURATION:PT1H", "RRULE:FREQ=WEEKLY;UNTIL=20261222T140000Z", 'EXDATE;TZID="Customized Time Zone":20261124T090000'), id);
  const text = serialiseICS(first.events, "20261120T000000Z", first.zones);
  expectEqual(["BEGIN:VTIMEZONE", "TZID:Customized Time Zone", "TZOFFSETFROM:-0500", "DTSTART;TZID=Customized Time Zone:20261117T090000"].every((l) => text.includes(l)), true, text);
  const again = parseICS(text, id);
  const strip = (e: object) => ({ ...e, id: "" });
  expectEqual(again.events.map(strip), first.events.map(strip), "the same events, on the same zone");
  expectEqual(Object.keys(again.zones), Object.keys(first.zones), "the same key");
  const odd = parseICS(file(["BEGIN:VTIMEZONE", "TZID:(UTC+08:00) Beijing, Chongqing", ...OUTLOOK_ZONE.slice(2)], 'DTSTART;TZID="(UTC+08:00) Beijing, Chongqing":20261117T090000'), id);
  expectEqual(serialiseICS(odd.events, "20261120T000000Z", odd.zones).includes('DTSTART;TZID="(UTC+08:00) Beijing, Chongqing":20261117T090000'), true, "a TZID with a colon or a comma is quoted");
});
