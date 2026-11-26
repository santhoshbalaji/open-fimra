import { translate } from '../language';
import { DEFAULT_DIALOG_CONFIG, Dialog, DialogConfig } from '@angular/cdk/dialog';
import { BreakpointObserver } from '@angular/cdk/layout';
import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, ElementRef, Injector, afterNextRender, afterRenderEffect, inject, input, output, signal, viewChild } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { BrnDialogService } from '@spartan-ng/brain/dialog';
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
  // The owner decides when to close a panel, including after changes to URL state.
  viewProviders: [BrnDialogService, Dialog, {
    provide: DEFAULT_DIALOG_CONFIG,
    useValue: { ...new DialogConfig(), closeOnNavigation: false }
  }],
  template: `
    <ng-template #body><ng-content /></ng-template>
    @if (open()) {
      @if (sheetOnly() || phone()) {
        <hlm-sheet #sheet side="bottom" state="open" autoFocus="first-heading" [restoreFocus]="false"
          (closed)="finishClose()">
          <hlm-sheet-content *hlmSheetPortal [style.height]="fullHeight() ? '100dvh' : null"
            [closeLabel]="closeLabel()" class="rounded-t-card border-border">
            <hlm-sheet-header>
              <h2 #sheetHeading hlmSheetTitle tabindex="-1" class="text-base">{{ heading() }}</h2>
            </hlm-sheet-header>
            <div class="px-4 pb-4"><ng-container [ngTemplateOutlet]="body" /></div>
          </hlm-sheet-content>
        </hlm-sheet>
      } @else {
        <section tabindex="-1" [attr.aria-labelledby]="headingId" (keydown.escape)="dismissDock($event)"
          class="grid min-w-0 content-start gap-3 rounded-card border border-border bg-card p-4"
          [class.max-h-full]="scrollDock()" [class.overflow-auto]="scrollDock()" [class.overscroll-contain]="scrollDock()">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h2 #dockHeading [id]="headingId" tabindex="-1" class="rounded-sm text-base font-semibold wrap-anywhere focus-visible:outline-2 focus-visible:outline-ring">{{ heading() }}</h2>
            <button type="button" hlmBtn variant="ghost" [attr.aria-label]="closeLabel()" (click)="close()">{{ msg('Close') }}</button>
          </div>
          <ng-container [ngTemplateOutlet]="body" />
        </section>
      }
    }
  `
})
export class SidePanel {
  protected readonly msg = translate;
  readonly heading = input.required<string>();
  readonly sheetOnly = input(false);
  readonly closeLabel = input('Close');
  readonly fullHeight = input(false);
  readonly scrollDock = input(false);
  readonly closed = output<void>();

  private readonly injector = inject(Injector);
  private readonly breakpoints = inject(BreakpointObserver);
  private readonly opened = signal(false);
  private readonly dockHeading = viewChild<ElementRef<HTMLElement>>('dockHeading');
  private readonly sheetHeading = viewChild<ElementRef<HTMLElement>>('sheetHeading');
  private readonly sheet = viewChild(HlmSheet);
  private trigger: HTMLElement | (() => HTMLElement | null) | null = null;
  private fallback: HTMLElement | null = null;
  protected readonly headingId = `fimra-panel-${++nextPanel}`;
  protected readonly phone = toSignal(
    this.breakpoints.observe('(width < 48rem)').pipe(map(match => match.matches)),
    { requireSync: true }
  );

  readonly open = this.opened.asReadonly();

  constructor() {
    afterRenderEffect(() => {
      // Moving to a linked record can change the title while the panel stays open.
      this.heading();
      (this.dockHeading() ?? this.sheetHeading())?.nativeElement.focus();
    });
  }

  show(trigger: HTMLElement | (() => HTMLElement | null), fallback?: HTMLElement) {
    this.trigger = trigger;
    this.fallback = fallback ?? null;
    this.opened.set(true);
  }

  close() {
    if (!this.open()) return;
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
    this.closed.emit();
    afterNextRender(() => {
      if (!this.open()) restoreOverlayFocus(
        typeof this.trigger === 'function' ? this.trigger() : this.trigger, this.fallback
      );
    }, { injector: this.injector });
  }
}
