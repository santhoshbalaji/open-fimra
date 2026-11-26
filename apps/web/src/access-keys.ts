import { translate } from './language';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, ElementRef, Injector, OnInit, afterNextRender, computed, inject, output, signal, viewChild } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ConfirmDialog } from './ui/confirm-dialog';
import { DataTable, TableColumn, TablePage } from './ui/data-table';
import { LiveStatus } from './ui/live-status';
import { restoreOverlayFocus } from './ui/overlay-focus';
import { DataState } from './ui/state-region';
import { StatusBadge } from './ui/status-badge';
import { WorkspaceService } from './workspace';
import { workspaceFormat } from './workspace-format';

export interface AccessKey {
  id: string; name: string; hint: string; createdAt: string;
  expiresAt: string | null; lastUsedAt: string | null; expired: boolean; key?: string;
}

const expiries = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: 'One year' },
  { value: 'never', label: 'Does not expire' }
] as const;

const time = (value: string) => workspaceFormat.dateTime(new Date(value));

/** Personal access keys for desktop GIS, and the GIS link address for the current workspace. */
@Component({
  selector: 'fimra-access-keys',
  imports: [DataTable, HlmButton, HlmFieldImports, HlmInput, HlmSelectImports, LiveStatus, StatusBadge],
  templateUrl: './access-keys.html',
  host: { class: 'block h-full overflow-auto' }
})
export class AccessKeys implements OnInit {
  protected readonly msg = translate;
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly workspace = inject(WorkspaceService).current;
  private readonly confirm = inject(ConfirmDialog);
  private readonly lifetime = inject(DestroyRef);
  private readonly injector = inject(Injector);
  private readonly reveal = viewChild<ElementRef<HTMLElement>>('reveal');
  private readonly keysHeading = viewChild.required<ElementRef<HTMLElement>>('keysHeading');

  readonly closed = output<void>();
  readonly expiries = expiries;
  readonly keys = signal<AccessKey[] | null>(null);
  readonly listProblem = signal('');
  readonly limit = signal(20);
  readonly expiry = signal<string>('90');
  readonly creating = signal(false);
  readonly revoking = signal(false);
  readonly createError = signal('');
  readonly revokeError = signal('');
  readonly keyCopy = signal('');
  readonly addressCopy = signal('');
  readonly keyFeedback = signal('');
  /** The key just made. It is shown once and never again. */
  readonly made = signal<AccessKey | null>(null);

  readonly address = computed(() => {
    const slug = this.workspace()?.slug;
    return slug ? this.api + '/ogc/workspaces/' + encodeURIComponent(slug) : '';
  });
  readonly full = computed(() => (this.keys()?.filter(key => !key.expired).length ?? 0) >= this.limit());
  readonly expiryLabel = (value: string) => expiries.find(option => option.value === value)?.label ?? '';
  readonly expiresLabel = (key: AccessKey) => key.expired ? translate('Expired') : key.expiresAt ? time(key.expiresAt) : translate('Never');
  readonly page = computed<TablePage<AccessKey>>(() => ({
    rows: this.keys() ?? [], nextCursor: null, position: { cursor: null, previousCursors: [] }
  }));
  readonly listState = computed<DataState>(() => {
    if (this.listProblem()) return { kind: 'error', heading: translate('Could not load keys'), detail: this.listProblem() };
    if (!this.keys()) return { kind: 'loading', heading: translate('Loading keys') };
    return this.keys()?.length ? { kind: 'ready' } :
      { kind: 'empty', heading: translate('No keys yet'), detail: translate('Create a key to open this workspace in desktop GIS.') };
  });
  readonly rowKey = (key: AccessKey) => key.id;
  readonly rowLabel = (key: AccessKey) => key.name;
  readonly columns = computed<TableColumn<AccessKey>[]>(() => [
    { id: 'name', label: translate('Name'), value: this.rowLabel, rowHeading: true },
    { id: 'hint', label: 'Starts with', value: key => 'fimra_' + key.hint + '…' },
    { id: 'created', label: translate('Created'), value: key => time(key.createdAt) },
    { id: 'expires', label: translate('Expires'), value: this.expiresLabel, cell: this.expiryCell() },
    { id: 'used', label: translate('Last used'), value: key => key.lastUsedAt ? time(key.lastUsedAt) : translate('Not yet') }
  ]);
  private readonly expiryCell = viewChild<NonNullable<TableColumn<AccessKey>['cell']>>('expiryCell');

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.listProblem.set('');
    this.http.get<{ items: AccessKey[]; limit: number }>(this.api + '/api/v1/access-keys').pipe(
      takeUntilDestroyed(this.lifetime)
    ).subscribe({
      next: page => { this.keys.set(page.items); this.limit.set(page.limit); },
      error: (error: HttpErrorResponse) => this.listProblem.set(this.problem(error))
    });
  }

  create(form: HTMLFormElement): void {
    if (!this.keys() || this.creating() || this.full() || this.made()) return;
    const name = (new FormData(form).get('name')?.toString() ?? '').trim();
    if (!name) {
      this.createError.set(translate('Give the key a name, such as the computer it is for.'));
      return;
    }
    const days = this.expiry() === 'never' ? null : Number(this.expiry());
    this.createError.set('');
    this.creating.set(true);
    this.http.post<AccessKey>(this.api + '/api/v1/access-keys', days ? { name, expiresInDays: days } : { name }).pipe(
      takeUntilDestroyed(this.lifetime)
    ).subscribe({
      next: key => {
        this.creating.set(false);
        form.reset();
        this.keyCopy.set('');
        this.made.set(key);
        this.keys.update(keys => [{ ...key, key: undefined }, ...(keys ?? [])]);
        afterNextRender(() => this.reveal()?.nativeElement.focus(), { injector: this.injector });
      },
      error: (error: HttpErrorResponse) => { this.creating.set(false); this.createError.set(this.problem(error)); }
    });
  }

  async revoke(key: AccessKey, trigger: HTMLElement): Promise<void> {
    if (this.revoking()) return;
    this.revoking.set(true);
    this.revokeError.set('');
    this.keyFeedback.set('');
    const confirmed = await this.confirm.open({
      heading: translate('Revoke ') + key.name + '?',
      detail: translate('Desktop GIS using this key stops working at once. This cannot be undone.'),
      confirmLabel: translate('Revoke key'),
      destructive: true,
      trigger
    });
    if (this.lifetime.destroyed) return;
    if (!confirmed) { this.revoking.set(false); return; }
    this.http.delete(this.api + '/api/v1/access-keys/' + encodeURIComponent(key.id)).pipe(
      takeUntilDestroyed(this.lifetime)
    ).subscribe({
      next: () => {
        this.revoking.set(false);
        if (this.made()?.id === key.id) this.made.set(null);
        const returnFocus = trigger.ownerDocument.activeElement === trigger;
        this.keys.update(keys => (keys ?? []).filter(item => item.id !== key.id));
        this.keyFeedback.set('The key ' + key.name + ' has been revoked.');
        if (returnFocus) {
          afterNextRender(() => restoreOverlayFocus(trigger, this.keysHeading().nativeElement), { injector: this.injector });
        }
      },
      error: (error: HttpErrorResponse) => { this.revoking.set(false); this.revokeError.set(this.problem(error)); }
    });
  }

  dismissKey(name: HTMLInputElement, heading: HTMLElement): void {
    this.made.set(null);
    this.keyCopy.set('');
    afterNextRender(() => restoreOverlayFocus(name, heading), { injector: this.injector });
  }

  async copy(text: string, what: 'Key' | 'Address'): Promise<void> {
    const feedback = what === 'Key' ? this.keyCopy : this.addressCopy;
    feedback.set('');
    try {
      await navigator.clipboard.writeText(text);
      feedback.set(what + ' copied.');
    } catch {
      feedback.set(translate('Could not copy. Select the text and copy it yourself.'));
    }
  }

  private problem(error: HttpErrorResponse): string {
    if (error.status === 401) this.auth.sessionEnded();
    return typeof error.error?.error === 'string' ? error.error.error : translate('The key request failed. Try again.');
  }
}
