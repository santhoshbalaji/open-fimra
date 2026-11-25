import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { DatePipe } from '@angular/common';
import { Component, OnInit, computed, inject, output, signal } from '@angular/core';
import { toast } from '@spartan-ng/brain/sonner';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmSkeleton } from '@spartan-ng/helm/skeleton';
import { HlmTextarea } from '@spartan-ng/helm/textarea';
import { Observable } from 'rxjs';
import { AuthService } from '../auth';
import { WEB_CONFIG } from '../config';
import { Icon } from '../ui/icon';
import { WorkspaceService } from '../workspace';
import { FormDesigner, ParentTemplate } from './form-designer';
import { TemplateRules, sameJSON } from './form-locks';
import { FormDefinition, blankForm, textOf } from './form-model';

export type Level = 'national' | 'state' | 'division';
interface Unit { hierarchy: string; path: string }
interface Ref { form: string; version: number; level?: Level }

export interface VersionSummary {
  form: string; version: number; title: Record<string, string>; level: Level; unit: Unit | null;
  status: 'draft' | 'published'; createdAt: string; updatedAt: string; publishedAt: string | null;
  extends: Ref | null; template: TemplateRules; canManage?: boolean; submissionCount?: number;
}
export interface VersionDetail extends VersionSummary { definition: FormDefinition }
export interface FormSummary {
  form: string; level: Level; unit: Unit | null; canManage: boolean; versions: VersionSummary[];
  assignments: { step: string; version: number; hierarchy: string | null; unitPaths: string[] }[];
}

/** The version open in the designer. */
interface Editing {
  definition: FormDefinition; rules: TemplateRules; parent: ParentTemplate | null; level: Level; unit: Unit | null;
  /** True once the server has the version. */
  stored: boolean; status: 'draft' | 'published'; readonly: boolean; submissionCount?: number;
}

/** A form being started: a blank one, or one that extends a template. */
interface Starting { level: Level; parent: VersionDetail | null; units: Unit[] }

export const levelWords: Record<Level, { group: string; one: string }> = {
  national: { group: 'National templates', one: 'National template' },
  state: { group: 'State forms', one: 'State form' },
  division: { group: 'Division forms', one: 'Division form' }
};

const formIdPattern = /^[a-z][a-z0-9]*(-[a-z0-9]+)*$/;

/**
 * The forms of a workspace by level, and the designer. National templates
 * arrive as files; a state extends them; a division extends the state's
 * form where the state allows it. Published versions never change, and each
 * shows how many reports were filled in on it.
 */
@Component({
  selector: 'fimra-forms-screen',
  imports: [DatePipe, FormDesigner, HlmAlertImports, HlmBadge, HlmButton, HlmCardImports, HlmInput, HlmSkeleton, HlmTextarea, Icon],
  templateUrl: './forms-screen.html',
  host: { class: 'block h-full overflow-auto' }
})
export class FormsScreen implements OnInit {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  readonly workspace = inject(WorkspaceService);
  readonly closed = output<void>();
  readonly levels: Level[] = ['national', 'state', 'division'];
  readonly levelWords = levelWords;
  readonly text = textOf;

  readonly forms = signal<FormSummary[] | null>(null);
  readonly error = signal('');
  readonly busy = signal(false);
  readonly editing = signal<Editing | null>(null);
  readonly savedDefinition = signal<FormDefinition | null>(null);
  readonly savedRules = signal<TemplateRules | null>(null);
  readonly serverProblems = signal<string[]>([]);
  readonly starting = signal<Starting | null>(null);
  readonly startError = signal('');
  readonly importing = signal(false);
  readonly importText = signal('');
  readonly confirmPublish = signal(false);

  readonly admin = computed(() => this.workspace.canManageLayers());
  /** The units where this user can own division forms. */
  readonly divisionUnits = computed<Unit[]>(() => this.workspace.roles()
    .filter(role => (role.role === 'division_officer' || role.role === 'state_admin') && role.path.includes('.'))
    .map(role => ({ hierarchy: role.hierarchy, path: role.path })));
  readonly byLevel = computed(() => {
    const forms = this.forms() ?? [];
    return this.levels.map(level => ({ level, forms: forms.filter(f => f.level === level) }));
  });
  readonly dirty = computed(() => {
    const editing = this.editing();
    return !!editing && !editing.readonly && (!editing.stored ||
      !sameJSON(editing.definition, this.savedDefinition()) || !sameJSON(editing.rules, this.savedRules()));
  });

  private base(): string {
    return this.api + '/api/v1/workspaces/' + encodeURIComponent(this.workspace.current()?.slug ?? '') + '/forms';
  }

  ngOnInit(): void { this.load(); }

  load(): void {
    this.http.get<FormSummary[]>(this.base()).subscribe({
      next: forms => this.forms.set(forms),
      error: (error: HttpErrorResponse) => this.failed(error)
    });
  }

  private failed(error: HttpErrorResponse, problems = false): void {
    this.busy.set(false);
    if (error.status === 401) { this.auth.sessionEnded(); return; }
    const body = error.error as { error?: string; problems?: string[] } | null;
    if (problems && body?.problems) {
      this.serverProblems.set(body.problems);
      this.error.set(body.error ?? 'The server did not accept the form.');
      return;
    }
    this.error.set(body?.error ?? (error.status === 0 ? 'The server cannot be reached. Try again.' : 'Something went wrong. Try again.'));
  }

  title(form: FormSummary): string {
    const latest = form.versions[form.versions.length - 1];
    return textOf(latest?.title) || form.form;
  }

  latest(form: FormSummary): VersionSummary | undefined { return form.versions[form.versions.length - 1]; }
  hasDraft(form: FormSummary): boolean { return form.versions.some(v => v.status === 'draft'); }

  /** States extend national templates; divisions extend state forms. */
  canExtend(version: VersionSummary): boolean {
    if (version.status !== 'published' || !version.template.allowExtension) return false;
    if (version.level === 'national') return this.admin();
    return version.level === 'state' && (this.admin() || this.divisionUnits().length > 0);
  }

  private getVersion(form: string, version: number): Observable<VersionDetail> {
    return this.http.get<VersionDetail>(this.base() + '/' + encodeURIComponent(form) + '/versions/' + version);
  }

  private parentOf(detail: VersionDetail): ParentTemplate {
    return { form: detail.form, version: detail.version, level: detail.level, definition: detail.definition, rules: detail.template };
  }

  /** Opens a stored version: a draft to edit, or a published version to read. */
  open(form: FormSummary, version: VersionSummary): void {
    this.error.set('');
    this.busy.set(true);
    this.getVersion(form.form, version.version).subscribe({
      next: detail => {
        const show = (parent: ParentTemplate | null) => {
          this.busy.set(false);
          this.edit({ definition: detail.definition, rules: detail.template, parent, level: detail.level, unit: detail.unit, stored: true,
            status: detail.status, readonly: detail.status === 'published' || !form.canManage || detail.level === 'national',
            submissionCount: detail.submissionCount });
        };
        if (!detail.extends) { show(null); return; }
        this.getVersion(detail.extends.form, detail.extends.version).subscribe({
          next: parent => show(this.parentOf(parent)),
          error: (error: HttpErrorResponse) => this.failed(error)
        });
      },
      error: (error: HttpErrorResponse) => this.failed(error)
    });
  }

  private edit(editing: Editing): void {
    this.editing.set(editing);
    this.savedDefinition.set(editing.stored ? structuredClone(editing.definition) : null);
    this.savedRules.set(editing.stored ? structuredClone(editing.rules) : null);
    this.serverProblems.set([]);
    this.confirmPublish.set(false);
    this.starting.set(null);
  }

  /** The next version of a form, starting from its latest version. */
  newVersion(form: FormSummary): void {
    const latest = this.latest(form);
    if (!latest) return;
    this.busy.set(true);
    this.getVersion(form.form, latest.version).subscribe({
      next: detail => {
        const definition = { ...structuredClone(detail.definition), version: detail.version + 1 };
        const start = (parent: ParentTemplate | null) => {
          this.busy.set(false);
          this.edit({ definition, rules: structuredClone(detail.template), parent, level: detail.level, unit: detail.unit,
            stored: false, status: 'draft', readonly: false });
        };
        if (!detail.extends) { start(null); return; }
        this.getVersion(detail.extends.form, detail.extends.version).subscribe({
          next: parent => start(this.parentOf(parent)),
          error: (error: HttpErrorResponse) => this.failed(error)
        });
      },
      error: (error: HttpErrorResponse) => this.failed(error)
    });
  }

  startBlank(): void {
    this.startError.set('');
    this.starting.set({ level: 'state', parent: null, units: [] });
  }

  startExtension(version: VersionSummary): void {
    this.startError.set('');
    this.busy.set(true);
    this.getVersion(version.form, version.version).subscribe({
      next: detail => {
        this.busy.set(false);
        const level: Level = detail.level === 'national' ? 'state' : 'division';
        this.starting.set({ level, parent: detail, units: level === 'division' ? this.divisionUnits() : [] });
      },
      error: (error: HttpErrorResponse) => this.failed(error)
    });
  }

  /** Checks the new form's id and opens it in the designer. */
  begin(form: HTMLFormElement): void {
    const start = this.starting();
    if (!start) return;
    const data = new FormData(form);
    const id = (data.get('id')?.toString() ?? '').trim();
    const title = (data.get('title')?.toString() ?? '').trim();
    const unitPath = (data.get('unit')?.toString() ?? '').trim();
    if (!formIdPattern.test(id) || id.length > 60) {
      this.startError.set('The id uses small letters and digits with single hyphens between words, such as demo-first-report.');
      return;
    }
    if (this.forms()?.some(f => f.form === id)) {
      this.startError.set(`The id "${id}" is already used by a form in this workspace.`);
      return;
    }
    if (!title) {
      this.startError.set('Give the form a title.');
      return;
    }
    let unit: Unit | null = null;
    if (start.level === 'division') {
      unit = start.units.find(u => u.path === unitPath) ?? (unitPath ? { hierarchy: start.units[0]?.hierarchy || 'territorial', path: unitPath } : null);
      if (!unit) {
        this.startError.set('Choose the division the form is for.');
        return;
      }
    }
    const parent = start.parent;
    const definition: FormDefinition = parent
      ? { ...structuredClone(parent.definition), id, version: 1, title: { ...parent.definition.title, en: title } }
      : blankForm(id, title);
    this.edit({ definition, rules: { allowExtension: false, locked: [...(parent?.template.locked ?? [])] },
      parent: parent ? this.parentOf(parent) : null, level: start.level, unit, stored: false, status: 'draft', readonly: false });
  }

  setDefinition(definition: FormDefinition): void {
    this.editing.update(editing => editing ? { ...editing, definition } : editing);
  }

  setRules(rules: TemplateRules): void {
    this.editing.update(editing => editing ? { ...editing, rules } : editing);
  }

  private envelope(editing: Editing): Record<string, unknown> {
    return {
      level: editing.level,
      ...(editing.unit ? { hierarchy: editing.unit.hierarchy, unitPath: editing.unit.path } : {}),
      ...(editing.parent ? { extends: { form: editing.parent.form, version: editing.parent.version } } : {}),
      template: editing.rules,
      definition: editing.definition
    };
  }

  /** Saves the draft: the first save adds the version, later saves replace it. */
  save(then?: () => void): void {
    const editing = this.editing();
    if (!editing || editing.readonly) return;
    this.busy.set(true);
    this.error.set('');
    this.serverProblems.set([]);
    const url = this.base() + '/' + encodeURIComponent(editing.definition.id) + '/versions/' + editing.definition.version;
    const request = editing.stored
      ? this.http.put<VersionSummary>(url, this.envelope(editing))
      : this.http.post<VersionSummary>(this.base(), this.envelope(editing));
    request.subscribe({
      next: saved => {
        this.busy.set(false);
        this.editing.update(current => current ? { ...current, stored: true, rules: saved.template } : current);
        this.savedDefinition.set(structuredClone(editing.definition));
        this.savedRules.set(structuredClone(saved.template));
        this.load();
        if (then) then();
        else toast.success('Draft saved');
      },
      error: (error: HttpErrorResponse) => this.failed(error, true)
    });
  }

  /** Publishing freezes the version. Unsaved changes are saved first. */
  publish(): void {
    const editing = this.editing();
    if (!editing || editing.readonly) return;
    if (this.dirty()) { this.save(() => this.publish()); return; }
    this.busy.set(true);
    const url = this.base() + '/' + encodeURIComponent(editing.definition.id) + '/versions/' + editing.definition.version + '/publish';
    this.http.post<VersionSummary>(url, {}).subscribe({
      next: () => {
        this.busy.set(false);
        this.confirmPublish.set(false);
        this.editing.update(current => current ? { ...current, status: 'published', readonly: true } : current);
        toast.success(`Published ${editing.definition.id} version ${editing.definition.version}`);
        this.load();
      },
      error: (error: HttpErrorResponse) => this.failed(error, true)
    });
  }

  closeDesigner(): void {
    if (this.dirty() && !confirm('Leave without saving your changes?')) return;
    this.editing.set(null);
    this.error.set('');
    this.serverProblems.set([]);
  }

  // National templates arrive as files from the national body.

  setImportText(event: Event): void { this.importText.set((event.target as HTMLTextAreaElement).value); }

  readImportFile(event: Event): void {
    const file = (event.target as HTMLInputElement).files?.[0];
    if (!file) return;
    void file.text().then(text => this.importText.set(text));
  }

  importNational(): void {
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(this.importText()) as Record<string, unknown>;
    } catch {
      this.error.set('The template file is not valid JSON.');
      return;
    }
    const body = 'definition' in parsed
      ? { level: 'national', definition: parsed['definition'], ...(parsed['template'] ? { template: parsed['template'] } : {}) }
      : { level: 'national', definition: parsed };
    this.busy.set(true);
    this.error.set('');
    this.serverProblems.set([]);
    this.http.post<VersionSummary>(this.base(), body).subscribe({
      next: added => {
        this.busy.set(false);
        this.importing.set(false);
        this.importText.set('');
        toast.success(`Added the national template ${added.form} version ${added.version}`);
        this.load();
      },
      error: (error: HttpErrorResponse) => this.failed(error, true)
    });
  }

  /** The template file of a published version, for a national body to share or a state to keep. */
  download(version: VersionSummary): void {
    this.getVersion(version.form, version.version).subscribe({
      next: detail => {
        const file = { level: detail.level, template: detail.template, definition: detail.definition };
        const url = URL.createObjectURL(new Blob([JSON.stringify(file, null, 2) + '\n'], { type: 'application/json' }));
        const link = document.createElement('a');
        link.href = url;
        link.download = `${detail.form}-version-${detail.version}.json`;
        link.click();
        setTimeout(() => URL.revokeObjectURL(url));
      },
      error: (error: HttpErrorResponse) => this.failed(error)
    });
  }
}
