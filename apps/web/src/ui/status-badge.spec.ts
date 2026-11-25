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

  it('requires a visible label', () => {
    const fixture = TestBed.createComponent(StatusBadge);
    expect(() => fixture.componentRef.setInput('label', '  ')).toThrow('visible label');
  });

  it.each([['overdue', 'Overdue'], ['step', 'Reached site'], ['complete', 'Closed']])(
    'shows the %s icon with a visible workflow label', (icon, label) => {
      const fixture = TestBed.createComponent(StatusBadge);
      fixture.componentRef.setInput('icon', icon);
      fixture.componentRef.setInput('label', label);
      fixture.detectChanges();
      expect(fixture.nativeElement.textContent.trim()).toBe(label);
      expect(fixture.nativeElement.querySelector('svg[aria-hidden="true"]')).toBeTruthy();
    });
});
