import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmLabelImports } from '@spartan-ng/helm/label';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { AuthService } from './auth';
import { classes, ForestClass } from './detections';

export const exportFormats = {
  gpkg: 'GeoPackage (recommended)',
  shapefile: 'Shapefile (zipped)',
  kml: 'KML for Google Earth (KMZ)',
  geojson: 'GeoJSON (zipped)',
  flatgeobuf: 'FlatGeobuf (zipped)',
  csv_wkt: 'CSV with the point as well-known text (WKT, zipped)',
  csv_lonlat: 'CSV with longitude and latitude columns (zipped)',
  xlsx: 'Excel workbook'
};
export type ExportFormat = keyof typeof exportFormats;
type Kind = 'alerts' | 'detections' | 'submissions';
export interface ExportStatus {
  id: string;
  status: 'pending' | 'ready' | 'failed' | 'expired';
  rowCount: number;
  featureCount: number | null;
  fileBytes: number | null;
  fileName?: string;
  expiresAt: string;
}

const until = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
const allClasses = Object.keys(classes) as ForestClass[];

@Component({
  selector: 'fimra-export-dialog',
  imports: [HlmButtonImports, HlmFieldImports, HlmLabelImports, HlmRadioGroupImports, HlmSelectImports],
  templateUrl: './export-dialog.html'
})
export class ExportDialog {
  /** The workspace's API address, ending in /api/v1/workspaces/{slug}. */
  readonly endpoint = input.required<string>();
  readonly hours = input.required<number>();
  /** One forest class, or '' for all, as in the shared filters. */
  readonly forestClass = input.required<ForestClass | ''>();
  /** A unit path prefix, or '' for all units. */
  readonly unitPath = input.required<string>();
  readonly queue = input.required<string>();
  readonly queueLabel = input.required<string>();
  /** What the dialog offers first: the panel beside the map decides. */
  readonly initialKind = input<Kind>('alerts');
  /** The reports list's own filters: one form, and a decision. */
  readonly form = input('');
  readonly formLabel = input('');
  readonly decision = input('');
  readonly closed = output<void>();

  readonly formatLabels = exportFormats;
  readonly formats = Object.entries(exportFormats) as [ExportFormat, string][];
  readonly kind = signal<Kind>('alerts');
  readonly decisionWords: Record<string, string> = { pending: 'waiting for a decision', approved: 'approved', returned: 'returned' };
  readonly format = signal<ExportFormat>('gpkg');
  readonly onlyQueue = signal(true);
  readonly status = signal<ExportStatus | null>(null);
  readonly busy = signal(false);
  readonly error = signal('');
  private readonly lifetime = inject(DestroyRef);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private timer?: ReturnType<typeof setTimeout>;
  private readonly classList = computed<ForestClass[]>(() => this.forestClass() ? [this.forestClass() as ForestClass] : allClasses);
  readonly summary = computed(() => {
    const hours = this.hours();
    const reports = this.kind() === 'submissions';
    const parts = [(reports ? 'received' : 'observed') + ' in the past ' + (hours % 24 === 0 && hours > 24 ? `${hours / 24} days` : `${hours} hours`)];
    parts.push(this.forestClass() ? classes[this.forestClass() as ForestClass] : 'all forest classes');
    if (this.unitPath()) parts.push('units under ' + this.unitPath());
    if (this.kind() === 'alerts') parts.push(this.onlyQueue() ? this.queueLabel() + ' queue' : 'all queues you can see');
    if (reports) {
      parts.push(this.form() ? (this.formLabel() || this.form()) + ' reports, with a column for each answer' : 'reports on every form');
      if (this.decision()) parts.push(this.decisionWords[this.decision()] ?? this.decision());
    }
    return parts.join(' · ');
  });
  readonly progress = computed(() => {
    const status = this.status();
    if (!status) return '';
    if (status.status === 'pending') return `Preparing ${rows(status.rowCount)}…`;
    if (status.status === 'ready') {
      return `Ready: ${rows(status.featureCount ?? status.rowCount)}, ${size(status.fileBytes ?? 0)}. ` +
        `Available until ${until.format(new Date(status.expiresAt))}.`;
    }
    if (status.status === 'expired') return 'This export has expired. Prepare it again.';
    return 'The file could not be prepared. Try again.';
  });

  constructor() {
    this.lifetime.onDestroy(() => clearTimeout(this.timer));
    effect(() => {
      const kind = this.initialKind();
      untracked(() => this.kind.set(kind));
    });
  }

  choose(kind: Kind): void {
    this.kind.set(kind);
    this.reset();
  }

  pick(format: string): void {
    this.format.set(format as ExportFormat);
    this.reset();
  }

  scope(only: boolean): void {
    this.onlyQueue.set(only);
    this.reset();
  }

  async prepare(): Promise<void> {
    this.reset();
    this.busy.set(true);
    const body: Record<string, unknown> = { kind: this.kind(), format: this.format(), hours: this.hours() };
    body['forestClasses'] = this.classList();
    if (this.unitPath()) body['unitPath'] = this.unitPath();
    if (this.kind() === 'alerts' && this.onlyQueue()) body['queue'] = this.queue();
    if (this.kind() === 'submissions') {
      if (this.form()) body['form'] = this.form();
      if (this.decision()) body['decision'] = this.decision();
    }
    try {
      this.track(await firstValueFrom(this.http.post<ExportStatus>(this.endpoint() + '/exports', body)));
    } catch (error) {
      this.busy.set(false);
      this.failed(error as HttpErrorResponse);
    }
  }

  async download(): Promise<void> {
    const status = this.status();
    if (!status?.fileName) return;
    this.error.set('');
    try {
      const file = await firstValueFrom(this.http.get(this.endpoint() + '/exports/' + status.id + '/file', { responseType: 'blob' }));
      const link = document.createElement('a');
      link.href = URL.createObjectURL(file);
      link.download = status.fileName;
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 60_000);
    } catch (error) {
      this.failed(error as HttpErrorResponse);
    }
  }

  close(): void {
    this.closed.emit();
  }

  private track(status: ExportStatus): void {
    if (this.lifetime.destroyed) return;
    this.status.set(status);
    this.busy.set(status.status === 'pending');
    if (status.status !== 'pending') return;
    this.timer = setTimeout(async () => {
      try {
        this.track(await firstValueFrom(this.http.get<ExportStatus>(this.endpoint() + '/exports/' + status.id)));
      } catch (error) {
        this.busy.set(false);
        this.failed(error as HttpErrorResponse);
      }
    }, 1000);
  }

  private reset(): void {
    clearTimeout(this.timer);
    this.status.set(null);
    this.error.set('');
    this.busy.set(false);
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) { this.auth.sessionEnded(); return; }
    const message = typeof error.error?.error === 'string' ? error.error.error : '';
    if (error.status === 403) this.error.set('Your role cannot export from this workspace.');
    else if (error.status === 410) { this.status.set(null); this.error.set('This export has expired. Prepare it again.'); }
    else if (error.status === 422 && message) this.error.set(message);
    else this.error.set('The export could not be prepared. Try again.');
  }
}

function rows(count: number): string {
  return count === 1 ? '1 row' : `${count.toLocaleString()} rows`;
}

function size(bytes: number): string {
  if (bytes < 1024) return `${bytes} bytes`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
