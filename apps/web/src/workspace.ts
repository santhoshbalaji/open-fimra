import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';

interface Workspace { slug: string; name: string; kind?: 'national' }
interface Assignment { workspace: Workspace; role: string; hierarchy?: string; unit: { path: string; name?: string } }
/** A role the user holds in the current workspace. */
export interface Role { role: string; hierarchy: string; path: string }
interface SetupInfo { bootstrapAvailable: boolean; unfinished: string[] }
interface Profile { roleAssignments: Assignment[]; setup?: SetupInfo }
export const noAccess = 'You have no access to this workspace yet';
export const unavailable = 'Fire detections could not load. Trying again in a minute.';

@Injectable({ providedIn: 'root' })
export class WorkspaceService {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly subject = computed(() => this.auth.user()?.profile.sub);
  readonly current = signal<Workspace | null>(null);
  readonly loaded = signal(false);
  readonly message = signal('');
  readonly failed = signal(false);
  readonly profileReady = computed(() => this.loaded() || this.failed());
  private readonly grants = signal<Assignment[]>([]);
  /** True on a fresh install that has not been claimed yet. */
  readonly bootstrapAvailable = signal(false);
  /** Workspaces whose first-run wizard this user should finish. */
  readonly unfinishedSetup = signal<string[]>([]);
  private readonly reloads = signal(0);
  readonly canManageLayers = computed(() => {
    const workspace = this.current();
    return this.grants().some(grant => grant.workspace.slug === workspace?.slug &&
      grant.role === 'state_admin' && !!grant.unit?.path && !grant.unit.path.includes('.'));
  });

  /** The user's roles in the current workspace. */
  readonly roles = computed<Role[]>(() => this.grants()
    .filter(grant => grant.workspace.slug === this.current()?.slug)
    .map(grant => ({ role: grant.role, hierarchy: grant.hierarchy ?? '', path: grant.unit?.path ?? '' })));

  readonly workspaces = computed(() => [...new Map(this.grants().map(grant => [grant.workspace.slug, grant.workspace])).values()]);
  readonly jurisdictions = computed(() => [...new Map(this.grants()
    .filter(grant => grant.workspace.slug === this.current()?.slug && grant.unit?.path)
    .map(grant => [grant.unit.path, grant.unit.name || grant.unit.path])).entries()]);

  allowsScope(path: string): boolean {
    return /^[a-zA-Z0-9_-]+(?:\.[a-zA-Z0-9_-]+)*$/.test(path) &&
      this.jurisdictions().some(([grant]) => path === grant || path.startsWith(grant + '.'));
  }

  /** Root administrators design forms; supervising officers design forms for their division. */
  readonly canDesignForms = computed(() => this.canManageLayers() ||
    this.roles().some(role => role.role === 'division_officer' && role.path.includes('.')));

  /** The wizard opens by itself until a root administrator finishes it. */
  readonly setupPending = computed(() => this.canManageLayers() &&
    this.unfinishedSetup().includes(this.current()?.slug ?? ''));

  constructor() {
    effect(onCleanup => {
      const subject = this.subject();
      this.reloads();
      const previous = untracked(this.current)?.slug;
      this.current.set(null);
      this.loaded.set(false);
      this.grants.set([]);
      this.message.set('');
      this.failed.set(false);
      this.bootstrapAvailable.set(false);
      this.unfinishedSetup.set([]);
      if (!subject) return;
      let retry: ReturnType<typeof setTimeout>;
      const load = () => this.http.get<Profile>(this.base + '/api/v1/me').subscribe({
        next: profile => {
          this.failed.set(false);
          this.grants.set(profile.roleAssignments);
          this.bootstrapAvailable.set(profile.setup?.bootstrapAvailable ?? false);
          this.unfinishedSetup.set(profile.setup?.unfinished ?? []);
          const workspace = profile.roleAssignments.find(grant => grant.workspace.slug === previous)?.workspace ??
            profile.roleAssignments[0]?.workspace;
          this.current.set(workspace ?? null);
          this.loaded.set(true);
          this.message.set(workspace || this.bootstrapAvailable() ? '' : noAccess);
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          else if (error.status === 403) this.deny();
          else {
            this.failed.set(true);
            this.message.set(unavailable);
            retry = setTimeout(() => { request = load(); }, 60_000);
          }
        }
      });
      let request = load();
      onCleanup(() => { clearTimeout(retry); request.unsubscribe(); });
    });
  }

  /** Reads the profile again, for example after setup creates a workspace. */
  reload(): void {
    this.reloads.update(count => count + 1);
  }

  select(slug: string): boolean {
    const workspace = this.grants().find(grant => grant.workspace.slug === slug)?.workspace;
    if (!workspace) return false;
    this.current.set(workspace);
    this.message.set('');
    return true;
  }

  deny(): void {
    this.current.set(null);
    this.loaded.set(true);
    this.message.set(noAccess);
  }
}
