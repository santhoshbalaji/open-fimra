import { DOCUMENT } from '@angular/common';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, Injector, OnInit, TemplateRef, afterNextRender, computed, inject, signal, viewChild } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCheckbox } from '@spartan-ng/helm/checkbox';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmLabel } from '@spartan-ng/helm/label';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { Observable, Subscription, exhaustMap, finalize, takeWhile, timer } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { LayerPreviewMap } from './layers-map';
import { LayerChecks, LayerProblem, LayerStatus, LayerStatusBadge } from './layer-status';
import { ConfirmDialog } from './ui/confirm-dialog';
import { DataTable, TableColumn, TablePage } from './ui/data-table';
import { FieldErrorSummary } from './ui/field-error-summary';
import { Icon } from './ui/icon';
import { LiveStatus } from './ui/live-status';
import { SidePanel } from './ui/side-panel';
import { DataState, StateRegion } from './ui/state-region';
import { WorkspaceService } from './workspace';
import { defaultWorkspaceFormat } from './workspace-format';

type Role = 'hierarchy' | 'forest' | 'exclusion' | 'analysis';
interface VersionName { layerName: string; version: number }
type ImportField = 'nameField' | 'codeField' | 'parentCodeField' | 'sourceLayer';
const importFields: ImportField[] = ['nameField', 'codeField', 'parentCodeField', 'sourceLayer'];
interface Version extends VersionName {
  versionId: string; layerSlug: string; sourceFile: string | null; depth: number | null;
  status: LayerStatus; blocksPublish: boolean; hasImport: boolean; fields: Partial<Record<ImportField, string>>;
}
interface Layer {
  slug: string; name: string; role: Role; hierarchy: string | null; depth: number | null;
  published: Version | null; draft: Version | null; previous: Version[]; lastChangedAt: string;
}
interface Hierarchy { slug: string; name: string; levels: { depth: number; name: string }[] | null }
interface Catalog { layers: Layer[]; hierarchies: Hierarchy[]; uploads: Version[] }
interface Draft {
  versionId: string; status: LayerStatus; role: Role; sourceCrs: string | null;
  featureCount: number; unitCount: number; problems: LayerProblem[];
  rematch?: { status: 'pending' | 'running' | 'completed' | 'failed'; processed: number; alertsCreated: number };
}
interface Change {
  detectionId: string; observedAt: string; oldClass: string; newClass: string;
  oldBeats: string[]; newBeats: string[]; oldTriage: string | null; newTriage: string | null;
}
const categories = [
  { key: 'included', label: 'Newly included' },
  { key: 'excluded', label: 'Newly excluded' },
  { key: 'reassigned', label: 'Beat or class changed' },
  { key: 'triage', label: 'Triage changed' }
] as const;
interface Preview {
  days: number; scanned: number; blocked: string; grantConfirmation: string; previous: VersionName[];
  changes: Partial<Record<typeof categories[number]['key'], { count: number; items: Change[] }>>;
  brokenGrants: { userId: string; username: string; role: string; unitPath: string }[];
}

@Component({
  selector: 'fimra-layers',
  imports: [DataTable, FieldErrorSummary, LayerChecks, LayerStatusBadge, LayerPreviewMap,
    LiveStatus, SidePanel, StateRegion, HlmAlertImports, HlmButton, HlmCheckbox, HlmFieldImports,
    HlmInput, HlmLabel, HlmSelectImports, Icon],
  templateUrl: './layers.html',
  host: { class: 'block h-full overflow-auto scroll-pb-16' }
})
export class Layers implements OnInit {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly destroy = inject(DestroyRef);
  private readonly injector = inject(Injector);
  private readonly document = inject(DOCUMENT);
  private readonly confirmation = inject(ConfirmDialog);
  private readonly workspace = inject(WorkspaceService).current();
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '') +
    '/api/v1/workspaces/' + encodeURIComponent(this.workspace?.slug ?? '') + '/layer-imports';
  private readonly panel = viewChild.required(SidePanel);
  private readonly checksCell = viewChild<TemplateRef<{ $implicit: Layer }>>('checksCell');
  private readonly timeCell = viewChild<TemplateRef<{ $implicit: Layer }>>('timeCell');
  readonly pagePrimary = viewChild<TemplateRef<unknown>>('primary');
  private watch?: Subscription;
  private catalogWatch?: Subscription;
  private catalogRequest?: Subscription;
  private destroyed = false;

  readonly workspaceName = this.workspace?.name ?? '';
  readonly format = defaultWorkspaceFormat;
  readonly dateTime = (value: string) => this.format.dateTime(new Date(value));
  readonly categories = categories;
  readonly roleLabels: Record<Role, string> = {
    hierarchy: 'Hierarchy boundaries', forest: 'Forest eligibility', exclusion: 'Known heat sources', analysis: 'Analysis'
  };
  readonly roles = ['forest', 'hierarchy', 'exclusion', 'analysis'] as const;
  readonly catalog = signal<Catalog | null>(null);
  readonly catalogState = signal<DataState>({ kind: 'loading', heading: 'Loading layers' });
  readonly rowKey = (row: Layer) => row.slug;
  readonly rowLabel = (row: Layer) => row.name;
  readonly page = computed<TablePage<Layer>>(() => ({
    rows: this.catalog()?.layers ?? [], nextCursor: null, position: { cursor: null, previousCursors: [] }
  }));
  readonly columns = computed<readonly TableColumn<Layer>[]>(() => [
    { id: 'name', label: 'Layer', rowHeading: true, value: this.rowLabel },
    { id: 'kind', label: 'Kind', value: row => this.roleLabels[row.role] },
    { id: 'published', label: 'Published', value: row => this.versionLabel(row.published) },
    { id: 'draft', label: 'Draft', value: row => this.versionLabel(row.draft) },
    { id: 'checks', label: 'Version status', value: () => '', cell: this.checksCell() },
    { id: 'change', label: 'Last change', value: () => '', cell: this.timeCell() }
  ]);

  readonly chosenLayer = signal('');
  readonly chosenRole = signal<Role>('forest');
  readonly chosenHierarchy = signal('');
  readonly chosenDepth = signal<number | null>(null);
  readonly checked = signal(false);
  readonly layer = computed(() => this.catalog()?.layers.find(item => item.slug === this.chosenLayer()));
  readonly role = computed(() => this.layer()?.role ?? this.chosenRole());
  readonly hierarchy = computed(() => this.catalog()?.hierarchies.find(item =>
    item.slug === (this.layer()?.hierarchy ?? this.chosenHierarchy())));
  readonly depth = computed(() => this.layer()?.depth ?? this.chosenDepth());
  readonly missing = computed(() => {
    if (!this.checked() || this.role() !== 'hierarchy') return [];
    return [
      ...(!this.hierarchy() ? [{ fieldId: 'layer-hierarchy', message: 'Choose a hierarchy.' }] : []),
      ...(this.depth() === null ? [{ fieldId: 'layer-depth', message: 'Choose a level.' }] : [])
    ];
  });

  readonly missingHierarchy = computed(() => this.missing().some(item => item.fieldId === 'layer-hierarchy'));
  readonly missingDepth = computed(() => this.missing().some(item => item.fieldId === 'layer-depth'));
  readonly draft = signal<Draft | null>(null);
  readonly version = signal('');
  readonly reviewing = computed(() => this.versions().find(item => item.versionId === this.version()));
  readonly versions = computed(() => {
    const layer = this.layer();
    if (!layer) return [];
    const items = [layer.draft, layer.published, ...layer.previous,
      ...(this.catalog()?.uploads.filter(item => item.layerSlug === layer.slug) ?? [])];
    return [...new Map(items.filter((item): item is Version => item !== null && item.layerSlug === layer.slug)
      .map(item => [item.versionId, item])).values()].sort((a, b) => b.version - a.version);
  });
  readonly days = signal(30);
  readonly preview = signal<Preview | null>(null);
  readonly mapURL = computed(() => this.versionURL() + '/map');
  readonly confirming = signal(false);
  readonly removeGrants = signal(false);
  readonly operation = signal<'upload' | 'preview' | 'publish' | null>(null);
  readonly busy = computed(() => this.operation() !== null || this.confirming());
  readonly uploading = signal(false);
  readonly draftState = signal<DataState>({ kind: 'ready' });
  readonly error = signal('');
  readonly needsReplacement = computed(() => this.draft()?.status === 'failed' ||
    !!this.draft()?.problems.some(problem => problem.blocksPublish));
  readonly announce = computed(() => {
    switch (this.operation()) {
      case 'upload': return 'Saving layer upload.';
      case 'preview': return 'Checking past detections.';
      case 'publish': return 'Publishing layer.';
    }
    const draft = this.draft();
    if (draft?.status === 'pending') return 'Upload saved. The file is being checked.';
    if (draft?.status === 'published') return this.versionLabel(this.reviewing()) + ' published.';
    if (this.needsReplacement()) return 'The layer needs a corrected file.';
    if (this.preview()) return 'Preview results are ready.';
    return draft?.status === 'ready' ? 'Layer checks passed. Preview is available.' : '';
  });

  readonly roleLabel = (role: Role) => this.roleLabels[role];
  readonly hierarchyLabel = (slug: string) => this.catalog()?.hierarchies.find(item => item.slug === slug)?.name ?? slug;
  readonly levelLabel = (depth: number) => this.hierarchy()?.levels?.find(level => level.depth === depth)?.name ?? String(depth);
  readonly versionLabel = (item: VersionName | null | undefined) => item ? 'Version ' + item.version : '—';
  readonly previousLabel = (items: VersionName[]) => items.map(item => item.layerName + ' ' + this.versionLabel(item).toLowerCase()).join(', ');

  constructor() {
    this.destroy.onDestroy(() => { this.destroyed = true; });
  }

  ngOnInit(): void {
    this.loadCatalog();
  }

  loadCatalog(): void {
    this.catalogWatch?.unsubscribe();
    this.catalogRequest?.unsubscribe();
    this.catalogRequest = this.http.get<Catalog>(this.base).pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: catalog => {
        this.catalog.set(catalog);
        this.catalogState.set(catalog.layers.length ? { kind: 'ready' } : {
          kind: 'empty', heading: 'No layers yet', detail: 'Add a layer to upload its first version.'
        });
        if (catalog.layers.some(row => row.draft?.status === 'pending')) {
          this.catalogWatch = timer(3000).pipe(takeUntilDestroyed(this.destroy)).subscribe(() => this.loadCatalog());
        }
      },
      error: error => this.catalogState.set(this.failureState(error, 'Could not load the layer catalog', !!this.catalog()?.layers.length))
    });
  }

  nextAction(layer: Layer): string {
    const draft = layer.draft;
    if (draft?.status === 'pending') return 'View progress';
    if (draft?.status === 'failed' || draft?.blocksPublish) return 'Fix problems';
    if (draft) return 'Review draft';
    return layer.published ? 'View layer' : 'Upload file';
  }

  openLayer(trigger: HTMLElement, layer?: Layer): void {
    if (this.busy()) return;
    this.closeLayer();
    this.chosenLayer.set(layer?.slug ?? '');
    this.chosenRole.set('forest');
    this.chooseHierarchy('');
    this.panel().show(trigger, this.document.getElementById('add-layer') ?? undefined);
    const version = layer?.draft ?? layer?.published;
    if (version) this.chooseVersion(version.versionId);
    else this.uploading.set(true);
  }

  closeLayer(): void {
    this.watch?.unsubscribe();
    this.version.set('');
    this.draft.set(null);
    this.draftState.set({ kind: 'ready' });
    this.resetPreview();
    this.uploading.set(false);
    this.error.set('');
    this.checked.set(false);
  }

  chooseVersion(id: string): void {
    if (this.busy()) return;
    this.uploading.set(false);
    this.error.set('');
    this.resetPreview();
    this.watch?.unsubscribe();
    this.draft.set(null);
    this.version.set(id);
    this.draftState.set({ kind: 'ready' });
    if (this.reviewing()?.hasImport) this.watchDraft();
  }

  scope(depth: number | null | undefined): string {
    if (this.role() !== 'hierarchy') return [this.workspaceName, this.roleLabels[this.role()]].filter(Boolean).join(' · ');
    return [this.workspaceName, this.hierarchy()?.name, depth == null ? 'All levels in the source' : this.levelLabel(depth)]
      .filter(Boolean).join(' · ');
  }

  beginUpload(): void {
    const layer = this.layer();
    this.chosenDepth.set(layer?.depth ?? this.reviewing()?.depth ?? null);
    this.uploading.set(true);
    this.error.set('');
    this.focus('layer-file');
  }

  cancelUpload(): void {
    this.uploading.set(false);
    this.error.set('');
    this.checked.set(false);
    this.focus('replace-layer');
  }

  upload(form: HTMLFormElement): void {
    if (this.busy()) return;
    this.checked.set(true);
    if (this.missing().length) {
      this.focus('layer-upload-errors');
      return;
    }
    this.checked.set(false);
    const data = new FormData(form);
    const layer = this.layer();
    const settings: Record<string, string | number> = {};
    for (const field of importFields) {
      const value = String(data.get(field) ?? '').trim();
      if (value) settings[field] = value;
      data.delete(field);
    }
    if (layer) {
      data.set('layerSlug', layer.slug);
      data.set('layerName', layer.name);
    }
    const slug = String(data.get('layerSlug'));
    data.set('role', this.role());
    if (this.role() === 'hierarchy') {
      settings['depth'] = this.depth() ?? 0;
      data.set('hierarchy', this.hierarchy()?.slug ?? '');
    }
    data.set('settings', JSON.stringify(settings));
    this.run('upload', this.http.post<Draft>(this.base, data), draft => {
      this.loadCatalog();
      if (!this.panel().open()) return;
      this.chosenLayer.set(slug);
      this.uploading.set(false);
      this.resetPreview();
      this.version.set(draft.versionId);
      this.watchDraft(draft);
      this.focus('layer-review');
    });
  }

  watchDraft(initial?: Draft): void {
    this.watch?.unsubscribe();
    const id = this.version();
    if (!id) return;
    this.draft.set(initial ?? null);
    this.draftState.set(initial ? { kind: 'ready' } : { kind: 'loading', heading: 'Loading layer version' });
    const processing = (draft: Draft) => draft.status === 'pending' ||
      draft.rematch?.status === 'pending' || draft.rematch?.status === 'running';
    this.watch = timer(initial ? 1000 : 0, 1000).pipe(
      exhaustMap(() => this.http.get<Draft>(this.versionURL())),
      takeWhile(processing, true),
      takeUntilDestroyed(this.destroy)
    ).subscribe({
      next: draft => {
        this.draftState.set({ kind: 'ready' });
        this.draft.set(draft);
        if (this.reviewing()?.status !== draft.status) this.loadCatalog();
      },
      error: error => this.draftState.set(this.failureState(error, 'Could not check the layer version', !!this.draft()))
    });
  }

  selectRole(role: string): void {
    const selection = this.roles.find(item => item === role);
    if (selection) this.chosenRole.set(selection);
  }

  chooseHierarchy(slug: string): void {
    this.chosenHierarchy.set(slug);
    this.chosenDepth.set(null);
  }

  changeDays(days: number): void {
    this.days.set(days);
    this.resetPreview();
  }

  previewDraft(): void {
    if (this.busy() || this.needsReplacement() || this.draft()?.status !== 'ready') return;
    this.resetPreview();
    this.run('preview', this.http.get<Preview>(this.versionURL() + '/preview', { params: { days: this.days() } }), preview => {
      if (!this.panel().open()) return;
      this.preview.set(preview);
      this.focus('preview-heading', true);
    });
  }

  async confirmPublish(trigger: HTMLElement): Promise<void> {
    const preview = this.preview();
    const item = this.reviewing();
    const draft = this.draft();
    const remove = this.removeGrants();
    if (this.busy() || !item || draft?.status !== 'ready' || !preview || preview.blocked || this.needsReplacement() ||
        (preview.brokenGrants.length && !remove)) return;

    const scope = this.scope(item.depth);
    const previous = this.previousLabel(preview.previous);
    const replacement = previous ? 'It will replace ' + previous + ' in this scope.' : 'This is the first publication in this scope.';
    const grants = remove && preview.brokenGrants.length ?
      ' The ' + (preview.brokenGrants.length === 1 ? 'grant' : preview.brokenGrants.length + ' grants') + ' you reviewed will be removed.' : '';
    this.confirming.set(true);
    const confirmed = await this.confirmation.open({
      heading: 'Publish ' + this.versionLabel(item).toLowerCase() + '?',
      detail: item.layerName + ' will go live for ' + scope + '. ' + replacement +
        ' The past ' + preview.days + ' days of detections will be checked again.' + grants,
      confirmLabel: 'Confirm and publish', trigger
    });
    if (this.destroyed) return;
    this.confirming.set(false);
    if (!confirmed || !this.panel().open() || this.preview() !== preview) return;

    this.run('publish', this.http.post(this.versionURL() + '/publish',
      { days: preview.days, grantConfirmation: remove ? preview.grantConfirmation : '' }), () => {
      this.loadCatalog();
      if (!this.panel().open()) return;
      this.resetPreview();
      this.watchDraft({ ...draft, status: 'published', rematch: { status: 'pending', processed: 0, alertsCreated: 0 } });
      this.focus('layer-review');
    });
  }

  changeNote(change: Change): string {
    const labels: Record<string, string> = {
      inside: 'Inside forest', near_edge: 'Near the edge', outside: 'Outside forest',
      fixed_heat_source: 'Known heat source', shared_boundary: 'Shared boundary',
      overlapping_units: 'Overlapping units', no_unit: 'No mapped beat'
    };
    const changes = [];
    if (change.oldClass !== change.newClass) {
      changes.push(labels[change.oldClass] + ' → ' + labels[change.newClass]);
    }
    if (change.oldBeats.join(',') !== change.newBeats.join(',')) {
      changes.push((change.oldBeats.join(', ') || 'No beat') + ' → ' + (change.newBeats.join(', ') || 'No beat'));
    }
    if (change.oldTriage !== change.newTriage) {
      changes.push((change.oldTriage ? labels[change.oldTriage] : 'No triage') + ' → ' +
        (change.newTriage ? labels[change.newTriage] : 'No triage'));
    }
    return changes.join(' · ');
  }

  private focus(id: string, scrollToStart = false): void {
    afterNextRender(() => {
      const target = this.document.getElementById(id);
      target?.focus({ preventScroll: scrollToStart });
      if (scrollToStart) target?.scrollIntoView({ block: 'start' });
    }, { injector: this.injector });
  }

  private versionURL(): string {
    return this.base + '/' + encodeURIComponent(this.version());
  }

  private resetPreview(): void {
    this.preview.set(null);
    this.removeGrants.set(false);
  }

  private run<T>(operation: NonNullable<ReturnType<typeof this.operation>>, request: Observable<T>, next: (value: T) => void): void {
    this.operation.set(operation);
    this.error.set('');
    request.pipe(takeUntilDestroyed(this.destroy), finalize(() => this.operation.set(null))).subscribe({
      next,
      error: (error: HttpErrorResponse) => {
        if (error.status === 409 && operation === 'publish') this.resetPreview();
        this.error.set(this.errorMessage(error));
        this.focus('layer-request-error');
      }
    });
  }

  private failureState(error: HttpErrorResponse, heading: string, keepContent: boolean): DataState {
    const detail = this.errorMessage(error);
    return error.status === 403 ? { kind: 'forbidden', heading, detail } : { kind: 'error', heading, detail, keepContent };
  }

  private errorMessage(error: HttpErrorResponse): string {
    if (error.status === 401) this.auth.sessionEnded();
    return typeof error.error?.error === 'string' ? error.error.error : 'The layer request failed. Try again.';
  }
}
