import { translate } from './language';
import { NgTemplateOutlet } from '@angular/common';
import { Component, effect, inject, input, output, viewChild, type TemplateRef } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { ShellNavigation, type DestinationPath } from './shell-navigation';
import { ThemeService } from './theme';
import { SidePanel } from './ui/side-panel';

@Component({
  selector: 'fimra-shell-drawer',
  imports: [NgTemplateOutlet, HlmButton, SidePanel],
  template: `
    <fimra-side-panel [heading]="msg('Destinations')" [closeLabel]="msg('Close')" [sheetOnly]="true" (closed)="drawerClosed()">
      <div class="shell-drawer">
        <p>{{ scope() }}</p>
        <nav [attr.aria-label]=" msg('Destinations') ">
          <ng-container *ngTemplateOutlet="links(); context: { close: dismiss, rail: false }" />
        </nav>
        <button hlmBtn variant="ghost" (click)="appearance.toggle()" [attr.aria-pressed]="appearance.theme() === 'dark'">{{ msg('Dark theme') }}</button>
      </div>
    </fimra-side-panel>
  `
})
export class ShellDrawer {
  protected readonly msg = translate;
  readonly request = input.required<{ trigger: HTMLElement }>();
  readonly scope = input.required<string>();
  readonly links = input.required<TemplateRef<{ close: (path: DestinationPath, event: MouseEvent) => void; rail: boolean }>>();
  readonly destinationClosed = output<void>();
  readonly appearance = inject(ThemeService);
  private readonly panel = viewChild(SidePanel);
  private readonly navigation = inject(ShellNavigation);
  private openingDestination?: DestinationPath;
  readonly dismiss = (path: DestinationPath, event: MouseEvent) => {
    if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    this.openingDestination = path !== this.navigation.path() ? path : undefined;
    this.panel()?.close();
  };

  constructor() {
    effect(() => {
      const { trigger } = this.request();
      this.openingDestination = undefined;
      this.panel()?.show(() => this.openingDestination ? null : trigger);
    });
  }

  protected drawerClosed(): void {
    if (this.openingDestination === this.navigation.path()) this.destinationClosed.emit();
  }
}
