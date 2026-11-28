// The calendar's sync with the account (Settings › Accounts › Calendar):
// the system holds the account's password and talks CalDAV with the
// server; the app hands it its changes (calendar.push) and takes the
// server's (calendar.pull), each an event with its changed occurrences
// (one UID) as iCalendar. The system wakes the app with calendar.sync:
// when it has changes for it, or wants all its events (a first sync); the
// app also asks when it starts and comes to the foreground. What the app
// takes is not a change on the phone and does not go back. An event the
// app has no room for (2000 events, 900 KB) waits in the system until a
// pull finds room. Where the account's server schedules (RFC 6638: the
// pull says so, with the user's addresses there), it sends the invitations
// of what the app hands it; an attendee's answer or a new invitation that
// comes in is pointed out, not what the first sync brings (first). The first hand-over of all the events marks the
// ones the user organizes as scheduled by the phone: their invitations
// went as files already and are not sent again. The sync ends at once
// when the user signs out (calendar.undo:account:) or the system says
// there is no account: nothing is the server's to send from then on, and
// a pull under way keeps nothing it brought.
import type { CalChange, CalPull, CalPullArgs, CalPushArgs, CalPushRes } from "kario";
import { sameAddress, serialiseICS } from "./ics";
import type { Attendee, CalEvent } from "./model";
import type { Calendar } from "./store";
import type { VZone } from "./vtz";

export interface SyncHost {
  pull(args: CalPullArgs): Promise<CalPull>;
  push(args: CalPushArgs): Promise<CalPushRes>;
  /** The events of an iCalendar text, the zones they are on loaded. */
  read(text: string): Promise<{ events: CalEvent[]; zones: Record<string, VZone> }>;
  /** Saves the calendar; false when that failed. */
  persist(): boolean;
  /** The phone's edit of an event the server changed too, kept as a copy. */
  copied(title: string): void;
  /** The user's own addresses (the events they organize: serialiseICS). */
  me(): string[];
  /** Attendees of an event the user organizes answered (who, with the answer). */
  answered(uid: string, title: string, who: Attendee[]): void;
  /** Someone invited the user through the account's server. */
  invited(uid: string, title: string, from: string): void;
  /** Whether the account's server schedules, or the user's addresses there, changed (the pages say who sends the invitations). */
  changed(): void;
}

// A calendar.push holds about this much iCalendar.
const PUSH_BYTES = 256 << 10;

export class Sync {
  /** The account the calendar syncs with: "" none, undefined not known yet. */
  account: string | undefined;
  /** The last pull stopped for want of room. */
  full = false;
  /** The account's server schedules (RFC 6638); the user's addresses there. */
  schedule = false;
  addresses: string[] = [];
  private busy: Promise<void> | undefined;
  private again = false;
  // Moves on when the sync ends: a pull from before keeps nothing.
  private gen = 0;

  constructor(
    private readonly cal: Calendar,
    private readonly host: SyncHost,
  ) {}

  /** An event with its changed occurrences as iCalendar (byPhone: its invitation marked as the phone's). */
  object(uid: string, byPhone = false): string {
    const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
    return serialiseICS(this.cal.group(uid), stamp, this.cal.zones, { me: this.host.me(), schedule: this.schedule, byPhone });
  }

  // Points out what came in: answers to an invitation the user organizes,
  // an invitation to the user from the account's server.
  private notice(before: CalEvent[], after: CalEvent[], first: boolean): void {
    const me = this.host.me();
    const e = after.find((x) => !x.recurrenceId) ?? after[0];
    if (first || !e?.organizer) return;
    if (me.some((m) => sameAddress(m, e.organizer!.addr))) {
      const was = before.find((x) => x.recurrenceId === e.recurrenceId);
      if (!was) return;
      const who = (e.attendees ?? []).filter((a) => {
        const old = was.attendees?.find((x) => sameAddress(x.addr, a.addr));
        return old && old.partstat !== a.partstat && a.partstat !== "NEEDS-ACTION" && !me.some((m) => sameAddress(m, a.addr));
      });
      if (who.length) this.host.answered(e.uid, e.title, who);
    } else if (this.schedule && !before.length && e.status !== "CANCELLED") {
      const mine = e.attendees?.find((a) => me.some((m) => sameAddress(m, a.addr)));
      if (mine?.partstat === "NEEDS-ACTION") this.host.invited(e.uid, e.title, e.organizer.name || e.organizer.addr.replace(/^mailto:/i, ""));
    }
  }

  /** The user signed out of the account (calendar.undo:account:): its server sends nothing of the calendar's any more. */
  signedOut(account: string): void {
    if (!this.account || this.account === account) this.end();
  }

  private end(): void {
    this.gen++;
    this.set("", false, []);
  }

  private set(account: string, schedule: boolean, addresses: string[]): void {
    const shown = (s: boolean, a: string | undefined, list: string[]) => (s && a ? [a, ...list].join("\n") : "");
    const was = shown(this.schedule, this.account, this.addresses);
    this.account = account;
    this.schedule = schedule;
    this.addresses = addresses;
    if (shown(schedule, account, addresses) !== was) this.host.changed();
  }

  /** Takes the server's changes (one pull at a time; one asked for meanwhile runs after). */
  pull(): Promise<void> {
    if (this.busy) {
      this.again = true;
      return this.busy;
    }
    this.busy = this.doPull().finally(() => {
      this.busy = undefined;
      if (this.again) {
        this.again = false;
        void this.pull();
      }
    });
    return this.busy;
  }

  private async doPull(): Promise<void> {
    const gen = this.gen;
    let taken: number[] = [];
    let full = false;
    const bytes = { n: this.cal.bytes() };
    for (let guard = 0; guard < 1000; guard++) {
      const r = await this.host.pull({ taken, full });
      if (gen !== this.gen) return;
      this.set(r.account, !!r.schedule, r.addresses ?? []);
      if (!r.account) return;
      if (r.want_all) await this.pushAll();
      if (gen !== this.gen) return;
      this.full = full;
      if (full || !r.items?.length) return;
      taken = [];
      let changed = false;
      for (const it of r.items) {
        if (it.gone) {
          changed = this.cal.dropUID(it.uid) || changed;
        } else {
          const got = await this.host.read(it.ics ?? "");
          if (gen !== this.gen) return;
          this.cal.addZones(got.zones);
          const before = this.cal.group(it.uid);
          if (!this.cal.replaceUID(it.uid, got.events, "account:" + r.account, bytes)) {
            full = true;
            break;
          }
          changed = true;
          if (it.copy) this.host.copied(got.events[0]?.title ?? "");
          else this.notice(before, this.cal.group(it.uid), !!it.first);
        }
        taken.push(it.seq ?? 0);
      }
      if (changed && !this.host.persist()) return;
    }
  }

  /** Hands the system the events changed on the phone (none while no account syncs the calendar). */
  async pushTouched(): Promise<void> {
    const uids = this.cal.takeTouched();
    if (!uids.length || this.account === "") return;
    await this.send(uids.map((uid) => (this.cal.group(uid).length ? { uid, ics: this.object(uid) } : { uid, gone: true })), false);
  }

  /** Hands the system all the events (a first sync asks for them); the invitations the user sent before are the phone's. */
  async pushAll(): Promise<void> {
    await this.send(this.cal.uids().map((uid) => ({ uid, ics: this.object(uid, true), all: true })), true);
  }

  private async send(items: CalChange[], done: boolean): Promise<void> {
    let chunk: CalChange[] = [];
    let size = 0;
    for (let i = 0; i <= items.length; i++) {
      const it = items[i];
      if (it === undefined || (chunk.length && size + (it.ics?.length ?? 0) > PUSH_BYTES)) {
        const last = it === undefined;
        if (chunk.length || (last && done)) {
          const r = await this.host.push({ items: chunk, done: last && done });
          if (this.account && r.account !== this.account) this.end();
          this.account = r.account;
          if (!r.account) return;
        }
        chunk = [];
        size = 0;
      }
      if (it) {
        chunk.push(it);
        size += it.ics?.length ?? 0;
      }
    }
  }
}
