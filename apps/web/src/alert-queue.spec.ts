import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { convertToParamMap } from '@angular/router';
import { ShellNavigation } from './shell-navigation';
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
  });
  function start() {
    queue = TestBed.inject(AlertQueue);
    http = TestBed.inject(HttpTestingController);
  }
  it('shows counts and only offers review queues to an eligible grant', () => {
    start();
    request('queue', 'unit').flush({ ...result, canReview: false, counts: {} });
    expect(queue.tabs()).toEqual(['unit']);
    expect(queue.rows()).toEqual([]);
  });
  it('selects a map point in its own queue even when it is beyond the first page', () => {
    start();
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
    start();
    vi.useFakeTimers();
    try {
      request('queue', 'unit').flush(result);
      queue.switchTo('edge_review');
      request('queue', 'edge_review').flush({ ...result, nextCursor: 'older' });
      queue.next();
      request('cursor', 'older').flush({ ...result, items: [edge] });
      queue.select({ ...edge, id: edge.detection_id });
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
    start();
    const first = request('hours', '168');
    queue.select({ ...edge, id: edge.detection_id });
    const selection = request('detection', '17');
    queue.hours.set(24);
    request('hours', '24').flush(result);
    expect(first.cancelled).toBe(true);
    expect(selection.cancelled).toBe(true);
    expect(queue.selected()).toBeNull();
    expect(queue.table().position.cursor).toBeNull();
    http.verify();
  });
  it('restores URL page history, keeps scope and selection during refresh and retries the same sorted page', () => {
    let params: Record<string, string | null> = {
      queue: 'all', group: 'overdue', alert: '3', hours: '72',
      alertSort: 'oldest', cursor: 'third', alertTrail: '-|second'
    };
    const query = signal(convertToParamMap(params));
    const update = (changes: Record<string, string | null>) => {
      params = { ...params, ...changes };
      query.set(convertToParamMap(params));
    };
    TestBed.configureTestingModule({ providers: [{ provide: ShellNavigation, useValue: { query, update } }] });
    start();
    queue.hours.set(72);
    const third = request('cursor', 'third');
    expect(third.request.params.get('sort')).toBe('oldest');
    expect(third.request.params.get('group')).toBe('overdue');
    expect(third.request.params.get('hours')).toBe('72');
    expect(third.request.params.has('queue')).toBe(false);
    third.flush({ ...result, items: [edge] });
    queue.selectLinked(edge);
    expect(queue.table().position.previousCursors).toEqual([null, 'second']);

    queue.refresh();
    const refresh = request('cursor', 'third');
    expect(queue.state().kind).toBe('stale');
    expect(queue.table().rows).toEqual([edge]);
    refresh.flush({}, { status: 500, statusText: 'Unavailable' });
    expect(queue.state()).toMatchObject({ kind: 'error', keepContent: true });
    expect(queue.table().rows).toEqual([edge]);
    expect(queue.selected()?.id).toBe('17');

    queue.refresh();
    request('cursor', 'third').flush({ ...result, items: [edge] });
    queue.previous();
    request('cursor', 'second').flush({ ...result, items: [edge], nextCursor: 'third' });
    expect(queue.table().position.previousCursors).toEqual([null]);
    queue.next();
    request('cursor', 'third').flush({ ...result, items: [edge] });
    queue.changePage({ cursor: null, previousCursors: [], sort: { columnId: 'observed', direction: 'descending' } });
    TestBed.tick();
    http.expectOne(req => !req.params.has('sort') && !req.params.has('cursor')).flush(result);
    expect(params).toMatchObject({ queue: 'all', group: 'overdue', alert: '3', hours: '72', cursor: null, alertTrail: null });
    expect(queue.selected()?.id).toBe('17');
    http.verify();
  });
});
