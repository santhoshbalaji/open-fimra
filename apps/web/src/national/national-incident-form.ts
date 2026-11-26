import { HttpClient } from '@angular/common/http';
import { Component, inject, input, output, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { firstValueFrom } from 'rxjs';
import { translate } from '../language';
import { nationalWriteError } from './national-write-error';

@Component({
  selector: 'fimra-national-incident-form',
  imports: [HlmButton],
  templateUrl: './national-incident-form.html',
})
export class NationalIncidentForm {
  private readonly http = inject(HttpClient);
  readonly base = input.required<string>();
  readonly states = input.required<string[]>();
  readonly updated = output<void>();
  protected readonly msg = translate;
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly saved = signal(false);

  protected async submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (this.busy()) return;
    const form = event.target as HTMLFormElement;
    const data = new FormData(form);
    const field = (name: string) => String(data.get(name) ?? '').trim();
    const instant = (name: string) => field(name) ? new Date(field(name)).toISOString() : null;
    const decimal = (name: string) => field(name) === '' ? null : Number(field(name));
    const base = this.base();
    this.error.set('');
    this.saved.set(false);
    this.busy.set(true);
    try {
      await firstValueFrom(this.http.put(
        base + '/states/' + encodeURIComponent(field('state')) + '/incidents/' + encodeURIComponent(field('incidentRef')),
        {
          sharedEventId: field('sharedEventId'), fsiId: field('fsiId') || null,
          status: field('status'), severity: field('severity'),
          occurredAt: instant('occurredAt'), nextUpdateAt: instant('nextUpdateAt'),
          precisionDecimals: decimal('precisionDecimals'), longitude: decimal('longitude'), latitude: decimal('latitude'),
          threats: field('threats'), aidRequested: field('aidRequested'), aidAccepted: field('aidAccepted'),
          provisionalImpacts: field('provisionalImpacts'), shared: data.has('shared')
        }
      ));
      if (base === this.base()) {
        this.saved.set(true);
        this.updated.emit();
      }
    } catch (error) {
      if (base === this.base()) this.error.set(nationalWriteError(error));
    } finally {
      this.busy.set(false);
    }
  }
}
