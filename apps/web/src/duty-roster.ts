import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, computed, effect, inject, signal } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import type { Observable } from 'rxjs';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { WorkspaceService } from './workspace';
import { translate } from './language';
import { workspaceFormat } from './workspace-format';

const kinds = { control_room: 'Control room', squad: 'Fire squad', watcher: 'Fire watcher' };

interface DutyShift {
  id: string;
  name: string;
  kind: keyof typeof kinds;
  startsAt: string;
  endsAt: string;
  cancelledAt: string | null;
  members: string[] | null;
  units: string[] | null;
}
interface Roster {
  shifts: DutyShift[];
  members: { id: string; name: string; username: string }[];
  units: { id: string; name: string; path: string; hierarchy: string }[];
}

@Component({
  selector: 'fimra-duty-roster',
  imports: [HlmButton, HlmCardImports],
  templateUrl: './duty-roster.html'
})
export class DutyRoster {
  private readonly config = inject(WEB_CONFIG);
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly lifetime = inject(DestroyRef);
  private readonly endpoint = computed(() => {
    const workspace = this.workspace.current();
    return workspace ? this.config.apiBaseUrl.replace(/\/$/, '') + '/api/v1/workspaces/' + workspace.slug + '/settings/duty' : '';
  });
  private readonly revision = signal(0);
  readonly roster = signal<Roster | null>(null);
  readonly readError = signal('');
  readonly writeError = signal('');
  readonly notice = signal('');
  readonly saving = signal(false);
  protected readonly msg = translate;
  readonly kinds = kinds;
  readonly zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  readonly dateTime = (value: string) => workspaceFormat.dateTime(new Date(value));

  constructor() {
    effect(() => {
      this.endpoint();
      this.roster.set(null);
      this.writeError.set('');
      this.notice.set('');
    });
    effect(onCleanup => {
      const url = this.endpoint();
      this.revision();
      this.readError.set('');
      if (!url) return;
      const request = this.http.get<Roster>(url).subscribe({
        next: roster => this.roster.set(roster),
        error: (error: HttpErrorResponse) => this.readError.set(this.message(error, 'Could not load the duty roster.'))
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  retry(): void {
    this.revision.update(value => value + 1);
  }

  status(shift: DutyShift): 'Cancelled' | 'Ended' | 'On duty' | 'Scheduled' {
    const now = Date.now();
    if (shift.cancelledAt) return 'Cancelled';
    if (Date.parse(shift.endsAt) <= now) return 'Ended';
    return Date.parse(shift.startsAt) <= now ? 'On duty' : 'Scheduled';
  }

  schedule(event: SubmitEvent): void {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const fields = new FormData(form);
    const starts = new Date(String(fields.get('startsAt')));
    const ends = new Date(String(fields.get('endsAt')));
    if (!(starts < ends)) {
      this.writeError.set('The shift must end after it starts.');
      return;
    }
    const url = this.endpoint();
    this.save(url, this.http.post(url, {
      name: fields.get('name'), kind: fields.get('kind'), startsAt: starts.toISOString(), endsAt: ends.toISOString(),
      memberIds: fields.getAll('memberIds'), unitIds: fields.getAll('unitIds')
    }), 'Shift scheduled.', form);
  }

  cancel(shift: DutyShift): void {
    const url = this.endpoint();
    this.save(url, this.http.delete(url + '/' + shift.id), 'Shift cancelled.');
  }

  private save(url: string, request: Observable<unknown>, notice: string, form?: HTMLFormElement): void {
    if (!url || this.saving()) return;
    this.saving.set(true);
    this.writeError.set('');
    this.notice.set('');
    request.pipe(takeUntilDestroyed(this.lifetime)).subscribe({
      next: () => {
        this.saving.set(false);
        if (this.endpoint() !== url) return;
        form?.reset();
        this.notice.set(notice);
        this.retry();
      },
      error: (error: HttpErrorResponse) => {
        this.saving.set(false);
        if (this.endpoint() === url) this.writeError.set(this.message(error, 'Could not save the shift.'));
      }
    });
  }

  private message(error: HttpErrorResponse, fallback: string): string {
    if (error.status === 401) this.auth.sessionEnded();
    return typeof error.error?.error === 'string' && error.error.error || fallback;
  }
}
