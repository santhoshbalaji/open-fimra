import { Component, OnInit, effect, inject, signal } from '@angular/core';
import { CdkMenu, CdkMenuItem, CdkMenuTrigger } from '@angular/cdk/menu';
import { BrnButton } from '@spartan-ng/brain/button';
import { LucideChevronDown, LucideCircleUserRound, LucideLogOut, LucideMoon, LucideSun } from '@lucide/angular';
import { AuthService } from './auth';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';
import { DemoMap } from './demo-map';
import { Layers } from './layers';
import { NotificationLink, Notifications, notificationURL } from './notifications';
import { Dashboard } from './dashboard';
import { DashboardFilters } from './dashboard-filters';
import { classes, ForestClass } from './detections';

@Component({
  selector: 'fimra-root',
  imports: [CdkMenu, CdkMenuItem, CdkMenuTrigger, BrnButton, DemoMap, Layers, Dashboard, Notifications,
    LucideChevronDown, LucideCircleUserRound, LucideLogOut, LucideMoon, LucideSun],
  templateUrl: './app.html',
  host: { '(window:popstate)': 'loadLink()' }
})
export class App implements OnInit {
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);
  readonly showLayers = signal(false);

  readonly dashboard = signal(false);
  readonly filters = inject(DashboardFilters);
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

  openNotice(link: NotificationLink): void {
    history.pushState(null, '', notificationURL(link));
    this.target.set(link);
  }
}
