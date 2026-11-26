import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmLabelImports } from '@spartan-ng/helm/label';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmButton } from '@spartan-ng/helm/button';
import { Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, inject, input, linkedSignal, output, signal, viewChild } from '@angular/core';
import { ForestClass } from './detections';
import { ExportJob } from './export-dialog-job';
import { ExportFormat, ExportJobs, ExportKind, ExportRequest, allClasses, exportFormats, exportReview, exportStates } from './export-dialog-jobs';
import { StatusBadge } from './ui/status-badge';

@Component({
  selector: 'fimra-export-dialog',
  imports: [ExportJob, HlmButton, HlmFieldImports, HlmLabelImports, HlmRadioGroupImports, HlmSelectImports, StatusBadge],
  templateUrl: './export-dialog.html'
})
export class ExportDialog {
  /** The workspace's API address, ending in /api/v1/workspaces/{slug}. */
  readonly endpoint = input.required<string>();
  readonly hours = input.required<number>();
  /** One forest class, or '' for all, as in the shared filters. */
  readonly forestClass = input.required<ForestClass | ''>();
  /** A unit path prefix, or '' for all units. */
  readonly unitPath = input.required<string>();
  readonly queue = input.required<string>();
  readonly queueLabel = input.required<string>();
  readonly initialKind = input<ExportKind>('alerts');
  readonly form = input('');
  readonly formLabel = input('');
  readonly decision = input('');
  readonly closed = output<void>();

  protected readonly jobs = inject(ExportJobs);
  protected readonly formatLabels = exportFormats;
  protected readonly states = exportStates;
  protected readonly formats = Object.entries(exportFormats) as [ExportFormat, string][];
  protected readonly kind = linkedSignal(() => this.initialKind());
  protected readonly format = signal<ExportFormat>('gpkg');
  protected readonly onlyQueue = signal(true);
  protected readonly id = signal<string | null>(null);
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly job = computed(() => this.jobs.items().find(job => job.id === this.id()));
  private readonly lifetime = inject(DestroyRef);
  private readonly injector = inject(Injector);
  private readonly jobHeading = viewChild<ElementRef<HTMLElement>>('jobHeading');

  protected readonly request = computed<ExportRequest>(() => {
    const kind = this.kind();
    const forestClass = this.forestClass();
    return {
      kind, format: this.format(), hours: this.hours(),
      forestClasses: forestClass ? [forestClass] : allClasses,
      unitPath: this.unitPath() || undefined,
      queue: kind === 'alerts' && this.onlyQueue() ? this.queue() || undefined : undefined,
      form: kind === 'submissions' ? this.form() || undefined : undefined,
      decision: kind === 'submissions' ? this.decision() || undefined : undefined
    };
  });
  protected readonly summary = computed(() => exportReview(this.request(), this.queueLabel(), this.formLabel()));

  protected async prepare(): Promise<void> {
    if (this.busy()) return;
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
