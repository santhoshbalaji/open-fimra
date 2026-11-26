import { translate } from './language';
import { Component, TemplateRef, ViewEncapsulation, inject, signal, viewChild } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { DashboardFilters, ScopeFilters } from './dashboard-filters';
import { ExportDialog } from './export-dialog';
import { Submissions, type ReportFilters } from './forms/submissions';
import { ShellListScroll } from './shell-list-scroll';
import { WorkspaceList } from './shell-workspace-list';
import { Icon } from './ui/icon';
import type { PageHeaderSlots } from './ui/page-header';

@Component({
  selector: 'fimra-field-report-screen',
  imports: [ExportDialog, HlmButton, HlmDialogImports, Icon, ShellListScroll, Submissions],
  template: `
    <ng-template #primary>
      <hlm-dialog #dialog aria-modal="true" aria-describedby="export-summary">
        <button hlmBtn variant="outline" hlmDialogTrigger><svg fimraIcon="download" aria-hidden="true"></svg> {{ msg('Export') }}</button>
        <hlm-dialog-content *hlmDialogPortal class="export-dialog" [showCloseButton]="false">
          <h2 hlmDialogTitle>{{ msg('Export') }}</h2>
          @defer (on immediate) {
            <fimra-export-dialog [endpoint]="endpoint()" [hours]="filters.hours()" [forestClass]="filters.forestClass()"
              [unitPath]="filters.unitPath()" [unitLabel]="scopeFilters.unitLabel()" queue="unit" queueLabel="Unit"
              initialKind="submissions" [form]="reportFilters().form" [formLabel]="reportFilters().formLabel"
              [decision]="reportFilters().decision" (closed)="dialog.close()" />
          }
        </hlm-dialog-content>
      </hlm-dialog>
    </ng-template>
    <div class="shell-list field-report-list" fimraListScroll>
      <section class="shell-list-body field-report-screen" [attr.aria-label]=" msg('Field reports') ">
        <fimra-submissions [endpoint]="endpoint()" [now]="now()" (filtersChanged)="reportFilters.set($event)" />
      </section>
    </div>
  `,
  encapsulation: ViewEncapsulation.None,
  styles: `
    .field-report-list .field-report-screen { display: grid; gap: 1rem; max-width: none; }
    .field-report-list .report-workspace { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1rem; }
    .field-report-list .report-panel { display: none; min-width: 0; min-height: 0; }

    @media (width >= 48rem) {
      .field-report-list .field-report-screen {
        height: 100%;
        grid-template-rows: minmax(0, 1fr);
      }
      .field-report-list .report-workspace { grid-template-rows: minmax(0, 1fr); }
      .field-report-list .report-workspace.has-report {
        grid-template-columns: minmax(0, 1fr) minmax(20rem, 42%);
      }
      .field-report-list .has-report .report-panel { display: block; }
      .field-report-list .report-queue { align-content: start; overflow: auto; }
    }
    @media (width >= 64rem) {
      .field-report-list .report-workspace.has-report { grid-template-columns: minmax(0, 1fr) minmax(32rem, 58%); }
      .field-report-list .has-report .filter-bar-controls .scope-filter-field { width: min(9.5rem, 100%); }
    }
  `,
  host: { class: 'block h-full' }
})
export class FieldReportScreen extends WorkspaceList implements PageHeaderSlots {
  protected readonly msg = translate;
  readonly pagePrimary = viewChild<TemplateRef<unknown>>('primary');
  readonly scopeFilters = inject(ScopeFilters);
  readonly filters = inject(DashboardFilters);
  readonly reportFilters = signal<ReportFilters>({ form: '', decision: 'pending', formLabel: '' });
}
