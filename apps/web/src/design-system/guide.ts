import { Component, DestroyRef, Injector, afterNextRender, computed, inject, signal } from '@angular/core';
import { bootstrapApplication } from '@angular/platform-browser';
import { toast } from '@spartan-ng/brain/sonner';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { ageColours, ageLabels, chartSeries, density, interfaceColours, mapColours, radii, typeScale } from '../token-values';
import { classes } from '../detections';
import { ThemeService } from '../theme';
import { EmptyState } from '../ui/empty-state';
import { StatusBadge } from '../ui/status-badge';
import { ConfirmDialog } from '../ui/confirm-dialog';
import { FieldErrorSummary } from '../ui/field-error-summary';
import { LiveStatus } from '../ui/live-status';
import { restoreOverlayFocus } from '../ui/overlay-focus';
import { SidePanel } from '../ui/side-panel';
import { DataState, StateRegion } from '../ui/state-region';
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
import { HlmToasterImports } from '@spartan-ng/helm/sonner';
import { HlmSwitchImports } from '@spartan-ng/helm/switch';
import { HlmTableImports } from '@spartan-ng/helm/table';
import { HlmTabsImports } from '@spartan-ng/helm/tabs';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { HlmTooltipImports } from '@spartan-ng/helm/tooltip';
import { Icon } from '../ui/icon';
import { WorkspaceExample } from './workspace-example';

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
  imports: [WorkspaceExample, EmptyState, StatusBadge, FieldErrorSummary, LiveStatus, SidePanel, StateRegion, HlmAlertImports, HlmBadgeImports, HlmButtonImports, HlmCardImports, HlmCheckboxImports, HlmDialogImports, HlmDropdownMenuImports, HlmFieldImports, HlmInputImports, HlmLabelImports, HlmRadioGroupImports, HlmSelectImports, HlmSeparatorImports, HlmSheetImports, HlmToasterImports, HlmSwitchImports, HlmTableImports, HlmTabsImports, HlmTextareaImports, HlmTooltipImports, Icon]
})
export class DesignGuide {
  private readonly confirm = inject(ConfirmDialog);
  private readonly injector = inject(Injector);
  private readonly destroyRef = inject(DestroyRef);
  readonly appearance = inject(ThemeService);
  readonly palette = computed(() => {
    const mode = this.appearance.theme();
    const colors = interfaceColours[mode];
    const card = luminance(colors.surface);
    const entries = { ...colors,
      ...Object.fromEntries(Object.entries(mapColours[mode]).map(([name, value]) => ['Map ' + name, value])),
      ...Object.fromEntries(chartSeries[mode].map((color, i) => ['Chart ' + (i + 1), color])),
      ...Object.fromEntries(ageColours[mode].map((color, i) => [ageLabels[i], color])) };
    return Object.entries(entries).filter(([, color]) => color.startsWith('#')).map(([name, color]) => {
      const value = luminance(color);
      return { name: name.replace(/[A-Z]/g, c => ' ' + c.toLowerCase()).trim(), color,
        ratio: ((Math.max(value, card) + 0.05) / (Math.min(value, card) + 0.05)).toFixed(2) };
    });
  });
  readonly sizes = Object.entries(typeScale);
  readonly radius = Object.entries(radii);
  readonly symbols = Object.entries(classes);
  readonly ages = ageLabels;
  readonly densities = Object.entries(density);
  readonly density = signal<keyof typeof density>('comfortable');

  toggleDensity() {
    this.density.update(value => value === 'comfortable' ? 'compact' : 'comfortable');
    document.documentElement.dataset['density'] = this.density();
  }

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
  readonly saving = signal(false);
  readonly saveMessage = signal('');
  readonly decision = signal('Alert open');
  readonly code = signal('');
  readonly checked = signal(false);
  readonly fieldErrors = computed(() => this.checked() && !this.code().trim()
    ? [{ fieldId: 'code', message: 'Enter a unit code.' }]
    : []);
  readonly examples: readonly DataState[] = [
    { kind: 'loading', heading: 'Loading detections' },
    { kind: 'empty', heading: 'No detections', detail: 'Choose a longer time window.' },
    { kind: 'forbidden', detail: 'Ask your administrator for a workspace role.' },
    { kind: 'stale', observedAt: { dateTime: '14:05:00Z', label: '14:05 UTC' } }
  ];
  readonly retryState = signal<DataState>({ kind: 'error', heading: 'Could not load the alert list' });

  saveNote() {
    this.saving.set(true);
    this.saveMessage.set('Saving the field note…');
    const timer = setTimeout(() => {
      unregister();
      this.saving.set(false);
      this.saveMessage.set('Field note saved.');
    }, 1000);
    const unregister = this.destroyRef.onDestroy(() => clearTimeout(timer));
  }

  validate(event: Event, summary: FieldErrorSummary) {
    event.preventDefault();
    this.checked.set(true);
    afterNextRender(() => summary.focus(), { injector: this.injector });
  }

  async closeAlert(trigger: HTMLElement, status: HTMLElement) {
    const approved = await this.confirm.open({
      heading: 'Close the demo alert?',
      detail: 'Closing the alert ends its workflow.',
      confirmLabel: 'Close alert',
      destructive: true,
      trigger
    });
    if (!approved) return;
    this.decision.set('Alert closed');
    afterNextRender(() => restoreOverlayFocus(status, trigger), { injector: this.injector });
  }

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
