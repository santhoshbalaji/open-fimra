import { computed } from '@angular/core';
import { formatLocale } from './language';

export type WorkspaceLocale = string;

export const reportingTimeZone = 'Asia/Kolkata';

const elapsedUnits = [
  { unit: 'day', seconds: 86400 },
  { unit: 'hour', seconds: 3600 },
  { unit: 'minute', seconds: 60 },
  { unit: 'second', seconds: 1 }
] as const;

/** Format values using the reader's locale and the workspace's reporting time zone. */
export class WorkspaceFormat {
  readonly number: (value: number) => string;
  private readonly plurals: Intl.PluralRules;
  private readonly absolute: Intl.DateTimeFormat;
  private readonly calendar: Intl.DateTimeFormat;
  private readonly clock: Intl.DateTimeFormat;
  private readonly clockWithSeconds: Intl.DateTimeFormat;
  private readonly relative: Intl.RelativeTimeFormat;
  private readonly units: readonly Intl.NumberFormat[];
  private readonly unitList: Intl.ListFormat;

  constructor(locale: string = 'en-IN', timeZone = reportingTimeZone, numberOptions?: Intl.NumberFormatOptions) {
    this.number = new Intl.NumberFormat(locale, numberOptions).format;
    this.plurals = new Intl.PluralRules(locale);
    this.absolute = new Intl.DateTimeFormat(locale, {
      timeZone, day: 'numeric', month: 'short', year: 'numeric',
      hour: 'numeric', minute: '2-digit', timeZoneName: 'short'
    });
    this.calendar = new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeZone: 'UTC' });
    this.clock = new Intl.DateTimeFormat(locale, { hour: 'numeric', minute: '2-digit', timeZone: 'UTC' });
    this.clockWithSeconds = new Intl.DateTimeFormat(locale, { hour: 'numeric', minute: '2-digit', second: '2-digit', timeZone: 'UTC' });
    this.relative = new Intl.RelativeTimeFormat(locale, { numeric: 'always' });
    this.units = elapsedUnits.map(({ unit }) => new Intl.NumberFormat(locale, { style: 'unit', unit, unitDisplay: 'long' }));
    this.unitList = new Intl.ListFormat(locale, { type: 'unit' });
  }

  plural(value: number): Intl.LDMLPluralRule {
    return this.plurals.select(value);
  }

  dateTime(instant: Date): string {
    return this.absolute.format(instant);
  }

  /** Format a calendar day encoded as midnight UTC. */
  date(day: Date): string {
    return this.calendar.format(day);
  }

  /** Format a time of day without applying a time zone offset. */
  time(secondsSinceMidnight: number, showSeconds: boolean): string {
    return (showSeconds ? this.clockWithSeconds : this.clock).format(secondsSinceMidnight * 1000);
  }

  age(instant: Date, now: Date): string {
    const seconds = (instant.getTime() - now.getTime()) / 1000;
    const unit = elapsedUnits.find(unit => Math.abs(seconds) >= unit.seconds) ?? elapsedUnits[3];
    return this.relative.format(Math.trunc(seconds / unit.seconds) || 0, unit.unit);
  }

  /** Format elapsed seconds using at most the two largest nonzero units. */
  duration(seconds: number): string {
    let remaining = Math.max(0, Math.trunc(seconds));
    const parts: string[] = [];
    for (const [index, unit] of elapsedUnits.entries()) {
      const value = Math.floor(remaining / unit.seconds);
      remaining %= unit.seconds;
      if (value > 0) parts.push(this.units[index].format(value));
      if (parts.length === 2) break;
    }
    return parts.length ? this.unitList.format(parts) : this.units[3].format(0);
  }
}

export const defaultWorkspaceFormat = new WorkspaceFormat();

const activeWorkspaceFormat = computed(() => new WorkspaceFormat(formatLocale()));

export const workspaceFormat = {
  number: (value: number) => activeWorkspaceFormat().number(value),
  plural: (value: number) => activeWorkspaceFormat().plural(value),
  dateTime: (instant: Date) => activeWorkspaceFormat().dateTime(instant),
  date: (day: Date) => activeWorkspaceFormat().date(day),
  time: (seconds: number, showSeconds: boolean) => activeWorkspaceFormat().time(seconds, showSeconds),
  age: (instant: Date, now: Date) => activeWorkspaceFormat().age(instant, now),
  duration: (seconds: number) => activeWorkspaceFormat().duration(seconds)
};
