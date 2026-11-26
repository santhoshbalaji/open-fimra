import { HlmSelect, HlmSelectContent, HlmSelectItem, HlmSelectPortal, HlmSelectTrigger } from '@spartan-ng/helm/select';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmField, HlmFieldLabel } from '@spartan-ng/helm/field';
import { HlmSwitch } from '@spartan-ng/helm/switch';
import { Component, computed, inject, signal } from '@angular/core';
import { DashboardFilters } from './dashboard-filters';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';
import { classes, ForestClass } from './detections';

/**
 * The map and dashboard filters. The shell loads them after the first screen,
 * so the form controls stay out of the first download.
 */
@Component({
  selector: 'fimra-filter-fields',
  imports: [HlmField, HlmFieldLabel, HlmInput, HlmSelect, HlmSelectContent, HlmSelectItem, HlmSelectPortal, HlmSelectTrigger, HlmSwitch],
  host: { class: 'filter-fields', id: 'map-filters' },
  template: `
    <hlm-field><label hlmFieldLabel for="hours">Past {{ filters.hours() }} hours</label>
      <input hlmInput id="hours" #hours type="range" min="1" max="720" [value]="filters.hours()" (change)="setFilter('hours', hours.value)">
    </hlm-field>
    <hlm-field><label hlmFieldLabel for="forest">Forest class</label>
      <hlm-select [value]="filters.forestClass() || 'all'" (valueChange)="selectForest($event)">
        <hlm-select-trigger buttonId="forest">{{ forestLabel() }}</hlm-select-trigger>
        <hlm-select-content *hlmSelectPortal><hlm-select-item value="all">All classes</hlm-select-item>
          @for (key of classKeys; track key) { <hlm-select-item [value]="key">{{ classes[key] }}</hlm-select-item> }
        </hlm-select-content>
      </hlm-select>
    </hlm-field>
    <hlm-field><label hlmFieldLabel for="unit-path">Unit path</label>
      <input hlmInput id="unit-path" #path placeholder="Assigned units" [value]="filters.unitPath()"
        (change)="selectScope(path)">
      @if (scopeError()) { <span role="status">{{ scopeError() }}</span> }
    </hlm-field>
    <label hlmFieldLabel for="low-bandwidth"><hlm-switch inputId="low-bandwidth" [checked]="filters.lowBandwidth()" (checkedChange)="filters.setLowBandwidth($event)" /> Low bandwidth</label>
  `
})
export class FilterFields {
  readonly filters = inject(DashboardFilters);
  readonly classes = classes;
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly workspace = inject(WorkspaceService, { optional: true });
  readonly scopeError = signal('');
  readonly classKeys = Object.keys(classes) as ForestClass[];
  readonly forestLabel = computed(() => {
    const key = this.filters.forestClass();
    return key ? classes[key] : 'All classes';
  });

  selectForest(value: string | null | undefined): void {
    this.setFilter('class', this.classKeys.find(key => key === value) ?? '');
  }

  selectScope(input: HTMLInputElement): void {
    const value = input.value.trim();
    if (value && this.workspace && !this.workspace.allowsScope(value)) {
      this.scopeError.set('Choose a unit within your assigned area.');
      input.value = this.filters.unitPath();
      return;
    }
    this.scopeError.set('');
    this.setFilter('unitPath', value);
  }

  setFilter(key: 'hours' | 'class' | 'unitPath', value: string): void {
    if (this.navigation) {
      this.navigation.update({ [key]: value || null, cursor: null, alert: null, detection: null, incident: null, report: null });
    } else if (key === 'hours') {
      this.filters.hours.set(+value);
    } else if (key === 'class') {
      this.filters.forestClass.set(value as ForestClass | '');
    } else {
      this.filters.unitPath.set(value);
    }
  }
}
