import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, OnInit, computed, inject, output, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { toast } from '@spartan-ng/brain/sonner';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmSkeleton } from '@spartan-ng/helm/skeleton';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { EmptyState } from './ui/empty-state';
import { Icon } from './ui/icon';
import { WorkspaceService } from './workspace';

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

/** Personal access keys for desktop GIS, and the GIS link address for the current workspace. */
@Component({
  selector: 'fimra-access-keys',
  imports: [DatePipe, EmptyState, HlmAlertImports, HlmBadge, HlmButton, HlmCardImports, HlmDialogImports, HlmFieldImports,
    HlmInput, HlmSelectImports, HlmSkeleton, HlmTableImports, Icon],
  templateUrl: './access-keys.html',
  host: { class: 'block h-full overflow-auto' }
})
export class AccessKeys implements OnInit {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly workspace = inject(WorkspaceService).current;
  readonly closed = output<void>();
  readonly expiries = expiries;
  readonly keys = signal<AccessKey[] | null>(null);
  readonly limit = signal(20);
  readonly expiry = signal<string>('90');
  readonly busy = signal(false);
  readonly error = signal('');
  /** The key just made. It is shown once and never again. */
  readonly made = signal<AccessKey | null>(null);
  readonly revoking = signal<AccessKey | null>(null);
  readonly address = computed(() => {
    const slug = this.workspace()?.slug;
    return slug ? this.api + '/ogc/workspaces/' + encodeURIComponent(slug) : '';
  });
  readonly full = computed(() => (this.keys()?.filter(key => !key.expired).length ?? 0) >= this.limit());
  readonly expiryLabel = (value: string) => expiries.find(option => option.value === value)?.label ?? '';

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.http.get<{ items: AccessKey[]; limit: number }>(this.api + '/api/v1/access-keys').subscribe({
      next: page => { this.keys.set(page.items); this.limit.set(page.limit); },
      error: (error: HttpErrorResponse) => this.failed(error)
    });
  }

  create(form: HTMLFormElement): void {
    const name = (new FormData(form).get('name')?.toString() ?? '').trim();
    if (!name) {
      this.error.set('Give the key a name, such as the computer it is for.');
      return;
    }
    const days = this.expiry() === 'never' ? null : Number(this.expiry());
    this.error.set('');
    this.busy.set(true);
    this.http.post<AccessKey>(this.api + '/api/v1/access-keys', days ? { name, expiresInDays: days } : { name }).subscribe({
      next: key => {
        this.busy.set(false);
        form.reset();
        this.made.set(key);
        this.keys.update(keys => [{ ...key, key: undefined }, ...(keys ?? [])]);
      },
      error: (error: HttpErrorResponse) => { this.busy.set(false); this.failed(error); }
    });
  }

  revoke(): void {
    const key = this.revoking();
    if (!key) return;
    this.busy.set(true);
    this.http.delete(this.api + '/api/v1/access-keys/' + encodeURIComponent(key.id)).subscribe({
      next: () => {
        this.busy.set(false);
        this.revoking.set(null);
        if (this.made()?.id === key.id) this.made.set(null);
        this.keys.update(keys => (keys ?? []).filter(item => item.id !== key.id));
        toast.success('The key ' + key.name + ' no longer works.');
      },
      error: (error: HttpErrorResponse) => { this.busy.set(false); this.revoking.set(null); this.failed(error); }
    });
  }

  dismissKey(): void {
    this.made.set(null);
  }

  copy(text: string, what: string): void {
    void navigator.clipboard?.writeText(text).then(
      () => toast.success(what + ' copied.'),
      () => toast.error('Could not copy. Select the text and copy it yourself.'));
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    this.error.set(typeof error.error?.error === 'string' ? error.error.error : 'The key request failed. Try again.');
  }
}
