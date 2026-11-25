import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { StatusBadge } from './status-badge';

describe('StatusBadge', () => {
  it.each([['healthy', 'lucideCircleCheck'], ['delayed', 'lucideClock'], ['quiet', 'lucideBellOff'], ['failing', 'lucideCircleX']])(
    'renders a distinct %s feed icon and label', (state, directive) => {
      const fixture = TestBed.createComponent(StatusBadge);
      fixture.componentRef.setInput('icon', state);
      fixture.componentRef.setInput('label', state);
      fixture.detectChanges();
      expect(fixture.nativeElement.querySelector('svg[' + directive + '][aria-hidden=true]')).toBeTruthy();
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
