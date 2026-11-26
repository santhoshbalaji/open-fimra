import { Location } from '@angular/common';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ElementRef, TemplateRef, afterRenderEffect, computed, effect, inject, input, signal, untracked, viewChild } from '@angular/core';
import { RouterLink } from '@angular/router';
import { HlmButton } from '@spartan-ng/helm/button';
import { EMPTY, expand, reduce } from 'rxjs';
import type { AlertRow } from './alert-queue';
import { AuthService } from './auth';
import { filterPeriodLabel } from './dashboard-filters';
import { classes, type ForestClass } from './detections';
import { ShellListScroll } from './shell-list-scroll';
import { ShellNavigation, recordId } from './shell-navigation';
import { DataTable, type TableColumn, type TablePage, type TablePosition } from './ui/data-table';
import { SidePanel } from './ui/side-panel';
import { StateRegion, type DataState } from './ui/state-region';
import { StatusBadge } from './ui/status-badge';
import { Timeline, type TimelineEvent } from './ui/timeline';
import { Workflow, type WorkflowEntry } from './workflow';
import { defaultWorkspaceFormat } from './workspace-format';

export interface IncidentRow {
  id: string;
  status: string;
  step_label: string;
  step_time: string;
  activity_at: string;
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
interface Page<Row> { items: Row[]; total: number; nextCursor?: string }
export interface IncidentFocus { id: string; points: [number, number][]; bounds: [number, number, number, number] }

const milestones = new Set(['acknowledged', 'reached_site', 'controlled', 'extinguished', 'closed']);

@Component({
  selector: 'fimra-incidents',
  imports: [DataTable, HlmButton, RouterLink, ShellListScroll, SidePanel, StateRegion, StatusBadge, Timeline, Workflow],
  templateUrl: './incidents.html',
  styleUrl: './incidents.css',
  host: { class: 'block h-full min-w-0' }
})
export class Incidents {
  readonly endpoint = input.required<string>();
  readonly now = input.required<number>();

  protected readonly navigation = inject(ShellNavigation);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly location = inject(Location);
  protected readonly incidentsUrl = computed(() => this.endpoint() + '/incidents');
  protected readonly selectedId = computed(() => recordId(this.navigation.query().get('incident')));
  protected readonly memberId = computed(() => recordId(this.navigation.query().get('member')));
  private readonly query = this.navigation.query;
  protected readonly cursor = computed(() => this.query().get('cursor'));
  private readonly sort = computed(() => this.query().get('sort') === 'activity-asc' ? 'activity-asc' : 'activity-desc');
  private readonly hours = computed(() => this.query().get('hours') ?? '168');
  private readonly forest = computed(() => this.query().get('class') as ForestClass | null);
  private readonly unit = computed(() => this.query().get('unitPath'));
  private readonly params = computed(() => ({
    hours: this.hours(), sort: this.sort(), limit: 20,
    ...(this.forest() && { class: this.forest()! }),
    ...(this.unit() && { unitPath: this.unit()! }),
    ...(this.cursor() && { cursor: this.cursor()! })
  }));
  protected readonly tableKey = computed(() => this.listKey() + '/table');
  protected readonly listKey = computed(() => this.incidentsUrl() + '?' + JSON.stringify(this.params()));
  protected readonly scope = computed(() => [
    filterPeriodLabel(Number(this.hours())), this.forest() && classes[this.forest()!]
  ].filter(Boolean).join(' · '));

  protected readonly format = defaultWorkspaceFormat;
  protected readonly nowDate = computed(() => new Date(this.now()));
  protected readonly classes = classes;
  protected readonly date = (seconds: number) => new Date(seconds * 1000);
  protected readonly activity = (row: IncidentRow) => new Date(row.activity_at);
  protected readonly places = (row: IncidentRow) => [...row.beat_names, ...row.division_names].join(' · ') || 'Unknown place';
  protected readonly rowKey = (row: IncidentRow) => row.id;

  private readonly placeCell = viewChild<TemplateRef<{ $implicit: IncidentRow }>>('placeCell');
  private readonly statusCell = viewChild<TemplateRef<{ $implicit: IncidentRow }>>('statusCell');
  private readonly activityCell = viewChild<TemplateRef<{ $implicit: IncidentRow }>>('activityCell');
  protected readonly columns = computed<readonly TableColumn<IncidentRow>[]>(() => [
    { id: 'place', label: 'Place', rowHeading: true, value: this.places, cell: this.placeCell() },
    { id: 'status', label: 'Status', value: row => row.step_label, cell: this.statusCell() },
    { id: 'activity', label: 'Latest activity', sortable: true,
      value: row => this.format.dateTime(this.activity(row)), cell: this.activityCell() },
    { id: 'id', label: 'Incident ID', value: row => '#' + row.id },
    { id: 'members', label: 'Member alerts', value: row => this.format.number(row.alert_count) },
    { id: 'age', label: 'Age since first seen', value: row => this.format.age(this.date(row.first_observed_at), this.nowDate()) }
  ]);

  private readonly result = signal<{ key: string; page: TablePage<IncidentRow>; total: number; observedAt: Date } | null>(null);
  protected readonly page = computed<TablePage<IncidentRow>>(() => this.result()?.page ?? {
    rows: [], nextCursor: null, position: { cursor: null, previousCursors: [] }
  });
  protected readonly total = computed(() => this.result()?.total ?? null);
  protected readonly listState = signal<DataState>({ kind: 'loading', heading: 'Loading incidents' });
  protected readonly detailState = signal<DataState>({ kind: 'loading', heading: 'Loading incident' });
  protected readonly memberState = signal<DataState>({ kind: 'loading', heading: 'Loading member alerts' });
  private readonly detail = signal<{ url: string; row: IncidentRow } | null>(null);
  protected readonly incident = computed(() => {
    const detail = this.detail();
    return detail?.url === this.incidentsUrl() && detail.row.id === this.selectedId() ? detail.row : null;
  });
  protected readonly members = signal<readonly AlertRow[]>([]);
  protected readonly member = computed(() => this.members().find(row => row.id === this.memberId()));
  protected readonly listRetry = signal(0);
  protected readonly detailRetry = signal(0);
  protected readonly memberRetry = signal(0);
  private readonly list = viewChild<ElementRef<HTMLElement>>('list');
  private readonly heading = viewChild<ElementRef<HTMLElement>>('listSummary');
  private readonly panel = viewChild(SidePanel);
  private origin: { id: string; endpoint: string } | null = null;

  constructor() {
    effect(cleanup => {
      const params = this.params();
      const key = this.listKey();
      this.listRetry();
      this.now();
      const prior = untracked(this.result);
      const sameList = prior?.key === key;
      const observed = sameList && prior.page.rows.length ? prior.observedAt : null;
      if (!sameList || observed) this.listState.set(observed ? {
        kind: 'stale', heading: 'Refreshing incidents',
        observedAt: { dateTime: observed.toISOString(), label: this.format.dateTime(observed) }
      } : { kind: 'loading', heading: 'Loading incidents' });
      if (!sameList) this.result.set(null);

      const cursor = this.cursor();
      const saved = this.routeState();
      const previousCursors = cursor
        ? (saved.incidentCursor === cursor ? saved.incidentPrevious ?? [null] : [null])
        : [];
      const position: TablePosition = {
        cursor, previousCursors, sort: { columnId: 'activity', direction: this.sort() === 'activity-asc' ? 'ascending' : 'descending' }
      };
      const request = this.http.get<Page<IncidentRow>>(this.incidentsUrl(), { params }).subscribe({
        next: data => {
          this.result.set({ key, total: data.total, observedAt: this.nowDate(),
            page: { rows: data.items, nextCursor: data.nextCursor ?? null, position } });
          this.listState.set(data.items.length ? { kind: 'ready' } : {
            kind: 'empty', heading: cursor ? 'No incidents on this page' : 'No incidents',
            detail: cursor ? 'Return to the first page to see the latest results.' : 'Try a wider time window or change the filters.'
          });
        },
        error: error => this.listState.set(this.failure(error, 'Could not load incidents', !!observed))
      });
      cleanup(() => request.unsubscribe());
    });

    effect(cleanup => {
      const id = this.selectedId();
      const url = this.incidentsUrl();
      if (!id) {
        this.detail.set(null);
        return;
      }
      this.detailRetry();
      this.now();
      if (!untracked(this.incident)) this.detailState.set({ kind: 'loading', heading: 'Loading incident' });
      const request = this.http.get<IncidentRow>(url + '/' + id).subscribe({
        next: row => { this.detail.set({ url, row }); this.detailState.set({ kind: 'ready' }); },
        error: error => { this.detail.set(null); this.detailState.set(this.failure(error, 'Could not load incident')); }
      });
      cleanup(() => request.unsubscribe());
    });

    effect(cleanup => {
      const incident = this.incident();
      const endpoint = this.endpoint();
      if (!incident) {
        this.members.set([]);
        return;
      }
      this.memberRetry();
      if (!untracked(this.members).length) this.memberState.set({ kind: 'loading', heading: 'Loading member alerts' });
      const page = (cursor: string) => this.http.get<Page<AlertRow>>(endpoint + '/alerts', {
        params: { incident: incident.id, limit: 200, ...(cursor && { cursor }) }
      });
      const request = page('').pipe(
        expand(data => data.nextCursor ? page(data.nextCursor) : EMPTY),
        reduce((rows, data) => rows.concat(data.items), [] as AlertRow[])
      ).subscribe({
        next: rows => {
          this.members.set(rows);
          this.memberState.set(rows.length ? { kind: 'ready' } : { kind: 'empty', heading: 'No member alerts' });
        },
        error: error => { this.members.set([]); this.memberState.set(this.failure(error, 'Could not load member alerts')); }
      });
      cleanup(() => request.unsubscribe());
    });

    afterRenderEffect(() => {
      const panel = this.panel();
      const id = this.selectedId();
      const endpoint = this.endpoint();
      if (!panel) return;
      if (id) {
        const start = this.routeState().incidentOrigin ?? id;
        this.origin = { id: start, endpoint };
        panel.show(() => this.findRow(), this.heading()?.nativeElement);
      } else {
        untracked(() => panel.close());
      }
    });
  }

  protected open(id: string): void {
    this.navigation.update({ incident: id, member: null }, {
      state: { ...this.routeState(), incidentOrigin: this.selectedId() ? this.origin?.id : id }
    });
  }

  protected closed(): void {
    if (this.selectedId()) {
      this.navigation.update({ incident: null, member: null }, { state: this.routeState() });
    }
  }

  protected selectMember(id: string): void {
    this.navigation.update({ member: this.memberId() === id ? null : id }, { state: this.routeState() });
  }

  protected firstPage(): void {
    this.changePage({ cursor: null, previousCursors: [], sort: this.page().position.sort });
  }

  protected changePage(position: TablePosition): void {
    this.navigation.update({
      cursor: position.cursor, sort: position.sort?.direction === 'ascending' ? 'activity-asc' : 'activity-desc',
      incident: null, member: null
    }, {
      state: { incidentCursor: position.cursor, incidentPrevious: position.previousCursors }
    });
  }

  protected retryList(): void { this.listRetry.update(n => n + 1); }
  protected retryDetail(): void { this.detailRetry.update(n => n + 1); }
  protected retryMembers(): void { this.memberRetry.update(n => n + 1); }

  protected changed(): void {
    this.listRetry.update(n => n + 1);
    this.detailRetry.update(n => n + 1);
  }

  protected timeline(entries: WorkflowEntry[] | null): TimelineEvent[] {
    return (entries ?? []).map((entry, index, all) => ({
      id: `${entry.at}/${index}`,
      title: entry.label,
      at: new Date(entry.at),
      milestone: index === 0 || index === all.length - 1 || milestones.has(entry.step),
      detail: [
        entry.actor || 'Recorded automatically',
        entry.latitude != null && `${entry.latitude}, ${entry.longitude}`,
        entry.note,
        entry.reason && `Location exception: ${entry.reason}`,
        entry.reportPending && 'Report pending',
        entry.alertsMoved ? `Moved ${this.format.number(entry.alertsMoved)} ${entry.alertsMoved === 1 ? 'alert' : 'alerts'}` : ''
      ].filter(Boolean).join(' · ')
    }));
  }

  private routeState() {
    return (this.location.getState() ?? {}) as {
      incidentCursor?: string; incidentPrevious?: readonly (string | null)[]; incidentOrigin?: string
    };
  }

  private findRow(): HTMLElement | null {
    const origin = this.origin;
    if (!origin || origin.endpoint !== this.endpoint()) return null;
    return this.list()?.nativeElement.querySelector<HTMLElement>(`[data-incident-id="${origin.id}"]`) ?? null;
  }

  private failure(error: HttpErrorResponse, heading: string, keepContent = false): DataState {
    if (error.status === 401) this.auth.sessionEnded();
    if (error.status === 403 || error.status === 404) return { kind: 'forbidden', heading: 'Access is unavailable' };
    return { kind: 'error', heading, keepContent };
  }
}
