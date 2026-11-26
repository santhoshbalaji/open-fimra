import { BreakpointObserver } from '@angular/cdk/layout';
import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, TemplateRef, computed, inject, input, output, signal } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { HlmButton } from '@spartan-ng/helm/button';
import { map } from 'rxjs';
import { ShellListScroll } from '../shell-list-scroll';
import { defaultWorkspaceFormat } from '../workspace-format';
import { Icon } from './icon';
import { LiveStatus } from './live-status';
import { DataState, StateRegion } from './state-region';
import { workspacePhone } from './workspace-breakpoint';

export interface TableColumn<Row> {
  id: string;
  label: string;
  value: (row: Row) => string;
  sortable?: boolean;
  /** A row heading stays visible so that users can identify every result. */
  rowHeading?: boolean;
  cell?: TemplateRef<{ $implicit: Row }>;
}

export interface TableSort {
  columnId: string;
  direction: 'ascending' | 'descending';
}

export interface TablePosition {
  cursor: string | null;
  previousCursors: readonly (string | null)[];
  sort?: TableSort;
}

export interface TablePage<Row> {
  rows: readonly Row[];
  nextCursor: string | null;
  position: TablePosition;
}

const ready: DataState = { kind: 'ready' };

@Component({
  selector: 'fimra-data-table',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButton, Icon, LiveStatus, NgTemplateOutlet, ShellListScroll, StateRegion],
  host: { class: 'block min-w-0' },
  styleUrls: ['./workspace-target.css', './data-table.css'],
  templateUrl: './data-table.html'
})
export class DataTable<Row> {
  readonly caption = input.required<string>();
  readonly columns = input.required<readonly TableColumn<Row>[]>();
  /** Supply the last successful page. Retain it during refresh and failed requests. */
  readonly page = input.required<TablePage<Row>>();
  readonly state = input<DataState>(ready);
  readonly rowKey = input.required<(row: Row) => string>();
  readonly rowLabel = input.required<(row: Row) => string>();
  readonly rowLanguage = input<(row: Row) => string | undefined>();
  readonly selected = input<ReadonlySet<string>>();
  readonly selectionChange = output<ReadonlySet<string>>();
  readonly rowActions = input<TemplateRef<{ $implicit: Row }>>();
  /** Enable only when every visible field fits in a scannable phone card. */
  readonly phoneCards = input(false);
  readonly scrollKey = input<string | null>(null);

  /** Fetch using cursor and sort, then return this position with the successful page. */
  readonly pageRequest = output<TablePosition>();
  readonly retry = output<void>();

  private readonly hiddenColumns = signal<ReadonlySet<string>>(new Set());
  private readonly phone = toSignal(
    inject(BreakpointObserver).observe(workspacePhone).pipe(map(result => result.matches)),
    { requireSync: true }
  );
  protected readonly cards = computed(() => this.phoneCards() && this.phone());
  protected readonly visibleColumns = computed(() => this.columns().filter(column => this.isVisible(column)));
  protected readonly sort = computed(() => this.page().position.sort);
  protected readonly sortLabel = computed(() => {
    const sort = this.sort();
    const column = this.columns().find(column => column.id === sort?.columnId);
    return column && sort ? `Sorted by ${column.label}, ${sort.direction}` : '';
  });
  protected readonly pageLabel = computed(() =>
    `Page ${defaultWorkspaceFormat.number(this.page().position.previousCursors.length + 1)}`);
  protected readonly selectionLabel = computed(() => {
    const count = this.selected()?.size ?? 0;
    return count ? `${defaultWorkspaceFormat.number(count)} selected` : '';
  });
  protected readonly selectionStatus = computed(() => this.selectionLabel() || 'No rows selected');
  protected readonly pageSelectionCount = computed(() =>
    this.page().rows.filter(row => this.isSelected(row)).length);
  protected readonly allSelected = computed(() => !!this.pageSelectionCount()
    && this.pageSelectionCount() === this.page().rows.length);
  protected readonly refreshing = computed(() => this.state().kind === 'stale');
  protected readonly canGoBack = computed(() => !this.refreshing() && !!this.page().position.previousCursors.length);
  protected readonly canGoNext = computed(() => !this.refreshing() && this.page().nextCursor !== null);

  protected isVisible(column: TableColumn<Row>) {
    return column.rowHeading || !this.hiddenColumns().has(column.id);
  }

  protected canHide(column: TableColumn<Row>) {
    return !column.rowHeading && (!this.isVisible(column) || this.visibleColumns().length > 1);
  }

  protected toggleColumn(column: TableColumn<Row>) {
    if (!this.canHide(column)) return;
    const hidden = new Set(this.hiddenColumns());
    if (hidden.has(column.id)) hidden.delete(column.id);
    else hidden.add(column.id);
    this.hiddenColumns.set(hidden);
  }

  protected direction(column: TableColumn<Row>) {
    const sort = this.sort();
    return sort?.columnId === column.id ? sort.direction : null;
  }

  protected sortBy(column: TableColumn<Row>) {
    if (this.refreshing()) return;
    const direction = this.direction(column) === 'ascending' ? 'descending' : 'ascending';
    this.pageRequest.emit({ cursor: null, previousCursors: [], sort: { columnId: column.id, direction } });
  }

  protected previousPage() {
    if (!this.canGoBack()) return;
    const { position } = this.page();
    const cursor = position.previousCursors.at(-1) ?? null;
    this.pageRequest.emit({ ...position, cursor, previousCursors: position.previousCursors.slice(0, -1) });
  }

  protected nextPage() {
    const { position, nextCursor } = this.page();
    if (!this.canGoNext() || nextCursor === null) return;
    const previousCursors = [...position.previousCursors, position.cursor];
    this.pageRequest.emit({ ...position, cursor: nextCursor, previousCursors });
  }

  protected isSelected(row: Row) {
    return this.selected()?.has(this.rowKey()(row)) ?? false;
  }

  protected toggleSelection(rows: readonly Row[], checked: boolean) {
    const selected = new Set(this.selected());
    for (const row of rows) {
      const key = this.rowKey()(row);
      if (checked) selected.add(key);
      else selected.delete(key);
    }
    this.selectionChange.emit(selected);
  }
}
