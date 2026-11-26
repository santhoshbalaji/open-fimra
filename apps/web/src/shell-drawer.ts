import { NgTemplateOutlet } from '@angular/common';
import { Component, effect, inject, input, viewChild, type TemplateRef } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { ThemeService } from './theme';
import { SidePanel } from './ui/side-panel';

@Component({
  selector: 'fimra-shell-drawer',
  imports: [NgTemplateOutlet, HlmButton, SidePanel],
  template: `
    <fimra-side-panel heading="Destinations">
      <div class="shell-drawer">
        <p>{{ scope() }}</p>
        <nav aria-label="Destinations">
          <ng-container *ngTemplateOutlet="links(); context: { close: dismiss }" />
        </nav>
        <button hlmBtn variant="ghost" (click)="appearance.toggle()" [attr.aria-pressed]="appearance.theme() === 'dark'">Dark theme</button>
      </div>
    </fimra-side-panel>
  `
})
export class ShellDrawer {
  readonly request = input.required<{ trigger: HTMLElement }>();
  readonly scope = input.required<string>();
  readonly links = input.required<TemplateRef<{ close: () => void }>>();
  readonly appearance = inject(ThemeService);
  private readonly panel = viewChild(SidePanel);
  readonly dismiss = () => this.panel()?.close();

  constructor() {
    effect(() => this.panel()?.show(this.request().trigger));
  }
}
