import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { DeferBlockBehavior, DeferBlockState, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertQueue } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Dashboard } from './dashboard';
import { DashboardFilters } from './dashboard-filters';
import { detectionSeries, detectionSeriesColors } from './dashboard.series';
import { WorkspaceService } from './workspace';

describe('Control-room dashboard', () => {
  const data = {
    today: { inside: 4, near_edge: 2 }, detections: { inside: 9, near_edge: 3 },
    active: 3, overdue: 2, backlogOverdue: 7, pendingReports: 1, queues: { unit: 2, edge_review: 1 },
    steps: { acknowledged: 2, controlled: 1 },
    responseSeconds: { acknowledged: { median: 120, p90: 240 } },
    feedHealth: [{ source: 'Demo', product: 'sample', status: 'quiet',
      lastSuccessAt: '2026-11-25T09:00:00Z', latestObservationAt: '2026-11-25T08:00:00Z' }]
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-11-26T12:00:00Z'));
    TestBed.configureTestingModule({
      deferBlockBehavior: DeferBlockBehavior.Manual,
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting(), AlertQueue,
        { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
        { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo', name: 'Demo State' }),
          canManageLayers: signal(true), jurisdictions: signal([['ds.c1', 'Demo Circle']]), allowsScope: () => true, deny: vi.fn() } },
        { provide: AuthService, useValue: { sessionEnded: vi.fn() } }]
    });
  });

  afterEach(() => {
    const http = TestBed.inject(HttpTestingController);
    http.match(r => r.url.endsWith('/situation-report/units')).forEach(request => request.flush({ units: [] }));
    http.verify();
    localStorage.clear();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function open(response: object = data) {
    const fixture = TestBed.createComponent(Dashboard);
    fixture.detectChanges();
    const page = fixture.nativeElement as HTMLElement;
    expect(page.textContent).toContain('Loading dashboard');
    expect(page.querySelector('[data-dashboard-skeleton]')?.closest('[aria-hidden=true]')).toBeTruthy();
    vi.advanceTimersByTime(0);
    flushSummary(response);
    fixture.detectChanges();
    expect(page.querySelector('[data-dashboard-skeleton]')).toBeNull();
    return { fixture, page };
  }

  function reviewRequest() {
    return TestBed.inject(HttpTestingController).expectOne(r => r.url.endsWith('/submissions'));
  }

  function flushSummary(response: object) {
    const http = TestBed.inject(HttpTestingController);
    http.match(r => r.url.endsWith('/situation-report/units')).forEach(request => request.flush({
      units: [{ path: 'ds.c1', name: 'Demo Circle', levelLabel: 'Circle' }]
    }));
    http.expectOne(r => r.url.endsWith('/summary')).flush(response);
    reviewRequest().flush({ items: [{ id: 'one-page-item' }], counts: { pending: 37, approved: 5 } });
  }

  it('keeps jurisdiction, period and last update visible and labels the midnight scope', () => {
    const filters = TestBed.inject(DashboardFilters);
    filters.hours.set(72);
    filters.forestClass.set('inside');
    filters.unitPath.set('ds.c1');
    const { page } = open();

    const header = page.querySelector('fimra-page-header');
    expect(header?.textContent).toContain('Demo State · Demo Circle (Circle) · Inside forest · Last 3 days');
    expect(header?.textContent).toContain('Last successful update');
    expect(header?.querySelector('time')?.getAttribute('datetime')).toBe('2026-11-26T12:00:00.000Z');
    const [today, active] = page.querySelectorAll('fimra-stat-tile');
    expect(today.textContent).toContain('Since midnight, India Standard Time');
    expect(active.textContent).toContain('Last 3 days');
    expect(page.querySelectorAll('fimra-stat-tile time')).toHaveLength(0);
  });

  it('links each actionable count to its matching list and carries workspace filters', () => {
    const filters = TestBed.inject(DashboardFilters);
    filters.hours.set(72);
    filters.forestClass.set('inside');
    filters.unitPath.set('ds.c1');
    const { page } = open();
    for (const [label, matches] of [
      ['Active alerts', { queue: 'all', group: 'active' }],
      ['Overdue alerts', { queue: 'all', group: 'overdue', alertSort: 'overdue' }],
      ['Waiting for a report', { queue: 'all', group: 'pending_reports' }],
      ['Near a forest boundary', { queue: 'edge_review' }],
      ['Controlled', { queue: 'all', status: 'controlled' }],
      ['Acknowledged', { queue: 'all', status: 'acknowledged' }]
    ] as const) {
      const link = [...page.querySelectorAll('a')].find(a => a.textContent?.trim() === label);
      const url = new URL(link!.href);
      expect(url.pathname).toBe('/monitor/alerts');
      expect(Object.fromEntries(url.searchParams)).toEqual({
        workspace: 'demo', ...(label === 'Overdue alerts' ? {} : { hours: '72', class: 'inside', unitPath: 'ds.c1' }), ...matches
      });
    }
    expect(page.querySelector('#count-overdue')?.textContent).toBe('7');
    expect(page.querySelector('#count-pending_reports')?.textContent).toBe('1');
    expect(page.querySelector('#count-pending_reports + dd')?.textContent).toBe('Alerts observed · Last 3 days');
    const link = [...page.querySelectorAll('a')].find(a => a.textContent?.trim() === 'Awaiting review')!;
    const url = new URL(link.href);
    expect(url.pathname).toBe('/reports/field');
    expect(Object.fromEntries(url.searchParams)).toEqual({
      workspace: 'demo', hours: '72', class: 'inside', unitPath: 'ds.c1', decision: 'pending'
    });
    expect(page.querySelector('#count-awaiting-review')?.textContent).toBe('37');
    expect(page.querySelector('#count-awaiting-review + dd')?.textContent).toBe('Field reports received · Last 3 days');
  });

  it('requests the field reports total for the same filters and distinguishes failure from zero', () => {
    const filters = TestBed.inject(DashboardFilters);
    filters.hours.set(72);
    filters.forestClass.set('inside');
    filters.unitPath.set('ds.c1');
    const fixture = TestBed.createComponent(Dashboard);
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(r => r.url.endsWith('/summary')).flush(data);
    const review = reviewRequest();
    expect(review.request.urlWithParams).toBe(
      'https://api.test/api/v1/workspaces/demo/submissions?hours=72&class=inside&unitPath=ds.c1&decision=pending&limit=1'
    );
    review.flush(null, { status: 500, statusText: 'Server error' });
    fixture.detectChanges();
    const page = fixture.nativeElement as HTMLElement;
    expect(page.querySelector('#count-awaiting-review')?.textContent).toBe('Unavailable');
    expect(page.querySelector('#count-pending_reports')?.textContent).toBe('1');

    vi.advanceTimersByTime(60_000);
    http.expectOne(r => r.url.endsWith('/summary')).flush(data);
    reviewRequest().flush({ items: [], counts: { approved: 5 } });
    fixture.detectChanges();
    expect(page.querySelector('#count-awaiting-review')?.textContent).toBe('0');
  });

  it('renders the same chart and table totals and keeps every value in low bandwidth', async () => {
    const { fixture, page } = open();
    const [chart] = await fixture.getDeferBlocks();
    await chart.render(DeferBlockState.Complete);
    const graphic = page.querySelector('fimra-class-chart')!;
    expect(graphic.querySelector('p span:last-child')?.textContent).toBe('12');
    expect(page.querySelector('tfoot td')?.textContent?.trim()).toBe('12');
    expect([...graphic.querySelectorAll('li:first-child span')].map(el => el.textContent)).toEqual(['Inside forest', '9']);
    const values = () => [...page.querySelectorAll('dd, td, time')].map(el => el.textContent?.trim());
    const before = values();
    TestBed.inject(DashboardFilters).setLowBandwidth(true);
    fixture.detectChanges();
    expect(page.querySelector('fimra-class-chart')).toBeNull();
    expect(values()).toEqual(before);
    expect(before).toContain('2 minutes');
    expect(before).toContain('4 minutes');
  });

  it('keeps individual category counts in the table when the chart uses Other', async () => {
    const detections = { ...data.detections, one: 1, two: 2, three: 3, four: 4, five: 5, six: 6 };
    const { fixture, page } = open({ ...data, detections });
    const [chart] = await fixture.getDeferBlocks();
    await chart.render(DeferBlockState.Complete);
    const graphic = page.querySelector('fimra-class-chart')!;
    expect(graphic.querySelectorAll('li')).toHaveLength(8);
    expect(graphic.querySelector('li:last-child span')?.textContent).toBe('Other');
    expect(graphic.querySelector('p span:last-child')?.textContent).toBe('33');
    expect(page.querySelector('tfoot td')?.textContent?.trim()).toBe('33');

    TestBed.inject(DashboardFilters).setLowBandwidth(true);
    fixture.detectChanges();
    const table = [...page.querySelectorAll('tbody tr')];
    expect(table).toHaveLength(10);
    expect(table.find(row => row.querySelector('th')?.textContent?.trim() === 'six')?.querySelector('td')?.textContent?.trim())
      .toBe('6');
  });

  it('separates zero recorded detections from missing or late feed data', () => {
    const response = { ...data, detections: {}, today: {}, steps: {}, feedHealth: [] };
    const { fixture, page } = open(response);
    expect(page.textContent).toContain('Zero detections recorded in this window');
    expect(page.textContent).toContain('No live feeds have polled yet');
    expect(page.textContent).toContain('Recorded fire outcomes');
    expect(page.querySelector('tfoot td')?.textContent?.trim()).toBe('0');
    expect(page.querySelector('fimra-class-chart')).toBeNull();

    vi.advanceTimersByTime(60_000);
    flushSummary({ ...response, feedHealth: data.feedHealth });
    fixture.detectChanges();
    expect(page.textContent).toContain('Some feeds are late, quiet, or missing data');
    expect(page.querySelector('[aria-labelledby=feeds-title] fimra-status-badge')?.textContent?.trim()).toBe('Quiet');

    vi.advanceTimersByTime(60_000);
    flushSummary({
      ...response, feedHealth: null, feedHealthUnavailable: true
    });
    fixture.detectChanges();
    expect(page.textContent).toContain('Feed health could not load');
    expect(page.querySelector('tfoot td')?.textContent?.trim()).toBe('0');
  });

  it('shows satellite latency, windows and match sample counts in feed health', () => {
    const firstSeen = { count: 214, p50Seconds: 6720, p95Seconds: 9600, maxSeconds: 10800 };
    const matched = { count: 200, p50Seconds: 7200, p95Seconds: 9900, maxSeconds: 10920 };
    const aqua = { count: 1, p50Seconds: 3000, p95Seconds: 3000, maxSeconds: 3000 };
    const { page } = open({ ...data, feedHealth: [
      { ...data.feedHealth[0], product: 'VIIRS_NOAA21_NRT', latency: [
        { satellite: 'NOAA-21', windowHours: 168, firstSeen, matched }
      ] },
      { ...data.feedHealth[0], product: 'MODIS_NRT', latency: [
        { satellite: 'Aqua', windowHours: 24, firstSeen: aqua,
          matched: { count: 0, p50Seconds: null, p95Seconds: null, maxSeconds: null } }
      ] }
    ] });
    const health = page.querySelector('[aria-labelledby=feeds-title]')?.textContent;
    expect(health).toContain('NOAA-21 · Last 7 days');
    expect(health).toContain('First seen typically 1 hour, 52 minutes after observation, 95% within 2 hours, 40 minutes, longest 3 hours (214 detections).');
    expect(health).toContain('Matched to this workspace typically 2 hours after observation, 95% within 2 hours, 45 minutes');
    expect(health).toContain('Aqua · Last 24 hours');
    expect(health).toContain('No timed workspace matches');
  });

  it('shows a neutral coverage note for users who cannot manage feeds', () => {
    Object.assign(TestBed.inject(WorkspaceService), { canManageLayers: signal(false) });
    const { page } = open();
    expect(page.textContent).toContain('Counts reflect the observations recorded so far.');
    expect(page.textContent).not.toContain('Some feeds are late');
    expect(page.querySelector('#feeds-title')).toBeNull();
  });

  it('keeps the last successful summary on refresh errors and retries on request', () => {
    const { fixture, page } = open();
    const http = TestBed.inject(HttpTestingController);
    vi.advanceTimersByTime(60_000);
    const review = reviewRequest();
    http.expectOne(r => r.url.endsWith('/summary')).flush(null, { status: 500, statusText: 'Server error' });
    expect(review.cancelled).toBe(true);
    fixture.detectChanges();
    expect(page.textContent).toContain('Showing the last successful update');
    expect(page.querySelector('time')?.getAttribute('datetime')).toBe('2026-11-26T12:00:00.000Z');
    expect(page.querySelector('tfoot td')?.textContent?.trim()).toBe('12');
    page.querySelector('fimra-state-region button')?.dispatchEvent(new MouseEvent('click'));
    flushSummary({ ...data, feedHealth: null });
    fixture.detectChanges();
    expect(page.textContent).not.toContain('Dashboard could not refresh');
    expect(page.querySelector('time')?.getAttribute('datetime')).toBe('2026-11-26T12:01:00.000Z');
  });

  it('uses one filter selection for the dashboard, queue and tile request', () => {
    const filters = TestBed.inject(DashboardFilters);
    TestBed.inject(AlertQueue);
    filters.hours.set(72);
    filters.forestClass.set('inside');
    filters.unitPath.set('ds.c1');
    open();
    const request = TestBed.inject(HttpTestingController).expectOne(r => r.url.endsWith('/alerts'));
    expect(Object.fromEntries(request.request.params.keys().map(key => [key, request.request.params.get(key)])))
      .toEqual({ hours: '72', class: 'inside', unitPath: 'ds.c1', queue: 'unit', limit: '25' });
    expect(filters.tileClasses()).toBe('inside');
    request.flush({ items: [], counts: {}, canReview: false });
  });

  it('remembers low bandwidth and works when storage is blocked', () => {
    const filters = new DashboardFilters();
    filters.setLowBandwidth(true);
    expect(new DashboardFilters().lowBandwidth()).toBe(true);
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw Error('Storage unavailable'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw Error('Storage unavailable'); });
    const blocked = new DashboardFilters();
    blocked.setLowBandwidth(true);
    expect(blocked.lowBandwidth()).toBe(true);
  });
});

describe('Detection chart series', () => {
  it('uses fixed blue, purple, grey and amber slots for the four forest classes', () => {
    expect(detectionSeries({}).map(({ slot }) => detectionSeriesColors[slot - 1])).toEqual([
      '--color-chart-1', '--color-chart-7', '--color-text-secondary', '--color-chart-4'
    ]);
  });

  it('keeps the original slot when earlier series are hidden', () => {
    const all = detectionSeries({ inside: 5, near_edge: 3 });
    const filtered = detectionSeries({ near_edge: 3 }, 'near_edge');
    expect(filtered).toEqual([all[1]]);
    expect(filtered[0].slot).toBe(2);
  });

  it('folds the remaining values into Other in the eighth slot', () => {
    const keys = Array.from({ length: 10 }, (_, index) => 'class' + index);
    const catalog = Object.fromEntries(keys.map(key => [key, key]));
    const counts = Object.fromEntries(keys.map((key, index) => [key, index + 1]));
    const rows = detectionSeries(counts, '', catalog);
    expect(rows.map(row => row.slot)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(rows[7]).toMatchObject({ label: 'Other', count: 27 });
    expect(rows.reduce((sum, row) => sum + row.count, 0)).toBe(55);
    expect(detectionSeries({ class9: 10 }, 'class9', catalog)).toEqual([{ id: '__other__', label: 'Other', slot: 8, count: 10 }]);
    expect(detectionSeries(counts, 'class3', catalog)).toEqual([rows[3]]);
  });
});
