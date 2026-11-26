import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, computed, effect, inject, linkedSignal, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { RouterLink } from '@angular/router';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmInput } from '@spartan-ng/helm/input';
import { firstValueFrom } from 'rxjs';
import { FormFill } from './forms/form-fill';
import { SubmissionView } from './forms/submission-view';
import { decisionLabels, serverProblems, type Decision } from './forms/form-api';
import { textIn } from './forms/form-text';
import { language, translate } from './language';
import { recordId, submissionId } from './shell-navigation';
import { WorkspaceList } from './shell-workspace-list';
import { StateRegion, type DataState } from './ui/state-region';
import { workspaceFormat } from './workspace-format';

type Kind = 'offence' | 'relief' | 'preparedness' | 'training' | 'readiness' | 'burn' | 'after-action' | 'patrol';
const kinds: Record<Kind, string> = {
  offence: 'Offence referral',
  relief: 'Relief referral',
  preparedness: 'Preparedness work',
  training: 'Fire training',
  readiness: 'Readiness check',
  burn: 'Prescribed burn',
  'after-action': 'After-action finding',
  patrol: 'Patrol log'
};
interface Card {
  id: string;
  kind: Kind;
  incidentId: string | null;
  summary: string | null;
  summaryOnly: boolean;
  unitPath?: string;
  owner?: string;
  completed?: boolean;
}
interface FormChoice {
  kind: Kind;
  form: string;
  title: Record<string, string>;
  version: number;
  completionOnly: boolean;
}
interface Entry extends FormChoice {
  id: string;
  decision: Decision | null;
  receivedAt: string;
  completesRecord: boolean;
}
interface Detail extends Card {
  entries?: Entry[];
  canWrite?: boolean;
  canLink?: boolean;
  alerts?: string[];
  completionPending?: boolean;
  burnAuthorised?: boolean;
}
interface Page { items: Card[]; nextCursor?: string }
interface Unit { id: string; path: string; name: string; hierarchy: string }
interface Options { forms: FormChoice[]; owners: { id: string; name: string }[] }

@Component({
  selector: 'fimra-operations-records',
  imports: [FormsModule, RouterLink, HlmButton, HlmInput, StateRegion, FormFill, SubmissionView],
  templateUrl: './operations-records.html',
  styles: `
    .record-fields, .record-list { display: grid; gap: .75rem; grid-template-columns: repeat(auto-fit, minmax(min(100%, 14rem), 1fr)); }
    .record-fields label { display: grid; gap: .25rem; }
    .record-details { margin-block: 1rem; }
    .record-details > div { display: flex; flex-wrap: wrap; gap: .5rem; }
  `,
  host: { class: 'block h-full' }
})
export class OperationsRecords extends WorkspaceList {
  private readonly http = inject(HttpClient);
  private readonly url = computed(() => this.endpoint() + '/operations-records');
  protected readonly selected = computed(() => submissionId(this.navigation.query().get('operation')));
  protected readonly incident = computed(() => recordId(this.navigation.query().get('operationCase')));
  protected readonly msg = translate;
  protected readonly decisionLabels = decisionLabels;
  protected readonly kindName = (kind: Kind) => translate(kinds[kind]);
  protected readonly localTitle = (title: Record<string, string>) => textIn(title, language());
  protected readonly dateTime = (value: string) => workspaceFormat.dateTime(new Date(value));

  protected readonly page = signal<Page | null>(null);
  protected readonly pageState = signal<DataState>({ kind: 'loading', heading: 'Loading records' });
  protected readonly cursorTrail = linkedSignal(() => {
    this.url();
    this.incident();
    return [''];
  });
  protected readonly detail = signal<Detail | null>(null);
  protected readonly detailState = signal<DataState>({ kind: 'loading', heading: 'Loading the record' });
  protected readonly newestForm = signal<FormChoice[]>([]);
  protected readonly historyBefore = linkedSignal({ source: this.selected, computation: () => '' });
  protected readonly entry = linkedSignal({ source: this.selected, computation: () => '' });
  protected readonly editing = linkedSignal<{ form: FormChoice; complete: boolean } | null>(() => {
    this.selected();
    return null;
  });
  protected readonly revision = signal(0);
  protected readonly notice = signal('');
  protected readonly error = signal('');
  protected readonly busy = signal(false);

  protected readonly starting = linkedSignal(() => {
    this.url();
    this.selected();
    return false;
  });
  protected readonly unitSearch = signal('');
  protected readonly unitMatches = signal<Unit[]>([]);
  protected readonly unit = linkedSignal({ source: this.url, computation: () => '' });
  protected readonly createOptions = signal<Options | null>(null);
  protected kind: Kind | '' = '';
  protected owner = '';
  protected caseInput = '';
  private createID = '';
  private lastCreate = '';

  constructor() {
    super();
    effect(cleanup => {
      const url = this.url();
      const incident = this.incident();
      const cursor = this.cursorTrail().at(-1)!;
      this.revision();
      this.page.set(null);
      this.pageState.set({ kind: 'loading', heading: 'Loading records' });
      const request = this.http.get<Page>(url, { params: { cursor, incident } }).subscribe({
        next: page => {
          this.page.set(page);
          this.pageState.set({ kind: 'ready' });
        },
        error: () => this.pageState.set({ kind: 'error', heading: 'Could not load records.' })
      });
      cleanup(() => request.unsubscribe());
    });
    effect(cleanup => {
      const url = this.url();
      const id = this.selected();
      const before = this.historyBefore();
      this.revision();
      if (!id) {
        this.detail.set(null);
        return;
      }
      this.detailState.set({ kind: 'loading', heading: 'Loading the record' });
      const request = this.http.get<Detail>(url + '/' + id, { params: { before } }).subscribe({
        next: detail => {
          this.detail.set(detail);
          this.detailState.set({ kind: 'ready' });
        },
        error: (err: HttpErrorResponse) => {
          this.detail.set(null);
          this.entry.set('');
          this.editing.set(null);
          this.detailState.set(err.status === 404
            ? { kind: 'forbidden', heading: 'This record is unavailable.' }
            : { kind: 'error', heading: 'Could not load the record.' });
        }
      });
      cleanup(() => request.unsubscribe());
    });
    effect(cleanup => {
      const url = this.url();
      const detail = this.detail();
      this.newestForm.set([]);
      if (!detail || !detail.canWrite || detail.id !== this.selected()) return;
      const request = this.http.get<Options>(url + '/options', { params: { record: detail.id } }).subscribe({
        next: options => this.newestForm.set(options.forms),
        error: () => this.error.set(translate('Could not load record options.'))
      });
      cleanup(() => request.unsubscribe());
    });
    effect(cleanup => {
      const url = this.url();
      const search = this.unitSearch();
      this.unitMatches.set([]);
      if (!this.starting()) return;
      const request = this.http.get<{ units: Unit[] }>(url + '/options', { params: { search } }).subscribe({
        next: options => this.unitMatches.set(options.units),
        error: () => this.error.set(translate('Could not load record options.'))
      });
      cleanup(() => request.unsubscribe());
    });
    effect(cleanup => {
      const url = this.url();
      const unit = this.unit();
      this.createOptions.set(null);
      if (!this.starting() || !unit) return;
      const request = this.http.get<Options>(url + '/options', { params: { unit } }).subscribe({
        next: options => this.createOptions.set(options),
        error: () => this.error.set(translate('Could not load record options.'))
      });
      cleanup(() => request.unsubscribe());
    });
  }

  protected nextPage(cursor: string): void { this.cursorTrail.update(history => [...history, cursor]); }
  protected previousPage(): void { this.cursorTrail.update(history => history.slice(0, -1)); }
  protected reload(): void { this.revision.update(n => n + 1); }
  protected listURL(id: string | null = null) {
    return this.navigation.href('/reports/operations', { operation: id });
  }
  protected closeDetail(): void {
    this.entry.set('');
    this.editing.set(null);
    this.error.set('');
    this.notice.set('');
  }
  protected openCreate(): void {
    this.error.set('');
    this.notice.set('');
    this.unit.set('');
    this.unitSearch.set('');
    this.kind = '';
    this.owner = '';
    this.caseInput = this.incident();
    this.lastCreate = '';
    this.starting.set(true);
  }
  protected pickUnit(unit: string): void {
    this.kind = '';
    this.owner = '';
    this.unit.set(unit);
  }
  protected referral(kind: Kind | ''): boolean { return kind === 'offence' || kind === 'relief'; }

  protected create(): void {
    if (!this.kind || !this.unit() || this.busy()) return;
    const url = this.url();
    const values = { kind: this.kind, unit: this.unit(), owner: this.owner, incidentId: this.referral(this.kind) ? this.caseInput.trim() : '' };
    const serialized = JSON.stringify(values);
    if (serialized !== this.lastCreate) {
      this.createID = crypto.randomUUID();
      this.lastCreate = serialized;
    }
    void this.save(() => firstValueFrom(this.http.post<{ id: string }>(url, { id: this.createID, ...values })), result => {
      this.starting.set(false);
      this.navigation.go('/reports/operations', { operation: result.id });
    });
  }

  protected edit(form: FormChoice, complete: boolean): void {
    this.entry.set('');
    this.editing.set({ form, complete });
  }
  protected viewEntry(id: string): void {
    this.entry.set(id);
    this.editing.set(null);
  }
  protected savedEntry(id: string): void {
    this.viewEntry(id);
    this.historyBefore.set('');
    this.reload();
  }
  protected linkAlert(input: HTMLInputElement): void {
    const id = recordId(input.value.trim());
    if (!id) {
      this.error.set(translate('Enter a valid alert ID.'));
      return;
    }
    const detail = this.detail();
    if (!detail?.canLink) return;
    void this.save(() => firstValueFrom(this.http.put(this.url() + '/' + detail.id + '/alerts/' + id, null)), () => {
      input.value = '';
      this.notice.set(translate('Alert linked.'));
    });
  }

  private async save<T>(action: () => Promise<T>, onSuccess: (result: T) => void): Promise<void> {
    if (this.busy()) return;
    const url = this.url();
    const selected = this.selected();
    this.error.set('');
    this.notice.set('');
    this.busy.set(true);
    try {
      const result = await action();
      if (this.url() === url && this.selected() === selected) {
        onSuccess(result);
        this.reload();
      }
    } catch (err) {
      if (this.url() === url && this.selected() === selected) this.error.set(serverProblems(err as HttpErrorResponse).message);
    } finally {
      this.busy.set(false);
    }
  }
}
