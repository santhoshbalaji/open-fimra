import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ViewEncapsulation, computed, effect, inject, input, output, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import type { AlertRow } from '../alert-queue';
import { AuthService } from '../auth';
import { DashboardFilters } from '../dashboard-filters';
import { classes, relativeAge } from '../detections';
import { EmptyState } from '../ui/empty-state';
import { StatusBadge } from '../ui/status-badge';
import { WorkspaceService } from '../workspace';
import { decisionLabels, type Decision, type SubmissionRow, type WorkspaceForm } from './form-api';
import { textIn } from './form-text';
import { SubmissionView } from './submission-view';

interface Page { items: SubmissionRow[]; counts: Partial<Record<Decision, number>>; nextCursor?: string }
export type DecisionFilter = Decision | '';
/** The list's own filters, for an export of the same reports. */
export interface ReportFilters { form: string; decision: DecisionFilter; formLabel: string }

const tabs: { value: DecisionFilter; label: string }[] = [
  { value: 'pending', label: 'Waiting' }, { value: 'approved', label: 'Approved' },
  { value: 'returned', label: 'Returned' }, { value: '', label: 'All' }
];

/**
 * Field reports beside the alert queue, with the same time window, forest
 * class and unit path, a form and a decision. Opening a report shows it with
 * its form version and, for supervising officers, approval.
 */
@Component({
  selector: 'fimra-submissions',
  imports: [HlmButtonImports, HlmFieldImports, HlmSelectImports, HlmSkeletonImports, HlmTableImports, EmptyState, StatusBadge, SubmissionView],
  styleUrl: './forms.css',
  encapsulation: ViewEncapsulation.None,
  template: `
    @if (selected(); as row) {
      <section aria-labelledby="report-title">
        <div class="queue-heading">
          <h3 id="report-title" tabindex="-1">{{ title(row) }}</h3>
          <button hlmBtn variant="ghost" (click)="close()">All reports</button>
        </div>
        <p class="incident-summary">{{ place(row) }} · {{ classes[row.forest_class] }}
          <button hlmBtn variant="link" size="sm" (click)="showAlert(row)">Show alert #{{ row.alert_id }}</button></p>
        <fimra-submission-view [workspaceUrl]="endpoint()" [submissionId]="row.id" (decided)="decided()" />
      </section>
    } @else {
      <div class="queue-tabs" role="group" aria-label="Decisions">
        @for (tab of tabs; track tab.value) {
          <button hlmBtn variant="ghost" [attr.aria-pressed]="decision() === tab.value" (click)="setDecision(tab.value)">{{ tab.label }}
            @if (tab.value && page(); as p) { <span>{{ p.counts[tab.value] || 0 }}</span> }
          </button>
        }
      </div>
      @if (forms().length) {
        <hlm-field class="report-form-filter"><label hlmFieldLabel for="report-form">Form</label>
          <hlm-select [value]="form() || 'all'" (valueChange)="setForm($event)">
            <hlm-select-trigger buttonId="report-form" size="sm">{{ formLabel() }}</hlm-select-trigger>
            <hlm-select-content *hlmSelectPortal>
              <hlm-select-item value="all">All forms</hlm-select-item>
              @for (f of forms(); track f.form) { <hlm-select-item [value]="f.form">{{ formTitle(f) }}</hlm-select-item> }
            </hlm-select-content>
          </hlm-select>
        </hlm-field>
      }
      @if (message()) { <p role="status">{{ message() }}</p> }
      @if (page(); as p) {
        @if (p.items.length) {
          <table hlmTable class="alert-table">
            <caption class="sr-only">Field reports</caption>
            <thead hlmTHead><tr hlmTr><th hlmTh scope="col">Report and place</th><th hlmTh scope="col">Decision</th></tr></thead>
            <tbody hlmTBody>
              @for (row of p.items; track row.id) {
                <tr hlmTr>
                  <td hlmTd><button class="row-open" (click)="open(row)"
                      [attr.aria-label]="title(row) + ', ' + place(row) + ', received ' + received(row)">{{ title(row) }}</button>
                    <p>{{ place(row) }}</p>
                    <small>{{ row.submitted_by }} · <time [attr.datetime]="row.received_time" [title]="receivedAt(row)">{{ received(row) }}</time></small>
                    <small>Alert #{{ row.alert_id }} · {{ row.alert_step_label }}</small>
                  </td>
                  <td hlmTd><div class="step-meta">
                    <fimra-status-badge [icon]="row.decision === 'approved' ? 'complete' : row.decision === 'returned' ? 'overdue' : 'step'" [label]="labels[row.decision]" />
                  </div></td>
                </tr>
              }
            </tbody>
          </table>
        } @else if (!message()) {
          <fimra-empty-state heading="No reports here" detail="Check another decision or change the filters." />
        }
        @if (cursor()) { <button hlmBtn variant="outline" (click)="changePage('')">Newest reports</button> }
        @if (p.nextCursor; as next) { <button hlmBtn variant="outline" (click)="changePage(next)">Older reports</button> }
      } @else if (!message()) {
        <div role="status" aria-label="Loading reports" class="grid gap-2"><hlm-skeleton class="h-12" /><hlm-skeleton class="h-12" /></div>
      }
    }
  `
})
export class Submissions {
  /** The workspace's API address, ending in /api/v1/workspaces/{slug}. */
  readonly endpoint = input.required<string>();
  readonly now = input.required<number>();
  readonly openAlert = output<AlertRow>();
  /** The list's own filters, so an export can use them. */
  readonly filtersChanged = output<ReportFilters>();
  readonly tabs = tabs;
  readonly labels = decisionLabels;
  readonly classes = classes as Record<string, string>;
  readonly page = signal<Page | null>(null);
  readonly cursor = signal('');
  readonly decision = signal<DecisionFilter>('pending');
  readonly form = signal('');
  readonly forms = signal<WorkspaceForm[]>([]);
  readonly message = signal('');
  readonly selected = signal<SubmissionRow | null>(null);
  private readonly reload = signal(0);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly filters = inject(DashboardFilters);
  private readonly receivedFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  readonly formLabel = computed(() => {
    const found = this.forms().find(f => f.form === this.form());
    return found ? this.formTitle(found) : 'All forms';
  });

  constructor() {
    effect(onCleanup => {
      const request = this.http.get<WorkspaceForm[]>(this.endpoint() + '/forms').subscribe({
        next: forms => this.forms.set(forms.filter(f => f.versions.some(v => v.status === 'published'))),
        error: (error: HttpErrorResponse) => this.failed(error)
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      this.endpoint();
      this.filters.params();
      this.decision();
      this.form();
      this.cursor.set('');
    });
    effect(() => this.filtersChanged.emit({ form: this.form(), decision: this.decision(), formLabel: this.formLabel() }));
    effect(onCleanup => {
      const params: Record<string, string | number> = { ...this.filters.params() };
      if (this.decision()) params['decision'] = this.decision();
      if (this.form()) params['form'] = this.form();
      if (this.cursor()) params['cursor'] = this.cursor();
      this.now();
      this.reload();
      const request = this.http.get<Page>(this.endpoint() + '/submissions', { params }).subscribe({
        next: page => { this.page.set(page); this.message.set(''); },
        error: (error: HttpErrorResponse) => { this.failed(error); this.message.set('Reports could not load. Trying again in a minute.'); }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  formTitle(form: WorkspaceForm): string {
    const newest = [...form.versions].reverse().find(v => v.status === 'published');
    return textIn(newest?.title, 'en') || form.form;
  }

  title(row: SubmissionRow): string {
    const title = textIn(row.title, 'en') || row.form;
    return title === row.step_label ? title : title + ' · ' + row.step_label;
  }

  place(row: SubmissionRow): string {
    return [row.beat_name, row.range_name, row.division_name].filter(Boolean).join(' · ') || 'Unknown place';
  }

  received(row: SubmissionRow): string {
    return relativeAge(Date.parse(row.received_time) / 1000, this.now());
  }

  receivedAt(row: SubmissionRow): string {
    return this.receivedFormat.format(new Date(row.received_time));
  }

  setDecision(value: DecisionFilter): void {
    this.page.set(null);
    this.decision.set(value);
  }

  setForm(value: string | null | undefined): void {
    this.page.set(null);
    this.form.set(!value || value === 'all' ? '' : value);
  }

  changePage(cursor: string): void {
    this.page.update(page => page && { ...page, items: [], nextCursor: undefined });
    this.cursor.set(cursor);
  }

  open(row: SubmissionRow): void {
    this.selected.set(row);
    queueMicrotask(() => document.getElementById('report-title')?.focus());
  }

  close(): void {
    this.selected.set(null);
  }

  decided(): void {
    this.reload.update(n => n + 1);
  }

  async showAlert(row: SubmissionRow): Promise<void> {
    try {
      this.openAlert.emit(await firstValueFrom(this.http.get<AlertRow>(this.endpoint() + '/alerts/' + row.alert_id)));
    } catch (error) {
      this.failed(error as HttpErrorResponse);
      this.message.set('The alert could not open.');
    }
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    else if (error.status === 403) this.workspace.deny();
  }
}

