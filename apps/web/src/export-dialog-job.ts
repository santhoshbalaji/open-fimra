import { Component, computed, inject, input, output, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { ExportJobs, ExportStatus, exportPeriod, exportRequestFromJob, jobMessage } from './export-dialog-jobs';
import { LiveStatus } from './ui/live-status';

@Component({
  selector: 'fimra-export-job',
  imports: [HlmButton, LiveStatus],
  host: { class: 'grid gap-2' },
  template: `
    <fimra-live-status class="text-text-secondary" [message]="message()" />
    @if (job().status === 'ready' && job().fileName) {
      <button hlmBtn type="button" class="download justify-self-start" [busy]="busy()" (click)="download()">Download file</button>
    }
    @if (job().status === 'failed' || job().status === 'expired') {
      <p class="text-text-secondary">{{ retryPeriod() }} at the time you retry, with the same filters and your current access.</p>
      <p class="text-text-secondary wrap-anywhere">{{ jobs.jurisdiction() }}</p>
      <button hlmBtn type="button" variant="outline" class="justify-self-start" [busy]="busy()" (click)="retry()">Retry export</button>
    }
    @if (error()) { <p class="text-danger" role="alert">{{ error() }}</p> }
  `
})
export class ExportJob {
  readonly job = input.required<ExportStatus>();
  readonly prepared = output<string>();
  protected readonly jobs = inject(ExportJobs);
  protected readonly message = computed(() => jobMessage(this.job()));
  protected readonly retryPeriod = computed(() => exportPeriod(this.job().filters.hours));
  protected readonly busy = signal(false);
  protected readonly error = signal('');

  protected async retry(): Promise<void> {
    if (this.busy()) return;
    this.error.set('');
    this.busy.set(true);
    try {
      const id = await this.jobs.prepare(this.jobs.endpoint(), exportRequestFromJob(this.job()));
      if (id) this.prepared.emit(id);
    } catch (error) {
      this.error.set((error as Error).message);
    } finally {
      this.busy.set(false);
    }
  }

  protected async download(): Promise<void> {
    if (this.busy()) return;
    this.error.set('');
    this.busy.set(true);
    try {
      await this.jobs.download(this.job());
    } catch (error) {
      this.error.set((error as Error).message);
    } finally {
      this.busy.set(false);
    }
  }
}
