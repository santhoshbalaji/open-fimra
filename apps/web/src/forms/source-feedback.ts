import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, computed, effect, inject, input, signal } from '@angular/core';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { firstValueFrom } from 'rxjs';
import { AuthService } from '../auth';
import { translate } from '../language';
import { workspaceFormat } from '../workspace-format';
import type { Submission } from './form-api';

const statuses = {
  prepared: 'Prepared for the source',
  submitted: 'Submitted to the source',
  accepted: 'Accepted by the source',
  rejected: 'Rejected by the source'
} as const;

interface Feedback {
  id: string;
  verificationId: string;
  assessmentId: string | null;
  status: keyof typeof statuses;
  submittedAt: string | null;
  decidedAt: string | null;
  providerReceipt: string | null;
  payload: { areaHa?: number; remarks?: string };
}

interface FeedbackState {
  externalAlertRef: string | null;
  canPrepare: boolean;
  records: Feedback[];
}

@Component({
  selector: 'fimra-source-feedback',
  imports: [HlmButtonImports],
  template: `
    @if (data(); as state) {
      @if (state.externalAlertRef) {
        <h3>{{ msg('Source feedback') }}</h3>
        <p>{{ msg('External alert reference') }} <strong>{{ state.externalAlertRef }}</strong></p>
        @if (!state.records.length) {
          <p>{{ msg('No source feedback has been prepared.') }}</p>
        } @else {
          <ul class="report-list">
            @for (record of state.records; track record.id) {
              <li>
                <strong>{{ msg(statuses[record.status]) }}</strong>
                @if (record.submittedAt) { <small>{{ msg('Submitted on {0}', [date(record.submittedAt)]) }}</small> }
                @if (record.decidedAt) { <small>{{ msg('Decision received on {0}', [date(record.decidedAt)]) }}</small> }
                @if (record.providerReceipt) { <small>{{ msg('Source receipt {0}', [record.providerReceipt]) }}</small> }
                @if (record.payload.areaHa !== undefined) { <small>{{ msg('{0} ha attributable to this alert', [number(record.payload.areaHa)]) }}</small> }
                @if (record.payload.remarks) { <small>{{ record.payload.remarks }}</small> }
                @if (state.canPrepare && (record.status === 'prepared' || record.status === 'submitted')) {
                  <button hlmBtn size="sm" variant="outline" [disabled]="busy()"
                    (click)="prepare(record.verificationId, record.assessmentId)">{{ msg('Check feedback status') }}</button>
                }
              </li>
            }
          </ul>
        }
        @if (state.canPrepare && verifications().length) {
          <form class="grid gap-2" (submit)="$event.preventDefault(); prepare(verification.value, canIncludeArea() ? assessment.value || null : null)">
            <label for="feedback-verification">{{ msg('Approved flash update') }}</label>
            <select class="rounded border border-divider bg-card p-2" id="feedback-verification" #verification required (change)="seen.set(verification.value)">
              <option value="">{{ msg('Choose a flash update') }}</option>
              @for (report of verifications(); track report.id) {
                <option [value]="report.id">{{ msg('{0} · {1}', [date(report.receivedAt), msg(report.values['fire_seen'] === 'yes' ? 'Fire seen' : 'No fire seen')]) }}</option>
              }
            </select>
            <label for="feedback-assessment">{{ msg('Approved damage assessment') }}</label>
            <select class="rounded border border-divider bg-card p-2" id="feedback-assessment" #assessment [disabled]="!canIncludeArea()">
              <option value="">{{ msg('Leave out the area') }}</option>
              @if (canIncludeArea()) {
                @for (report of assessments(); track report.id) {
                  <option [value]="report.id">{{ msg('{0} ha · {1}', [number(report.values['alert_area_ha']), date(report.receivedAt)]) }}</option>
                }
              }
            </select>
            <button hlmBtn size="sm" type="submit" [disabled]="busy() || !seen()">{{ msg('Prepare source feedback') }}</button>
          </form>
        }
      }
    }
    @if (problem()) { <p role="status">{{ problem() }}</p> }
  `
})
export class SourceFeedback {
  readonly url = input.required<string>();
  readonly submissions = input.required<Submission[]>();
  protected readonly msg = translate;
  protected readonly statuses = statuses;
  protected readonly data = signal<FeedbackState | null>(null);
  protected readonly busy = signal(false);
  protected readonly problem = signal('');
  protected readonly seen = signal('');
  protected readonly verifications = computed(() => this.submissions().filter(s =>
    s.form === 'response-flash' && s.approval?.decision === 'approved' && ['yes', 'no'].includes(String(s.values['fire_seen']))));
  protected readonly assessments = computed(() => this.submissions().filter(s =>
    s.form === 'damage-assessment' && s.approval?.decision === 'approved' && typeof s.values['alert_area_ha'] === 'number'));
  protected readonly canIncludeArea = computed(() => this.verifications().some(s => s.id === this.seen() && s.values['fire_seen'] === 'yes'));
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly refresh = signal(0);
  protected date = (v: string) => workspaceFormat.dateTime(new Date(v));
  protected number = (v: unknown) => workspaceFormat.number(Number(v));

  constructor() {
    let previousUrl = '';
    effect(onCleanup => {
      const url = this.url();
      this.refresh();
      if (previousUrl !== url) {
        previousUrl = url;
        this.data.set(null);
        this.seen.set('');
        this.problem.set('');
      }
      const request = this.http.get<FeedbackState>(url).subscribe({
        next: state => this.data.set(state),
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          else this.problem.set(translate('Could not load source feedback.'));
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  protected async prepare(verificationId: string, assessmentId: string | null): Promise<void> {
    if (this.busy() || !verificationId) return;
    const url = this.url();
    this.busy.set(true);
    this.problem.set('');
    try {
      await firstValueFrom(this.http.post(url, { verificationId, assessmentId }));
    } catch (error) {
      if ((error as HttpErrorResponse).status === 401) this.auth.sessionEnded();
      else if (this.url() === url) this.problem.set(translate('Could not confirm source feedback. Try again.'));
    } finally {
      this.busy.set(false);
      this.refresh.update(n => n + 1);
    }
  }
}
