import { language, translate } from '../language';
import { BreakpointObserver } from '@angular/cdk/layout';
import { toSignal } from '@angular/core/rxjs-interop';
import { EMPTY, expand, filter, map, take } from 'rxjs';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ElementRef, TemplateRef, ViewEncapsulation, computed, effect, inject, input, output, signal, untracked, viewChild } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { AuthService } from '../auth';
import { DashboardFilters, ScopeFilters } from '../dashboard-filters';
import { FilterFields } from '../filter-fields';
import { ShellNavigation, submissionId } from '../shell-navigation';
import { DataTable, type TableColumn, type TablePage, type TablePosition } from '../ui/data-table';
import { FilterBar, FilterControls } from '../ui/filter-bar';
import { SidePanel } from '../ui/side-panel';
import { type DataState } from '../ui/state-region';
import { workspacePhone } from '../ui/workspace-breakpoint';
import { StatusBadge } from '../ui/status-badge';
import { workspaceFormat } from '../workspace-format';
import { WorkspaceService } from '../workspace';
import { decisionLabels as statuses, type Decision, type Submission, type SubmissionRow, type WorkspaceForm } from './form-api';
import { textIn } from './form-text';
import { SubmissionView } from './submission-view';

interface Page { items: SubmissionRow[]; nextCursor?: string }
export type DecisionFilter = Decision | '';
export interface ReportFilters { form: string; decision: DecisionFilter; formLabel: string }

const firstPage: TablePosition = { cursor: null, previousCursors: [], sort: { columnId: 'received', direction: 'descending' } };
const emptyPage: TablePage<SubmissionRow> = { rows: [], nextCursor: null, position: firstPage };

@Component({
  selector: 'fimra-submissions',
  imports: [DataTable, FilterBar, FilterControls, FilterFields, HlmButton, HlmFieldImports, HlmSelectImports, SidePanel, StatusBadge, SubmissionView],
  styleUrl: './forms.css',
  host: { class: 'report-workspace', '[class.has-report]': 'panel()?.open()' },
  encapsulation: ViewEncapsulation.None,
  template: `
    <div #queue tabindex="-1" class="report-queue" role="region" [attr.aria-label]=" msg('Field report queue') ">
      <fimra-filter-bar [filters]="activeFilters()" (remove)="removeFilter($event)" (clearAll)="clearFilters()">
        <ng-template fimraFilterControls>
          <fimra-filter-fields [forReports]="true" />
          <hlm-field class="scope-filter-field"><label hlmFieldLabel for="report-decision">{{ msg('Report status') }}</label>
            <hlm-select [value]="decision() || 'all'" (valueChange)="setFilter('decision', $event)">
              <hlm-select-trigger buttonId="report-decision">{{ decision() ? msg(statuses[decision() || 'pending']) : msg('All statuses') }}</hlm-select-trigger>
              <hlm-select-content *hlmSelectPortal>
                <hlm-select-item value="all">{{ msg('All statuses') }}</hlm-select-item>
                <hlm-select-item value="pending">{{ statuses.pending }}</hlm-select-item>
                <hlm-select-item value="returned">{{ msg('Returned') }}</hlm-select-item>
                <hlm-select-item value="approved">{{ msg('Approved') }}</hlm-select-item>
              </hlm-select-content>
            </hlm-select>
          </hlm-field>
          <hlm-field class="scope-filter-field"><label hlmFieldLabel for="report-form">{{ msg('Form') }}</label>
            <hlm-select [value]="form() || 'all'" (valueChange)="setFilter('form', $event === 'all' ? null : $event)">
              <hlm-select-trigger buttonId="report-form">{{ formLabel() }}</hlm-select-trigger>
              <hlm-select-content *hlmSelectPortal>
                <hlm-select-item value="all">{{ msg('All forms') }}</hlm-select-item>
                @for (f of forms(); track f.form) { <hlm-select-item [value]="f.form">{{ formTitle(f) }}</hlm-select-item> }
              </hlm-select-content>
            </hlm-select>
          </hlm-field>
        </ng-template>
      </fimra-filter-bar>
      <ng-template #reportCell let-row>
        <button #report hlmBtn variant="link" class="report-open" type="button"
          [attr.data-report-id]="row.id" [attr.aria-expanded]="selectedId() === row.id" (click)="open(row, report)">
          {{ title(row) }} <span class="sr-only">{{ msg('for alert #{0}, received {1}', [row.alert_id, received(row)]) }}</span>
        </button>
        <p>{{ place(row) }}</p>
      </ng-template>
      <ng-template #statusCell let-row>
        <fimra-status-badge [icon]="row.decision === 'approved' ? 'complete' : row.decision === 'returned' ? 'overdue' : 'pending'"
          [label]="status(row)" />
      </ng-template>
      <ng-template #phoneActions let-row>
        <button #report hlmBtn variant="outline" type="button"
          [attr.data-report-id]="row.id" [attr.aria-expanded]="selectedId() === row.id" (click)="open(row, report)">{{ msg('Open {0}', [title(row)]) }} <span class="sr-only">{{ msg('for alert #{0}', [row.alert_id]) }}</span></button>
      </ng-template>
      <fimra-data-table [caption]=" msg('Field reports') " [columns]="columns()" [rowKey]="rowKey" [rowLabel]="rowLabel"
        [page]="page()" [state]="state()" [phoneCards]="true" [rowActions]="phoneCards() ? phoneActions : undefined"
        [hiddenColumns]="hiddenColumns()" (hiddenColumnsChange)="setColumns($event)"
        (pageRequest)="setPosition($event)" (retry)="reload()" />
      @if (state().kind === 'ready' && !page().rows.length) { <p>{{ msg('No reports on this page.') }}</p> }
    </div>
    <fimra-side-panel class="report-panel" [heading]="view()?.title() || msg('Field report')"
      [scrollDock]="true" [fullHeight]="true" (closed)="closeReview()">
      @if (panel()?.open() && selectedId(); as id) {
        <fimra-submission-view [workspaceUrl]="endpoint()" [submissionId]="id" [showTitle]="false"
          [now]="now()" [stage]="selectedStage()" [reviewerRole]="reviewerRole()" [nextPendingId]="nextPending()?.id"
          (decided)="afterDecision($event)" (next)="openNext()" />
      }
    </fimra-side-panel>
  `
})
export class Submissions {
  protected readonly msg = translate;
  readonly endpoint = input.required<string>();
  readonly now = input.required<number>();
  readonly filtersChanged = output<ReportFilters>();
  protected readonly statuses = statuses;
  protected readonly page = signal<TablePage<SubmissionRow>>(emptyPage);
  protected readonly state = signal<DataState>({ kind: 'loading', heading: 'Loading field reports' });
  protected readonly forms = signal<WorkspaceForm[]>([]);
  protected readonly panel = viewChild(SidePanel);
  private readonly queue = viewChild<ElementRef<HTMLElement>>('queue');
  private readonly navigation = inject(ShellNavigation);
  protected readonly selectedId = computed(() => submissionId(this.navigation.query().get('report')));
  protected readonly view = viewChild(SubmissionView);
  private readonly attempt = signal(0);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  protected readonly reviewerRole = computed(() => {
    const roles = this.workspace.roles();
    const labels = [
      roles.some(role => role.role === 'state_admin') ? translate('State administrator') : '',
      roles.some(role => role.role === 'division_officer') ? translate('Division officer') : ''
    ];
    return labels.filter(Boolean).join(', ');
  });
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
    return found ? this.formTitle(found) : this.form() || translate('All forms');
  });
  protected readonly hiddenColumns = computed<ReadonlySet<string>>(() =>
    new Set(this.navigation.query().get('reportColumns')?.split(',') ?? []));
  protected readonly activeFilters = computed(() => {
    const filters = [];
    if (this.decision()) filters.push({ id: 'decision', label: statuses[this.decision() || 'pending'] });
    if (this.form()) filters.push({ id: 'form', label: this.formLabel() });
    return [...filters, ...this.scopeFilters.active()];
  });
  private readonly cursor = computed(() => this.navigation.query().get('cursor'));
  private readonly previous = computed(() => this.navigation.query().get('reportPages'));
  private readonly oldest = computed(() => this.navigation.query().get('reportSort') === 'oldest');

  protected readonly selectedStage = computed(() => this.page().rows.find(row => row.id === this.selectedId())?.step_label ?? '');
  private readonly decidedId = signal('');
  private readonly receiptId = computed(() => this.decidedId() === this.selectedId() ? this.decidedId() : '');
  private readonly nextOnPage = computed(() => {
    if (!this.receiptId()) return undefined;
    const rows = this.page().rows;
    const current = rows.findIndex(row => row.id === this.receiptId());
    const pending = (row: SubmissionRow) => row.id !== this.receiptId() && row.decision === 'pending' && row.can_approve;
    return rows.slice(current + 1).find(pending) ?? rows.find(pending);
  });
  private readonly nextFromQueue = signal<SubmissionRow | undefined>(undefined);
  protected readonly nextPending = computed(() => this.receiptId() ? this.nextOnPage() ?? this.nextFromQueue() : undefined);

  private readonly reportCell = viewChild<TemplateRef<{ $implicit: SubmissionRow }>>('reportCell');
  private readonly statusCell = viewChild<TemplateRef<{ $implicit: SubmissionRow }>>('statusCell');
  protected readonly columns = computed<readonly TableColumn<SubmissionRow>[]>(() => [
    { id: 'report', label: translate('Report and place'), value: row => this.title(row), rowHeading: true, cell: this.reportCell() },
    { id: 'status', label: translate('Status'), value: row => this.status(row), cell: this.statusCell() },
    { id: 'sender', label: translate('Sender'), value: row => row.submitted_by },
    { id: 'received', label: translate('Received'), value: row => this.received(row), sortable: true },
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
        if (!id) {
          panel.close();
          return;
        }
        const trigger = this.trigger;
        panel.show(() => trigger?.isConnected ? trigger :
          fallback.querySelector<HTMLElement>(`[data-report-id="${id}"]`), fallback);
      });
    });

    effect(onCleanup => {
      const decided = this.receiptId();
      const endpoint = this.endpoint();
      this.nextFromQueue.set(undefined);
      if (!decided || this.nextOnPage()) return;
      const params: Record<string, string | number> = { ...this.filters.params(), decision: 'pending', limit: 20 };
      if (this.form()) params['form'] = this.form();
      if (this.oldest()) params['sort'] = 'oldest';
      const load = (cursor?: string) => this.http.get<Page>(endpoint + '/submissions', {
        params: cursor ? { ...params, cursor } : params
      });
      const candidate = (page: Page) => page.items.find(row => row.id !== decided && row.can_approve);
      const request = load().pipe(
        expand(page => !candidate(page) && page.nextCursor ? load(page.nextCursor) : EMPTY),
        map(candidate),
        filter(row => !!row),
        take(1)
      ).subscribe({
        next: row => this.nextFromQueue.set(row),
        error: (error: HttpErrorResponse) => { if (error.status === 401) this.auth.sessionEnded(); }
      });
      onCleanup(() => request.unsubscribe());
    });

    let lastRequest = '';
    let loadedAt: Date | null = null;
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
      if (key !== lastRequest) {
        this.page.set(emptyPage);
        loadedAt = null;
      }
      const observed = loadedAt;
      if (observed) {
        this.state.set({
          kind: 'stale', heading: 'Refreshing field reports',
          observedAt: { dateTime: observed.toISOString(), label: untracked(() => workspaceFormat.dateTime(observed)) }
        });
      } else if (key !== lastRequest || untracked(this.state).kind === 'error') {
        this.state.set({ kind: 'loading', heading: 'Loading field reports' });
      }
      lastRequest = key;
      const request = this.http.get<Page>(endpoint + '/submissions', { params }).subscribe({
        next: page => {
          loadedAt = page.items.length ? new Date() : null;
          this.page.set({ rows: page.items, nextCursor: page.nextCursor ?? null, position });
          this.state.set(page.items.length || cursor ? { kind: 'ready' } :
            { kind: 'empty', heading: translate('No field reports match'), detail: translate('Change the filters to see more reports.') });
        },
        error: (error: HttpErrorResponse) => {
          const lostAccess = error.status === 401 || error.status === 403;
          if (lostAccess) {
            loadedAt = null;
            this.page.set(emptyPage);
          }
          if (error.status === 401) this.auth.sessionEnded();
          this.state.set(error.status === 403 ? { kind: 'forbidden' } :
            { kind: 'error', heading: translate('Field reports could not load'), keepContent: !!observed && !lostAccess });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  protected formTitle(form: WorkspaceForm): string {
    const newest = [...form.versions].reverse().find(v => v.status === 'published');
    return textIn(newest?.title, language()) || form.form;
  }

  protected title(row: SubmissionRow): string {
    const title = textIn(row.title, language()) || row.form;
    return title === row.step_label ? title : title + ' · ' + translate(row.step_label);
  }

  protected place(row: SubmissionRow): string {
    return [row.beat_name, row.range_name, row.division_name].filter(Boolean).join(' · ') || translate('Unknown place');
  }

  protected status(row: SubmissionRow): string {
    return statuses[row.decision];
  }

  protected received(row: SubmissionRow): string {
    return workspaceFormat.dateTime(new Date(row.received_time));
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

  protected afterDecision(report: Submission): void {
    if (report.approval) this.decidedId.set(report.id);
    this.reload();
  }

  protected openNext(): void {
    const next = this.nextPending();
    if (!next) return;
    this.trigger = null;
    this.navigation.update({ report: next.id });
  }

  protected reload(): void {
    this.attempt.update(n => n + 1);
  }
}
