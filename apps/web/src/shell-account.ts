import { translate } from './language';
import { Component, computed, inject, input } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDropdownMenu, HlmDropdownMenuCheckbox, HlmDropdownMenuCheckboxIndicator, HlmDropdownMenuItem, HlmDropdownMenuTrigger } from '@spartan-ng/helm/dropdown-menu';
import { language, languageName } from './language';
import { LanguagePreference } from './language-preference';
import { AuthService } from './auth';
import { ShellNavigation } from './shell-navigation';
import { Icon } from './ui/icon';
import { WallDisplay } from './wall-display';
import { WorkspaceService } from './workspace';

@Component({
  selector: 'fimra-shell-account',
  imports: [HlmButton, HlmDropdownMenu, HlmDropdownMenuCheckbox, HlmDropdownMenuCheckboxIndicator, HlmDropdownMenuItem, HlmDropdownMenuTrigger, Icon],
  template: `
    <button hlmBtn variant="ghost" [hlmDropdownMenuTrigger]="account" class="user-trigger account-trigger"
            [attr.aria-label]="msg('Account for ') + name() + ', ' + initials()" [title]="name()">
      <span class="user-name">{{ name() }}</span>
      <span class="account-initials" aria-hidden="true">{{ initials() }}</span>
      <svg fimraIcon="chevron-down" class="chevron" aria-hidden="true"></svg>
    </button>
    <ng-template #account>
      <hlm-dropdown-menu>
        @for (other of workspace.workspaces(); track other.slug) {
          @if (other.slug !== workspace.current()?.slug) {
            <button hlmDropdownMenuItem (triggered)="switchWorkspace(other.slug)">{{ msg('Open {0}', [other.name]) }}</button>
          }
        }
        <button hlmDropdownMenuItem (triggered)="preference.choose('')">{{ msg('Use workspace or browser language') }}</button>
        @for (code of preference.supported; track code) {
          <button hlmDropdownMenuItem role="menuitemradio" [attr.aria-checked]="code === language()"
                  [attr.lang]="code" (triggered)="preference.choose(code)">{{ languageName(code) }}</button>
        }
        <button hlmDropdownMenuCheckbox [checked]="wallDisplay.enabled()" [keepOpen]="false" (triggered)="wallDisplay.toggle()">
          {{ msg('Wall display') }}<hlm-dropdown-menu-checkbox-indicator />
        </button>
        <button hlmDropdownMenuItem (triggered)="auth.signOut(true)">{{ msg('Switch account') }}</button>
        <button hlmDropdownMenuItem (triggered)="auth.signOut()">{{ msg('Sign out') }}</button>
      </hlm-dropdown-menu>
    </ng-template>
  `
})
export class ShellAccount {
  protected readonly msg = translate;
  protected readonly wallDisplay = inject(WallDisplay);
  protected readonly preference = inject(LanguagePreference);
  protected readonly language = language;
  protected readonly languageName = languageName;
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
