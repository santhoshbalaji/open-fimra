import { Component, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { HlmButton } from '@spartan-ng/helm/button';
import { Incidents, type IncidentFocus } from './incidents';
import { ShellListScroll } from './shell-list-scroll';
import { WorkspaceList } from './shell-workspace-list';

@Component({
  selector: 'fimra-incident-screen',
  imports: [HlmButton, Incidents, RouterLink, ShellListScroll],
  template: `
    <div class="shell-list" fimraListScroll>
      <section class="shell-list-body" aria-label="Incidents">
        @if (focus(); as incident) {
          <a hlmBtn variant="outline" [routerLink]="navigation.href('/monitor/map', { incident: incident.id })">Show incident on map</a>
        }
        <fimra-incidents [endpoint]="endpoint()" [now]="now()" (openAlert)="openAlert($event.id)" (showOnMap)="focus.set($event)" />
      </section>
    </div>
  `,
  host: { class: 'block h-full' }
})
export class IncidentScreen extends WorkspaceList {
  readonly focus = signal<IncidentFocus | null>(null);
}
