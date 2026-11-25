import { DatePipe } from '@angular/common';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, effect, inject, input, output, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';

interface Move { step: string; label: string; location_required: boolean; can_override: boolean; terminal: boolean }
interface Entry {
  step: string; label: string; at: string; actor: string | null; note: string; reason: string;
  latitude: number | null; longitude: number | null; reportPending: boolean;
}
interface History { entries: Entry[]; moves: Move[] }

@Component({
  selector: 'fimra-workflow',
  imports: [DatePipe],
  templateUrl: './workflow.html'
})
export class Workflow {
  readonly url = input.required<string>();
  readonly stepTime = input.required<string>();
  readonly changed = output<void>();
  readonly history = signal<History | null>(null);
  readonly chosen = signal<Move | null>(null);
  readonly busy = signal(false);
  readonly message = signal('');
  readonly exception = signal(false);
  readonly reload = signal(0);
  private readonly http = inject(HttpClient);
  private readonly lifetime = inject(DestroyRef);

  constructor() {
    effect(cleanup => {
      const url = this.url();
      this.stepTime();
      this.reload();
      this.history.set(null);
      this.chosen.set(null);
      const sub = this.http.get<History>(url + '/history').subscribe({
        next: history => this.history.set(history),
        error: () => this.message.set('Could not load the timeline.')
      });
      cleanup(() => sub.unsubscribe());
    });
  }
  choose(move: Move | null) {
    this.message.set('');
    this.chosen.set(move);
    this.exception.set(false);
  }
  async save(note: string, reason: string) {
    const move = this.chosen();
    if (!move || this.busy()) return;
    const url = this.url();
    this.busy.set(true);
    this.message.set('');
    try {
      let location;
      if (move.location_required && !this.exception()) {
        const position = await new Promise<GeolocationPosition>((resolve, reject) =>
          navigator.geolocation ? navigator.geolocation.getCurrentPosition(resolve, reject, { timeout: 15000 })
            : reject(new Error('Location is unavailable.')));
        location = { latitude: position.coords.latitude, longitude: position.coords.longitude };
      }
      if (this.lifetime.destroyed || this.url() !== url || this.chosen() !== move) return;
      await firstValueFrom(this.http.post(url + '/steps', { step: move.step, note, location,
        reason: this.exception() ? reason.trim() : '', clientId: crypto.randomUUID() }));
      if (this.lifetime.destroyed || this.url() !== url) return;
      this.choose(null);
      this.message.set(move.label + ' recorded.');
      this.reload.update(n => n + 1);
      this.changed.emit();
    } catch (error) {
      if (this.lifetime.destroyed || this.url() !== url) return;
      if (error instanceof HttpErrorResponse) {
        this.message.set(error.error?.error || 'Could not save the step. Check the timeline before trying again.');
        if (error.status === 409) this.reload.update(n => n + 1);
      } else this.message.set('Could not get your location. Allow location access and try again.');
    } finally { this.busy.set(false); }
  }
}
