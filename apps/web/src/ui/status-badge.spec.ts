import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { StatusBadge } from './status-badge';

describe('StatusBadge', () => {
  it.each([['healthy', 'circle-check'], ['delayed', 'clock'], ['quiet', 'bell-off'], ['failing', 'circle-x']])(
    'renders a distinct %s feed icon and label', (state, icon) => {
      const fixture = TestBed.createComponent(StatusBadge);
      fixture.componentRef.setInput('icon', state);
      fixture.componentRef.setInput('label', state);
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('svg[data-icon="' + icon + '"][aria-hidden=true] path, svg[data-icon="' + icon + '"][aria-hidden=true] circle')).toBeTruthy();
      expect(fixture.nativeElement.textContent.trim()).toBe(state);
    });

  it.each([
    ['draft', 'Draft', 'file-pen-line'], ['published', 'Published', 'circle-check'],
    ['validation-success', 'Validation passed', 'check'], ['pending', 'Pending', 'clock'],
    ['failed', 'Failed', 'circle-x'], ['queued', 'Queued', 'list-ordered']
  ])('renders the %s icon and label', (state, label, icon) => {
    const fixture = TestBed.createComponent(StatusBadge);
    fixture.componentRef.setInput('icon', state);
    fixture.componentRef.setInput('label', label);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector(`svg[data-icon="${icon}"][aria-hidden=true]`)).toBeTruthy();
    expect(fixture.nativeElement.textContent.trim()).toBe(label);
  });

  it('requires a visible label', () => {
    const fixture = TestBed.createComponent(StatusBadge);
    expect(() => fixture.componentRef.setInput('label', '  ')).toThrow('visible label');
  });

  it.each([
    ['overdue', 'Overdue', 'triangle-alert', 'warning'],
    ['step', 'New', 'circle-alert', 'urgent'],
    ['step', 'Acknowledged', 'check', 'active'],
    ['step', 'Team sent', 'chevron-right', 'active'],
    ['step', 'Reached site', 'circle-check', 'active'],
    ['step', 'First report', 'file-pen-line', 'active'],
    ['step', 'Final report', 'list-ordered', 'success'],
    ['complete', 'Closed', 'lock', 'success'],
    ['complete', 'False alarm', 'bell-off', 'muted'],
    ['complete', 'Duplicate', 'layers', 'muted'],
    ['complete', 'Controlled burn', 'sun', 'success'],
    ['complete', 'Not reachable', 'circle-x', 'warning']
  ])(
    'shows the %s variant for %s with its own icon and a visible label', (icon, label, glyph, tone) => {
      const fixture = TestBed.createComponent(StatusBadge);
      fixture.componentRef.setInput('icon', icon);
      fixture.componentRef.setInput('label', label);
      fixture.detectChanges();
      expect(fixture.nativeElement.textContent.trim()).toBe(label);
      expect(fixture.nativeElement.querySelector(`[data-tone="${tone}"] svg[data-icon="${glyph}"][aria-hidden="true"]`)).toBeTruthy();
    });
});
