import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertQueue } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { chartColors } from './class-chart';
import { Dashboard } from './dashboard';
import { DashboardFilters } from './dashboard-filters';
import { WorkspaceService } from './workspace';

describe('Control-room dashboard', () => {
  const data = {today: {inside: 4}, detections: {inside: 9}, active: 3, overdue: 2, pendingReports: 1,
    queues: {unit: 3}, steps: {acknowledged: 2}, responseSeconds: {
      acknowledged: {median: 120, p90: 240}, reached_site: {median: null, p90: null}, controlled: {median: null, p90: null}},
    feedHealth: [{source: 'Demo', product: 'sample', status: 'quiet'}]};
  beforeEach(() => {
    vi.useFakeTimers();
    TestBed.configureTestingModule({providers: [provideHttpClient(), provideHttpClientTesting(), AlertQueue,
      {provide: WEB_CONFIG, useValue: {apiBaseUrl: 'https://api.test'}},
      {provide: WorkspaceService, useValue: {current: signal({slug: 'demo', name: 'Demo State'}),
        jurisdictions: signal([['ds.c1.d1', 'Hill Division']])}},
      {provide: AuthService, useValue: {sessionEnded: vi.fn()}}
    ]});
  });
  afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); vi.useRealTimers(); });

  it('keeps counts, response times, feed labels and chart data available in low bandwidth', () => {
    TestBed.inject(DashboardFilters).setLowBandwidth(true);
    const fixture = TestBed.createComponent(Dashboard);
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(r => r.url.endsWith('/summary')).flush(data);
    fixture.detectChanges();
    const page = fixture.nativeElement as HTMLElement;
    expect(page.querySelector('#dashboard-scope')?.textContent).toContain('Jurisdiction: Demo State · Hill Division');
    expect(page.querySelector('.stat-grid')?.getAttribute('aria-describedby')).toBe('dashboard-scope');
    expect(page.querySelector('[aria-labelledby=today-title]')?.textContent).toContain('Since midnight, India Standard Time');
    expect(page.querySelector('[aria-labelledby=tile-active]')?.textContent).toContain('Past 168 hours');
    expect(page.querySelector('.stat-grid dl')?.textContent).toContain('Inside forest4');
    expect([...page.querySelectorAll('.stat-value')].map(el => el.textContent)).toEqual(['3', '2', '1']);
    expect(page.querySelector('tbody')?.textContent).toContain('Inside forest9');
    expect(page.textContent?.replace(/\s+/g, '')).toContain('Acknowledged2min4min');
    const feed = page.querySelector('[aria-labelledby=feeds-title] li') as HTMLElement;
    expect(feed.textContent).toContain('Demo · sample');
    expect(feed.querySelector('fimra-status-badge')?.textContent?.trim()).toBe('Quiet');
    expect(feed.querySelector('fimra-status-badge svg')?.getAttribute('aria-hidden')).toBe('true');
    expect(page.querySelector('[aria-labelledby=steps-title]')?.textContent).toContain('Acknowledged');
    expect(page.querySelector('fimra-class-chart')).toBeNull();
    vi.advanceTimersByTime(60_000);
    http.expectOne(r => r.url.endsWith('/summary')).flush({...data, feedHealth: null});
    fixture.detectChanges();
    expect(page.textContent).not.toContain('Feed health');
    http.verify();
  });

  it('shows skeletons while loading and empty states when there is no data', () => {
    const fixture = TestBed.createComponent(Dashboard);
    fixture.detectChanges();
    const page = fixture.nativeElement as HTMLElement;
    expect(page.querySelector('[role=status]')?.textContent).toContain('Loading dashboard');
    expect(page.querySelectorAll('hlm-skeleton').length).toBeGreaterThan(0);
    vi.advanceTimersByTime(0);
    TestBed.inject(HttpTestingController).expectOne(r => r.url.endsWith('/summary'))
      .flush({...data, today: {}, detections: {}, steps: {}, feedHealth: []});
    fixture.detectChanges();
    expect(page.querySelector('hlm-skeleton')).toBeNull();
    expect([...page.querySelectorAll('fimra-empty-state')].map(el => el.textContent?.trim()))
      .toEqual([expect.stringContaining('No detections'), expect.stringContaining('No alerts'),
        expect.stringContaining('No feeds have polled yet.')]);
    expect(page.querySelector('fimra-class-chart')).toBeNull();
  });

  it('reads chart colours from the theme in a fixed order', () => {
    const root = document.createElement('div');
    ['first', 'second', 'third', 'fourth'].forEach((colour, index) => root.style.setProperty('--color-chart-' + (index + 1), colour));
    root.style.setProperty('--color-text', 'ink');
    document.body.append(root);
    const colours = chartColors(root);
    root.remove();
    expect(colours.series).toEqual(['first', 'second', 'third', 'fourth']);
    expect(colours.text).toBe('ink');
  });

  it('uses one filter selection for the dashboard, queue and tile request', () => {
    const filters = TestBed.inject(DashboardFilters);
    TestBed.inject(AlertQueue);
    filters.hours.set(72);
    filters.forestClass.set('inside');
    filters.unitPath.set('ds.c1');
    const fixture = TestBed.createComponent(Dashboard);
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    const requests = TestBed.inject(HttpTestingController).match(r =>
      r.params.get('hours') === '72' && r.params.get('class') === 'inside' && r.params.get('unitPath') === 'ds.c1');
    expect(requests.map(r => r.request.url.split('/').pop()).sort()).toEqual(['alerts', 'summary']);
    expect(filters.tileClasses()).toBe('inside');
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
