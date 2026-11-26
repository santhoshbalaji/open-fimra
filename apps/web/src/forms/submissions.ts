import { BreakpointObserver } from '@angular/cdk/layout';
import { toSignal } from '@angular/core/rxjs-interop';
import { map } from 'rxjs';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ElementRef, TemplateRef, ViewEncapsulation, computed, effect, inject, input, output, signal, untracked, viewChild } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { AuthService } from '../auth';
import { DashboardFilters, ScopeFilters } from '../dashboard-filters';
import { FilterFields } from '../filter-fields';
import { ShellNavigation } from '../shell-navigation';
import { DataTable, type TableColumn, type TablePage, type TablePosition } from '../ui/data-table';
import { FilterBar, FilterControls } from '../ui/filter-bar';
import { SidePanel } from '../ui/side-panel';
import { type DataState } from '../ui/state-region';
import { workspacePhone } from '../ui/workspace-breakpoint';
import { StatusBadge } from '../ui/status-badge';
import { defaultWorkspaceFormat } from '../workspace-format';
import { WorkspaceService } from '../workspace';
import { type Decision, type SubmissionRow, type WorkspaceForm } from './form-api';
import { textIn } from './form-text';
import { SubmissionView } from './submission-view';

interface Page { items: SubmissionRow[]; nextCursor?: string }
export type DecisionFilter = Decision | '';
export interface ReportFilters { form: string; decision: DecisionFilter; formLabel: string }

const statuses = { pending: 'Submitted', approved: 'Approved', returned: 'Returned' };
const filterStatuses = { ...statuses, pending: 'Awaiting review' };
const firstPage: TablePosition = { cursor: null, previousCursors: [], sort: { columnId: 'received', direction: 'descending' } };
const emptyPage: TablePage<SubmissionRow> = { rows: [], nextCursor: null, position: firstPage };

@Component({
  selector: 'fimra-submissions',
  imports: [DataTable, FilterBar, FilterControls, FilterFields, HlmButton, HlmFieldImports, HlmSelectImports, SidePanel, StatusBadge, SubmissionView],
  styleUrl: './forms.css',
  encapsulation: ViewEncapsulation.None,
  template: `
    <div #queue tabindex="-1" class="report-queue" role="region" aria-label="Field report queue">
      <fimra-side-panel heading="Review field report" [fullHeight]="true" (closed)="closeReview()">
        @if (panel()?.open() && selectedId(); as id) {
          <fimra-submission-view [workspaceUrl]="endpoint()" [submissionId]="id" (decided)="reload()" />
        }
      </fimra-side-panel>
      <fimra-filter-bar [filters]="activeFilters()" (remove)="removeFilter($event)" (clearAll)="clearFilters()">
        <ng-template fimraFilterControls>
          <fimra-filter-fields [forReports]="true" />
          <hlm-field class="scope-filter-field"><label hlmFieldLabel for="report-decision">Report status</label>
            <hlm-select [value]="decision() || 'all'" (valueChange)="setFilter('decision', $event)">
              <hlm-select-trigger buttonId="report-decision">{{ decision() ? filterStatuses[decision() || 'pending'] : 'All statuses' }}</hlm-select-trigger>
              <hlm-select-content *hlmSelectPortal>
                <hlm-select-item value="all">All statuses</hlm-select-item>
                <hlm-select-item value="pending">{{ filterStatuses.pending }}</hlm-select-item>
                <hlm-select-item value="returned">Returned</hlm-select-item>
                <hlm-select-item value="approved">Approved</hlm-select-item>
              </hlm-select-content>
            </hlm-select>
          </hlm-field>
          <hlm-field class="scope-filter-field"><label hlmFieldLabel for="report-form">Form</label>
            <hlm-select [value]="form() || 'all'" (valueChange)="setFilter('form', $event === 'all' ? null : $event)">
              <hlm-select-trigger buttonId="report-form">{{ formLabel() }}</hlm-select-trigger>
              <hlm-select-content *hlmSelectPortal>
                <hlm-select-item value="all">All forms</hlm-select-item>
                @for (f of forms(); track f.form) { <hlm-select-item [value]="f.form">{{ formTitle(f) }}</hlm-select-item> }
              </hlm-select-content>
            </hlm-select>
          </hlm-field>
        </ng-template>
      </fimra-filter-bar>
      <ng-template #reportCell let-row>
        <button #report hlmBtn variant="link" class="report-open" type="button" (click)="open(row, report)">
          {{ title(row) }} <span class="sr-only">for alert #{{ row.alert_id }}, received {{ received(row) }}</span>
        </button>
        <p>{{ place(row) }}</p>
      </ng-template>
      <ng-template #statusCell let-row>
        <fimra-status-badge [icon]="row.decision === 'approved' ? 'complete' : row.decision === 'returned' ? 'overdue' : 'pending'"
          [label]="status(row)" />
      </ng-template>
      <ng-template #phoneActions let-row>
        <button #report hlmBtn variant="outline" type="button" (click)="open(row, report)">Open {{ title(row) }} <span class="sr-only">for alert #{{ row.alert_id }}</span></button>
      </ng-template>
      <fimra-data-table caption="Field reports" [columns]="columns()" [rowKey]="rowKey" [rowLabel]="rowLabel"
        [page]="page()" [state]="state()" [phoneCards]="true" [rowActions]="phoneCards() ? phoneActions : undefined"
        [hiddenColumns]="hiddenColumns()" (hiddenColumnsChange)="setColumns($event)"
        (pageRequest)="setPosition($event)" (retry)="reload()" />
      @if (state().kind === 'ready' && !page().rows.length) { <p>No reports on this page.</p> }
    </div>
  `
})
export class Submissions {
  readonly endpoint = input.required<string>();
  readonly now = input.required<number>();
  readonly filtersChanged = output<ReportFilters>();
  protected readonly filterStatuses = filterStatuses;
  protected readonly page = signal<TablePage<SubmissionRow>>(emptyPage);
  protected readonly state = signal<DataState>({ kind: 'loading', heading: 'Loading field reports' });
  protected readonly forms = signal<WorkspaceForm[]>([]);
  protected readonly panel = viewChild(SidePanel);
  private readonly queue = viewChild<ElementRef<HTMLElement>>('queue');
  private readonly navigation = inject(ShellNavigation);
  protected readonly selectedId = computed(() => this.navigation.query().get('report'));
  private readonly attempt = signal(0);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  protected readonly phoneCards = toSignal(
    inject(BreakpointObserver).observe(workspacePhone).pipe(map(result => result.matches)), { requireSync: true });
  private readonly filters = inject(DashboardFilters);
  private readonly scopeFilters = inject(ScopeFilters);
  private trigger: HTMLElement | null = null;
  protected readonly decision = computed<DecisionFilter>(() => {
    const value = this.navigation.query().get('decision');
    if (value === 'all') return '';
    return value === 'approved' || value === 'returned' ? value : 'pending';
  });
  protected readonly form = computed(() => this.navigation.query().get('form') ?? '');
  protected readonly formLabel = computed(() => {
    const found = this.forms().find(f => f.form === this.form());
    return found ? this.formTitle(found) : this.form() || 'All forms';
  });
  protected readonly hiddenColumns = computed<ReadonlySet<string>>(() =>
    new Set(this.navigation.query().get('reportColumns')?.split(',') ?? []));
  protected readonly activeFilters = computed(() => {
    const filters = [];
    if (this.decision()) filters.push({ id: 'decision', label: filterStatuses[this.decision() || 'pending'] });
    if (this.form()) filters.push({ id: 'form', label: this.formLabel() });
    return [...filters, ...this.scopeFilters.active()];
  });
  private readonly cursor = computed(() => this.navigation.query().get('cursor'));
  private readonly previous = computed(() => this.navigation.query().get('reportPages'));
  private readonly oldest = computed(() => this.navigation.query().get('reportSort') === 'oldest');

  private readonly reportCell = viewChild<TemplateRef<{ $implicit: SubmissionRow }>>('reportCell');
  private readonly statusCell = viewChild<TemplateRef<{ $implicit: SubmissionRow }>>('statusCell');
  protected readonly columns = computed<readonly TableColumn<SubmissionRow>[]>(() => [
    { id: 'report', label: 'Report and place', value: row => this.title(row), rowHeading: true, cell: this.reportCell() },
    { id: 'status', label: 'Status', value: row => this.status(row), cell: this.statusCell() },
    { id: 'sender', label: 'Sender', value: row => row.submitted_by },
    { id: 'received', label: 'Received', value: row => this.received(row), sortable: true },
    { id: 'alert', label: 'Alert', value: row => `#${row.alert_id} · ${row.alert_step_label}` }
  ]);
  protected readonly rowKey = (row: SubmissionRow) => row.id;
  protected readonly rowLabel = (row: SubmissionRow) => `${this.title(row)} · ${this.place(row)}`;

  constructor() {
    effect(onCleanup => {
      const request = this.http.get<WorkspaceForm[]>(this.endpoint() + '/forms').subscribe({
        next: forms => this.forms.set(forms.filter(f => f.versions.some(v => v.status === 'published'))),
        error: (error: HttpErrorResponse) => { if (error.status === 401) this.auth.sessionEnded(); }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => this.filtersChanged.emit({ form: this.form(), decision: this.decision(), formLabel: this.formLabel() }));
    effect(() => {
      const panel = this.panel();
      const fallback = this.queue()?.nativeElement;
      const id = this.selectedId();
      if (!panel || !fallback) return;
      untracked(() => {
        if (id && !panel.open()) panel.show(this.trigger ?? fallback, fallback);
        else if (!id && panel.open()) panel.close();
      });
    });

    let lastRequest = '';
    effect(onCleanup => {
      const endpoint = this.endpoint();
      const cursor = this.cursor();
      const oldest = this.oldest();
      const previous = this.previous();
      const position: TablePosition = {
        cursor,
        previousCursors: cursor ? (previous?.split('|') ?? ['']).map(page => page || null) : [],
        sort: { columnId: 'received', direction: oldest ? 'ascending' : 'descending' }
      };
      const params: Record<string, string | number> = { ...this.filters.params(), limit: 20 };
      if (this.decision()) params['decision'] = this.decision();
      if (this.form()) params['form'] = this.form();
      if (cursor) params['cursor'] = cursor;
      if (oldest) params['sort'] = 'oldest';
      this.now();
      this.attempt();
      const key = endpoint + JSON.stringify(params);
      if (key !== lastRequest || untracked(this.state).kind === 'error') {
        this.state.set({ kind: 'loading', heading: 'Loading field reports' });
      }
      lastRequest = key;
      const request = this.http.get<Page>(endpoint + '/submissions', { params }).subscribe({
        next: page => {
          this.page.set({ rows: page.items, nextCursor: page.nextCursor ?? null, position });
          this.state.set(page.items.length || cursor ? { kind: 'ready' } :
            { kind: 'empty', heading: 'No field reports match', detail: 'Change the filters to see more reports.' });
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.state.set(error.status === 403 ? { kind: 'forbidden' } :
            { kind: 'error', heading: 'Field reports could not load' });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  protected formTitle(form: WorkspaceForm): string {
    const newest = [...form.versions].reverse().find(v => v.status === 'published');
    return textIn(newest?.title, 'en') || form.form;
  }

  protected title(row: SubmissionRow): string {
    const title = textIn(row.title, 'en') || row.form;
    return title === row.step_label ? title : title + ' · ' + row.step_label;
  }

  protected place(row: SubmissionRow): string {
    return [row.beat_name, row.range_name, row.division_name].filter(Boolean).join(' · ') || 'Unknown place';
  }

  protected status(row: SubmissionRow): string {
    return statuses[row.decision];
  }

  protected received(row: SubmissionRow): string {
    return defaultWorkspaceFormat.dateTime(new Date(row.received_time));
  }

  protected setFilter(key: string, value: string | null | undefined): void {
    this.navigation.update({ [key]: value || null, cursor: null, reportPages: null, report: null });
  }

  protected removeFilter(key: string): void {
    this.setFilter(key, key === 'decision' ? 'all' : null);
  }

  protected clearFilters(): void {
    this.navigation.update({ decision: 'all', form: null, hours: null, class: null, unitPath: null,
      cursor: null, reportPages: null, report: null });
  }

  protected setPosition(position: TablePosition): void {
    this.navigation.update({
      cursor: position.cursor,
      reportPages: position.cursor && position.previousCursors.length > 1 ? position.previousCursors.join('|') : null,
      reportSort: position.sort?.direction === 'ascending' ? 'oldest' : null
    });
  }

  protected setColumns(columns: ReadonlySet<string>): void {
    this.navigation.update({ reportColumns: Array.from(columns).join(',') || null });
  }

  protected open(row: SubmissionRow, trigger: HTMLElement): void {
    this.trigger = trigger;
    this.navigation.update({ report: row.id });
  }

  protected closeReview(): void {
    this.trigger = null;
    if (this.selectedId()) this.navigation.update({ report: null }, { replaceUrl: true });
  }

  protected reload(): void {
    this.attempt.update(n => n + 1);
  }
}
