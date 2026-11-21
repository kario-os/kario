// The calendar's sync with the account (Settings › Accounts › Calendar):
// the system holds the account's password and talks CalDAV with the
// server; the app hands it its changes (calendar.push) and takes the
// server's (calendar.pull), each an event with its changed occurrences
// (one UID) as iCalendar. The system wakes the app with calendar.sync:
// when it has changes for it, or wants all its events (a first sync); the
// app also asks when it starts and comes to the foreground. What the app
// takes is not a change on the phone and does not go back. An event the
// app has no room for (2000 events, 900 KB) waits in the system until a
// pull finds room.
import type { CalChange, CalPull, CalPullArgs, CalPushArgs, CalPushRes } from "kario";
import { serialiseICS } from "./ics";
import type { CalEvent } from "./model";
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
}

// A calendar.push holds about this much iCalendar.
const PUSH_BYTES = 256 << 10;

export class Sync {
  /** The account the calendar syncs with: "" none, undefined not known yet. */
  account: string | undefined;
  /** The last pull stopped for want of room. */
  full = false;
  private busy: Promise<void> | undefined;
  private again = false;

  constructor(
    private readonly cal: Calendar,
    private readonly host: SyncHost,
  ) {}

  /** An event with its changed occurrences as iCalendar. */
  object(uid: string): string {
    const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
    return serialiseICS(this.cal.group(uid), stamp, this.cal.zones, { me: this.host.me() });
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
    let taken: number[] = [];
    let full = false;
    const bytes = { n: this.cal.bytes() };
    for (let guard = 0; guard < 1000; guard++) {
      const r = await this.host.pull({ taken, full });
      this.account = r.account;
      if (!r.account) return;
      if (r.want_all) await this.pushAll();
      this.full = full;
      if (full || !r.items?.length) return;
      taken = [];
      let changed = false;
      for (const it of r.items) {
        if (it.gone) {
          changed = this.cal.dropUID(it.uid) || changed;
        } else {
          const got = await this.host.read(it.ics ?? "");
          this.cal.addZones(got.zones);
          if (!this.cal.replaceUID(it.uid, got.events, "account:" + r.account, bytes)) {
            full = true;
            break;
          }
          changed = true;
          if (it.copy) this.host.copied(got.events[0]?.title ?? "");
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

  /** Hands the system all the events (a first sync asks for them). */
  async pushAll(): Promise<void> {
    await this.send(this.cal.uids().map((uid) => ({ uid, ics: this.object(uid), all: true })), true);
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
