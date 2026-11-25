import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, effect, inject, input, output, signal } from '@angular/core';
import { toast } from '@spartan-ng/brain/sonner';
import { firstValueFrom } from 'rxjs';
import { AuthService } from '../auth';
import { StatusBadge } from '../ui/status-badge';
import { decisionLabels, serverProblems, type Submission } from './form-api';
import { FormFill } from './form-fill';

const when = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

/**
 * One field report, shown with the form version it was filled in on, and the
 * officer's decision. A supervising officer on the alert can approve it or
 * return it with a note. Decisions never change.
 */
@Component({
  selector: 'fimra-submission-view',
  imports: [HlmButtonImports, HlmFieldImports, HlmSkeletonImports, HlmTextareaImports, StatusBadge, FormFill],
  host: { class: 'submission-view' },
  template: `
    @if (submission(); as s) {
      <p class="submission-meta">Version {{ s.version }} · sent by {{ s.submittedBy }} · received {{ time(s.receivedAt) }}
        @if (s.deviceTime) { · saved on the device {{ time(s.deviceTime) }} }
        · alert #{{ s.alertId }}</p>
      <section class="approval" aria-labelledby="approval-title">
        <h4 id="approval-title">Decision</h4>
        @if (s.approval; as a) {
          <p><fimra-status-badge [icon]="a.decision === 'approved' ? 'complete' : 'overdue'" [label]="labels[a.decision]" />
            by {{ a.by }}, {{ time(a.at) }}</p>
          @if (a.note) { <p class="approval-note">{{ a.note }}</p> }
        } @else if (s.canApprove) {
          @if (returning()) {
            <form novalidate (submit)="$event.preventDefault(); decide('returned', note.value)">
              <hlm-field><label hlmFieldLabel for="return-note">Why is the report returned?</label>
                <textarea hlmTextarea id="return-note" #note maxlength="2000" required></textarea>
              </hlm-field>
              <p class="form-hint">The report no longer counts, so the alert waits for a new one at its step.</p>
              <div class="form-actions">
                <button hlmBtn type="submit" [disabled]="busy()">Return the report</button>
                <button hlmBtn type="button" variant="ghost" [disabled]="busy()" (click)="returning.set(false)">Cancel</button>
              </div>
            </form>
          } @else {
            <p>{{ labels.pending }}. A decision cannot be changed later.</p>
            <div class="form-actions">
              <button hlmBtn type="button" [disabled]="busy()" (click)="decide('approved', '')">Approve</button>
              <button hlmBtn type="button" variant="outline" [disabled]="busy()" (click)="returning.set(true)">Return with a note</button>
            </div>
          }
        } @else {
          <p>{{ labels.pending }} from a supervising officer.</p>
        }
        @if (message()) { <p role="alert">{{ message() }}</p> }
      </section>
      <fimra-form-fill [workspaceUrl]="workspaceUrl()" [form]="s.form" [version]="s.version" [step]="s.step"
        [alertId]="s.alertId" [initial]="s.values" [readonly]="true" />
    } @else if (message()) {
      <p role="status">{{ message() }}</p>
    } @else {
      <div role="status" aria-label="Loading the report" class="grid gap-2"><hlm-skeleton class="h-10" /><hlm-skeleton class="h-24" /></div>
    }
  `
})
export class SubmissionView {
  readonly workspaceUrl = input.required<string>();
  readonly submissionId = input.required<string>();
  readonly decided = output<Submission>();
  readonly submission = signal<Submission | null>(null);
  readonly message = signal('');
  readonly busy = signal(false);
  readonly returning = signal(false);
  readonly labels = decisionLabels;
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);

  constructor() {
    effect(onCleanup => {
      const url = this.workspaceUrl() + '/submissions/' + this.submissionId();
      this.submission.set(null);
      this.message.set('');
      this.returning.set(false);
      const request = this.http.get<Submission>(url).subscribe({
        next: s => this.submission.set(s),
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.message.set(error.status === 404 ? 'You cannot see this report.' : 'The report could not load. Try again.');
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  time(value: string): string {
    return when.format(new Date(value));
  }

  async decide(decision: 'approved' | 'returned', note: string): Promise<void> {
    const current = this.submission();
    if (!current || this.busy()) return;
    if (decision === 'returned' && !note.trim()) {
      this.message.set('Say why the report is returned.');
      return;
    }
    this.busy.set(true);
    this.message.set('');
    try {
      const saved = await firstValueFrom(this.http.post<Submission>(this.workspaceUrl() + '/submissions/' + current.id + '/approval',
        { decision, note: note.trim() }));
      this.submission.set(saved);
      this.returning.set(false);
      toast.success(decision === 'approved' ? 'Report approved.' : 'Report returned.');
      this.decided.emit(saved);
    } catch (error) {
      const failed = error as HttpErrorResponse;
      if (failed.status === 401) { this.auth.sessionEnded(); return; }
      this.message.set(failed.status === 403 ? 'Only a supervising officer on this alert can decide.' : serverProblems(failed).message);
      if (failed.status === 409) {
        // Someone else decided first. Show their decision.
        this.http.get<Submission>(this.workspaceUrl() + '/submissions/' + current.id).subscribe({ next: s => this.submission.set(s) });
      }
    } finally {
      this.busy.set(false);
    }
  }
}
