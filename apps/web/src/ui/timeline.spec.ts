import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { Timeline } from './timeline';

describe('Timeline', () => {
  it('starts with milestones and can show the full chronological activity', () => {
    const fixture = TestBed.createComponent(Timeline);
    const at = new Date('2026-01-01T20:00:00Z');
    fixture.componentRef.setInput('now', new Date('2026-01-01T22:00:00Z'));
    fixture.componentRef.setInput('events', [
      { id: 'opened', title: 'Alert opened', milestone: true, at },
      { id: 'note', title: 'Note added', at: new Date('2026-01-01T20:10:00Z') },
      { id: 'assigned', title: 'Team assigned', milestone: true, at: new Date('2026-01-01T20:30:00Z') }
    ]);
    fixture.detectChanges();
    const element: HTMLElement = fixture.nativeElement;
    const items = () => [...element.querySelectorAll('li')].map(item => item.querySelector('p')?.textContent);
    const button = element.querySelector('button')!;
    expect(items()).toEqual(['Alert opened', 'Team assigned']);
    expect(element.querySelector('time')?.dateTime).toBe(at.toISOString());
    expect(element.querySelector('time')?.textContent).toContain('IST');
    expect(element.querySelector('li')?.textContent).toContain('2 hours ago');
    expect(button.getAttribute('aria-expanded')).toBe('false');
    button.click();
    fixture.detectChanges();
    expect(items()).toEqual(['Alert opened', 'Note added', 'Team assigned']);
    expect(button.getAttribute('aria-expanded')).toBe('true');
    expect(element.querySelector('ol')?.id).toBe(button.getAttribute('aria-controls'));
    button.click();
    fixture.detectChanges();
    expect(items()).toEqual(['Alert opened', 'Team assigned']);
  });
});
