import { Component, input } from '@angular/core';
import { HlmEmptyImports } from '@spartan-ng/helm/empty';

@Component({
  selector: 'fimra-empty-state',
  imports: [HlmEmptyImports],
  template: `
    <hlm-empty>
      <hlm-empty-header>
        <h3 hlmEmptyTitle>{{ heading() }}</h3>
        <p hlmEmptyDescription>{{ detail() }}</p>
      </hlm-empty-header>
      <ng-content />
    </hlm-empty>
  `
})
export class EmptyState {
  readonly heading = input.required<string>();
  readonly detail = input.required<string>();
}
