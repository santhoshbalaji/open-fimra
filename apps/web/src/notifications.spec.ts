import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Notifications } from './notifications';

const endpoint = 'https://api.test/api/v1/notifications';
const alert = {
  id: '51', alertId: '42', workspace: 'demo', forestClass: 'inside',
  beatName: 'Beat 05', divisionName: 'Hill Division', event: 'alert_new',
  createdAt: '2026-11-25T00:00:00Z', readAt: null
};
const feed = { ...alert, id: '50', event: 'feed_open', alertId: null };
const readAlert = { ...alert, readAt: alert.createdAt };
const page = { unreadCount: 2, items: [alert, feed], hasMore: false };
const empty = { unreadCount: 0, items: [], hasMore: false };
const unavailable = { status: 503, statusText: 'Unavailable' };

describe('Notifications', () => {
  let fixture: ComponentFixture<Notifications>;
  let http: HttpTestingController;
  const element = <T extends HTMLElement = HTMLElement>(selector: string): T =>
    fixture.nativeElement.querySelector(selector);
  const bell = () => element<HTMLButtonElement>('.icon-button');
  const inbox = () => element('#notification-inbox');
  const button = (label: string) => Array.from(fixture.nativeElement.querySelectorAll('button') as NodeListOf<HTMLButtonElement>)
    .find(button => button.textContent?.trim() === label)!;
  const row = (id: string) => element(`[data-notice-id="${id}"]`);
  const get = (query = 'view=all') => http.expectOne({ method: 'GET', url: endpoint + '?' + query });
  const post = (path: string) => http.expectOne({ method: 'POST', url: endpoint + '/' + path });

  function open(result = page as object) {
    bell().click();
    fixture.detectChanges();
    get().flush(result);
    fixture.detectChanges();
  }

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [Notifications], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
    fixture = TestBed.createComponent(Notifications);
    http = TestBed.inject(HttpTestingController);
    get().flush(page);
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
  });

  it('moves focus into the inbox and returns it to the bell on Escape and Close', () => {
    expect(bell().getAttribute('aria-label')).toBe('Notifications, 2 unread');
    expect(inbox()).toBeNull();
    open();
    expect(document.activeElement).toBe(element('section h2'));
    expect(bell().getAttribute('aria-expanded')).toBe('true');
    row(alert.id).querySelector('a')?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    fixture.detectChanges();
    expect(bell().getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(bell());

    open();
    button('Close').click();
    fixture.detectChanges();
    expect(inbox()).toBeNull();
    expect(document.activeElement).toBe(bell());
  });

  it('filters the entire inbox, keeps the total unread count and handles an empty filter', () => {
    open({ ...page, unreadCount: 1, items: [readAlert, feed] });
    expect(button('All').getAttribute('aria-pressed')).toBe('true');
    button('Unread').click();
    fixture.detectChanges();
    expect(row(alert.id)).toBeTruthy();
    get('view=unread').flush({ ...page, unreadCount: 1, items: [feed] });
    fixture.detectChanges();
    expect(button('Unread').getAttribute('aria-pressed')).toBe('true');
    expect(row(alert.id)).toBeNull();
    expect(row(feed.id)).toBeTruthy();
    expect(bell().getAttribute('aria-label')).toBe('Notifications, 1 unread');

    button('Mark all as read').focus();
    button('Mark all as read').click();
    post('read-all').flush(null);
    fixture.detectChanges();
    expect(inbox().textContent).toContain('You have no unread notifications.');
    expect(document.activeElement).toBe(button('Mark all as read'));

    button('All').click();
    get().flush({ ...page, unreadCount: 0, items: [readAlert, { ...feed, readAt: feed.createdAt }] });
    fixture.detectChanges();
    expect(row(alert.id)).toBeTruthy();
    expect(row(feed.id)).toBeTruthy();
  });

  it('marks a row without navigating and retains position on pending, error and Retry', () => {
    const follow = vi.fn();
    fixture.componentInstance.openNotice.subscribe(follow);
    open({ ...page, hasMore: true });
    button('Show more').click();
    get('view=all&before=50').flush({ ...page, items: [{ ...feed, id: '49', readAt: feed.createdAt }] });
    fixture.detectChanges();
    const list = element('.notification-list');
    list.scrollTop = 120;
    const mark = row(alert.id).querySelector('button')!;
    mark.focus();
    mark.click();
    fixture.detectChanges();
    expect(inbox().textContent).toContain('Updating notifications');
    expect(document.activeElement).toBe(mark);
    expect(list.scrollTop).toBe(120);
    post('51/read').flush(null, unavailable);
    fixture.detectChanges();
    expect(inbox().textContent).toContain('Could not mark the notification as read.');
    expect(document.activeElement).toBe(mark);
    expect(list.scrollTop).toBe(120);
    button('Retry').click();
    post('51/read').flush(null);
    fixture.detectChanges();
    expect(row(alert.id).querySelector('button')).toBeNull();
    expect(row('49')).toBeTruthy();
    expect(list.scrollTop).toBe(120);
    expect(element('#notification-inbox fimra-live-status').textContent?.trim()).toBe('1 unread');
    expect(element('#notification-inbox fimra-live-status').getAttribute('aria-live')).toBe('polite');
    expect(follow).not.toHaveBeenCalled();
  });

  it.each(['all', 'unread'])('moves focus to a nearby link when a mark control disappears in %s', filter => {
    open();
    if (filter === 'unread') {
      button('Unread').click();
      get('view=unread').flush(page);
      fixture.detectChanges();
    }
    const mark = row(alert.id).querySelector('button')!;
    mark.focus();
    mark.click();
    post('51/read').flush(null);
    fixture.detectChanges();
    expect(document.activeElement).toBe(row(filter === 'all' ? alert.id : feed.id).querySelector('a'));
  });

  it('keeps all loaded rows after a bulk update in All', () => {
    open();
    button('Mark all as read').click();
    post('read-all').flush(null, unavailable);
    fixture.detectChanges();
    expect(row(alert.id).querySelector('button')).toBeTruthy();
    expect(inbox().textContent).toContain('Could not mark all notifications as read.');
    button('Retry').click();
    post('read-all').flush(null);
    fixture.detectChanges();
    expect(inbox().querySelectorAll('li')).toHaveLength(2);
    expect(inbox().querySelectorAll('[data-mark-read]')).toHaveLength(0);
    expect(bell().getAttribute('aria-label')).toBe('Notifications, 0 unread');
  });

  it.each([
    { item: alert, search: '?workspace=demo&alert=42', label: 'Inside forest · Beat 05, Hill Division' },
    { item: feed, search: '?workspace=demo&view=feed-health', label: 'Feed needs attention' }
  ])('marks $label read and emits the routed destination when following the link', ({ item, search, label }) => {
    const follow = vi.fn();
    fixture.componentInstance.openNotice.subscribe(follow);
    open();
    const link = row(item.id).querySelector('a')!;
    expect(link.textContent).toBe(label);
    expect(new URL(link.href).search).toBe(search);
    link.click();
    expect(follow).toHaveBeenCalledWith({ workspace: 'demo', alert: item.alertId });
    post(item.id + '/read').flush(null);
    fixture.detectChanges();
    expect(inbox()).toBeNull();
    expect(bell().getAttribute('aria-label')).toBe('Notifications, 1 unread');
  });

  it('shows relative age, Indian Standard Time and a title for an unmapped alert', () => {
    fixture.componentInstance.now.set(new Date(Date.parse(alert.createdAt) + 7200000));
    open({ ...page, items: [{ ...alert, beatName: null, divisionName: null }] });
    expect(row(alert.id).querySelector('a')?.textContent).toContain('Inside forest · Unknown unit');
    const time = row(alert.id).querySelector('time');
    expect(time?.textContent).toBe('2 hours ago');
    expect(time?.title).toContain('IST');
  });

  it('retries the same list request after an error and shows an empty inbox', () => {
    bell().click();
    get().flush(null, unavailable);
    fixture.detectChanges();
    expect(inbox().textContent).toContain('Could not load notifications.');
    expect(row(alert.id)).toBeTruthy();
    const markAll = button('Mark all as read');
    expect(markAll.getAttribute('aria-disabled')).toBe('true');
    markAll.click();
    http.expectNone({ method: 'POST', url: endpoint + '/read-all' });
    button('Retry').click();
    get().flush(page);
    fixture.detectChanges();
    expect(markAll.hasAttribute('aria-disabled')).toBe(false);

    button('Unread').click();
    get('view=unread').flush(empty);
    fixture.detectChanges();
    expect(inbox().textContent).toContain('You have no unread notifications.');
    expect(row(alert.id)).toBeNull();
  });
});
