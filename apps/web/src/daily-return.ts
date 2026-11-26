import { NgTemplateOutlet } from '@angular/common';
import { HttpClient, HttpErrorResponse, HttpParams } from '@angular/common/http';
import { Component, computed, effect, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { RouterLink } from '@angular/router';
import { firstValueFrom } from 'rxjs';
import metrics from '../public/locales/daily-return-metrics.json';
import { WEB_CONFIG } from './config';
import { formatLocale, language, translate } from './language';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';
import { workspaceFormat } from './workspace-format';
import { HlmButtonImports } from '@spartan-ng/helm/button';

type Totals = Record<string, number>;
interface Policy { hierarchy: string; level: string; timeZone: string; cutoff: string; from: string; to: string; approver: string }
interface Unit { path: string; name: string; nil?: boolean; status?: 'reported' | 'missing' | 'pending';
  revision?: number; decision?: ReviewStatus | null }
interface Coverage { expected: number; reported: number; missing: number; daily: Totals; season: Totals; units: Unit[] }
interface View { day: string; today: string; canConfigure: boolean; policy?: Policy; cutoff?: string; scopes?: Unit[]; coverage?: Coverage }
type ReviewStatus = 'submitted' | 'approved' | 'returned';
interface Revision { id: string; revision: number; status: ReviewStatus; submittedBy: string; submittedAt: string;
  decidedBy: string | null; decidedAt: string | null; reviewNote: string | null }
interface Snapshot { scope: string; period: { end: string; partial: boolean };
  dailyReturn: { nil: boolean; remarks: string; totals: Totals }; review: Revision }
interface UnitView { unit: string; cutoff: string; history: Revision[]; snapshot: Snapshot | null; canSubmit: boolean; canReview: boolean }

export const reviewLabel = { submitted: 'Submitted for review', approved: 'Approved', returned: 'Returned' };
export function coverageLabel(unit: Unit): string {
  if (unit.status === 'reported') return unit.nil ? 'Nil return' : 'Return received';
  return unit.status === 'missing' ? 'Reporting gap' : 'Awaiting cutoff';
}

export function returnDateTime(value: string, timeZone: string): string {
  return new Intl.DateTimeFormat(formatLocale(), { dateStyle: 'medium', timeStyle: 'short', timeZone }).format(new Date(value));
}

function dailyURL(config: { apiBaseUrl: string }, slug: string): string {
  return config.apiBaseUrl.replace(/\/$/, '') + '/api/v1/workspaces/' + encodeURIComponent(slug);
}

function coverageText(coverage: Coverage): string {
  return translate('{0} expected, {1} reported, {2} missing',
    [coverage.expected, coverage.reported, coverage.missing].map(v => workspaceFormat.number(v)));
}

@Component({
  selector: 'fimra-daily-coverage-link',
  imports: [RouterLink],
  template: `
    @if (view()?.coverage; as coverage) {
      @if (coverage.expected) {
        <div class="rounded-card border border-border bg-card p-3">
          <a class="underline" routerLink="/reports/daily" [queryParams]="{ workspace: workspace.current()?.slug, day: view()?.day }">
            {{ msg('Daily return coverage') }}
          </a>
          <p>{{ summary(coverage) }}</p>
          <ul class="flex flex-wrap gap-x-4 text-text-secondary">
            @for (unit of coverage.units; track unit.path) {
              @if (unit.nil || unit.status === 'missing') {
                <li>{{ unit.name }} · {{ msg(label(unit)) }}</li>
              }
            }
          </ul>
        </div>
      }
    }
  `
})
export class DailyCoverageLink {
  protected readonly workspace = inject(WorkspaceService);
  protected readonly view = signal<View | null>(null);
  protected readonly msg = translate;
  protected readonly summary = coverageText;
  protected readonly label = coverageLabel;
  private readonly http = inject(HttpClient);
  private readonly config = inject(WEB_CONFIG);

  constructor() {
    effect(onCleanup => {
      this.view.set(null);
      const slug = this.workspace.current()?.slug;
      if (!slug) return;
      const request = this.http.get<View>(dailyURL(this.config, slug) + '/daily-returns', { params: { coverageOnly: '1' } })
        .subscribe({ next: view => this.view.set(view), error: () => this.view.set(null) });
      onCleanup(() => request.unsubscribe());
    });
  }
}

@Component({
  selector: 'fimra-daily-return',
  imports: [FormsModule, NgTemplateOutlet, HlmButtonImports],
  templateUrl: './daily-return.html',
  styles: `
    label:not(.flex) { display: grid; gap: .25rem; }
    input:not([type=checkbox]), select, textarea { width: 100%; padding: .4rem .6rem; background: var(--color-card); border: 1px solid var(--color-border); border-radius: .375rem; }
    th, td { padding: .35rem .65rem; text-align: start; border-block-end: 1px solid var(--color-border); }
    td:last-child, th:last-child { text-align: end; }
  `
})
export class DailyReturnView {
  private readonly http = inject(HttpClient);
  private readonly config = inject(WEB_CONFIG);
  private readonly workspace = inject(WorkspaceService);
  private readonly navigation = inject(ShellNavigation);
  private readonly base = computed(() => {
    const slug = this.workspace.current()?.slug;
    return slug ? dailyURL(this.config, slug) : '';
  });
  protected readonly msg = translate;
  protected readonly label = coverageLabel;
  protected readonly reviewLabel = reviewLabel;
  protected readonly coverageText = coverageText;
  protected readonly format = workspaceFormat;
  protected readonly when = returnDateTime;
  protected readonly groups = metrics.map(group => ({ label: group.label, rows: Object.entries(group.rows) }));
  protected readonly view = signal<View | null>(null);
  protected readonly unit = signal<UnitView | null>(null);
  protected readonly failure = signal('');
  protected readonly loading = signal(false);
  protected readonly saving = signal(false);
  protected readonly pdfBusy = signal(false);
  protected readonly day = signal('');
  protected readonly scope = signal('');
  protected readonly path = signal('');
  protected readonly revision = signal('');
  protected readonly rollup = signal<'daily' | 'season'>('daily');
  protected remarks = '';
  protected reviewNote = '';
  protected nil = false;
  protected received = 0;
  protected policy: Policy = { hierarchy: 'territorial', level: 'division', timeZone: 'Asia/Kolkata',
    cutoff: '18:00', from: '', to: '', approver: 'state_admin' };
  private readonly refresh = signal(0);

  constructor() {
    effect(() => {
      const query = this.navigation.query();
      this.view.set(null);
      this.day.set(query.get('day') ?? '');
      this.scope.set('');
      this.openUnit(query.get('unit') ?? '');
    });
    effect(onCleanup => {
      this.refresh();
      const base = this.base();
      const day = this.day();
      const scope = this.scope();
      if (!base) return;
      this.loading.set(true);
      this.failure.set('');
      const request = this.http.get<View>(base + '/daily-returns', { params: { day, scope } }).subscribe({
        next: view => { this.view.set(view); this.loading.set(false); },
        error: error => { this.view.set(null); this.loading.set(false); this.fail(error, 'Could not load the daily return.'); }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(onCleanup => {
      this.refresh();
      const base = this.base();
      const day = this.view()?.day;
      const path = this.path();
      const revision = this.revision();
      if (!base || !day || !path) return;
      const request = this.http.get<UnitView>(base + '/daily-returns', { params: { day, unit: path, revision } }).subscribe({
        next: detail => this.unit.set(detail),
        error: error => this.fail(error, 'Could not load the daily return.')
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  protected chooseDay(day: string): void {
    this.view.set(null);
    this.day.set(day);
    this.openUnit('');
  }

  protected openUnit(unit: string): void {
    this.path.set(unit);
    this.unit.set(null);
    this.revision.set('');
    this.remarks = '';
    this.reviewNote = '';
    this.nil = false;
    this.received = 0;
  }

  protected async configure(): Promise<void> {
    this.failure.set('');
    this.saving.set(true);
    try {
      await firstValueFrom(this.http.post(this.base() + '/daily-return-policy', this.policy));
      this.chooseDay('');
      this.refresh.update(n => n + 1);
    } catch (error) {
      this.fail(error, 'Could not save the daily return.');
    } finally {
      this.saving.set(false);
    }
  }

  protected async change(action: 'submit' | 'approved' | 'returned'): Promise<void> {
    this.failure.set('');
    this.saving.set(true);
    const base = this.unit()?.history[0]?.id ?? '';
    const body = action === 'submit' ? { action, base, nil: this.nil, received: this.nil ? 0 : this.received, remarks: this.remarks } :
      { action, base, remarks: this.reviewNote };
    try {
      await firstValueFrom(this.http.post(this.base() + '/daily-returns', body, { params: this.unitParams() }));
      this.revision.set('');
      this.refresh.update(n => n + 1);
    } catch (error) {
      this.fail(error, 'Could not save the daily return.');
    } finally {
      this.saving.set(false);
    }
  }

  protected async download(snapshot: Snapshot): Promise<void> {
    this.failure.set('');
    this.pdfBusy.set(true);
    const base = this.base();
    try {
      const { id } = await firstValueFrom(this.http.post<{ id: string }>(base + '/daily-returns',
        { action: 'pdf', base: snapshot.review.id, language: language() === 'hi' ? 'hi' : 'en' },
        { params: this.unitParams() }));
      const url = base + '/situation-reports/' + id;
      for (let attempt = 0; attempt < 40; attempt++) {
        const status = await firstValueFrom(this.http.get<{ status: string; fileName: string }>(url));
        if (status.status === 'ready') {
          const file = await firstValueFrom(this.http.get(url + '/file', { responseType: 'blob' }));
          const href = URL.createObjectURL(file);
          const link = document.createElement('a');
          link.href = href;
          link.download = status.fileName;
          link.click();
          setTimeout(() => URL.revokeObjectURL(href), 1000);
          return;
        }
        if (status.status !== 'pending') break;
        await new Promise<void>(resolve => setTimeout(resolve, 1000));
      }
      this.failure.set('Could not prepare the daily return PDF.');
    } catch (error) {
      this.fail(error, 'Could not prepare the daily return PDF.');
    } finally {
      this.pdfBusy.set(false);
    }
  }

  private unitParams(): HttpParams {
    return new HttpParams().set('day', this.view()?.day ?? '').set('unit', this.path());
  }

  private fail(error: unknown, fallback: string): void {
    this.failure.set(error instanceof HttpErrorResponse && typeof error.error?.error === 'string' && error.status < 500 ?
      error.error.error : fallback);
  }
}
