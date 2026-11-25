import { Component, inject } from '@angular/core';
import { HlmLabel } from '@spartan-ng/helm/label';
import { HlmSwitch } from '@spartan-ng/helm/switch';
import { DashboardFilters } from './dashboard-filters';

/** The shared low-bandwidth choice for the map and dashboard, as a Helm switch. */
@Component({
  selector: 'fimra-low-bandwidth-switch',
  imports: [HlmLabel, HlmSwitch],
  template: `
    <label hlmLabel class="font-normal">
      <hlm-switch [checked]="filters.lowBandwidth()" (checkedChange)="filters.setLowBandwidth($event)"
        aria-describedby="low-bandwidth-help" />
      Low bandwidth
    </label>
    <span id="low-bandwidth-help" class="sr-only">Skips the chart and reference layers.</span>
  `
})
export class LowBandwidthSwitch {
  readonly filters = inject(DashboardFilters);
}
