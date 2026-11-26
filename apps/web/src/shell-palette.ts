import { translate } from './language';
import { Component, computed, inject, input, output, signal, viewChild } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDialog, HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmInput } from '@spartan-ng/helm/input';
import { ShellNavigation, type DestinationPath } from './shell-navigation';
import { ShellSidebar } from './shell-sidebar';
import { ThemeService } from './theme';

interface Command {
  name: string;
  group: string;
  run: () => void;
  shortcut?: string;
}

@Component({
  selector: 'fimra-shell-palette',
  imports: [HlmButton, HlmDialogImports, HlmInput],
  template: `
    <hlm-dialog [state]="open() ? 'open' : 'closed'" [restoreFocus]="false" (closed)="dialogClosed()" aria-modal="true">
      <hlm-dialog-content *hlmDialogPortal class="shell-palette">
        <h2 hlmDialogTitle>{{ msg('Search destinations') }}</h2>
        <p hlmDialogDescription>{{ msg('Use the arrow keys to move, Enter to open and Escape to close.') }}</p>
        <input hlmInput cdkFocusInitial [attr.aria-label]=" msg('Search destinations') " [placeholder]=" msg('Search') "
               [value]="search()" (input)="search.set($any($event.target).value); index.set(0)"
               (keydown)="key($event)">
        <div class="palette-results">
          @for (result of results(); track result.name; let i = $index) {
            <button hlmBtn variant="ghost" [class.palette-active]="i === active()" type="button"
                    (click)="choose(i)" (focus)="index.set(i)" (keydown)="key($event)">
              {{ result.name }} <span>{{ result.group }}</span>
              @if (result.shortcut) { <kbd>{{ result.shortcut }}</kbd> }
            </button>
          } @empty { <p role="status">{{ msg('No matching commands.') }}</p> }
        </div>
      </hlm-dialog-content>
    </hlm-dialog>
  `
})
export class ShellPalette {
  protected readonly msg = translate;
  readonly open = input.required<boolean>();
  readonly closed = output<void>();
  readonly reselected = output<void>();
  readonly search = signal('');
  readonly index = signal(0);
  private readonly navigation = inject(ShellNavigation);
  private readonly appearance = inject(ThemeService);
  private readonly sidebar = inject(ShellSidebar);
  private readonly dialog = viewChild.required(HlmDialog);
  private pending?: () => void;
  private readonly actions = [
    { name: 'Toggle theme', group: 'Action', run: () => this.appearance.toggle() },
    { name: 'Reset filters', group: 'Action', run: () => this.navigation.update({
      hours: null, class: null, unitPath: null, cursor: null, alert: null, detection: null, incident: null, report: null, decision: null, form: null,
      period: null, from: null, to: null, hierarchy: null, reportHours: null, show: null
    }) }
  ];
  readonly results = computed(() => {
    const destinations = this.navigation.authorized().map(item => ({ name: item.name, group: item.group, run: () => this.go(item.path) }));
    const commands: Command[] = [...destinations, ...this.actions];
    if (!this.sidebar.drawer()) {
      commands.push({ name: 'Toggle sidebar', group: 'Action', shortcut: translate('Alt+Shift+S'), run: () => this.sidebar.toggle() });
    }
    const search = this.search().trim().toLowerCase();
    return commands.map(item => ({ ...item, name: translate(item.name), group: translate(item.group) }))
      .filter(item => (item.name + ' ' + item.group).toLowerCase().includes(search));
  });
  readonly active = computed(() => Math.min(this.index(), this.results().length - 1));

  private go(path: DestinationPath): void {
    if (path === this.navigation.path()) this.reselected.emit();
    else this.navigation.go(path);
  }

  choose(index: number): void {
    const command = this.results()[index];
    if (command) {
      this.pending = command.run;
      this.dialog().close();
    }
  }

  key(event: KeyboardEvent): void {
    const count = this.results().length;
    if (!count) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const index = (this.active() + (event.key === 'ArrowDown' ? 1 : count - 1)) % count;
      this.index.set(index);
      const item = document.querySelectorAll<HTMLButtonElement>('.palette-results button')[index];
      item?.focus();
    } else if (event.key === 'Enter') {
      event.preventDefault();
      this.choose(this.active());
    }
  }

  dialogClosed(): void {
    this.search.set('');
    this.index.set(0);
    this.closed.emit();
    const run = this.pending;
    this.pending = undefined;
    run?.();
  }
}
