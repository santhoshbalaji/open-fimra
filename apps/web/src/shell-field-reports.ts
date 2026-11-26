import { Component, inject, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { DashboardFilters } from './dashboard-filters';
import { ExportDialog } from './export-dialog';
import { Submissions, type ReportFilters } from './forms/submissions';
import { ShellListScroll } from './shell-list-scroll';
import { WorkspaceList } from './shell-workspace-list';
import { Icon } from './ui/icon';

@Component({
  selector: 'fimra-field-report-screen',
  imports: [ExportDialog, HlmButton, HlmDialogImports, Icon, ShellListScroll, Submissions],
  template: `
    <div class="shell-list" fimraListScroll>
      <section class="shell-list-body" aria-label="Field reports">
        <div class="queue-heading">
          <hlm-dialog #dialog aria-modal="true" aria-describedby="export-summary">
            <button hlmBtn variant="outline" hlmDialogTrigger><svg fimraIcon="download" aria-hidden="true"></svg> Export</button>
            <hlm-dialog-content *hlmDialogPortal class="export-dialog" [showCloseButton]="false">
              <h2 hlmDialogTitle>Export</h2>
              @defer (on immediate) {
                <fimra-export-dialog [endpoint]="endpoint()" [hours]="filters.hours()" [forestClass]="filters.forestClass()"
                  [unitPath]="filters.unitPath()" queue="unit" queueLabel="Unit" initialKind="submissions" [form]="reportFilters().form"
                  [formLabel]="reportFilters().formLabel" [decision]="reportFilters().decision" (closed)="dialog.close()" />
              }
            </hlm-dialog-content>
          </hlm-dialog>
        </div>
        <fimra-submissions [endpoint]="endpoint()" [now]="now()" (openAlert)="openAlert($event.id)" (filtersChanged)="reportFilters.set($event)" />
      </section>
    </div>
  `,
  host: { class: 'block h-full' }
})
export class FieldReportScreen extends WorkspaceList {
  readonly filters = inject(DashboardFilters);
  readonly reportFilters = signal<ReportFilters>({ form: '', decision: 'pending', formLabel: '' });
}
