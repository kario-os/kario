// An event as the calendar keeps it. Times are wall times on the event's
// zone (Zone, an IANA name; "" floats on the phone's): "YYYY-MM-DD" for
// an all-day event, whose End is the day after its last, else
// "YYYY-MM-DDTHH:MM". An event that changes one occurrence of a repeating
// one has the repeating one's UID and RecurrenceID, the start it replaces.

export interface CalEvent {
  /** The calendar's own ID. */
  id: string;
  /** RFC 5545's UID, kept from an import. */
  uid: string;
  title: string;
  allDay: boolean;
  start: string;
  end: string;
  zone: string;
  location: string;
  notes: string;
  /** Reminders: minutes before the start (negative: after it; an all-day event's start is its midnight). */
  alarms: number[];
  /** RRULE's value, "" for none. */
  rrule: string;
  /** Starts taken out of the repetition, as start is written. */
  exdates: string[];
  /** More starts (RDATE), as start is written; absent in events kept before there were any. */
  rdates?: string[];
  recurrenceId: string;
  /** The file of Files it was imported from by the system (calendar.import:), "account:<id>" from the account's sync, "" otherwise. */
  src: string;
  /** An invitation's (iTIP, RFC 5546) organizer and attendees, and its SEQUENCE. */
  organizer?: Person;
  attendees?: Attendee[];
  seq?: number;
  /** "CANCELLED": the organizer cancelled it and the user kept it, marked. */
  status?: string;
}

/** A calendar user: a "mailto:" address (CAL-ADDRESS) and a name (CN). */
export interface Person {
  addr: string;
  name: string;
}

/** An attendee and the answer (PARTSTAT: NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED, DELEGATED). */
export interface Attendee extends Person {
  partstat: string;
  role?: string;
  rsvp?: boolean;
}

// The bounds of what the calendar keeps (the app's storage is 1 MB).
export const MAX_EVENTS = 2000;
export const MAX_TITLE = 200;
export const MAX_LOCATION = 200;
export const MAX_NOTES = 2000;
export const MAX_ALARMS = 4;
export const MAX_EXDATES = 200;
export const MAX_RDATES = 200;
export const MAX_ATTENDEES = 50;
export const MAX_NAME = 100;
export const MAX_ADDRESS = 200;

export function clip(s: string, n: number): string {
  const chars = [...s];
  return chars.length <= n ? s : chars.slice(0, n - 1).join("") + "…";
}
