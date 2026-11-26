import { BreakpointObserver } from '@angular/cdk/layout';
import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, ElementRef, Injector, afterNextRender, afterRenderEffect, inject, input, signal, viewChild } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmSheet, HlmSheetImports } from '@spartan-ng/helm/sheet';
import { map } from 'rxjs';
import { restoreOverlayFocus } from './overlay-focus';

let nextPanel = 0;

@Component({
  selector: 'fimra-side-panel',
  exportAs: 'fimraSidePanel',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButton, HlmSheetImports, NgTemplateOutlet],
  template: `
    <ng-template #body><ng-content /></ng-template>
    @if (open()) {
      @if (phone()) {
        <hlm-sheet #sheet side="bottom" state="open" autoFocus="first-heading" [restoreFocus]="false"
          (closed)="finishClose()">
          <hlm-sheet-content *hlmSheetPortal class="rounded-t-card border-border">
            <hlm-sheet-header>
              <h2 hlmSheetTitle tabindex="-1" class="text-base">{{ heading() }}</h2>
            </hlm-sheet-header>
            <div class="px-4 pb-4"><ng-container [ngTemplateOutlet]="body" /></div>
          </hlm-sheet-content>
        </hlm-sheet>
      } @else {
        <section tabindex="-1" [attr.aria-labelledby]="headingId" (keydown.escape)="dismissDock($event)"
          class="grid min-w-0 gap-3 rounded-card border border-border bg-card p-4">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h2 #dockHeading [id]="headingId" tabindex="-1" class="rounded-sm text-base font-semibold focus-visible:outline-2 focus-visible:outline-ring">{{ heading() }}</h2>
            <button type="button" hlmBtn variant="ghost" (click)="close()">Close</button>
          </div>
          <ng-container [ngTemplateOutlet]="body" />
        </section>
      }
    }
  `
})
export class SidePanel {
  readonly heading = input.required<string>();

  private readonly injector = inject(Injector);
  private readonly breakpoints = inject(BreakpointObserver);
  private readonly opened = signal(false);
  private readonly dockHeading = viewChild<ElementRef<HTMLElement>>('dockHeading');
  private readonly sheet = viewChild(HlmSheet);
  private trigger: HTMLElement | null = null;
  private fallback: HTMLElement | null = null;
  protected readonly headingId = `fimra-panel-${++nextPanel}`;
  protected readonly phone = toSignal(
    this.breakpoints.observe('(width < 48rem)').pipe(map(match => match.matches)),
    { requireSync: true }
  );

  readonly open = this.opened.asReadonly();

  constructor() {
    afterRenderEffect(() => this.dockHeading()?.nativeElement.focus());
  }

  show(trigger: HTMLElement, fallback?: HTMLElement) {
    this.trigger = trigger;
    this.fallback = fallback ?? null;
    this.opened.set(true);
  }

  close() {
    const sheet = this.sheet();
    if (sheet) sheet.close();
    else this.finishClose();
  }

  protected dismissDock(event: Event) {
    if (event.defaultPrevented) return;
    event.stopPropagation();
    this.close();
  }

  protected finishClose() {
    if (!this.open()) return;
    this.opened.set(false);
    afterNextRender(() => {
      if (!this.open()) restoreOverlayFocus(this.trigger, this.fallback);
    }, { injector: this.injector });
  }
}
