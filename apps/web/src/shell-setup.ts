import { HttpClient } from '@angular/common/http';
import { Component, type TemplateRef, inject, signal, viewChild } from '@angular/core';
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
    <ng-template #primary>
      @if (!workspace.setupPending()) {
        <button hlmBtn [disabled]="busy()" (click)="reopen()">Reopen setup</button>
      }
    </ng-template>
    <ng-template #secondary>
      @if (workspace.setupPending()) {
        <button hlmBtn variant="ghost" type="button" (click)="navigation.monitor()">Leave setup for now</button>
      }
    </ng-template>
    @if (workspace.setupPending()) {
      <fimra-setup-wizard (finished)="finished()" (openLayers)="navigation.go('/settings/layers')" />
    } @else {
      <section class="grid gap-2 px-(--page-gutter) pb-12">
        <p>First-run setup is complete.</p>
        @if (message()) { <p role="alert">{{ message() }}</p> }
      </section>
    }
  `
})
export class SetupScreen {
  readonly pagePrimary = viewChild<TemplateRef<unknown>>('primary');
  readonly pageSecondary = viewChild<TemplateRef<unknown>>('secondary');
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
