import { translate } from './language';
import { Component, computed, inject, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { ExportJob } from './export-dialog-job';
import { ExportJobs, ExportStatus, datasets, exportDate, exportFormats, exportRequestFromJob, exportReview, exportStates } from './export-dialog-jobs';
import { Icon } from './ui/icon';
import { LiveStatus } from './ui/live-status';
import { DataState, StateRegion } from './ui/state-region';
import { StatusBadge } from './ui/status-badge';

@Component({
  selector: 'fimra-export-tray',
  imports: [ExportJob, HlmButton, HlmDialogImports, Icon, LiveStatus, StateRegion, StatusBadge],
  templateUrl: './export-dialog-tray.html'
})
export class ExportTray {
  protected readonly msg = translate;
  protected readonly jobs = inject(ExportJobs);
  protected readonly datasets = datasets;
  protected readonly formats = exportFormats;
  protected readonly states = exportStates;
  protected readonly date = exportDate;
  protected readonly selected = signal<string | null>(null);
  protected readonly state = computed<DataState>(() => {
    if (this.jobs.error()) return { kind: 'error', heading: translate('Could not check your exports'),
      detail: translate('Retry to get the latest status.'), keepContent: true };
    if (this.jobs.loading()) return { kind: 'loading', heading: translate('Loading exports') };
    return this.jobs.items().length ? { kind: 'ready' } :
      { kind: 'empty', heading: translate('No exports yet'), detail: translate('Prepare a file from the alert queue or field reports.') };
  });

  protected review(job: ExportStatus): [string, string][] {
    return exportReview(exportRequestFromJob(job));
  }
}
