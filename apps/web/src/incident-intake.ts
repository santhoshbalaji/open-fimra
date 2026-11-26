import { HttpClient } from '@angular/common/http';
import { Component, inject, input, output, signal } from '@angular/core';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { firstValueFrom } from 'rxjs';
import { translate } from './language';
import { LiveStatus } from './ui/live-status';

@Component({
  selector: 'fimra-incident-intake',
  imports: [HlmButtonImports, HlmInputImports, HlmTextareaImports, LiveStatus],
  templateUrl: './incident-intake.html'
})
export class IncidentIntake {
  readonly endpoint = input.required<string>();
  readonly incidentId = input('');
  readonly saved = output<string>();
  protected readonly msg = translate;
  protected readonly open = signal(false);
  protected readonly busy = signal(false);
  protected readonly message = signal('');
  protected readonly sources = [
    { id: 'staff', label: 'Staff' }, { id: 'phone', label: 'Phone' }, { id: 'radio', label: 'Radio' },
    { id: 'public', label: 'Public' },
  ];
  private readonly http = inject(HttpClient);
  private attempt?: { key: string; id: string };

  protected toggle(): void {
    this.open.update(value => !value);
    this.message.set('');
  }

  protected async save(source: string, observed: string, latitude: string, longitude: string, note: string, join: boolean): Promise<void> {
    if (this.busy()) return;
    const date = new Date(observed);
    if (!Number.isFinite(date.valueOf())) return;
    const report = { source, observedAt: date.toISOString(), latitude: Number(latitude), longitude: Number(longitude), note,
      ...(join && { incidentId: this.incidentId() }) };
    const endpoint = this.endpoint();
    const key = JSON.stringify([endpoint, report]);
    if (this.attempt?.key !== key) this.attempt = { key, id: crypto.randomUUID() };
    this.busy.set(true);
    this.message.set('');
    try {
      const receipt = await firstValueFrom(this.http.post<{ incidentId: string }>(endpoint + '/fire-intake', { id: this.attempt.id, ...report }));
      if (this.endpoint() !== endpoint) return;
      this.attempt = undefined;
      this.open.set(false);
      this.message.set(translate('Report added to incident #{0}.', [receipt.incidentId]));
      this.saved.emit(receipt.incidentId);
    } catch {
      if (this.endpoint() === endpoint) this.message.set(translate('Could not save the report. Check the location and your permissions.'));
    } finally {
      this.busy.set(false);
    }
  }
}
