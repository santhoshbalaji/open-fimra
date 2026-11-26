import { language, translate } from '../language';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { RouterLink } from '@angular/router';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, effect, inject, input, output, signal, viewChild } from '@angular/core';

import { firstValueFrom } from 'rxjs';
import { AuthService } from '../auth';
import type { AlertRow } from '../alert-queue';
import { placesOf } from '../detections';
import { ShellNavigation } from '../shell-navigation';
import { LiveStatus } from '../ui/live-status';
import { ConfirmDialog } from '../ui/confirm-dialog';
import { StateRegion, type DataState } from '../ui/state-region';
import { Timeline, type TimelineEvent } from '../ui/timeline';
import { StatusBadge } from '../ui/status-badge';
import { workspaceFormat } from '../workspace-format';
import { decisionLabels, serverProblems, type FormVersion, type Submission } from './form-api';
import { textIn } from './form-text';
import { FormPresentation } from './form-presentation';

/**
 * One field report, shown with the form version it was filled in on, and the
 * officer's decision. A supervising officer on the alert can approve it or
 * return it with a note. Decisions never change.
 */
@Component({
  selector: 'fimra-submission-view',
  imports: [HlmButtonImports, HlmFieldImports, HlmTextareaImports, LiveStatus, RouterLink, StatusBadge, StateRegion, FormPresentation, Timeline],
  host: { class: 'submission-view' },
  template: `
    <fimra-live-status class="sr-only" [message]="announcement()" />
    <fimra-state-region [state]="reportState()" (retry)="reloadReport()">
      @if (submission(); as s) {
        <header class="submission-header">
          @if (showTitle()) { <h3>{{ title() }}</h3> }
          <div class="submission-context">
            <p class="font-medium wrap-anywhere">{{ place() }}</p>
            <div #decisionResult tabindex="-1" class="flex flex-wrap items-center gap-2">
              <fimra-status-badge [icon]="s.approval?.decision === 'approved' ? 'complete' : s.approval ? 'overdue' : 'pending'"
                [label]="labels[s.approval?.decision ?? 'pending']" />
            </div>
          </div>
          <dl class="submission-facts">
            <div><dt>{{ msg('Submitted by') }}</dt><dd>{{ s.submittedBy }}</dd></div>
            <div><dt>{{ msg('Related alert') }}</dt><dd>
              @if (navigation) {
                <a class="report-alert-link" [routerLink]="navigation.href('/monitor/alerts', { alert: s.alertId })">{{ msg('Alert #{0}', [s.alertId]) }}</a>
              } @else { {{ msg('Alert #{0}', [s.alertId]) }} }
            </dd></div>
            @if (stage() && stage() !== title()) {
              <div><dt>{{ msg('Stage') }}</dt><dd>{{ stage() }}</dd></div>
            }
            <div><dt>{{ msg('Received') }}</dt><dd><time [attr.datetime]="s.receivedAt">{{ time(s.receivedAt) }}</time></dd></div>
            @if (s.canApprove && reviewerRole()) {
              <div><dt>{{ msg('Your review role') }}</dt><dd>{{ reviewerRole() }}</dd></div>
            }
            <div class="submission-meta"><dt class="sr-only">{{ msg('Form version') }}</dt><dd>{{ msg('Version {0}', [s.version]) }}</dd></div>
          </dl>
        </header>

        <div class="submission-evidence">
          <fimra-state-region class="block" [state]="formState()" (retry)="retryForm()">
            @if (definition(); as form) {
              <fimra-form-presentation mode="historical" [definition]="form" [step]="s.step"
                [values]="s.values" [workspaceUrl]="workspaceUrl()" [showHeader]="false" />
            }
          </fimra-state-region>

          <section [attr.aria-label]=" msg('Report timeline') " class="grid gap-2 border-t border-divider pt-3">
            <h3 class="font-semibold">{{ msg('Timeline') }}</h3>
            <fimra-timeline [events]="timeline()" [now]="nowDate()" />
          </section>
        </div>

        @if (!s.approval && s.canApprove && definition()) {
          <section class="approval bg-card" [attr.aria-label]=" msg('Review actions') ">
            @if (message()) { <p role="alert" class="report-refused">{{ message() }}</p> }
            @if (returning()) {
              <form novalidate (submit)="$event.preventDefault(); decide('returned', note.value)">
                <hlm-field>
                  <label hlmFieldLabel for="return-note">{{ msg('Why is the report returned?') }}</label>
                  <textarea #note hlmTextarea id="return-note" maxlength="2000" required [readOnly]="busy()" [forceInvalid]="!!noteError()"
                    (input)="noteError.set('')"></textarea>
                  <p hlmFieldDescription id="return-hint">{{ msg('The alert will need a new report at this step.') }}</p>
                  @if (noteError()) { <hlm-field-error id="return-error" forceShow>{{ noteError() }}</hlm-field-error> }
                </hlm-field>
                <div class="form-actions">
                  <button hlmBtn type="submit" [busy]="busy()">{{ msg('Return the report') }}</button>
                  <button hlmBtn type="button" variant="ghost" [disabled]="busy()" (click)="cancelReturn()">{{ msg('Cancel') }}</button>
                </div>
              </form>
            } @else {
              <div class="form-actions">
                <button #approve hlmBtn type="button" [busy]="busy()" (click)="approveReport(approve)">{{ msg('Approve') }}</button>
                <button #returnTrigger hlmBtn type="button" variant="outline" [disabled]="busy()" (click)="startReturn()">{{ msg('Return with a note') }}</button>
              </div>
            }
          </section>
        } @else if (s.approval && nextPendingId()) {
          <div class="approval bg-card">
            @if (message()) { <p role="alert" class="report-refused">{{ message() }}</p> }
            <button hlmBtn type="button" variant="outline" class="justify-self-start" (click)="next.emit()">{{ msg('Next pending report') }}</button>
          </div>
        } @else if (message()) {
          <p role="alert" class="report-refused">{{ message() }}</p>
        }
      }
    </fimra-state-region>
  `
})
export class SubmissionView {
  protected readonly msg = translate;
  readonly workspaceUrl = input.required<string>();
  readonly submissionId = input.required<string>();
  readonly showTitle = input(true);
  readonly stage = input('');
  readonly reviewerRole = input('');
  readonly nextPendingId = input<string>();
  readonly next = output<void>();
  readonly now = input<number>();
  readonly decided = output<Submission>();
  readonly submission = signal<Submission | null>(null);
  readonly message = signal('');
  protected readonly reportState = signal<DataState>({ kind: 'loading', heading: 'Loading the report' });
  protected readonly announcement = signal('');
  protected readonly noteError = signal('');
  protected readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly noteInput = viewChild<ElementRef<HTMLTextAreaElement>>('note');
  private readonly returnTrigger = viewChild<ElementRef<HTMLButtonElement>>('returnTrigger');
  private readonly reportAttempt = signal(0);
  readonly busy = signal(false);
  readonly returning = signal(false);
  readonly labels = decisionLabels;
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly confirmation = inject(ConfirmDialog);
  private readonly lifetime = inject(DestroyRef);
  private confirming = false;
  private readonly injector = inject(Injector);
  private readonly decisionResult = viewChild<ElementRef<HTMLElement>>('decisionResult');
  protected readonly definition = signal<FormVersion['definition'] | null>(null);
  protected readonly formState = signal<DataState>({ kind: 'loading', heading: 'Loading the form version' });
  readonly title = computed(() => textIn(this.definition()?.['title'], language()) || this.submission()?.form);
  protected readonly place = signal('Unknown place');
  protected readonly nowDate = computed(() => new Date(this.now() ?? Date.now()));
  protected readonly timeline = computed<TimelineEvent[]>(() => {
    const report = this.submission();
    if (!report) return [];
    const events: TimelineEvent[] = [{
      id: 'submitted', title: 'Submitted by ' + report.submittedBy, at: new Date(report.receivedAt), milestone: true,
      detail: report.deviceTime ? 'Saved on the device ' + this.time(report.deviceTime) : undefined
    }];
    const approval = report.approval;
    if (approval) events.push({
      id: 'decision', title: this.labels[approval.decision] + ' by ' + approval.by,
      at: new Date(approval.at), milestone: true, detail: approval.note
    });
    return events;
  });
  private readonly alertId = computed(() => this.submission()?.alertId);
  private readonly formAttempt = signal(0);
  private readonly formUrl = computed(() => {
    const report = this.submission();
    return report ? this.workspaceUrl() + '/forms/' + encodeURIComponent(report.form) + '/versions/' + report.version : null;
  });

  constructor() {
    effect(onCleanup => {
      const url = this.workspaceUrl() + '/submissions/' + this.submissionId();
      this.reportAttempt();
      this.submission.set(null);
      this.reportState.set({ kind: 'loading', heading: 'Loading the report' });
      this.message.set('');
      this.announcement.set('');
      this.noteError.set('');
      this.returning.set(false);
      const request = this.http.get<Submission>(url).subscribe({
        next: s => {
          this.submission.set(s);
          this.reportState.set({ kind: 'ready' });
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.reportState.set(error.status === 403 || error.status === 404 ?
            { kind: 'forbidden', heading: 'This report is unavailable' } : { kind: 'error', heading: 'The report could not load' });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(onCleanup => {
      const id = this.alertId();
      this.place.set(translate('Unknown place'));
      if (!id) return;
      const request = this.http.get<AlertRow>(this.workspaceUrl() + '/alerts/' + id).subscribe({
        next: alert => {
          const names = placesOf(alert, []).map(place => place.name || place.code).filter(Boolean);
          this.place.set(names.join(' · ') ||
            [alert.beat_name, alert.range_name, alert.division_name].filter(Boolean).join(' · ') || translate('Unknown place'));
        },
        error: (error: HttpErrorResponse) => { if (error.status === 401) this.auth.sessionEnded(); }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(onCleanup => {
      const url = this.formUrl();
      this.formAttempt();
      this.definition.set(null);
      this.formState.set({ kind: 'loading', heading: 'Loading the form version' });
      if (!url) return;
      const request = this.http.get<FormVersion>(url).subscribe({
        next: version => {
          this.definition.set(version.definition);
          this.formState.set({ kind: 'ready' });
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.formState.set(error.status === 403 || error.status === 404
            ? { kind: 'forbidden', heading: 'This form version is unavailable' }
            : { kind: 'error', heading: 'The form version could not load' });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  protected reloadReport(): void {
    this.reportAttempt.update(attempt => attempt + 1);
  }

  protected startReturn(): void {
    this.message.set('');
    this.returning.set(true);
    afterNextRender(() => this.noteInput()?.nativeElement.focus(), { injector: this.injector });
  }

  protected cancelReturn(): void {
    this.noteError.set('');
    this.message.set('');
    this.returning.set(false);
    afterNextRender(() => this.returnTrigger()?.nativeElement.focus(), { injector: this.injector });
  }

  protected retryForm(): void {
    this.formAttempt.update(attempt => attempt + 1);
  }

  time(value: string): string {
    return workspaceFormat.dateTime(new Date(value));
  }

  async approveReport(trigger: HTMLButtonElement): Promise<void> {
    const report = this.submission();
    if (!report?.canApprove || report.approval || this.confirming || this.busy()) return;
    this.confirming = true;
    try {
      const confirmed = await this.confirmation.open({
        heading: 'Approve this report?',
        detail: 'This decision cannot be changed later.',
        confirmLabel: 'Approve report',
        trigger
      });
      if (confirmed && !this.lifetime.destroyed && this.submission() === report) await this.decide('approved', '');
    } finally {
      this.confirming = false;
    }
  }

  async decide(decision: 'approved' | 'returned', note: string): Promise<void> {
    const current = this.submission();
    if (!current?.canApprove || current.approval || this.busy()) return;
    if (decision === 'returned' && !note.trim()) {
      this.noteError.set('Say why the report is returned.');
      this.noteInput()?.nativeElement.focus();
      return;
    }
    this.busy.set(true);
    this.announcement.set('Saving decision…');
    this.message.set('');
    this.noteError.set('');
    const url = this.workspaceUrl() + '/submissions/' + current.id;
    try {
      const saved = await firstValueFrom(this.http.post<Submission>(url + '/approval',
        { decision, note: note.trim() }));
      if (this.lifetime.destroyed || this.submission() !== current) return;
      this.submission.set(saved);
      this.returning.set(false);
      this.announcement.set(decision === 'approved' ? 'Report approved.' : 'Report returned.');
      this.decided.emit(saved);
      this.focusDecision();
    } catch (error) {
      const failed = error as HttpErrorResponse;
      if (failed.status === 401) { this.auth.sessionEnded(); return; }
      if (this.lifetime.destroyed || this.submission() !== current) return;
      this.announcement.set('');
      this.message.set(serverProblems(failed).message);
      if (failed.status === 409) {
        // Another officer decided first. Keep the server's message and show the saved decision.
        try {
          const decided = await firstValueFrom(this.http.get<Submission>(url));
          if (!this.lifetime.destroyed && this.submission() === current) {
            this.submission.set(decided);
            this.returning.set(false);
            this.decided.emit(decided);
            this.focusDecision();
          }
        } catch (error) {
          if ((error as HttpErrorResponse).status === 401) this.auth.sessionEnded();
        }
      }
    } finally {
      this.busy.set(false);
    }
  }

  private focusDecision(): void {
    afterNextRender(() => this.decisionResult()?.nativeElement.focus(), { injector: this.injector });
  }
}
