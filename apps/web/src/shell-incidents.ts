import { Component } from '@angular/core';
import { Incidents } from './incidents';
import { WorkspaceList } from './shell-workspace-list';

@Component({
  selector: 'fimra-incident-screen',
  imports: [Incidents],
  template: `<fimra-incidents [endpoint]="endpoint()" [now]="now()" />`,
  host: { class: 'block h-full min-w-0' }
})
export class IncidentScreen extends WorkspaceList {}
