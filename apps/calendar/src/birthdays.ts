// Birthdays and anniversaries from Contacts: the system gives the system's
// calendar app those dates alone (contacts.dates), and the calendar shows
// each as a yearly all-day event of its own making. They are not kept in
// the calendar's storage, not synced with the account, not exported, and
// have no reminder; Contacts is where they change. A date without its year
// starts in 1972, a leap year; 29 February falls on the last day of
// February in the other years.
import type { ContactDate } from "kario";
import { fmtDate, parseDate } from "./dates";
import type { CalEvent } from "./model";

/** The src of the events made of contacts' dates. */
export const CONTACTS_SRC = "contacts";

export type DateKind = "birthday" | "anniversary";

/** The events of the contacts' dates; title names one. */
export function dateEvents(list: ContactDate[], title: (name: string, kind: DateKind) => string): CalEvent[] {
  const out: CalEvent[] = [];
  for (const c of list) {
    for (const [kind, date] of [["birthday", c.birthday], ["anniversary", c.anniversary]] as [DateKind, string | undefined][]) {
      const m = /^(\d{4}|-)-(\d{2})-(\d{2})$/.exec(date ?? "");
      if (!m) continue;
      const start = `${m[1] === "-" ? "1972" : m[1]}-${m[2]}-${m[3]}`;
      const d = parseDate(start);
      if (d === undefined) continue;
      out.push({
        id: `${kind}:${c.id}`,
        uid: `${kind}:${c.id}`,
        title: title(c.name, kind),
        allDay: true,
        start,
        end: fmtDate(d + 1),
        zone: "",
        location: "",
        notes: "",
        alarms: [],
        rrule: m[2] === "02" && m[3] === "29" ? "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=-1" : "FREQ=YEARLY",
        exdates: [],
        recurrenceId: "",
        src: CONTACTS_SRC,
      });
    }
  }
  return out;
}

/** The years a contact's date is old in year y (undefined without its year). */
export function yearsIn(date: string | undefined, y: number): number | undefined {
  const m = /^(\d{4})-\d{2}-\d{2}$/.exec(date ?? "");
  return m ? y - Number(m[1]) : undefined;
}
