import { NgTemplateOutlet } from '@angular/common';
import { Component, OnInit, computed, effect, inject, signal, untracked } from '@angular/core';
import { Router, RouterLink, RouterOutlet } from '@angular/router';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmToaster } from '@spartan-ng/helm/sonner';
import { AuthService } from './auth';
import { Bootstrap } from './bootstrap';
import { DashboardFilters } from './dashboard-filters';
import { FilterFields } from './filter-fields';
import { Notifications, type NotificationLink } from './notifications';
import { ShellNavigation, homePath } from './shell-navigation';
import { ShellAccount } from './shell-account';
import { ShellDrawer } from './shell-drawer';
import { ShellPalette } from './shell-palette';
import { ThemeService } from './theme';
import { LiveStatus } from './ui/live-status';
import { Icon } from './ui/icon';
import { WorkspaceService } from './workspace';
import { classes, type ForestClass } from './detections';

@Component({
  selector: 'fimra-root',
  imports: [NgTemplateOutlet, RouterLink, RouterOutlet, HlmButton, HlmToaster, Bootstrap, FilterFields, Notifications, ShellAccount, ShellDrawer, ShellPalette, Icon, LiveStatus],
  providers: [ShellNavigation],
  templateUrl: './app.html',
  host: { '(window:keydown)': 'shortcut($event)' }
})
export class App implements OnInit {
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);
  readonly navigation = inject(ShellNavigation);
  private readonly router = inject(Router);
  private readonly filters = inject(DashboardFilters);
  private readonly authOpened = signal(false);
  private routed = false;
  readonly filtersOpen = signal(false);
  readonly drawerRequest = signal<{ trigger: HTMLElement } | null>(null);
  readonly paletteOpen = signal(false);
  readonly groups = ['Monitor', 'Reports', 'Dashboard', 'Settings'] as const;
  readonly showBootstrap = computed(() => this.workspace.bootstrapAvailable() && !this.workspace.current());
  readonly showFilters = computed(() => {
    const view = this.navigation.destination();
    return view && 'filters' in view;
  });
  readonly scope = computed(() => {
    const unit = this.navigation.query().get('unitPath');
    const jurisdictions = this.workspace.jurisdictions();
    if (unit) return jurisdictions.find(([path]) => path === unit)?.[1] || unit;
    const [first] = jurisdictions;
    if (jurisdictions.length === 1 && first && !first[0].includes('.') && first[1] === this.workspace.current()?.name) {
      return 'All units';
    }
    return jurisdictions.map(([, label]) => label).join(', ');
  });

  constructor() {
    effect(() => {
      if (!this.authOpened() || !this.auth.user() || !this.workspace.loaded()) return;
      if (!this.routed) {
        this.routed = true;
        this.router.initialNavigation();
      }
    });
    effect(() => {
      if (this.navigation.path() === '/' && this.router.lastSuccessfulNavigation() &&
          this.workspace.current() && this.workspace.loaded()) {
        untracked(() => this.navigation.go(homePath(this.workspace), {}, true));
      }
    });
    effect(() => {
      const query = this.navigation.query();
      this.filters.hours.set(Number(query.get('hours') ?? 168));
      const forest = query.get('class');
      this.filters.forestClass.set(forest && Object.hasOwn(classes, forest) ? forest as ForestClass : '');
      this.filters.unitPath.set(query.get('unitPath') ?? '');
    });
    effect(() => {
      this.navigation.path();
      this.filtersOpen.set(false);
    });
  }

  ngOnInit(): void {
    void this.auth.open().finally(() => this.authOpened.set(true));
  }

  shortcut(event: KeyboardEvent): void {
    if (event.defaultPrevented || event.repeat || event.altKey || !(event.ctrlKey || event.metaKey) ||
        event.key.toLowerCase() !== 'k' || !this.workspace.current()) return;
    event.preventDefault();
    this.paletteOpen.set(true);
  }

  reload(): void {
    location.reload();
  }

  openNotice(link: NotificationLink): void {
    this.navigation.go(link.alert ? '/monitor/alerts' : '/dashboard', {
      workspace: link.workspace,
      unitPath: null,
      cursor: null,
      queue: null,
      detection: null,
      alert: link.alert,
      view: link.alert ? null : 'feed-health'
    });
  }

  screenOpened(screen: unknown): void {
    if (screen && typeof screen === 'object' && 'closed' in screen) {
      (screen.closed as { subscribe: (callback: () => void) => void }).subscribe(() => this.navigation.monitor());
    }
  }
}
