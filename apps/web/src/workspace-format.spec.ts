import { describe, expect, it } from 'vitest';
import { WorkspaceFormat } from './workspace-format';

describe('WorkspaceFormat', () => {
  const english = new WorkspaceFormat();
  const hindi = new WorkspaceFormat('hi-IN');
  const instant = new Date('2026-01-01T20:00:00Z');
  const now = new Date('2026-01-01T22:00:00Z');

  it('groups numbers in lakhs and crores for both locales', () => {
    expect(english.number(12345678.5)).toBe('1,23,45,678.5');
    expect(hindi.number(12345678)).toBe('1,23,45,678');
  });

  it('uses each locale’s plural rules', () => {
    expect([0, 1, 2].map(value => english.plural(value))).toEqual(['other', 'one', 'other']);
    expect([0, 1, 2].map(value => hindi.plural(value))).toEqual(['one', 'one', 'other']);
  });

  it('shows the reporting date, time and zone, including across midnight', () => {
    for (const format of [english, hindi]) {
      expect(format.dateTime(instant)).toMatch(/^2 .+ 2026.*1:30.*IST$/u);
    }
    expect(hindi.dateTime(instant)).toContain('जन॰');
    expect(new WorkspaceFormat('en-IN', 'UTC').dateTime(instant)).toMatch(/^1 .+ 2026.*8:00.*UTC$/u);
  });

  it('formats durations and relative ages using localized units', () => {
    expect(english.duration(3661)).toBe('1 hour, 1 minute');
    expect(english.duration(0)).toBe('0 seconds');
    expect(english.age(instant, now)).toBe('2 hours ago');
    expect(english.age(now, instant)).toBe('in 2 hours');
    expect(hindi.age(instant, now)).toBe('2 घंटे पहले');
    expect(hindi.duration(3661)).toMatch(/1 घंटा.*1 मिनट/u);
  });
});
