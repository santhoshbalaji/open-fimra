import { Location } from '@angular/common';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Params, Router, convertToParamMap, provideRouter } from '@angular/router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AlertRow } from './alert-queue';
import { AuthService } from './auth';
import { IncidentRow, Incidents } from './incidents';
import { ShellNavigation } from './shell-navigation';

describe('Incidents', () => {
  const base = 'https://api.test/api/v1/workspaces/demo';
  const now = Date.parse('2026-01-01T12:00:00Z');
  const incident: IncidentRow = {
    id: '7', status: 'new', step_label: 'New', step_time: '2026-01-01T10:00:00Z', overdue: true, terminal: false,
    activity_at: '2026-01-01T11:00:00Z', merged_into: null, alert_count: 2, first_observed_at: now / 1000 - 7200, last_observed_at: now / 1000 - 3600,
    beat_names: ['Beat 05'], division_names: ['Hill Division'], bounds: [86.29, 16.42, 86.3, 16.43]
  };
  const member: AlertRow = {
    id: '201', detection_id: '101', queue: 'unit', status: 'new', step_label: 'New', step_time: '', overdue: false,
    forest_class: 'inside', observed_at: now / 1000 - 3600, longitude: 86.29, latitude: 16.42, beat_name: 'Beat 05'
  };
  const entry = (label: string, step: string, at: string, extra: object = {}) =>
    ({ label, step, at: '2026-01-01T' + at + 'Z', actor: 'Demo Officer', latitude: null, ...extra });

  beforeEach(() => TestBed.configureTestingModule({ providers: [
    provideHttpClient(), provideHttpClientTesting(), provideRouter([]),
    { provide: AuthService, useValue: { sessionEnded: vi.fn() } }
  ] }));

  function render(initial: Params = {}) {
    const query = signal(convertToParamMap(initial));
    let params = initial;
    let state = {};
    TestBed.overrideProvider(Location, { useValue: { getState: () => state } });
    TestBed.overrideProvider(ShellNavigation, { useFactory: () => ({
      query,
      href: (path: string, queryParams: Params) => TestBed.inject(Router).createUrlTree([path], { queryParams }),
      update: (changes: Params, options: { state?: object } = {}) => {
        params = Object.fromEntries(Object.entries({ ...params, ...changes }).filter(([, value]) => value !== null));
        state = options.state ?? {};
        query.set(convertToParamMap(params));
      }
    }) });
    const fixture = TestBed.createComponent(Incidents);
    fixture.componentRef.setInput('endpoint', base);
    fixture.componentRef.setInput('now', now);
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    const http = TestBed.inject(HttpTestingController);
    const list = (rows = [incident], nextCursor?: string) => {
      const request = http.expectOne(req => req.url === base + '/incidents');
      request.flush({ items: rows, total: rows.length, nextCursor });
      fixture.detectChanges();
      return request.request.params;
    };
    const button = (label: string, root = el) => Array.from(root.querySelectorAll('button'))
      .find(b => b.textContent.includes(label))!;
    const row = () => el.querySelector<HTMLButtonElement>('[data-incident-id]')!;
    const click = (element: HTMLElement) => { element.click(); fixture.detectChanges(); };
    const detail = (item = incident, members = [member], entries = [entry('New', 'new', '10:00:00')]) => {
      http.expectOne(base + '/incidents/' + item.id).flush(item);
      fixture.detectChanges();
      const request = http.expectOne(req => req.url === base + '/alerts');
      expect(request.request.params.get('incident')).toBe(item.id);
      request.flush({ items: members });
      http.expectOne(base + '/incidents/' + item.id + '/history').flush({
        entries,
        moves: item.merged_into ? [] : [{ step: 'acknowledged', label: 'Acknowledge', terminal: false }]
      });
      fixture.detectChanges();
    };
    return { el, http, query, list, button, row, click, detail };
  }

  it('leads with place, status and activity and requests sorted pages with the URL filters', () => {
    const { el, list, button, click, query, http } = render({ hours: '72', class: 'inside', unitPath: 'ds.c1' });
    const params = list([incident], '2026-01-01T11:00:00Z,7');
    expect(params.get('hours')).toBe('72');
    expect(params.get('class')).toBe('inside');
    expect(params.get('unitPath')).toBe('ds.c1');
    expect(params.get('sort')).toBe('activity-desc');
    expect(el.querySelector('th[scope=row]')?.textContent).toContain('Beat 05 · Hill Division');
    expect(el.querySelector('th[aria-sort]')?.textContent).toContain('Latest activity');
    expect(el.textContent).toContain('IST');
    expect(el.textContent).toContain('Overdue');

    click(button('Next'));
    expect(query().get('cursor')).toContain(',7');
    list([{ ...incident, id: '8' }]);
    expect(el.textContent).toContain('Page 2');
    click(button('Previous'));
    expect(query().get('cursor')).toBeNull();
    list();
    click(button('Latest activity', el.querySelector('thead')!));
    expect(query().get('sort')).toBe('activity-asc');
    expect(list().get('sort')).toBe('activity-asc');
    http.verify();
  });

  it('focuses detail before data loads and returns focus to the originating row on close', () => {
    const { el, list, row, button, click, detail, http, query } = render();
    list();
    const trigger = row();
    trigger.focus();
    click(trigger);
    expect(document.activeElement).toBe(el.querySelector('h2'));
    expect(document.activeElement?.textContent).toBe('Incident #7');
    detail();
    click(button('Close'));
    expect(query().has('incident')).toBe(false);
    expect(document.activeElement).toBe(trigger);
    http.verify();
  });

  it('shows milestones first and exposes the full history on demand', () => {
    const { el, list, row, detail, click, button, http } = render();
    list();
    click(row());
    detail(incident, [member], [
      entry('New', 'new', '09:40:00'),
      entry('Team sent', 'team_sent', '09:50:00', { note: 'Demo team notified' }),
      entry('Reached site', 'reached_site', '10:00:00', { alertsMoved: 1 })
    ]);
    const timeline = el.querySelector('fimra-timeline')! as HTMLElement;
    expect(timeline.textContent).toContain('Reached site');
    expect(timeline.textContent).toContain('Moved 1 alert');
    expect(timeline.textContent).not.toContain('Team sent');
    const titles = () => Array.from(timeline.querySelectorAll('li p:first-child'), e => e.textContent?.trim());
    expect(titles()).toEqual(['Reached site', 'New']);
    click(button('Show full history', timeline));
    expect(timeline.textContent).toContain('Team sent');
    expect(timeline.textContent).toContain('Demo team notified');
    expect(titles()).toEqual(['Reached site', 'Team sent', 'New']);
    http.verify();
  });

  it('uses server moves and keeps incident and visible member actions separate', () => {
    const { el, list, click, detail, button, query, http } = render({ incident: '7', member: '202' });
    list();
    detail();
    const incidentActions = el.querySelector('[aria-labelledby=incident-actions]')! as HTMLElement;
    const memberActions = el.querySelector('[aria-labelledby=member-alerts]')! as HTMLElement;
    expect(incidentActions.querySelector('a')?.getAttribute('href')).toBe('/monitor/map?incident=7');
    expect(button('Acknowledge', incidentActions)).toBeDefined();
    expect(button('Acknowledge', memberActions)).toBeUndefined();
    expect(memberActions.textContent).not.toContain('Alert #202');
    expect(memberActions.querySelector('a')).toBeNull();
    click(button('Alert #201', memberActions));
    expect(query().get('member')).toBe('201');
    expect(memberActions.querySelector('a')?.getAttribute('href')).toBe('/monitor/map?incident=7&alert=201');
    expect(memberActions.querySelector('a:nth-of-type(2)')?.getAttribute('href')).toBe('/monitor/alerts?alert=201');
    http.verify();
  });

  it('links a merged incident to the continuing incident and retains its history', () => {
    const { el, list, row, click, detail, http } = render();
    const merged = { ...incident, id: '8', merged_into: '7' };
    list([merged]);
    click(row());
    detail(merged);
    expect(el.textContent).toContain('This incident joined incident #7');
    expect(el.querySelector('fimra-timeline')?.textContent).toContain('New');
    expect(el.textContent).not.toContain('Next step');
    http.verify();
  });
});
