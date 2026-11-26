import { translate } from './language';
import { DOCUMENT, NgTemplateOutlet } from '@angular/common';
import { Component, Injector, OnInit, afterNextRender, computed, effect, inject, signal, untracked } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { NavigationEnd, Router, RouterLink, RouterOutlet } from '@angular/router';
import { HlmToaster } from '@spartan-ng/helm/sonner';
import { LanguagePreference } from './language-preference';
import { AuthService } from './auth';
import { Bootstrap } from './bootstrap';
import { DashboardFilters, defaultDashboardHours } from './dashboard-filter-state';
import { ExportTray } from './export-dialog-tray';
import { MonitorFilters } from './filter-fields';
import { Notifications, type NotificationLink } from './notifications';
import { ShellNavigation, homePath } from './shell-navigation';
import { ShellAccount } from './shell-account';
import { ShellDrawer } from './shell-drawer';
import { ShellPalette } from './shell-palette';
import { ShellSidebar } from './shell-sidebar';
import { ThemeService } from './theme';
import { LiveStatus } from './ui/live-status';
import { ShellIcon } from './ui/shell-icon';
import { SidebarTooltip } from './ui/sidebar-tooltip';
import { restoreOverlayFocus } from './ui/overlay-focus';
import { PageHeader, type PageHeaderSlots } from './ui/page-header';
import { WorkspaceService } from './workspace';
import { classes, type ForestClass } from './detection-classes';
import { WallDisplay } from './wall-display';

@Component({
  selector: 'fimra-root',
  imports: [NgTemplateOutlet, RouterLink, RouterOutlet, HlmToaster, Bootstrap, MonitorFilters, Notifications, ShellAccount, ShellDrawer, ShellPalette, ShellIcon, LiveStatus, PageHeader, ExportTray, SidebarTooltip],
  providers: [ShellNavigation, ShellSidebar],
  templateUrl: './app.html',
  host: {
    '(window:keydown)': 'shortcut($event)', '(window:pointerdown)': 'pointerInput()',
    '[attr.data-wall-display]': "wallDisplay.enabled() ? '' : null"
  }
})
export class App implements OnInit {
  protected readonly msg = translate;
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);
  protected readonly wallDisplay = inject(WallDisplay);
  readonly navigation = inject(ShellNavigation);
  private readonly router = inject(Router);
  private readonly document = inject(DOCUMENT);
  private readonly injector = inject(Injector);
  private readonly filters = inject(DashboardFilters);
  private readonly authOpened = signal(false);
  private routed = false;
  protected keyboardInput = false;
  readonly drawerRequest = signal<{ trigger: HTMLElement } | null>(null);
  readonly paletteOpen = signal(false);
  private paletteOrigin: HTMLElement | null = null;
  readonly routeAnnouncement = signal('');
  readonly headerSlots = signal<PageHeaderSlots | null>(null);
  readonly sidebar = inject(ShellSidebar);
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
      return translate('All units');
    }
    return jurisdictions.map(([, label]) => label).join(', ');
  });

  constructor() {
    inject(LanguagePreference);
    effect(() => {
      if (!this.sidebar.drawer()) this.drawerRequest.set(null);
    });
    this.router.events.pipe(takeUntilDestroyed()).subscribe(event => {
      if (!(event instanceof NavigationEnd)) return;
      const previous = this.router.lastSuccessfulNavigation()?.previousNavigation?.finalUrl;
      const current = this.router.parseUrl(event.urlAfterRedirects);
      // Keep focus on a filter or paging control during an in-place update.
      if (previous && previous.toString().split(/[?#]/, 1)[0] === current.toString().split(/[?#]/, 1)[0] &&
          previous.queryParamMap.get('workspace') === current.queryParamMap.get('workspace')) return;
      this.focusPage(!!previous && this.keyboardInput);
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

  focusPage(keyboard = this.keyboardInput): void {
    afterNextRender(() => {
      const main = this.document.getElementById('task');
      if (!main) return;
      const heading = main.querySelector<HTMLElement>('h1');
      if (!heading) return;
      const active = this.document.activeElement;
      const detail = active && active !== main && active !== heading &&
        (active.closest('[role="dialog"]') || (main.contains(active) && active.matches('[tabindex="-1"]')));
      if (!detail) {
        heading.toggleAttribute('data-keyboard-focus', keyboard);
        heading.focus({ preventScroll: true });
      }
      this.routeAnnouncement.set(translate('{0} page opened.', [heading.textContent?.trim()]));
    }, { injector: this.injector });
  }

  skipToContent(event: MouseEvent): void {
    event.preventDefault();
    this.document.getElementById('task')?.focus();
  }

  protected pointerInput(): void {
    // Returning false from a host listener cancels the mouse event CDK dragging needs.
    this.keyboardInput = false;
  }

  shortcut(event: KeyboardEvent): void {
    if (!['Alt', 'Control', 'Meta', 'Shift'].includes(event.key)) this.keyboardInput = true;
    if (event.defaultPrevented || event.repeat || !this.workspace.current()) return;
    if (event.code === 'KeyS' && event.altKey && event.shiftKey && !event.ctrlKey && !event.metaKey && !this.sidebar.drawer()) {
      event.preventDefault();
      this.sidebar.toggle();
    } else if (event.key.toLowerCase() === 'k' && !event.altKey && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      this.openPalette();
    }
  }

  protected openPalette(trigger?: HTMLElement): void {
    if (this.paletteOpen()) return;
    const active = this.document.activeElement;
    this.paletteOrigin = trigger ?? (active instanceof HTMLElement && active !== this.document.body ? active : null);
    this.paletteOpen.set(true);
  }

  protected closePalette(): void {
    this.paletteOpen.set(false);
    const origin = this.paletteOrigin;
    this.paletteOrigin = null;
    // Restore focus after the overlay is gone, unless navigation already moved it.
    afterNextRender(() => {
      if (!this.paletteOpen() && this.document.activeElement === this.document.body) {
        restoreOverlayFocus(origin, this.document.querySelector<HTMLElement>('.search-trigger'));
      }
    }, { injector: this.injector });
  }

  reload(): void {
    location.reload();
  }

  openNotice(link: NotificationLink): void {
    if (link.dailyDay && link.dailyUnit) {
      this.navigation.go('/reports/daily', { workspace: link.workspace, day: link.dailyDay, unit: link.dailyUnit });
      return;
    }
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
