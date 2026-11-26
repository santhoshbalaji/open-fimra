import { translate } from './language';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { DOCUMENT, NgTemplateOutlet } from '@angular/common';
import { Component, DestroyRef, ElementRef, Injector, OnInit, afterNextRender, computed, inject, output, signal, viewChild } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmLabel } from '@spartan-ng/helm/label';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { Observable, Subscription, exhaustMap, finalize, forkJoin, of, switchMap, takeWhile, timer } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { EmptyState } from './ui/empty-state';
import { FieldError, FieldErrorSummary } from './ui/field-error-summary';
import { LiveStatus } from './ui/live-status';
import { DataState, StateRegion } from './ui/state-region';
import { LayerChecks, LayerProblem, LayerStatus, LayerStatusBadge } from './layer-status';
import { StatusBadge } from './ui/status-badge';
import { WorkspaceService } from './workspace';
import { workspaceFormat } from './workspace-format';
import { Icon } from './ui/icon';

export type StepKey = 'workspace' | 'hierarchy' | 'layers' | 'firms' | 'forms' | 'invitations' | 'finish';
type Labels = Record<string, string>;
interface Level { depth: number; slug: string; labels: Labels; units: number }
interface PresetLevel { slug: string; labels: Labels }
interface Preset { slug: string; name: string; levels: PresetLevel[] }
interface Upload { versionId: string; version: number; status: LayerStatus }
export interface SetupLayer {
  key: string; slug: string; name: string; role: 'hierarchy' | 'forest' | 'exclusion';
  depth?: number; required: boolean; published: boolean; latest: Upload | null;
}
interface Firms { set: boolean; setAt?: string; hint?: string; source: 'settings' | 'environment' | 'none'; encryption: boolean }
export interface SetupState {
  workspace: { slug: string; name: string }; mode: 'state' | 'national'; preset: string | null;
  hierarchy: string; root: { code: string; path: string; published: boolean } | null; boundarySet: boolean;
  levels: Level[]; layers: SetupLayer[]; steps: Partial<Record<StepKey | 'mode', { status: 'done' | 'skipped'; updatedAt: string }>>;
  firms: Firms; emailEnabled: boolean; finishedAt: string | null; presets: Preset[];
}
interface Draft { versionId: string; status: Upload['status']; unitCount: number; featureCount: number; problems: LayerProblem[] }
interface Preview { days: number; blocked: string; grantConfirmation: string; brokenGrants: unknown[] }
interface Invitation { id: string; email: string; role: string; unitPath: string; unitName: string; expiresAt: string; emailStatus: string }
interface Unit { depth: number; code: string; name: string; path: string }
export interface PackForm {
  form: string; title: string; version: number; steps: string[]; status: 'added' | 'present' | 'missing';
  assigned: string[]; skipped?: { step: string; reason: string }[];
}
export interface StarterPack { enabled: boolean; forms: PackForm[] }

const workflowStepLabels: Record<string, string> = {
  team_sent: 'Team sent', reached_site: 'Reached site', first_report: 'First report', final_report: 'Final report'
};

export const steps: { key: StepKey; label: string }[] = [
  { key: 'workspace', label: 'Workspace' },
  { key: 'hierarchy', label: 'Hierarchy' },
  { key: 'layers', label: 'Layers' },
  { key: 'firms', label: 'NASA FIRMS' },
  { key: 'forms', label: 'Starter forms' },
  { key: 'invitations', label: 'Invite officers' },
  { key: 'finish', label: 'Finish' }
];

export const roleLabels: Record<string, string> = {
  state_admin: 'State administrator', division_officer: 'Division officer', field_staff: 'Field staff'
};

/** The guided first-run wizard (PLAN.md section 7) for a root administrator. */
@Component({
  selector: 'fimra-setup-wizard',
  imports: [NgTemplateOutlet, EmptyState, FieldErrorSummary, LiveStatus, StateRegion, StatusBadge, LayerChecks, LayerStatusBadge, HlmAlertImports, HlmButton, HlmCardImports, HlmFieldImports, HlmInput, HlmLabel, HlmRadioGroupImports, HlmSelectImports, HlmTableImports, Icon],
  templateUrl: './setup-wizard.html',
  styleUrl: './setup-wizard.css',
  host: { class: 'block h-full overflow-auto max-md:scroll-pt-16 max-md:scroll-pb-16' }
})
export class SetupWizard implements OnInit {
  protected readonly msg = translate;
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly destroy = inject(DestroyRef);
  private readonly workspaces = inject(WorkspaceService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '') + '/api/v1/workspaces/' +
    encodeURIComponent(this.workspaces.current()?.slug ?? '');
  private readonly heading = viewChild<ElementRef<HTMLHeadingElement>>('stepHeading');
  private readonly disclosure = viewChild<ElementRef<HTMLDetailsElement>>('stepDisclosure');
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly injector = inject(Injector);
  private readonly document = inject(DOCUMENT);
  private readonly watches = new Map<string, Subscription>();
  readonly finished = output<void>();
  readonly openLayers = output<void>();
  readonly dateTime = (value: string) => workspaceFormat.dateTime(new Date(value));
  readonly steps = steps;
  readonly roleLabels = roleLabels;
  readonly roles = Object.keys(roleLabels);
  readonly state = signal<SetupState | null>(null);
  readonly step = signal<StepKey>('workspace');
  readonly setupLoad = signal<DataState>({ kind: 'loading', heading: 'Loading setup' });
  readonly busy = signal(false);
  readonly error = signal('');
  readonly notice = signal('');

  readonly presetSlug = signal('');
  readonly levels = signal<PresetLevel[]>([]);

  readonly drafts = signal<Record<string, Draft>>({});
  readonly layerMessages = signal<Record<string, string>>({});
  readonly checkErrors = signal<Record<string, string>>({});
  readonly layerRefresh = signal<DataState>({ kind: 'ready' });
  readonly advancingDisabled = computed(() => this.busy() || this.layerRefresh().kind === 'loading');
  readonly updatingLayers = computed(() => this.busy() || this.layerRefresh().kind !== 'ready');
  readonly fields = signal({ nameField: 'name', codeField: 'code', parentCodeField: 'parent_code' });

  readonly firmsResult = signal<{ ok: boolean; message: string } | null>(null);
  readonly skippingFirms = signal(false);

  readonly pack = signal<StarterPack | null>(null);
  readonly packLoad = signal<DataState>({ kind: 'loading', heading: 'Loading starter forms' });

  readonly invitations = signal<Invitation[]>([]);
  readonly units = signal<Unit[]>([]);
  readonly invitationsLoad = signal<DataState>({ kind: 'loading', heading: 'Loading invitations and units' });
  readonly inviteRole = signal('field_staff');
  readonly inviteUnit = signal('');
  private readonly inviteTried = signal(false);
  readonly unitMissing = computed(() => this.inviteTried() && !this.inviteUnit());
  readonly roleLabel = (role: string) => roleLabels[role] ?? role;
  readonly unitName = (path: string) => {
    const unit = this.units().find(item => item.path === path);
    return unit ? this.unitLabel(unit) : path;
  };

  readonly stepIndex = computed(() => steps.findIndex(item => item.key === this.step()));
  readonly lockedLayers = computed(() => {
    // Parents come from published units. A branch may skip an optional level.
    const layers = this.state()?.layers ?? [];
    const locked = new Set<string>();
    let blocked = false;
    for (const layer of layers) {
      if (blocked) locked.add(layer.key);
      if (layer.role === 'hierarchy' && layer.required && !layer.published) blocked = true;
    }
    return locked;
  });
  readonly layersReady = computed(() => {
    const state = this.state();
    return !!state && state.boundarySet && state.layers.every(layer => !layer.required || layer.published);
  });
  readonly layerBlockers = computed(() => (this.state()?.layers ?? []).flatMap(layer => {
    const fieldId = 'review-' + layer.key;
    const problems: FieldError[] = this.blocking(layer).map(problem => ({
      fieldId, message: `${layer.name}${problem.featureRef ? ' · ' + problem.featureRef : ''}: ${problem.message}`
    }));
    if (!problems.length && this.layerFailed(layer)) {
      problems.push({ fieldId, message: `${layer.name}: Import failed. Replace the file.` });
    }
    for (const refusal of [this.checkErrors()[layer.key], this.layerMessages()[layer.key]]) {
      if (refusal) problems.push({ fieldId, message: `${layer.name}: ${refusal}` });
    }
    return problems;
  }));
  readonly nextLayer = computed(() => this.state()?.layers.find(layer =>
    layer.required && !layer.published && !this.lockedLayers().has(layer.key)));

  ngOnInit(): void {
    this.load();
  }

  status(key: StepKey): 'done' | 'skipped' | 'todo' {
    const state = this.state();
    // Once this screen can load a workspace, the claim step is complete.
    if (key === 'workspace') return state ? 'done' : 'todo';
    if (key === 'finish') return state?.finishedAt ? 'done' : 'todo';
    return state?.steps[key]?.status ?? 'todo';
  }

  statusLabel(key: StepKey): string {
    return { done: translate('Done'), skipped: translate('Skipped'), todo: translate('To do') }[this.status(key)];
  }

  load(): void {
    this.setupLoad.set({ kind: 'loading', heading: translate('Loading setup') });
    this.http.get<SetupState>(this.base + '/setup').pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: state => {
        this.apply(state);
        this.setupLoad.set({ kind: 'ready' });
        const next = steps.find(item => this.status(item.key) === 'todo');
        this.go(next?.key ?? 'finish');
      },
      error: error => {
        const detail = this.failureMessage(error);
        this.setupLoad.set(error.status === 403 ?
          { kind: 'forbidden', detail } : { kind: 'error', heading: translate('Could not load setup'), detail });
      }
    });
  }

  go(key: StepKey, notice = ''): void {
    if (this.advancingDisabled()) return;
    this.step.set(key);
    const disclosure = this.disclosure()?.nativeElement;
    if (disclosure) disclosure.open = false;
    this.skippingFirms.set(false);
    this.error.set('');
    this.notice.set(notice);
    if (key === 'invitations') this.loadInvitations();
    if (key === 'forms') this.loadPack();
    afterNextRender(() => {
      const heading = this.heading()?.nativeElement;
      if (this.step() !== key || !heading) return;
      heading.focus({ preventScroll: true });
      this.host.nativeElement.scrollTop = 0;
    }, { injector: this.injector });
  }

  next(notice = ''): void {
    const following = steps[this.stepIndex() + 1];
    if (following) this.go(following.key, notice);
  }

  // Hierarchy

  choosePreset(slug: string): void {
    const preset = this.state()?.presets.find(item => item.slug === slug);
    if (!preset) return;
    this.presetSlug.set(slug);
    this.levels.set(preset.levels.map(level => ({ slug: level.slug, labels: { ...level.labels } })));
  }

  setLabel(index: number, language: string, value: string): void {
    this.levels.update(levels => levels.map((level, i) =>
      i === index ? { ...level, labels: { ...level.labels, [language]: value } } : level));
  }

  saveHierarchy(): void {
    const levels = this.levels().map(level => ({
      slug: level.slug,
      labels: Object.fromEntries(Object.entries(level.labels).map(([key, value]) => [key, value.trim()]).filter(([, value]) => value))
    }));
    this.run(this.http.post<SetupState>(this.base + '/setup/hierarchy', { preset: this.presetSlug(), levels }), state => {
      this.apply(state);
      this.next(translate('Hierarchy saved.'));
    });
  }

  // Layers

  layerFailed(layer: SetupLayer): boolean {
    return (this.drafts()[layer.key]?.status ?? layer.latest?.status) === 'failed';
  }

  focusLayer(key: string): void {
    this.document.getElementById('review-' + key)?.focus();
  }

  retryCheck(layer: SetupLayer): void {
    const versionId = this.drafts()[layer.key]?.versionId ?? layer.latest?.versionId;
    if (versionId) {
      this.focusLayer(layer.key);
      this.watch(layer, null, versionId);
    }
  }

  blocking(layer: SetupLayer): LayerProblem[] {
    return this.drafts()[layer.key]?.problems.filter(problem => problem.blocksPublish) ?? [];
  }

  canPublish(layer: SetupLayer): boolean {
    const draft = this.drafts()[layer.key];
    return draft?.status === 'ready' && !this.blocking(layer).length;
  }

  uploadLayer(layer: SetupLayer, input: HTMLInputElement): void {
    const file = input.files?.[0];
    if (!file) {
      this.setLayerMessage(layer, translate('Choose a file first.'));
      input.focus();
      return;
    }
    const fields = this.fields();
    const settings: Record<string, string | number> = { nameField: fields.nameField };
    if (fields.codeField) settings['codeField'] = fields.codeField;
    const data = new FormData();
    data.set('layerSlug', layer.slug);
    data.set('layerName', layer.name);
    data.set('role', layer.role);
    if (layer.role === 'hierarchy') {
      settings['depth'] = layer.depth ?? 0;
      if (fields.parentCodeField && layer.depth) settings['parentCodeField'] = fields.parentCodeField;
      data.set('hierarchy', this.state()?.hierarchy ?? '');
    }
    data.set('settings', JSON.stringify(settings));
    data.set('file', file);
    this.setLayerMessage(layer, '');
    this.run(this.http.post<Draft>(this.base + '/layer-imports', data), draft => {
      input.value = '';
      this.watch(layer, draft);
      this.notice.set(layer.name + ' uploaded. The file is being checked.');
    }, layer);
  }

  publishLayer(layer: SetupLayer): void {
    const draft = this.drafts()[layer.key];
    if (!draft) return;
    const url = this.base + '/layer-imports/' + encodeURIComponent(draft.versionId);
    this.setLayerMessage(layer, '');
    this.run(this.http.get<Preview>(url + '/preview').pipe(
      switchMap(preview => {
        if (preview.blocked) throw new HttpErrorResponse({ status: 409, error: { error: preview.blocked } });
        if (preview.brokenGrants.length) {
          throw new HttpErrorResponse({ status: 409, error: {
            error: translate('Publishing would remove role grants. Review it on the Layers screen.') } });
        }
        return this.http.post(url + '/publish', { days: preview.days, grantConfirmation: '' });
      })
    ), () => {
      this.drafts.update(drafts => {
        const remaining = { ...drafts };
        delete remaining[layer.key];
        return remaining;
      });
      this.units.set([]);
      this.notice.set(layer.name + ' published.');
      this.refreshLayers();
    }, layer);
  }

  refreshLayers(): void {
    this.layerRefresh.set({ kind: 'loading', heading: translate('Refreshing the layer list') });
    this.http.get<SetupState>(this.base + '/setup').pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: state => { this.apply(state); this.layerRefresh.set({ kind: 'ready' }); },
      error: error => this.layerRefresh.set({
        kind: 'error', heading: translate('Could not refresh the layer list'), detail: this.failureMessage(error)
      })
    });
  }

  finishLayers(): void {
    this.markStep('layers', 'done');
  }

  // FIRMS

  saveKey(input: HTMLInputElement): void {
    const mapKey = input.value.trim();
    if (!mapKey) return;
    this.firmsResult.set(null);
    this.run(this.http.put<Firms>(this.base + '/settings/firms', { mapKey }), firms => {
      // Never keep the key in the page after saving it.
      input.value = '';
      this.state.update(state => state && { ...state, firms });
      this.testKey();
    });
  }

  testKey(): void {
    this.run(this.http.post<{ ok: boolean; message: string }>(this.base + '/settings/firms/test', {}),
      result => this.firmsResult.set(result));
  }

  // Starter forms

  loadPack(): void {
    this.packLoad.set({ kind: 'loading', heading: translate('Loading starter forms') });
    this.http.get<StarterPack>(this.base + '/starter-pack').pipe(takeUntilDestroyed(this.destroy))
      .subscribe({
        next: pack => { this.pack.set(pack); this.packLoad.set({ kind: 'ready' }); },
        error: error => this.packLoad.set({ kind: 'error', heading: translate('Could not load starter forms'), detail: this.failureMessage(error) })
      });
  }

  enablePack(): void {
    this.run(this.http.post<StarterPack>(this.base + '/starter-pack', {}), pack => {
      this.pack.set(pack);
      const kept = pack.forms.filter(form => form.status === 'present').length;
      this.notice.set(translate('The starter forms are on.') +
        (kept ? ' Forms this workspace already had are left as they are.' : ''));
    });
  }

  packUse(form: PackForm): string {
    if (form.steps.length) return 'Filled in at ' + form.steps.map(step => workflowStepLabels[step] ?? step).join(' and ');
    if (form.form === 'manual-fire-report') return translate('Records a fire reported by another source and makes an alert');
    return translate('Kept on its own, not on a workflow step');
  }

  packStatus(form: PackForm): string {
    return { added: translate('Added'), present: translate('In this workspace'), missing: translate('Not added') }[form.status];
  }

  // Invitations

  loadInvitations(): void {
    if (!this.units().length) this.invitationsLoad.set({ kind: 'loading', heading: translate('Loading invitations and units') });
    forkJoin({
      list: this.http.get<{ invitations: Invitation[] }>(this.base + '/invitations'),
      scope: this.units().length ? of({ units: this.units() }) : this.http.get<{ units: Unit[] }>(this.base + '/setup/units')
    }).pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: ({ list, scope }) => {
        this.invitations.set(list.invitations);
        this.units.set(scope.units);
        this.invitationsLoad.set({ kind: 'ready' });
      },
      error: error => this.invitationsLoad.set({
        kind: 'error', heading: translate('Could not load invitations'), detail: this.failureMessage(error), keepContent: !!this.units().length
      })
    });
  }

  unitLabel(unit: Unit): string {
    const level = this.state()?.levels[unit.depth]?.labels['en'] ?? '';
    return unit.name + ' (' + level + ' ' + unit.code + ')';
  }

  invite(form: HTMLFormElement): void {
    if (!form.reportValidity()) return;
    this.inviteTried.set(true);
    if (!this.inviteUnit()) {
      form.querySelector<HTMLElement>('#invite-unit')?.focus();
      return;
    }
    this.inviteTried.set(false);
    const data = new FormData(form);
    const body = { email: String(data.get('email') ?? '').trim(), role: this.inviteRole(), unitPath: this.inviteUnit() };
    this.run(this.http.post(this.base + '/invitations', body), () => {
      (form.querySelector('[name=email]') as HTMLInputElement).value = '';
      this.notice.set(translate('Invitation saved.'));
      this.loadInvitations();
    });
  }

  revoke(item: Invitation): void {
    this.run(this.http.delete<{ invitations: Invitation[] }>(this.base + '/invitations/' + encodeURIComponent(item.id)),
      list => { this.invitations.set(list.invitations); this.notice.set(translate('Invitation revoked.')); });
  }

  // Steps

  markStep(step: StepKey, status: 'done' | 'skipped'): void {
    this.run(this.http.post<SetupState>(this.base + '/setup/steps/' + step, { status }), state => {
      this.apply(state);
      const label = steps.find(item => item.key === step)?.label ?? '';
      this.next(status === 'done' ? label + ' saved.' : label + ' skipped.');
    });
  }

  finish(): void {
    this.run(this.http.post<SetupState>(this.base + '/setup/finish', {}), state => {
      this.apply(state);
      this.finished.emit();
    });
  }

  private apply(state: SetupState): void {
    this.state.set(state);
    if (!this.presetSlug()) {
      const preset = state.presets.find(item => item.slug === state.preset) ?? state.presets[0];
      if (state.preset && state.levels.length > 1) {
        this.presetSlug.set(state.preset);
        this.levels.set(state.levels.map(level => ({ slug: level.slug, labels: { ...level.labels } })));
      } else if (preset) {
        this.choosePreset(preset.slug);
      }
    }
    for (const layer of state.layers) {
      const latest = layer.latest;
      if (latest && (latest.status === 'pending' || latest.status === 'ready' || latest.status === 'failed') &&
          !this.watches.has(layer.key) && this.drafts()[layer.key]?.versionId !== latest.versionId) {
        this.watch(layer, null, latest.versionId);
      }
    }
  }

  private watch(layer: SetupLayer, initial: Draft | null, versionId = initial?.versionId ?? ''): void {
    this.watches.get(layer.key)?.unsubscribe();
    this.checkErrors.update(errors => ({ ...errors, [layer.key]: '' }));
    if (initial) this.drafts.update(drafts => ({ ...drafts, [layer.key]: initial }));
    const url = this.base + '/layer-imports/' + encodeURIComponent(versionId);
    const subscription = timer(initial ? 1000 : 0, 1000).pipe(
      exhaustMap(() => this.http.get<Draft>(url)),
      takeWhile(draft => draft.status === 'pending', true),
      takeUntilDestroyed(this.destroy),
      finalize(() => this.watches.delete(layer.key))
    ).subscribe({
      next: draft => this.drafts.update(drafts => ({ ...drafts, [layer.key]: draft })),
      error: error => this.checkErrors.update(errors => ({ ...errors, [layer.key]: this.failureMessage(error) }))
    });
    this.watches.set(layer.key, subscription);
  }

  private setLayerMessage(layer: SetupLayer, message: string): void {
    this.layerMessages.update(messages => ({ ...messages, [layer.key]: message }));
  }

  private run<T>(request: Observable<T>, next: (value: T) => void, layer?: SetupLayer): void {
    if (this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    this.notice.set('');
    request.pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: value => { this.busy.set(false); next(value); },
      error: error => {
        this.busy.set(false);
        const message = this.failureMessage(error);
        if (layer) this.setLayerMessage(layer, message);
        else this.error.set(message);
      }
    });
  }

  private failureMessage(error: HttpErrorResponse): string {
    if (error.status === 401) this.auth.sessionEnded();
    return typeof error.error?.error === 'string' ? error.error.error : translate('The setup request failed. Try again.');
  }
}
