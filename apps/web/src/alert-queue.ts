import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Detection } from './detections';
import { WorkspaceService } from './workspace';

import { DashboardFilters } from './dashboard-filters';

export const queueLabels = { unit: 'Unit', edge_review: 'Edge review', triage: 'Triage' };
type Queue = keyof typeof queueLabels;
export interface AlertRow extends Detection {
  id: string;
  detection_id: string;
  status: string;
  step_label: string;
  step_time: string;
  overdue: boolean;
  queue: Queue;
  longitude: number;
  latitude: number;
}
interface Page { items: AlertRow[]; counts: Partial<Record<Queue, number>>; canReview: boolean; nextCursor?: string }

@Injectable()
export class AlertQueue {
  private readonly http = inject(HttpClient);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly workspace = inject(WorkspaceService);
  private readonly auth = inject(AuthService);
  private readonly filters = inject(DashboardFilters);
  readonly hours = this.filters.hours;
  readonly now = signal(Date.now());
  readonly selected = signal<Detection | null>(null);
  readonly active = signal<Queue>('unit');
  readonly cursor = signal('');
  readonly page = signal<Page | null>(null);
  readonly message = signal('');
  readonly pinned = signal<AlertRow | null>(null);
  readonly linked = signal<AlertRow | null>(null);
  readonly tabs = computed<Queue[]>(() => this.page()?.canReview ? ['unit', 'edge_review', 'triage'] : ['unit']);
  readonly rows = computed(() => {
    const rows = this.page()?.items ?? [];
    const pin = this.pinned();
    return pin && pin.queue === this.active() && !rows.some(row => row.id === pin.id) ? [pin, ...rows] : rows;
  });
  readonly endpoint = computed(() => {
    const workspace = this.workspace.current();
    return workspace && this.base + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug) + '/alerts';
  });

  constructor() {
    effect(onCleanup => {
      const timer = setInterval(() => this.now.set(Date.now()), 60_000);
      onCleanup(() => clearInterval(timer));
    });
    effect(() => {
      this.endpoint();
      this.filters.params();
      this.message.set('');
      this.page.set(null);
      this.cursor.set('');
      this.selected.set(null);
      this.linked.set(null);
      this.pinned.set(null);
    });
    effect(onCleanup => {
      const url = this.endpoint();
      const params: Record<string, string | number> = { ...this.filters.params(), queue: this.active() };
      const cursor = this.cursor();
      if (cursor) params['cursor'] = cursor;
      this.now();
      if (!url) return;
      const request = this.http.get<Page>(url, { params }).subscribe({
        next: page => {
          this.page.set(page);
          this.message.set('');
          if (!page.canReview && this.active() !== 'unit') this.switchTo('unit');
        },
        error: (error: HttpErrorResponse) => this.failed(error)
      });
      onCleanup(() => request.unsubscribe());
    });
    // A map tile can contain a point on another queue page, or a detection
    // with no visible alert. Resolve it with the same grants as the list.
    effect(onCleanup => {
      const point = this.selected();
      const url = this.endpoint();
      const hours = this.hours();
      const now = this.now();
      if (this.linked() || !point || !url) return;
      if (point.observed_at * 1000 < now - hours * 3600_000) { this.select(null); return; }
      const request = this.http.get<Page>(url, { params: { ...this.filters.params(), detection: String(point.id) } }).subscribe({
        next: result => {
          const row = result.items[0];
          this.pinned.set(row ?? null);
          if (row && row.queue !== this.active()) {
            this.page.set(null);
            this.cursor.set('');
            this.active.set(row.queue);
          }
        },
        error: (error: HttpErrorResponse) => { this.pinned.set(null); this.failed(error); }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  refresh(): void {
    this.now.set(Date.now());
  }

  select(point: Detection | null): void {
    this.linked.set(null);
    this.pinned.set(null);
    this.selected.set(point);
  }

  selectRow(row: AlertRow): void {
    this.select({ ...row, id: row.detection_id });
  }

  selectLinked(row: AlertRow): void {
    this.changePage('');
    this.active.set(row.queue);
    this.linked.set(row);
    this.pinned.set(row);
    this.selected.set({ ...row, id: row.detection_id });
  }

  switchTo(queue: Queue): void {
    if (this.active() === queue) return;
    this.select(null);
    this.active.set(queue);
    this.changePage('');
  }

  changePage(cursor: string): void {
    this.page.update(page => page && { ...page, items: [], nextCursor: undefined });
    this.cursor.set(cursor);
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    else if (error.status === 403) this.workspace.deny();
    this.message.set('Alerts could not load. Trying again in a minute.');
  }
}
