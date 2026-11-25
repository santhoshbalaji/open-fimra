import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ElementRef, effect, inject, input, signal, viewChild } from '@angular/core';
import { KeyValuePipe } from '@angular/common';
import { timer, switchMap, catchError, EMPTY } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { classes, ForestClass } from './detections';
import { WorkspaceService } from './workspace';
import { ClassChart } from './class-chart';
import { DashboardFilters } from './dashboard-filters';
import { queueLabels } from './alert-queue';
import { LucideTriangleAlert } from '@lucide/angular';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmSkeleton } from '@spartan-ng/helm/skeleton';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { EmptyState } from './ui/empty-state';
import { StatusBadge } from './ui/status-badge';

type BadgeIcon = 'step' | 'complete' | 'overdue';
// The badge always pairs its icon with the visible label.
const feedLabels = { healthy: 'Healthy', delayed: 'Delayed', quiet: 'Quiet', failing: 'Failing' };
const finishedSteps = new Set(['closed', 'false_alarm', 'verified']);
interface Summary {
  detections: Partial<Record<ForestClass, number>>;
  today: Partial<Record<ForestClass, number>>;
  queues: Partial<Record<keyof typeof queueLabels, number>>;
  steps: Record<string, number>;
  active: number;
  overdue: number;
  pendingReports: number;
  responseSeconds: Record<string, {median: number | null; p90: number | null}>;
  feedHealth: {source: string; product: string; status: 'healthy' | 'delayed' | 'quiet' | 'failing'}[] | null;
}

@Component({
  selector: 'fimra-dashboard',
  imports: [KeyValuePipe, ClassChart, LucideTriangleAlert, HlmCardImports, HlmSkeleton, HlmTableImports, EmptyState, StatusBadge],
  templateUrl: './dashboard.html'
})
export class Dashboard {
  readonly focusFeedHealth = input(false);
  private readonly feedHeading = viewChild<ElementRef<HTMLElement>>('feedHeading');
  readonly filters = inject(DashboardFilters);
  readonly summary = signal<Summary | null>(null);
  readonly message = signal('');
  readonly classes = classes;
  readonly classKeys = Object.keys(classes) as ForestClass[];
  readonly queues = queueLabels;
  readonly queueKeys = Object.keys(queueLabels) as (keyof typeof queueLabels)[];
  readonly feedLabels = feedLabels;
  readonly tiles = [
    {key: 'active', label: 'Active alerts'}, {key: 'overdue', label: 'Overdue alerts'},
    {key: 'pendingReports', label: 'Pending reports'}
  ] as const;
  readonly milestones = {acknowledged: 'Acknowledged', reached_site: 'Reached site', controlled: 'Controlled'};
  private readonly http = inject(HttpClient);
  private readonly workspace = inject(WorkspaceService);
  private readonly auth = inject(AuthService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');

  constructor() {
    effect(() => {
      if (this.focusFeedHealth()) this.feedHeading()?.nativeElement.focus();
    });
    effect(onCleanup => {
      const workspace = this.workspace.current();
      const params = this.filters.params();
      this.summary.set(null);
      this.message.set('');
      if (!workspace) return;
      const url = this.base + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug) + '/summary';
      const request = timer(0, 60_000).pipe(switchMap(() =>
        this.http.get<Summary>(url, {params}).pipe(catchError((error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          if (error.status === 403) this.workspace.deny();
          this.message.set('Dashboard could not refresh. Trying again in a minute.');
          return EMPTY;
        }))
      )).subscribe(data => { this.message.set(''); this.summary.set(data); });
      onCleanup(() => request.unsubscribe());
    });
  }

  detectionTotal(data: Summary): number {
    return this.classKeys.reduce((total, key) => total + (data.detections[key] ?? 0), 0);
  }

  stepRows(data: Summary): {key: string; label: string; count: number; icon: BadgeIcon}[] {
    return Object.entries(data.steps).sort(([a], [b]) => a.localeCompare(b)).map(([key, count]) => {
      const words = key.replaceAll('_', ' ');
      return {key, count, label: words.charAt(0).toUpperCase() + words.slice(1),
        icon: finishedSteps.has(key) ? 'complete' : 'step'};
    });
  }

  duration(seconds: number | null): string {
    if (seconds === null) return 'No data';
    return seconds < 60 ? Math.round(seconds) + ' s' : Math.round(seconds / 60) + ' min';
  }
}
