import { translate } from './language';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmSelect, HlmSelectContent, HlmSelectItem, HlmSelectPortal, HlmSelectTrigger } from '@spartan-ng/helm/select';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmField, HlmFieldLabel } from '@spartan-ng/helm/field';
import { HlmSwitch } from '@spartan-ng/helm/switch';
import { Component, Injector, afterNextRender, computed, inject, input, signal, viewChild, type ElementRef } from '@angular/core';
import { DashboardFilters, ScopeFilters, filterPeriods, setScopeFilters } from './dashboard-filters';
import { ShellNavigation } from './shell-navigation';
import { classes, ForestClass } from './detections';
import { FilterBar, FilterControls } from './ui/filter-bar';
import { Icon } from './ui/icon';

let nextPicker = 0;
const unitPageSize = 20;

@Component({
  selector: 'fimra-filter-fields',
  imports: [HlmButton, HlmField, HlmFieldLabel, HlmInput, HlmSelect, HlmSelectContent, HlmSelectItem, HlmSelectPortal, HlmSelectTrigger, HlmSwitch, Icon],
  host: { class: 'contents' },
  template: `
    <hlm-field class="scope-filter-field">
      <label hlmFieldLabel for="filter-period">{{ msg('Period') }}</label>
      <div class="scope-period-control">
        <select hlmInput id="filter-period" #period (change)="changePeriod(period.value)">
          @for (option of periods; track option.hours) {
            <option [value]="option.hours" [selected]="selectedPeriod() === option.hours">{{ msg(option.label) }}</option>
          }
          <option value="custom" [selected]="selectedPeriod() === 'custom'">{{ msg('Custom window') }}</option>
        </select>
        <svg fimraIcon="chevron-down" aria-hidden="true"></svg>
      </div>
      @if (selectedPeriod() === 'custom') {
        <form class="grid gap-2" (submit)="$event.preventDefault(); applyHours(hours.valueAsNumber, period)">
          <label hlmFieldLabel for="filter-hours">{{ msg('Hours back from now') }}</label>
          <input hlmInput id="filter-hours" #hours #customHours aria-describedby="filter-hours-hint"
            type="number" min="1" max="720" step="1" required [value]="filters.hours()">
          <p id="filter-hours-hint" class="text-label text-text-secondary">{{ msg('1 to 720 hours') }}</p>
          <button type="submit" hlmBtn variant="outline">{{ msg('Apply period') }}</button>
        </form>
      }
    </hlm-field>
    <hlm-field class="scope-filter-field">
      <label hlmFieldLabel for="forest">{{ msg('Forest class') }}</label>
      <hlm-select [value]="filters.forestClass() || 'all'" (valueChange)="selectForest($event)">
        <hlm-select-trigger buttonId="forest">{{ forestLabel() }}</hlm-select-trigger>
        <hlm-select-content *hlmSelectPortal><hlm-select-item value="all">{{ msg('All classes') }}</hlm-select-item>
          @for (key of classKeys; track key) { <hlm-select-item [value]="key">{{ msg(classes[key]) }}</hlm-select-item> }
        </hlm-select-content>
      </hlm-select>
    </hlm-field>
    <hlm-field class="scope-filter-field unit-filter-field" (focusout)="leaveUnitField($event)"
      (keydown.escape)="dismissUnits($event, unitTrigger)">
      <span [id]="pickerId + '-label'" class="scope-filter-label">{{ msg('Unit') }}</span>
      <button hlmBtn variant="outline" type="button" #unitTrigger class="scope-unit-trigger"
        [id]="pickerId + '-trigger'" [attr.aria-labelledby]="pickerId + '-label ' + pickerId + '-trigger'"
        [attr.aria-expanded]="unitOpen()" [attr.aria-controls]="unitOpen() ? pickerId : null"
        (click)="toggleUnits()">
        <span class="min-w-0 wrap-anywhere">{{ scope.unitLabel() }}</span>
        <svg fimraIcon="chevron-down" aria-hidden="true"></svg>
      </button>
      @if (unitOpen()) {
        <div [id]="pickerId" class="unit-filter-options rounded-control border border-border bg-popover p-3 text-popover-foreground shadow-lg">
          <label hlmFieldLabel [for]="pickerId + '-search'">{{ msg('Search units') }}</label>
          <input hlmInput type="search" #unitSearch [id]="pickerId + '-search'" [value]="search()"
            (input)="search.set(unitSearch.value)">
          @if (scope.loading()) {
            <p role="status">{{ msg('Loading units…') }}</p>
          } @else if (scope.failed()) {
            <p role="status">{{ msg('Could not load units.') }}</p>
            <button type="button" hlmBtn variant="outline" (click)="retryUnits()">{{ msg('Retry') }}</button>
          } @else {
            <p role="status" class="text-label text-text-secondary">{{ unitResultsLabel() }}</p>
          }
          <div class="unit-filter-results">
            <button type="button" hlmBtn variant="ghost" [attr.aria-current]="!filters.unitPath() ? 'true' : null"
              (click)="chooseUnit('', unitTrigger)">{{ msg('All assigned units') }}</button>
            @for (unit of visibleUnits(); track unit.path) {
              <button type="button" hlmBtn variant="ghost" [attr.aria-current]="unit.path === filters.unitPath() ? 'true' : null"
                (click)="chooseUnit(unit.path, unitTrigger)">{{ unit.label }}</button>
            }
          </div>
        </div>
      }
    </hlm-field>
    @if (!forReports()) {
      <label hlmFieldLabel class="scope-filter-toggle" for="low-bandwidth">
        <hlm-switch inputId="low-bandwidth" [checked]="filters.lowBandwidth()" (checkedChange)="filters.setLowBandwidth($event)" /> {{ msg('Low bandwidth') }}
      </label>
    }
  `
})
export class FilterFields {
  protected readonly msg = translate;
  readonly forReports = input(false);
  protected readonly filters = inject(DashboardFilters);
  protected readonly scope = inject(ScopeFilters);
  protected readonly classes = classes;
  protected readonly periods = filterPeriods;
  protected readonly pickerId = `filter-units-${++nextPicker}`;
  protected readonly unitOpen = signal(false);
  protected readonly search = signal('');
  private readonly editHours = signal<number | null>(null);
  private readonly unitSearch = viewChild<ElementRef<HTMLInputElement>>('unitSearch');
  private readonly customHours = viewChild<ElementRef<HTMLInputElement>>('customHours');
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly injector = inject(Injector);

  protected readonly classKeys = Object.keys(classes) as ForestClass[];
  protected readonly forestLabel = computed(() => {
    const key = this.filters.forestClass();
    return key ? classes[key] : translate('All classes');
  });
  protected readonly selectedPeriod = computed(() => {
    const hours = this.filters.hours();
    if (this.editHours() === hours) return 'custom';
    return this.periods.find(option => option.hours === hours)?.hours ?? 'custom';
  });
  private readonly matchingUnits = computed(() => {
    const search = this.search().trim().toLocaleLowerCase();
    return this.scope.units().filter(unit => unit.label.toLocaleLowerCase().includes(search));
  });
  protected readonly visibleUnits = computed(() => this.matchingUnits().slice(0, unitPageSize));
  protected readonly unitResultsLabel = computed(() => {
    const count = this.matchingUnits().length;
    if (!count) return translate('No matching units.');
    if (count > unitPageSize) return translate('Showing {0} of {1} units. Search to narrow the list.', [unitPageSize, count]);
    return count === 1 ? translate('1 unit found.') : translate('{0} units found.', [count]);
  });

  protected changePeriod(value: string): void {
    if (value === 'custom') {
      this.editHours.set(this.filters.hours());
      afterNextRender(() => this.customHours()?.nativeElement.focus(), { injector: this.injector });
    } else if (this.periods.some(option => String(option.hours) === value)) {
      this.editHours.set(null);
      setScopeFilters(this.filters, this.navigation, { hours: value });
    }
  }

  protected applyHours(hours: number, period: HTMLSelectElement): void {
    if (!Number.isInteger(hours) || hours < 1 || hours > 720) return;
    this.editHours.set(null);
    setScopeFilters(this.filters, this.navigation, { hours: String(hours) });
    period.focus();
  }

  protected selectForest(value: string | null | undefined): void {
    setScopeFilters(this.filters, this.navigation, { class: this.classKeys.find(key => key === value) ?? null });
  }

  protected toggleUnits(): void {
    if (this.unitOpen()) {
      this.unitOpen.set(false);
      return;
    }
    this.search.set('');
    this.unitOpen.set(true);
    afterNextRender(() => this.unitSearch()?.nativeElement.focus(), { injector: this.injector });
  }

  protected leaveUnitField(event: FocusEvent): void {
    const field = event.currentTarget as HTMLElement;
    if (event.relatedTarget && !field.contains(event.relatedTarget as Node)) this.unitOpen.set(false);
  }

  protected dismissUnits(event: Event, trigger: HTMLButtonElement): void {
    if (!this.unitOpen()) return;
    event.preventDefault();
    event.stopPropagation();
    this.unitOpen.set(false);
    trigger.focus();
  }

  protected chooseUnit(path: string, trigger: HTMLButtonElement): void {
    setScopeFilters(this.filters, this.navigation, { unitPath: path || null });
    this.unitOpen.set(false);
    trigger.focus();
  }

  protected retryUnits(): void {
    this.unitSearch()?.nativeElement.focus();
    this.scope.retry();
  }
}

@Component({
  selector: 'fimra-monitor-filters',
  imports: [FilterBar, FilterControls, FilterFields],
  template: `
    <fimra-filter-bar [filters]="scope.active()" (remove)="clear($event)" (clearAll)="clear()">
      <ng-template fimraFilterControls><fimra-filter-fields /></ng-template>
    </fimra-filter-bar>
  `
})
export class MonitorFilters {
  protected readonly scope = inject(ScopeFilters);
  private readonly filters = inject(DashboardFilters);
  private readonly navigation = inject(ShellNavigation);

  protected clear(key?: string): void {
    setScopeFilters(this.filters, this.navigation, key ? { [key]: null } : { hours: null, class: null, unitPath: null });
  }
}
