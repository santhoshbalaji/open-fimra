import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ElementRef, computed, effect, inject, input, signal, viewChild } from '@angular/core';
import { Router } from '@angular/router';
import { EMPTY, Subject, catchError, merge, switchMap, timer } from 'rxjs';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { alertGroupLabels, queueLabels, stepLabel } from './alert-queue';
import { AuthService } from './auth';
import { ClassChart } from './class-chart';
import { WEB_CONFIG } from './config';
import { DashboardFilters, ScopeFilters, filterPeriodLabel, setScopeFilters } from './dashboard-filters';
import { detectionCatalog, detectionSeries } from './dashboard.series';
import { classes, sourceLabel } from './detections';
import { FilterFields } from './filter-fields';
import { ShellNavigation } from './shell-navigation';
import { defaultWorkspaceFormat } from './workspace-format';
import { WorkspaceService } from './workspace';
import { FilterBar, FilterControls } from './ui/filter-bar';
import { PageHeader } from './ui/page-header';
import { StatTile } from './ui/stat-tile';
import { DataState, StateRegion } from './ui/state-region';
import { StatusBadge } from './ui/status-badge';

const feedLabels = { healthy: 'Healthy', delayed: 'Delayed', quiet: 'Quiet', failing: 'Failing' };

interface Feed {
  source: string;
  product: string;
  status: keyof typeof feedLabels;
  lastSuccessAt: string | null;
  latestObservationAt: string | null;
}

interface Summary {
  detections: Record<string, number>;
  today: Record<string, number>;
  queues: Partial<Record<keyof typeof queueLabels, number>>;
  steps: Record<string, number>;
  active: number;
  overdue: number;
  pendingReports: number;
  responseSeconds: Record<string, { median: number | null; p90: number | null }>;
  feedHealth: Feed[] | null;
  feedHealthUnavailable?: boolean;
}

const alertTiles = [
  { key: 'active', group: 'active' },
  { key: 'overdue', group: 'overdue' },
  { key: 'pendingReports', group: 'pending_reports' }
] as const;
const outcomeSteps = ['controlled', 'extinguished'];
const milestones = [
  { key: 'acknowledged', label: 'Acknowledged' },
  { key: 'reached_site', label: 'Reached site' },
  { key: 'controlled', label: 'Controlled' }
];

@Component({
  selector: 'fimra-dashboard',
  imports: [ClassChart, FilterBar, FilterControls, FilterFields, HlmCardImports, HlmTableImports, PageHeader, StateRegion, StatTile, StatusBadge],
  templateUrl: './dashboard.html'
})
export class Dashboard {
  readonly focusFeedHealth = input(false);
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly router = inject(Router);
  private readonly feedHeading = viewChild<ElementRef<HTMLElement>>('feedHeading');
  private readonly workspace = inject(WorkspaceService);
  private readonly auth = inject(AuthService);
  private readonly http = inject(HttpClient);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly reload = new Subject<void>();
  private readonly snapshot = signal<{ data: Summary; updated: Date } | null>(null);

  readonly filters = inject(DashboardFilters);
  readonly workspaceName = computed(() => this.workspace.current()?.name);
  readonly summary = computed(() => this.snapshot()?.data);
  readonly lastUpdate = computed(() => this.snapshot()?.updated);
  readonly state = signal<DataState>({ kind: 'loading', heading: 'Loading dashboard' });
  readonly sourceLabel = sourceLabel;
  readonly queueKeys = Object.keys(queueLabels) as (keyof typeof queueLabels)[];
  readonly queueLabels = queueLabels;
  readonly feedLabels = feedLabels;
  readonly alertTiles = alertTiles;
  readonly alertGroupLabels = alertGroupLabels;
  readonly outcomeSteps = outcomeSteps;
  readonly milestones = milestones;
  readonly stepLabel = stepLabel;
  readonly format = defaultWorkspaceFormat;

  private readonly scopeFilters = inject(ScopeFilters);
  readonly period = computed(() => filterPeriodLabel(this.filters.hours()));
  readonly jurisdiction = computed(() => {
    const name = this.workspace.current()?.name;
    const grants = this.workspace.jurisdictions().map(([, label]) => label);
    const scope = [name, ...grants.filter(label => label !== name)].filter(Boolean).join(' · ') || 'Your assigned area';
    if (!this.filters.unitPath()) return scope;
    return [name, this.scopeFilters.unitLabel()].filter(Boolean).join(' · ');
  });
  readonly forestScope = computed(() => {
    const forest = this.filters.forestClass();
    return forest ? classes[forest] : 'All forest classes';
  });
  readonly scope = computed(() => [this.jurisdiction(), this.forestScope(), this.period()].join(' · '));

  readonly activeFilters = this.scopeFilters.active;

  private readonly catalog = computed(() => detectionCatalog(this.summary()?.detections ?? {}, this.summary()?.today ?? {}));
  readonly chartRows = computed(() => detectionSeries(this.summary()?.detections ?? {}, this.filters.forestClass(), this.catalog()));
  readonly detectionRows = computed(() => {
    const data = this.summary();
    const selected = this.filters.forestClass();
    return Object.entries(this.catalog()).filter(([id]) => !selected || id === selected)
      .map(([id, label]) => ({ id, label, count: data?.detections[id] ?? 0, today: data?.today[id] ?? 0 }));
  });
  readonly detectionTotal = computed(() => this.detectionRows().reduce((total, row) => total + row.count, 0));
  readonly todayTotal = computed(() => this.detectionRows().reduce((total, row) => total + row.today, 0));
  readonly otherSteps = computed(() => Object.keys(this.summary()?.steps ?? {})
    .filter(step => !outcomeSteps.includes(step)).sort());
  readonly feedState = computed<DataState>(() => {
    const data = this.summary();
    if (data?.feedHealthUnavailable) {
      return { kind: 'error', heading: 'Feed health could not load', detail: 'Detection counts are still available.' };
    }
    if (!data?.feedHealth?.length) {
      return { kind: 'empty', heading: 'No feed data', detail: 'No live feeds have polled yet.' };
    }
    return { kind: 'ready' };
  });
  readonly coverage = computed(() => {
    const data = this.summary();
    if (!data) return '';
    if (data.feedHealthUnavailable) return 'Feed health could not load. This window may have missing observations.';
    const feeds = data.feedHealth;
    if (!feeds) return 'Ask a workspace administrator to check feed health.';
    if (!feeds.length) return 'No live feeds have polled yet. This window may have missing observations.';
    if (feeds.some(feed => feed.status !== 'healthy' || !feed.latestObservationAt || !feed.lastSuccessAt)) {
      return 'Some feeds are late, quiet, or missing data. This window may have missing observations.';
    }
    return this.detectionTotal() === 0 ? 'All monitored feeds are currently reporting.' : '';
  });

  constructor() {
    effect(() => {
      if (this.focusFeedHealth() || this.navigation?.query().get('view') === 'feed-health') this.feedHeading()?.nativeElement.focus();
    });
    effect(onCleanup => {
      const workspace = this.workspace.current();
      const params = this.filters.params();
      this.snapshot.set(null);
      this.state.set({ kind: 'loading', heading: 'Loading dashboard' });
      if (!workspace) return;

      const url = this.base + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug) + '/summary';
      const request = merge(timer(0, 60_000), this.reload).pipe(switchMap(() =>
        this.http.get<Summary>(url, { params }).pipe(catchError((error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          if (error.status === 403) {
            this.snapshot.set(null);
            this.state.set({ kind: 'forbidden', heading: 'You do not have access to this dashboard' });
            this.workspace.deny();
          } else {
            this.state.set({ kind: 'error', heading: 'Dashboard could not refresh',
              keepContent: !!this.snapshot(), detail: this.snapshot() ? 'Showing the last successful update.' : undefined });
          }
          return EMPTY;
        }))
      )).subscribe(data => {
        this.snapshot.set({ data, updated: new Date() });
        this.state.set({ kind: 'ready' });
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  retry(): void {
    this.reload.next();
  }

  clearFilters(key?: string): void {
    setScopeFilters(this.filters, this.navigation, key ? { [key]: null } : { hours: null, class: null, unitPath: null });
  }

  alertRoute(extra: { queue?: string; group?: string; status?: string }) {
    return this.router.createUrlTree(['/monitor/alerts'], {
      queryParams: { workspace: this.workspace.current()?.slug, ...this.filters.params(), queue: 'all', ...extra }
    });
  }

  feedDate(timestamp: string): string {
    return this.format.dateTime(new Date(timestamp));
  }

  duration(seconds: number | null | undefined): string {
    return seconds == null ? 'No data' : this.format.duration(seconds);
  }
}
