import { HlmToaster } from '@spartan-ng/helm/sonner';
import { HlmDropdownMenu, HlmDropdownMenuItem, HlmDropdownMenuTrigger } from '@spartan-ng/helm/dropdown-menu';
import { HlmButton } from '@spartan-ng/helm/button';
import { Component, OnInit, computed, effect, inject, signal } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { AuthService } from './auth';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';
import { DemoMap } from './demo-map';
import { Layers } from './layers';
import { NotificationLink, Notifications, notificationURL } from './notifications';
import { Dashboard } from './dashboard';
import { Bootstrap } from './bootstrap';
import { SetupWizard } from './setup-wizard';
import { WEB_CONFIG } from './config';
import { Icon } from './ui/icon';
import { FilterFields } from './filter-fields';
import { AccessKeys } from './access-keys';
import { FormsScreen } from './forms/forms-screen';
import { SituationReportView } from './situation-report';

@Component({
  selector: 'fimra-root',
  imports: [HlmButton, HlmDropdownMenu, HlmDropdownMenuItem, HlmDropdownMenuTrigger, HlmToaster, FilterFields, DemoMap, Layers, Dashboard, Notifications, Bootstrap, SetupWizard, AccessKeys, FormsScreen, SituationReportView, Icon],
  templateUrl: './app.html',
  host: { '(window:popstate)': 'loadLink()' }
})
export class App implements OnInit {
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);
  readonly showLayers = signal(false);
  readonly showKeys = signal(false);
  readonly showForms = signal(false);
  private readonly http = inject(HttpClient);
  private readonly api = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  /** null follows the setup state: the wizard opens until it is finished. */
  private readonly setupChoice = signal<boolean | null>(null);
  readonly showSetup = computed(() => this.workspace.canManageLayers() &&
    (this.setupChoice() ?? this.workspace.setupPending()));
  readonly showBootstrap = computed(() => this.workspace.bootstrapAvailable() && !this.workspace.current());

  readonly filtersOpen = signal(false);
  readonly dashboard = signal(false);
  /** The printable situation report, loaded only when it is opened. */
  readonly report = signal(false);
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
      this.showKeys.set(false);
      this.showForms.set(false);
      this.setupChoice.set(false);
      this.dashboard.set(target.alert === null);
      this.report.set(false);
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
    this.showKeys.set(false);
    this.showForms.set(false);
    this.showLayers.set(!this.showLayers());
  }

  toggleForms(): void {
    this.setupChoice.set(false);
    this.showKeys.set(false);
    this.showLayers.set(false);
    this.showForms.set(!this.showForms());
  }

  openKeys(): void {
    this.setupChoice.set(false);
    this.showLayers.set(false);
    this.showForms.set(false);
    this.showKeys.set(true);
  }

  openSetup(): void {
    const workspace = this.workspace.current();
    if (!workspace) return;
    this.showLayers.set(false);
    this.showKeys.set(false);
    this.showForms.set(false);
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
    this.report.set(false);
    this.workspace.reload();
  }

  openNotice(link: NotificationLink): void {
    history.pushState(null, '', notificationURL(link));
    this.target.set(link);
  }
}
