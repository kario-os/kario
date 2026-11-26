// Dates, times, spans, reminders and rules as the user reads them, in the
// order and on the clock the system uses (locale.get).
import { tr } from "kario";
import type { DateOrder, LocaleInfo } from "kario";
import { type Day, fmtTime, weekday, ymd } from "./dates";
import { monthName, WEEKDAYS, weekdayName } from "./names";
import type { Rule } from "./recur";
import type { Occ } from "./store";

let order: DateOrder = "ymd";
let clock24 = true;

export function setLocale(l: Pick<LocaleInfo, "date_order" | "clock_24h">): void {
  order = l.date_order;
  clock24 = l.clock_24h;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** A date in digits, the region's order: 2026-11-17, 17/11/2026, 11/17/2026. */
export function dateDigits(d: Day): string {
  const [y, m, dd] = ymd(d);
  if (order === "dmy") return `${pad(dd)}/${pad(m)}/${y}`;
  if (order === "mdy") return `${pad(m)}/${pad(dd)}/${y}`;
  return `${y}-${pad(m)}-${pad(dd)}`;
}

/** "Tuesday, November 17" (the year when it is not this one). */
export function dateLong(d: Day, thisYear?: number): string {
  const [y, m, dd] = ymd(d);
  const layout = y === thisYear ? tr("{weekday}, {month} {day}") : tr("{weekday}, {month} {day}, {year}");
  return layout.replace("{weekday}", weekdayName(d)).replace("{month}", monthName(m)).replace("{m}", String(m)).replace("{day}", String(dd)).replace("{year}", String(y));
}

/** "November 2026". */
export function monthTitle(y: number, m: number): string {
  return tr("{month} {year}").replace("{month}", monthName(m)).replace("{m}", String(m)).replace("{year}", String(y));
}

/** "Nov 17": a short date for a span. */
export function dateShort(d: Day): string {
  const [, m, dd] = ymd(d);
  return tr("{month} {day}").replace("{month}", monthName(m)).replace("{m}", String(m)).replace("{day}", String(dd));
}

/** A time of day on the user's clock. */
export function timeText(min: number): string {
  if (clock24) return fmtTime(min);
  const h = Math.floor(min / 60);
  const h12 = h % 12 === 0 ? 12 : h % 12;
  return h < 12 ? tr("%d:%02d AM", h12, min % 60) : tr("%d:%02d PM", h12, min % 60);
}

/** When an occurrence is, on its first day d0 (where a list shows it): "09:00–10:00", "All day", spans over days. */
export function occText(o: Occ): string {
  const last = o.ev.allDay ? o.end.day - 1 : o.end.day - (o.end.min === 0 && o.end.day > o.start.day ? 1 : 0);
  if (o.ev.allDay) return last > o.start.day ? tr("All day, %s – %s", dateShort(o.start.day), dateShort(last)) : tr("All day");
  if (o.end.day === o.start.day) {
    return o.end.min === o.start.min ? timeText(o.start.min) : `${timeText(o.start.min)}–${timeText(o.end.min)}`;
  }
  return `${dateShort(o.start.day)} ${timeText(o.start.min)} – ${dateShort(o.end.day)} ${timeText(o.end.min)}`;
}

/** A reminder: minutes before the start (an all-day event's: before its midnight). */
export function alarmText(min: number, allDay: boolean): string {
  if (allDay) {
    const days = Math.ceil(min / 1440);
    const at = timeText(((days * 1440 - min) % 1440 + 1440) % 1440);
    if (days <= 0) return tr("On the day at %s", at);
    return days === 1 ? tr("The day before at %s", at) : tr("%d days before at %s", days, at);
  }
  if (min === 0) return tr("At the start");
  if (min < 0) return tr("%d minutes after the start", -min);
  if (min % 1440 === 0) return min === 1440 ? tr("1 day before") : tr("%d days before", min / 1440);
  if (min % 60 === 0) return min === 60 ? tr("1 hour before") : tr("%d hours before", min / 60);
  return tr("%d minutes before", min);
}

/** A rule as a sentence: "Every week on Tuesday, 10 times". */
export function ruleText(r: Rule | undefined, startDay: Day, until?: Day, startMin = 0): string {
  if (!r) return tr("Does not repeat");
  const [, , d] = ymd(startDay);
  let s: string;
  const n = r.interval;
  switch (r.freq) {
    case "MINUTELY":
      s = n === 1 ? tr("Every minute") : tr("Every %d minutes", n);
      break;
    case "HOURLY":
      s = n === 1 ? tr("Every hour") : tr("Every %d hours", n);
      break;
    case "DAILY":
      s = n === 1 ? tr("Every day") : tr("Every %d days", n);
      break;
    case "WEEKLY": {
      const days = (r.byDay?.length ? r.byDay.map((b) => b.wd) : [weekday(startDay)]).map((w) => tr(WEEKDAYS[w])).join(tr(", "));
      s = n === 1 ? tr("Every week on %s", days) : tr("Every %d weeks on %s", n, days);
      break;
    }
    case "MONTHLY":
      s = r.byDay?.length || r.byMonthDay?.length ? tr("Every month") : n === 1 ? tr("Every month on day %d", d) : tr("Every %d months on day %d", n, d);
      break;
    case "YEARLY":
      if (r.byDay || r.byMonthDay || r.byMonth || r.byWeekNo || r.byYearDay) s = n === 1 ? tr("Every year") : tr("Every %d years", n);
      else s = n === 1 ? tr("Every year on %s", dateShort(startDay)) : tr("Every %d years on %s", n, dateShort(startDay));
      break;
  }
  if (r.byWeekNo) s = tr("%s, in week %s", s, r.byWeekNo.join(tr(", ")));
  if (r.byYearDay) s = tr("%s, on day %s of the year", s, r.byYearDay.join(tr(", ")));
  const fine = r.freq === "HOURLY" || r.freq === "MINUTELY";
  if ((r.freq === "YEARLY" || fine) && r.byDay && r.byDay.every((b) => b.n === 0)) s = tr("%s, on %s", s, r.byDay.map((b) => tr(WEEKDAYS[b.wd])).join(tr(", ")));
  if (fine && r.byMonthDay) s = tr("%s, on day %s of the month", s, r.byMonthDay.join(tr(", ")));
  if (fine) {
    if (r.byHour) s = tr("%s, in the hours %s", s, r.byHour.map((h) => timeText(h * 60)).join(tr(", ")));
    if (r.byMinute && r.freq === "HOURLY") s = tr("%s, at minutes %s", s, r.byMinute.join(tr(", ")));
  } else if (r.byHour || r.byMinute) {
    const at: number[] = [];
    for (const h of r.byHour ?? [Math.floor(startMin / 60)]) for (const m of r.byMinute ?? [startMin % 60]) at.push(h * 60 + m);
    const shown = [...new Set(at)].sort((a, b) => a - b);
    s = tr("%s, at %s", s, shown.slice(0, 12).map(timeText).join(tr(", ")) + (shown.length > 12 ? "…" : ""));
  }
  if (r.count) s = tr("%s, %d times", s, r.count);
  else if (until !== undefined) s = tr("%s, until %s", s, dateDigits(until));
  return s;
}
