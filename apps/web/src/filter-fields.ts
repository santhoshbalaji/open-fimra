import { HlmSelect, HlmSelectContent, HlmSelectItem, HlmSelectPortal, HlmSelectTrigger } from '@spartan-ng/helm/select';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmField, HlmFieldLabel } from '@spartan-ng/helm/field';
import { HlmSwitch } from '@spartan-ng/helm/switch';
import { Component, computed, inject } from '@angular/core';
import { DashboardFilters } from './dashboard-filters';
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
      <input hlmInput id="hours" #hours type="range" min="1" max="720" [value]="filters.hours()" (change)="filters.hours.set(+hours.value)">
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
      <input hlmInput id="unit-path" #path placeholder="All units" pattern="[a-zA-Z0-9_\\-]+(\\.[a-zA-Z0-9_\\-]+)*"
        [value]="filters.unitPath()" (change)="path.reportValidity() && filters.unitPath.set(path.value)">
    </hlm-field>
    <label hlmFieldLabel for="low-bandwidth"><hlm-switch inputId="low-bandwidth" [checked]="filters.lowBandwidth()" (checkedChange)="filters.setLowBandwidth($event)" /> Low bandwidth</label>
  `
})
export class FilterFields {
  readonly filters = inject(DashboardFilters);
  readonly classes = classes;
  readonly classKeys = Object.keys(classes) as ForestClass[];
  readonly forestLabel = computed(() => {
    const key = this.filters.forestClass();
    return key ? classes[key] : 'All classes';
  });

  selectForest(value: string | null | undefined): void {
    this.filters.forestClass.set(this.classKeys.find(key => key === value) ?? '');
  }
}
