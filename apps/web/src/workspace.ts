import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';

interface Workspace { slug: string; name: string }
interface Profile { roleAssignments: { workspace: Workspace }[] }
export const noAccess = 'You have no access to this workspace yet';
export const unavailable = 'Fire detections could not load. Trying again in a minute.';

@Injectable({ providedIn: 'root' })
export class WorkspaceService {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly subject = computed(() => this.auth.user()?.profile.sub);
  readonly current = signal<Workspace | null>(null);
  readonly message = signal('');

  constructor() {
    effect(onCleanup => {
      const subject = this.subject();
      this.current.set(null);
      this.message.set('');
      if (!subject) return;
      let retry: ReturnType<typeof setTimeout>;
      const load = () => this.http.get<Profile>(this.base + '/api/v1/me').subscribe({
        next: profile => {
          const workspace = profile.roleAssignments[0]?.workspace;
          this.current.set(workspace ?? null);
          this.message.set(workspace ? '' : noAccess);
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

  deny(): void {
    this.current.set(null);
    this.message.set(noAccess);
  }
}
