import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertQueue } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
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
      {provide: WorkspaceService, useValue: {current: signal({slug: 'demo'})}},
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
    expect(page.querySelector('.metric-grid dl')?.textContent).toContain('Inside forest4');
    expect([...page.querySelectorAll('.metric-grid strong')].map(el => el.textContent)).toEqual(['3', '2', '1']);
    expect(page.querySelector('tbody')?.textContent).toContain('Inside forest9');
    expect(page.textContent).toContain('Acknowledged2 min4 min');
    expect(page.textContent).toContain('Quiet');
    expect(page.querySelector('fimra-class-chart')).toBeNull();
    vi.advanceTimersByTime(60_000);
    http.expectOne(r => r.url.endsWith('/summary')).flush({...data, feedHealth: null});
    fixture.detectChanges();
    expect(page.textContent).not.toContain('Feed health');
    http.verify();
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
