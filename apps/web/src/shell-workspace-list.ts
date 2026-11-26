import { computed, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { map, timer } from 'rxjs';
import { WEB_CONFIG } from './config';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';

export class WorkspaceList {
  private readonly workspace = inject(WorkspaceService);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  readonly navigation = inject(ShellNavigation);
  readonly endpoint = computed(() => this.api + '/api/v1/workspaces/' + encodeURIComponent(this.workspace.current()?.slug ?? ''));
  readonly now = toSignal(timer(60_000, 60_000).pipe(map(() => Date.now())), { initialValue: Date.now() });

  openAlert(id: string): void {
    this.navigation.go('/monitor/alerts', { alert: id });
  }
}
