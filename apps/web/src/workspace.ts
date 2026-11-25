import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';

interface Workspace { slug: string; name: string }
interface Assignment { workspace: Workspace; role: string; unit: { path: string } }
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
      if (!subject) return;
      let retry: ReturnType<typeof setTimeout>;
      const load = () => this.http.get<Profile>(this.base + '/api/v1/me').subscribe({
        next: profile => {
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
    this.message.set(noAccess);
  }
}
