import { Component, computed, inject, input } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDropdownMenu, HlmDropdownMenuItem, HlmDropdownMenuTrigger } from '@spartan-ng/helm/dropdown-menu';
import { AuthService } from './auth';
import { ShellNavigation } from './shell-navigation';
import { Icon } from './ui/icon';
import { WorkspaceService } from './workspace';

@Component({
  selector: 'fimra-shell-account',
  imports: [HlmButton, HlmDropdownMenu, HlmDropdownMenuItem, HlmDropdownMenuTrigger, Icon],
  template: `
    <button hlmBtn variant="ghost" [hlmDropdownMenuTrigger]="account" class="user-trigger account-trigger"
            [attr.aria-label]="'Account for ' + name() + ', ' + initials()" [title]="name()">
      <span class="user-name">{{ name() }}</span>
      <span class="account-initials" aria-hidden="true">{{ initials() }}</span>
      <svg fimraIcon="chevron-down" class="chevron" aria-hidden="true"></svg>
    </button>
    <ng-template #account>
      <hlm-dropdown-menu>
        @for (other of workspace.workspaces(); track other.slug) {
          @if (other.slug !== workspace.current()?.slug) {
            <button hlmDropdownMenuItem (triggered)="switchWorkspace(other.slug)">Open {{ other.name }}</button>
          }
        }
        <button hlmDropdownMenuItem (triggered)="auth.signOut(true)">Switch account</button>
        <button hlmDropdownMenuItem (triggered)="auth.signOut()">Sign out</button>
      </hlm-dropdown-menu>
    </ng-template>
  `
})
export class ShellAccount {
  readonly name = input.required<string>();
  protected readonly initials = computed(() =>
    this.name().trim().split(/\s+/).slice(0, 2).map(part => Array.from(part)[0]).join('').toLocaleUpperCase());
  readonly auth = inject(AuthService);
  readonly workspace = inject(WorkspaceService);
  private readonly navigation = inject(ShellNavigation);

  switchWorkspace(slug: string): void {
    this.navigation.go('/monitor/map', {
      workspace: slug, unitPath: null, cursor: null, alert: null, detection: null, incident: null, queue: null
    });
  }
}
