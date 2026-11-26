import { JsonPipe } from '@angular/common';
import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { firstValueFrom } from 'rxjs';
import { WEB_CONFIG } from './config';
import { translate } from './language';
import { WorkspaceService } from './workspace';
import { workspaceFormat } from './workspace-format';

type Status = 'reported' | 'zero' | 'not_reported' | 'incomplete';
type Kind = 'incident' | 'preparedness';
interface Receipt { kind: Kind; status: Status; keyId: string; sha256: string; validatedAt: string }
interface Revision {
  id: string; revision: number; supersedes: string | null; from: string; to: string; current: boolean; receipts: Receipt[];
}
interface Coverage {
  status: Status; reported: number; expected: number;
  units: { code: string; day: string; status: Status; overlappingDetectionRecords: number | null }[];
}
interface Preview {
  digest: string;
  documents: {
    incident: {
      revision: number; window: { from: string; to: string; cutoff: string; timeZone: string }; coverage: Coverage;
      totals?: {
        detections: { records: number; deduplicated: number }; alerts: { routed: number };
        incidents: { new: number; active: number }; area: { hectares: number | null };
      };
    };
    preparedness: { preparedness: { status: Status; approvedRecords: number } };
  };
}
interface History { authority?: { stateCode: string; unitCodeSystem: string }; items: Revision[] }

@Component({
  selector: 'fimra-state-return',
  imports: [FormsModule, JsonPipe, HlmButtonImports],
  templateUrl: './state-return.html'
})
export class StateReturnView {
  private readonly http = inject(HttpClient);
  private readonly config = inject(WEB_CONFIG);
  private readonly workspace = inject(WorkspaceService);
  private readonly base = computed(() => {
    const slug = this.workspace.current()?.slug;
    return slug ? this.config.apiBaseUrl.replace(/\/$/, '') + '/api/v1/workspaces/' + encodeURIComponent(slug) + '/state-extracts' : '';
  });

  readonly msg = translate;
  readonly number = workspaceFormat.number;
  readonly dateTime = (value: string) => workspaceFormat.dateTime(new Date(value));
  readonly resultLabel: Record<Status, string> = {
    reported: 'Reported', zero: 'Zero', not_reported: 'Not reported', incomplete: 'Incomplete'
  };
  readonly kindLabel: Record<Kind, string> = { incident: 'Incident figures', preparedness: 'Preparedness and grants' };
  readonly history = signal<History | null>(null);
  readonly preview = signal<Preview | null>(null);
  readonly busy = signal(false);
  readonly failure = signal('');
  readonly notice = signal('');
  from = '';
  to = '';
  stateCode = '';
  unitCodeSystem = '';

  constructor() {
    effect(onCleanup => {
      const base = this.base();
      this.history.set(null);
      this.preview.set(null);
      this.failure.set('');
      this.notice.set('');
      this.busy.set(false);
      if (!base) return;
      const request = this.http.get<History>(base).subscribe({
        next: history => this.history.set(history),
        error: () => this.failure.set('Could not load state returns.')
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  chooseDates(from: string, to: string): void {
    this.from = from;
    this.to = to;
    this.preview.set(null);
  }

  async requestPreview(): Promise<void> {
    const base = this.base();
    this.preview.set(null);
    await this.run(base, 'Could not preview the state return. Check the dates and reporting season.', async () => {
      const preview = await firstValueFrom(this.http.post<Preview>(base, { action: 'preview', from: this.from, to: this.to }));
      if (base === this.base()) this.preview.set(preview);
    });
  }

  async sign(): Promise<void> {
    const current = this.preview();
    if (!current) return;
    const base = this.base();
    const { from, to } = current.documents.incident.window;
    await this.run(base, 'Could not sign the state return. Preview the latest figures and try again.', async () => {
      const revision = await firstValueFrom(this.http.post<Revision>(base, { action: 'approve', from, to, digest: current.digest }));
      if (base !== this.base()) return;
      this.preview.set(null);
      this.history.update(history => ({
        authority: history?.authority,
        items: [revision, ...(history?.items ?? []).map(item => item.id === revision.supersedes ? { ...item, current: false } : item)]
      }));
      this.notice.set('State return signed.');
    });
  }

  async configure(): Promise<void> {
    const base = this.base();
    await this.run(base, 'Could not configure the producing authority.', async () => {
      await firstValueFrom(this.http.post(base + '/settings', {
        action: 'configure', stateCode: this.stateCode, unitCodeSystem: this.unitCodeSystem
      }));
      const history = await firstValueFrom(this.http.get<History>(base));
      if (base === this.base()) this.history.set(history);
    });
  }

  async download(revision: Revision, kind: Kind): Promise<void> {
    const base = this.base();
    await this.run(base, 'Could not download the state return.', async () => {
      const file = await firstValueFrom(this.http.get(base + '/' + revision.id + '/' + kind, { responseType: 'blob' }));
      if (base !== this.base()) return;
      this.save(file, 'state-' + kind + '-' + revision.id + '.json');
    });
  }

  receiptFile(revision: Revision, receipt: Receipt): void {
    this.save(new Blob([JSON.stringify(receipt, null, 2) + '\n'], { type: 'application/json' }),
      'state-' + receipt.kind + '-' + revision.id + '-receipt.json');
  }

  private save(file: Blob, name: string): void {
    const href = URL.createObjectURL(file);
    const link = document.createElement('a');
    link.href = href;
    link.download = name;
    link.click();
    setTimeout(() => URL.revokeObjectURL(href), 1000);
  }

  private async run(base: string, error: string, task: () => Promise<void>): Promise<void> {
    if (!base || this.busy()) return;
    this.busy.set(true);
    this.failure.set('');
    this.notice.set('');
    try {
      await task();
    } catch {
      if (base === this.base()) this.failure.set(error);
    } finally {
      if (base === this.base()) this.busy.set(false);
    }
  }
}
