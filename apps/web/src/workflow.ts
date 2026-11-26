import { translate } from './language';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDropdownMenuImports } from '@spartan-ng/helm/dropdown-menu';
import { HlmCheckboxImports } from '@spartan-ng/helm/checkbox';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { Timeline, type TimelineEvent } from './ui/timeline';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { afterNextRender, Component, computed, DestroyRef, effect, ElementRef, inject, Injector, input, output, signal, viewChild } from '@angular/core';
import { RouterLink, type UrlTree } from '@angular/router';
import { ConfirmDialog } from './ui/confirm-dialog';
import { StateRegion, type DataState } from './ui/state-region';
import { LiveStatus } from './ui/live-status';
import { restoreOverlayFocus } from './ui/overlay-focus';
import { workspaceFormat } from './workspace-format';
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
const unchangedReasons = {
  already_at_step: 'Already at this step.',
  closed: 'Already closed.',
  different_step: 'This move is unavailable from the alert’s current step.',
  role: 'Your role cannot make this move on this alert.',
  report_required: 'A required report is missing.'
};
interface MemberOutcome {
  alertId: string;
  fromLabel: string;
  reason: keyof typeof unchangedReasons | 'transition_allowed';
}
interface IncidentImpact { alerts: MemberOutcome[] }
interface IncidentReceipt extends IncidentImpact { alertsMoved: number }

@Component({
  selector: 'fimra-workflow',
  imports: [HlmButtonImports, HlmCheckboxImports, HlmDropdownMenuImports, HlmFieldImports, HlmTextareaImports, RouterLink, StateRegion, Timeline, LiveStatus],
  templateUrl: './workflow.html'
})
export class Workflow {
  protected readonly msg = translate;
  readonly url = input.required<string>();
  readonly stepTime = input.required<string>();
  readonly now = input<number>();
  /** What the workflow belongs to, for its wording. */
  readonly subject = input<'alert' | 'incident'>('alert');
  readonly alertLink = input<(id: string) => UrlTree>();
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
  protected readonly preview = signal<IncidentImpact | 'error' | null>(null);
  protected readonly previewNotice = computed(() => {
    const preview = this.preview();
    if (!preview) return translate('Checking member alerts…');
    if (preview === 'error') return translate('Could not check member alerts.');
    const moving = preview.alerts.filter(alert => alert.reason === 'transition_allowed').length;
    return translate('{0} will move. {1} will stay at their current steps.', [moving, preview.alerts.length - moving]);
  });
  protected readonly receipt = signal<IncidentReceipt | null>(null);
  protected readonly unchangedReasons = unchangedReasons;
  private readonly previewAttempt = signal(0);
  protected readonly loadError = signal(false);
  protected readonly loading = signal(true);
  protected readonly historyState = computed<DataState>(() => {
    const incident = this.subject() === 'incident';
    if (this.loadError()) return { kind: 'error', keepContent: !!this.history(),
      heading: incident ? translate('Could not load incident actions and history') : translate('Could not load the alert timeline') };
    if (this.loading() && !this.history()) return { kind: 'loading',
      heading: incident ? translate('Loading incident actions and history') : translate('Loading alert timeline') };
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
      title: translate(entry.label),
      at: new Date(entry.at),
      milestone: index === 0 || index === all.length - 1 || milestones.has(entry.step),
      detail: [
        entry.actor || translate('Recorded automatically'),
        incident && entry.latitude != null && `${entry.latitude}, ${entry.longitude}`,
        entry.note,
        entry.reason && translate('Location exception: {0}', [entry.reason]),
        entry.reportPending && translate('Waiting for a report'),
        entry.alertsMoved ? translate('Moved {0} {1}', [workspaceFormat.number(entry.alertsMoved), translate(entry.alertsMoved === 1 ? 'alert' : 'alerts')]) : '',
        entry.incidentId && translate('Recorded on incident #{0}', [entry.incidentId])
      ].filter(Boolean).join('\n')
    })).reverse();
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
        this.receipt.set(null);
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
    effect(cleanup => {
      const step = this.chosen();
      const url = this.url();
      this.previewAttempt();
      this.preview.set(null);
      if (this.subject() !== 'incident' || !step) return;
      const sub = this.http.get<IncidentImpact>(url + '/history', { params: { preview: step } }).subscribe({
        next: impact => this.preview.set(impact),
        error: () => this.preview.set('error')
      });
      cleanup(() => sub.unsubscribe());
    });
  }

  reload(): void {
    this.version.update(n => n + 1);
  }

  protected retryPreview(): void {
    this.previewAttempt.update(n => n + 1);
    afterNextRender(() => this.formTitle()?.nativeElement.focus(), { injector: this.injector });
  }

  protected choose(move: Move, trigger: HTMLElement): void {
    if (this.busy()) return;
    this.trigger = trigger;
    if (this.chosen() !== move.step) {
      this.formTitle()?.nativeElement.closest('form')?.reset();
      this.preview.set(null);
      this.receipt.set(null);
      this.result.set(move.location_required && !move.missingReports?.length ? translate('Location required for this step.') : '');
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
    const preview = this.preview();
    const incident = this.subject() === 'incident';
    if (incident && (!preview || preview === 'error')) return;
    const url = this.url();
    const current = () => !this.lifetime.destroyed && this.url() === url;
    const exception = this.exception();
    this.busy.set(true);
    try {
      if (move.terminal && !await this.confirm.open({
        heading: translate('Confirm ') + translate(move.label),
        detail: translate('This ends the workflow for this ') + this.subject() + '.',
        confirmLabel: translate('Confirm ') + translate(move.label),
        destructive: true,
        trigger: submit
      })) return;
      if (!current()) return;

      let location;
      if (move.location_required && !exception) {
        this.result.set(translate('Getting your location…'));
        try {
          const position = await new Promise<GeolocationPosition>((resolve, reject) =>
            navigator.geolocation ? navigator.geolocation.getCurrentPosition(resolve, reject, { timeout: 15000 })
              : reject(new Error(translate('Location is unavailable.'))));
          location = { latitude: position.coords.latitude, longitude: position.coords.longitude };
        } catch (problem) {
          if (current()) this.result.set((problem as GeolocationPositionError)?.code === 1
            ? translate('Location permission denied. Allow location access and try again.')
            : translate('Location required. Your browser could not get your location. Try again.'));
          return;
        }
      }
      if (!current()) return;
      this.result.set(translate('Saving ') + translate(move.label) + '…');
      const receipt = await firstValueFrom(this.http.post<IncidentReceipt>(url + '/steps', { step: move.step, note, location,
        reason: exception ? reason.trim() : '', clientId: crypto.randomUUID() }));
      if (!current()) return;
      this.chosen.set(null);
      if (incident) this.receipt.set(receipt);
      this.result.set(translate('Saved: ') + translate(move.label) + '.');
      this.reload();
      this.changed.emit();
      this.focusResult();
    } catch (error) {
      if (!current()) return;
      const reason = error instanceof HttpErrorResponse && typeof error.error?.error === 'string' ? error.error.error :
        translate('Check the timeline before trying again.');
      this.result.set(error instanceof HttpErrorResponse && error.status === 403 ?
        translate('Permission denied. ') + reason : translate('Step not saved. ') + reason);
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
