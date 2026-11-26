import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import symbols from '../../../services/geo-worker/situation-report-symbols.json';

export type ReportClass = keyof typeof symbols.forestClasses;
export const reportClasses = Object.keys(symbols.forestClasses) as ReportClass[];

export function reportClass(value: string): ReportClass {
  return Object.hasOwn(symbols.forestClasses, value) ? value as ReportClass : 'outside';
}

export function reportAge(observed: string | undefined, end: string): number | null {
  if (!observed) return null;
  const hours = (Date.parse(end) - Date.parse(observed)) / 3_600_000;
  const index = symbols.ageHours.findIndex(limit => hours < limit);
  return index < 0 ? symbols.ageHours.length : index;
}

export function alertSymbol(cls: ReportClass | 'incident', age: number | null = null) {
  const symbol = cls === 'incident' ? symbols.incident : symbols.forestClasses[cls];
  const palette = symbol.palette === 'age' ? (age === null ? 'neutral-mark' : 'age-' + (age + 1))
    : symbol.palette === 'neutralMark' ? 'neutral-mark' : symbol.palette;
  return { ...symbol, colour: 'var(--map-' + palette + ')' };
}

@Component({
  selector: '[fimraReportSymbol]',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @let mark = symbol();
    @let r = mark.radius;
    @switch (mark.shape) {
      @case ('square') {
        <svg:rect [attr.x]="-r" [attr.y]="-r" [attr.width]="r * 2" [attr.height]="r * 2"
          [attr.fill]="mark.colour" stroke="var(--map-age-outline)" />
      }
      @case ('diamond') {
        <svg:path [attr.d]="'M0 ' + -r + 'L' + r + ' 0L0 ' + r + 'L' + -r + ' 0Z'"
          [attr.fill]="mark.colour" stroke="var(--map-age-outline)" />
      }
      @case ('ring') {
        <svg:circle [attr.r]="r" fill="none" stroke="var(--map-age-outline)" stroke-width="3.6" />
        <svg:circle [attr.r]="r" fill="none" [attr.stroke]="mark.colour" stroke-width="2" />
      }
      @default {
        <svg:circle [attr.r]="r" [attr.fill]="mark.colour" stroke="var(--map-age-outline)" />
      }
    }
    @if (closed()) {
      @let slash = closedStroke;
      <svg:line [attr.x1]="slash.start[0] * r" [attr.y1]="slash.start[1] * r"
        [attr.x2]="slash.end[0] * r" [attr.y2]="slash.end[1] * r"
        stroke="var(--map-land)" [attr.stroke-width]="slash.width + 1.6" />
      <svg:line [attr.x1]="slash.start[0] * r" [attr.y1]="slash.start[1] * r"
        [attr.x2]="slash.end[0] * r" [attr.y2]="slash.end[1] * r"
        stroke="var(--map-age-outline)" [attr.stroke-width]="slash.width" />
    }
  `
})
export class SituationReportSymbol {
  readonly symbol = input.required<ReturnType<typeof alertSymbol>>({ alias: 'fimraReportSymbol' });
  readonly closed = input(false);
  readonly closedStroke = symbols.closedStroke;
}
