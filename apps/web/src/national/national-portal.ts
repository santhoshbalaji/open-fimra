import { NgTemplateOutlet } from '@angular/common';
import { HttpClient, HttpErrorResponse, httpResource } from '@angular/common/http';
import { Component, computed, effect, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { HlmButton } from '@spartan-ng/helm/button';
import { firstValueFrom } from 'rxjs';
import { WEB_CONFIG } from '../config';
import { translate } from '../language';
import { ShellNavigation } from '../shell-navigation';
import { StateRegion, type DataState } from '../ui/state-region';
import { WorkspaceService } from '../workspace';
import { workspaceFormat } from '../workspace-format';
import { NationalIncidentForm } from './national-incident-form';
import { nationalWriteError } from './national-write-error';

const views = [
  { id: 'ministry', label: 'National forest ministry', roles: ['national_ministry'] },
  { id: 'survey', label: 'Forest Survey of India', roles: ['national_survey'] },
  { id: 'submissions', label: 'State submissions', roles: ['national_nodal'] },
  { id: 'incidents', label: 'Major incidents and assistance', roles: ['national_disaster', 'national_nodal'] },
  { id: 'public', label: 'Public aggregates', roles: ['national_public'] },
] as const;

type Status = 'reported' | 'zero' | 'not_reported' | 'incomplete';
interface Portal { roles: string[]; submittingStates: string[] }
interface Window { from: string; to: string }
interface Coverage { expected: number; reported: number; status: Status }
interface Provenance {
  source: string;
  window: Window & { startsAt: string; endsAt: string; timeZone: string; cutoff: string };
  generatedAt: string;
  revision: number;
}
interface StateReturn extends Provenance {
  coverage: Coverage;
  detections?: Record<string, number> | null;
  totals?: {
    detections: Record<string, number>;
    alerts: Record<string, number>;
    incidents: Record<string, number>;
    operations: Record<string, number>;
    area: Coverage & { hectares: number | null };
  } | null;
}
type Quantity = Coverage & { value: number | null };
interface Preparedness extends Provenance {
  figures: {
    status: Status;
    approvedRecords: number;
    trained: Quantity;
    target: Quantity;
    workDone: Quantity;
    expenditureINR: Quantity;
  };
}
interface Satellite {
  source: string; product: string; startsAt: string; endsAt: string;
  detections: number | null; expected: number; reported: number; samePeriod: boolean | null;
}
interface Receipt {
  extractId: string; revision: number; kind: 'incident' | 'preparedness';
  status: Status; sha256: string; validatedAt: string;
}
interface Row extends Window {
  state: string; geography: string;
  stateReturn?: StateReturn; satellite?: Satellite; preparedness?: Preparedness;
  feedback?: { prepared: number; submitted: number; accepted: number; rejected: number };
  stateReturnShared?: boolean; preparednessShared?: boolean; feedbackShared?: boolean;
  currentIncidentId?: string; ministry?: boolean; survey?: boolean; public?: boolean; receipts?: Receipt[];
  source?: string; confirmedFires?: number | null; coverage?: Status;
}
interface Update {
  state: string; geography: string; incidentRef: string; sharedEventId: string; fsiId?: string;
  status: 'active' | 'controlled' | 'extinguished' | 'closed'; severity: 'major' | 'severe' | 'critical';
  occurredAt: string; nextUpdateAt?: string; precisionDecimals: number; longitude: number | null; latitude: number | null;
  threats: string; aidRequested: string; aidAccepted: string; provisionalImpacts: string; shared: boolean;
}
interface IncidentView { eventCount: number; updates: Update[] }

@Component({
  selector: 'fimra-national-portal',
  imports: [RouterLink, NgTemplateOutlet, HlmButton, StateRegion, NationalIncidentForm],
  templateUrl: './national-portal.html',
})
export class NationalPortal {
  private readonly http = inject(HttpClient);
  private readonly workspace = inject(WorkspaceService);
  private readonly config = inject(WEB_CONFIG);
  private readonly navigation = inject(ShellNavigation);
  protected readonly base = computed(() => {
    const slug = this.workspace.current()?.slug;
    return slug && this.config.apiBaseUrl.replace(/\/$/, '') + '/api/v1/workspaces/' + encodeURIComponent(slug) + '/national';
  });

  protected readonly portal = httpResource<Portal>(() => this.base());
  protected readonly choices = computed(() => {
    const roles = this.portal.hasValue() ? this.portal.value().roles : [];
    return views.filter(view => view.roles.some(role => roles.includes(role)));
  });
  protected readonly selected = computed(() => this.choices().find(view => view.id === this.navigation.query().get('view')) ?? this.choices()[0]);
  protected readonly rows = httpResource<Row[]>(() => {
    const view = this.selected()?.id;
    return view && view !== 'incidents' ? this.base() + '/' + view : undefined;
  });
  protected readonly incidents = httpResource<IncidentView>(() => this.selected()?.id === 'incidents' ? this.base() + '/incidents' : undefined);
  protected readonly events = computed(() => {
    const groups = new Map<string, Update[]>();
    for (const update of this.incidents.hasValue() ? this.incidents.value().updates : []) {
      const group = groups.get(update.sharedEventId);
      if (group) group.push(update);
      else groups.set(update.sharedEventId, [update]);
    }
    return [...groups.values()];
  });

  protected readonly state = computed<DataState>(() => {
    const restricted = this.selected()?.id === 'incidents';
    const resource = restricted ? this.incidents : this.rows;
    const problem = this.portal.error() ?? resource.error();
    if (problem) return { kind: problem instanceof HttpErrorResponse && problem.status === 403 ? 'forbidden' : 'error' };
    if (!this.portal.hasValue() || !(restricted ? this.incidents.hasValue() : this.rows.hasValue())) return { kind: 'loading' };
    return { kind: 'ready' };
  });

  protected readonly busy = signal(false);
  protected readonly notice = signal('');
  protected readonly failure = signal('');
  protected readonly receipt = signal<Receipt | null>(null);

  protected readonly msg = translate;
  protected readonly number = workspaceFormat.number;
  protected readonly day = (value: string) => workspaceFormat.date(new Date(value));
  protected readonly time = (value: string) => workspaceFormat.dateTime(new Date(value));
  protected readonly statuses = { reported: 'Reported', zero: 'Zero', not_reported: 'Not reported', incomplete: 'Incomplete' };
  protected readonly sourceNames: Record<string, string> = {
    approved_daily_returns_and_assessments: 'Approved daily returns and assessments',
    approved_preparedness_records: 'Approved preparedness records'
  };
  protected readonly incidentStatuses = { active: 'Active', controlled: 'Controlled', extinguished: 'Extinguished', closed: 'Closed' };
  protected readonly severities = { major: 'Major', severe: 'Severe', critical: 'Critical' };

  constructor() {
    effect(() => {
      this.base();
      this.busy.set(false);
      this.notice.set('');
      this.failure.set('');
      this.receipt.set(null);
    });
  }

  protected choose(view: string) {
    return this.navigation.href('/national', { view });
  }

  protected retry(): void {
    if (this.portal.error()) this.portal.reload();
    else if (this.selected()?.id === 'incidents') this.incidents.reload();
    else this.rows.reload();
  }

  protected coverage(data: { expected: number; reported: number }): string {
    return translate('{0} expected · {1} reported · {2} missing',
      [this.number(data.expected), this.number(data.reported), this.number(data.expected - data.reported)]);
  }

  protected async upload(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const form = event.target as HTMLFormElement;
    const file = new FormData(form).get('extract');
    if (!(file instanceof File) || file.size === 0) return;
    await this.save(async base => {
      const receipt = await firstValueFrom(this.http.post<Receipt>(base + '/upload', file, {
        headers: { 'Content-Type': 'application/json' }
      }));
      if (base !== this.base()) return;
      this.receipt.set(receipt);
      form.reset();
    }, 'Extract received and verified.');
  }

  protected async share(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const form = new FormData(event.target as HTMLFormElement);
    const code = String(form.get('state'));
    await this.save(base => firstValueFrom(this.http.put(base + '/states/' + encodeURIComponent(code) + '/sharing', {
      from: form.get('from'), to: form.get('to'), ministry: form.has('ministry'), survey: form.has('survey'),
      preparedness: form.has('preparedness'), feedback: form.has('feedback')
    })), 'Sharing saved.');
  }

  protected async publish(row: Row): Promise<void> {
    await this.save(base => firstValueFrom(this.http.put(base + '/states/' + encodeURIComponent(row.state) + '/publication', {
      from: row.from, to: row.to, extractId: row.public ? null : row.currentIncidentId
    })), row.public ? 'Public release withdrawn.' : 'Public aggregate published.');
  }

  private async save(send: (base: string) => Promise<unknown>, success: string): Promise<void> {
    const base = this.base();
    if (!base || this.busy()) return;
    this.busy.set(true);
    this.failure.set('');
    this.notice.set('');
    this.receipt.set(null);
    try {
      await send(base);
      if (base === this.base()) {
        this.notice.set(success);
        this.rows.reload();
      }
    } catch (error) {
      if (base === this.base()) this.failure.set(nationalWriteError(error));
    } finally {
      if (base === this.base()) this.busy.set(false);
    }
  }
}
