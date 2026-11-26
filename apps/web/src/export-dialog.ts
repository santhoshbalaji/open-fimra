import { translate } from './language';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmLabelImports } from '@spartan-ng/helm/label';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmButton } from '@spartan-ng/helm/button';
import { Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, effect, inject, input, linkedSignal, output, signal, viewChild } from '@angular/core';
import { AuthService } from './auth';
import { ForestClass } from './detections';
import { ExportJob } from './export-dialog-job';
import { ExportFormat, ExportJobs, ExportKind, ExportRequest, allClasses, decisions, exportFormats, exportReview, exportStates } from './export-dialog-jobs';
import { workspaceFormat } from './workspace-format';
import { StatusBadge } from './ui/status-badge';

@Component({
  selector: 'fimra-export-dialog',
  imports: [ExportJob, HlmButton, HlmFieldImports, HlmInput, HlmLabelImports, HlmRadioGroupImports, StatusBadge],
  templateUrl: './export-dialog.html'
})
export class ExportDialog {
  protected readonly msg = translate;
  /** The workspace's API address, ending in /api/v1/workspaces/{slug}. */
  readonly endpoint = input.required<string>();
  readonly hours = input.required<number>();
  /** One forest class, or '' for all, as in the shared filters. */
  readonly forestClass = input.required<ForestClass | ''>();
  /** A unit path prefix, or '' for all units. */
  readonly unitPath = input.required<string>();
  readonly unitLabel = input('');
  readonly queue = input.required<string>();
  readonly queueLabel = input.required<string>();
  readonly group = input('');
  readonly step = input('');
  readonly allObservationDates = input(false);
  readonly initialKind = input<ExportKind>('alerts');
  readonly form = input('');
  readonly formLabel = input('');
  readonly decision = input('');
  readonly closed = output<void>();

  protected readonly jobs = inject(ExportJobs);
  protected readonly formatLabels = exportFormats;
  protected readonly states = exportStates;
  protected readonly gisFormats = (Object.keys(exportFormats) as ExportFormat[])
    .filter(key => key !== 'xlsx' && key !== 'csv_lonlat');
  protected readonly decisions = decisions;
  protected readonly kind = linkedSignal(() => this.initialKind());
  protected readonly format = linkedSignal<ExportFormat>(() => this.kind() === 'detections' ? 'gpkg' : 'xlsx');
  protected readonly exportDecision = signal('');
  protected readonly onlyQueue = signal(true);
  protected readonly id = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly job = computed(() => this.jobs.items().find(job => job.id === this.id()));
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly lifetime = inject(DestroyRef);
  private readonly injector = inject(Injector);
  private readonly jobHeading = viewChild<ElementRef<HTMLElement>>('jobHeading');

  protected readonly request = computed<ExportRequest>(() => {
    const kind = this.kind();
    const forestClass = this.forestClass();
    return {
      kind, format: this.format(), hours: kind === 'alerts' && this.allObservationDates() ? undefined : this.hours(),
      group: kind === 'alerts' ? this.group() || undefined : undefined,
      step: kind === 'alerts' ? this.step() || undefined : undefined,
      forestClasses: forestClass ? [forestClass] : allClasses,
      unitPath: this.unitPath() || undefined,
      queue: kind === 'alerts' && this.onlyQueue() ? this.queue() || undefined : undefined,
      form: kind === 'submissions' ? this.form() || undefined : undefined,
      decision: kind === 'submissions' ? this.exportDecision() || undefined : undefined
    };
  });
  protected readonly summary = computed(() => exportReview(this.request(), this.queueLabel(), this.formLabel(), this.unitLabel()));

  private readonly countUrl = computed(() => {
    const { kind, hours, unitPath, form, group, step } = this.request();
    const params = new URLSearchParams();
    if (hours != null) params.set('hours', String(hours));
    if (group) params.set('group', group);
    if (step) params.set('status', step);
    const forestClass = this.forestClass();
    if (forestClass) params.set('class', forestClass);
    if (unitPath) params.set('unitPath', unitPath);
    if (kind !== 'detections') params.set('limit', '1');
    if (form) params.set('form', form);
    return this.endpoint() + '/' + (kind === 'detections' ? 'summary' : kind) + '?' + params;
  });
  private readonly countResult = signal<{ url: string; counts: Record<string, number> | null } | null>(null);
  private readonly countAttempt = signal(0);
  protected readonly count = computed(() => {
    const result = this.countResult();
    if (!result || result.url !== this.countUrl() || !result.counts) return null;
    const { kind, decision, queue } = this.request();
    const key = kind === 'submissions' ? decision : kind === 'alerts' ? queue : this.forestClass();
    return key ? result.counts[key] ?? 0 : Object.values(result.counts).reduce((total, count) => total + count, 0);
  });
  protected readonly countLabel = computed(() => {
    const count = this.count();
    if (count !== null) return workspaceFormat.number(count);
    return this.countFailed() ? translate('Could not count matching rows.') : translate('Counting…');
  });
  protected readonly countFailed = computed(() => {
    const result = this.countResult();
    return result?.url === this.countUrl() && result.counts === null;
  });

  constructor() {
    effect(onCleanup => {
      const url = this.countUrl();
      this.countAttempt();
      this.countResult.set(null);
      // These endpoints count all matching rows before pagination. Alerts
      // return counts by queue; submissions return counts by decision.
      const request = this.http.get<{ counts?: Record<string, number>; detections?: Record<string, number> }>(url).subscribe({
        next: data => this.countResult.set({ url, counts: data.counts ?? data.detections ?? {} }),
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.countResult.set({ url, counts: null });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  protected retryCount(): void {
    this.countAttempt.update(attempt => attempt + 1);
  }

  protected async prepare(): Promise<void> {
    if (this.busy() || this.count() === null) return;
    this.error.set('');
    this.busy.set(true);
    try {
      const id = await this.jobs.prepare(this.endpoint(), this.request());
      if (id && !this.lifetime.destroyed) this.showJob(id);
    } catch (error) {
      this.error.set((error as Error).message);
    } finally {
      this.busy.set(false);
    }
  }

  protected showJob(id: string): void {
    this.id.set(id);
    afterNextRender(() => this.jobHeading()?.nativeElement.focus(), { injector: this.injector });
  }
}
