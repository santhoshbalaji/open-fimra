import { ChangeDetectionStrategy, Component, DestroyRef, computed, inject, input, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { defaultWorkspaceFormat, WorkspaceFormat } from '../workspace-format';
import { Breadcrumbs } from '../ui/breadcrumbs';
import { DataTable, TableColumn, TablePage, TablePosition } from '../ui/data-table';
import { ActiveFilter, FilterBar, FilterControls } from '../ui/filter-bar';
import { LiveStatus } from '../ui/live-status';
import { PageHeader } from '../ui/page-header';
import { StatTile } from '../ui/stat-tile';
import { DataState } from '../ui/state-region';
import { StatusBadge } from '../ui/status-badge';
import { Timeline, TimelineEvent } from '../ui/timeline';

interface AlertExample {
  id: string;
  place: string;
  lang?: string;
  state: 'Open' | 'Closed';
  at: Date;
}

const observedAt = new Date('2025-11-08T08:35:00Z');
const longPlace = 'উত্তর পাহাড় সংরক্ষিত বনাঞ্চলের পূর্ব প্রান্তের পর্যবেক্ষণ কেন্দ্র';
const alerts: readonly AlertExample[] = [
  { id: 'A-601', place: 'Cedar ridge', state: 'Open', at: new Date('2025-11-08T08:30:00Z') },
  { id: 'A-602', place: longPlace, lang: 'bn', state: 'Open', at: new Date('2025-11-08T08:15:00Z') },
  { id: 'A-603', place: 'East ridge', state: 'Open', at: new Date('2025-11-08T07:30:00Z') },
  { id: 'A-604', place: 'Forest gate', state: 'Open', at: new Date('2025-11-08T07:00:00Z') },
  { id: 'A-605', place: 'Pine track', state: 'Open', at: new Date('2025-11-08T06:15:00Z') },
  { id: 'A-606', place: 'West ridge', state: 'Open', at: new Date('2025-11-08T05:30:00Z') },
  { id: 'A-607', place: 'Lower trail', state: 'Closed', at: new Date('2025-11-08T05:00:00Z') }
];

const firstPage: TablePosition = {
  cursor: null, previousCursors: [], sort: { columnId: 'seen', direction: 'descending' }
};
const ready: DataState = { kind: 'ready' };
const byPlace = new Intl.Collator('en-IN');

@Component({
  selector: 'fimra-workspace-example',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [Breadcrumbs, DataTable, FilterBar, FilterControls, HlmButton, LiveStatus, PageHeader, StatTile, StatusBadge, Timeline],
  host: { '[attr.data-density]': 'density()', class: 'block min-w-0' },
  templateUrl: './workspace-example.html',
  styleUrl: './workspace-example.css'
})
export class WorkspaceExample {
  readonly density = input.required<'comfortable' | 'compact'>();
  private readonly linkedStatus = new URLSearchParams(location.search).get('status');
  private readonly destroyRef = inject(DestroyRef);
  protected readonly format = defaultWorkspaceFormat;
  protected readonly hindi = new WorkspaceFormat('hi-IN');
  protected readonly now = observedAt;
  protected readonly ancestors = [{ label: 'Workspace components', href: '#workspace-components' }];
  protected readonly rowLanguage = (row: AlertExample) => row.lang;
  protected readonly rowKey = (row: AlertExample) => row.id;
  protected readonly rowLabel = (row: AlertExample) => row.place + ' · ' + row.id;
  protected readonly columns: readonly TableColumn<AlertExample>[] = [
    { id: 'place', label: 'Place', sortable: true, rowHeading: true, value: this.rowLabel },
    { id: 'status', label: 'Status', value: row => row.state },
    { id: 'seen', label: 'Last seen', sortable: true, value: row => this.format.dateTime(row.at) }
  ];
  protected readonly events: readonly TimelineEvent[] = [
    { id: 'opened', title: 'Alert opened', milestone: true, at: alerts[0].at },
    { id: 'note', title: 'Field note added', at: new Date('2025-11-08T08:32:00Z'), detail: 'The team reached the trail entrance.' },
    { id: 'assigned', title: 'Team assigned', milestone: true, at: new Date('2025-11-08T08:34:00Z') }
  ];
  protected readonly status = signal(this.linkedStatus === 'closed' ? 'Closed' : 'Open');
  protected readonly search = signal('');
  protected readonly selected = signal<ReadonlySet<string>>(new Set());
  protected readonly inspected = signal<AlertExample | null>(null);
  protected readonly tableState = signal<DataState>(ready);
  protected readonly page = signal(this.getPage(firstPage));
  protected readonly filters = computed(() => {
    const result: ActiveFilter[] = [];
    if (this.status()) result.push({ id: 'status', label: 'Status: ' + this.status() });
    if (this.search().trim()) result.push({ id: 'place', label: 'Place: ' + this.search().trim() });
    return result;
  });
  private refreshTimer?: ReturnType<typeof setTimeout>;

  constructor() {
    this.destroyRef.onDestroy(() => clearTimeout(this.refreshTimer));
  }

  protected fetchPage(position: TablePosition) {
    clearTimeout(this.refreshTimer);
    this.tableState.set({ kind: 'stale', observedAt: {
      dateTime: observedAt.toISOString(), label: this.format.dateTime(observedAt)
    } });
    this.refreshTimer = setTimeout(() => this.showPage(position), 350);
  }

  protected changeFilter(id: string, value: string) {
    if (id === 'status') this.status.set(value);
    else this.search.set(value);
    this.resetPage();
  }

  protected clearFilters() {
    this.status.set('');
    this.search.set('');
    this.resetPage();
  }

  private resetPage() {
    clearTimeout(this.refreshTimer);
    this.showPage({ ...firstPage, sort: this.page().position.sort });
  }

  private showPage(position: TablePosition) {
    const page = this.getPage(position);
    this.page.set(page);
    this.tableState.set(page.rows.length ? ready : {
      kind: 'empty', heading: 'No matching alerts', detail: 'Change the filters to see more alerts.'
    });
  }

  private getPage(position: TablePosition): TablePage<AlertExample> {
    const query = this.search().trim().toLocaleLowerCase('en-IN');
    const status = this.status();
    const matches = alerts.filter(row => (!status || row.state === status)
      && this.rowLabel(row).toLocaleLowerCase('en-IN').includes(query));
    const sort = position.sort;
    if (sort) {
      const factor = sort.direction === 'ascending' ? 1 : -1;
      matches.sort((a, b) => factor * (sort.columnId === 'place' ? byPlace.compare(a.place, b.place)
        : a.at.getTime() - b.at.getTime()));
    }
    const offset = Number(position.cursor ?? 0);
    const end = offset + 2;
    return { rows: matches.slice(offset, end), nextCursor: end < matches.length ? String(end) : null, position };
  }
}
