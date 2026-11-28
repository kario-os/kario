// Calendar, with KarioUI. The month is a grid of days the system moves in
// (← → at its edges and * # step the month, 0 is today); a day lists its
// events; an event shows when, where, how it repeats and its reminders.
// Reminders are the system's scheduled notifications: the calendar keeps
// the next ones scheduled (on every change, at start, from a background
// job twice a day and after each one), so they come while the app sleeps.
// Chinese adds the lunar date and the solar terms under each day, and the
// region's public holidays and adjusted workdays mark the days. The
// contacts' birthdays and anniversaries come as yearly all-day events
// (birthdays.ts). An event the user organizes gets attendees here, and the
// invitation or its cancellation goes out as a file, or, where the
// account's server schedules, the server sends it (itip.ts).
import { act, app, Button, call, cancelled, DateInput, Input, Item, lang, nav, notify, page, refresh, Section, state, storage, Text, Tile, TimeInput, toast, tr } from "@kario/ui";
import type { ActionEvent, Child, Node, Page, View } from "@kario/ui";
import type { ContactDate, FileItem, LocaleInfo, ScheduledNotif } from "kario";
import { CONTACTS_SRC, dateEvents, yearsIn } from "./birthdays";
import { addMinutes, addMonths, day, type Day, daysInMonth, fmtDate, fmtTime, getPhoneZone, hasZone, missingZones, parseDate, parseTime, parseWall, setPhoneZone, setZoneSpans, toInstant, today, toWall, weekday, ymd } from "./dates";
import { alarmText, dateDigits, dateLong, monthTitle, occText, ruleText, setLocale, timeText } from "./fmt";
import { decodeUTF8, encodeUTF8, ianaZone, type Message, parseICS, ruleOf, serialiseICS, untilText, vtimezones, zonesNamed } from "./ics";
import { type Attendee, clip, MAX_ATTENDEES, MAX_LOCATION, MAX_NOTES, type CalEvent, type Person } from "./model";
import { festival, lunarInRange, lunarLong, lunarShort, WEEKDAY_HEADS } from "./names";
import { beyondEditor, formatRule, lastStart, type Freq, type Rule } from "./recur";
import { Calendar, keyMinutes, type Occ, type Reminder, reminders, repeats, shiftKey, span } from "./store";
import { Sync } from "./sync";
import { answered, type Answer, applyCancel, applyReply, invitees, occurrenceEvent, organizerICS, organizes, personOf, replyICS, sameAddress, serverSchedules, target, whoAmI, withAttendee, withoutAttendee } from "./itip";
import { matchZone, ownZone, type VZone } from "./vtz";

const cal = new Calendar();
cal.load();
const sync = new Sync(cal, {
  pull: (args) => call("calendar.pull", args),
  push: (args) => call("calendar.push", args),
  read: async (text) => {
    const r = await readText(text, "sync");
    return { events: r.events, zones: r.zones };
  },
  persist: () => persist(),
  copied: (title) => notify({ id: "sync-copy", title: tr("Calendar"), body: tr("“%s” was changed on another device too: your change is kept as a separate event", title || tr("(No title)")) }),
  me: () => mine(),
  answered: (uid, title, who) =>
    notify({
      id: "answers:" + uid,
      title: tr("Calendar"),
      body: tr("Answers to “%s”: %s", title || tr("(No title)"), who.map((a) => `${a.name || a.addr.replace(/^mailto:/i, "")}: ${partstatText(a.partstat)}`).join(tr(", "))),
    }),
  invited: (uid, title, from) => notify({ id: "invite:" + uid, title: tr("Calendar"), body: tr("%s invites you to “%s”", from, title || tr("(No title)")) }),
  changed: () => refresh(),
});

// Whether the account's server sends an event's invitations, updates and answers (RFC 6638).
function scheduled(e: CalEvent): boolean {
  return serverSchedules(e, sync.schedule && !!sync.account, mine());
}

function syncNow(): void {
  sync.pull().catch((e: unknown) => console.warn(`calendar sync: ${errText(e)}`));
}

const zh = lang.startsWith("zh");
let loc: LocaleInfo = { lang, region: "", zone: "", clock_24h: true, first_weekday: zh || lang === "es" ? 1 : 0, date_order: zh ? "ymd" : "dmy" };
let lunarSetting = "With the language";
let birthdaySetting = "Show";
let contactDates: ContactDate[] = [];
const holidays = { off: new Set<string>(), work: new Set<string>(), source: "" };
let ready: Promise<void> = Promise.resolve();

function lunarOn(): boolean {
  return lunarSetting === "Show" || (lunarSetting !== "Hide" && zh);
}

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

// The month shown and the day selected in it (kept for a tombstone).
const t0 = today();
const view = state({ y: ymd(t0)[0], m: ymd(t0)[1], sel: t0 }, { save: "view" });

function select(d: Day): void {
  const [y, m] = ymd(d);
  view.set({ y, m, sel: d });
  if (nav.top === "month") nav.replace(Month, undefined);
}

function focusedDay(e: ActionEvent): Day {
  return (e.node.startsWith("d:") && parseDate(e.node.slice(2))) || view.value.sel;
}

function counts(from: Day, to: Day): Map<Day, number> {
  const out = new Map<Day, number>();
  for (const o of cal.occurrences(from, to)) {
    const last = o.ev.allDay || (o.end.min === 0 && o.end.day > o.start.day) ? o.end.day - 1 : o.end.day;
    for (let d = Math.max(o.start.day, from); d <= Math.min(last, to); d++) out.set(d, (out.get(d) ?? 0) + 1);
  }
  return out;
}

function mark(d: Day): string {
  const k = fmtDate(d);
  if (holidays.off.has(k)) return tr("Off");
  if (holidays.work.has(k)) return tr("Work");
  return "";
}

// What a day of the grid shows under its number.
function subtitle(d: Day, t: Day): string {
  if (d === t) return tr("Today");
  const m = mark(d);
  return lunarOn() ? m + lunarShort(d) : m;
}

function newEvent(d: Day): void {
  const now = Date.now();
  let sd = d;
  let st = 9 * 60;
  if (d === today(now)) {
    // The next full hour at least half an hour away, so that the default
    // reminder is still ahead.
    const next = new Date(now + 30 * 60000);
    next.setMinutes(0, 0, 0);
    next.setHours(next.getHours() + 1);
    sd = day(next.getFullYear(), next.getMonth() + 1, next.getDate());
    st = next.getHours() * 60;
  }
  const end = addMinutes({ day: sd, min: st }, 60);
  edit({
    id: "", key: "", scope: "new", title: "", allDay: false, sd: fmtDate(sd), st: fmtTime(st), ed: fmtDate(end.day), et: fmtTime(end.min),
    zone: getPhoneZone(), location: "", notes: "", repeat: "", ends: "never", until: "", count: "", alarms: [10, null], rule: "",
  });
}

const Month = page("month", () => {
  const v = view.value;
  const fw = loc.first_weekday;
  const first = day(v.y, v.m, 1);
  const lead = (weekday(first) - fw + 7) % 7;
  const start = first - lead;
  const rows = Math.ceil((lead + daysInMonth(v.y, v.m)) / 7);
  const end = start + rows * 7 - 1;
  const n = counts(start, end);
  const t = today();
  const heads = Array.from({ length: 7 }, (_, i) => Tile(tr(WEEKDAY_HEADS[(fw + i) % 7]), { id: "w" + i }));
  const days = Array.from({ length: rows * 7 }, (_, i) => {
    const d = start + i;
    const inMonth = ymd(d)[1] === v.m;
    const c = n.get(d) ?? 0;
    return Tile(String(ymd(d)[2]), {
      id: "d:" + fmtDate(d),
      subtitle: inMonth || d === t ? subtitle(d, t) : mark(d),
      badge: c ? (c > 9 ? "9+" : String(c)) : undefined,
      onOk: act("open", tr("Open the day"), () => nav.push(DayPage, { d: fmtDate(d) })),
    });
  });
  let marked = false;
  for (let d = first; d < first + daysInMonth(v.y, v.m); d++) marked ||= mark(d) !== "";
  const newAct = act("new", tr("New event"), (e) => newEvent(focusedDay(e)), { key: "1" });
  return {
    title: monthTitle(v.y, v.m),
    columns: 7,
    focus: "d:" + fmtDate(v.sel),
    body: [
      heads,
      Section(""),
      days,
      marked && holidays.source ? Text(tr("Days off and workdays: %s", holidays.source), { style: "dim" }) : null,
      Text(tr("OK opens the day · * # change the month · 0 today"), { style: "dim" }),
    ],
    actions: [
      newAct,
      act("agenda", tr("Upcoming"), () => nav.push(Agenda, undefined), { key: "2" }),
      act("search", tr("Search events"), () => nav.push(Search, undefined), { key: "3" }),
      act("today", tr("Today"), () => select(today()), { key: "0" }),
      act("prev", tr("Previous month"), (e) => select(addMonths(focusedDay(e), -1)), { key: "*" }),
      act("next", tr("Next month"), (e) => select(addMonths(focusedDay(e), 1)), { key: "#" }),
      act("import", tr("Import from Files"), () => importPick(), { key: "4" }),
      act("export", tr("Export all to Files"), () => exportEvents(cal.events, tr("Calendar") + " " + fmtDate(today())), { key: "5" }),
      // The grid's own arrows move inside the month; at its edges these step over into the next.
      act("left", tr("Previous day"), (e) => select(focusedDay(e) - 1), { key: "left", hidden: true }),
      act("right", tr("Next day"), (e) => select(focusedDay(e) + 1), { key: "right", hidden: true }),
    ],
    right: newAct,
  };
});

function relative(d: Day, t: Day): string {
  if (d === t) return tr("Today");
  if (d === t + 1) return tr("Tomorrow");
  if (d === t - 1) return tr("Yesterday");
  return d > t ? tr("In %d days", d - t) : tr("%d days ago", t - d);
}

function occItem(o: Occ, id: string, withDate = false): Node {
  const where = o.ev.location ? " · " + o.ev.location : "";
  const title = o.ev.title || tr("(No title)");
  return Item(o.ev.status === "CANCELLED" ? tr("Cancelled: %s", title) : title, {
    id,
    subtitle: (withDate ? dateDigits(o.start.day) + " " : "") + occText(o) + where,
    trailing: o.ev.rrule || o.ev.recurrenceId ? "↻" : undefined,
    onOk: act("open", tr("Open"), () => nav.push(EventPage, { id: o.ev.id, key: o.key })),
  });
}

const DayPage: Page<{ d: string }> = page<{ d: string }>("day", (p): View => {
  const d = parseDate(p.d) ?? today();
  const t = today();
  const occs = cal.onDay(d);
  const k = fmtDate(d);
  const newAct = act("new", tr("New event"), () => newEvent(d), { key: "1" });
  const go = (to: Day): void => nav.replace(DayPage, { d: fmtDate(to) });
  return {
    title: dateLong(d, ymd(t)[0]),
    body: [
      Text(relative(d, t), { style: "accent" }),
      lunarOn() && lunarInRange(d) ? Text(lunarLong(d)) : null,
      lunarOn() && festival(d) ? Text(festival(d), { style: "accent" }) : null,
      holidays.off.has(k) ? Text(tr("Public holiday: a day off"), { style: "ok" }) : null,
      holidays.work.has(k) ? Text(tr("Adjusted workday"), { style: "warn" }) : null,
      (holidays.off.has(k) || holidays.work.has(k)) && holidays.source ? Text(tr("Holiday calendar: %s", holidays.source), { style: "dim" }) : null,
      Section(tr("Events")),
      occs.map((o, i) => occItem(o, `o${i}`)),
      occs.length ? null : Text(tr("No events"), { style: "dim" }),
    ],
    actions: [
      newAct,
      act("prevday", tr("Previous day"), () => go(d - 1), { key: "left" }),
      act("nextday", tr("Next day"), () => go(d + 1), { key: "right" }),
      act("today", tr("Today"), () => go(today()), { key: "0" }),
    ],
    right: newAct,
  };
});

const EventPage = page<{ id: string; key: string }>("event", (p) => {
  const e = cal.get(p.id);
  if (!e) return { title: tr("Event"), body: [Text(tr("This event is no longer in the calendar"), { style: "dim" })] };
  const o = cal.occAt(e, p.key) ?? cal.next(e, today());
  if (e.src === CONTACTS_SRC) return contactDateView(e, o);
  const rule = e.recurrenceId ? undefined : ruleOf(e.rrule, e.zone);
  const sp = span(e)!;
  const last = rule?.until ? rule.until.day : undefined;
  const repeating = repeats(e) || !!e.recurrenceId;
  const organizing = organizes(e, mine()) && e.status !== "CANCELLED";
  const invited = invitees(e);
  const own = e.zone && e.zone !== getPhoneZone() && !e.allDay && o ? parseWall(o.key) : undefined;
  const master = cal.master(e) ?? e;
  const at = e.recurrenceId || (o?.key ?? e.start);
  const actions = repeating
    ? [
        act("editone", tr("Edit this occurrence"), () => editEvent(e, o?.key ?? e.start, "one"), { key: "1" }),
        act("editfollow", tr("Edit this and following occurrences"), () => editEvent(master, at, cal.split(master, at) ? "following" : "all"), { key: "3" }),
        act("editall", tr("Edit all occurrences"), () => editEvent(master, o?.key ?? e.start, "all"), { key: "2" }),
        act("delone", tr("Delete this occurrence"), () => deleteOne(e, o?.key ?? e.start), { group: "danger", confirm: tr("Delete this occurrence?"), key: "d" }),
        act("delfollow", tr("Delete this and following occurrences"), () => deleteFollowing(master, at), { group: "danger", confirm: tr("Delete this and every following occurrence?") }),
        act("delall", tr("Delete all occurrences"), () => deleteAll(master), { group: "danger", confirm: tr("Delete every occurrence of “%s”?", e.title) }),
      ]
    : [
        act("edit", tr("Edit"), () => editEvent(e, e.start, "all"), { key: "1" }),
        act("delete", tr("Delete"), () => deleteAll(e), { group: "danger", confirm: tr("Delete “%s”?", e.title), key: "d" }),
      ];
  actions.push(act("export", tr("Export to Files"), () => exportEvents([e, ...cal.events.filter((x) => x !== e && x.uid === e.uid)], e.title || tr("Event")), { key: "5" }));
  if (organizing) {
    actions.push(act("invite", invited.length ? tr("Attendees") : tr("Invite people"), () => nav.push(AttendeesPage, { id: e.id }), { key: "7" }));
  }
  if (organizing && invited.length && !scheduled(e)) {
    actions.push(act("send", tr("Send the invitation"), () => void shareInvite(e.uid), { key: "8" }));
  }
  if (!organizing && e.organizer && e.attendees?.length && e.status !== "CANCELLED") {
    actions.push(act("answer", tr("Answer the invitation"), () => {
      invite = { events: cal.group(e.uid), zones: cal.zones };
      nav.push(InvitePage, undefined);
    }, { key: "6" }));
  }
  return {
    title: e.title || tr("(No title)"),
    body: [
      o ? Text(dateLong(o.start.day, ymd(today())[0]), { style: "title" }) : null,
      o ? Text(occText(o)) : null,
      own ? Text(tr("%s in %s", `${dateDigits(own.day)} ${timeText(own.min)}`, cal.zoneName(e.zone)), { style: "dim" }) : null,
      !e.allDay && !e.zone ? Text(tr("Floating time: follows the phone's time zone"), { style: "dim" }) : null,
      repeating ? Text(e.recurrenceId ? tr("A changed occurrence of a repeating event") : ruleText(rule, sp.start.day, last, sp.start.min)) : null,
      e.rdates?.length && !e.recurrenceId ? Text(tr("And on %s", rdatesText(e))) : null,
      e.location ? Text(tr("Location: %s", e.location)) : null,
      e.alarms.length ? Text(tr("Reminders: %s", e.alarms.map((a) => alarmText(a, e.allDay)).join(tr(", ")))) : Text(tr("No reminders"), { style: "dim" }),
      e.status === "CANCELLED" ? Text(tr("Cancelled by the organizer"), { style: "warn" }) : null,
      e.organizer ? Text(organizing ? tr("Organizer: you (%s)", personText(e.organizer)) : tr("Organizer: %s", personText(e.organizer))) : null,
      e.attendees?.length ? Section(tr("Attendees")) : null,
      (e.attendees ?? []).slice(0, 20).map((a, i) => Item(a.name || a.addr.replace(/^mailto:/i, ""), { id: "at" + i, subtitle: attendeeText(a, organizing) })),
      e.attendees?.length && scheduled(e) ? Text(organizing ? tr("The account's server sends the invitations and updates") : tr("Your answer goes to the organizer through the account's server"), { style: "dim" }) : null,
      e.notes ? Section(tr("Notes")) : null,
      e.notes ? Text(e.notes) : null,
    ],
    actions,
    right: actions[0],
  };
});

// An event's RDATEs on the phone's clock, the first few.
function rdatesText(e: CalEvent): string {
  const list = (e.rdates ?? []).slice(0, 5).map((k) => cal.occAt(e, k)).filter((o): o is Occ => !!o);
  const text = list.map((o) => dateDigits(o.start.day) + (e.allDay ? "" : " " + timeText(o.start.min))).join(tr(", "));
  return (e.rdates?.length ?? 0) > list.length ? text + "…" : text;
}

// The editor works on a draft (kept for a tombstone); typed text comes
// back with every action's fields.
interface Draft {
  id: string;
  key: string;
  scope: "new" | "all" | "one" | "following";
  title: string;
  allDay: boolean;
  sd: string;
  st: string;
  ed: string;
  et: string;
  zone: string;
  location: string;
  notes: string;
  repeat: "" | Freq;
  ends: "never" | "until" | "count";
  until: string;
  count: string;
  alarms: (number | null)[];
  /** A rule the choices here do not say (an import's): kept while Repeat is not changed. */
  rule: string;
}

const draft = state<Draft | null>(null, { save: "draft" });

function edit(d: Draft): void {
  draft.set(d);
  nav.push(Editor, undefined);
}

function editEvent(e: CalEvent, key: string, scope: "all" | "one" | "following"): void {
  const sp = span(e)!;
  let s = sp.start;
  if (scope !== "all") s = (e.allDay ? { day: parseDate(key) ?? s.day, min: 0 } : parseWall(key)) ?? s;
  const en = addMinutes(s, sp.minutes);
  const rrule = scope === "following" ? (cal.split(e, key)?.rest.rrule ?? e.rrule) : e.rrule;
  const rule = ruleOf(rrule, e.zone);
  const lastDay = e.allDay ? en.day - 1 : en.day;
  edit({
    id: e.id, key, scope, title: e.title, allDay: e.allDay, sd: fmtDate(s.day), st: fmtTime(s.min), ed: fmtDate(Math.max(s.day, lastDay)), et: fmtTime(en.min),
    zone: e.zone, location: e.location, notes: e.notes, repeat: rule?.freq ?? "", ends: rule?.count ? "count" : rule?.until ? "until" : "never",
    until: rule?.until ? fmtDate(rule.until.day) : "", count: rule?.count ? String(rule.count) : "", alarms: [e.alarms[0] ?? null, e.alarms[1] ?? null], rule: rrule,
  });
}

// What an action's fields say goes into the draft.
function absorb(e: ActionEvent): Draft {
  const d = { ...draft.value! };
  const f = e.fields;
  for (const k of ["title", "sd", "st", "ed", "et", "location", "notes", "until", "count"] as const) if (k in f) d[k] = f[k];
  draft.set(d);
  return d;
}

function choiceItem(id: string, label: string, value: string, what: string): Node {
  return Item(label, {
    id,
    subtitle: value,
    onOk: act("choose", tr("Change"), (e) => {
      absorb(e);
      nav.push(Choose, { what });
    }),
  });
}

const REPEATS: ["" | Freq, string][] = [
  ["", "Does not repeat"],
  ["DAILY", "Every day"],
  ["WEEKLY", "Every week"],
  ["MONTHLY", "Every month"],
  ["YEARLY", "Every year"],
];
const TIMED_ALARMS = [0, 5, 10, 15, 30, 60, 120, 1440, 2880];
const ALLDAY_ALARMS = [-540, 900, 2340, 9540];

const Editor = page("edit", () => {
  const d = draft.value;
  if (!d) return { title: tr("Event"), body: [Text(tr("Nothing to edit"), { style: "dim" })] };
  const save = act("save", tr("Save"), (e) => saveDraft(absorb(e)));
  const repeatLabel = tr(REPEATS.find((r) => r[0] === d.repeat)?.[1] ?? "Does not repeat");
  const endsLabel = { never: tr("Never"), until: tr("On a date"), count: tr("After a number of times") }[d.ends];
  const alarmLabel = (a: number | null) => (a === null ? tr("None") : alarmText(a, d.allDay));
  const kept = d.rule && d.repeat === (ruleOf(d.rule, d.zone)?.freq ?? "") ? ruleOf(d.rule, d.zone) : undefined;
  const fixed = d.scope !== "one" && beyondEditor(kept);
  const sd = parseDate(d.sd) ?? today();
  const title = { new: tr("New event"), one: tr("Edit this occurrence"), following: tr("Edit this and following occurrences"), all: tr("Edit event") }[d.scope];
  return {
    title,
    body: [
      Input("title", { placeholder: tr("Title"), value: d.title }),
      Item(tr("All day"), {
        id: "allday",
        subtitle: d.allDay ? tr("Yes") : tr("No"),
        onOk: act("toggle", tr("Change"), (e) => {
          const x = absorb(e);
          draft.set({ ...x, allDay: !x.allDay, alarms: [x.allDay ? 10 : -540, null] });
        }),
      }),
      Text(tr("Starts"), { style: "dim" }),
      DateInput("sd", { value: d.sd }),
      d.allDay ? null : TimeInput("st", { value: d.st }),
      Text(d.allDay ? tr("Last day") : tr("Ends"), { style: "dim" }),
      DateInput("ed", { value: d.ed }),
      d.allDay ? null : TimeInput("et", { value: d.et }),
      fixed ? Item(tr("Repeat"), { id: "repeat", subtitle: ruleText(kept, sd, kept?.until?.day, parseTime(d.st) ?? 0) }) : null,
      fixed ? Text(tr("This repetition comes from another calendar and is kept as it is: the editor cannot change it"), { style: "dim" }) : null,
      d.scope === "one" || fixed ? null : choiceItem("repeat", tr("Repeat"), kept && !simpleRule(d.rule) ? ruleText(kept, sd, undefined, parseTime(d.st) ?? 0) : repeatLabel, "repeat"),
      d.scope !== "one" && !fixed && d.repeat ? choiceItem("ends", tr("Ends"), endsLabel, "ends") : null,
      d.scope !== "one" && !fixed && d.repeat && d.ends === "until" ? DateInput("until", { value: d.until || d.sd }) : null,
      d.scope !== "one" && !fixed && d.repeat && d.ends === "count" ? Input("count", { placeholder: tr("How many times"), value: d.count, keyboard: "number" }) : null,
      choiceItem("alarm0", tr("Reminder"), alarmLabel(d.alarms[0]), "alarm0"),
      d.alarms[0] !== null ? choiceItem("alarm1", tr("Second reminder"), alarmLabel(d.alarms[1]), "alarm1") : null,
      d.allDay ? null : choiceItem("zone", tr("Time zone"), d.zone ? cal.zoneName(d.zone) : tr("Floating (follows the phone)"), "zone"),
      Input("location", { placeholder: tr("Location"), value: d.location }),
      Input("notes", { placeholder: tr("Notes"), value: d.notes }),
      Button(tr("Save"), save, { id: "savebtn" }),
    ],
    actions: [save],
    right: save,
  };
}, { onPop: () => draft.set(null) });

// A rule the editor's choices say as they are (every period, no BY parts).
function simpleRule(rrule: string): boolean {
  return /^FREQ=(DAILY|WEEKLY|MONTHLY|YEARLY)(;(COUNT|UNTIL)=[0-9TZ]+)?$/.test(rrule);
}

function choices(what: string, d: Draft): [string, string][] {
  switch (what) {
    case "repeat":
      return REPEATS.map(([v, l]) => [v, tr(l)]);
    case "ends":
      return [["never", tr("Never")], ["until", tr("On a date")], ["count", tr("After a number of times")]];
    case "alarm0":
    case "alarm1":
      return [["", tr("None")], ...(d.allDay ? ALLDAY_ALARMS : TIMED_ALARMS).map((a): [string, string] => [String(a), alarmText(a, d.allDay)])];
    case "zone": {
      const out: [string, string][] = [];
      if (getPhoneZone()) out.push([getPhoneZone(), tr("This phone's time zone (%s)", getPhoneZone())]);
      if (d.zone && d.zone !== getPhoneZone()) out.push([d.zone, cal.zoneName(d.zone)]);
      out.push(["", tr("Floating (follows the phone)")]);
      return out;
    }
  }
  return [];
}

const Choose = page<{ what: string }>(
  "choose",
  (p) => {
    const d = draft.value;
    if (!d) return { title: "", body: [] };
    const opts = choices(p.what, d);
    const cur = { repeat: d.repeat, ends: d.ends, alarm0: String(d.alarms[0] ?? ""), alarm1: String(d.alarms[1] ?? ""), zone: d.zone }[p.what];
    const title = { repeat: tr("Repeat"), ends: tr("Ends"), alarm0: tr("Reminder"), alarm1: tr("Second reminder"), zone: tr("Time zone") }[p.what] ?? "";
    return {
      title,
      focus: "c" + Math.max(0, opts.findIndex((o) => o[0] === cur)),
      body: opts.map(([v, l], i) =>
        Item(l, {
          id: "c" + i,
          badge: v === cur ? "✓" : undefined,
          onOk: act("pick", tr("Choose"), () => {
            const x = { ...draft.value! };
            if (p.what === "repeat") {
              x.repeat = v as Draft["repeat"];
              x.rule = "";
            } else if (p.what === "ends") {
              x.ends = v as Draft["ends"];
              x.rule = "";
            } else if (p.what === "alarm0" || p.what === "alarm1") {
              x.alarms = [...x.alarms];
              x.alarms[p.what === "alarm0" ? 0 : 1] = v === "" ? null : Number(v);
              if (x.alarms[0] === null) x.alarms[1] = null;
            } else if (p.what === "zone") x.zone = v;
            draft.set(x);
            nav.pop();
          }),
        }),
      ),
    };
  },
  { transient: true },
);

function saveDraft(d: Draft): void {
  const sd = parseDate(d.sd);
  const ed = parseDate(d.ed);
  const st = parseTime(d.st);
  const et = parseTime(d.et);
  if (sd === undefined || ed === undefined || (!d.allDay && (st === undefined || et === undefined))) {
    toast(tr("Give the start and the end"));
    return;
  }
  let start: string;
  let end: string;
  if (d.allDay) {
    if (ed < sd) return toast(tr("The event ends before it starts"));
    start = fmtDate(sd);
    end = fmtDate(ed + 1);
  } else {
    if (ed * 1440 + et! < sd * 1440 + st!) return toast(tr("The event ends before it starts"));
    start = `${fmtDate(sd)}T${fmtTime(st!)}`;
    end = `${fmtDate(ed)}T${fmtTime(et!)}`;
  }
  const zone = d.allDay ? "" : d.zone;
  let rrule = "";
  if (d.repeat) {
    const kept = ruleOf(d.rule, zone);
    if (kept && kept.freq === d.repeat && !simpleRule(d.rule)) rrule = d.rule;
    else {
      const r: Rule = { freq: d.repeat, interval: 1, wkst: 1 };
      if (d.ends === "count") {
        const n = Number(d.count);
        if (!Number.isInteger(n) || n < 1 || n > 1000) return toast(tr("Repeat 1 to 1000 times"));
        r.count = n;
      } else if (d.ends === "until") {
        const u = parseDate(d.until);
        if (u === undefined || u < sd) return toast(tr("The repetition ends before the event"));
        r.until = { day: u, min: 1439 };
      }
      rrule = formatRule(r, (w) => untilText(w, d.allDay, zone));
    }
  }
  const alarms = d.alarms.filter((a): a is number => a !== null).filter((a, i, all) => all.indexOf(a) === i);
  const old = d.id ? cal.get(d.id) : undefined;
  // An invitation's new time is another event for its attendees (RFC 5546 §2.1.4).
  const update = told(old) && (old.start !== start || old.end !== end || old.allDay !== d.allDay || old.zone !== zone || (d.scope !== "one" && old.rrule !== rrule));
  const fields = {
    title: d.title.trim(), allDay: d.allDay, start, end, zone, location: clip(d.location.trim(), MAX_LOCATION), notes: clip(d.notes.trim(), MAX_NOTES), alarms,
    ...(update ? { seq: (old!.seq ?? 0) + 1 } : {}),
  };
  if (d.scope === "new" || !old) {
    if (!cal.room()) return toast(tr("The calendar is full"));
    cal.put({ id: cal.newId(), uid: "", rrule, exdates: [], recurrenceId: "", src: "", ...fields });
  } else if (d.scope === "following" && cal.split(old, d.key)) {
    if (!cal.room()) return toast(tr("The calendar is full"));
    const plan = cal.split(old, d.key, cal.newId())!;
    const delta = old.allDay === d.allDay ? (keyMinutes(start, d.allDay) ?? 0) - (keyMinutes(d.key, old.allDay) ?? 0) : 0;
    cal.put(plan.old);
    cal.put({ ...plan.rest, ...fields, rrule, exdates: plan.rest.exdates.map((x) => shiftKey(x, old.allDay, delta)) });
    for (const m of plan.moved) {
      if (m.recurrenceId === d.key) cal.remove(m.id);
      else cal.put({ ...m, recurrenceId: shiftKey(m.recurrenceId, m.allDay, delta) });
    }
  } else if (d.scope === "all" || d.scope === "following") {
    cal.put({ ...old, ...fields, rrule: old.recurrenceId ? "" : rrule });
  } else if (old.recurrenceId) {
    cal.put({ ...old, ...fields });
  } else {
    if (!cal.room()) return toast(tr("The calendar is full"));
    cal.put({ id: cal.newId(), uid: old.uid, rrule: "", exdates: [], recurrenceId: d.key, src: old.src, ...fields });
  }
  if (!persist()) return;
  draft.set(null);
  nav.pop();
  if (nav.top === "event") nav.pop();
  toast(update && !scheduled(old) ? tr("Saved: send the attendees the new invitation (the event's Options)") : update ? tr("Saved: the attendees are told") : tr("Saved"));
}

function persist(): boolean {
  const err = cal.save();
  if (err) {
    toast(err === "full" ? tr("The calendar is full") : tr("Could not save: %s", err));
    cal.load();
    return false;
  }
  // The file is written in the background: a failure is known only later.
  storage.flush().catch((e: unknown) => toast(tr("Could not save: %s", e instanceof Error ? e.message : String(e))));
  sync.pushTouched().catch((e: unknown) => console.warn(`calendar sync: ${errText(e)}`));
  if (sync.full) syncNow();
  takeBack();
  syncReminders();
  refresh();
  return true;
}

// Whether the attendees of an event the user organizes are to hear of a change.
function told(e: CalEvent | undefined): e is CalEvent {
  return !!e && invitees(e).length > 0 && organizes(e, mine());
}

function deleteOne(e: CalEvent, key: string): void {
  const master = e.recurrenceId ? cal.master(e) : e;
  if (e.recurrenceId) cal.remove(e.id);
  const k = e.recurrenceId || key;
  const tell = told(master ?? e);
  const byServer = scheduled(master ?? e);
  const seq = ((master ?? e).seq ?? 0) + 1;
  const gone = master ? occurrenceEvent(master, e.recurrenceId ? e : cal.override(master, k), k) : e;
  if (master && !master.exdates.includes(k)) cal.put({ ...master, exdates: [...master.exdates, k], ...(tell ? { seq } : {}) });
  if (persist()) {
    nav.pop();
    toast(tr("Deleted"));
    if (tell && !byServer) void shareOrganizer("CANCEL", [{ ...gone, seq }], gone.title);
  }
}

function deleteFollowing(master: CalEvent, key: string): void {
  const plan = cal.split(master, key);
  if (!plan) return deleteAll(master);
  const tell = told(master);
  const byServer = scheduled(master);
  const old = tell ? { ...plan.old, seq: (master.seq ?? 0) + 1 } : plan.old;
  cal.put(old);
  for (const m of plan.moved) cal.remove(m.id);
  if (persist()) {
    nav.pop();
    toast(tr("Deleted"));
    if (tell && !byServer) void shareOrganizer("REQUEST", cal.group(master.uid), master.title);
  }
}

function deleteAll(e: CalEvent): void {
  const group = cal.group(e.uid);
  const tell = told(e) && !scheduled(e);
  cal.remove(e.id);
  if (persist()) {
    nav.pop();
    toast(tr("Deleted"));
    if (tell) void shareOrganizer("CANCEL", group.map((x) => ({ ...x, seq: (x.seq ?? 0) + 1 })), e.title);
  }
}

// The organizer's REQUEST or CANCEL saved to Files and handed to the system's share (only: the attendees a CANCEL goes to).
async function shareOrganizer(method: "REQUEST" | "CANCEL", events: CalEvent[], title: string, only?: Attendee[]): Promise<void> {
  if (!events.length) return;
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
  const what = method === "REQUEST" ? tr("Invitation") : tr("Cancellation");
  const name = (what + " " + (title || tr("Event"))).replace(/[\\/:*?"<>|\n]/g, " ").trim().slice(0, 50) + ".ics";
  try {
    const f = await call("file.save", { name, data: btoa(encodeUTF8(organizerICS(method, events, stamp, cal.zones, only))) });
    await call("share.open", { kind: "file", file: f.id });
  } catch (e) {
    if (!cancelled(e)) toast(errText(e));
  }
}

function shareInvite(uid: string): Promise<void> {
  const group = cal.group(uid);
  return shareOrganizer("REQUEST", group, group[0]?.title ?? "");
}

// The attendees taken out of an event on its page, not told yet.
const removed = state<{ uid: string; people: Attendee[] } | null>(null, { save: "removed" });

// The attendees of an event the user organizes: added by e-mail address,
// taken out, and the invitation and the cancellation sent.
const AttendeesPage = page<{ id: string }>("attendees", (p) => {
  const e = cal.get(p.id);
  if (!e) return { title: tr("Attendees"), body: [Text(tr("This event is no longer in the calendar"), { style: "dim" })] };
  const me = mine()[0] ?? "";
  const list = invitees(e);
  const byServer = scheduled(e) || (sync.schedule && !!sync.account && !e.organizer);
  const out = removed.value?.uid === e.uid && !byServer ? removed.value.people : [];
  const add = act("add", tr("Add the attendee"), (ev) => addAttendee(e.uid, ev.fields), { key: "1" });
  const send = act("send", tr("Send the invitation"), () => void shareInvite(e.uid), { key: "2" });
  const tell = act("tell", tr("Send the cancellation to those taken out"), () => {
    void shareOrganizer("CANCEL", cal.group(e.uid), e.title, out).then(() => removed.set(null));
  }, { key: "3" });
  return {
    title: tr("Attendees"),
    focus: me ? "who" : "me",
    body: [
      Text(e.title || tr("(No title)"), { style: "title" }),
      me ? Text(tr("You organize it as %s", me.replace(/^mailto:/i, "")), { style: "dim" }) : Input("me", { placeholder: tr("Your e-mail address, as the organizer"), keyboard: "email" }),
      Input("who", { placeholder: tr("Attendee: e-mail address, or Name <address>"), keyboard: "email", clear: true, onSubmit: (_, ev) => addAttendee(e.uid, ev.fields) }),
      Button(tr("Add the attendee"), add, { id: "addbtn" }),
      Section(tr("Attendees")),
      list.map((a, i) =>
        Item(a.name || a.addr.replace(/^mailto:/i, ""), {
          id: "at" + i,
          subtitle: attendeeText(a, true),
          onOk: act("remove", tr("Take out"), () => removeAttendee(e.uid, a, byServer), { group: "danger", confirm: tr("Take “%s” out of the event?", a.name || a.addr.replace(/^mailto:/i, "")) }),
        }),
      ),
      list.length ? null : Text(tr("No attendees yet"), { style: "dim" }),
      out.length ? Text(tr("Taken out, not told yet: %s", out.map((a) => a.name || a.addr.replace(/^mailto:/i, "")).join(tr(", "))), { style: "warn" }) : null,
      byServer
        ? Text(tr("The account's server sends the invitations, updates and cancellations by itself"), { style: "dim" })
        : Text(tr("Invitations go as a file you share (message, Bluetooth…): no e-mail is sent"), { style: "dim" }),
    ],
    actions: [add, ...(list.length && !byServer ? [send] : []), ...(out.length ? [tell] : [])],
    right: list.length && !byServer ? send : add,
  };
});

function addAttendee(uid: string, fields: Record<string, string>): void {
  let me = mine()[0] ?? "";
  if (!me) {
    const p = personOf(fields.me ?? "");
    if (!p) return toast(tr("Give your e-mail address first"));
    me = p.addr;
  }
  const who = personOf(fields.who ?? "");
  if (!who) return toast(tr("Give the attendee's e-mail address"));
  const group = cal.group(uid);
  if (!group.length) return;
  if ((group[0].attendees ?? []).some((a) => sameAddress(a.addr, who.addr))) return toast(tr("Already invited"));
  if ((group[0].attendees?.length ?? 0) + 2 > MAX_ATTENDEES) return toast(tr("At most %d attendees", MAX_ATTENDEES));
  remember(me);
  for (const e of group) cal.put(withAttendee(e, me, who));
  const r = removed.value;
  if (r?.uid === uid) removed.set({ uid, people: r.people.filter((a) => !sameAddress(a.addr, who.addr)) });
  if (persist()) toast(tr("Added %s", who.name || who.addr.replace(/^mailto:/i, "")));
}

function removeAttendee(uid: string, a: Attendee, byServer: boolean): void {
  for (const e of cal.group(uid)) cal.put(withoutAttendee(e, a.addr));
  const r = removed.value;
  if (!byServer) removed.set({ uid, people: [...(r?.uid === uid ? r.people : []), a] });
  if (persist()) toast(byServer ? tr("Taken out: the server tells them") : tr("Taken out"));
}

// A contact's birthday or anniversary (birthdays.ts): when, how many
// years, and where it comes from.
function contactDateView(e: CalEvent, o: Occ | undefined): View {
  const [kind, id] = [e.id.slice(0, e.id.indexOf(":")), e.id.slice(e.id.indexOf(":") + 1)];
  const c = contactDates.find((x) => x.id === id);
  const n = o ? yearsIn(kind === "birthday" ? c?.birthday : c?.anniversary, ymd(o.start.day)[0]) : undefined;
  return {
    title: e.title,
    body: [
      o ? Text(dateLong(o.start.day, ymd(today())[0]), { style: "title" }) : null,
      n !== undefined && n > 0 ? Text(kind === "birthday" ? tr("Turns %d", n) : tr("%d years", n)) : null,
      Text(tr("From Contacts: change it there. The calendar shows it every year and keeps no copy"), { style: "dim" }),
    ],
  };
}

async function readDates(): Promise<void> {
  contactDates = [];
  if (birthdaySetting !== "Hide") {
    try {
      contactDates = await call("contacts.dates");
    } catch (e) {
      console.warn(`contacts' dates: ${errText(e)}`);
    }
  }
  cal.virtual = dateEvents(contactDates, (name, kind) => (kind === "birthday" ? tr("%s's birthday", name) : tr("%s's anniversary", name)));
}

const Agenda = page("agenda", () => {
  const t = today();
  const days = 30;
  const occs = cal.occurrences(t, t + days - 1).filter((o) => o.start.day >= t);
  const body: Child[] = [Text(tr("%d events in the next %d days", occs.length, days), { style: "dim" })];
  let last = -1;
  occs.forEach((o, i) => {
    if (o.start.day !== last) {
      last = o.start.day;
      body.push(Section(dateLong(o.start.day, ymd(t)[0])));
    }
    body.push(occItem(o, "a" + i));
  });
  return { title: tr("Upcoming"), body };
});

const query = state("", { save: "query" });

const Search = page("search", () => {
  const t = today();
  const hits = cal.search(query.value, t);
  return {
    title: tr("Search events"),
    focus: "q",
    body: [
      Input("q", { placeholder: tr("Title, place or notes"), value: query, onChange: (v) => query.set(v), onSubmit: (v) => query.set(v) }),
      query.value.trim() && !hits.length ? Text(tr("No events found"), { style: "dim" }) : null,
      hits.map((o, i) => occItem(o, "s" + i, true)),
    ],
  };
});

// An import the user looks at before it goes in (from Files, or shared).
let pending: { name: string; events: CalEvent[]; problems: number; zones: Record<string, VZone> } | undefined;

const ImportPage = page<{ name: string }>(
  "import",
  () => {
    const p = pending;
    if (!p) return { title: tr("Import"), body: [Text(tr("Nothing to import"), { style: "dim" })] };
    const fresh = p.events.filter((e) => !cal.events.some((x) => e.uid && x.uid === e.uid && x.recurrenceId === e.recurrenceId));
    const go = act("import", tr("Import"), () => {
      cal.addZones(p.zones);
      const r = cal.merge(fresh, "");
      if (!persist()) return;
      pending = undefined;
      nav.pop();
      toast(r.full ? tr("Imported %d events; the calendar is full", r.added) : tr("Imported %d events", r.added));
    });
    return {
      title: tr("Import “%s”", p.name),
      body: [
        Text(tr("%d new events", fresh.length), { style: "title" }),
        p.events.length > fresh.length ? Text(tr("%d already in the calendar", p.events.length - fresh.length), { style: "dim" }) : null,
        p.problems ? Text(tr("%d could not be read and are left out", p.problems), { style: "warn" }) : null,
        fresh.length ? Button(tr("Import"), go, { id: "go" }) : null,
        fresh.slice(0, 20).map((e, i) => {
          const sp = span(e);
          return Item(e.title || tr("(No title)"), { id: "i" + i, subtitle: sp ? dateDigits(sp.start.day) + (e.allDay ? "" : " " + timeText(sp.start.min)) : "" });
        }),
      ],
      actions: fresh.length ? [go] : [],
      right: fresh.length ? go : undefined,
    };
  },
  { transient: true },
);

async function offsets(zone: string): Promise<{ start: number; offset: number }[] | undefined> {
  const now = Math.floor(Date.now() / 1000);
  try {
    return (await call("time.offsets", { zone, from: now - 3 * 366 * 86400, to: now + 20 * 366 * 86400 })).spans;
  } catch (e) {
    console.warn(`time zone ${zone}: ${errText(e)}`);
    return undefined;
  }
}

async function loadZones(zones: Iterable<string>): Promise<void> {
  for (const zone of missingZones([...zones].filter((z) => !ownZone(z)))) {
    const spans = await offsets(zone);
    if (spans) setZoneSpans(zone, spans);
  }
}

// The IANA zones a file's VTIMEZONEs are (for TZIDs the system does not know), by TZID.
async function matchZones(text: string): Promise<Record<string, string>> {
  const out: Record<string, string> = {};
  for (const [tzid, z] of vtimezones(text)) {
    const iana = ianaZone(tzid);
    if (iana && hasZone(iana)) continue;
    const hit = await matchZone(z, Date.now(), getPhoneZone(), offsets);
    if (hit) out[tzid] = hit;
  }
  await loadZones(Object.values(out));
  return out;
}

interface Read {
  events: CalEvent[];
  problems: number;
  zones: Record<string, VZone>;
  method: string;
  messages: Message[];
}

async function readICS(it: FileItem): Promise<Read> {
  const text = decodeUTF8(atob(it.data ?? ""));
  return readText(text, it.name);
}

async function readText(text: string, name: string): Promise<Read> {
  await loadZones(zonesNamed(text));
  const matched = await matchZones(text);
  let k = 0;
  const res = parseICS(text, () => "import" + ++k, matched);
  for (const p of res.problems) console.warn(`${name}:${p.line}: ${p.why}`);
  return { events: res.events, problems: res.problems.length, zones: res.zones, method: res.method, messages: res.messages };
}

// A file shows as what it is: an invitation to answer, answers or a
// cancellation for events here, or events to import.
async function showImport(it: FileItem): Promise<void> {
  const r = await readICS(it);
  if (r.method === "REQUEST" && r.events.some((e) => e.organizer)) {
    invite = { events: r.events, zones: r.zones };
    nav.push(InvitePage, undefined);
  } else if ((r.method === "REPLY" || r.method === "CANCEL") && r.messages.length) {
    message = { method: r.method, messages: r.messages };
    nav.push(MessagePage, undefined);
  } else {
    pending = { name: it.name, events: r.events, problems: r.problems, zones: r.zones };
    nav.push(ImportPage, { name: it.name });
  }
}

// The addresses the user answered invitations as (the attendee they are),
// the account's first where its server schedules.
function mine(): string[] {
  const kept = remembered();
  if (!sync.schedule || !sync.account) return kept;
  return [...sync.addresses, ...kept.filter((x) => !sync.addresses.some((a) => sameAddress(a, x)))];
}

function remembered(): string[] {
  const m = storage.get<string[]>("me");
  return Array.isArray(m) ? m : [];
}

// Remembers an address as the user's, first; not the account's, which the account says while its server schedules.
function remember(addr: string): void {
  if (sync.schedule && sync.addresses.some((a) => sameAddress(a, addr))) return;
  storage.set("me", [addr, ...remembered().filter((x) => !sameAddress(x, addr))].slice(0, 8));
}

function partstatText(p: string): string {
  return { ACCEPTED: tr("Accepted"), TENTATIVE: tr("Tentative"), DECLINED: tr("Declined"), DELEGATED: tr("Delegated") }[p] ?? tr("Not answered yet");
}

// An attendee's answer, and to the organizer what the account's server said of delivering the invitation (SCHEDULE-STATUS).
function attendeeText(a: Attendee, organizing: boolean): string {
  const d = a.delivery ?? "";
  if (!organizing || !d || a.partstat !== "NEEDS-ACTION") return partstatText(a.partstat);
  const sent = d === "1.2" ? tr("Invitation delivered") : d === "1.1" ? tr("Invitation sent by e-mail") : d.startsWith("1.") ? tr("Invitation on its way") : tr("Invitation not delivered");
  return partstatText(a.partstat) + " · " + sent;
}

function personText(p: Person | undefined): string {
  if (!p) return "";
  const addr = p.addr.replace(/^mailto:/i, "");
  return p.name && p.name !== addr ? `${p.name} <${addr}>` : addr;
}

// When an event is, on the phone's clock, for a page about a file.
function whenText(e: CalEvent): string {
  const sp = span(e);
  if (!sp) return "";
  if (e.allDay) {
    const last = sp.end.day - 1;
    return dateDigits(sp.start.day) + (last > sp.start.day ? " – " + dateDigits(last) : "");
  }
  const ms = toInstant(sp.start, e.zone);
  const s = toWall(ms, "");
  const t = toWall(ms + sp.minutes * 60000, "");
  return `${dateDigits(s.day)} ${timeText(s.min)}–${t.day === s.day ? "" : dateDigits(t.day) + " "}${timeText(t.min)}`;
}

// An invitation being answered (from a file, or an event of the calendar).
let invite: { events: CalEvent[]; zones: Record<string, VZone> } | undefined;

const InvitePage = page(
  "invite",
  () => {
    const p = invite;
    const e = p?.events.find((x) => !x.recurrenceId) ?? p?.events[0];
    if (!p || !e) return { title: tr("Invitation"), body: [Text(tr("Nothing to answer"), { style: "dim" })] };
    const me = whoAmI(e.attendees, mine());
    const have = cal.events.some((x) => x.uid === e.uid);
    const ans = (id: string, label: string, a: Answer, key: string) => act(id, label, () => void answerInvite(p, me!, a), { key });
    const actions = me ? [ans("accept", tr("Accept"), "ACCEPTED", "1"), ans("tentative", tr("Tentative"), "TENTATIVE", "2"), ans("decline", tr("Decline"), "DECLINED", "3")] : [];
    const rule = e.recurrenceId ? undefined : ruleOf(e.rrule, e.zone);
    return {
      title: tr("Invitation"),
      body: [
        Text(e.title || tr("(No title)"), { style: "title" }),
        Text(whenText(e)),
        rule ? Text(ruleText(rule, span(e)!.start.day, rule.until?.day, span(e)!.start.min)) : null,
        e.location ? Text(tr("Location: %s", e.location)) : null,
        Text(tr("Organizer: %s", personText(e.organizer))),
        p.events.length > 1 ? Text(tr("With %d changed occurrences", p.events.length - 1), { style: "dim" }) : null,
        have ? Text(tr("Already in the calendar: the answer updates it"), { style: "dim" }) : null,
        byServer(p) ? Text(tr("Your answer goes to the organizer through the account's server"), { style: "dim" }) : null,
        me ? Text(tr("Your answer: %s", partstatText(me.partstat))) : Text(tr("Which of the attendees are you? OK on your name"), { style: "warn" }),
        Section(tr("Attendees")),
        (e.attendees ?? []).map((a, i) =>
          Item(a.name || a.addr.replace(/^mailto:/i, ""), {
            id: "at" + i,
            subtitle: partstatText(a.partstat) + (me && a.addr === me.addr ? " · " + tr("You") : ""),
            onOk: me ? undefined : act("me", tr("This is me"), () => {
              remember(a.addr);
              refresh();
            }),
          }),
        ),
        actions.length ? Button(tr("Accept"), actions[0], { id: "accept" }) : null,
      ],
      actions,
      right: actions[0],
    };
  },
  { transient: true },
);

// An invitation the account's server answers for: one already in the calendar that it schedules.
function byServer(p: { events: CalEvent[] }): boolean {
  const old = cal.group(p.events[0]?.uid ?? "");
  return old.length > 0 && scheduled(old[0]);
}

async function answerInvite(p: { events: CalEvent[]; zones: Record<string, VZone> }, me: Attendee, a: Answer): Promise<void> {
  const uid = p.events[0].uid;
  const evs = answered(p.events, me.addr, a);
  const server = byServer(p);
  const old = cal.group(uid);
  if (a !== "DECLINED" && !cal.room(evs.length - old.length)) return toast(tr("The calendar is full"));
  remember(me.addr);
  cal.addZones(p.zones);
  for (const e of old) cal.remove(e.id);
  if (a !== "DECLINED") for (const e of evs) cal.put({ ...e, id: old.find((x) => x.recurrenceId === e.recurrenceId)?.id ?? cal.newId(), src: old[0]?.src ?? "" });
  if (!persist()) return;
  invite = undefined;
  nav.pop();
  toast(tr("Your answer: %s", partstatText(a)));
  if (server) return;
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
  const name = (tr("Reply") + " " + (evs[0].title || tr("Event"))).replace(/[\\/:*?"<>|\n]/g, " ").trim().slice(0, 50) + ".ics";
  try {
    const f = await call("file.save", { name, data: btoa(encodeUTF8(replyICS(evs, me, a, stamp))) });
    await call("share.open", { kind: "file", file: f.id });
  } catch (e) {
    if (!cancelled(e)) toast(errText(e));
  }
}

// Answers (REPLY) or a cancellation (CANCEL) for events of the calendar.
let message: { method: string; messages: Message[] } | undefined;

const MessagePage = page(
  "message",
  () => {
    const p = message;
    if (!p) return { title: tr("Invitation"), body: [Text(tr("Nothing to answer"), { style: "dim" })] };
    const reply = p.method === "REPLY";
    const known = p.messages.filter((m) => cal.group(m.uid).length);
    const title = (m: Message) => cal.group(m.uid)[0]?.title || m.title || tr("(No title)");
    const lines: Child[] = p.messages.map((m, i) => {
      const t = target(cal, m);
      const when = t.key ? " · " + tr("one occurrence: %s", t.key.replace("T", " ")) : "";
      if (!cal.group(m.uid).length) return Item(title(m), { id: "m" + i, subtitle: tr("Not in this calendar") });
      if (!reply) return Item(title(m), { id: "m" + i, subtitle: tr("Cancelled by the organizer") + when });
      return Item(title(m), { id: "m" + i, subtitle: m.attendees.map((a) => `${a.name || a.addr.replace(/^mailto:/i, "")}: ${partstatText(a.partstat)}`).join(tr(", ")) + when });
    });
    const done = (text: string) => {
      if (!persist()) return;
      message = undefined;
      nav.pop();
      toast(text);
    };
    const actions = !known.length
      ? []
      : reply
        ? [act("apply", tr("Update the event"), () => done(tr("%d answers updated", known.reduce((n, m) => n + applyReply(cal, m), 0))), { key: "1" })]
        : [
            act("remove", tr("Remove from the calendar"), () => {
              for (const m of known) applyCancel(cal, m, true);
              done(tr("Deleted"));
            }, { key: "1", group: "danger" }),
            act("mark", tr("Keep it, marked cancelled"), () => {
              for (const m of known) applyCancel(cal, m, false);
              done(tr("Marked cancelled"));
            }, { key: "2" }),
          ];
    return {
      title: reply ? tr("Answers to an invitation") : tr("Cancelled by the organizer"),
      body: [...lines, actions.length ? Button(actions[0].label, actions[0], { id: "go" }) : Text(tr("Nothing here to update"), { style: "dim" })],
      actions,
      right: actions[0],
    };
  },
  { transient: true },
);

async function importPick(): Promise<void> {
  try {
    await showImport(await call("file.pick", { types: ["ics", "text/calendar"], read: true }));
  } catch (e) {
    if (!cancelled(e)) toast(errText(e));
  }
}

// The system's import (moving from an old phone): no questions, a notification says how it went.
async function systemImport(id: string): Promise<void> {
  try {
    const it = await call("file.open", { id, read: true });
    const r = await readICS(it);
    cal.addZones(r.zones);
    const m = cal.merge(r.events, id);
    if (!persist()) return;
    notify({ id: "import:" + id, title: tr("Calendar"), body: tr("Imported %d events from “%s”", m.added, it.name) });
  } catch (e) {
    console.warn(`import ${id}: ${errText(e)}`);
  }
}

function systemUndo(id: string): void {
  if (id.startsWith("account:")) sync.signedOut(id.slice("account:".length));
  const n = cal.unmerge(id);
  if (n) persist();
  console.info(`undo of import ${id}: ${n} events taken out`);
}

async function exportEvents(list: CalEvent[], name: string): Promise<void> {
  if (!list.length) {
    toast(tr("No events to export"));
    return;
  }
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
  const file = name.replace(/[\\/:*?"<>|\n]/g, " ").trim().slice(0, 50) + ".ics";
  try {
    await call("file.save", { name: file, data: btoa(encodeUTF8(serialiseICS(list, stamp, cal.zones))) });
  } catch (e) {
    if (!cancelled(e)) toast(errText(e));
  }
}

// Reminders: the next ones the system keeps (60 of its 64 for an app).
let syncing: Promise<void> | undefined;
let again = false;
// The reminders the system may keep: the last notify.list's and those wanted since.
const known = new Set<string>();
// Moves on with every change: a sync from before it stops, the one after runs.
let changes = 0;

function wanted(): Reminder[] {
  return reminders(cal, Date.now(), 35, 60, (o) => occText(o) + (o.ev.location ? " · " + o.ev.location : ""));
}

// The reminders a change lost (an occurrence deleted or moved, a reminder
// changed, one past the 60) are cancelled at once and not awaited: the
// Shell takes an app's requests in order, so they are gone before the
// page after the change shows; the sync waits for notify.list first,
// maybe behind one under way.
function takeBack(): void {
  changes++;
  const want = new Set(wanted().map((w) => w.id));
  for (const id of known) {
    if (want.has(id)) continue;
    known.delete(id);
    call("notify.cancel", { id }).catch((e: unknown) => console.warn(`reminder ${id}: ${errText(e)}`));
  }
}

function syncReminders(): void {
  if (syncing) {
    again = true;
    return;
  }
  syncing = doSync().finally(() => {
    syncing = undefined;
    if (again) {
      again = false;
      syncReminders();
    }
  });
}

async function doSync(): Promise<void> {
  const gen = changes;
  const want = wanted();
  for (const w of want) known.add(w.id);
  let have: ScheduledNotif[];
  try {
    have = await call("notify.list");
  } catch (e) {
    console.warn(`reminders: ${errText(e)}`);
    return;
  }
  if (gen !== changes) return;
  known.clear();
  for (const x of [...have, ...want]) known.add(x.id);
  const ids = new Set(want.map((w) => w.id));
  for (const h of have) {
    if (gen !== changes) return;
    if (ids.has(h.id)) continue;
    known.delete(h.id);
    await call("notify.cancel", { id: h.id }).catch(() => undefined);
  }
  for (const w of want) {
    if (gen !== changes) return;
    const title = w.title || tr("(No title)");
    const h = have.find((x) => x.id === w.id);
    if (h && h.at === w.at && (h.zone ?? "") === w.zone && h.title === title && (h.body ?? "") === w.body && h.arg === w.arg) continue;
    await call("notify.schedule", { id: w.id, at: w.at, zone: w.zone, title, body: w.body, arg: w.arg }).catch((e) => console.warn(`reminder ${w.id}: ${errText(e)}`));
  }
}

async function init(): Promise<void> {
  try {
    loc = await call("locale.get");
  } catch (e) {
    console.warn(`locale: ${errText(e)}`);
  }
  setPhoneZone(loc.zone);
  setLocale(loc);
  await readHolidays();
  await readSettings();
  await readDates();
  await loadZones(cal.events.map((e) => e.zone));
  refresh();
  syncReminders();
  syncNow();
  call("job.schedule", { id: "reminders", every_min: 720 }).catch((e) => console.warn(`job: ${errText(e)}`));
}

// The region's holiday calendar: the system fetches it once a day, so it
// is read again whenever the app comes back.
async function readHolidays(): Promise<void> {
  try {
    const h = await call("holidays.get");
    holidays.off = new Set(h.days_off);
    holidays.work = new Set(h.workdays);
    holidays.source = h.source ?? "";
  } catch (e) {
    console.warn(`holidays: ${errText(e)}`);
  }
}

async function readSettings(): Promise<void> {
  try {
    const s = await call("app.settings");
    lunarSetting = String(s.lunar ?? "With the language");
    birthdaySetting = String(s.birthdays ?? "Show");
  } catch (e) {
    console.warn(`settings: ${errText(e)}`);
  }
}

async function open(link: string): Promise<void> {
  await ready;
  const [kind, rest = ""] = [link.slice(0, link.indexOf(":") + 1) || link, link.slice(link.indexOf(":") + 1)];
  switch (kind) {
    case "calendar.import:":
      return systemImport(rest);
    case "calendar.undo:":
      return systemUndo(rest);
    case "calendar.sync:":
      return sync.pull().catch((e: unknown) => console.warn(`calendar sync: ${errText(e)}`));
    case "share:": {
      if (!rest.startsWith("file:")) return;
      try {
        await showImport(await call("file.open", { id: rest.slice(5), read: true }));
      } catch (e) {
        toast(errText(e));
      }
      return;
    }
    case "event:": {
      const i = rest.indexOf(":");
      const id = i < 0 ? rest : rest.slice(0, i);
      if (cal.get(id)) nav.push(EventPage, { id, key: i < 0 ? "" : rest.slice(i + 1) });
      return;
    }
    case "day:":
      if (parseDate(rest) !== undefined) nav.push(DayPage, { d: rest });
      return;
  }
  switch (link) {
    case "new":
      return newEvent(view.value.sel);
    case "search":
      nav.push(Search, undefined);
      return;
    case "agenda":
      nav.push(Agenda, undefined);
      return;
    case "today":
      select(today());
  }
}

// No event tells an app of midnight: past it, the month's today, the
// agenda and the days' "Tomorrow" are drawn again. The timer's clock stops
// while the phone sleeps, so the screen coming back on looks too.
let shownDay = today();
let midnight = 0;
function newDay(): void {
  const now = Date.now();
  if (today(now) !== shownDay) {
    shownDay = today(now);
    refresh();
  }
  clearTimeout(midnight);
  const d = new Date(now);
  midnight = setTimeout(newDay, new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime() - now);
}

app({
  root: Month,
  onStart: () => {
    ready = init();
    newDay();
  },
  onOpen: (link) => void open(link),
  onForeground: () => {
    void readHolidays()
      .then(readDates)
      .then(refresh);
    syncNow();
  },
  onEvent: (name, value) => {
    switch (name) {
      case "time.zone":
        loc = { ...loc, zone: value };
        setPhoneZone(value);
        refresh();
        syncReminders();
        newDay();
        break;
      case "time.clock":
        refresh();
        newDay();
        break;
      case "focus":
        if (value === "gained") newDay();
        break;
      case "job":
      case "notify.fired":
        syncReminders();
        break;
      case "settings":
        void readSettings()
          .then(readDates)
          .then(refresh);
        break;
    }
  },
});
