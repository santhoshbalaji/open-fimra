import { ChangeDetectionStrategy, Component, Injectable, inject } from '@angular/core';
import { BrnDialogRef, injectBrnDialogContext } from '@spartan-ng/brain/dialog';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmDialogDescription, HlmDialogFooter, HlmDialogHeader, HlmDialogService, HlmDialogTitle } from '@spartan-ng/helm/dialog';
import { firstValueFrom } from 'rxjs';

export interface ConfirmOptions {
  heading: string;
  detail: string;
  confirmLabel: string;
  cancelLabel?: string;
  destructive?: boolean;
  trigger?: HTMLElement;
}

@Component({
  selector: 'fimra-confirm-content',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButton, HlmDialogDescription, HlmDialogFooter, HlmDialogHeader, HlmDialogTitle],
  host: { class: 'grid gap-6' },
  template: `
    <hlm-dialog-header>
      <h2 hlmDialogTitle class="text-base">{{ options.heading }}</h2>
      <p hlmDialogDescription>{{ options.detail }}</p>
    </hlm-dialog-header>
    <hlm-dialog-footer class="flex-col">
      <button type="button" hlmBtn variant="outline" (click)="dialog.close(false)">{{ options.cancelLabel ?? 'Cancel' }}</button>
      <button type="button" hlmBtn [variant]="options.destructive ? 'destructive' : 'primary'"
        (click)="dialog.close(true)">{{ options.confirmLabel }}</button>
    </hlm-dialog-footer>
  `
})
class ConfirmContent {
  protected readonly options = injectBrnDialogContext<ConfirmOptions>();
  protected readonly dialog = inject<BrnDialogRef<boolean>>(BrnDialogRef);
}

let nextConfirmation = 0;

@Injectable({ providedIn: 'root' })
export class ConfirmDialog {
  private readonly dialogs = inject(HlmDialogService);

  async open(options: ConfirmOptions): Promise<boolean> {
    const dialog = this.dialogs.open<boolean, ConfirmOptions>(ConfirmContent, {
      // A confirmation can open over a sheet with a generated dialog ID.
      id: `fimra-confirm-${++nextConfirmation}`,
      context: options,
      role: 'alertdialog',
      autoFocus: 'first-tabbable',
      showCloseButton: false,
      closeOnOutsidePointerEvents: false,
      restoreFocus: options.trigger ?? true
    });
    return (await firstValueFrom(dialog.closed$)) === true;
  }
}
