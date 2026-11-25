import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Component, DestroyRef, inject, output, signal, viewChild, ElementRef } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { classes, ForestClass, relativeAge } from './detections';
import { EmptyState } from './ui/empty-state';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Icon } from './ui/icon';

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
  event: 'alert_new' | 'alert_overdue' | 'feed_open';
  createdAt: string;
  readAt: string | null;
  workspace: string;
  alertId: string | null;
  forestClass: ForestClass | null;
  beatName: string | null;
  divisionName: string | null;
}
interface Inbox { unreadCount: number; items: Notice[]; hasMore: boolean }

@Component({
  selector: 'fimra-notifications',
  imports: [HlmButtonImports, HlmSkeletonImports, EmptyState, Icon],
  templateUrl: './notifications.html',
  styleUrl: './notifications.css'
})
export class Notifications {
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly destroy = inject(DestroyRef);
  private readonly endpoint = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '') + '/api/v1/notifications';
  private readonly trigger = viewChild.required<ElementRef<HTMLButtonElement>>('bell');
  private readonly pending = new Set<string>();
  private refreshAfterLoad = false;
  private offset = 0;
  readonly openNotice = output<NotificationLink>();
  readonly open = signal(false);
  readonly inbox = signal<Inbox | null>(null);
  readonly error = signal('');
  readonly loading = signal(false);
  readonly labels = { alert_new: 'New fire alert', alert_overdue: 'Fire alert overdue', feed_open: 'Feed needs attention' };
  readonly date = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  readonly now = signal(Date.now());
  readonly relativeAge = relativeAge;
  readonly timestamp = Date.parse;

  title(item: Notice): string {
    if (!item.alertId) return this.labels[item.event];
    const place = [item.beatName, item.divisionName].filter(Boolean).join(', ');
    const title = item.forestClass ? classes[item.forestClass] : 'Fire alert';
    return title + ' · ' + (place || 'Unknown unit');
  }

  constructor() {
    this.load();
    const timer = setInterval(() => {
      this.now.set(Date.now());
      if (!this.open()) this.load();
    }, 60_000);
    this.destroy.onDestroy(() => clearInterval(timer));
  }

  toggle(): void {
    this.open.update(open => !open);
    if (this.open()) this.load();
  }

  close(event: Event): void {
    if (!this.open()) return;
    event.stopPropagation();
    this.open.set(false);
    this.trigger().nativeElement.focus();
  }

  link(item: Notice): string {
    return notificationURL({ workspace: item.workspace, alert: item.alertId }).href;
  }

  follow(event: MouseEvent, item: Notice): void {
    this.read(item);
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    this.close(event);
    this.openNotice.emit({ workspace: item.workspace, alert: item.alertId });
  }

  read(item: Notice): void {
    if (item.readAt || this.pending.has(item.id)) return;
    this.pending.add(item.id);
    this.http.post(this.endpoint + '/' + item.id + '/read', null).pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: () => {
        this.pending.delete(item.id);
        this.load();
      },
      error: (err: HttpErrorResponse) => {
        this.pending.delete(item.id);
        if (err.status === 401) this.auth.sessionEnded();
        this.error.set('Could not mark the notification as read.');
      }
    });
  }

  load(more = false): void {
    if (this.loading()) {
      if (!more) this.refreshAfterLoad = true;
      return;
    }
    this.loading.set(true);
    const offset = more ? this.offset : 0;
    this.http.get<Inbox>(this.endpoint, { params: { offset } }).pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: page => {
        const previous = more ? this.inbox()?.items ?? [] : [];
        const items = new Map([...previous, ...page.items].map(item => [item.id, item]));
        this.inbox.set({ ...page, items: [...items.values()] });
        this.error.set('');
        this.offset = offset + page.items.length;
        this.loading.set(false);
        if (this.refreshAfterLoad) {
          this.refreshAfterLoad = false;
          this.load();
        }
      },
      error: (err: HttpErrorResponse) => {
        if (err.status === 401) this.auth.sessionEnded();
        this.error.set('Could not load notifications.');
        this.refreshAfterLoad = false;
        this.loading.set(false);
      }
    });
  }
}
