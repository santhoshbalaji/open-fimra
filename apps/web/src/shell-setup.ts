import { HttpClient } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { WEB_CONFIG } from './config';
import { SetupWizard } from './setup-wizard';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';

@Component({
  selector: 'fimra-shell-setup',
  imports: [HlmButton, SetupWizard],
  host: { class: 'block h-full overflow-auto' },
  template: `
    @if (workspace.setupPending()) {
      <fimra-setup-wizard (finished)="finished()" (leave)="navigation.monitor()" (openLayers)="navigation.go('/settings/layers')" />
    } @else {
      <section class="welcome">
        <h2>First-run setup is complete</h2>
        <button hlmBtn [disabled]="busy()" (click)="reopen()">Reopen setup</button>
        @if (message()) { <p role="alert">{{ message() }}</p> }
      </section>
    }
  `
})
export class SetupScreen {
  readonly workspace = inject(WorkspaceService);
  readonly navigation = inject(ShellNavigation);
  private readonly http = inject(HttpClient);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  readonly busy = signal(false);
  readonly message = signal('');

  reopen(): void {
    const slug = this.workspace.current()?.slug;
    if (!slug || this.busy()) return;
    this.busy.set(true);
    this.message.set('');
    this.http.post(this.base + '/api/v1/workspaces/' + encodeURIComponent(slug) + '/setup/reopen', {}).subscribe({
      next: () => { this.busy.set(false); this.workspace.reload(); },
      error: () => { this.busy.set(false); this.message.set('Setup could not reopen. Try again.'); }
    });
  }

  finished(): void {
    this.navigation.monitor();
    this.workspace.reload();
  }
}
