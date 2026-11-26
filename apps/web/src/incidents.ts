import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import type { AlertRow } from './alert-queue';
import { AuthService } from './auth';
import { DashboardFilters } from './dashboard-filters';
import { classes, relativeAge } from './detections';
import { EmptyState } from './ui/empty-state';
import { StatusBadge } from './ui/status-badge';
import { Workflow } from './workflow';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';

export interface IncidentRow {
  id: string;
  status: string;
  step_label: string;
  step_time: string;
  overdue: boolean;
  terminal: boolean;
  merged_into: string | null;
  alert_count: number;
  first_observed_at: number;
  last_observed_at: number;
  beat_names: string[];
  division_names: string[];
  /** West, south, east and north of the member alerts you can see. */
  bounds: [number, number, number, number];
}
interface Page { items: IncidentRow[]; total: number; nextCursor?: string }
/** The member alerts to show on the map, or null to clear them. */
export interface IncidentFocus { id: string; points: [number, number][]; bounds: [number, number, number, number] }

/** Incidents beside the alert queue: alerts close in space and time, with their own workflow. */
@Component({
  selector: 'fimra-incidents',
  imports: [HlmButtonImports, HlmSkeletonImports, HlmTableImports, EmptyState, StatusBadge, Workflow],
  templateUrl: './incidents.html'
})
export class Incidents {
  /** The workspace's API address, such as …/api/v1/workspaces/demo. */
  readonly endpoint = input.required<string>();
  readonly now = input.required<number>();
  readonly openAlert = output<AlertRow>();
  readonly showOnMap = output<IncidentFocus | null>();
  readonly classes = classes;
  readonly relativeAge = relativeAge;
  readonly stepDate = Date.parse;
  readonly localTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  readonly page = signal<Page | null>(null);
  readonly cursor = signal('');
  readonly message = signal('');
  readonly selected = signal<IncidentRow | null>(null);
  readonly members = signal<AlertRow[] | null>(null);
  readonly detailMessage = signal('');
  private readonly reload = signal(0);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly filters = inject(DashboardFilters);
  readonly incidentsUrl = computed(() => this.endpoint() + '/incidents');
  private readonly navigation = inject(ShellNavigation, { optional: true });
  readonly selectedId = computed(() => this.navigation ? this.navigation.query().get('incident') : this.selected()?.id);

  constructor() {
    effect(() => {
      this.endpoint();
      this.filters.params();
      this.page.set(null);
      if (!this.navigation) {
        this.cursor.set('');
        this.selected.set(null);
      }
    });
    if (this.navigation) {
      const query = this.navigation.query;
      effect(() => this.cursor.set(query().get('cursor') ?? ''));
    }
    effect(onCleanup => {
      const params: Record<string, string | number> = { ...this.filters.params() };
      const cursor = this.cursor();
      if (cursor) params['cursor'] = cursor;
      this.now();
      this.reload();
      const request = this.http.get<Page>(this.incidentsUrl(), { params }).subscribe({
        next: page => { this.page.set(page); this.message.set(''); },
        error: (error: HttpErrorResponse) => { this.failed(error); this.message.set('Incidents could not load. Trying again in a minute.'); }
      });
      onCleanup(() => request.unsubscribe());
    });
    // Details and members are read again with the list, and after each step.
    effect(onCleanup => {
      const id = this.selectedId();
      this.now();
      this.reload();
      if (!id || untracked(this.selected)?.id !== id) {
        this.selected.set(null);
        this.members.set(null);
      }
      if (!id) return;
      this.detailMessage.set('');
      const detail = this.http.get<IncidentRow>(this.incidentsUrl() + '/' + id).subscribe({
        next: row => this.selected.set(row),
        error: (error: HttpErrorResponse) => { this.failed(error); this.detailMessage.set('This incident could not load.'); }
      });
      const members = this.http.get<{ items: AlertRow[] }>(this.endpoint() + '/alerts', { params: { incident: id, limit: 200 } }).subscribe({
        next: page => this.members.set(page.items),
        error: (error: HttpErrorResponse) => { this.failed(error); this.detailMessage.set('Member alerts could not load.'); }
      });
      onCleanup(() => { detail.unsubscribe(); members.unsubscribe(); });
    });
    effect(() => {
      const row = this.selected();
      const members = this.members();
      if (!row || !members?.length) { this.showOnMap.emit(null); return; }
      this.showOnMap.emit({ id: row.id, bounds: row.bounds, points: members.map(alert => [alert.longitude, alert.latitude]) });
    });
  }

  open(row: IncidentRow): void {
    if (this.navigation) {
      this.navigation.update({ incident: row.id });
      return;
    }
    this.detailMessage.set('');
    if (row.id !== this.selected()?.id) this.members.set(null);
    this.selected.set(row);
  }

  openById(id: string): void {
    this.open({ ...this.selected()!, id });
  }

  close(): void {
    if (this.navigation) this.navigation.update({ incident: null });
    else this.selected.set(null);
  }

  changed(): void {
    this.reload.update(n => n + 1);
  }

  changePage(cursor: string): void {
    if (this.navigation) {
      this.navigation.update({ cursor: cursor || null });
      return;
    }
    this.page.update(page => page && { ...page, items: [], nextCursor: undefined });
    this.cursor.set(cursor);
  }

  places(row: IncidentRow): string {
    return [...row.beat_names, ...row.division_names].join(' · ') || 'Unknown place';
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    else if (error.status === 403) this.workspace.deny();
  }
}
