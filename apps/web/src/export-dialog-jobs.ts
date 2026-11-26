import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { EMPTY, Subject, catchError, exhaustMap, firstValueFrom, merge, of, tap, timer } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { classes, ForestClass } from './detections';
import { WorkspaceService } from './workspace';
import { defaultWorkspaceFormat } from './workspace-format';

export const exportFormats = {
  gpkg: 'GeoPackage (recommended)',
  shapefile: 'Shapefile (zipped)',
  kml: 'KML (KMZ)',
  geojson: 'GeoJSON (zipped)',
  flatgeobuf: 'FlatGeobuf (zipped)',
  csv_wkt: 'CSV with the point as well-known text (WKT, zipped)',
  csv_lonlat: 'CSV with longitude and latitude columns (zipped)',
  xlsx: 'Excel workbook'
};
export type ExportFormat = keyof typeof exportFormats;
export type ExportKind = 'alerts' | 'detections' | 'submissions';
export const datasets: Record<ExportKind, string> = { alerts: 'Alerts', detections: 'Detections on the map', submissions: 'Field reports' };
export const allClasses = Object.keys(classes) as ForestClass[];

interface ExportFilters {
  hours: number;
  forestClasses: readonly ForestClass[];
  unitPath?: string | null;
  queue?: string | null;
  step?: string | null;
  form?: string | null;
  decision?: string | null;
}
export interface ExportRequest extends ExportFilters {
  kind: ExportKind;
  format: ExportFormat;
}
export interface ExportStatus {
  id: string;
  kind: ExportKind;
  format: ExportFormat;
  filters: ExportFilters & { from: string; to: string };
  status: 'pending' | 'ready' | 'failed' | 'expired';
  rowCount: number;
  featureCount: number | null;
  fileBytes: number | null;
  fileName?: string;
  createdAt: string;
  expiresAt: string;
}

export function exportRequestFromJob(job: ExportStatus): ExportRequest {
  const { hours, forestClasses, unitPath, queue, step, form, decision } = job.filters;
  return { kind: job.kind, format: job.format, hours, forestClasses, unitPath,
    queue: queue ?? undefined, step: step ?? undefined, form: form ?? undefined, decision: decision ?? undefined };
}

const format = defaultWorkspaceFormat;
export const exportDate = (iso: string) => format.dateTime(new Date(iso));
export const exportPeriod = (hours: number) => 'Past ' + format.duration(hours * 3600);
const decisions: Record<string, string> = { pending: 'Waiting for a decision', approved: 'Approved', returned: 'Returned' };
const queues: Record<string, string> = { unit: 'Unit', edge_review: 'Edge review', triage: 'Triage' };

export function exportReview(request: ExportRequest, queueLabel?: string, formLabel?: string): [string, string][] {
  const rows: [string, string][] = [
    ['Dataset', datasets[request.kind]],
    [request.kind === 'submissions' ? 'Received' : 'Observed', exportPeriod(request.hours)],
    ['Forest class', request.forestClasses.length === allClasses.length ? 'All forest classes' :
      request.forestClasses.map(value => classes[value]).join(', ')],
    ['Unit filter', request.unitPath ? 'Units under ' + request.unitPath : 'All units you can see']
  ];
  if (request.kind === 'alerts' || request.queue) rows.push(['Queue', request.queue ?
    queueLabel || queues[request.queue] || request.queue : 'All queues you can see']);
  if (request.step) rows.push(['Workflow step', request.step]);
  if (request.kind === 'submissions') {
    rows.push(['Form', request.form ? (formLabel || request.form) + ' (one column per answer)' : 'All forms']);
    rows.push(['Decision', request.decision ? decisions[request.decision] ?? request.decision : 'All decisions']);
  }
  return rows;
}

export const exportStates = {
  pending: { icon: 'pending', label: 'Pending' },
  ready: { icon: 'complete', label: 'Ready' },
  failed: { icon: 'failed', label: 'Failed' },
  expired: { icon: 'delayed', label: 'Expired' }
} as const;

export function jobMessage(job: ExportStatus): string {
  const count = job.featureCount ?? job.rowCount;
  const rows = format.number(count) + (count === 1 ? ' row' : ' rows');
  if (job.status === 'pending') return 'Preparing ' + rows + '…';
  if (job.status === 'ready') return rows + '. Available until ' + exportDate(job.expiresAt) + '.';
  if (job.status === 'expired') return 'This file has expired.';
  return 'The file could not be prepared.';
}

interface ExportContext { subject: string; endpoint: string }

const listFailure = 'Could not check your exports. Try again.';
const expiry = (job: ExportStatus) => new Date(job.expiresAt).getTime();
const toExpired = (job: ExportStatus): ExportStatus => ({ ...job, status: 'expired', fileName: undefined });

/** The signed-in workspace's export history. The shell keeps polling when a dialog closes. */
@Injectable({ providedIn: 'root' })
export class ExportJobs {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  readonly endpoint = computed(() => {
    const slug = this.workspace.current()?.slug;
    return slug ? this.base + '/api/v1/workspaces/' + encodeURIComponent(slug) : '';
  });
  private readonly subject = computed(() => this.auth.user()?.profile.sub);
  private readonly context = computed<ExportContext | null>(() => {
    const subject = this.subject();
    const endpoint = this.endpoint();
    return subject && endpoint ? { subject, endpoint } : null;
  });
  private readonly refreshes = new Subject<void>();
  private readonly history = signal<{ context: ExportContext; items: ExportStatus[] } | null>(null);
  readonly items = computed(() => {
    const history = this.history();
    return history && history.context === this.context() ? history.items : [];
  });
  private readonly hasPending = computed(() => this.items().some(job => job.status === 'pending'));
  readonly loading = signal(true);
  readonly error = signal('');
  readonly announcement = signal('');
  readonly jurisdiction = computed(() => {
    const names = this.workspace.jurisdictions().map(([, label]) => label);
    return names.length ? 'Your assigned areas: ' + names.join(', ') : 'Only records your role can see';
  });

  constructor() {
    effect(onCleanup => {
      const context = this.context();
      this.history.set(null);
      this.loading.set(true);
      this.error.set('');
      this.announcement.set('');
      if (!context) return;
      const request = merge(of(undefined), this.refreshes).pipe(
        exhaustMap(() => {
          const before = untracked(this.items);
          return this.http.get<{ items: ExportStatus[] }>(context.endpoint + '/exports').pipe(
            tap(page => {
              if (context !== this.context()) return;
              const listed = page.items.map(job => this.expire(job));
              const finished = listed.filter(job => job.status !== 'pending' &&
                before.some(previous => previous.id === job.id && previous.status === 'pending'));
              if (finished.length) {
                this.announcement.set(finished.map(job => datasets[job.kind] + ' export. ' + exportStates[job.status].label + '.').join(' '));
              }
              // Keep a job accepted after this GET began, even if it is absent from this response.
              const created = this.items().filter(job =>
                !before.some(previous => previous.id === job.id) && !listed.some(entry => entry.id === job.id));
              this.history.set({ context, items: [...created, ...listed] });
              this.loading.set(false);
              this.error.set('');
            }),
            catchError((error: HttpErrorResponse) => {
              if (context !== this.context()) return EMPTY;
              this.loading.set(false);
              this.error.set(listFailure);
              if (error.status === 401) this.auth.sessionEnded();
              return EMPTY;
            })
          );
        })
      ).subscribe();
      onCleanup(() => request.unsubscribe());
    });

    effect(onCleanup => {
      if (!this.hasPending() || this.error()) return;
      const poll = timer(2000, 2000).subscribe(() => this.refresh());
      onCleanup(() => poll.unsubscribe());
    });

    effect(onCleanup => {
      const context = this.context();
      const due = Math.min(...this.items().filter(job => job.status !== 'expired').map(expiry));
      if (!context || !Number.isFinite(due)) return;
      const timeout = setTimeout(() => {
        if (context === this.context()) {
          this.history.set({ context, items: this.items().map(job => this.expire(job)) });
        }
      }, Math.min(Math.max(0, due - Date.now()), 2_147_483_647));
      onCleanup(() => clearTimeout(timeout));
    });
  }

  refresh(): void {
    this.refreshes.next();
  }

  async prepare(endpoint: string, body: ExportRequest): Promise<string | null> {
    const context = this.context();
    if (!context || context.endpoint !== endpoint) return null;
    this.announcement.set('');
    try {
      const job = this.expire(await firstValueFrom(this.http.post<ExportStatus>(endpoint + '/exports', body)));
      if (context !== this.context()) return null;
      this.history.set({ context, items: [job, ...this.items().filter(item => item.id !== job.id)] });
      this.announcement.set(datasets[job.kind] + ' export. ' + jobMessage(job));
      return job.id;
    } catch (error) {
      if (context !== this.context()) return null;
      const response = error as HttpErrorResponse;
      if (response.status === 401) { this.auth.sessionEnded(); return null; }
      let message = 'The export could not be prepared. Try again.';
      if (response.status === 403) message = 'Your role cannot export from this workspace.';
      if (response.status === 422 && typeof response.error?.error === 'string') message = response.error.error;
      this.announcement.set(message);
      throw new Error(message);
    }
  }

  async download(job: ExportStatus): Promise<void> {
    const context = this.context();
    if (!context || job.status !== 'ready' || !job.fileName) return;
    try {
      const file = await firstValueFrom(this.http.get(
        context.endpoint + '/exports/' + encodeURIComponent(job.id) + '/file', { responseType: 'blob' }
      ));
      if (context !== this.context()) return;
      const url = URL.createObjectURL(file);
      const link = document.createElement('a');
      link.href = url;
      link.download = job.fileName;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (error) {
      if (context !== this.context()) return;
      const response = error as HttpErrorResponse;
      if (response.status === 401) { this.auth.sessionEnded(); return; }
      if (response.status === 410) {
        this.history.set({ context, items: this.items().map(item => item.id === job.id ? toExpired(item) : item) });
        return;
      }
      throw new Error(response.status === 403 ? 'Your role cannot download this export.' : 'Could not download the file. Try again.');
    }
  }

  private expire(job: ExportStatus): ExportStatus {
    return job.status !== 'expired' && expiry(job) <= Date.now() ? toExpired(job) : job;
  }
}
