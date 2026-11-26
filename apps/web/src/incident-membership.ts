import { HttpClient } from '@angular/common/http';
import { Component, effect, inject, input, output, signal } from '@angular/core';
import { RouterLink, type UrlTree } from '@angular/router';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { firstValueFrom } from 'rxjs';
import { translate } from './language';
import { LiveStatus } from './ui/live-status';

type Operation = 'merge' | 'split';
interface MemberCase { id: string; step: string; alertIds: string[] }
interface Impact { source: MemberCase; target?: MemberCase; moving: string[]; previewKey: string }
interface Receipt { targetId: string; sourceId: string; moving: string[] }

@Component({
  selector: 'fimra-incident-membership',
  imports: [HlmButtonImports, HlmInputImports, HlmTextareaImports, LiveStatus, RouterLink],
  templateUrl: './incident-membership.html'
})
export class IncidentMembership {
  readonly url = input.required<string>();
  readonly enabled = input.required<boolean>();
  readonly members = input.required<readonly { id: string }[]>();
  readonly incidentLink = input.required<(id: string) => UrlTree>();
  readonly saved = output<void>();
  protected readonly msg = translate;
  protected readonly operation = signal<Operation | null>(null);
  protected readonly moving = signal<string[]>([]);
  protected readonly impact = signal<Impact | null>(null);
  protected readonly receipt = signal<Receipt | null>(null);
  protected readonly busy = signal(false);
  protected readonly message = signal('');
  private readonly http = inject(HttpClient);

  constructor() {
    effect(() => {
      this.url();
      this.choose(null);
    });
  }

  protected choose(operation: Operation | null): void {
    this.operation.set(operation);
    this.moving.set([]);
    this.impact.set(null);
    this.receipt.set(null);
    this.message.set('');
  }

  protected select(alert: string, checked: boolean): void {
    this.moving.update(ids => checked ? [...ids, alert] : ids.filter(id => id !== alert));
    this.impact.set(null);
  }

  protected async submit(target: string, reason: string): Promise<void> {
    const operation = this.operation();
    if (!operation || this.busy() || !this.enabled() || !reason.trim()) return;
    const url = this.url();
    const impact = this.impact();
    const details = operation === 'merge' ? { targetId: target.trim() } : { alertIds: this.moving() };
    this.busy.set(true);
    this.message.set('');
    try {
      if (impact) {
        const receipt = await firstValueFrom(this.http.post<Receipt>(url + '/membership', {
          operation, ...details, reason, previewKey: impact.previewKey
        }));
        if (this.url() !== url) return;
        this.choose(null);
        this.receipt.set(receipt);
        this.message.set(translate('Incident membership saved.'));
        this.saved.emit();
      } else {
        const preview = await firstValueFrom(this.http.post<Impact>(url + '/membership', { operation, ...details }, { params: { preview: true } }));
        if (this.url() === url) {
          this.impact.set(preview);
          this.message.set(translate('Impact preview ready.'));
        }
      }
    } catch {
      if (this.url() === url) {
        this.impact.set(null);
        this.message.set(translate('Could not complete the request. Check the incident and member alerts, then preview again.'));
      }
    } finally {
      this.busy.set(false);
    }
  }
}
