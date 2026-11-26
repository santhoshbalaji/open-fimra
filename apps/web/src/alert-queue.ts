import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Detection } from './detections';
import { WorkspaceService } from './workspace';

import { ShellNavigation } from './shell-navigation';
import { DashboardFilters } from './dashboard-filters';
import type { TablePage, TablePosition } from './ui/data-table';
import type { DataState } from './ui/state-region';
import { defaultWorkspaceFormat } from './workspace-format';

export const queueLabels = { unit: 'My area', edge_review: 'Edge review', triage: 'Triage' };
export const alertGroupLabels = { active: 'Active alerts', overdue: 'Overdue alerts', pending_reports: 'Waiting for a report' };
export const stepLabel = (key: string) => key.charAt(0).toUpperCase() + key.slice(1).replaceAll('_', ' ');
type Queue = keyof typeof queueLabels;
export interface AlertRow extends Detection {
  id: string;
  detection_id: string;
  incident_id?: string | null;
  status: string;
  step_label: string;
  step_time: string;
  overdue: boolean;
  next_action?: string;
  queue: Queue;
  longitude: number;
  latitude: number;
}
interface Page { items: AlertRow[]; counts: Partial<Record<Queue, number>>; canReview: boolean; nextCursor?: string }

/** Name the place first and distinguish alerts from different observations there. */
export function alertRowLabel(row: AlertRow, place: string): string {
  return `${place} · Alert #${row.id}`;
}

const firstPage: TablePosition = {
  cursor: null, previousCursors: [], sort: { columnId: 'observed', direction: 'descending' }
};

@Injectable()
export class AlertQueue {
  private readonly http = inject(HttpClient);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly workspace = inject(WorkspaceService);
  private readonly auth = inject(AuthService);
  private readonly filters = inject(DashboardFilters);
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly localQueue = signal<Queue>('unit');
  private readonly localPosition = signal(firstPage);
  private readonly saved = signal<{ page: Page; position: TablePosition; at: Date } | null>(null);
  private readonly loading = signal(true);
  private readonly failed = signal(false);
  readonly hours = this.filters.hours;
  readonly now = signal(Date.now());
  readonly revision = signal(0);
  readonly selected = signal<Detection | null>(null);
  readonly active = computed<Queue>(() => {
    const value = this.navigation?.query().get('queue');
    if (value === 'unit' || value === 'edge_review' || value === 'triage') return value;
    if (value === 'all') return 'unit';
    return this.navigation ? this.linked()?.queue ?? 'unit' : this.localQueue();
  });
  readonly allQueues = computed(() => this.navigation?.query().get('queue') === 'all');
  private readonly group = computed(() => this.navigation?.query().get('group') || '');
  private readonly status = computed(() => this.navigation?.query().get('status') || '');
  readonly listScope = computed(() => {
    const group = this.group();
    return [Object.hasOwn(alertGroupLabels, group) ? alertGroupLabels[group as keyof typeof alertGroupLabels] : '',
      stepLabel(this.status())].filter(Boolean).join(' · ');
  });
  private readonly cursor = computed(() => this.navigation ? this.navigation.query().get('cursor') : this.localPosition().cursor);
  private readonly trail = computed(() => this.navigation?.query().get('alertTrail'));
  private readonly sortOrder = computed(() => {
    if (!this.navigation) return this.localPosition().sort?.direction === 'ascending' ? 'oldest' : 'newest';
    return this.navigation.query().get('alertSort') === 'oldest' ? 'oldest' : 'newest';
  });
  private readonly position = computed<TablePosition>(() => {
    if (!this.navigation) return this.localPosition();
    const cursor = this.cursor();
    return {
      cursor,
      previousCursors: cursor ? (this.trail() || '-').split('|').map(value => value === '-' ? null : value) : [],
      sort: { columnId: 'observed', direction: this.sortOrder() === 'oldest' ? 'ascending' : 'descending' }
    };
  });
  readonly page = computed(() => this.saved()?.page);
  private readonly canReview = signal(false);
  readonly pinned = signal<AlertRow | null>(null);
  readonly linked = signal<AlertRow | null>(null);
  readonly tabs = computed<Queue[]>(() => this.canReview() ? ['unit', 'edge_review', 'triage'] : ['unit']);
  readonly table = computed<TablePage<AlertRow>>(() => {
    const saved = this.saved();
    return { rows: saved?.page.items ?? [], nextCursor: saved?.page.nextCursor ?? null, position: saved?.position ?? this.position() };
  });
  readonly rows = computed(() => {
    const rows = this.table().rows;
    const pin = this.pinned();
    return pin && !this.listScope() && (this.allQueues() || pin.queue === this.active()) &&
      !rows.some(row => row.id === pin.id) ? [pin, ...rows] : rows;
  });
  readonly state = computed<DataState>(() => {
    const saved = this.saved();
    if (this.failed()) return { kind: 'error', heading: 'Could not load alerts', keepContent: !!saved,
      detail: saved ? 'Showing the last loaded page.' : undefined };
    if (this.loading()) return saved ? {
      kind: 'stale', heading: 'Refreshing alerts',
      observedAt: { dateTime: saved.at.toISOString(), label: defaultWorkspaceFormat.dateTime(saved.at) }
    } : { kind: 'loading', heading: 'Loading alerts' };
    return this.table().rows.length || this.table().position.cursor ? { kind: 'ready' } : {
      kind: 'empty', heading: 'No alerts in this queue', detail: 'Check another queue or change the filters.'
    };
  });
  readonly endpoint = computed(() => {
    const workspace = this.workspace.current();
    return workspace && this.base + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug) + '/alerts';
  });

  constructor() {
    effect(onCleanup => {
      const timer = setInterval(() => this.refresh(), 60_000);
      onCleanup(() => clearInterval(timer));
    });
    effect(() => {
      this.endpoint();
      this.canReview.set(false);
      this.select(null);
    });
    effect(() => {
      this.filters.params();
      if (!this.navigation) {
        this.localPosition.update(position => ({ ...position, cursor: null, previousCursors: [] }));
        this.select(null);
      }
    });
    effect(() => {
      this.endpoint();
      this.filters.params();
      this.active();
      this.allQueues();
      this.group();
      this.status();
      this.saved.set(null);
    });
    effect(onCleanup => {
      const url = this.endpoint();
      const position = this.position();
      const queue = this.active();
      const allQueues = this.allQueues();
      const params: Record<string, string | number> = { ...this.filters.params(), limit: 25 };
      if (this.sortOrder() === 'oldest') params['sort'] = 'oldest';
      if (!allQueues) params['queue'] = queue;
      if (this.group()) params['group'] = this.group();
      if (this.status()) params['status'] = this.status();
      if (position.cursor) params['cursor'] = position.cursor;
      this.revision();
      this.failed.set(false);
      this.loading.set(true);
      if (!url) return;
      const request = this.http.get<Page>(url, { params }).subscribe({
        next: page => {
          this.canReview.set(page.canReview);
          if (!page.canReview && !allQueues && queue !== 'unit') {
            if (this.navigation) this.navigation.update({ queue: 'unit', cursor: null, alertTrail: null }, { replaceUrl: true });
            else this.switchTo('unit');
            return;
          }
          this.saved.set({ page, position, at: new Date() });
          this.loading.set(false);
        },
        error: (error: HttpErrorResponse) => {
          this.handleAccess(error);
          this.failed.set(true);
          this.loading.set(false);
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    // Resolve a map point even when the alert is beyond this page.
    effect(onCleanup => {
      const point = this.selected();
      const url = this.endpoint();
      const params = this.filters.params();
      this.revision();
      if (this.linked() || !point || !url) return;
      if (point.observed_at * 1000 < this.now() - this.hours() * 3600_000) { this.select(null); return; }
      const request = this.http.get<Page>(url, { params: { ...params, detection: String(point.id) } }).subscribe({
        next: result => {
          const row = result.items[0];
          this.pinned.set(row ?? null);
          if (row && !untracked(this.allQueues) && row.queue !== untracked(this.active)) this.switchTo(row.queue);
        },
        error: (error: HttpErrorResponse) => this.handleAccess(error)
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  refresh(): void {
    this.now.set(Date.now());
    this.revision.update(value => value + 1);
  }

  select(point: Detection | null): void {
    this.linked.set(null);
    this.pinned.set(null);
    this.selected.set(point);
  }

  selectLinked(row: AlertRow): void {
    this.linked.set(row);
    this.pinned.set(row);
    this.selected.set({ ...row, id: row.detection_id });
  }

  switchTo(queue: Queue | 'all'): void {
    if (queue === (this.allQueues() ? 'all' : this.active())) return;
    if (this.navigation) {
      this.navigation.update({ queue, cursor: null, alertTrail: null });
    } else if (queue !== 'all') {
      this.localQueue.set(queue);
      this.localPosition.update(position => ({ ...position, cursor: null, previousCursors: [] }));
    }
  }

  clearListScope(): void {
    this.navigation?.update({ group: null, status: null, cursor: null, alertTrail: null, alert: null, detection: null });
  }

  changePage(position: TablePosition): void {
    if (this.navigation) {
      this.navigation.update({
        queue: this.allQueues() ? 'all' : this.active(),
        cursor: position.cursor,
        alertTrail: position.previousCursors.map(cursor => cursor ?? '-').join('|') || null,
        alertSort: position.sort?.direction === 'ascending' ? 'oldest' : null
      });
    } else {
      this.localPosition.set(position);
    }
  }

  next(): void {
    const { position, nextCursor } = this.table();
    if (!this.loading() && nextCursor) {
      this.changePage({ ...position, cursor: nextCursor, previousCursors: [...position.previousCursors, position.cursor] });
    }
  }

  previous(): void {
    const { position } = this.table();
    if (!this.loading() && position.previousCursors.length) {
      this.changePage({ ...position, cursor: position.previousCursors.at(-1) ?? null,
        previousCursors: position.previousCursors.slice(0, -1) });
    }
  }

  private handleAccess(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    else if (error.status === 403) this.workspace.deny();
  }
}
