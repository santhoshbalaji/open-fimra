import { DOCUMENT, NgTemplateOutlet } from '@angular/common';
import { Component, Injector, OnInit, afterNextRender, computed, effect, inject, signal, untracked } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { NavigationEnd, Router, RouterLink, RouterOutlet } from '@angular/router';
import { HlmToaster } from '@spartan-ng/helm/sonner';
import { AuthService } from './auth';
import { Bootstrap } from './bootstrap';
import { DashboardFilters, defaultDashboardHours } from './dashboard-filters';
import { ExportTray } from './export-dialog-tray';
import { MonitorFilters } from './filter-fields';
import { Notifications, type NotificationLink } from './notifications';
import { ShellNavigation, homePath } from './shell-navigation';
import { ShellAccount } from './shell-account';
import { ShellDrawer } from './shell-drawer';
import { ShellPalette } from './shell-palette';
import { ThemeService } from './theme';
import { LiveStatus } from './ui/live-status';
import { Icon } from './ui/icon';
import { PageHeader, type PageHeaderSlots } from './ui/page-header';
import { WorkspaceService } from './workspace';
import { classes, type ForestClass } from './detections';

@Component({
  selector: 'fimra-root',
  imports: [NgTemplateOutlet, RouterLink, RouterOutlet, HlmToaster, Bootstrap, MonitorFilters, Notifications, ShellAccount, ShellDrawer, ShellPalette, Icon, LiveStatus, PageHeader, ExportTray],
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
  private readonly document = inject(DOCUMENT);
  private readonly injector = inject(Injector);
  private readonly filters = inject(DashboardFilters);
  private readonly authOpened = signal(false);
  private routed = false;
  readonly drawerRequest = signal<{ trigger: HTMLElement } | null>(null);
  readonly paletteOpen = signal(false);
  readonly routeAnnouncement = signal('');
  readonly headerSlots = signal<PageHeaderSlots | null>(null);
  readonly groups = ['Monitor', 'Reports', 'Dashboard', 'Settings'] as const;
  readonly showBootstrap = computed(() => this.workspace.bootstrapAvailable() && !this.workspace.current());
  readonly showFilters = computed(() => {
    const view = this.navigation.destination();
    return view?.group === 'Monitor';
  });
  readonly scope = computed(() => {
    const unit = this.navigation.query().get('unitPath');
    const jurisdictions = this.workspace.jurisdictions();
    const selected = jurisdictions.find(([path]) => path === unit);
    if (selected) return selected[1];
    const [first] = jurisdictions;
    if (jurisdictions.length === 1 && first && !first[0].includes('.') && first[1] === this.workspace.current()?.name) {
      return 'All units';
    }
    return jurisdictions.map(([, label]) => label).join(', ');
  });

  constructor() {
    this.router.events.pipe(takeUntilDestroyed()).subscribe(event => {
      if (!(event instanceof NavigationEnd)) return;
      const previous = this.router.lastSuccessfulNavigation()?.previousNavigation?.finalUrl;
      const current = this.router.parseUrl(event.urlAfterRedirects);
      // Keep focus on a filter or paging control during an in-place update.
      if (previous && previous.toString().split(/[?#]/, 1)[0] === current.toString().split(/[?#]/, 1)[0] &&
          previous.queryParamMap.get('workspace') === current.queryParamMap.get('workspace')) return;
      this.focusPage();
    });
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
      this.filters.hours.set(Number(query.get('hours') ?? defaultDashboardHours));
      const forest = query.get('class');
      this.filters.forestClass.set(forest && Object.hasOwn(classes, forest) ? forest as ForestClass : '');
      this.filters.unitPath.set(query.get('unitPath') ?? '');
    });
  }

  ngOnInit(): void {
    void this.auth.open().finally(() => this.authOpened.set(true));
  }

  focusPage(): void {
    afterNextRender(() => {
      const main = this.document.getElementById('task');
      if (!main) return;
      const heading = main.querySelector<HTMLElement>('h1');
      if (!heading) return;
      const active = this.document.activeElement;
      const detail = active && active !== main && active !== heading &&
        (active.closest('[role="dialog"]') || (main.contains(active) && active.matches('[tabindex="-1"]')));
      if (!detail) heading.focus({ preventScroll: true });
      this.routeAnnouncement.set(`${heading.textContent?.trim()} page opened.`);
    }, { injector: this.injector });
  }

  skipToContent(event: MouseEvent): void {
    event.preventDefault();
    this.document.getElementById('task')?.focus();
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
    this.headerSlots.set(screen as PageHeaderSlots);
    if (screen && typeof screen === 'object' && 'closed' in screen) {
      (screen.closed as { subscribe: (callback: () => void) => void }).subscribe(() => this.navigation.monitor());
    }
  }
}
