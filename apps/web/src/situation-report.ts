import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmLabelImports } from '@spartan-ng/helm/label';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmSkeleton } from '@spartan-ng/helm/skeleton';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { HttpClient, HttpErrorResponse, HttpParams } from '@angular/common/http';
import { Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, effect, inject, signal, untracked, viewChild } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { incidentOffset } from './detections';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';
import { StatusBadge } from './ui/status-badge';
import { LiveStatus } from './ui/live-status';
import { DataState, StateRegion } from './ui/state-region';
import { ageLabels } from './token-values';
import { reportingTimeZone } from './workspace-format';
import { alertSymbol, reportAge, reportClass, reportClasses, SituationReportSymbol } from './situation-report-symbol';
import { Icon } from './ui/icon';

type Geometry = { type: 'Polygon'; coordinates: number[][][] } | { type: 'MultiPolygon'; coordinates: number[][][][] };
type Bounds = [number, number, number, number];
export interface ReportUnit { hierarchy: string; path: string; code: string; name: string; depth: number; levelLabel: string }
interface Milestone { step: string; label: string; median: number | null; p90: number | null; count: number }
interface IncidentItem {
  id: string; stepLabel: string; overdue: boolean; terminal: boolean; firstObserved: string; lastObserved: string;
  alertCount: number; places: string[];
}
interface FormRow { form: string; title: string; submitted: number; approved: number; returned: number; awaitingReview: number; missing: number }
/** A situation report as the API returns it. The PDF is drawn from the same data. */
export interface SituationReport {
  snapshotId: string;
  workspace: { slug: string; name: string };
  unit: { name: string; levelLabel: string; path: string; hierarchy: string } | null;
  scope: string;
  period: { hours: number | null; from: string; to: string; days: number; start: string; end: string; partial: boolean };
  generatedAt: string;
  preparedFor: { role: string; levelLabel: string; unitName: string }[];
  counts: { detections: Record<string, number>; queues: Record<string, number>; overdue: number; pendingReports: number };
  alerts: { total: number; active: number; closed: number };
  steps: { step: string; label: string; terminal: boolean; count: number }[];
  responseTimes: Milestone[];
  placeLevels: string[];
  incidents: { total: number; opened: number; active: number; closed: number; overdue: number; items: IncidentItem[] };
  fieldReports: { submitted: number; approved: number; returned: number; awaitingReview: number; missing: number;
    alertsMissing: number; forms: FormRow[] };
  overdueAlerts: { id: string; stepLabel: string; enteredAt: string; overdueSeconds: number; place: string | null }[];
  unitLevel: string | null;
  units: { code: string; name: string; path: string; alerts: number; active: number; overdue: number;
    pendingReports: number; formsMissing: number }[];
  map: { bounds: Bounds | null; outline: Geometry | null; units: { name: string; geometry: Geometry }[];
    forest: Geometry[]; alerts: [number, number, string, boolean, string?][]; alertsShown: number;
    incidents: { id: string; active: boolean; bounds: Bounds }[] };
  labels: { forestClasses: Record<string, string>; queues: Record<string, string>; roles: Record<string, string> };
  notes: string[];
}
interface PdfStatus { id: string; status: 'pending' | 'ready' | 'failed' | 'expired'; pageCount: number | null; fileName?: string }
type PeriodKind = 'day' | 'range' | 'hours';

const india = reportingTimeZone;
const zoneLabel = 'India Standard Time';
// The same forms as the PDF, such as 16 Mar 2026, 08:45 and Sunday 15 March 2026.
const timeFormat = new Intl.DateTimeFormat('en-IN', { timeZone: india, day: 'numeric', month: 'short', year: 'numeric',
  hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
const weekday = new Intl.DateTimeFormat('en-IN', { timeZone: 'UTC', weekday: 'long' });
const shortDay = new Intl.DateTimeFormat('en-IN', { timeZone: 'UTC', day: 'numeric', month: 'long', year: 'numeric' });
export const mapSize = { width: 720, height: 420 };

/** Today's date in India Standard Time, such as 2026-03-15. */
export function indiaToday(now = new Date()): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: india }).format(now);
}

export function formatTime(value: string): string {
  return timeFormat.format(new Date(value));
}

/** A response time in plain words, the same as in the PDF. */
export function duration(seconds: number | null): string {
  if (seconds === null || seconds === undefined) return 'No data';
  const value = Math.max(0, seconds);
  if (value < 60) return `${Math.round(value)} s`;
  const minutes = Math.round(value / 60);
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60), rest = minutes % 60;
  if (hours < 48) return rest ? `${hours} h ${rest} min` : `${hours} h`;
  const days = Math.floor(hours / 24), left = hours % 24;
  return left ? `${days} days ${left} h` : `${days} days`;
}

export function periodText(period: SituationReport['period'], includeZone = true): string {
  let text: string;
  const date = (value: string) => new Date(value + 'T00:00:00Z');
  if (period.hours) text = `Past ${period.hours} hours: ${formatTime(period.start)} to ${formatTime(period.end)}`;
  else if (period.from === period.to) text = `${weekday.format(date(period.from))} ${shortDay.format(date(period.from))}`;
  else text = `${shortDay.format(date(period.from))} to ${shortDay.format(date(period.to))} (${period.days} days)`;
  if (period.partial && !period.hours) text += `, up to ${formatTime(period.end)}`;
  return includeZone ? text + ', ' + zoneLabel : text;
}

function rings(geometry: Geometry | null): number[][][] {
  if (!geometry) return [];
  return geometry.type === 'Polygon' ? geometry.coordinates : geometry.coordinates.flat();
}

/** The report map as SVG shapes, projected like the PDF with north up. */
export function projectMap(map: SituationReport['map'], end = '', width = mapSize.width, height = mapSize.height) {
  const mapWidth = width;
  if (!map.bounds) return null;
  const [west, south, east, north] = map.bounds;
  const kx = Math.cos(((south + north) / 2) * Math.PI / 180);
  const pad = 8;
  const spanX = Math.max((east - west) * kx, 1e-6), spanY = Math.max(north - south, 1e-6);
  const scale = Math.min((width - 2 * pad) / spanX, (height - 2 * pad) / spanY);
  const dx = (width - spanX * scale) / 2, dy = (height - spanY * scale) / 2;
  const point = (lon: number, lat: number): [number, number] =>
    [dx + (lon - west) * kx * scale, height - dy - (lat - south) * scale];
  const path = (geometry: Geometry | null) => rings(geometry).map(ring => ring.map(([lon, lat], i) => {
    const [x, y] = point(lon, lat);
    return `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`;
  }).join('') + 'Z').join('');
  const centre = (geometry: Geometry) => {
    const ring = rings(geometry)[0] ?? [];
    const lon = ring.reduce((sum, p) => sum + p[0], 0) / (ring.length || 1);
    const lat = ring.reduce((sum, p) => sum + p[1], 0) / (ring.length || 1);
    return point(lon, lat);
  };
  // A label that would cover another is left out, as in the PDF; the tables name everything.
  const placed: [number, number, number, number][] = [];
  const fits = (x: number, y: number, text: string, size: number, middle = false) => {
    const width = text.length * size * 0.55;
    const left = Math.min(Math.max(middle ? x - width / 2 : x, 2), mapWidth - width - 2);
    const box: [number, number, number, number] = [left - 1, y - size, left + width + 1, y + 2];
    if (placed.some(b => box[0] < b[2] && b[0] < box[2] && box[1] < b[3] && b[1] < box[3])) return false;
    placed.push(box);
    return true;
  };
  const incidentRadius = alertSymbol('incident').radius;
  const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);
  const incidents = map.incidents.map(incident => {
    const [x1, y1] = point(incident.bounds[0], incident.bounds[1]);
    const [x2, y2] = point(incident.bounds[2], incident.bounds[3]);
    const x = clamp((x1 + x2) / 2 + incidentOffset[0], incidentRadius + 1, width - incidentRadius - 1);
    const y = clamp((y1 + y2) / 2 + incidentOffset[1], incidentRadius + 1, height - incidentRadius - 1);
    const text = 'Incident ' + incident.id;
    const labelX = Math.min(Math.max(x + 8, 2), width - text.length * 5.5 - 2);
    const labelY = Math.max(y - 7, 12);
    return { id: incident.id, active: incident.active, x, y, labelX, labelY,
      labelled: fits(labelX, labelY, text, 10) };
  });
  const units = map.units.map(unit => {
    const label = centre(unit.geometry);
    return { name: unit.name, path: path(unit.geometry), label, labelled: fits(label[0], label[1], unit.name, 11, true) };
  });
  return {
    outline: path(map.outline),
    forest: map.forest.map(path),
    units,
    alerts: map.alerts.map(([lon, lat, rawClass, active, observed]) => {
      const [x, y] = point(lon, lat);
      const forestClass = reportClass(rawClass);
      const age = end ? reportAge(observed, end) : null;
      return { x, y, forestClass, active, age, symbol: alertSymbol(forestClass, age) };
    }),
    incidents
  };
}

/**
 * A printable situation report for a unit and a day, a date range or the past
 * hours. The page and the PDF show the same report from the API.
 */
@Component({
  selector: 'fimra-situation-report',
  imports: [HlmButtonImports, HlmFieldImports, HlmInput, HlmLabelImports, HlmRadioGroupImports, HlmSelectImports,
    HlmSkeleton, HlmTableImports, StatusBadge, Icon, LiveStatus, SituationReportSymbol, StateRegion],
  templateUrl: './situation-report.html',
  styleUrl: './situation-report.css'
})
export class SituationReportView {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly lifetime = inject(DestroyRef);
  protected readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly injector = inject(Injector);
  private readonly reportTitle = viewChild<ElementRef<HTMLElement>>('reportTitle');
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly unitControl = viewChild<unknown, ElementRef<HTMLElement>>('unitControl', { read: ElementRef });
  private readonly unitAttempt = signal(0);
  private reportRequest = 0;
  private timer?: ReturnType<typeof setTimeout>;

  readonly today = indiaToday();
  readonly units = signal<ReportUnit[]>([]);
  readonly unitState = signal<DataState>({ kind: 'loading', heading: 'Loading units' });
  readonly unitKey = signal('');
  readonly kind = signal<PeriodKind>('day');
  readonly from = signal(this.today);
  readonly to = signal(this.today);
  readonly hours = signal(24);
  readonly report = signal<SituationReport | null>(null);
  readonly loading = signal(false);
  readonly message = signal('');
  readonly pdf = signal<PdfStatus | null>(null);
  readonly pdfMessage = signal('');
  readonly pdfNeedsReport = signal(false);
  readonly sendingPdf = signal(false);
  readonly downloading = signal(false);
  readonly pdfBusy = computed(() => this.sendingPdf() || this.downloading() || this.pdf()?.status === 'pending');
  readonly pdfLabel = computed(() => {
    if (this.downloading()) return 'Downloading PDF…';
    if (this.pdfBusy()) return 'Preparing PDF…';
    if (this.pdfNeedsReport()) return 'Refresh report';
    if (this.pdfMessage()) return 'Retry PDF';
    const file = this.pdf();
    if (file?.status !== 'ready') return 'Download PDF';
    return `Download PDF (${file.pageCount} ${file.pageCount === 1 ? 'page' : 'pages'})`;
  });
  readonly pdfStatus = computed(() => this.pdfBusy() ? this.pdfLabel() :
    this.pdf()?.status === 'ready' && !this.pdfMessage() ? 'PDF ready. Use Download PDF to save another copy.' : '');
  readonly mapSize = mapSize;
  readonly duration = duration;
  readonly formatTime = formatTime;
  readonly classKeys = reportClasses;
  readonly alertSymbol = alertSymbol;
  readonly incidentSymbol = alertSymbol('incident');
  readonly zoneLabel = zoneLabel;
  readonly mapAges = computed(() => this.map()?.alerts.some(alert => alert.age !== null)
    ? [...ageLabels.slice(0, -1), '3 days or more'] : []);
  readonly mapCounts = computed(() => {
    const alerts = this.map()?.alerts ?? [];
    const incidents = this.map()?.incidents ?? [];
    const tally = (marks: { active: boolean }[]) => {
      const active = marks.filter(mark => mark.active).length;
      return { active, closed: marks.length - active, total: marks.length };
    };
    return [
      ...this.classKeys.map(key => ({ label: this.label('forestClasses', key),
        ...tally(alerts.filter(alert => alert.forestClass === key)) })),
      { label: 'Incidents', ...tally(incidents) }
    ];
  });
  readonly queueKeys = ['unit', 'edge_review', 'triage'];

  readonly endpoint = computed(() => {
    const slug = this.workspace.current()?.slug;
    return slug ? this.base + '/api/v1/workspaces/' + encodeURIComponent(slug) : '';
  });
  readonly selectedUnit = computed(() => this.units().find(unit => this.key(unit) === this.unitKey()) ?? null);
  readonly workspaceName = computed(() => this.workspace.current()?.name);
  readonly unitLabel = computed(() => {
    const unit = this.selectedUnit();
    return unit ? `${unit.name} (${unit.levelLabel})` : 'Choose a unit';
  });
  readonly map = computed(() => {
    const report = this.report();
    return report ? projectMap(report.map, report.period.end) : null;
  });
  readonly heading = computed(() => {
    const report = this.report();
    if (!report) return '';
    return report.unit?.name === report.workspace.name ? report.scope : `${report.scope} · ${report.workspace.name}`;
  });
  readonly preparedFor = computed(() => (this.report()?.preparedFor ?? [])
    .map(grant => `${this.report()?.labels.roles[grant.role] ?? grant.role}, ${grant.unitName} (${grant.levelLabel})`).join('; '));
  readonly figures = computed(() => {
    const report = this.report();
    if (!report) return [];
    return [
      { label: 'Detections', value: Object.values(report.counts.detections).reduce((a, b) => a + b, 0) },
      { label: 'Alerts', value: report.alerts.total }, { label: 'Active alerts', value: report.alerts.active },
      { label: 'Overdue alerts', value: report.counts.overdue }, { label: 'Incidents', value: report.incidents.total },
      { label: 'Forms missing', value: report.fieldReports.missing }
    ];
  });
  readonly periodSummary = computed(() => {
    const report = this.report();
    return report ? periodText(report.period, false) : '';
  });

  constructor() {
    this.lifetime.onDestroy(() => { this.reportRequest++; clearTimeout(this.timer); });
    effect(onCleanup => {
      const endpoint = this.endpoint();
      this.unitAttempt();
      this.clearReport();
      this.units.set([]);
      this.message.set('');
      this.unitState.set({ kind: 'loading', heading: 'Loading units' });
      if (!endpoint) return;
      const request = this.http.get<{ units: ReportUnit[] }>(endpoint + '/situation-report/units').subscribe({
        next: ({ units }) => {
          this.units.set(units);
          this.unitState.set(units.length
            ? { kind: 'ready', announcement: 'Units loaded' }
            : { kind: 'empty', heading: 'There are no units you can report on.' });
          if (!this.navigation && units.length) {
            this.unitKey.set(this.initialUnit(units));
            void this.loadReport();
          }
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) { this.auth.sessionEnded(); return; }
          if (error.status === 403 && !serverMessage(error).includes('own area')) {
            this.unitState.set({ kind: 'forbidden', heading: 'You do not have access to these units.' });
            this.workspace.deny();
            return;
          }
          this.unitState.set({ kind: 'error', heading: 'Could not load units for the report.' });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    if (this.navigation) {
      const query = this.navigation.query;
      effect(() => {
        const params = query();
        const units = this.units();
        const path = params.get('unitPath');
        const hierarchy = params.get('hierarchy');
        const unit = units.find(unit => unit.path === path && (!hierarchy || hierarchy === unit.hierarchy));
        const period = params.get('period');
        this.unitKey.set(unit ? this.key(unit) : path ? '' : this.initialUnit(units));
        this.kind.set(period === 'hours' || period === 'range' ? period : 'day');
        this.from.set(params.get('from') ?? this.today);
        this.to.set(params.get('to') ?? this.today);
        this.hours.set(Number(params.get('reportHours') ?? 24));
        if (!units.length) return;
        untracked(() => {
          this.clearReport();
          if (!path || unit) void this.loadReport();
          else this.message.set('Choose a unit you can report on.');
        });
      });
    }
  }

  retryUnits(): void {
    this.unitAttempt.update(attempt => attempt + 1);
  }

  private initialUnit(units: ReportUnit[]): string {
    const top = units.reduce<ReportUnit | null>((best, unit) => !best || unit.depth < best.depth ? unit : best, null);
    return top ? this.key(top) : '';
  }

  pickUnit(value: string | null | undefined): void {
    const unit = this.units().find(unit => this.key(unit) === value);
    if (!unit) return;
    this.unitKey.set(this.key(unit));
  }

  setPeriodValue(key: 'from' | 'to' | 'reportHours', value: string): void {
    if (key === 'reportHours') this.hours.set(+value);
    else this[key].set(value);
  }

  key(unit: ReportUnit): string {
    return unit.hierarchy + ':' + unit.path;
  }

  label(group: 'forestClasses' | 'queues', key: string): string {
    return this.report()?.labels[group][key] ?? key;
  }

  pickKind(value: string | null | undefined): void {
    if (value !== 'day' && value !== 'range' && value !== 'hours') return;
    this.kind.set(value);
  }

  private request(): Record<string, string | number> {
    const body: Record<string, string | number> = {};
    const unit = this.selectedUnit();
    if (unit) { body['unitPath'] = unit.path; body['hierarchy'] = unit.hierarchy; }
    if (this.kind() === 'hours') body['hours'] = this.hours();
    else {
      body['from'] = this.from();
      if (this.kind() === 'range') body['to'] = this.to();
    }
    return body;
  }

  async show(): Promise<void> {
    const unit = this.selectedUnit();
    if (!unit) return;
    if (this.navigation) {
      const kind = this.kind();
      const changes = {
        unitPath: unit.path, hierarchy: unit.hierarchy, period: kind,
        from: kind === 'hours' ? null : this.from(),
        to: kind === 'range' ? this.to() : null,
        reportHours: kind === 'hours' ? String(this.hours()) : null,
        show: '1'
      };
      const params = this.navigation.query();
      if (!Object.entries(changes).every(([key, value]) => params.get(key) === value)) {
        this.navigation.update(changes);
        return;
      }
    }
    await this.loadReport();
  }

  edit(): void {
    this.clearReport();
    afterNextRender(() => this.unitControl()?.nativeElement.querySelector('button')?.focus(), { injector: this.injector });
  }

  private clearReport(): void {
    this.reportRequest++;
    this.loading.set(false);
    this.report.set(null);
    this.resetPdf();
  }

  private resetPdf(): void {
    clearTimeout(this.timer);
    this.pdf.set(null);
    this.pdfMessage.set('');
    this.pdfNeedsReport.set(false);
    this.sendingPdf.set(false);
    this.downloading.set(false);
  }

  private async loadReport(): Promise<void> {
    if (!this.endpoint()) return;
    const request = ++this.reportRequest;
    this.resetPdf();
    this.loading.set(true);
    this.message.set('');
    try {
      const params = new HttpParams({ fromObject: { ...this.request(), snapshot: 1 } });
      const report = await firstValueFrom(this.http.get<SituationReport>(this.endpoint() + '/situation-report', { params }));
      if (request === this.reportRequest) {
        this.report.set(report);
        afterNextRender(() => {
          if (request !== this.reportRequest) return;
          const title = this.reportTitle()?.nativeElement;
          const pageHeading = this.host.nativeElement.closest('main')?.querySelector<HTMLElement>('.shell-page-header h1');
          // The shell focuses its heading on navigation. Only restore it
          // when a report load removed the focused form control.
          if (document.activeElement === document.body) (pageHeading ?? title)?.focus();
        }, { injector: this.injector });
      }
    } catch (error) {
      if (request === this.reportRequest) {
        this.report.set(null);
        this.failed(error as HttpErrorResponse);
      }
    } finally {
      if (request === this.reportRequest) this.loading.set(false);
    }
  }

  print(): void {
    window.print();
  }

  async pdfAction(): Promise<void> {
    if (this.pdfBusy()) return;
    if (this.pdfNeedsReport()) {
      await this.loadReport();
      return;
    }
    const snapshotId = this.report()?.snapshotId;
    if (!snapshotId) return;
    const request = this.reportRequest;
    const endpoint = this.endpoint();
    const file = this.pdf();
    this.pdfMessage.set('');
    if (file?.status === 'ready') {
      await this.downloadPdf(file, request, endpoint);
      return;
    }

    this.sendingPdf.set(true);
    try {
      const status = await firstValueFrom(this.http.post<PdfStatus>(endpoint + '/situation-reports', { snapshotId }));
      this.track(status, request, endpoint);
    } catch (error) {
      if (request === this.reportRequest) this.pdfFailed(error as HttpErrorResponse);
    } finally {
      if (request === this.reportRequest) this.sendingPdf.set(false);
    }
  }

  private async downloadPdf(file: PdfStatus, request: number, endpoint: string): Promise<void> {
    this.downloading.set(true);
    try {
      const blob = await firstValueFrom(this.http.get(endpoint + '/situation-reports/' + file.id + '/file', { responseType: 'blob' }));
      if (request !== this.reportRequest) return;
      const link = document.createElement('a');
      const url = URL.createObjectURL(blob);
      link.href = url;
      link.download = file.fileName ?? 'situation-report.pdf';
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (error) {
      if (request === this.reportRequest) {
        const response = error as HttpErrorResponse;
        if (response.status === 401 || response.status === 404 || response.status === 410) this.pdfFailed(response);
        else this.pdfMessage.set('The PDF could not download. Try again.');
      }
    } finally {
      if (request === this.reportRequest) this.downloading.set(false);
    }
  }

  private track(status: PdfStatus, request: number, endpoint: string): void {
    if (request !== this.reportRequest) return;
    this.pdf.set(status);
    if (status.status === 'ready') void this.downloadPdf(status, request, endpoint);
    if (status.status === 'failed') this.pdfMessage.set('The PDF could not be drawn. Try again.');
    if (status.status === 'expired') this.pdfUnavailable();
    if (status.status !== 'pending') return;
    this.timer = setTimeout(async () => {
      try {
        const next = await firstValueFrom(this.http.get<PdfStatus>(endpoint + '/situation-reports/' + status.id));
        this.track(next, request, endpoint);
      } catch (error) {
        if (request === this.reportRequest) this.pdfFailed(error as HttpErrorResponse);
      }
    }, 1000);
  }

  private pdfFailed(error: HttpErrorResponse): void {
    this.pdf.set(null);
    if (error.status === 401) { this.auth.sessionEnded(); return; }
    if (error.status === 404 || error.status === 410) {
      this.pdfUnavailable();
    } else this.pdfMessage.set(serverMessage(error) || 'The PDF could not be prepared. Try again.');
  }

  private pdfUnavailable(): void {
    this.pdfNeedsReport.set(true);
    this.pdfMessage.set('Refresh the report to prepare its PDF.');
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) { this.auth.sessionEnded(); return; }
    if (error.status === 403 && !serverMessage(error).includes('own area')) { this.workspace.deny(); return; }
    this.message.set(serverMessage(error) || 'The report could not be made. Try again.');
  }
}

function serverMessage(error: HttpErrorResponse): string {
  return typeof error.error?.error === 'string' ? error.error.error : '';
}
