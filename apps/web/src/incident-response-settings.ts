import { HttpClient } from '@angular/common/http';
import { Component, computed, effect, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { firstValueFrom } from 'rxjs';
import { WEB_CONFIG } from './config';
import { translate } from './language';
import { LiveStatus } from './ui/live-status';
import { StateRegion, type DataState } from './ui/state-region';
import { WorkspaceService } from './workspace';

interface Target {
  step: string;
  from: string;
  through: string;
  targetMinutes: number | null;
  escalationMinutes: number | null;
}
interface Settings { extended: boolean; targets: Target[] }

@Component({
  selector: 'fimra-incident-response-settings',
  imports: [FormsModule, HlmButtonImports, HlmInputImports, LiveStatus, StateRegion],
  templateUrl: './incident-response-settings.html'
})
export class IncidentResponseSettings {
  private readonly http = inject(HttpClient);
  private readonly workspace = inject(WorkspaceService);
  private readonly config = inject(WEB_CONFIG);
  private readonly endpoint = computed(() => {
    const workspace = this.workspace.current();
    return workspace && this.config.apiBaseUrl.replace(/\/$/, '') + '/api/v1/workspaces/' + workspace.slug + '/settings/incident-response';
  });
  private readonly revision = signal(0);
  protected readonly msg = translate;
  protected readonly steps = [
    { step: 'new', name: 'Acknowledgement' }, { step: 'reopened', name: 'Acknowledgement after reopening' },
    { step: 'acknowledged', name: 'Assignment' }, { step: 'assigned', name: 'Dispatch' },
    { step: 'team_sent', name: 'Arrival' },
  ];
  protected readonly settings = signal<Settings | null>(null);
  protected readonly enabling = signal(false);
  protected readonly saving = signal(false);
  protected readonly notice = signal('');
  protected readonly state = signal<DataState>({ kind: 'loading', heading: 'Loading incident response settings' });

  constructor() {
    effect(cleanup => {
      const url = this.endpoint();
      this.revision();
      this.settings.set(null);
      this.enabling.set(false);
      this.notice.set('');
      this.state.set({ kind: 'loading', heading: 'Loading incident response settings' });
      if (!url) return;
      const request = this.http.get<Settings>(url).subscribe({
        next: settings => { this.settings.set(settings); this.state.set({ kind: 'ready' }); },
        error: () => this.state.set({ kind: 'error', heading: 'Could not load incident response settings.' })
      });
      cleanup(() => request.unsubscribe());
    });
  }

  protected reload(): void { this.revision.update(value => value + 1); }

  protected addTarget(): void {
    this.settings.update(settings => settings && { ...settings, targets: [...settings.targets,
      { step: 'new', from: '', through: '', targetMinutes: 30, escalationMinutes: null }] });
  }

  protected removeTarget(index: number): void {
    this.settings.update(settings => settings && { ...settings, targets: settings.targets.filter((_, i) => i !== index) });
  }

  protected editTarget(index: number, change: Partial<Target>): void {
    this.settings.update(settings => settings && { ...settings, targets: settings.targets.map((target, i) =>
      i === index ? { ...target, ...change } : target) });
  }

  protected async save(): Promise<void> {
    const url = this.endpoint();
    const settings = this.settings();
    if (!url || !settings || this.saving()) return;
    this.saving.set(true);
    this.notice.set('');
    try {
      const saved = await firstValueFrom(this.http.put<Settings>(url, { ...settings, extended: settings.extended || this.enabling() }));
      if (this.endpoint() === url) {
        this.settings.set(saved);
        this.enabling.set(false);
        this.notice.set(translate('Incident response settings saved.'));
      }
    } catch {
      if (this.endpoint() === url) {
        this.notice.set(translate('Could not save incident response settings. Check the dates and escalation minutes.'));
      }
    } finally {
      this.saving.set(false);
    }
  }
}
