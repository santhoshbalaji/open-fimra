import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { InteractivityChecker } from '@angular/cdk/a11y';
import { stubLayoutApis } from './overlay-testing';
import { Workflow } from './workflow';

describe('Workflow panel', () => {
  const url = '/api/v1/workspaces/demo/alerts/1';
  const fieldMove = { step: 'reached_site', label: 'Reached site', location_required: true, terminal: false, can_override: false };
  const close = { step: 'closed', label: 'Closed', location_required: false, terminal: true, can_override: true };
  beforeEach(() => {
    stubLayoutApis();
    TestBed.configureTestingModule({providers: [provideHttpClient(), provideHttpClientTesting()]});
    vi.spyOn(TestBed.inject(InteractivityChecker), 'isVisible').mockReturnValue(true);
  });
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });
  function render(moves: (typeof fieldMove & { missingReports?: string[] })[], subject = 'alert') {
    const fixture = TestBed.createComponent(Workflow);
    fixture.componentRef.setInput('url', url);
    fixture.componentRef.setInput('stepTime', '2026-01-01T12:00:00Z');
    fixture.componentRef.setInput('subject', subject);
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(url + '/history').flush({moves, entries: [
      { label: 'New', at: '2026-01-01T10:00:00Z', actor: null, latitude: 16, longitude: 86 },
      { label: 'First report', at: '2026-01-01T12:00:00Z', actor: 'Demo Guard', latitude: null, reportPending: true }
    ]});
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement, http };
  }
  it('shows the ordered timeline with the actor, time and pending reports without repeating coordinates', () => {
    const {el} = render([]);
    const entries = Array.from(el.querySelectorAll('fimra-timeline li'));
    expect(entries[1].textContent).toContain('New');
    expect(entries[1].textContent).not.toContain('16, 86');
    expect(entries[0].textContent).toContain('Demo Guard');
    expect(entries[0].textContent).toContain('Waiting for a report');
    expect(entries[0].querySelector('time')?.dateTime).toBe('2026-01-01T12:00:00.000Z');
    expect(el.querySelector('button')).toBeNull();
  });
  it('only offers the returned moves and hides the field location exception', () => {
    const {el, fixture} = render([fieldMove]);
    expect(Array.from(el.querySelectorAll('button'), b => b.textContent.trim())).toEqual(['Reached site']);
    el.querySelector('button')!.click();
    fixture.detectChanges();
    expect(el.querySelector('[role=checkbox]')).toBeNull();
    expect(el.textContent).toContain('share your location');
    expect(el.textContent).toContain('Location required');
    expect(document.activeElement).toBe(el.querySelector('form h3'));
  });
  it('returns focus to the close action and can dismiss a terminal confirmation without saving', async () => {
    const {el, fixture, http} = render([{...fieldMove, can_override:true}, close]);
    const field = el.querySelector('button')!;
    field.click();
    fixture.detectChanges();
    expect(el.querySelector('[role=checkbox]')).toBeTruthy();
    el.querySelector('form h3')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    fixture.detectChanges();
    expect(document.activeElement).toBe(field);
    const closeButton = Array.from(el.querySelectorAll('button')).find(b => b.textContent.includes('Close alert'))!;
    closeButton.click();
    fixture.detectChanges();
    document.querySelector<HTMLButtonElement>('[role=menuitem]')!.click();
    fixture.detectChanges();
    const review = el.querySelector<HTMLButtonElement>('button[type=submit]')!;
    review.focus();
    review.click();
    TestBed.tick();
    const modal = document.querySelector('[role=alertdialog]')!;
    expect(modal.textContent).toContain('Confirm Closed');
    modal.querySelector('button')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    await fixture.whenStable();
    expect(document.activeElement).toBe(review);
    expect(document.querySelector('[role=alertdialog]')).toBeNull();
    http.expectNone(url + '/steps');
    el.querySelector('form h3')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    fixture.detectChanges();
    expect(document.activeElement).toBe(closeButton);
  });
  it('announces saving and success and focuses the saved result across a history reload', async () => {
    const {el, fixture, http} = render([{...fieldMove, location_required:false}]);
    el.querySelector('button')!.click();
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')!.click();
    fixture.detectChanges();
    expect(el.textContent).toContain('Saving Reached site');
    http.expectOne(url + '/steps').flush({});
    await fixture.whenStable();
    fixture.detectChanges();
    http.expectOne(url + '/history').flush({ moves: [], entries: [] });
    fixture.detectChanges();
    expect(document.activeElement?.textContent).toContain('Saved: Reached site.');
    expect(el.querySelector('form')).toBeNull();
  });
  it('waits for an incident preview and displays the actual member outcomes after saving', async () => {
    const {el, fixture, http} = render([{...fieldMove, location_required: false}], 'incident');
    el.querySelector('button')!.click();
    fixture.detectChanges();
    const submit = el.querySelector<HTMLButtonElement>('button[type=submit]')!;
    expect(submit.disabled).toBe(true);
    const request = url + '/history?preview=reached_site';
    http.expectOne(request).flush({}, { status: 500, statusText: 'Server error' });
    fixture.detectChanges();
    expect(submit.disabled).toBe(true);
    Array.from(el.querySelectorAll('button')).find(button => button.textContent.trim() === 'Retry preview')!.click();
    fixture.detectChanges();
    const alerts = [
      { alertId: '1', fromLabel: 'Team sent', reason: 'transition_allowed' },
      { alertId: '2', fromLabel: 'False alarm', reason: 'closed' }
    ];
    http.expectOne(request).flush({ alerts });
    fixture.detectChanges();
    expect(el.textContent).toContain('Will move from Team sent to Reached site.');
    expect(el.textContent).toContain('Will stay at False alarm. Already closed.');
    submit.click();
    http.expectOne(url + '/steps').flush({
      alertsMoved: 0,
      alerts: [{ alertId: '1', fromLabel: 'Reached site', reason: 'already_at_step' }, alerts[1]]
    });
    await fixture.whenStable();
    http.expectOne(url + '/history').flush({ moves: [], entries: [] });
    fixture.detectChanges();
    expect(document.activeElement?.textContent).toContain('0 alerts moved.');
    expect(document.activeElement?.textContent).toContain('Stayed at Reached site. Already at this step.');
    expect(document.activeElement?.textContent).toContain('Stayed at False alarm. Already closed.');
    expect(el.querySelector('form')).toBeNull();
    http.verify();
  });
  it('announces a browser location refusal and the server’s reason for a refused step', async () => {
    const mockNavigator = Object.create(navigator);
    Object.defineProperty(mockNavigator, 'geolocation', { value: {
      getCurrentPosition: (_success: PositionCallback, failure: PositionErrorCallback) => failure({ code: 1 } as GeolocationPositionError)
    } });
    vi.stubGlobal('navigator', mockNavigator);
    const {el, fixture, http} = render([{...fieldMove, can_override: true}]);
    el.querySelector('button')!.click();
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')!.click();
    await fixture.whenStable();
    expect(el.textContent).toContain('Location permission denied');
    http.expectNone(url + '/steps');

    el.querySelector<HTMLButtonElement>('[role=checkbox]')!.click();
    fixture.detectChanges();
    el.querySelector<HTMLTextAreaElement>('#location-reason')!.value = 'Confirmed by radio';
    el.querySelector<HTMLButtonElement>('button[type=submit]')!.click();
    http.expectOne(url + '/steps').flush({ error: 'Your role cannot record this step.' }, { status: 403, statusText: 'Forbidden' });
    await fixture.whenStable();
    expect(el.textContent).toContain('Permission denied. Your role cannot record this step.');
    expect(el.querySelector('form')).not.toBeNull();
  });
  it('hands a report gate to the field report form without posting a step', () => {
    const {el, fixture, http} = render([{...fieldMove, missingReports: ['first-report']}]);
    const open = vi.fn();
    fixture.componentInstance.reportNeeded.subscribe(open);
    el.querySelector('button')!.click();
    fixture.detectChanges();
    Array.from(el.querySelectorAll('button')).find(button => button.textContent.trim() === 'Open required report')!.click();
    expect(open).toHaveBeenCalledWith('first-report');
    http.expectNone(url + '/steps');
  });
});
