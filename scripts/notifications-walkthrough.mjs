import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect as playwrightExpect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
const desktop = { width: 1440, height: 900 };
const phone = { width: 390, height: 844 };
const expect = playwrightExpect.configure({ timeout: 25_000 });
const empty = { unreadCount: 0, items: [], hasMore: false };

try {
  const context = await browser.newContext({ viewport: desktop, colorScheme: 'light', reducedMotion: 'reduce' });
  const page = await context.newPage();
  const bell = page.getByRole('button', { name: /^Notifications(,|$)/ });
  const inbox = page.locator('#notification-inbox');
  const heading = page.getByRole('heading', { name: 'Notifications', exact: true });
  const list = inbox.locator('.notification-list');
  const links = list.getByRole('link');
  const retry = inbox.getByRole('button', { name: 'Retry' });
  const message = text => inbox.locator('p').filter({ hasText: text });
  let inboxResponse;
  await page.route(url => url.origin === API && url.pathname === '/api/v1/notifications', async route => {
    if (inboxResponse) await route.fulfill(inboxResponse);
    else await route.continue();
  });

  async function openInbox() {
    await bell.focus();
    await page.keyboard.press('Enter');
    await expect(inbox).toBeVisible();
    await expect(heading).toBeFocused();
  }

  async function closeInbox() {
    await page.keyboard.press('Escape');
    await expect(inbox).toBeHidden();
    await expect(bell).toBeFocused();
  }

  async function checkState(state, size, theme) {
    const label = `${state} ${size} ${theme}`;
    const scope = size === 'phone' ? '[data-slot="sheet-content"]' : 'fimra-side-panel.inbox';
    const { violations } = await new AxeBuilder({ page }).include(scope)
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']).analyze();
    const serious = violations.filter(item => item.impact === 'serious' || item.impact === 'critical');
    assert.deepEqual(serious.map(item => ({ id: item.id, nodes: item.nodes.map(node => node.failureSummary) })), [], label);
    console.log(label + ' — zero serious or critical axe violations');
    await page.screenshot({ path: join(SHOTS, `${state}-${size}-${theme}.png`) });
  }

  async function tabTo(target) {
    for (let step = 0; step < 60; step++) {
      if (await target.evaluate(node => document.activeElement === node)) return;
      await page.keyboard.press('Tab');
    }
    await expect(target).toBeFocused();
  }

  await page.goto(WEB + '/monitor/alerts');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.locator('#username').fill('state.admin');
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
  await expect(page.getByRole('heading', { name: 'Alerts', level: 1 })).toBeVisible();
  await expect(bell).toHaveAccessibleName(/^Notifications, [1-9]\d* unread$/);

  for (const [size, viewport] of [['desktop', desktop], ['phone', phone]]) {
    await page.setViewportSize(viewport);
    for (const theme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await openInbox();
      await expect(links.first()).toBeVisible();
      await expect(message('Updating notifications')).toBeHidden();
      const close = page.getByRole('button', { name: 'Close', exact: true });
      await expect(close).toBeVisible();
      if (size === 'phone') {
        const sheet = page.getByRole('dialog', { name: 'Notifications' });
        const bounds = await sheet.locator('[data-slot="sheet-content"]').boundingBox();
        assert.equal(bounds.height, viewport.height);
      } else {
        const panel = await heading.locator('xpath=../..').boundingBox();
        const trigger = await bell.boundingBox();
        assert.ok(panel.y >= trigger.y + trigger.height && Math.abs(panel.x + panel.width - trigger.x - trigger.width) < 4);
      }
      await checkState('loaded', size, theme);
      if (size === 'phone') {
        await page.keyboard.press('Shift+Tab');
        await expect(close).toBeFocused();
      } else {
        await tabTo(close);
      }
      await page.keyboard.press('Enter');
      await expect(inbox).toBeHidden();
      await expect(bell).toBeFocused();

      inboxResponse = { status: 503, json: { message: 'Inbox unavailable' } };
      await openInbox();
      await expect(retry).toBeVisible();
      await expect(links.first()).toBeVisible();
      await checkState('error', size, theme);
      inboxResponse = undefined;
      await retry.press('Enter');
      await expect(retry).toBeHidden();
      await expect(message('Updating notifications')).toBeHidden();
      await closeInbox();

      inboxResponse = { json: empty };
      await openInbox();
      await expect(message('You have no notifications.')).toBeVisible();
      await expect(links).toHaveCount(0);
      await checkState('empty', size, theme);
      await closeInbox();
      inboxResponse = undefined;
    }
  }

  // On a phone, reach the mark action and both destinations by keyboard.
  await openInbox();
  await expect(inbox.getByRole('link', { name: 'Feed needs attention' })).toBeVisible();
  const unread = inbox.getByRole('button', { name: 'Unread', exact: true });
  await tabTo(unread);
  await page.keyboard.press('Enter');
  await expect(unread).toHaveAttribute('aria-pressed', 'true');
  const all = inbox.getByRole('button', { name: 'All', exact: true });
  await page.keyboard.press('Shift+Tab');
  await expect(all).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(all).toHaveAttribute('aria-pressed', 'true');
  const row = list.locator('li').filter({ has: page.locator('a[href*="alert="]') }).nth(7);
  const mark = row.getByRole('button', { name: /^Mark as read:/ });
  await tabTo(mark);
  const position = () => list.evaluate(node => node.scrollTop);
  const offset = await position();
  assert.ok(offset > 0, 'The inbox must scroll to reach the selected notice.');
  const noticeId = await row.getAttribute('data-notice-id');
  const alertRow = list.locator(`[data-notice-id="${noticeId}"]`);

  const markURL = API + '/api/v1/notifications/' + noticeId + '/read';
  let failMark;
  const pending = new Promise(resolve => { failMark = resolve; });
  await page.route(markURL, async route => {
    await pending;
    await route.fulfill({ status: 503, json: { message: 'Write unavailable' } });
  });
  await page.keyboard.press('Enter');
  await expect(message('Updating notifications')).toBeVisible();
  await expect(mark).toBeFocused();
  assert.equal(await position(), offset);
  failMark();
  await expect(retry).toBeVisible();
  await expect(mark).toBeFocused();
  assert.equal(await position(), offset);
  await page.unroute(markURL);

  // The failed mark is still actionable and moving to Retry keeps the list in place.
  await retry.press('Enter');
  await expect(alertRow.getByRole('button', { name: /^Mark as read:/ })).toHaveCount(0);
  assert.equal(await position(), offset);
  // Mark a second row to check where focus lands when its button disappears.
  await tabTo(all);
  const nextAlertRow = list.locator('li').first();
  const nextMark = nextAlertRow.getByRole('button', { name: /^Mark as read:/ });
  await tabTo(nextMark);
  await page.keyboard.press('Enter');
  const nextLink = nextAlertRow.getByRole('link');
  await expect(nextLink).toBeFocused();
  const nextAlert = new URL(await nextLink.getAttribute('href')).searchParams.get('alert');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp('/monitor/alerts\\?.*alert=' + nextAlert + '(?:&|$)'));
  await expect(page.getByRole('heading', { name: 'Alert #' + nextAlert, exact: true })).toBeVisible();
  await expect(inbox).toBeHidden();

  await openInbox();
  const feedLink = inbox.getByRole('link', { name: 'Feed needs attention', exact: true });
  await tabTo(feedLink);
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/dashboard\?.*view=feed-health/);
  await expect(page.getByRole('heading', { name: 'Feed health', exact: true })).toBeVisible();
  await expect(inbox).toBeHidden();

  await openInbox();
  await tabTo(unread);
  await page.keyboard.press('Enter');
  await expect(unread).toHaveAttribute('aria-pressed', 'true');
  const markAll = inbox.getByRole('button', { name: 'Mark all as read' });
  await tabTo(markAll);
  await page.keyboard.press('Enter');
  await expect(message('You have no unread notifications.')).toBeVisible();
  await expect(markAll).toBeFocused();
  await closeInbox();
  await expect(bell).toHaveAccessibleName('Notifications, 0 unread');
  console.log('Keyboard opened the inbox, marked notices, followed the alert and feed links, and closed the sheet.');

  const longNotice = {
    id: '1000', workspace: 'demo', alertId: '1', event: 'alert_new', forestClass: 'inside',
    beatName: 'उत्तर की पहाड़ियों के पास नदी के किनारे का वन क्षेत्र',
    divisionName: 'পূর্ব পাহাড়ের নদীর ধারের বন বিভাগ এবং সংরক্ষিত এলাকা',
    createdAt: new Date().toISOString(), readAt: null
  };
  inboxResponse = { json: { unreadCount: 1, items: [longNotice], hasMore: false } };
  const textSize = await page.addStyleTag({ content: ':root { font-size: 200%; }' });
  await openInbox();
  const longLink = links.first();
  await expect(longLink).toContainText(longNotice.beatName);
  await expect(longLink).toContainText(longNotice.divisionName);
  await tabTo(longLink);
  assert.ok(await list.evaluate(node => node.scrollWidth <= node.clientWidth), 'Long labels must wrap at 200% text size.');
  const sheet = page.getByRole('dialog', { name: 'Notifications' });
  await expect(sheet.getByRole('button', { name: 'Close', exact: true })).toBeVisible();
  await closeInbox();
  await textSize.evaluate(style => style.remove());
  console.log('Long place names wrapped on a phone at 200% text size.');
} finally {
  await browser.close();
}
