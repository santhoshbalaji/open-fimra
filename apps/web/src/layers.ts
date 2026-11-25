import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, OnInit, computed, inject, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { BrnButton } from '@spartan-ng/brain/button';
import { Observable, Subscription, exhaustMap, finalize, takeWhile, timer } from 'rxjs';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { WorkspaceService } from './workspace';

type Role = 'hierarchy' | 'forest' | 'exclusion' | 'analysis';
type Status = 'pending' | 'ready' | 'failed' | 'published' | 'retired';
interface Layer { slug: string; name: string; role: Role; hierarchy: string | null; depth: number | null }
interface Hierarchy { slug: string; name: string; levels: { depth: number; name: string }[] | null }
interface Upload { versionId: string; layerName: string; version: number; sourceFile: string; status: Status }
interface Catalog { layers: Layer[]; hierarchies: Hierarchy[]; uploads: Upload[] }
interface Problem { kind: string; featureRef: string | null; message: string; blocksPublish: boolean }
interface Draft {
  versionId: string; status: Status; role: Role; sourceCrs: string | null;
  featureCount: number; unitCount: number; problems: Problem[];
  rematch?: { status: 'pending' | 'running' | 'completed' | 'failed'; processed: number; alertsCreated: number };
}
interface Change {
  detectionId: string; observedAt: string; oldClass: string; newClass: string;
  oldBeats: string[]; newBeats: string[]; oldTriage: string | null; newTriage: string | null;
}
const categories = [
  { key: 'included', label: 'Newly included' },
  { key: 'excluded', label: 'Newly excluded' },
  { key: 'reassigned', label: 'Beat or class changed' },
  { key: 'triage', label: 'Triage changed' }
] as const;
interface Preview {
  days: number; scanned: number; blocked: string; grantConfirmation: string;
  changes: Partial<Record<typeof categories[number]['key'], { count: number; items: Change[] }>>;
  brokenGrants: { userId: string; username: string; role: string; unitPath: string }[];
}

@Component({
  selector: 'fimra-layers',
  imports: [BrnButton, DatePipe],
  templateUrl: './layers.html',
  styleUrl: './layers.css'
})
export class Layers implements OnInit {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly destroy = inject(DestroyRef);
  private readonly workspace = inject(WorkspaceService).current();
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '') +
    '/api/v1/workspaces/' + encodeURIComponent(this.workspace?.slug ?? '') + '/layer-imports';
  private watch?: Subscription;
  readonly categories = categories;
  readonly roleLabels: Record<Role, string> = {
    hierarchy: 'Hierarchy boundaries', forest: 'Forest eligibility', exclusion: 'Known heat sources', analysis: 'Analysis'
  };
  readonly roles = ['forest', 'hierarchy', 'exclusion', 'analysis'] as const;
  readonly statusLabels: Record<Status, string> = {
    pending: 'Importing', ready: 'Ready for review', failed: 'Import failed', published: 'Published', retired: 'Retired'
  };
  readonly catalog = signal<Catalog | null>(null);
  readonly chosenLayer = signal('');
  readonly chosenRole = signal<Role>('forest');
  readonly chosenHierarchy = signal('');
  readonly layer = computed(() => this.catalog()?.layers.find(item => item.slug === this.chosenLayer()));
  readonly role = computed(() => this.layer()?.role ?? this.chosenRole());
  readonly hierarchy = computed(() => this.catalog()?.hierarchies.find(item =>
    item.slug === (this.layer()?.hierarchy ?? this.chosenHierarchy())));
  readonly draft = signal<Draft | null>(null);
  readonly version = signal('');
  readonly days = signal(30);
  readonly preview = signal<Preview | null>(null);
  readonly confirming = signal(false);
  readonly removeGrants = signal(false);
  readonly busy = signal(false);
  readonly loadingDraft = signal(false);
  readonly error = signal('');
  readonly hasBlockingProblems = computed(() => this.draft()?.problems.some(problem => problem.blocksPublish) ?? false);

  ngOnInit(): void {
    this.loadCatalog();
  }

  loadCatalog(): void {
    this.http.get<Catalog>(this.base).pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: catalog => this.catalog.set(catalog), error: error => this.failed(error)
    });
  }

  upload(form: HTMLFormElement): void {
    const data = new FormData(form);
    const layer = this.layer();
    const settings: Record<string, string | number> = {};
    for (const field of ['nameField', 'codeField', 'parentCodeField', 'sourceLayer']) {
      const value = String(data.get(field) ?? '').trim();
      if (value) settings[field] = value;
      data.delete(field);
    }
    if (layer) {
      data.set('layerSlug', layer.slug);
      data.set('layerName', layer.name);
    }
    data.set('role', this.role());
    if (this.role() === 'hierarchy') {
      settings['depth'] = layer?.depth ?? Number(data.get('depth'));
      data.set('hierarchy', this.hierarchy()?.slug ?? '');
    }
    data.delete('depth');
    data.set('settings', JSON.stringify(settings));
    this.run(this.http.post<Draft>(this.base, data), draft => {
      this.open(draft.versionId, draft);
      this.loadCatalog();
    });
  }

  open(version: string, initial?: Draft): void {
    this.watch?.unsubscribe();
    this.resetPreview();
    this.error.set('');
    this.version.set(version);
    this.draft.set(initial ?? null);
    this.loadingDraft.set(!initial);
    const needsRefresh = (draft: Draft) => draft.status === 'pending' ||
      draft.rematch?.status === 'pending' || draft.rematch?.status === 'running';
    this.watch = timer(initial ? 1000 : 0, 1000).pipe(
      exhaustMap(() => this.http.get<Draft>(this.versionURL())),
      takeWhile(needsRefresh, true),
      takeUntilDestroyed(this.destroy)
    ).subscribe({
      next: draft => {
        this.loadingDraft.set(false);
        this.draft.set(draft);
        if (!needsRefresh(draft)) this.loadCatalog();
      },
      error: error => { this.loadingDraft.set(false); this.failed(error); }
    });
  }

  selectRole(role: string): void {
    const selection = this.roles.find(item => item === role);
    if (selection) this.chosenRole.set(selection);
  }

  changeDays(days: number): void {
    this.days.set(days);
    this.resetPreview();
  }

  previewDraft(): void {
    this.resetPreview();
    this.run(this.http.get<Preview>(this.versionURL() + '/preview', { params: { days: this.days() } }),
      preview => this.preview.set(preview));
  }

  publish(): void {
    const preview = this.preview();
    if (!preview || preview.blocked || this.hasBlockingProblems() || !this.confirming() ||
        (preview.brokenGrants.length && !this.removeGrants())) return;

    this.run(this.http.post(this.versionURL() + '/publish',
      { days: preview.days, grantConfirmation: this.removeGrants() ? preview.grantConfirmation : '' }),
    () => this.open(this.version()));
  }

  changeNote(change: Change): string {
    const labels: Record<string, string> = {
      inside: 'Inside forest', near_edge: 'Near the edge', outside: 'Outside forest',
      fixed_heat_source: 'Known heat source', shared_boundary: 'Shared boundary',
      overlapping_units: 'Overlapping units', no_unit: 'No mapped beat'
    };
    const changes = [];
    if (change.oldClass !== change.newClass) {
      changes.push(labels[change.oldClass] + ' → ' + labels[change.newClass]);
    }
    if (change.oldBeats.join(',') !== change.newBeats.join(',')) {
      changes.push((change.oldBeats.join(', ') || 'No beat') + ' → ' + (change.newBeats.join(', ') || 'No beat'));
    }
    if (change.oldTriage !== change.newTriage) {
      changes.push((change.oldTriage ? labels[change.oldTriage] : 'No triage') + ' → ' +
        (change.newTriage ? labels[change.newTriage] : 'No triage'));
    }
    return changes.join(' · ');
  }

  private versionURL(): string {
    return this.base + '/' + encodeURIComponent(this.version());
  }

  private resetPreview(): void {
    this.preview.set(null);
    this.confirming.set(false);
    this.removeGrants.set(false);
  }

  private run<T>(request: Observable<T>, next: (value: T) => void): void {
    this.busy.set(true);
    this.error.set('');
    request.pipe(takeUntilDestroyed(this.destroy), finalize(() => this.busy.set(false))).subscribe({
      next,
      error: (error: HttpErrorResponse) => {
        if (error.status === 409) this.resetPreview();
        this.failed(error);
      }
    });
  }

  private failed(error: HttpErrorResponse): void {
    if (error.status === 401) this.auth.sessionEnded();
    this.error.set(typeof error.error?.error === 'string' ? error.error.error : 'The layer request failed. Try again.');
  }
}
