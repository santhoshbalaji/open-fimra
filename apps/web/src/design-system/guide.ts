import { Component, computed, inject, signal } from '@angular/core';
import { bootstrapApplication } from '@angular/platform-browser';
import { toast } from '@spartan-ng/brain/sonner';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import tokens from '../../../../docs/design-system/tokens.json';
import { classes } from '../detections';
import { ThemeService } from '../theme';
import { EmptyState } from '../ui/empty-state';
import { StatusBadge } from '../ui/status-badge';
import { HlmAlertImports } from '@spartan-ng/helm/alert';
import { HlmBadgeImports } from '@spartan-ng/helm/badge';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmCardImports } from '@spartan-ng/helm/card';
import { HlmCheckboxImports } from '@spartan-ng/helm/checkbox';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmDropdownMenuImports } from '@spartan-ng/helm/dropdown-menu';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { HlmLabelImports } from '@spartan-ng/helm/label';
import { HlmRadioGroupImports } from '@spartan-ng/helm/radio-group';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmSeparatorImports } from '@spartan-ng/helm/separator';
import { HlmSheetImports } from '@spartan-ng/helm/sheet';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmToasterImports } from '@spartan-ng/helm/sonner';
import { HlmSwitchImports } from '@spartan-ng/helm/switch';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { HlmTabsImports } from '@spartan-ng/helm/tabs';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HlmTooltipImports } from '@spartan-ng/helm/tooltip';
import { Icon } from '../ui/icon';

// WCAG relative luminance for an opaque sRGB swatch.
function luminance(hex: string): number {
  const channel = (start: number) => {
    const value = parseInt(hex.slice(start, start + 2), 16) / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  };
  return channel(1) * 0.2126 + channel(3) * 0.7152 + channel(5) * 0.0722;
}

@Component({
  selector: 'fimra-root',
  templateUrl: './guide.html',
  styleUrl: './guide.css',
  imports: [EmptyState, StatusBadge, HlmAlertImports, HlmBadgeImports, HlmButtonImports, HlmCardImports, HlmCheckboxImports, HlmDialogImports, HlmDropdownMenuImports, HlmFieldImports, HlmInputImports, HlmLabelImports, HlmRadioGroupImports, HlmSelectImports, HlmSeparatorImports, HlmSheetImports, HlmSkeletonImports, HlmToasterImports, HlmSwitchImports, HlmTableImports, HlmTabsImports, HlmTextareaImports, HlmTooltipImports, Icon]
})
export class DesignGuide {
  readonly appearance = inject(ThemeService);
  readonly palette = computed(() => {
    const mode = this.appearance.theme();
    const colors = tokens.color;
    const card = luminance(colors[mode].card);
    const entries = { ...colors[mode], ...colors.status,
      ...Object.fromEntries(colors.chart[mode].map((color, i) => ['Chart ' + (i + 1), color])),
      ...Object.fromEntries(colors.fireAge[mode].map((color, i) => [colors.fireAge.steps[i], color])) };
    return Object.entries(entries).filter(([, color]) => color.startsWith('#')).map(([name, color]) => {
      const value = luminance(color);
      return { name: name.replace(/[A-Z]/g, c => ' ' + c.toLowerCase()).trim(), color,
        ratio: ((Math.max(value, card) + 0.05) / (Math.min(value, card) + 0.05)).toFixed(2) };
    });
  });
  readonly sizes = Object.entries(tokens.font.sizePx);
  readonly radius = Object.entries(tokens.radiusPx);
  readonly symbols = Object.entries(classes);
  readonly ages = tokens.color.fireAge.steps;
  readonly variants = ['primary', 'secondary', 'outline', 'ghost', 'destructive', 'link'] as const;
  readonly alertVariants = ['default', 'destructive'] as const;
  readonly badges = ['default', 'secondary', 'outline', 'ghost', 'destructive', 'link'] as const;
  readonly buttonSizes = ['xs', 'sm', 'default', 'lg'] as const;
  readonly iconSizes = ['icon-xs', 'icon-sm', 'icon', 'icon-lg'] as const;
  readonly sides = ['right', 'left', 'top', 'bottom'] as const;
  readonly side = signal<typeof this.sides[number]>('right');
  readonly cardSizes = ['default', 'sm'] as const;
  readonly tabStyles = ['default', 'line'] as const;
  readonly toastTypes = ['success', 'info', 'warning', 'error', 'loading'] as const;
  readonly toastType = signal<typeof this.toastTypes[number]>('success');
  readonly fieldStates = ['Default', 'Invalid', 'Disabled'] as const;
  readonly fieldState = signal<typeof this.fieldStates[number]>('Default');
  readonly disabled = computed(() => this.fieldState() === 'Disabled');
  readonly invalid = computed(() => this.fieldState() === 'Invalid');
  readonly marked = signal(false);
  readonly notify = toast;

  showToast() {
    const messages = { success: 'Field note saved', info: 'New field note', warning: 'Field note is overdue',
      error: 'Could not save the field note', loading: 'Saving the field note…' };
    const kind = this.toastType();
    toast[kind](messages[kind], { duration: 4000 });
  }
}

export function openGuide() {
  document.title = 'Design system · Open FIMRA';
  return bootstrapApplication(DesignGuide, { providers: [provideSpartanHlm()] });
}
