import { translate } from '../language';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ViewEncapsulation, computed, effect, inject, input, output, signal } from '@angular/core';
import { forkJoin } from 'rxjs';
import type { AlertRow } from '../alert-queue';
import { AuthService } from '../auth';
import { StatusBadge } from '../ui/status-badge';
import { SidePanel } from '../ui/side-panel';
import { workspaceFormat } from '../workspace-format';
import { decisionLabels, type AssignedForm, type Submission } from './form-api';
import { FormFill } from './form-fill';
import { SubmissionView } from './submission-view';
import { SourceFeedback } from './source-feedback';

interface History { entries: { step: string; label: string }[]; moves: { step: string; label: string }[] }
interface Draft { form: AssignedForm; initial: Record<string, unknown>; provisionalSubmissionId?: string }

/**
 * The report forms on an alert's workflow, and the reports already sent.
 * Filling in and reading a report open in a dialog, which loads only when
 * it is first opened.
 */
@Component({
  selector: 'fimra-alert-reports',
  imports: [HlmButtonImports, HlmDialogImports, HlmSkeletonImports, SidePanel, StatusBadge, FormFill, SubmissionView, SourceFeedback],
  host: { class: 'alert-reports' },
  styleUrl: './forms.css',
  encapsulation: ViewEncapsulation.None,
  template: `
    @if (forms(); as list) {
      @if (list.length || submissions().length) {
        <h3>{{ msg('Reports') }}</h3>
        @if (list.length) {
          <ul class="report-forms">
            @for (f of list; track f.form + f.step) {
              <li>
                <span><strong>{{ msg(f.title) }}</strong> <small>{{ msg('at {0}', [stepLabel(f.step)]) }}</small></span>
                @if (f.requiredNow) { <fimra-status-badge icon="overdue" [label]=" msg('Needed before the next step') " /> }
                @else if (f.submitted) { <fimra-status-badge icon="complete" [label]=" msg('Sent') " /> }
                @if (canFill(f)) {
                  <button hlmBtn size="sm" [variant]="f.requiredNow ? 'primary' : 'outline'" (click)="fill(f)">{{ msg('Start {0}', [msg(f.title)]) }}</button>
                }
              </li>
            }
          </ul>
        }
        @if (submissions().length) {
          <ul class="report-list" [attr.aria-label]=" msg('Reports sent for this alert') ">
            @for (s of submissions(); track s.id) {
              <li>
                <button #report hlmBtn variant="link" size="sm" (click)="selected.set(s.id); review.show(report)">{{ msg('{0}, version {1}', [titleOf(s), s.version]) }}</button>
                @if (assessmentForm(); as assessment) {
                  @if (s.form === assessment.form && !s.provisionalAssessment) {
                    <button hlmBtn size="sm" variant="outline" (click)="revise(assessment, s)">{{ msg('Revise this assessment') }}</button>
                  }
                }
                <small>{{ s.submittedBy }} · {{ time(s.receivedAt) }}</small>
                <fimra-status-badge [icon]="s.approval?.decision === 'approved' ? 'complete' : s.approval ? 'overdue' : 'pending'"
                  [label]="labels[s.approval?.decision ?? 'pending']" />
              </li>
            }
          </ul>
        }
      }
    } @else if (message()) {
      <p role="status">{{ message() }}</p>
    }
    @if (alert().source === 'fsi' || alert().source === 'demo_fsi') {
      @defer (on immediate) {
        <fimra-source-feedback [url]="alertUrl() + '/source-feedback'" [submissions]="submissions()" />
      }
    }
    <fimra-side-panel #review="fimraSidePanel" [heading]=" msg('Review field report') " [fullHeight]="true" (closed)="selected.set(null)">
      @if (selected(); as id) {
        @defer (on immediate) {
          <fimra-submission-view [workspaceUrl]="workspaceUrl()" [submissionId]="id" (decided)="reload()" />
        }
      }
    </fimra-side-panel>
    <hlm-dialog [state]="open() ? 'open' : 'closed'" (closed)="open.set(null)" aria-modal="true">
      <hlm-dialog-content *hlmDialogPortal class="report-dialog" [showCloseButton]="false">
        @if (open(); as o) {
          <div class="report-dialog-heading">
            <h2 hlmDialogTitle>{{ o.provisionalSubmissionId ? msg('Revised damage assessment') : msg('Start {0}', [msg(o.form.title)]) }}</h2>
            <button hlmBtn variant="ghost" size="sm" (click)="open.set(null)">{{ msg('Close') }}</button>
          </div>
          @defer (on immediate) {
            <fimra-form-fill [workspaceUrl]="workspaceUrl()" [form]="o.form.form" [version]="o.form.version" [step]="o.form.step"
              [stepLabel]="stepLabel(o.form.step)" [currentStepLabel]="stepLabel(currentStep())"
              [alert]="alert()" [initial]="o.initial" [provisionalSubmissionId]="o.provisionalSubmissionId" (submitted)="reload()" (cancelled)="open.set(null)" />
          } @placeholder {
            <hlm-skeleton class="h-24" role="status" [attr.aria-label]=" msg('Loading') " />
          }
        }
      </hlm-dialog-content>
    </hlm-dialog>
  `
})
export class AlertReports {
  protected readonly msg = translate;
  /** The workspace's API address, ending in /api/v1/workspaces/{slug}. */
  readonly workspaceUrl = input.required<string>();
  readonly alert = input.required<AlertRow>();
  /** When the alert reached its step. A change reloads the forms. */
  readonly stepTime = input.required<string>();
  /** A report was sent or decided, so the alert's step may move on. */
  readonly changed = output<void>();
  readonly forms = signal<AssignedForm[] | null>(null);
  readonly submissions = signal<Submission[]>([]);
  readonly reached = signal<string[]>([]);
  private readonly stepLabels = signal<Record<string, string>>({});
  readonly message = signal('');
  readonly open = signal<Draft | null>(null);
  protected readonly selected = signal<string | null>(null);
  protected readonly currentStep = signal('');
  protected readonly labels = decisionLabels;
  private readonly version = signal(0);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  protected readonly assessmentForm = computed(() => this.forms()?.find(f => f.form === 'damage-assessment'));
  protected readonly alertUrl = computed(() => this.workspaceUrl() + '/alerts/' + this.alert().id);

  constructor() {
    let previousUrl = '';
    effect(onCleanup => {
      const url = this.alertUrl();
      this.stepTime();
      this.version();
      if (previousUrl !== url) {
        previousUrl = url;
        this.forms.set(null);
        this.submissions.set([]);
        this.reached.set([]);
        this.open.set(null);
        this.selected.set(null);
      }
      const request = forkJoin({
        forms: this.http.get<{ step: string; forms: AssignedForm[] }>(url + '/forms'),
        submissions: this.http.get<Submission[]>(url + '/submissions'),
        history: this.http.get<History>(url + '/history')
      }).subscribe({
        next: ({ forms, submissions, history }) => {
          this.forms.set(forms.forms);
          this.currentStep.set(forms.step);
          this.submissions.set([...submissions].reverse());
          this.reached.set(history.entries.map(e => e.step));
          this.stepLabels.set(Object.fromEntries([...history.moves, ...history.entries].map(e => [e.step, e.label])));
          this.message.set('');
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.message.set('The reports for this alert could not load.');
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  /** A form can be filled in once the alert has reached its step, until a report counts. */
  canFill(form: AssignedForm): boolean {
    return this.reached().includes(form.step) && (form.form === 'response-update' || (!form.submitted &&
      (form.form !== 'damage-assessment' || !this.submissions().some(s => s.form === form.form))));
  }

  stepLabel(step: string): string {
    return this.stepLabels()[step] ?? step.replace(/_/g, ' ');
  }

  titleOf(report: Submission): string {
    if (report.form === 'damage-assessment') {
      return translate(report.provisionalAssessment ? 'Revised damage assessment' : 'Provisional damage assessment');
    }
    return translate(this.forms()?.find(f => f.form === report.form)?.title ?? report.form);
  }

  time(value: string): string {
    return workspaceFormat.dateTime(new Date(value));
  }

  fill(form: AssignedForm): void {
    // Later stages keep the answers of earlier ones, from the newest report that counts.
    const earlier = this.submissions().find(s => s.form === form.form && s.step !== form.step && s.approval?.decision !== 'returned');
    this.open.set({ form, initial: earlier?.values ?? {} });
  }

  protected revise(form: AssignedForm, assessment: Submission): void {
    this.open.set({ form, provisionalSubmissionId: assessment.id, initial: {} });
  }

  reload(): void {
    this.version.update(n => n + 1);
    this.changed.emit();
  }
}
