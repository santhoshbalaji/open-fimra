import { translate } from './language';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, OnInit, inject, signal } from '@angular/core';
import { NgTemplateOutlet } from '@angular/common';
import { toast } from '@spartan-ng/brain/sonner';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmSkeleton } from '@spartan-ng/helm/skeleton';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { EmptyState } from './ui/empty-state';
import { Icon, IconName } from './ui/icon';
import { WorkspaceService } from './workspace';
import { workspaceFormat } from './workspace-format';

export type DeviceStatus = 'active' | 'signed_out' | 'removed' | 'wipe_pending' | 'wiped';

export interface Device {
  id: string; platform: string; appVersion: string; push: boolean; status: DeviceStatus;
  registeredAt: string; lastSeenAt: string; wipeRequestedAt: string | null; wipedAt: string | null;
  current?: boolean; owner?: { username: string; name: string }; wipeRequestedBy?: string | null;
}

const statuses: Record<DeviceStatus, { label: string; icon: IconName }> = {
  active: { label: 'Signed in', icon: 'circle-check' },
  signed_out: { label: 'Signed out', icon: 'log-out' },
  removed: { label: 'Removed', icon: 'circle-x' },
  wipe_pending: { label: 'Wipe requested', icon: 'clock' },
  wiped: { label: 'Data deleted', icon: 'lock' }
};

/** What to confirm: removing one of your phones, or wiping a phone. */
interface Pending { device: Device; action: 'remove' | 'wipe'; area: boolean }

/** Field phones: your own, and for administrators those in their part of the hierarchy. */
@Component({
  selector: 'fimra-devices',
  imports: [NgTemplateOutlet, EmptyState, HlmAlertImports, HlmBadge, HlmButton, HlmCardImports, HlmDialogImports, HlmSkeleton,
    HlmTableImports, Icon],
  templateUrl: './devices.html',
  styleUrl: './devices.css',
  host: { class: 'block min-w-0' }
})
export class Devices implements OnInit {
  protected readonly msg = translate;
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly workspace = inject(WorkspaceService);
  readonly dateTime = (value: string) => workspaceFormat.dateTime(new Date(value));
  readonly mine = signal<Device[] | null>(null);
  /** Null while loading, and stays null for people who manage no phones. */
  readonly area = signal<Device[] | null>(null);
  readonly manages = signal(false);
  readonly error = signal('');
  readonly busy = signal(false);
  readonly pending = signal<Pending | null>(null);

  ngOnInit(): void {
    this.load();
  }

  load(): void {
    this.http.get<{ items: Device[] }>(this.api + '/api/v1/devices').subscribe({
      next: page => this.mine.set(page.items),
      error: (error: HttpErrorResponse) => this.failed(error)
    });
    const slug = this.workspace.current()?.slug;
    if (!slug || !this.workspace.roles().some(({ role }) => role === 'state_admin' || role === 'division_officer')) return;
    this.http.get<{ items: Device[] }>(this.api + '/api/v1/workspaces/' + encodeURIComponent(slug) + '/devices').subscribe({
      next: page => { this.manages.set(true); this.area.set(page.items); },
      error: (error: HttpErrorResponse) => { if (error.status !== 403) this.failed(error); }
    });
  }

  statusOf(device: Device): { label: string; icon: IconName } {
    return statuses[device.status] ?? statuses.removed;
  }

  platform(device: Device): string {
    return device.platform === 'web' ? translate('Browser test build') : device.platform === 'ios' ? 'iPhone' : translate('Android phone');
  }

  owner(device: Device): string {
    return device.owner?.name || device.owner?.username || 'Someone';
  }

  canWipe(device: Device): boolean {
    return device.status !== 'wipe_pending' && device.status !== 'wiped';
  }

  confirm(): void {
    const pending = this.pending();
    if (!pending) return;
    const { device, action, area } = pending;
    const slug = this.workspace.current()?.slug ?? '';
    const url = action === 'remove' ? this.api + '/api/v1/devices/' + encodeURIComponent(device.id)
      : area ? this.api + '/api/v1/workspaces/' + encodeURIComponent(slug) + '/devices/' + encodeURIComponent(device.id) + '/wipe'
        : this.api + '/api/v1/devices/' + encodeURIComponent(device.id) + '/wipe';
    this.busy.set(true);
    const request = action === 'remove' ? this.http.delete(url) : this.http.post(url, {});
    request.subscribe({
      next: () => {
        this.busy.set(false);
        this.pending.set(null);
        toast.success(action === 'remove' ? translate('The phone was removed.')
          : translate('The phone will delete its field data the next time it connects.'));
        this.load();
      },
      error: (error: HttpErrorResponse) => { this.busy.set(false); this.pending.set(null); this.failed(error); }
    });
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    this.error.set(typeof error.error?.error === 'string' ? error.error.error : translate('The phone list could not load. Try again.'));
  }
}
