import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertQueue, AlertRow } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { WorkspaceService } from './workspace';

describe('Alert queue', () => {
  const edge: AlertRow = { detection_id: '17', id: '3', queue: 'edge_review', status: 'new', step_label: 'New', step_time: '', overdue: false, forest_class: 'near_edge',
    observed_at: Math.floor(Date.now() / 1000), longitude: 86, latitude: 16 };
  const result = { counts: { unit: 0, edge_review: 1 }, canReview: true, items: [] as AlertRow[] };
  let queue: AlertQueue;
  let http: HttpTestingController;
  function request(param: string, value: string) {
    TestBed.tick();
    return http.expectOne(req => req.params.get(param) === value);
  }
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [
      AlertQueue, provideHttpClient(), provideHttpClientTesting(),
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }), deny: vi.fn() } },
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } }
    ] });
    queue = TestBed.inject(AlertQueue);
    http = TestBed.inject(HttpTestingController);
  });
  it('shows counts and only offers review queues to an eligible grant', () => {
    request('queue', 'unit').flush({ ...result, canReview: false, counts: {} });
    expect(queue.tabs()).toEqual(['unit']);
    expect(queue.rows()).toEqual([]);
  });
  it('selects a map point in its own queue even when it is beyond the first page', () => {
    request('queue', 'unit').flush(result);
    queue.select({ ...edge, id: 17 });
    request('detection', '17').flush({ ...result, items: [edge] });
    request('queue', 'edge_review').flush(result);
    expect(queue.active()).toBe('edge_review');
    expect(queue.rows()).toEqual([edge]);
    expect(queue.page()?.counts.edge_review).toBe(1);
    expect(queue.tabs()).toEqual(['unit', 'edge_review', 'triage']);
    queue.select(null);
    expect(queue.rows()).toEqual([]);
    http.verify();
  });
  it('keeps row selection and the current page across the minute refresh', () => {
    vi.useFakeTimers();
    try {
      request('queue', 'unit').flush(result);
      queue.switchTo('edge_review');
      request('queue', 'edge_review').flush({ ...result, nextCursor: 'older' });
      queue.changePage('older');
      request('cursor', 'older').flush({ ...result, items: [edge] });
      queue.selectRow(queue.rows()[0]);
      request('detection', '17').flush({ ...result, items: [edge] });
      vi.advanceTimersByTime(60_000);
      request('cursor', 'older').flush({ ...result, items: [edge] });
      request('detection', '17').flush({ ...result, items: [edge] });
      expect(queue.selected()?.id).toBe(edge.detection_id);
      expect(queue.rows()).toEqual([edge]);
      http.verify();
    } finally { vi.useRealTimers(); }
  });
  it('cancels stale pages and point lookups when the shared time window changes', () => {
    const first = request('hours', '168');
    queue.selectRow(edge);
    const selection = request('detection', '17');
    queue.hours.set(24);
    request('hours', '24').flush(result);
    expect(first.cancelled).toBe(true);
    expect(selection.cancelled).toBe(true);
    expect(queue.selected()).toBeNull();
    expect(queue.cursor()).toBe('');
    http.verify();
  });
});
