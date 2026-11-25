import { HlmToasterImports } from '@spartan-ng/helm/sonner';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDropdownMenuImports } from '@spartan-ng/helm/dropdown-menu';
import { HlmCheckboxImports } from '@spartan-ng/helm/checkbox';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { Component, OnInit, computed, effect, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { LucideChevronDown, LucideLogOut, LucideMoon, LucideSettings, LucideSun } from '@lucide/angular';
import { AuthService } from './auth';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';
import { DemoMap } from './demo-map';
import { Layers } from './layers';
import { NotificationLink, Notifications, notificationURL } from './notifications';
import { Dashboard } from './dashboard';
import { DashboardFilters } from './dashboard-filters';
import { classes, ForestClass } from './detections';
import { Bootstrap } from './bootstrap';
import { SetupWizard } from './setup-wizard';
import { WEB_CONFIG } from './config';

@Component({
  selector: 'fimra-root',
  imports: [HlmButtonImports, HlmCheckboxImports, HlmDropdownMenuImports, HlmFieldImports, HlmInputImports, HlmSelectImports, HlmToasterImports, DemoMap, Layers, Dashboard, Notifications, Bootstrap, SetupWizard,
    LucideChevronDown, LucideLogOut, LucideMoon, LucideSettings, LucideSun],
  templateUrl: './app.html',
  host: { '(window:popstate)': 'loadLink()' }
})
export class App implements OnInit {
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);
  readonly showLayers = signal(false);
  private readonly http = inject(HttpClient);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  /** null follows the setup state: the wizard opens until it is finished. */
  private readonly setupChoice = signal<boolean | null>(null);
  readonly showSetup = computed(() => this.workspace.canManageLayers() &&
    (this.setupChoice() ?? this.workspace.setupPending()));
  readonly showBootstrap = computed(() => this.workspace.bootstrapAvailable() && !this.workspace.current());

  readonly filtersOpen = signal(false);
  readonly dashboard = signal(false);
  readonly filters = inject(DashboardFilters);
  readonly forestLabel = computed(() => {
    const key = this.filters.forestClass();
    return key ? classes[key] : 'All classes';
  });
  readonly classes = classes;
  readonly classKeys = Object.keys(classes) as ForestClass[];
  readonly linkedAlert = signal<NotificationLink | null>(null);
  readonly linkMessage = signal('');
  readonly focusFeedHealth = signal(false);
  private readonly target = signal<NotificationLink | null>(null);

  constructor() {
    effect(() => {
      const target = this.target();
      if (!this.workspace.loaded()) return;
      this.linkedAlert.set(null);
      this.linkMessage.set('');
      this.focusFeedHealth.set(false);
      if (!target) return;
      if (!this.workspace.select(target.workspace)) {
        this.linkMessage.set('You cannot open this workspace.');
        return;
      }
      this.showLayers.set(false);
      this.setupChoice.set(false);
      this.dashboard.set(target.alert === null);
      if (target.alert) this.linkedAlert.set(target);
      else this.focusFeedHealth.set(true);
    });
  }

  ngOnInit(): void {
    void this.auth.open().then(() => this.loadLink());
  }

  loadLink(): void {
    const params = new URLSearchParams(location.search);
    const workspace = params.get('workspace');
    const alert = params.get('alert');
    const feedHealth = params.get('view') === 'feed-health';
    this.target.set(workspace && (alert ? /^[1-9][0-9]*$/.test(alert) : feedHealth) ? { workspace, alert } : null);
  }

  toggleLayers(): void {
    this.setupChoice.set(false);
    this.showLayers.set(!this.showLayers());
  }

  selectForest(value: string | null | undefined): void {
    this.filters.forestClass.set(this.classKeys.find(key => key === value) ?? '');
  }

  openSetup(): void {
    const workspace = this.workspace.current();
    if (!workspace) return;
    this.showLayers.set(false);
    if (this.workspace.setupPending()) {
      this.setupChoice.set(true);
      return;
    }
    // Reopening records the choice and shows the wizard until it is finished again.
    this.http.post(this.api + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug) + '/setup/reopen', {})
      .subscribe({ next: () => { this.setupChoice.set(true); this.workspace.reload(); } });
  }

  leaveSetup(layers: boolean): void {
    this.setupChoice.set(false);
    this.showLayers.set(layers);
  }

  setupFinished(): void {
    this.setupChoice.set(null);
    this.showLayers.set(false);
    this.dashboard.set(false);
    this.workspace.reload();
  }

  openNotice(link: NotificationLink): void {
    history.pushState(null, '', notificationURL(link));
    this.target.set(link);
  }
}
