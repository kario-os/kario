// Repetition on the event's own clock, and its instants in a zone with
// daylight saving (America/New_York) and in one without (Asia/Shanghai).
import { expectEqual, test } from "@kario/test";
import { day, fmtDate, fmtWall, setPhoneZone, setZoneSpans, toInstant, toWall, type Wall } from "../src/dates";
import { ruleText, setLocale } from "../src/fmt";
import { ruleOf } from "../src/ics";
import { beyondEditor, expand, formatRule, lastStart, parseRule, weekNo } from "../src/recur";

const w = (y: number, m: number, d: number, h = 9, mi = 0): Wall => ({ day: day(y, m, d), min: h * 60 + mi });
const days = (ws: Wall[]) => ws.map((x) => fmtDate(x.day));
const all = (start: Wall, rrule: string, from = start.day, to = start.day + 800, ex: Wall[] = []) =>
  expand(start, ruleOf(rrule, ""), from, to, new Set(ex.map((x) => x.day * 1440 + x.min)));

// America/New_York in 2026: EDT from 2026-03-08 07:00 UTC to 2026-11-01 06:00 UTC.
setZoneSpans("America/New_York", [
  { start: Date.UTC(2025, 10, 2, 6) / 1000, offset: -5 * 3600 },
  { start: Date.UTC(2026, 2, 8, 7) / 1000, offset: -4 * 3600 },
  { start: Date.UTC(2026, 10, 1, 6) / 1000, offset: -5 * 3600 },
]);
setZoneSpans("Asia/Shanghai", [{ start: Date.UTC(1991, 8, 15) / 1000, offset: 8 * 3600 }]);
setPhoneZone("Europe/Nowhere");

test("daily, weekly, monthly and yearly, with COUNT, UNTIL and INTERVAL", () => {
  expectEqual(days(all(w(2026, 11, 17), "FREQ=DAILY;COUNT=3")), ["2026-11-17", "2026-11-18", "2026-11-19"], "daily");
  expectEqual(days(all(w(2026, 11, 17), "FREQ=DAILY;INTERVAL=10;UNTIL=20261217")), ["2026-11-17", "2026-11-27", "2026-12-07", "2026-12-17"], "every 10 days until a date");
  expectEqual(days(all(w(2026, 11, 17), "FREQ=WEEKLY;COUNT=3")), ["2026-11-17", "2026-11-24", "2026-12-01"], "weekly on the start's day");
  expectEqual(days(all(w(2026, 11, 16), "FREQ=WEEKLY;BYDAY=MO,WE,FR;COUNT=5")), ["2026-11-16", "2026-11-18", "2026-11-20", "2026-11-23", "2026-11-25"], "Monday, Wednesday, Friday");
  expectEqual(days(all(w(2026, 11, 17), "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,TH;COUNT=4")), ["2026-11-17", "2026-11-19", "2026-12-01", "2026-12-03"], "every other week");
  expectEqual(days(all(w(2026, 1, 31), "FREQ=MONTHLY;COUNT=5")), ["2026-01-31", "2026-03-31", "2026-05-31", "2026-07-31", "2026-08-31"], "the 31st: months without one are passed over");
  expectEqual(days(all(w(2026, 11, 9), "FREQ=MONTHLY;BYDAY=2MO;COUNT=3")), ["2026-11-09", "2026-12-14", "2027-01-11"], "the second Monday");
  expectEqual(days(all(w(2026, 11, 27), "FREQ=MONTHLY;BYDAY=-1FR;COUNT=3")), ["2026-11-27", "2026-12-25", "2027-01-29"], "the last Friday");
  expectEqual(days(all(w(2026, 11, 30), "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3")), ["2026-11-30", "2026-12-31", "2027-01-29"], "the last weekday");
  expectEqual(days(all(w(2024, 2, 29), "FREQ=YEARLY;COUNT=3", day(2024, 1, 1), day(2040, 1, 1))), ["2024-02-29", "2028-02-29", "2032-02-29"], "February 29th: leap years only");
  expectEqual(days(all(w(2026, 11, 26), "FREQ=YEARLY;BYMONTH=11;BYDAY=4TH;COUNT=3")), ["2026-11-26", "2027-11-25", "2028-11-23"], "Thanksgiving");
});

test("EXDATE takes occurrences out after COUNT counted them; windows; a rule far in the past", () => {
  const start = w(2026, 11, 17);
  expectEqual(days(all(start, "FREQ=DAILY;COUNT=4", start.day, start.day + 10, [w(2026, 11, 18)])), ["2026-11-17", "2026-11-19", "2026-11-20"], "3 of 4");
  expectEqual(days(all(start, "FREQ=WEEKLY", day(2027, 1, 1), day(2027, 1, 14))), ["2027-01-05", "2027-01-12"], "a window later on");
  const old = w(1990, 1, 2);
  expectEqual(days(all(old, "FREQ=DAILY", day(2026, 11, 17), day(2026, 11, 18))), ["2026-11-17", "2026-11-18"], "36 years of a daily rule, stepped over");
  expectEqual(days(all(old, "FREQ=MONTHLY", day(2026, 11, 1), day(2026, 11, 30))), ["2026-11-02"], "and a monthly one");
  expectEqual(fmtWall(lastStart(start, parseRule("FREQ=WEEKLY;COUNT=10"))!), "2027-01-19T09:00", "the last of 10");
  expectEqual(lastStart(start, parseRule("FREQ=WEEKLY")), undefined, "one that goes on");
});

test("rules this calendar does not repeat by are refused, and rules write back as read", () => {
  for (const bad of [
    "FREQ=SECONDLY",
    "FREQ=HOURLY;BYWEEKNO=3",
    "FREQ=DAILY;BYHOUR=24",
    "FREQ=DAILY;BYMINUTE=60",
    "FREQ=DAILY;BYHOUR=-1",
    "FREQ=DAILY;BYSECOND=30",
    "FREQ=MONTHLY;BYWEEKNO=20",
    "FREQ=DAILY;BYYEARDAY=100",
    "FREQ=YEARLY;BYWEEKNO=54",
    "FREQ=YEARLY;BYYEARDAY=0",
    "FREQ=YEARLY;BYWEEKNO=20;BYDAY=1MO",
    "FREQ=WEEKLY;BYMONTHDAY=3",
    "FREQ=DAILY;COUNT=3;UNTIL=20261231",
    "FREQ=WEEKLY;BYDAY=XX",
    "FREQ=DAILY;INTERVAL=0",
    "INTERVAL=2",
    "FREQ=MONTHLY;BYMONTHDAY=0",
    "garbage",
  ]) {
    expectEqual(parseRule(bad, () => w(2026, 12, 31)), undefined, bad);
  }
  for (const ok of [
    "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,TH",
    "FREQ=MONTHLY;COUNT=10;BYDAY=-1FR",
    "FREQ=YEARLY;BYMONTH=11;BYDAY=4TH",
    "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1",
    "FREQ=DAILY;BYHOUR=9,10,11;BYMINUTE=0,20,40",
    "FREQ=YEARLY;BYWEEKNO=-1,20;BYDAY=MO;WKST=SU",
    "FREQ=YEARLY;INTERVAL=3;COUNT=10;BYYEARDAY=1,100,-1",
    "FREQ=HOURLY;INTERVAL=3;BYDAY=MO",
    "FREQ=MINUTELY;INTERVAL=15;BYHOUR=9,10;BYYEARDAY=-1",
  ]) {
    const once = formatRule(parseRule(ok)!);
    expectEqual(formatRule(parseRule(once)!), once, ok);
    expectEqual(once.split(";").sort(), ok.split(";").sort(), `${ok}: the same parts`);
  }
  expectEqual(parseRule("FREQ=DAILY;COUNT=99999")!.count, 1000, "COUNT is bounded");
  expectEqual(parseRule("FREQ=DAILY;BYSECOND=0")?.freq, "DAILY", "BYSECOND=0 is a whole minute");
});

const walls = (ws: Wall[]) => ws.map(fmtWall);

// The examples of RFC 5545 §3.8.5.3 for the parts BYHOUR, BYMINUTE,
// BYWEEKNO and BYYEARDAY, with their answers.
test("BYYEARDAY, BYWEEKNO, BYHOUR and BYMINUTE: RFC 5545's examples", () => {
  expectEqual(
    days(all(w(1997, 1, 1), "FREQ=YEARLY;INTERVAL=3;COUNT=10;BYYEARDAY=1,100,200", day(1997, 1, 1), day(2010, 1, 1))),
    ["1997-01-01", "1997-04-10", "1997-07-19", "2000-01-01", "2000-04-09", "2000-07-18", "2003-01-01", "2003-04-10", "2003-07-19", "2006-01-01"],
    "every third year on the 1st, 100th and 200th day",
  );
  expectEqual(days(all(w(1997, 5, 12), "FREQ=YEARLY;BYWEEKNO=20;BYDAY=MO", day(1997, 1, 1), day(1999, 12, 31))), ["1997-05-12", "1998-05-11", "1999-05-17"], "Monday of week 20");
  expectEqual(days(all(w(1997, 5, 19), "FREQ=YEARLY;BYDAY=20MO", day(1997, 1, 1), day(1999, 12, 31))), ["1997-05-19", "1998-05-18", "1999-05-17"], "the 20th Monday of the year");
  expectEqual(days(all(w(1997, 3, 13), "FREQ=YEARLY;BYMONTH=3;BYDAY=TH", day(1997, 1, 1), day(1997, 12, 31))), ["1997-03-13", "1997-03-20", "1997-03-27"], "every Thursday in March");
  const twenty = walls(all(w(1997, 9, 2), "FREQ=DAILY;BYHOUR=9,10,11,12,13,14,15,16;BYMINUTE=0,20,40", day(1997, 9, 2), day(1997, 9, 3)));
  expectEqual([twenty.length, twenty.slice(0, 4), twenty.slice(23, 25)], [48, ["1997-09-02T09:00", "1997-09-02T09:20", "1997-09-02T09:40", "1997-09-02T10:00"], ["1997-09-02T16:40", "1997-09-03T09:00"]], "every 20 minutes, 9:00 to 16:40");
  expectEqual(walls(all(w(1997, 9, 2), "FREQ=WEEKLY;BYHOUR=9,17;COUNT=4")), ["1997-09-02T09:00", "1997-09-02T17:00", "1997-09-09T09:00", "1997-09-09T17:00"], "twice on the day, weekly");
});

test("BYWEEKNO by WKST and across the year's ends; BYYEARDAY from the end; BYSETPOS over times", () => {
  // 2026's week 1 (weeks from Monday) starts on Monday 2025-12-29: in
  // year 2025's set, so a yearly rule from 2025 gives it in 2025.
  expectEqual(days(all(w(2025, 1, 6), "FREQ=YEARLY;BYWEEKNO=1;BYDAY=MO", day(2025, 1, 1), day(2027, 12, 31))), ["2025-01-06", "2025-12-29", "2027-01-04"], "week 1's Monday");
  // 2026 has 53 weeks (from Monday): the last is 28 December to 3 January.
  expectEqual(days(all(w(2026, 1, 1), "FREQ=YEARLY;BYWEEKNO=-1;BYDAY=MO,SU", day(2026, 1, 1), day(2026, 12, 31))), ["2026-01-01", "2026-12-28"], "the last week, Monday (Sunday is 2027's)");
  expectEqual(days(all(w(2026, 1, 1), "FREQ=YEARLY;BYWEEKNO=53;BYDAY=TH", day(2026, 1, 1), day(2033, 12, 31))), ["2026-01-01", "2026-12-31", "2032-12-30"], "week 53 only in the years that have one");
  expectEqual(weekNo(day(2026, 1, 1), 1), { n: 1, weeks: 53 }, "2026-01-01 is in week 1 of 53");
  expectEqual(weekNo(day(2026, 1, 1), 0).n, 53, "but with weeks from Sunday in 2025's 53rd");
  expectEqual(days(all(w(2026, 1, 1), "FREQ=YEARLY;BYYEARDAY=-1;COUNT=3", day(2026, 1, 1), day(2030, 1, 1))), ["2026-01-01", "2026-12-31", "2027-12-31"], "the year's last day");
  expectEqual(days(all(w(2024, 1, 1), "FREQ=YEARLY;BYYEARDAY=366;COUNT=3", day(2024, 1, 1), day(2040, 1, 1))), ["2024-01-01", "2024-12-31", "2028-12-31"], "day 366: leap years only");
  expectEqual(walls(all(w(2026, 11, 17, 9), "FREQ=DAILY;BYHOUR=9,12,18;BYSETPOS=-1;COUNT=3")), ["2026-11-17T09:00", "2026-11-17T18:00", "2026-11-18T18:00"], "the day's last time");
  expectEqual(walls(all(w(2026, 11, 2, 8), "FREQ=MONTHLY;BYDAY=MO;BYHOUR=8,20;BYSETPOS=2,-1;COUNT=4")), ["2026-11-02T08:00", "2026-11-02T20:00", "2026-11-30T20:00", "2026-12-07T20:00"], "the month's 2nd and last of its Mondays' times");
  // A yearly BYMONTHDAY without BYMONTH is every month's (RFC 5545's table: expand).
  expectEqual(days(all(w(2026, 1, 15), "FREQ=YEARLY;BYMONTHDAY=15;COUNT=3")), ["2026-01-15", "2026-02-15", "2026-03-15"], "every month's 15th");
});

test("a rule's sentence names its hours, weeks and days of the year", () => {
  setLocale({ date_order: "ymd", clock_24h: true });
  expectEqual(ruleText(parseRule("FREQ=DAILY;BYHOUR=9,17")!, day(2026, 11, 17), undefined, 9 * 60 + 30), "Every day, at 09:30, 17:30", "hours, the start's minute");
  expectEqual(ruleText(parseRule("FREQ=YEARLY;BYWEEKNO=20;BYDAY=MO")!, day(2026, 5, 11)), "Every year, in week 20, on Monday", "week");
  expectEqual(ruleText(parseRule("FREQ=YEARLY;BYYEARDAY=1,-1;COUNT=4")!, day(2026, 1, 1)), "Every year, on day 1, -1 of the year, 4 times", "days of the year");
  expectEqual([beyondEditor(parseRule("FREQ=WEEKLY;BYDAY=TU")), beyondEditor(parseRule("FREQ=DAILY;BYHOUR=9"))], [false, true], "what the editor cannot say");
});

test("a weekly 09:00 in New York keeps its wall time over the change to daylight saving", () => {
  const start = w(2026, 2, 24);
  const occ = expand(start, ruleOf("FREQ=WEEKLY;COUNT=4", "America/New_York"), start.day, start.day + 40);
  const utc = occ.map((o) => fmtWall(toWall(toInstant(o, "America/New_York"), "UTC")));
  expectEqual(utc, ["2026-02-24T14:00", "2026-03-03T14:00", "2026-03-10T13:00", "2026-03-17T13:00"], "14:00 UTC in winter, 13:00 in summer");
  const sh = expand(start, ruleOf("FREQ=WEEKLY;COUNT=3", "Asia/Shanghai"), start.day, start.day + 40).map((o) => fmtWall(toWall(toInstant(o, "Asia/Shanghai"), "UTC")));
  expectEqual(sh, ["2026-02-24T01:00", "2026-03-03T01:00", "2026-03-10T01:00"], "Shanghai has no daylight saving");
  // The UNTIL of a zoned rule is a UTC time: 2026-03-10 13:00 UTC is 09:00 in New York.
  const until = expand(start, ruleOf("FREQ=WEEKLY;UNTIL=20260310T130000Z", "America/New_York"), start.day, start.day + 60);
  expectEqual(days(until), ["2026-02-24", "2026-03-03", "2026-03-10"], "UNTIL in UTC");
});

test("a time daylight saving skips or repeats", () => {
  expectEqual(fmtWall(toWall(toInstant(w(2026, 3, 8, 2, 30), "America/New_York"), "UTC")), "2026-03-08T07:30", "02:30 does not exist: 03:30 EDT");
  expectEqual(fmtWall(toWall(toInstant(w(2026, 11, 1, 1, 30), "America/New_York"), "UTC")), "2026-11-01T05:30", "01:30 twice: the first, EDT");
  expectEqual(fmtWall(toWall(Date.UTC(2026, 10, 1, 6, 30), "America/New_York")), "2026-11-01T01:30", "the second 01:30 back on the clock");
});

test("hourly and minutely (RFC 5545's examples), BY parts limiting them, a window far on", () => {
  const at = (ws: Wall[]) => ws.map((x) => fmtWall(x));
  const start = w(1997, 9, 2);
  expectEqual(at(all(start, "FREQ=HOURLY;INTERVAL=3;UNTIL=19970902T170000")), ["1997-09-02T09:00", "1997-09-02T12:00", "1997-09-02T15:00"], "every 3 hours until 17:00");
  expectEqual(at(all(start, "FREQ=MINUTELY;INTERVAL=15;COUNT=6")), ["1997-09-02T09:00", "1997-09-02T09:15", "1997-09-02T09:30", "1997-09-02T09:45", "1997-09-02T10:00", "1997-09-02T10:15"], "every 15 minutes, 6 times");
  expectEqual(at(all(start, "FREQ=MINUTELY;INTERVAL=90;COUNT=4")), ["1997-09-02T09:00", "1997-09-02T10:30", "1997-09-02T12:00", "1997-09-02T13:30"], "every hour and a half");
  const twenty = all(start, "FREQ=MINUTELY;INTERVAL=20;BYHOUR=9,10,11,12,13,14,15,16", start.day, start.day + 1);
  expectEqual([twenty.length, fmtWall(twenty[23]), fmtWall(twenty[24])], [48, "1997-09-02T16:40", "1997-09-03T09:00"], "every 20 minutes from 9:00 to 16:40, each day");
  expectEqual(at(all(start, "FREQ=MINUTELY;BYMINUTE=0,30;COUNT=4")), ["1997-09-02T09:00", "1997-09-02T09:30", "1997-09-02T10:00", "1997-09-02T10:30"], "BYMINUTE limits a minutely rule within the hour");
  const monday = w(2026, 11, 16);
  expectEqual(at(all(monday, "FREQ=HOURLY;INTERVAL=6;BYDAY=MO", monday.day, monday.day + 7)), ["2026-11-16T09:00", "2026-11-16T15:00", "2026-11-16T21:00", "2026-11-23T03:00", "2026-11-23T09:00", "2026-11-23T15:00", "2026-11-23T21:00"], "every 6 hours on Mondays only");
  expectEqual(at(all(w(2026, 11, 16, 9, 10), "FREQ=HOURLY;BYMINUTE=0,30;COUNT=3")), ["2026-11-16T09:10", "2026-11-16T09:30", "2026-11-16T10:00"], "BYMINUTE expands an hourly rule");
  const far = all(w(2020, 1, 1, 0, 0), "FREQ=HOURLY", day(2030, 6, 1), day(2030, 6, 1));
  expectEqual([far.length, fmtWall(far[0])], [24, "2030-06-01T00:00"], "ten years of an hourly rule, stepped over");
  expectEqual(fmtWall(lastStart(start, parseRule("FREQ=MINUTELY;BYDAY=MO;BYHOUR=9;BYMINUTE=0;COUNT=300"))!), "2003-05-26T09:00", "DTSTART and 299 Mondays at 9:00, counted without walking every minute");
  expectEqual(beyondEditor(parseRule("FREQ=HOURLY")), true, "the editor keeps an hourly rule as it is");
  setLocale({ date_order: "ymd", clock_24h: true });
  expectEqual(ruleText(parseRule("FREQ=HOURLY;INTERVAL=3;BYDAY=MO"), start.day), "Every 3 hours, on Monday", "an hourly rule said");
  expectEqual(ruleText(parseRule("FREQ=MINUTELY;INTERVAL=20;BYHOUR=9,10"), start.day), "Every 20 minutes, in the hours 09:00, 10:00", "a minutely one");
});

test("RDATE adds starts to the rule's, in order, once each; EXDATE takes them out too", () => {
  const start = w(2026, 11, 16);
  const extra = [w(2026, 11, 18), w(2026, 11, 23), w(2026, 12, 2, 14), w(2026, 11, 25)];
  const got = expand(start, ruleOf("FREQ=WEEKLY;COUNT=2", ""), start.day, start.day + 30, new Set([w(2026, 11, 25).day * 1440 + 540]), 100, extra);
  expectEqual(got.map((x) => fmtWall(x)), ["2026-11-16T09:00", "2026-11-18T09:00", "2026-11-23T09:00", "2026-12-02T14:00"], "the rule's two, the extra ones, one excluded");
  expectEqual(expand(start, undefined, start.day, start.day + 30, new Set(), 100, extra.slice(0, 1)).length, 2, "RDATE without a rule");
  expectEqual(expand(start, ruleOf("FREQ=DAILY", ""), start.day, start.day + 30, new Set(), 3, [w(2026, 11, 16, 8)]).map((x) => fmtWall(x)), ["2026-11-16T08:00", "2026-11-16T09:00", "2026-11-17T09:00"], "at most the limit, the earliest");
});
