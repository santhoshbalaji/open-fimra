import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmCheckboxImports } from '@spartan-ng/helm/checkbox';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { HlmLabelImports } from '@spartan-ng/helm/label';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { NgTemplateOutlet } from '@angular/common';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, ViewEncapsulation, DestroyRef, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { AnswerSession, FormTree, readForm, type EntryPath, type Node, type StoredAttachment } from '@open-fimra/form-schema/answers';
import { toast } from '@spartan-ng/brain/sonner';
import { BrnField } from '@spartan-ng/brain/field';
import { firstValueFrom } from 'rxjs';
import type { AlertRow } from '../alert-queue';
import { AuthService } from '../auth';
import { alertFacts, fileSize, serverProblems, type FormVersion, type Submission } from './form-api';
import { entryName, explain, formLanguages, itemName, languageName, pathId, startLanguage, textIn, type Problem } from './form-text';
import { ShapeInput, positionsOf } from './shape-input';
import { SignaturePad } from './signature-pad';

type Obj = Record<string, unknown>;
interface Choice { key: string; label: Record<string, string>; parent?: string }
type Path = [string, number][];
interface Page { key: string; title: string; sections: Node[] }
/** An uploaded file, with a preview address for images. */
interface Upload extends StoredAttachment { preview?: string }

const imageTypes = ['image/jpeg', 'image/png', 'image/webp'];
const pad = (n: number) => String(n).padStart(2, '0');

/** The answers object at an entry path, or an empty object. */
function scopeAt(values: Obj, path: EntryPath): Obj {
  let at: unknown = values;
  for (const [key, index] of path) at = ((at as Obj)?.[key] as Obj[] | undefined)?.[index];
  return (at && typeof at === 'object' ? at : {}) as Obj;
}

/** A copy of the answers with one value changed at an entry path. */
function setIn(values: Obj, path: EntryPath, key: string, value: unknown): Obj {
  if (!path.length) {
    const copy = { ...values };
    if (value === undefined || value === null || value === '') delete copy[key];
    else copy[key] = value;
    return copy;
  }
  const [[group, index], ...rest] = path;
  const entries = [...((values[group] as Obj[] | undefined) ?? [])];
  entries[index] = setIn(entries[index] ?? {}, rest, key, value);
  return { ...values, [group]: entries };
}

/** A date and time from a datetime-local input, with the browser's offset from universal time. */
export function withOffset(local: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(local)) return null;
  const date = new Date(local);
  if (Number.isNaN(date.getTime())) return null;
  const minutes = -date.getTimezoneOffset();
  const sign = minutes >= 0 ? '+' : '-';
  return local.slice(0, 16) + ':00' + sign + pad(Math.floor(Math.abs(minutes) / 60)) + ':' + pad(Math.abs(minutes) % 60);
}

/** The value for a datetime-local input, in the browser's time zone. */
export function toLocalInput(value: unknown): string {
  if (typeof value !== 'string') return '';
  const date = new Date(value.length === 16 ? value + ':00Z' : value);
  if (Number.isNaN(date.getTime())) return '';
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/**
 * Fills in a form version (form format, sections 6 to 12): every
 * field type, conditions, repeating groups, stages, pages, widgets and
 * translated labels. Answers are checked with the server's rules as people
 * type; photos, signatures and files upload to the alert first.
 */
@Component({
  selector: 'fimra-form-fill',
  imports: [BrnField, HlmButtonImports, HlmCheckboxImports, HlmFieldImports, HlmInputImports, HlmLabelImports, HlmRadioGroupImports, HlmSelectImports,
    HlmSkeletonImports, HlmTextareaImports, NgTemplateOutlet, ShapeInput, SignaturePad],
  templateUrl: './form-fill.html',
  styleUrl: './forms.css',
  encapsulation: ViewEncapsulation.None,
  host: { class: 'form-fill' }
})
export class FormFill {
  /** The workspace's API address, ending in /api/v1/workspaces/{slug}. */
  readonly workspaceUrl = input.required<string>();
  readonly form = input.required<string>();
  readonly version = input.required<number>();
  /** The workflow step being filled in, or the step a shown report was filled in at. */
  readonly step = input.required<string>();
  /** The alert, when filling in. It gives the alert values and where files upload. */
  readonly alert = input<AlertRow | null>(null);
  /** Earlier answers to keep. */
  readonly initial = input<Obj>({});
  /** The form file, when the caller has it already. */
  readonly definition = input<Obj | null>(null);
  readonly submitted = output<Submission>();
  readonly cancelled = output<void>();

  readonly loaded = signal<Obj | null>(null);
  readonly loadError = signal('');
  readonly values = signal<Obj>({});
  readonly uploads = signal<Record<string, Upload | null>>({});
  readonly uploading = signal<string | null>(null);
  readonly language = signal('en');
  readonly page = signal(0);
  readonly tried = signal(false);
  readonly busy = signal(false);
  readonly serverMessage = signal('');
  readonly serverList = signal<string[]>([]);

  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly lifetime = inject(DestroyRef);
  private readonly paths = new Map<string, Path>();
  private submissionId = crypto.randomUUID();
  private deviceTime = '';
  private lastSent = '';

  readonly file = computed(() => this.definition() ?? this.loaded());
  readonly tree = computed<FormTree | null>(() => { const f = this.file(); return f ? readForm(f) : null; });
  readonly languages = computed(() => { const f = this.file(); return f ? formLanguages(f) : ['en']; });
  readonly title = computed(() => textIn(this.file()?.['title'], this.language()));
  readonly description = computed(() => textIn(this.file()?.['description'], this.language()));
  readonly stage = computed(() => this.tree()?.stageAt(this.step()) ?? -1);
  readonly stageTitle = computed(() => {
    const tree = this.tree();
    const stages = this.file()?.['stages'] as Obj[] | undefined;
    return tree?.hasStages && stages ? textIn(stages[this.stage()]?.['title'], this.language()) : '';
  });
  readonly alertKey = computed(() => this.alert()?.id);
  readonly session = computed(() => {
    const tree = this.tree();
    if (!tree) return null;
    const alert = this.alert();
    return new AnswerSession(tree, this.values(), {
      step: this.step(), alert: alert ? alertFacts(alert) : undefined, attachments: this.uploads()
    });
  });
  readonly layoutItems = computed(() => {
    const out = new Map<string, Obj>();
    for (const item of ((this.file()?.['layout'] as Obj | undefined)?.['items'] as Obj[] | undefined) ?? []) out.set(item['item'] as string, item);
    return out;
  });
  /** The pages that have something to show at this step. */
  readonly pages = computed<Page[]>(() => {
    const tree = this.tree();
    const session = this.session();
    if (!tree || !session) return [];
    const listed = ((this.file()?.['layout'] as Obj | undefined)?.['pages'] as Obj[] | undefined);
    const pages: Page[] = listed
      ? listed.map(p => ({ key: p['key'] as string, title: textIn(p['title'], this.language()),
          sections: (p['sections'] as string[]).map(key => tree.byKey.get(key) as Node) }))
      : tree.sections.map(s => ({ key: s.key, title: '', sections: [s] }));
    return pages.map(p => ({ ...p, sections: p.sections.filter(s => session.visible(s.key)) })).filter(p => p.sections.length);
  });
  readonly currentPage = computed(() => this.pages()[Math.min(this.page(), Math.max(0, this.pages().length - 1))]);
  readonly problems = computed<Problem[]>(() => {
    const tree = this.tree();
    return (this.session()?.problems ?? []).map(message => explain(message, tree, this.language()));
  });
  readonly serverProblems = computed<Problem[]>(() => this.serverList().map(message => explain(message, this.tree(), this.language())));
  readonly languageName = languageName;

  constructor() {
    effect(onCleanup => {
      const url = this.workspaceUrl() + '/forms/' + encodeURIComponent(this.form()) + '/versions/' + this.version();
      if (this.definition()) return;
      this.loaded.set(null);
      this.loadError.set('');
      const request = this.http.get<FormVersion>(url).subscribe({
        next: version => this.loaded.set(version.definition),
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.loadError.set(error.status === 404 ? 'This form version is not published in this workspace.' : 'The form could not load. Try again.');
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      const tree = this.tree();
      const initial = this.initial();
      if (!tree) return;
      untracked(() => {
        this.language.set(startLanguage(this.languages()));
        this.values.set(this.startValues(tree, initial));
        // Attachments in the starting answers were uploaded earlier for this alert.
        const known: Record<string, Upload | null> = {};
        for (const id of this.attachmentIds(initial)) known[id] = null;
        this.uploads.set(known);
        this.page.set(0);
      });
    });
    this.lifetime.onDestroy(() => {
      for (const upload of Object.values(this.uploads())) if (upload?.preview) URL.revokeObjectURL(upload.preview);
    });
  }

  /** The starting answers: earlier answers, and the fewest entries each repeating group needs. */
  private startValues(tree: FormTree, initial: Obj): Obj {
    const fill = (members: Node[], values: Obj): Obj => {
      const out = { ...values };
      for (const n of members) {
        if (n.kind !== 'repeat') continue;
        const least = typeof n.node['minRepeats'] === 'number' ? n.node['minRepeats'] as number : 0;
        const entries = [...((out[n.key] as Obj[] | undefined) ?? [])];
        while (entries.length < least) entries.push({});
        if (entries.length) out[n.key] = entries.map(entry => fill(n.repeatChildren, entry));
      }
      return out;
    };
    return fill(tree.top, initial);
  }

  private attachmentIds(values: Obj): string[] {
    const tree = this.tree();
    const ids: string[] = [];
    const walk = (scope: Obj) => {
      for (const [key, value] of Object.entries(scope)) {
        const kind = tree?.byKey.get(key)?.kind;
        if (kind === 'repeat' && Array.isArray(value)) value.forEach(entry => walk(entry as Obj));
        else if ((kind === 'photo' || kind === 'file') && Array.isArray(value)) ids.push(...value.filter(v => typeof v === 'string'));
        else if (kind === 'signature' && typeof value === 'string') ids.push(value);
      }
    };
    walk(values);
    return ids;
  }

  text(value: unknown): string {
    return textIn(value, this.language());
  }

  name(node: Node): string {
    return itemName(node, this.language());
  }

  entryTitle(node: Node, index: number): string {
    return entryName(node, index, this.language());
  }

  /** The same path object each time, so the template does not rebuild entries. */
  entryPath(path: Path, key: string, index: number): Path {
    const id = pathId(path) + '|' + key + '-' + index;
    let found = this.paths.get(id);
    if (!found) {
      found = [...path, [key, index]];
      this.paths.set(id, found);
    }
    return found;
  }

  fieldId(node: Node, path: Path): string {
    return 'field-' + node.key + (path.length ? '-' + pathId(path) : '');
  }

  layout(node: Node): Obj {
    return this.layoutItems().get(node.key) ?? {};
  }

  visible(node: Node, path: Path): boolean {
    return this.session()?.visible(node.key, path) ?? false;
  }

  required(node: Node, path: Path): boolean {
    return this.session()?.isRequired(node.key, path) ?? false;
  }

  /** Answers from an earlier stage are kept and shown read-only. */
  locked(node: Node): boolean {
    const tree = this.tree();
    return !!tree?.hasStages && node.stage < this.stage();
  }

  value(node: Node, path: Path): unknown {
    if (node.kind === 'calculated' || node.kind === 'alert-value') return this.session()?.value(node.key, path);
    return scopeAt(this.values(), path)[node.key];
  }

  textValue(node: Node, path: Path): string {
    const v = this.value(node, path);
    return v === undefined || v === null ? '' : String(v);
  }

  set(node: Node, path: Path, value: unknown): void {
    this.values.update(values => setIn(values, path, node.key, value));
    this.serverMessage.set('');
  }

  setNumber(node: Node, path: Path, raw: string): void {
    this.set(node, path, raw.trim() === '' ? null : Number(raw));
  }

  nudge(node: Node, path: Path, by: number): void {
    const current = this.value(node, path);
    const next = (typeof current === 'number' ? current : 0) + by;
    this.set(node, path, Math.round(next * 1e6) / 1e6);
  }

  setDateTime(node: Node, path: Path, local: string): void {
    this.set(node, path, local ? withOffset(local) ?? local : null);
  }

  choices(node: Node, path: Path): Choice[] {
    return (this.session()?.choices(node.key, path) ?? []) as unknown as Choice[];
  }

  chosen(node: Node, path: Path, key: string): boolean {
    const v = this.value(node, path);
    return Array.isArray(v) ? v.includes(key) : v === key;
  }

  toggle(node: Node, path: Path, key: string, on: boolean): void {
    const v = this.value(node, path);
    const list = Array.isArray(v) ? (v as string[]).filter(k => k !== key) : [];
    if (on) list.push(key);
    this.set(node, path, list.length ? list : null);
  }

  widget(node: Node): string {
    const chosen = this.layout(node)['widget'];
    if (typeof chosen === 'string') return chosen;
    return node.kind === 'single-choice' ? (this.tree()!.choicesOf(node).length > 6 ? 'dropdown' : 'radio') : node.kind === 'multiple-choice' ? 'checkboxes' : '';
  }

  choiceLabel(node: Node, key: unknown): string {
    const choice = this.tree()?.choicesOf(node).find(c => c['key'] === key);
    return choice ? this.text(choice['label']) : String(key ?? '');
  }

  entries(node: Node, path: Path): Obj[] {
    const list = scopeAt(this.values(), path)[node.key];
    return Array.isArray(list) ? (list as Obj[]) : [];
  }

  canAdd(node: Node, path: Path): boolean {
    const most = node.node['maxRepeats'];
    return typeof most !== 'number' || this.entries(node, path).length < most;
  }

  addEntry(node: Node, path: Path): void {
    this.set(node, path, [...this.entries(node, path), {}]);
  }

  removeEntry(node: Node, path: Path, index: number): void {
    const list = this.entries(node, path).filter((_, i) => i !== index);
    this.paths.clear();
    this.set(node, path, list.length ? list : null);
  }

  unitOf(node: Node): string {
    const unit = node.node['unit'];
    return typeof unit === 'string' ? unit : '';
  }

  /** How a calculated value, an alert value or a shown answer reads. */
  shown(node: Node, path: Path): string {
    const v = this.value(node, path);
    if (v === undefined || v === null || v === '') return 'No answer';
    const unit = this.unitOf(node);
    switch (node.kind) {
      case 'single-choice':
        return this.choiceLabel(node, v);
      case 'multiple-choice':
        return (v as string[]).map(k => this.choiceLabel(node, k)).join(', ');
      case 'date-time':
        return new Date(String(v)).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
      case 'point':
      case 'track':
      case 'polygon': {
        const points = positionsOf(v);
        return node.kind === 'point' && points[0] ? points[0].join(', ') : `${points.length} positions`;
      }
      case 'alert-value': {
        const source = node.node['source'];
        if (source === 'location') return positionsOf(v)[0]?.join(', ') ?? '';
        if (source === 'observed-at' || source === 'raised-at') return new Date(String(v)).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
        return String(v) + (source === 'fire-power' ? ' MW' : '');
      }
    }
    if (typeof v === 'number') return v.toLocaleString() + (unit ? ' ' + unit : '');
    return String(v);
  }

  alertPoint(): [number, number] | null {
    const alert = this.alert();
    return alert ? [alert.longitude, alert.latitude] : null;
  }

  /** The problems about one field in one entry, shown beside it after a first try. */
  fieldProblems(node: Node, path: Path): Problem[] {
    if (!this.tried()) return [];
    const id = pathId(path);
    return [...this.problems(), ...this.serverProblems()].filter(p => p.field === node.key && pathId(p.path) === id);
  }

  /** The files a photo, file or signature field holds. */
  files(node: Node, path: Path): string[] {
    const v = this.value(node, path);
    if (typeof v === 'string') return [v];
    return Array.isArray(v) ? (v as string[]) : [];
  }

  fileLabel(id: string): string {
    const upload = this.uploads()[id];
    return upload ? `${upload.fileName} (${fileSize(upload.sizeBytes)})` : 'File uploaded earlier';
  }

  preview(id: string): string | undefined {
    return this.uploads()[id]?.preview;
  }

  accept(node: Node): string {
    if (node.kind === 'photo') return imageTypes.join(',');
    const endings = node.node['accept'];
    return Array.isArray(endings) ? endings.join(',') : '';
  }

  canAddFile(node: Node, path: Path): boolean {
    const most = typeof node.node['maxCount'] === 'number' ? node.node['maxCount'] as number : 20;
    return this.files(node, path).length < most;
  }

  removeFile(node: Node, path: Path, id: string): void {
    const left = this.files(node, path).filter(f => f !== id);
    this.set(node, path, node.kind === 'signature' ? null : left.length ? left : null);
  }

  async addFiles(node: Node, path: Path, list: FileList | null): Promise<void> {
    for (const file of Array.from(list ?? [])) {
      const id = await this.upload(file, file.name);
      if (id) this.set(node, path, [...this.files(node, path), id]);
    }
  }

  async saveSignature(node: Node, path: Path, image: Blob): Promise<void> {
    const id = await this.upload(image, 'signature.png');
    if (id) this.set(node, path, id);
  }

  /** Uploads a file for the alert and returns its attachment ID. */
  private async upload(file: Blob, name: string): Promise<string | null> {
    const alert = this.alertKey();
    if (!alert) return null;
    const id = crypto.randomUUID();
    this.uploading.set(name);
    this.serverMessage.set('');
    try {
      await firstValueFrom(this.http.put(this.workspaceUrl() + '/alerts/' + alert + '/attachments/' + id, file,
        { params: { name: name.slice(0, 200) || 'file' }, headers: { 'Content-Type': file.type || 'application/octet-stream' } }));
      const preview = imageTypes.includes(file.type) ? URL.createObjectURL(file) : undefined;
      this.uploads.update(all => ({ ...all, [id]: { contentType: file.type, fileName: name, sizeBytes: file.size, preview } }));
      return id;
    } catch (error) {
      const failed = error as HttpErrorResponse;
      if (failed.status === 401) this.auth.sessionEnded();
      const { message } = serverProblems(failed);
      this.serverMessage.set(failed.status === 415 ? `“${name}” is not a file type the server accepts.` : `“${name}” could not upload. ${message}`);
      return null;
    } finally {
      this.uploading.set(null);
    }
  }

  goTo(index: number): void {
    this.page.set(Math.max(0, Math.min(index, this.pages().length - 1)));
    queueMicrotask(() => document.getElementById('form-page-title')?.focus());
  }

  /** Opens the page that holds a field with a problem, and moves to it. */
  show(problem: Problem): void {
    const tree = this.tree();
    let node = problem.field ? tree?.byKey.get(problem.field) : undefined;
    while (node?.parent) node = node.parent;
    const index = node ? this.pages().findIndex(p => p.sections.includes(node)) : -1;
    if (index >= 0) this.page.set(index);
    if (problem.field && node) {
      const id = 'field-' + problem.field + (problem.path.length ? '-' + pathId(problem.path) : '');
      queueMicrotask(() => document.getElementById(id)?.focus());
    }
  }

  async submit(): Promise<void> {
    this.tried.set(true);
    this.serverMessage.set('');
    this.serverList.set([]);
    const session = this.session();
    const alert = this.alertKey();
    if (!session || !alert || this.busy()) return;
    if (!session.answers) {
      queueMicrotask(() => document.getElementById('form-problems')?.focus());
      return;
    }
    const body = { form: this.form(), version: this.version(), step: this.step(), values: session.answers };
    const sent = JSON.stringify(body);
    // A retry of the same answers keeps its ID, so the server can tell it is the same report.
    if (sent !== this.lastSent) {
      this.submissionId = crypto.randomUUID();
      this.deviceTime = new Date().toISOString();
    }
    this.lastSent = sent;
    this.busy.set(true);
    try {
      const saved = await firstValueFrom(this.http.post<Submission>(this.workspaceUrl() + '/alerts/' + alert + '/submissions',
        { id: this.submissionId, ...body, deviceTime: this.deviceTime }));
      if (this.lifetime.destroyed) return;
      toast.success(this.title() + ' submitted.');
      this.submitted.emit(saved);
    } catch (error) {
      const failed = error as HttpErrorResponse;
      if (failed.status === 401) { this.auth.sessionEnded(); return; }
      const { message, problems } = serverProblems(failed);
      // Only a request that may have arrived is sent again with the same ID.
      if (failed.status !== 0 && failed.status < 500) this.lastSent = '';
      this.serverMessage.set(failed.status === 404 ? 'You can no longer see this alert.' : message);
      this.serverList.set(problems);
      queueMicrotask(() => document.getElementById('form-problems')?.focus());
    } finally {
      this.busy.set(false);
    }
  }

  /** The step of a number input: whole numbers, or the decimals the field allows. */
  stepOf(node: Node): string {
    const decimals = node.node['decimals'];
    return typeof decimals === 'number' ? String(1 / 10 ** decimals) : 'any';
  }

  readonly toLocalInput = toLocalInput;
}
