import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AlertRow } from './alert-queue';
import { AuthService } from './auth';
import { IncidentFocus, IncidentRow, Incidents } from './incidents';
import { WorkspaceService } from './workspace';

describe('Incidents', () => {
  const base = 'https://api.test/api/v1/workspaces/demo';
  const now = Date.parse('2026-01-01T12:00:00Z');
  const incident: IncidentRow = {
    id: '7', status: 'new', step_label: 'New', step_time: '2026-01-01T10:00:00Z', overdue: true, terminal: false,
    merged_into: null, alert_count: 2, first_observed_at: now / 1000 - 7200, last_observed_at: now / 1000 - 3600,
    beat_names: ['Beat 05'], division_names: ['Hill Division'], bounds: [86.29, 16.42, 86.3, 16.43]
  };
  const member = (id: string, longitude: number): AlertRow => ({
    id, detection_id: '1' + id, queue: 'unit', status: 'new', step_label: 'New', step_time: '', overdue: false,
    forest_class: 'inside', observed_at: now / 1000 - 3600, longitude, latitude: 16.42, beat_name: 'Beat 05'
  });
  const deny = vi.fn();
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }), deny } },
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } }
    ] });
  });
  function render() {
    const fixture = TestBed.createComponent(Incidents);
    fixture.componentRef.setInput('endpoint', base);
    fixture.componentRef.setInput('now', now);
    const focus: (IncidentFocus | null)[] = [];
    const opened: AlertRow[] = [];
    fixture.componentInstance.showOnMap.subscribe(value => focus.push(value));
    fixture.componentInstance.openAlert.subscribe(row => opened.push(row));
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    return { fixture, el: fixture.nativeElement as HTMLElement, http, focus, opened };
  }

  it('lists incidents with the shared filters and their visible places', () => {
    const { el, fixture, http } = render();
    const list = http.expectOne(req => req.url === base + '/incidents');
    expect(list.request.params.get('hours')).toBe('168');
    list.flush({ items: [incident], total: 1 });
    fixture.detectChanges();
    expect(el.textContent).toContain('1 incident in this time window');
    expect(el.textContent).toContain('2 alerts · Beat 05 · Hill Division');
    expect(el.textContent).toContain('Overdue');
    http.verify();
  });

  it('opens an incident, shows its members on the map and its own workflow', () => {
    const { el, fixture, http, focus, opened } = render();
    http.expectOne(req => req.url === base + '/incidents').flush({ items: [incident], total: 1 });
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('.row-open')!.click();
    fixture.detectChanges();
    http.expectOne(base + '/incidents/7').flush(incident);
    const members = http.expectOne(req => req.url === base + '/alerts');
    expect(members.request.params.get('incident')).toBe('7');
    members.flush({ items: [member('1', 86.29), member('2', 86.3)] });
    fixture.detectChanges();
    http.expectOne(base + '/incidents/7/history').flush({ moves: [], entries: [
      { label: 'New', step: 'new', at: '2026-01-01T10:00:00Z', actor: null, latitude: null, longitude: null },
      { label: 'Acknowledged', step: 'acknowledged', at: '2026-01-01T10:10:00Z', actor: 'Demo Officer', latitude: null, alertsMoved: 2 }
    ] });
    fixture.detectChanges();
    expect(el.querySelector('h3')?.textContent).toContain('Incident #7');
    expect(el.textContent).toContain('Moved 2 alerts');
    expect(focus.at(-1)).toEqual({ id: '7', bounds: incident.bounds, points: [[86.29, 16.42], [86.3, 16.42]] });
    Array.from(el.querySelectorAll<HTMLButtonElement>('.row-open'))[1].click();
    expect(opened.map(row => row.id)).toEqual(['2']);
    Array.from(el.querySelectorAll('button')).find(b => b.textContent.includes('All incidents'))!.click();
    fixture.detectChanges();
    expect(focus.at(-1)).toBeNull();
  });

  it('points a merged incident to the one that continues and offers no steps', () => {
    const { el, fixture, http } = render();
    const merged = { ...incident, id: '8', merged_into: '7' };
    http.expectOne(req => req.url === base + '/incidents').flush({ items: [merged], total: 1 });
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('.row-open')!.click();
    fixture.detectChanges();
    http.expectOne(base + '/incidents/8').flush(merged);
    http.expectOne(req => req.url === base + '/alerts').flush({ items: [member('1', 86.29)] });
    fixture.detectChanges();
    expect(el.textContent).toContain('This incident joined incident #7');
    http.expectNone(base + '/incidents/8/history');
  });
});
