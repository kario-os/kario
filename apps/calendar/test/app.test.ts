// The calendar under a fake Shell: the month grid and its keys, an event
// with a reminder, a repeating one changed once, importing and exporting,
// the system's import entry point, Spanish. Dates are relative to today.
import { expectEqual, launch, phoneZone, test, type Capability } from "@kario/test";
import { fmtDate, parseDate, today } from "../src/dates";
import { encodeUTF8 } from "../src/ics";
import zhHans from "../src/locale/zh-Hans.json";

// Where fake()'s locale.get says the phone is.
phoneZone("Asia/Shanghai");

// A message as the app shows it in Simplified Chinese (its catalog, the arguments in order).
const catalog = zhHans as Record<string, string>;
function zh(msg: string, ...args: unknown[]): string {
  let i = 0;
  return (catalog[msg] ?? msg).replace(/%[sd]/g, () => String(args[i++]));
}

const t = today();
const ICS = [
  "BEGIN:VCALENDAR",
  "VERSION:2.0",
  "BEGIN:VEVENT",
  "UID:selftest-1@kario",
  "DTSTART;TZID=Asia/Shanghai:20301015T090000",
  "DTEND;TZID=Asia/Shanghai:20301015T100000",
  "SUMMARY:kaihui",
  "RRULE:FREQ=WEEKLY;COUNT=3",
  "BEGIN:VALARM",
  "ACTION:DISPLAY",
  "TRIGGER:-PT30M",
  "END:VALARM",
  "END:VEVENT",
  "END:VCALENDAR",
].join("\r\n");

function fake(lang = "zh-Hans", extra: Record<string, Capability> = {}) {
  const scheduled = new Map<string, Record<string, unknown>>();
  const saved: { name: string; data: string }[] = [];
  const caps: Record<string, Capability> = {
    "locale.get": () => ({ lang, region: lang === "es" ? "ES" : "CN", zone: "Asia/Shanghai", clock_24h: true, first_weekday: 1, date_order: lang === "es" ? "dmy" : "ymd" }),
    "holidays.get": () => ({ region: "CN", source: "test fixture", days_off: [fmtDate(t + 3)], workdays: [fmtDate(t + 5)] }),
    "app.settings": () => ({ lunar: "With the language" }),
    "notify.list": () => [...scheduled.values()],
    "notify.schedule": (a: Record<string, unknown>) => {
      scheduled.set(String(a.id), a);
      return a;
    },
    "notify.cancel": (a: { id: string }) => ({ cancelled: Number(scheduled.delete(a.id)) }),
    "job.schedule": () => ({ ok: true }),
    "time.offsets": (a: { zone: string }) => ({ zone: a.zone, spans: [{ start: 0, offset: 8 * 3600 }] }),
    "file.pick": () => ({ id: "f1", name: "meeting.ics", mime: "text/calendar", size: ICS.length, at: 0, data: btoa(encodeUTF8(ICS)) }),
    "file.open": () => ({ id: "f9", name: "Calendar 2026-11-17.ics", mime: "text/calendar", size: ICS.length, at: 0, data: btoa(encodeUTF8(ICS)) }),
    "file.save": (a: { name: string; data: string }) => {
      saved.push(a);
      return { id: "f2", name: a.name, mime: "text/calendar", size: a.data.length, at: 0 };
    },
    "calendar.pull": () => ({ account: "", items: [] }),
    "calendar.push": () => ({ account: "", pending: 0 }),
    ...extra,
  };
  return { caps, scheduled, saved };
}

test("the month: Monday first, today marked, holidays, the lunar date; keys move the day and the month", async () => {
  const f = fake();
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  app.at("month");
  expectEqual(app.screen.body.slice(0, 7).map((n) => n.title), ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"].map((h) => zh(h)), "the heads");
  expectEqual(app.focus, "d:" + fmtDate(t), "today has the focus");
  expectEqual(app.find("d:" + fmtDate(t))?.subtitle, zh("Today"), "today is marked");
  expectEqual(app.find("d:" + fmtDate(t + 3))?.subtitle?.startsWith(zh("Off")), true, "a day off");
  expectEqual(app.find("d:" + fmtDate(t + 5))?.subtitle?.startsWith(zh("Work")), true, "an adjusted workday");
  expectEqual(f.scheduled.size, 0, "nothing to remind of");
  await app.key("right");
  expectEqual(app.focus, "d:" + fmtDate(t + 1), "→ the next day");
  await app.key("left");
  await app.key("left");
  expectEqual(app.focus, "d:" + fmtDate(t - 1), "← the day before");
  const title = app.screen.title;
  await app.key("#");
  expectEqual(app.screen.title !== title, true, "# the next month");
  await app.key("*");
  expectEqual(app.screen.title, title, "* back");
  await app.key("0");
  expectEqual(app.focus, "d:" + fmtDate(t), "0 today");
  await app.key("enter");
  app.at("day");
  app.expect(zh("Today"));
  app.expect(catalog["Lunar %s %s, %s%s year of the %s"].split("%s")[0]);
  app.expect(zh("No events"));
  await app.key("right");
  app.expect(zh("Tomorrow"));
});

test("midnight with the app on screen: today's mark and the agenda move to the new day, the day selected stays", async () => {
  const realNow = Date.now;
  const g = globalThis as unknown as { setTimeout: (fn: () => void, ms?: number) => unknown };
  const realTimeout = g.setTimeout;
  // The app's timers of a minute or more, run by hand.
  let timers: (() => void)[] = [];
  g.setTimeout = (fn, ms = 0) => (ms >= 60000 ? timers.push(fn) : realTimeout(fn, ms));
  let now = new Date(2030, 9, 15, 23, 50).getTime();
  Date.now = () => now;
  const d0 = parseDate("2030-10-15")!;
  const ev = (id: string, d: number) => ({ id, uid: id + "@kario", title: id, allDay: false, start: fmtDate(d) + "T10:00", end: fmtDate(d) + "T11:00", zone: "Asia/Shanghai", location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "" });
  try {
    const f = fake();
    const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([ev("e1", d0 + 1), ev("e2", d0 + 2)]), seq: "2" } });
    app.at("month");
    expectEqual(app.find("d:" + fmtDate(d0))?.subtitle, zh("Today"), "today is marked");
    now = new Date(2030, 9, 16).getTime();
    const due = timers;
    timers = [];
    due.forEach((fn) => fn());
    await app.settle();
    expectEqual(app.find("d:" + fmtDate(d0 + 1))?.subtitle, zh("Today"), "past midnight, the next day is today");
    expectEqual(app.find("d:" + fmtDate(d0))?.subtitle === zh("Today"), false, "the day before is not");
    expectEqual(app.focus, "d:" + fmtDate(d0), "the day selected stays");
    await app.key("2");
    app.at("agenda");
    app.expect(zh("%d events in the next %d days", 2, 30));
    // The phone slept past the next midnight: the timer is late, the screen coming back on is not.
    now = new Date(2030, 9, 17, 7, 0).getTime();
    await app.event("focus", "gained");
    app.expect(zh("%d events in the next %d days", 1, 30));
  } finally {
    Date.now = realNow;
    g.setTimeout = realTimeout;
  }
});

test("a new event with a reminder: the system is asked to remind ten minutes before", async () => {
  // At 10:55 the next full hour is too close for a reminder ten minutes
  // before it: the event starts at 12:00.
  await at(10, 55, newEventWithReminder);
});

async function newEventWithReminder(): Promise<void> {
  const f = fake();
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.key("1");
  app.at("edit");
  app.focusOn("title");
  await app.fill("standup");
  await app.key("f2");
  app.at("month");
  expectEqual(app.toasts.at(-1), zh("Saved"), "saved");
  expectEqual(f.scheduled.size, 1, "one reminder");
  const r = [...f.scheduled.values()][0];
  expectEqual([r.title, r.zone], ["standup", "Asia/Shanghai"], "its notification");
  const start = new Date(Date.now());
  start.setHours(12, 0, 0, 0);
  const at = new Date(start.getTime() - 10 * 60000);
  const pad = (n: number) => String(n).padStart(2, "0");
  expectEqual(r.at, `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`, "ten minutes before 12:00");
  expectEqual(String(r.arg).startsWith("event:e1:"), true, "it opens the event");
  const sd = fmtDate(today(start.getTime()));
  expectEqual(app.find("d:" + sd)?.badge, "1", "the day has an event");
  const again = await app.relaunch();
  again.at("month");
  expectEqual(again.find("d:" + sd)?.badge, "1", "kept in storage");
  await again.event("open", String(r.arg));
  again.at("event");
  again.expect("standup");
  again.expect(zh("Reminders: %s", zh("%d minutes before", 10)));
}

// An event on day d at 10:00, reminding ten minutes before.
const timed = (id: string, d: number, more: Record<string, unknown> = {}) => ({
  id, uid: id + "@kario", title: id, allDay: false, start: fmtDate(d) + "T10:00", end: fmtDate(d) + "T11:00", zone: "Asia/Shanghai",
  location: "", notes: "", alarms: [10], rrule: "", exdates: [], recurrenceId: "", src: "", ...more,
});

// The Shell slow to list the reminders (a busy phone): notify.list answers
// only once the returned function is called.
function slowList(f: ReturnType<typeof fake>): () => void {
  const held: (() => void)[] = [];
  const list = f.caps["notify.list"];
  f.caps["notify.list"] = (a) => new Promise((r) => held.push(() => r(list(a))));
  return () => {
    f.caps["notify.list"] = list;
    for (const r of held.splice(0)) r();
  };
}

const cancels = (app: { calls: readonly { cap: string; args: unknown }[] }) => app.calls.filter((c) => c.cap === "notify.cancel").map((c) => (c.args as { id: string }).id);
const reminderIds = (f: ReturnType<typeof fake>, arg: string) => [...f.scheduled.values()].filter((r) => String(r.arg).startsWith(arg)).map((r) => String(r.id));

test("a deleted or moved event's reminder is taken back before the next page shows, however slow the Shell is to list them", async () => {
  const f = fake();
  const d = t + 2;
  const weeklyKey = fmtDate(d + 7) + "T10:00";
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([timed("gone", d), timed("moved", d), timed("weekly", d, { rrule: "FREQ=WEEKLY" })]), seq: "3" } });
  await app.settle();
  const [gone] = reminderIds(f, "event:gone:");
  const [moved] = reminderIds(f, "event:moved:");
  const [second] = reminderIds(f, "event:weekly:" + weeklyKey);
  const weekly = reminderIds(f, "event:weekly:");
  expectEqual([!!gone, !!moved, !!second, weekly.length], [true, true, true, 5], "the reminders scheduled");
  const release = slowList(f);
  await app.event("open", "event:gone:");
  app.at("event");
  await app.choose(zh("Delete"));
  app.at("month");
  expectEqual(cancels(app), [gone], "deleted: its reminder taken back with the next page on screen");
  await app.event("open", "event:moved:");
  await app.choose(zh("Edit"));
  app.at("edit");
  app.focusOn("st");
  await app.fill("12:00");
  app.focusOn("et");
  await app.fill("13:00");
  await app.key("f2");
  app.at("month");
  expectEqual(cancels(app), [gone, moved], "moved: the old time's reminder taken back");
  await app.event("open", "event:weekly:" + weeklyKey);
  await app.choose(zh("Delete this occurrence"));
  app.at("month");
  expectEqual(cancels(app), [gone, moved, second], "one occurrence deleted: its reminder alone");
  release();
  await app.settle();
  expectEqual([reminderIds(f, "event:gone:"), reminderIds(f, "event:weekly:").filter((id) => weekly.includes(id) && id !== second).length], [[], 4], "the sync brings none back");
  const now = reminderIds(f, "event:moved:");
  expectEqual([now.length, now[0] !== moved, String(f.scheduled.get(now[0])?.at).endsWith("T11:50")], [1, true, true], "the new time's reminder scheduled");
});

test("the server deleting an event, or its organizer cancelling it, takes its reminder back at once", async () => {
  const sys = fakeSystem();
  const d = fmtDate(t + 2);
  sys.add({ uid: "web-1", ics: serverEvent("web-1", "from-the-web", d.replace(/-/g, "")).replace("END:VEVENT", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT10M\r\nEND:VALARM\r\nEND:VEVENT") });
  const party = timed("party", t + 3, { uid: "party@example.com", organizer: { addr: "mailto:fang@example.com", name: "Fang" }, attendees: [{ addr: "mailto:lei@example.com", name: "Lei", partstat: "ACCEPTED" }] });
  const cancel = "BEGIN:VCALENDAR\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:party@example.com\r\nSEQUENCE:2\r\nORGANIZER:mailto:fang@example.com\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n";
  const f = fake("zh-Hans", { ...sys.caps, "file.open": (a: { id: string }) => ({ id: a.id, name: "can.ics", mime: "text/calendar", size: cancel.length, at: 0, data: btoa(cancel) }) });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([party]), seq: "1" } });
  await app.settle();
  const web = (JSON.parse(app.storage.events) as { id: string; uid: string }[]).find((e) => e.uid === "web-1")!.id;
  const [fromWeb] = reminderIds(f, `event:${web}:`);
  const [partyId] = reminderIds(f, "event:party:");
  expectEqual([!!fromWeb, !!partyId], [true, true], "the reminders scheduled");
  const release = slowList(f);
  sys.add({ uid: "web-1", gone: true });
  await app.event("open", "calendar.sync:");
  await app.settle();
  expectEqual(cancels(app), [fromWeb], "deleted on the server: its reminder taken back");
  await app.event("open", "share:file:can");
  app.at("message");
  await app.choose(zh("Keep it, marked cancelled"));
  expectEqual(cancels(app), [fromWeb, partyId], "cancelled by the organizer and kept: no reminder");
  release();
  await app.settle();
  expectEqual(f.scheduled.size, 0, "the sync brings none back");
});

test("a weekly event, one occurrence changed and one deleted", async () => {
  await at(10, 0, weeklyEvent);
});

// at runs f with the clock at h:m today: a new event's default start (the
// next full hour at least half an hour away) is tomorrow's after 23:30.
async function at(h: number, m: number, f: () => Promise<void>): Promise<void> {
  const realNow = Date.now;
  const now = new Date();
  now.setHours(h, m, 0, 0);
  Date.now = () => now.getTime();
  try {
    await f();
  } finally {
    Date.now = realNow;
  }
}

async function weeklyEvent(): Promise<void> {
  const f = fake();
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.key("1");
  app.focusOn("title");
  await app.fill("weekly");
  await app.press("repeat");
  app.at("choose");
  await app.press(zh("Every week"));
  app.at("edit");
  app.expect(zh("Every week"));
  await app.key("f2");
  await app.key("2");
  app.at("agenda");
  app.expect(zh("%d events in the next %d days", 5, 30));
  await app.press("a1");
  app.at("event");
  app.expect(zh("Every week"));
  expectEqual(
    app.menu().slice(0, 6),
    ["Edit this occurrence", "Edit this and following occurrences", "Edit all occurrences", "Delete this occurrence", "Delete this and following occurrences", "Delete all occurrences"].map((m) => zh(m)),
    "this one, this and the following, or all",
  );
  await app.choose(zh("Edit this occurrence"));
  app.at("edit");
  app.focusOn("title");
  await app.fill("weekly-moved");
  await app.key("f2");
  app.at("agenda");
  app.expect("weekly-moved");
  app.expect(zh("%d events in the next %d days", 5, 30));
  await app.press("a2");
  await app.choose(zh("Delete this occurrence"));
  app.at("agenda");
  app.expect(zh("%d events in the next %d days", 4, 30));
  expectEqual(f.scheduled.size, 5, "a reminder for each occurrence left in the next 35 days");
}

test("this and following: a weekly event changed from its third occurrence on, then deleted from its fourth", async () => {
  await at(10, 0, async () => {
    const f = fake();
    const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
    await app.key("1");
    app.focusOn("title");
    await app.fill("series");
    await app.press("repeat");
    await app.press(zh("Every week"));
    await app.key("f2");
    await app.key("2");
    app.at("agenda");
    await app.press("a2");
    app.at("event");
    await app.choose(zh("Edit this and following occurrences"));
    app.at("edit");
    app.expect(zh("Edit this and following occurrences"));
    app.focusOn("title");
    await app.fill("series-b");
    await app.key("f2");
    app.at("agenda");
    expectEqual([0, 1, 2, 3, 4].map((i) => app.find("a" + i)?.title), ["series", "series", "series-b", "series-b", "series-b"], "the first two keep the old title");
    const stored = JSON.parse(app.storage.events) as { uid: string; rrule: string; title: string }[];
    expectEqual(stored.length, 2, "two series");
    expectEqual([stored[0].rrule.startsWith("FREQ=WEEKLY;UNTIL="), stored[1].rrule, stored[0].uid !== stored[1].uid], [true, "FREQ=WEEKLY", true], "the old one ends; the new one has its own UID");
    await app.press("a3");
    await app.choose(zh("Delete this and following occurrences"));
    app.at("agenda");
    app.expect(zh("%d events in the next %d days", 3, 30));
    expectEqual(f.scheduled.size, 3, "reminders for what is left");
  });
});

test("a rule the editor cannot say (BYHOUR) is shown and kept as it is", async () => {
  const ics = "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:twice@x\r\nDTSTART:20301015T090000\r\nDURATION:PT30M\r\nSUMMARY:twice\r\nRRULE:FREQ=DAILY;BYHOUR=9,17;COUNT=4\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n";
  const f = fake("zh-Hans", { "file.pick": () => ({ id: "f3", name: "twice.ics", mime: "text/calendar", size: ics.length, at: 0, data: btoa(ics) }) });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.key("4");
  await app.press("go");
  await app.event("open", "day:2030-10-15");
  app.at("day");
  expectEqual([app.find("o0")?.subtitle, app.find("o1")?.subtitle], ["09:00–09:30", "17:00–17:30"], "twice on the day");
  await app.press("o1");
  app.expect(zh("%s, %d times", zh("%s, at %s", zh("Every day"), "09:00" + zh(", ") + "17:00"), 4));
  await app.choose(zh("Edit all occurrences"));
  app.at("edit");
  app.expect(zh("This repetition comes from another calendar and is kept as it is: the editor cannot change it"));
  expectEqual(app.find("ends"), undefined, "no Ends to change");
  app.focusOn("title");
  await app.fill("twice-renamed");
  await app.key("f2");
  const stored = JSON.parse(app.storage.events) as { rrule: string; title: string }[];
  expectEqual([stored[0].title, stored[0].rrule], ["twice-renamed", "FREQ=DAILY;BYHOUR=9,17;COUNT=4"], "the rule kept");
});

test("a file's own zone (VTIMEZONE): the times on the phone's clock, kept over a restart, written back on export", async () => {
  const ics = [
    "BEGIN:VCALENDAR",
    "BEGIN:VTIMEZONE",
    "TZID:Customized Time Zone",
    "BEGIN:STANDARD",
    "DTSTART:16010101T020000",
    "TZOFFSETFROM:-0400",
    "TZOFFSETTO:-0500",
    "RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=11",
    "END:STANDARD",
    "BEGIN:DAYLIGHT",
    "DTSTART:16010101T020000",
    "TZOFFSETFROM:-0500",
    "TZOFFSETTO:-0400",
    "RRULE:FREQ=YEARLY;BYDAY=2SU;BYMONTH=3",
    "END:DAYLIGHT",
    "END:VTIMEZONE",
    "BEGIN:VEVENT",
    "UID:own-zone@x",
    'DTSTART;TZID="Customized Time Zone":20301015T090000',
    'DTEND;TZID="Customized Time Zone":20301015T100000',
    "SUMMARY:ownzone",
    "END:VEVENT",
    "END:VCALENDAR",
  ].join("\r\n");
  // The fake system gives +08:00 for every IANA zone: none has these rules.
  const f = fake("zh-Hans", { "file.pick": () => ({ id: "f4", name: "own.ics", mime: "text/calendar", size: ics.length, at: 0, data: btoa(ics) }) });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.key("4");
  await app.press("go");
  await app.event("open", "day:2030-10-15");
  expectEqual(app.find("o0")?.subtitle, "21:00–22:00", "09:00 at -04:00 is 21:00 in Shanghai");
  const again = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { ...app.storage } });
  await again.event("open", "day:2030-10-15");
  expectEqual(again.find("o0")?.subtitle, "21:00–22:00", "the zone kept with the events");
  await again.press("o0");
  again.expect(zh("%s in %s", "2030-10-15 09:00", "Customized Time Zone"));
  await again.choose(zh("Export to Files"));
  const text = atob(f.saved.at(-1)!.data);
  expectEqual(["BEGIN:VTIMEZONE", "TZID:Customized Time Zone", "DTSTART;TZID=Customized Time Zone:20301015T090000"].every((l) => text.includes(l)), true, text);
});

// The system's side of the account's calendar sync: an inbox the app pulls
// from (calendar.pull) and what the app pushes (calendar.push).
function fakeSystem(account = "a1", schedule?: string[]) {
  const inbox: { uid: string; ics?: string; gone?: boolean; copy?: boolean; first?: boolean; seq: number }[] = [];
  const pushed: { uid: string; ics?: string; gone?: boolean; all?: boolean }[] = [];
  const pulls: { taken?: number[]; full?: boolean }[] = [];
  let seq = 0;
  let wantAll = true;
  const sys = {
    inbox,
    pushed,
    pulls,
    add: (it: { uid: string; ics?: string; gone?: boolean; copy?: boolean; first?: boolean }) => inbox.push({ ...it, seq: ++seq }),
    /** The user signs out of the account (or turns its calendar off): the system's answers say there is none. */
    signOut: () => {
      account = "";
      schedule = undefined;
      inbox.length = 0;
    },
    caps: {
      "calendar.pull": (a: { taken?: number[]; full?: boolean }) => {
        pulls.push(a ?? {});
        for (const s of a?.taken ?? []) inbox.splice(inbox.findIndex((x) => x.seq === s), 1);
        return { account, items: a?.full ? [] : inbox.slice(0, 2), more: inbox.length > 2, want_all: wantAll, ...(schedule ? { schedule: true, addresses: schedule } : {}) };
      },
      "calendar.push": (a: { items: typeof pushed; done?: boolean }) => {
        pushed.push(...a.items);
        if (a.done) wantAll = false;
        return { account, pending: 0 };
      },
    } as Record<string, Capability>,
  };
  return sys;
}

const serverEvent = (uid: string, summary: string, day = "20301020") =>
  `BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:${uid}\r\nDTSTART;TZID=Asia/Shanghai:${day}T090000\r\nDTEND;TZID=Asia/Shanghai:${day}T100000\r\nSUMMARY:${summary}\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n`;

test("the account's calendar: the server's events come in, the phone's go out, a conflict's copy is pointed out, signing out takes the account's off", async () => {
  const sys = fakeSystem();
  sys.add({ uid: "web-1", ics: serverEvent("web-1", "from-the-web") });
  sys.add({ uid: "web-2", ics: serverEvent("web-2", "second-from-the-web") });
  sys.add({ uid: "web-3", ics: serverEvent("web-3", "third-from-the-web") });
  const f = fake("zh-Hans", sys.caps);
  const own = { id: "e1", uid: "phone-1@kario", title: "phone-made", allDay: false, start: "2030-10-21T09:00", end: "2030-10-21T10:00", zone: "Asia/Shanghai", location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "" };
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([own]), seq: "1" } });
  await app.settle();
  const stored = () => JSON.parse(app.storage.events) as { uid: string; title: string; src: string; id: string }[];
  expectEqual(stored().map((e) => e.title).sort(), ["from-the-web", "phone-made", "second-from-the-web", "third-from-the-web"], "the server's events in pages, the phone's kept");
  expectEqual(sys.inbox.length, 0, "all taken");
  expectEqual([sys.pushed.length, sys.pushed[0].uid, sys.pushed[0].all, sys.pushed[0].ics?.includes("SUMMARY:phone-made")], [1, "phone-1@kario", true, true], "the first sync's hand-over");
  expectEqual(stored().find((e) => e.uid === "web-1")?.src, "account:a1", "marked as the account's");
  // A new event on the phone goes to the system at once; one taken from the server does not go back.
  await app.key("1");
  app.focusOn("title");
  await app.fill("new-on-phone");
  await app.key("f2");
  expectEqual(sys.pushed.at(-1)?.ics?.includes("SUMMARY:new-on-phone"), true, "pushed");
  const n = sys.pushed.length;
  // The system wakes the app: a change, a deletion, and the phone's edit kept as a copy.
  sys.add({ uid: "web-1", ics: serverEvent("web-1", "renamed-on-the-web") });
  sys.add({ uid: "web-2", gone: true });
  sys.add({ uid: "copy-9", ics: serverEvent("copy-9", "phone-made-copy"), copy: true });
  const web1 = stored().find((e) => e.uid === "web-1")!.id;
  await app.event("open", "calendar.sync:");
  await app.settle();
  expectEqual(stored().find((e) => e.uid === "web-1")?.title, "renamed-on-the-web", "changed");
  expectEqual(stored().find((e) => e.uid === "web-1")?.id, web1, "the same ID for the same event");
  expectEqual(stored().some((e) => e.uid === "web-2"), false, "deleted on the server");
  expectEqual(app.notifications.at(-1)?.body, zh("“%s” was changed on another device too: your change is kept as a separate event", "phone-made-copy"), "the copy is pointed out");
  expectEqual(sys.pushed.length, n, "nothing taken goes back");
  // Signing out with the account's events: those from the server go, the phone's stay.
  await app.event("open", "calendar.undo:account:a1");
  expectEqual(stored().map((e) => e.title).sort(), ["new-on-phone", "phone-made"], "the phone's own stay");
});

test("the account's calendar: an event the app has no room for waits in the system", async () => {
  const sys = fakeSystem();
  const many = Array.from({ length: 2000 }, (_, i) => ({ id: "e" + i, uid: "u" + i, title: "x", allDay: true, start: "2030-01-01", end: "2030-01-02", zone: "", location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "" }));
  sys.add({ uid: "web-1", ics: serverEvent("web-1", "no-room") });
  const f = fake("zh-Hans", { ...sys.caps, "calendar.push": () => ({ account: "a1", pending: 0 }) });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify(many), seq: "2000" } });
  await app.settle();
  expectEqual([sys.inbox.length, sys.pulls.at(-1)?.full], [1, true], "left in the system, the app said it is full");
  expectEqual(JSON.parse(app.storage.events).length, 2000, "nothing went past the bound");
});

test("an invitation shared to the calendar: organizer and attendees, which one the user is, accept, the REPLY shared; a CANCEL removes it", async () => {
  const invitation = [
    "BEGIN:VCALENDAR",
    "METHOD:REQUEST",
    "BEGIN:VEVENT",
    "UID:party@example.com",
    "SEQUENCE:1",
    "DTSTART;TZID=Asia/Shanghai:20301104T190000",
    "DTEND;TZID=Asia/Shanghai:20301104T210000",
    "SUMMARY:party",
    "ORGANIZER;CN=Fang:mailto:fang@example.com",
    "ATTENDEE;CN=Fang;PARTSTAT=ACCEPTED:mailto:fang@example.com",
    "ATTENDEE;CN=Lei;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:lei@example.com",
    "END:VEVENT",
    "END:VCALENDAR",
  ].join("\r\n");
  const cancel = "BEGIN:VCALENDAR\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:party@example.com\r\nSEQUENCE:2\r\nORGANIZER:mailto:fang@example.com\r\nSTATUS:CANCELLED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n";
  const files: Record<string, string> = { inv: invitation, can: cancel };
  const shared: unknown[] = [];
  const f = fake("zh-Hans", {
    "file.open": (a: { id: string }) => ({ id: a.id, name: a.id + ".ics", mime: "text/calendar", size: 1, at: 0, data: btoa(files[a.id]) }),
    "share.open": (a: unknown) => {
      shared.push(a);
      return {};
    },
  });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.event("open", "share:file:inv");
  app.at("invite");
  app.expect("party");
  app.expect(zh("Organizer: %s", "Fang <fang@example.com>"));
  app.expect(zh("Which of the attendees are you? OK on your name"));
  expectEqual(app.menu().includes(zh("Accept")), false, "no answer before the user says who they are");
  await app.press("at1");
  app.expect(zh("Your answer: %s", zh("Not answered yet")));
  await app.choose(zh("Accept"));
  expectEqual(app.toasts.at(-1), zh("Your answer: %s", zh("Accepted")), "answered");
  const stored = JSON.parse(app.storage.events) as { title: string; attendees: { addr: string; partstat: string }[] }[];
  expectEqual([stored.length, stored[0].attendees[1].partstat], [1, "ACCEPTED"], "in the calendar with the answer");
  const reply = atob(f.saved.at(-1)!.data);
  expectEqual(["METHOD:REPLY", "UID:party@example.com", "ATTENDEE;CN=\"Lei\";PARTSTAT=ACCEPTED:mailto:lei@example.com", "SEQUENCE:1"].every((l) => reply.includes(l)), true, reply);
  expectEqual(shared, [{ kind: "file", file: "f2" }], "the REPLY goes to the system's share sheet");
  // The next invitation knows who the user is.
  await app.event("open", "share:file:inv");
  app.at("invite");
  app.expect(zh("Already in the calendar: the answer updates it"));
  expectEqual(app.menu().slice(-3), [zh("Accept"), zh("Tentative"), zh("Decline")], "the answers");
  await app.back();
  await app.event("open", "share:file:can");
  app.at("message");
  app.expect(zh("Cancelled by the organizer"));
  await app.choose(zh("Remove from the calendar"));
  expectEqual(JSON.parse(app.storage.events).length, 0, "removed");
});

test("import from Files: a summary first; search finds it; export writes iCalendar", async () => {
  const f = fake();
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.key("4");
  app.at("import");
  app.expect(zh("%d new events", 1));
  app.expect("kaihui");
  await app.press("go");
  app.at("month");
  expectEqual(app.toasts.at(-1), zh("Imported %d events", 1), "imported");
  await app.key("4");
  app.expect(zh("%d already in the calendar", 1));
  await app.back();
  await app.key("3");
  app.at("search");
  await app.fill("kai");
  await app.key("f2");
  const q = app.find("q")!;
  await app.press(q.id!, "changed").catch(() => undefined);
  await app.relaunch();
  const again = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { ...app.storage } });
  await again.key("3");
  again.focusOn("q");
  await again.type("kai");
  again.expect("kaihui");
  again.expect("2030-10-15 09:00–10:00");
  await again.press("s0");
  again.at("event");
  again.expect(zh("%s, %d times", zh("Every week on %s", zh("Tuesday")), 3));
  again.expect(zh("%d minutes before", 30));
  await again.choose(zh("Export to Files"));
  expectEqual(f.saved.length, 1, "saved to Files");
  expectEqual(f.saved[0].name, "kaihui.ics", "its name");
  const text = atob(f.saved[0].data);
  expectEqual(["DTSTART;TZID=Asia/Shanghai:20301015T090000", "RRULE:FREQ=WEEKLY;COUNT=3", "TRIGGER:-PT30M"].every((l) => text.includes(l)), true, text);
});

test("the system's import (moving from an old phone) and its undo", async () => {
  const f = fake();
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, arg: "calendar.import:f9" });
  await app.settle();
  expectEqual(app.notifications.at(-1)?.body, zh("Imported %d events from “%s”", 1, "Calendar 2026-11-17.ics"), "a notification says so");
  await app.key("3");
  app.focusOn("q");
  await app.type("kai");
  app.expect("kaihui");
  await app.event("open", "calendar.import:f9");
  expectEqual(JSON.parse(app.storage.events).length, 1, "imported once only");
  await app.event("open", "calendar.undo:f9");
  expectEqual(JSON.parse(app.storage.events).length, 0, "taken out again");
});

test("Spanish: no lunar dates, Monday first by the region, the month in Spanish", async () => {
  const f = fake("es");
  const app = await launch({ lang: "es", capabilities: f.caps });
  expectEqual(app.screen.body.slice(0, 7).map((n) => n.title), ["L", "M", "X", "J", "V", "S", "D"], "the heads");
  expectEqual(app.screen.title.includes(" de "), true, app.screen.title);
  expectEqual(app.find("d:" + fmtDate(t))?.subtitle, "Hoy", "today");
  expectEqual(app.find("d:" + fmtDate(t + 1))?.subtitle ?? "", "", "no lunar date");
  expectEqual(app.menu().includes("Nuevo evento"), true, "the menu");
});

test("the contacts' birthdays and anniversaries: yearly all-day events, the age, kept nowhere; hidden by the setting", async () => {
  const [, m, d] = fmtDate(t).split("-");
  const dates = [
    { id: "c1", name: "Ann", birthday: `--${m}-${d}` },
    { id: "c2", name: "Bob", birthday: `${Number(fmtDate(t).slice(0, 4)) - 30}-${m}-${d}`, anniversary: `2001-${m}-${d}` },
  ];
  const f = fake("zh-Hans", { "contacts.dates": () => dates });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.settle();
  expectEqual(app.find("d:" + fmtDate(t))?.badge, "3", "two birthdays and an anniversary today");
  await app.key("enter");
  app.at("day");
  app.expect(zh("%s's birthday", "Ann"));
  app.expect(zh("%s's anniversary", "Bob"));
  const bob = ["o0", "o1", "o2"].find((id) => app.find(id)?.title === zh("%s's birthday", "Bob"))!;
  await app.press(bob);
  app.at("event");
  app.expect(zh("Turns %d", 30));
  app.expect(zh("From Contacts: change it there. The calendar shows it every year and keeps no copy"));
  expectEqual(app.menu().length, 0, "nothing to edit, delete or export");
  expectEqual(app.storage.events, undefined, "not in the calendar's storage");
  expectEqual(f.scheduled.size, 0, "no reminder");
  const hidden = await launch({ lang: "zh-Hans", capabilities: { ...f.caps, "app.settings": () => ({ lunar: "With the language", birthdays: "Hide" }) } });
  await hidden.settle();
  expectEqual(hidden.find("d:" + fmtDate(t))?.badge, undefined, "hidden");
});

test("the user's own event: attendees added and taken out, the invitation and the cancellation shared, the attendees marked for the server, a new time numbered, deleting sends a cancellation", async () => {
  const sys = fakeSystem();
  const shared: unknown[] = [];
  const f = fake("zh-Hans", { ...sys.caps, "share.open": (a: unknown) => (shared.push(a), {}) });
  const own = { id: "e1", uid: "kick@kario", title: "kickoff", allDay: false, start: "2030-10-21T09:00", end: "2030-10-21T10:00", zone: "Asia/Shanghai", location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "" };
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([own]), seq: "1" } });
  await app.settle();
  await app.event("open", "event:e1:");
  app.at("event");
  await app.choose(zh("Invite people"));
  app.at("attendees");
  app.focusOn("me");
  await app.fill("me@kario.test");
  app.focusOn("who");
  await app.fill("Li Lei <lei@example.com>");
  await app.press("addbtn");
  expectEqual(app.toasts.at(-1), zh("Added %s", "Li Lei"), "added");
  app.focusOn("who");
  await app.fill("mei@example.com");
  await app.press("addbtn");
  app.expect(zh("You organize it as %s", "me@kario.test"));
  const stored = () => JSON.parse(app.storage.events)[0] as { organizer: { addr: string }; attendees: { addr: string }[]; seq?: number };
  expectEqual([stored().organizer.addr, stored().attendees.map((a) => a.addr)], ["mailto:me@kario.test", ["mailto:me@kario.test", "mailto:lei@example.com", "mailto:mei@example.com"]], "the user organizes it");
  expectEqual(sys.pushed.at(-1)?.ics?.replace(/\r\n /g, "").includes("SCHEDULE-AGENT=CLIENT:mailto:mei@example.com"), true, "the account's server is told the phone sends the invitations");
  await app.press("at1");
  expectEqual(stored().attendees.map((a) => a.addr), ["mailto:me@kario.test", "mailto:lei@example.com"], "Mei taken out");
  app.expect(zh("Taken out, not told yet: %s", "mei@example.com"));
  await app.choose(zh("Send the invitation"));
  const req = atob(f.saved.at(-1)!.data).replace(/\r\n /g, "");
  expectEqual(["METHOD:REQUEST", "ORGANIZER:mailto:me@kario.test", "mailto:lei@example.com"].every((l) => req.includes(l)) && !req.includes("mei@") && !req.includes("SCHEDULE-AGENT"), true, req);
  expectEqual(shared.length, 1, "shared");
  await app.choose(zh("Send the cancellation to those taken out"));
  const can = atob(f.saved.at(-1)!.data).replace(/\r\n /g, "");
  expectEqual(["METHOD:CANCEL", "STATUS:CANCELLED", "mailto:mei@example.com"].every((l) => can.includes(l)) && !can.includes("lei@"), true, can);
  await app.back();
  app.at("event");
  app.expect(zh("Organizer: you (%s)", "me@kario.test"));
  expectEqual(app.menu().includes(zh("Answer the invitation")), false, "the organizer does not answer");
  await app.choose(zh("Edit"));
  app.at("edit");
  app.focusOn("st");
  await app.fill("11:00");
  app.focusOn("et");
  await app.fill("12:00");
  await app.key("f2");
  expectEqual(app.toasts.at(-1), zh("Saved: send the attendees the new invitation (the event's Options)"), "the new time is to be sent");
  expectEqual(stored().seq, 1, "SEQUENCE moved on");
  await app.event("open", "event:e1:");
  await app.choose(zh("Delete"));
  const gone = atob(f.saved.at(-1)!.data).replace(/\r\n /g, "");
  expectEqual(["METHOD:CANCEL", "UID:kick@kario", "SEQUENCE:2", "mailto:lei@example.com"].every((l) => gone.includes(l)), true, gone);
  expectEqual(shared.length, 3, "the cancellation shared");
});

test("the account's server schedules: the user is the account's address, invitations and cancellations go without files, the delivery and the answers come back and are pointed out, an invitation answered through the server", async () => {
  const sys = fakeSystem("a1", ["mailto:me@kario.test"]);
  const shared: unknown[] = [];
  const f = fake("zh-Hans", { ...sys.caps, "share.open": (a: unknown) => (shared.push(a), {}) });
  const own = { id: "e1", uid: "kick@kario", title: "kickoff", allDay: false, start: "2030-10-21T09:00", end: "2030-10-21T10:00", zone: "Asia/Shanghai", location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "" };
  const before = { ...own, id: "e2", uid: "old@kario", title: "invited-by-file", organizer: { addr: "mailto:me@kario.test", name: "" }, attendees: [{ addr: "mailto:fang@example.com", name: "", partstat: "ACCEPTED" }] };
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([own, before]), seq: "2" } });
  await app.settle();
  const old = sys.pushed.find((p) => p.uid === "old@kario")?.ics?.replace(/\r\n /g, "") ?? "";
  expectEqual(old.includes("SCHEDULE-AGENT=CLIENT:mailto:fang@example.com"), true, "the first hand-over keeps the invitations sent as files the phone's: " + old);
  await app.event("open", "event:e1:");
  app.at("event");
  await app.choose(zh("Invite people"));
  app.at("attendees");
  expectEqual(app.screen.body.some((n) => n.id === "me"), false, "no address to type: the account's");
  app.expect(zh("You organize it as %s", "me@kario.test"));
  app.expect(zh("The account's server sends the invitations, updates and cancellations by itself"));
  app.focusOn("who");
  await app.fill("Li Lei <lei@example.com>");
  await app.press("addbtn");
  const sent = sys.pushed.at(-1)?.ics?.replace(/\r\n /g, "") ?? "";
  expectEqual([sent.includes("ORGANIZER:mailto:me@kario.test"), sent.includes("mailto:lei@example.com"), sent.includes("SCHEDULE-AGENT")], [true, true, false], sent);
  expectEqual(app.menu().includes(zh("Send the invitation")), false, "no file to send");
  expectEqual(f.saved.length, 0, "nothing saved to Files");
  // The server's copy comes back with the delivery, then with the answer.
  const copy = (lei: string) =>
    `BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:kick@kario\r\nDTSTART;TZID=Asia/Shanghai:20301021T090000\r\nDTEND;TZID=Asia/Shanghai:20301021T100000\r\nSUMMARY:kickoff\r\n` +
    `ORGANIZER:mailto:me@kario.test\r\nATTENDEE;PARTSTAT=ACCEPTED;ROLE=CHAIR:mailto:me@kario.test\r\nATTENDEE;CN=Li Lei;${lei}:mailto:lei@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n`;
  sys.add({ uid: "kick@kario", ics: copy("PARTSTAT=NEEDS-ACTION;SCHEDULE-STATUS=1.2") });
  await app.event("open", "calendar.sync:");
  await app.settle();
  await app.event("open", "event:e1:");
  expectEqual(app.find("at1")?.subtitle, zh("Not answered yet") + " · " + zh("Invitation delivered"), "delivered");
  app.expect(zh("The account's server sends the invitations and updates"));
  sys.add({ uid: "kick@kario", ics: copy("PARTSTAT=ACCEPTED;SCHEDULE-STATUS=1.2") });
  await app.event("open", "calendar.sync:");
  await app.settle();
  expectEqual(app.notifications.at(-1)?.body, zh("Answers to “%s”: %s", "kickoff", "Li Lei: " + zh("Accepted")), "the answer is pointed out");
  // Someone invites the user through the server; the answer goes the same way. The first sync's are not pointed out.
  const notes = app.notifications.length;
  sys.add({ uid: "was@web", first: true, ics: serverEvent("was@web", "was-there").replace("END:VEVENT", "ORGANIZER:mailto:fang@example.com\r\nATTENDEE:mailto:me@kario.test\r\nEND:VEVENT") });
  await app.event("open", "calendar.sync:");
  await app.settle();
  expectEqual(app.notifications.length, notes, "nothing pointed out of the first sync");
  sys.add({
    uid: "party@web",
    ics: "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:party@web\r\nDTSTART;TZID=Asia/Shanghai:20301104T190000\r\nDTEND;TZID=Asia/Shanghai:20301104T210000\r\nSUMMARY:party\r\n" +
      "ORGANIZER;CN=Fang:mailto:fang@example.com\r\nATTENDEE;CN=Fang;PARTSTAT=ACCEPTED:mailto:fang@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:me@kario.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
  });
  await app.event("open", "calendar.sync:");
  await app.settle();
  expectEqual(app.notifications.at(-1)?.body, zh("%s invites you to “%s”", "Fang", "party"), "the invitation is pointed out");
  const party = (JSON.parse(app.storage.events) as { id: string; uid: string }[]).find((e) => e.uid === "party@web")!;
  await app.event("open", `event:${party.id}:`);
  await app.choose(zh("Answer the invitation"));
  app.at("invite");
  app.expect(zh("Your answer goes to the organizer through the account's server"));
  await app.choose(zh("Accept"));
  const answer = sys.pushed.at(-1)?.ics?.replace(/\r\n /g, "") ?? "";
  expectEqual([answer.includes("PARTSTAT=ACCEPTED:mailto:me@kario.test"), answer.includes("ORGANIZER;SCHEDULE-AGENT")], [true, false], answer);
  expectEqual([f.saved.length, shared.length], [0, 0], "no REPLY file");
  // Deleting the user's own event: the server tells the attendees, no file.
  await app.event("open", "event:e1:");
  await app.choose(zh("Delete"));
  expectEqual([sys.pushed.at(-1)?.uid, sys.pushed.at(-1)?.gone, f.saved.length], ["kick@kario", true, 0], "deleted without a file");
  expectEqual(app.storage.me, undefined, "the account's address is not remembered as the user's");
});

test("signing out of an account whose server schedules: the calendar open on an event's attendees organizes by files again at once, whether the account's events go or stay", async () => {
  for (const link of ["calendar.undo:account:a1", "calendar.sync:"]) {
    const sys = fakeSystem("a1", ["mailto:me@kario.test"]);
    const f = fake("zh-Hans", sys.caps);
    const own = { id: "e1", uid: "kick@kario", title: "kickoff", allDay: false, start: "2030-10-21T09:00", end: "2030-10-21T10:00", zone: "Asia/Shanghai", location: "", notes: "", alarms: [], rrule: "", exdates: [], recurrenceId: "", src: "" };
    const app = await launch({ lang: "zh-Hans", capabilities: f.caps, storage: { events: JSON.stringify([own]), seq: "1" } });
    await app.settle();
    await app.event("open", "event:e1:");
    await app.choose(zh("Invite people"));
    app.at("attendees");
    app.expect(zh("You organize it as %s", "me@kario.test"));
    sys.signOut();
    await app.event("open", link);
    app.at("attendees");
    expectEqual(app.find("me")?.kind, "input", link + ": the user's own address is asked for again");
    app.expectGone(zh("You organize it as %s", "me@kario.test"));
    app.expect(zh("Invitations go as a file you share (message, Bluetooth…): no e-mail is sent"));
    app.focusOn("me");
    await app.fill("me@kario.test");
    app.focusOn("who");
    await app.fill("lilei@kario.test");
    await app.press("addbtn");
    expectEqual(app.menu().includes(zh("Send the invitation")), true, link + ": the invitation is a file to send");
  }
});

test("signing out while a pull is under way: what the pull brought from the account is not kept", async () => {
  const sys = fakeSystem();
  let zoneAsked = (): void => undefined;
  const zone = new Promise<void>((r) => (zoneAsked = r));
  const f = fake("zh-Hans", {
    ...sys.caps,
    "time.offsets": async (a: { zone: string }) => {
      if (a.zone === "America/New_York") await zone;
      return { zone: a.zone, spans: [{ start: 0, offset: -5 * 3600 }] };
    },
  });
  const app = await launch({ lang: "zh-Hans", capabilities: f.caps });
  await app.settle();
  sys.add({ uid: "ny@web", ics: serverEvent("ny@web", "new-york").replace(/TZID=Asia\/Shanghai/g, "TZID=America/New_York") });
  await app.event("open", "calendar.sync:");
  sys.signOut();
  await app.event("open", "calendar.undo:account:a1");
  zoneAsked();
  await app.settle();
  expectEqual((JSON.parse(app.storage.events ?? "[]") as { title: string }[]).map((e) => e.title), [], "the account's event is not on the phone");
});
