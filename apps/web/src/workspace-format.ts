export type WorkspaceLocale = 'en-IN' | 'hi-IN';

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
  private readonly relative: Intl.RelativeTimeFormat;
  private readonly units: readonly Intl.NumberFormat[];
  private readonly unitList: Intl.ListFormat;

  constructor(locale: WorkspaceLocale = 'en-IN', timeZone = reportingTimeZone) {
    this.number = new Intl.NumberFormat(locale).format;
    this.plurals = new Intl.PluralRules(locale);
    this.absolute = new Intl.DateTimeFormat(locale, {
      timeZone, day: 'numeric', month: 'short', year: 'numeric',
      hour: 'numeric', minute: '2-digit', timeZoneName: 'short'
    });
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
