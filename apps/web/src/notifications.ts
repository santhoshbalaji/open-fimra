import { translate } from './language';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, ElementRef, Injector, afterNextRender, computed, inject, output, signal, viewChild } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Observable } from 'rxjs';
import { classes, ForestClass } from './detections';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { workspaceFormat } from './workspace-format';
import { Icon } from './ui/icon';
import { LiveStatus } from './ui/live-status';
import { SidePanel } from './ui/side-panel';
import { DataState, StateRegion } from './ui/state-region';
import { StatusBadge } from './ui/status-badge';

export interface NotificationLink { workspace: string; alert: string | null }

export function notificationURL(link: NotificationLink): URL {
  const url = new URL(document.baseURI);
  const params = new URLSearchParams({ workspace: link.workspace });
  if (link.alert) params.set('alert', link.alert);
  else params.set('view', 'feed-health');
  url.search = params.toString();
  return url;
}

interface Notice {
  id: string;
  event: 'alert_new' | 'alert_overdue' | 'feed_open' | 'incident_overdue' | 'incident_escalated';
  createdAt: string;
  readAt: string | null;
  workspace: string;
  alertId: string | null;
  forestClass: ForestClass | null;
  beatName: string | null;
  divisionName: string | null;
}
interface Inbox { unreadCount: number; items: Notice[]; hasMore: boolean }
type InboxFilter = 'all' | 'unread';

@Component({
  selector: 'fimra-notifications',
  imports: [HlmButtonImports, Icon, LiveStatus, SidePanel, StateRegion, StatusBadge],
  templateUrl: './notifications.html',
  styleUrl: './notifications.css'
})
export class Notifications {
  protected readonly msg = translate;
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly destroy = inject(DestroyRef);
  private readonly injector = inject(Injector);
  private readonly endpoint = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '') + '/api/v1/notifications';
  private readonly panel = viewChild.required(SidePanel);
  private readonly list = viewChild<ElementRef<HTMLElement>>('list');
  private readonly unreadFilter = viewChild<ElementRef<HTMLButtonElement>>('unreadFilter');
  private readonly showMore = viewChild<ElementRef<HTMLButtonElement>>('showMore');
  private readonly failure = signal<{ message: string; retry: () => void } | null>(null);
  private readonly loadedAt = signal<Date | null>(null);
  private before: string | undefined;
  readonly openNotice = output<NotificationLink>();
  readonly inbox = signal<Inbox | null>(null);
  readonly filter = signal<InboxFilter>('all');
  readonly busy = signal(false);
  readonly labels = { alert_new: 'New fire alert', alert_overdue: 'Fire alert overdue', feed_open: 'Feed needs attention',
    incident_overdue: 'Incident response overdue', incident_escalated: 'Incident response escalated' };
  readonly now = signal(new Date());
  readonly count = computed(() => this.inbox()?.unreadCount ?? 0);
  readonly unreadMessage = computed(() => this.inbox() ? translate('{0} unread', [this.format.number(this.count())]) : '');
  readonly bellLabel = computed(() => [translate('Notifications'), this.unreadMessage()].filter(Boolean).join(', '));
  readonly state = computed<DataState>(() => {
    const failure = this.failure();
    if (failure) return { kind: 'error', heading: failure.message };
    if (this.busy()) {
      const at = this.loadedAt();
      return at
        ? { kind: 'stale', heading: translate('Updating notifications'), observedAt: { dateTime: at.toISOString(), label: this.format.dateTime(at) } }
        : { kind: 'loading', heading: translate('Loading notifications') };
    }
    const inbox = this.inbox();
    if (inbox && !inbox.items.length && !inbox.hasMore) {
      return { kind: 'empty', heading: this.filter() === 'unread' ? translate('You have no unread notifications.') : translate('You have no notifications.') };
    }
    return { kind: 'ready' };
  });
  private readonly format = workspaceFormat;

  constructor() {
    this.load('all');
    const timer = setInterval(() => {
      this.now.set(new Date());
      if (!this.panel().open() && !this.failure()) this.load(this.filter());
    }, 60_000);
    this.destroy.onDestroy(() => clearInterval(timer));
  }

  toggle(bell: HTMLButtonElement): void {
    const panel = this.panel();
    if (panel.open()) {
      panel.close();
      return;
    }
    panel.show(bell);
    if (!this.failure()) this.load(this.filter());
  }

  title(item: Notice): string {
    if (!item.alertId) return translate(this.labels[item.event]);
    const place = [item.beatName, item.divisionName].filter(Boolean).join(', ');
    const title = item.forestClass ? translate(classes[item.forestClass]) : translate('Fire alert');
    return title + ' · ' + (place || translate('Unknown unit'));
  }

  dateTime(value: string): string {
    return this.format.dateTime(new Date(value));
  }

  age(value: string): string {
    return this.format.age(new Date(value), this.now());
  }

  link(item: Notice): string {
    return notificationURL({ workspace: item.workspace, alert: item.alertId }).href;
  }

  follow(event: MouseEvent, item: Notice): void {
    this.markRead(item);
    if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    this.panel().close();
    this.openNotice.emit({ workspace: item.workspace, alert: item.alertId });
  }

  selectFilter(filter: InboxFilter): void {
    if (filter !== this.filter()) this.load(filter);
  }

  loadMore(): void {
    if (this.before && this.inbox()?.hasMore) this.load(this.filter(), this.before);
  }

  private load(filter: InboxFilter, before?: string): void {
    const params: Record<string, string> = { view: filter };
    if (before) params['before'] = before;
    this.request(this.http.get<Inbox>(this.endpoint, { params }), translate('Could not load notifications.'),
      () => this.load(filter, before), page => {
        const list = this.list()?.nativeElement;
        const switching = filter !== this.filter();
        const previous = before ? this.inbox()?.items ?? [] : [];
        const items = [...previous, ...page.items];
        if (!switching) this.keepListFocus(items, before ? page.items[0]?.id : undefined);
        this.before = page.items.at(-1)?.id;
        this.filter.set(filter);
        this.inbox.set({ ...page, items });
        if (switching && list) list.scrollTop = 0;
      });
  }

  markRead(item: Notice): void {
    if (item.readAt) return;
    this.request(this.http.post(this.endpoint + '/' + item.id + '/read', null), translate('Could not mark the notification as read.'),
      () => this.markRead(item), () => this.updateRead(item.id));
  }

  markAllRead(): void {
    if (!this.count()) return;
    this.request(this.http.post(this.endpoint + '/read-all', null), translate('Could not mark all notifications as read.'),
      () => this.markAllRead(), () => this.updateRead());
  }

  retry(): void {
    this.failure()?.retry();
  }

  private updateRead(id?: string): void {
    const inbox = this.inbox();
    if (!inbox) return;
    const readAt = new Date().toISOString();
    const unreadOnly = this.filter() === 'unread';
    const items = inbox.items
      .map(item => !item.readAt && (!id || item.id === id) ? { ...item, readAt } : item)
      .filter(item => !unreadOnly || !item.readAt);
    this.keepListFocus(items);
    this.inbox.set({
      items,
      unreadCount: id ? Math.max(0, inbox.unreadCount - 1) : 0,
      hasMore: (id || !unreadOnly) ? inbox.hasMore : false
    });
  }

  private keepListFocus(items: Notice[], firstAddedId?: string): void {
    const list = this.list()?.nativeElement;
    if (!list) return;
    const focused = list.ownerDocument.activeElement;
    const row = focused?.closest<HTMLElement>('[data-notice-id]');
    const id = row?.dataset['noticeId'];
    let targetId = firstAddedId;
    if (row && id && list.contains(row)) {
      const remaining = items.find(item => item.id === id);
      if (remaining && !(remaining.readAt && focused?.matches('[data-mark-read]'))) return;
      const index = (this.inbox()?.items ?? []).findIndex(item => item.id === id);
      targetId = remaining?.id ?? items[Math.min(index, items.length - 1)]?.id;
    } else if (!focused || focused !== this.showMore()?.nativeElement) {
      return;
    }
    afterNextRender(() => {
      // Leave focus alone if the reader has already moved it elsewhere.
      const active = list.ownerDocument.activeElement;
      if (active !== focused && active !== list.ownerDocument.body) return;
      const link = targetId ? list.querySelector<HTMLAnchorElement>(`[data-notice-id="${targetId}"] a`) : null;
      (link ?? this.showMore()?.nativeElement ?? this.unreadFilter()?.nativeElement)?.focus({ preventScroll: true });
    }, { injector: this.injector });
  }

  private request<T>(request: Observable<T>, message: string, retry: () => void, apply: (result: T) => void): void {
    if (this.busy()) return;
    this.failure.set(null);
    this.busy.set(true);
    request.pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: result => {
        apply(result);
        this.loadedAt.set(new Date());
        this.busy.set(false);
      },
      error: (err: HttpErrorResponse) => {
        if (err.status === 401) this.auth.sessionEnded();
        this.failure.set({ message, retry });
        this.busy.set(false);
      }
    });
  }
}
