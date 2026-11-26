import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDropdownMenuImports } from '@spartan-ng/helm/dropdown-menu';
import { HlmCheckboxImports } from '@spartan-ng/helm/checkbox';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { Timeline, type TimelineEvent } from './ui/timeline';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { afterNextRender, Component, computed, DestroyRef, effect, ElementRef, inject, Injector, input, output, signal, viewChild } from '@angular/core';
import { ConfirmDialog } from './ui/confirm-dialog';
import { StateRegion, type DataState } from './ui/state-region';
import { LiveStatus } from './ui/live-status';
import { restoreOverlayFocus } from './ui/overlay-focus';
import { defaultWorkspaceFormat } from './workspace-format';
import { firstValueFrom } from 'rxjs';

const milestones = new Set(['acknowledged', 'reached_site', 'controlled', 'extinguished', 'closed']);

interface Move { step: string; label: string; location_required: boolean; can_override: boolean; terminal: boolean; missingReports?: string[] }
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
  imports: [HlmButtonImports, HlmCheckboxImports, HlmDropdownMenuImports, HlmFieldImports, HlmTextareaImports, StateRegion, Timeline, LiveStatus],
  templateUrl: './workflow.html'
})
export class Workflow {
  readonly url = input.required<string>();
  readonly stepTime = input.required<string>();
  readonly now = input<number>();
  /** What the workflow belongs to, for its wording. */
  readonly subject = input<'alert' | 'incident'>('alert');
  readonly changed = output<void>();
  readonly reportNeeded = output<string>();
  readonly history = signal<History | null>(null);
  protected readonly steps = computed(() => this.history()?.moves.filter(move => !move.terminal) ?? []);
  protected readonly outcomes = computed(() => this.history()?.moves.filter(move => move.terminal) ?? []);
  private readonly chosen = signal<string | null>(null);
  protected readonly move = computed(() => this.history()?.moves.find(move => move.step === this.chosen()));
  protected readonly pendingReport = computed(() => this.move()?.missingReports?.[0]);
  protected readonly busy = signal(false);
  protected readonly result = signal('');
  protected readonly loadError = signal(false);
  protected readonly loading = signal(true);
  protected readonly historyState = computed<DataState>(() => {
    const incident = this.subject() === 'incident';
    if (this.loadError()) return { kind: 'error', keepContent: !!this.history(),
      heading: incident ? 'Could not load incident actions and history' : 'Could not load the alert timeline' };
    if (this.loading() && !this.history()) return { kind: 'loading',
      heading: incident ? 'Loading incident actions and history' : 'Loading alert timeline' };
    return { kind: 'ready' };
  });
  protected readonly exception = signal(false);
  private readonly version = signal(0);
  protected readonly formTitle = viewChild<ElementRef<HTMLElement>>('formTitle');
  private readonly resultTarget = viewChild<ElementRef<HTMLElement>>('resultTarget');
  private readonly http = inject(HttpClient);
  private readonly lifetime = inject(DestroyRef);
  private readonly injector = inject(Injector);
  private readonly confirm = inject(ConfirmDialog);
  private trigger?: HTMLElement;
  protected readonly nowDate = computed(() => new Date(this.now() ?? Date.now()));
  readonly timeline = computed<TimelineEvent[]>(() => {
    const incident = this.subject() === 'incident';
    return (this.history()?.entries ?? []).map((entry, index, all) => ({
      id: `${entry.at}/${index}`,
      title: entry.label,
      at: new Date(entry.at),
      milestone: !incident || index === 0 || index === all.length - 1 || milestones.has(entry.step),
      detail: [
        entry.actor || 'Recorded automatically',
        incident && entry.latitude != null && `${entry.latitude}, ${entry.longitude}`,
        entry.note,
        entry.reason && `Location exception: ${entry.reason}`,
        entry.reportPending && 'Report pending',
        entry.alertsMoved ? `Moved ${defaultWorkspaceFormat.number(entry.alertsMoved)} ${entry.alertsMoved === 1 ? 'alert' : 'alerts'}` : '',
        entry.incidentId && `Recorded on incident #${entry.incidentId}`
      ].filter(Boolean).join('\n')
    }));
  });

  constructor() {
    let previousUrl = '';
    effect(cleanup => {
      const url = this.url();
      this.stepTime();
      this.version();
      if (previousUrl !== url) {
        this.history.set(null);
        this.chosen.set(null);
        this.result.set('');
        this.trigger = undefined;
        previousUrl = url;
      }
      this.loading.set(true);
      this.loadError.set(false);
      const sub = this.http.get<History>(url + '/history').subscribe({
        next: history => {
          this.history.set(history);
          this.loading.set(false);
        },
        error: () => {
          this.loading.set(false);
          this.loadError.set(true);
        }
      });
      cleanup(() => sub.unsubscribe());
    });
  }

  reload(): void {
    this.version.update(n => n + 1);
  }

  protected choose(move: Move, trigger: HTMLElement): void {
    if (this.busy()) return;
    this.trigger = trigger;
    if (this.chosen() !== move.step) {
      this.formTitle()?.nativeElement.closest('form')?.reset();
      this.result.set(move.location_required && !move.missingReports?.length ? 'Location required for this step.' : '');
      this.exception.set(false);
      this.chosen.set(move.step);
    }
    afterNextRender(() => this.formTitle()?.nativeElement.focus(), { injector: this.injector });
  }

  protected cancel(event?: Event): void {
    event?.stopPropagation();
    if (this.busy()) return;
    this.chosen.set(null);
    this.result.set('');
    afterNextRender(() => restoreOverlayFocus(this.trigger ?? null, this.resultTarget()?.nativeElement), { injector: this.injector });
  }

  protected async save(note: string, reason: string, submit: HTMLElement): Promise<void> {
    const move = this.move();
    if (!move || this.busy() || this.loading() || this.loadError() || this.pendingReport()) return;
    const url = this.url();
    const current = () => !this.lifetime.destroyed && this.url() === url;
    const exception = this.exception();
    this.busy.set(true);
    try {
      if (move.terminal && !await this.confirm.open({
        heading: 'Confirm ' + move.label,
        detail: 'This ends the workflow for this ' + this.subject() + '.',
        confirmLabel: 'Confirm ' + move.label,
        destructive: true,
        trigger: submit
      })) return;
      if (!current()) return;

      let location;
      if (move.location_required && !exception) {
        this.result.set('Getting your location…');
        try {
          const position = await new Promise<GeolocationPosition>((resolve, reject) =>
            navigator.geolocation ? navigator.geolocation.getCurrentPosition(resolve, reject, { timeout: 15000 })
              : reject(new Error('Location is unavailable.')));
          location = { latitude: position.coords.latitude, longitude: position.coords.longitude };
        } catch (problem) {
          if (current()) this.result.set((problem as GeolocationPositionError)?.code === 1
            ? 'Location permission denied. Allow location access and try again.'
            : 'Location required. Your browser could not get your location. Try again.');
          return;
        }
      }
      if (!current()) return;
      this.result.set('Saving ' + move.label + '…');
      await firstValueFrom(this.http.post(url + '/steps', { step: move.step, note, location,
        reason: exception ? reason.trim() : '', clientId: crypto.randomUUID() }));
      if (!current()) return;
      this.chosen.set(null);
      this.result.set('Saved: ' + move.label + '.');
      this.reload();
      this.changed.emit();
      this.focusResult();
    } catch (error) {
      if (!current()) return;
      const reason = error instanceof HttpErrorResponse && typeof error.error?.error === 'string' ? error.error.error :
        'Check the timeline before trying again.';
      this.result.set(error instanceof HttpErrorResponse && error.status === 403 ?
        'Permission denied. ' + reason : 'Step not saved. ' + reason);
      if (error instanceof HttpErrorResponse && (error.status === 409 || error.status === 422)) {
        this.reload();
        if (error.status === 409) {
          this.chosen.set(null);
          this.focusResult();
        }
      }
    } finally {
      this.busy.set(false);
    }
  }

  private focusResult(): void {
    afterNextRender(() => this.resultTarget()?.nativeElement.focus(), { injector: this.injector });
  }
}
