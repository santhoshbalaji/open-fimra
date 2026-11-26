import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { SituationReport, SituationReportView, duration, indiaToday, periodText, projectMap } from './situation-report';
import { WorkspaceService } from './workspace';

const square = { type: 'Polygon' as const, coordinates: [[[86.2, 16.4], [86.3, 16.4], [86.3, 16.5], [86.2, 16.5], [86.2, 16.4]]] };
const report: SituationReport = {
  snapshotId: 'snapshot-1',
  workspace: { slug: 'demo', name: 'Demo State' },
  unit: { name: 'Hill Division', levelLabel: 'Division', path: 'ds.c1.d1', hierarchy: 'territorial' },
  scope: 'Hill Division (Division)',
  period: { hours: null, from: '2026-03-09', to: '2026-03-15', days: 7, start: '2026-03-08T18:30:00Z',
    end: '2026-03-15T18:29:59.999999Z', partial: false },
  generatedAt: '2026-03-16T03:15:00Z',
  preparedFor: [{ role: 'division_officer', levelLabel: 'Division', unitName: 'Hill Division' }],
  counts: { detections: { inside: 9, outside: 4 }, queues: { unit: 6, edge_review: 1 }, overdue: 2, pendingReports: 3 },
  alerts: { total: 7, active: 5, closed: 2 },
  steps: [{ step: 'new', label: 'New', terminal: false, count: 5 }, { step: 'closed', label: 'Closed', terminal: true, count: 2 }],
  responseTimes: [{ step: 'alerted', label: 'Alert raised', median: 1500, p90: 3000, count: 7 },
    { step: 'team_sent', label: 'Team sent', median: 2400, p90: null, count: 1 }],
  placeLevels: ['Range', 'Beat'],
  incidents: { total: 1, opened: 1, active: 1, closed: 0, overdue: 1, items: [{ id: '41', stepLabel: 'Reached site',
    overdue: true, terminal: false, firstObserved: '2026-03-10T08:10:00Z', lastObserved: '2026-03-10T09:40:00Z',
    alertCount: 3, places: ['Range 2 › Beat 05'] }] },
  fieldReports: { submitted: 3, approved: 1, returned: 1, awaitingReview: 1, missing: 2, alertsMissing: 2,
    forms: [{ form: 'first-report', title: 'First report', submitted: 3, approved: 1, returned: 1, awaitingReview: 1, missing: 2 }] },
  overdueAlerts: [{ id: '1207', stepLabel: 'First report', enteredAt: '2026-03-11T04:00:00Z', overdueSeconds: 190000,
    place: 'Range 2 › Beat 05' }],
  unitLevel: 'Range',
  units: [{ code: 'R1', name: 'Range 1', path: 'ds.c1.d1.r1', alerts: 2, active: 1, overdue: 0, pendingReports: 1, formsMissing: 0 }],
  map: { bounds: [86.2, 16.4, 86.3, 16.5], outline: square, units: [{ name: 'Range 1', geometry: square }], forest: [square],
    alerts: [[86.25, 16.45, 'inside', true], [86.21, 16.41, 'near_edge', false]], alertsShown: 2,
    incidents: [{ id: '41', active: true, bounds: [86.25, 16.45, 86.25, 16.45] }] },
  labels: { forestClasses: { inside: 'Inside forest', near_edge: 'Near the forest edge', outside: 'Outside forest',
    fixed_heat_source: 'Suspected fixed heat source' }, queues: { unit: 'Unit', edge_review: 'Edge review', triage: 'Triage' },
    roles: { division_officer: 'Division officer' } },
  notes: ['Days run from midnight to midnight in India Standard Time.']
};
const units = [
  { hierarchy: 'territorial', path: 'ds.c1.d1', code: 'D1', name: 'Hill Division', depth: 2, levelLabel: 'Division' },
  { hierarchy: 'territorial', path: 'ds.c1.d1.r1', code: 'R1', name: 'Range 1', depth: 3, levelLabel: 'Range' }
];

describe('Situation report', () => {
  const deny = vi.fn();
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(),
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo', name: 'Demo State' }), deny } },
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } }] });
  });
  afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); deny.mockReset(); });

  function open() {
    const fixture = TestBed.createComponent(SituationReportView);
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne('https://api.test/api/v1/workspaces/demo/situation-report/units').flush({ units });
    fixture.detectChanges();
    return { fixture, http, page: fixture.nativeElement as HTMLElement };
  }

  it('starts with the highest unit and today, then shows the report with its numbers and map', async () => {
    const { fixture, http, page } = open();
    expect(page.querySelector('#report-unit')?.textContent).toContain('Hill Division (Division)');
    expect(page.querySelector('form')?.getAttribute('aria-describedby')).toBe('report-scope');
    expect(page.querySelector('#report-scope')?.textContent?.replace(/\s+/g, ' ')).toMatch(/Demo State.*India Standard Time/);
    const shown = fixture.componentInstance.show();
    const request = http.expectOne(r => r.url.endsWith('/situation-report'));
    expect(request.request.params.get('unitPath')).toBe('ds.c1.d1');
    expect(request.request.params.get('snapshot')).toBe('1');
    expect(request.request.params.get('hierarchy')).toBe('territorial');
    expect(request.request.params.get('from')).toBe(indiaToday());
    expect(request.request.params.has('to')).toBe(false);
    request.flush(report);
    await shown;
    fixture.detectChanges();
    const text = page.textContent?.replace(/\s+/g, ' ') ?? '';
    expect(text).toContain('Hill Division (Division) · Demo State');
    expect(text).toContain('9 March 2026 to 15 March 2026 (7 days)');
    expect(text).toContain('India Standard Time');
    expect(page.querySelector('form')).toBeNull();
    expect(page.querySelectorAll('h1')).toHaveLength(1);
    expect(document.activeElement).toBe(page.querySelector('#report-title'));
    expect(page.querySelector('caption')?.textContent).toContain('Markers shown on the map');
    expect([...page.querySelectorAll('#report-map-title ~ div tbody tr')].map(row =>
      [...row.children].map(cell => cell.textContent?.trim()))).toEqual([
      ['Inside forest', '1', '0', '1'], ['Near the forest edge', '0', '1', '1'],
      ['Outside forest', '0', '0', '0'], ['Suspected fixed heat source', '0', '0', '0'],
      ['Incidents', '1', '0', '1']
    ]);
    expect(text).toContain('Shows what is visible to: Division officer, Hill Division (Division).');
    expect([...page.querySelectorAll('.report-figures dd')].map(el => el.textContent?.trim())).toEqual(['13', '7', '5', '2', '1', '2']);
    expect(text).toContain('7 alerts in the period: 5 active and 2 closed.');
    const team = [...page.querySelectorAll('tr')].find(row => row.textContent?.includes('Team sent'));
    expect([...team!.children].map(cell => cell.textContent?.trim())).toEqual(['Team sent', '40 min', 'No data', '1']);
    expect(text).toContain('Made 16 Mar 2026, 08:45.');
    expect(text).toContain('Range 2 › Beat 05');
    expect(text).toContain('Reached site (overdue)');
    expect(text).toContain('Missing: 2, on 2 alerts.');
    expect(text).toContain('2 days 4 h');
    expect(text).toContain('By range');
    expect(page.querySelectorAll('.report-map .map-alert').length).toBe(2);
    expect(page.querySelector('.report-map .map-alert.closed')).not.toBeNull();
    expect(page.querySelector('.report-map .map-incident-label')?.textContent).toBe('Incident 41');
    const print = vi.spyOn(window, 'print').mockImplementation(() => undefined);
    ([...page.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Print'))?.click();
    expect(print).toHaveBeenCalled();
    ([...page.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Edit scope'))?.click();
    fixture.detectChanges();
    expect(page.querySelector('form')).not.toBeNull();
    expect(document.activeElement).toBe(page.querySelector('#report-unit'));
    http.verify();
  });

  it('sends a date range or past hours', async () => {
    const { fixture, http } = open();
    const view = fixture.componentInstance;
    view.pickKind('range');
    view.from.set('2026-03-09');
    view.to.set('2026-03-15');
    view.unitKey.set('territorial:ds.c1.d1.r1');
    let shown = view.show();
    let request = http.expectOne(r => r.url.endsWith('/situation-report'));
    expect(request.request.params.get('from')).toBe('2026-03-09');
    expect(request.request.params.get('to')).toBe('2026-03-15');
    expect(request.request.params.get('unitPath')).toBe('ds.c1.d1.r1');
    request.flush(report);
    await shown;
    view.pickKind('hours');
    view.hours.set(48);
    shown = view.show();
    request = http.expectOne(r => r.url.endsWith('/situation-report'));
    expect(request.request.params.get('hours')).toBe('48');
    expect(request.request.params.has('from')).toBe(false);
    request.flush(report);
    await shown;
  });

  it('shows a refusal for a unit outside the area without leaving the workspace', async () => {
    const { fixture, http, page } = open();
    const shown = fixture.componentInstance.show();
    http.expectOne(r => r.url.endsWith('/situation-report'))
      .flush({ error: 'You can report only on units in your own area.' }, { status: 403, statusText: 'Forbidden' });
    await shown;
    fixture.detectChanges();
    expect(page.querySelector('[role=alert]')?.textContent).toContain('You can report only on units in your own area.');
    expect(deny).not.toHaveBeenCalled();
  });

  it('uses one PDF button to prepare, retry and download the saved report', async () => {
    const { fixture, http, page } = open();
    const view = fixture.componentInstance;
    const shown = view.show();
    http.expectOne(r => r.url.endsWith('/situation-report')).flush(report);
    await shown;
    fixture.detectChanges();
    vi.useFakeTimers();
    const button = [...page.querySelectorAll('button')].find(button => button.textContent?.includes('Prepare PDF'))!;
    const postUrl = 'https://api.test/api/v1/workspaces/demo/situation-reports';
    const prepared = view.pdfAction();
    fixture.detectChanges();
    expect(button.textContent).toContain('Preparing PDF');
    expect(button.getAttribute('aria-busy')).toBe('true');
    button.click();
    const post = http.expectOne(postUrl);
    expect(post.request.method).toBe('POST');
    expect(post.request.body).toEqual({ snapshotId: 'snapshot-1' });
    post.flush({ id: 'r1', status: 'pending', pageCount: null });
    await prepared;
    vi.advanceTimersByTime(1000);
    http.expectOne(postUrl + '/r1').flush({ id: 'r1', status: 'failed', pageCount: null });
    await Promise.resolve();
    fixture.detectChanges();
    expect(button.textContent).toContain('Retry PDF');
    expect(page.querySelector('[role=alert]')?.textContent).toContain('could not be drawn');

    const retried = view.pdfAction();
    http.expectOne(postUrl).flush({ id: 'r1', status: 'pending', pageCount: null });
    await retried;
    vi.advanceTimersByTime(1000);
    http.expectOne(postUrl + '/r1')
      .flush({ id: 'r1', status: 'ready', pageCount: 2, fileName: 'open-fimra-demo-situation-report-2026-03-15.pdf' });
    await Promise.resolve();
    fixture.detectChanges();
    expect(button.textContent).toContain('Download PDF (2 pages)');
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:report');
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    const downloaded = view.pdfAction();
    http.expectOne(postUrl + '/r1/file').flush(new Blob(['%PDF-']));
    await downloaded;
    expect(click).toHaveBeenCalled();
    http.verify();
  });
});

describe('Situation report text', () => {
  it('writes response times in plain words', () => {
    expect([null, 0, 59.4, 60, 3599, 3600, 7500, 172800, 190000].map(duration))
      .toEqual(['No data', '0 s', '59 s', '1 min', '1 h', '1 h', '2 h 5 min', '2 days', '2 days 4 h']);
  });

  it('names one day, a range and past hours in India Standard Time', () => {
    expect(periodText({ ...report.period, from: '2026-03-15', to: '2026-03-15', days: 1 }))
      .toBe('Sunday 15 March 2026, India Standard Time');
    expect(periodText({ ...report.period, hours: 24, start: '2026-03-14T06:00:00Z', end: '2026-03-15T06:00:00Z', partial: true }))
      .toBe('Past 24 hours: 14 Mar 2026, 11:30 to 15 Mar 2026, 11:30, India Standard Time');
    expect(indiaToday(new Date('2026-03-14T19:00:00Z'))).toBe('2026-03-15');
  });

  it('projects the map with north up and keeps shapes inside the frame', () => {
    const shapes = projectMap(report.map, report.period.end, 200, 100);
    expect(shapes).not.toBeNull();
    const [inside, edge] = shapes!.alerts;
    expect(inside.y).toBeLessThan(edge.y);
    expect(shapes!.incidents[0].x).toBeGreaterThan(inside.x);
    expect(shapes!.incidents[0].y).toBeLessThan(inside.y);
    for (const point of shapes!.alerts) {
      expect(point.x).toBeGreaterThan(0);
      expect(point.x).toBeLessThan(200);
      expect(point.y).toBeGreaterThan(0);
      expect(point.y).toBeLessThan(100);
    }
    expect(shapes!.outline.startsWith('M')).toBe(true);
    expect(projectMap({ ...report.map, bounds: null })).toBeNull();
    // Labels that would cover each other are left out.
    const crowded = projectMap({ ...report.map, incidents: [report.map.incidents[0], { ...report.map.incidents[0], id: '42' }] });
    expect(crowded!.incidents.map(incident => incident.labelled)).toEqual([true, false]);
  });
});
