import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmLabel } from '@spartan-ng/helm/label';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { WorkspaceService } from './workspace';
import { Icon } from './ui/icon';
import { FieldErrorSummary } from './ui/field-error-summary';
import { LiveStatus } from './ui/live-status';

/** Turns a workspace name into a web address part, such as "demo-state". */
export function slugFrom(name: string): string {
  return name.normalize('NFKD').replace(/[^\x20-\x7e]/g, '').toLowerCase()
    .replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-+$/, '');
}

/**
 * The first step on a fresh install: the person with the setup code from the
 * server log claims the install and becomes its first administrator.
 */
@Component({
  selector: 'fimra-bootstrap',
  imports: [HlmAlertImports, HlmBadge, HlmButton, HlmCardImports, HlmFieldImports, HlmInput, HlmLabel, HlmRadioGroupImports, FieldErrorSummary, Icon, LiveStatus],
  templateUrl: './bootstrap.html',
  host: { class: 'block h-full overflow-auto max-sm:scroll-pt-16 max-sm:scroll-pb-16' }
})
export class Bootstrap {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly url = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '') + '/api/v1/setup/bootstrap';
  readonly slug = signal('');
  readonly slugEdited = signal(false);
  readonly showCode = signal(false);
  readonly busy = signal(false);
  readonly error = signal('');
  readonly codeError = signal('');
  readonly saved = signal(false);

  nameChanged(name: string): void {
    if (!this.slugEdited()) this.slug.set(slugFrom(name));
  }

  submit(form: HTMLFormElement): void {
    if (this.busy() || this.saved() || !form.reportValidity()) return;
    const data = new FormData(form);
    const body = {
      setupCode: String(data.get('setupCode') ?? '').trim(),
      mode: 'state',
      workspaceName: String(data.get('workspaceName') ?? '').trim(),
      workspaceSlug: this.slug(),
      stateCode: String(data.get('stateCode') ?? '').trim()
    };
    this.busy.set(true);
    this.error.set('');
    this.codeError.set('');
    this.http.post(this.url, body).subscribe({
      next: () => { this.saved.set(true); this.busy.set(false); this.workspace.reload(); },
      error: (error: HttpErrorResponse) => {
        this.busy.set(false);
        if (error.status === 401) this.auth.sessionEnded();
        const message = typeof error.error?.error === 'string' ? error.error.error : 'Setup could not start. Try again.';
        this.error.set(message);
        if (error.status === 403) this.codeError.set(message);
      }
    });
  }
}
