import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { beforeEach, describe, expect, it } from 'vitest';
import { Workflow } from './workflow';

describe('Workflow panel', () => {
  const url = '/api/v1/workspaces/demo/alerts/1';
  const fieldMove = { step: 'reached_site', label: 'Reached site', location_required: true, terminal: false, can_override: false };
  const close = { step: 'closed', label: 'Closed', location_required: false, terminal: true, can_override: true };
  beforeEach(() => { TestBed.configureTestingModule({providers: [provideHttpClient(), provideHttpClientTesting()]}); });
  function render(moves: typeof fieldMove[]) {
    const fixture = TestBed.createComponent(Workflow);
    fixture.componentRef.setInput('url', url);
    fixture.componentRef.setInput('stepTime', '2026-01-01T12:00:00Z');
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(url + '/history').flush({moves, entries: [
      { label: 'New', at: '2026-01-01T10:00:00Z', actor: null, latitude: 16, longitude: 86 },
      { label: 'First report', at: '2026-01-01T12:00:00Z', actor: 'Demo Guard', latitude: null, reportPending: true }
    ]});
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement, http };
  }
  it('shows the ordered timeline with the actor, time, location and pending reports', () => {
    const {el} = render([]);
    const entries = Array.from(el.querySelectorAll('li'));
    expect(entries[0].textContent).toContain('New');
    expect(entries[0].textContent).toContain('16, 86');
    expect(entries[1].textContent).toContain('Demo Guard');
    expect(entries[1].textContent).toContain('Report pending');
    expect(entries[1].querySelector('time')?.dateTime).toBe('2026-01-01T12:00:00Z');
    expect(el.querySelector('button')).toBeNull();
  });
  it('only offers the returned moves and hides the field location exception', () => {
    const {el, fixture} = render([fieldMove]);
    expect(Array.from(el.querySelectorAll('button'), b => b.textContent.trim())).toEqual(['Reached site']);
    el.querySelector('button')!.click();
    fixture.detectChanges();
    expect(el.querySelector('[role=checkbox]')).toBeNull();
    expect(el.textContent).toContain('share your location');
  });
  it('lets an officer give a reason and requires confirmation to close', () => {
    const {el, fixture, http} = render([{...fieldMove, can_override:true}, close]);
    el.querySelector('button')!.click();
    fixture.detectChanges();
    expect(el.querySelector('[role=checkbox]')).toBeTruthy();
    Array.from(el.querySelectorAll('button')).find(b => b.textContent.trim() === 'Cancel')!.click();
    fixture.detectChanges();
    expect(el.textContent).not.toContain('Confirm Closed');
    Array.from(el.querySelectorAll('button')).find(b => b.textContent.includes('Close alert'))!.click();
    fixture.detectChanges();
    document.querySelector<HTMLButtonElement>('[role=menuitem]')!.click();
    fixture.detectChanges();
    expect(el.querySelector('button[type=submit]')?.textContent).toContain('Confirm Closed');
    http.expectNone(url+'/steps');
  });
});
