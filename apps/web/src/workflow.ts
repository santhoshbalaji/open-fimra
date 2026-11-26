import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDropdownMenuImports } from '@spartan-ng/helm/dropdown-menu';
import { HlmCheckboxImports } from '@spartan-ng/helm/checkbox';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { StatusBadge } from './ui/status-badge';
import { StateRegion, type DataState } from './ui/state-region';
import { DatePipe, NgTemplateOutlet } from '@angular/common';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { afterNextRender, Component, computed, DestroyRef, effect, ElementRef, inject, Injector, input, output, signal, viewChild } from '@angular/core';
import { toast } from '@spartan-ng/brain/sonner';
import { firstValueFrom } from 'rxjs';

interface Move { step: string; label: string; location_required: boolean; can_override: boolean; terminal: boolean }
export interface WorkflowEntry {
  step: string; label: string; at: string; actor: string | null; note: string; reason: string;
  latitude: number | null; longitude: number | null; reportPending: boolean;
  /** Incident entries: how many of the member alerts you can see moved with this step. */
  alertsMoved?: number;
  /** Alert entries: the incident whose step moved this alert. */
  incidentId?: string | null;
}
interface History { entries: WorkflowEntry[] | null; moves: Move[]; mergedInto?: string | null }

@Component({
  selector: 'fimra-workflow',
  imports: [HlmButtonImports, HlmCheckboxImports, HlmDropdownMenuImports, HlmFieldImports, HlmTextareaImports, HlmSkeletonImports, StatusBadge, DatePipe, NgTemplateOutlet, StateRegion],
  templateUrl: './workflow.html'
})
export class Workflow {
  readonly url = input.required<string>();
  readonly stepTime = input.required<string>();
  /** What the workflow belongs to, for its wording. */
  readonly subject = input<'alert' | 'incident'>('alert');
  readonly changed = output<void>();
  readonly history = signal<History | null>(null);
  readonly steps = computed(() => this.history()?.moves.filter(move => !move.terminal) ?? []);
  readonly outcomes = computed(() => this.history()?.moves.filter(move => move.terminal) ?? []);
  readonly chosen = signal<Move | null>(null);
  readonly busy = signal(false);
  readonly message = signal('');
  readonly exception = signal(false);
  readonly reload = signal(0);
  readonly historyState = signal<DataState>({ kind: 'loading', heading: 'Loading incident actions and history' });
  private readonly injector = inject(Injector);
  private readonly formTitle = viewChild<ElementRef<HTMLElement>>('formTitle');
  private readonly nextStep = viewChild<ElementRef<HTMLElement>>('nextStep');
  private readonly http = inject(HttpClient);
  private readonly lifetime = inject(DestroyRef);

  constructor() {
    effect(cleanup => {
      const url = this.url();
      this.stepTime();
      this.reload();
      this.historyState.set({ kind: 'loading', heading: 'Loading incident actions and history' });
      this.history.set(null);
      this.chosen.set(null);
      const sub = this.http.get<History>(url + '/history').subscribe({
        next: history => {
          this.history.set(history);
          this.historyState.set({ kind: 'ready' });
        },
        error: () => {
          if (this.subject() === 'incident') this.historyState.set({ kind: 'error', heading: 'Could not load incident actions and history' });
          else this.message.set('Could not load the timeline.');
        }
      });
      cleanup(() => sub.unsubscribe());
    });
  }
  choose(move: Move | null) {
    this.message.set('');
    this.chosen.set(move);
    this.exception.set(false);
    if (this.subject() === 'incident') {
      afterNextRender(() => (move ? this.formTitle() : this.nextStep())?.nativeElement.focus(), { injector: this.injector });
    }
  }
  retryHistory() {
    this.reload.update(n => n + 1);
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
      toast.success(move.label + ' recorded.');
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
