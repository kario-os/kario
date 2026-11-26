// "This and following": a repeating event split at an occurrence, its
// COUNT shared out, its UNTIL on the event's clock, the EXDATEs and the
// changed occurrences from the split on going with the new series.
import { expectEqual, test } from "@kario/test";
import { fmtDate, setPhoneZone, setZoneSpans } from "../src/dates";
import { ruleOf } from "../src/ics";
import type { CalEvent } from "../src/model";
import { expand } from "../src/recur";
import { Calendar, span } from "../src/store";

setPhoneZone("Asia/Shanghai");
setZoneSpans("America/New_York", [
  { start: Date.UTC(2025, 10, 2, 6) / 1000, offset: -5 * 3600 },
  { start: Date.UTC(2026, 2, 8, 7) / 1000, offset: -4 * 3600 },
  { start: Date.UTC(2026, 10, 1, 6) / 1000, offset: -5 * 3600 },
]);

const ev = (o: Partial<CalEvent>): CalEvent => ({
  id: "e1", uid: "u1@x", title: "weekly", allDay: false, start: "2026-11-17T09:00", end: "2026-11-17T10:00", zone: "",
  location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "", ...o,
});

const days = (e: CalEvent) => {
  const sp = span(e)!;
  return expand(sp.start, ruleOf(e.rrule, e.zone), sp.start.day, sp.start.day + 800, new Set(), 50).map((w) => fmtDate(w.day));
};

test("a weekly COUNT split at its third: two before, three from it, the override and EXDATE go along", () => {
  const cal = new Calendar();
  const master = ev({ rrule: "FREQ=WEEKLY;COUNT=5", exdates: ["2026-11-24T09:00", "2026-12-08T09:00"] });
  const moved = ev({ id: "e2", title: "moved", start: "2026-12-01T11:00", end: "2026-12-01T12:00", recurrenceId: "2026-12-01T09:00" });
  const early = ev({ id: "e3", title: "early", start: "2026-11-24T08:00", end: "2026-11-24T09:00", recurrenceId: "2026-11-24T09:00" });
  cal.events = [master, moved, early];
  const p = cal.split(master, "2026-12-01T09:00", "e9")!;
  expectEqual([p.old.rrule, p.old.exdates], ["FREQ=WEEKLY;UNTIL=20261201T085900", ["2026-11-24T09:00"]], "the old series ends a minute before (floating time)");
  expectEqual(days(p.old), ["2026-11-17", "2026-11-24"], "its occurrences");
  expectEqual([p.rest.id, p.rest.uid.startsWith("e9-"), p.rest.start, p.rest.end, p.rest.rrule, p.rest.exdates], ["e9", true, "2026-12-01T09:00", "2026-12-01T10:00", "FREQ=WEEKLY;COUNT=3", ["2026-12-08T09:00"]], "the new series");
  expectEqual(days(p.rest), ["2026-12-01", "2026-12-08", "2026-12-15"], "what COUNT has left");
  expectEqual(p.moved.map((x) => [x.id, x.uid]), [["e2", p.rest.uid]], "the override from the split on goes with it, the earlier stays");
  expectEqual(cal.events[1].uid, "u1@x", "nothing changed yet");
  expectEqual(cal.split(master, "2026-11-17T09:00"), undefined, "at the first occurrence: the whole series");
  expectEqual(cal.split(master, "2026-12-02T09:00"), undefined, "not an occurrence");
});

test("UNTIL in UTC for a zoned event, the day before for an all-day one; rules the editor cannot say split too", () => {
  const cal = new Calendar();
  const ny = ev({ zone: "America/New_York", rrule: "FREQ=DAILY;BYHOUR=9,17;UNTIL=20261231T235900Z" });
  const p = cal.split(ny, "2026-11-20T17:00")!;
  expectEqual(p.old.rrule, "FREQ=DAILY;UNTIL=20261120T215900Z;BYHOUR=9,17", "16:59 in New York is 21:59 UTC");
  expectEqual(p.rest.rrule, "FREQ=DAILY;UNTIL=20261231T235900Z;BYHOUR=9,17", "the rest keeps the end");
  expectEqual([p.rest.start, p.rest.end], ["2026-11-20T17:00", "2026-11-20T18:00"], "from the 17:00 one");
  const bday = ev({ allDay: true, start: "2020-03-02", end: "2020-03-03", rrule: "FREQ=YEARLY" });
  const q = cal.split(bday, "2027-03-02")!;
  expectEqual([q.old.rrule, q.rest.start, q.rest.end, q.rest.rrule], ["FREQ=YEARLY;UNTIL=20270301", "2027-03-02", "2027-03-03", "FREQ=YEARLY"], "all day");
});

test("RDATE: the extra starts are occurrences (and EXDATE takes one out); a split shares them out but never starts at one", () => {
  const cal = new Calendar();
  const master = ev({ rrule: "FREQ=WEEKLY;COUNT=3", rdates: ["2026-11-19T15:00", "2026-12-03T15:00", "2026-12-10T09:00"], exdates: ["2026-12-03T15:00"] });
  cal.events = [master];
  const when = cal.occurrences(fmtDay("2026-11-17"), fmtDay("2026-12-31")).map((o) => o.key);
  expectEqual(when, ["2026-11-17T09:00", "2026-11-19T15:00", "2026-11-24T09:00", "2026-12-01T09:00", "2026-12-10T09:00"], "the rule's three and two RDATEs");
  expectEqual(cal.occAt(master, "2026-11-19T15:00")?.key, "2026-11-19T15:00", "an RDATE's occurrence opens");
  const p = cal.split(master, "2026-11-24T09:00", "e9")!;
  expectEqual([p.old.rdates, p.rest.rdates], [["2026-11-19T15:00"], ["2026-12-03T15:00", "2026-12-10T09:00"]], "the RDATEs before and after");
  expectEqual(cal.split(master, "2026-11-19T15:00"), undefined, "not at an RDATE: the rule would start over from it");
  const only = ev({ id: "e5", uid: "u5@x", rdates: ["2026-11-20T09:00"] });
  cal.events = [only, ev({ id: "e6", uid: "u5@x", title: "changed", start: "2026-11-20T10:00", end: "2026-11-20T11:00", recurrenceId: "2026-11-20T09:00" })];
  expectEqual(cal.occurrences(fmtDay("2026-11-17"), fmtDay("2026-11-30")).map((o) => o.ev.title + " " + o.key), ["weekly 2026-11-17T09:00", "changed 2026-11-20T10:00"], "an RDATE replaced by a changed occurrence");
  expectEqual(cal.master(cal.events[1])?.id, "e5", "its event, without a rule");
});

function fmtDay(s: string): number {
  const [y, m, d] = s.split("-").map(Number);
  return Math.round(Date.UTC(y, m - 1, d) / 86400000);
}
