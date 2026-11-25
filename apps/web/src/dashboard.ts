import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, effect, inject, signal } from '@angular/core';
import { KeyValuePipe } from '@angular/common';
import { timer, switchMap, catchError, EMPTY } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { classes, ForestClass } from './detections';
import { WorkspaceService } from './workspace';
import { ClassChart } from './class-chart';
import { DashboardFilters } from './dashboard-filters';
import { queueLabels } from './alert-queue';

const feedStates = {
  healthy: {icon: '✓', label: 'Healthy', color: 'good'},
  delayed: {icon: '◷', label: 'Delayed', color: 'warning'},
  quiet: {icon: '◌', label: 'Quiet', color: 'serious'},
  failing: {icon: '!', label: 'Failing', color: 'critical'}
};
interface Summary {
  detections: Partial<Record<ForestClass, number>>;
  today: Partial<Record<ForestClass, number>>;
  queues: Partial<Record<keyof typeof queueLabels, number>>;
  steps: Record<string, number>;
  active: number;
  overdue: number;
  pendingReports: number;
  responseSeconds: Record<string, {median: number | null; p90: number | null}>;
  feedHealth: {source: string; product: string; status: keyof typeof feedStates}[] | null;
}

@Component({selector: 'fimra-dashboard', imports: [KeyValuePipe, ClassChart], templateUrl: './dashboard.html'})
export class Dashboard {
  readonly filters = inject(DashboardFilters);
  readonly summary = signal<Summary | null>(null);
  readonly message = signal('');
  readonly classes = classes;
  readonly classKeys = Object.keys(classes) as ForestClass[];
  readonly queues = queueLabels;
  readonly queueKeys = Object.keys(queueLabels) as (keyof typeof queueLabels)[];
  readonly feedStates = feedStates;
  readonly milestones = {acknowledged: 'Acknowledged', reached_site: 'Reached site', controlled: 'Controlled'};
  private readonly http = inject(HttpClient);
  private readonly workspace = inject(WorkspaceService);
  private readonly auth = inject(AuthService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');

  constructor() {
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

  duration(seconds: number | null): string {
    if (seconds === null) return 'No data';
    return seconds < 60 ? Math.round(seconds) + ' s' : Math.round(seconds / 60) + ' min';
  }
}
