import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Notifications } from './notifications';

const endpoint = 'https://api.test/api/v1/notifications';
const item = {
  id: '51', alertId: '42', workspace: 'demo', event: 'alert_new',
  createdAt: '2026-11-25T00:00:00Z', readAt: null
};
const page = { unreadCount: 1, items: [item], hasMore: false };

describe('Notifications', () => {
  let fixture: ComponentFixture<Notifications>;
  let http: HttpTestingController;
  const bell = () => fixture.nativeElement.querySelector('button') as HTMLButtonElement;
  const list = () => fixture.nativeElement.querySelector('section') as HTMLElement;
  const flush = (result: object) => http.expectOne(endpoint + '?offset=0').flush(result);

  beforeEach(() => {
    TestBed.configureTestingModule({ imports: [Notifications], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
    fixture = TestBed.createComponent(Notifications);
    http = TestBed.inject(HttpTestingController);
    flush(page);
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
  });

  it('shows the unread count and restores keyboard focus after Escape', () => {
    expect(bell().getAttribute('aria-label')).toBe('Notifications, 1 unread');
    expect(list().hidden).toBe(true);
    bell().click();
    flush(page);
    fixture.detectChanges();
    expect(bell().getAttribute('aria-expanded')).toBe('true');
    expect(list().textContent).toContain('Unread');
    list().querySelector('a')?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    fixture.detectChanges();
    expect(bell().getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(bell());
  });

  it('links to the alert, marks its notification read and updates the count', () => {
    const open = vi.fn();
    fixture.componentInstance.openNotice.subscribe(open);
    bell().click();
    flush(page);
    fixture.detectChanges();
    const link = list().querySelector('a')!;
    expect(link.textContent).toContain('New fire alert #42');
    expect(new URL(link.href).search).toBe('?workspace=demo&alert=42');
    link.click();
    expect(open).toHaveBeenCalledWith({ workspace: 'demo', alert: '42' });
    http.expectOne({ method: 'POST', url: endpoint + '/51/read' }).flush(null);
    flush({ ...page, unreadCount: 0, items: [{ ...item, readAt: item.createdAt }] });
    fixture.detectChanges();
    expect(bell().getAttribute('aria-label')).toBe('Notifications, 0 unread');
    expect(list().hidden).toBe(true);
  });

  it('links feed notifications to feed health', () => {
    bell().click();
    flush({ ...page, items: [{ ...item, event: 'feed_open', alertId: null }] });
    fixture.detectChanges();
    const link = list().querySelector('a')!;
    expect(link.textContent).toContain('Feed needs attention');
    expect(new URL(link.href).search).toBe('?workspace=demo&view=feed-health');
  });

  it('shows an empty list when there are no notifications', () => {
    bell().click();
    flush({ unreadCount: 0, items: [], hasMore: false });
    fixture.detectChanges();
    expect(list().textContent).toContain('You have no notifications.');
  });
});
