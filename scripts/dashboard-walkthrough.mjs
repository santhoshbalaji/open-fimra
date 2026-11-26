import assert from 'node:assert/strict';
import { isAbsolute, join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
assert.ok(WEB && API && SHOTS && isAbsolute(SHOTS), 'Set WEB, API and an absolute screenshot directory outside the checkout.');
const loadedPath = '/dashboard?hours=144&class=inside&unitPath=ds.c1';
const emptyPath = '/dashboard?hours=1&class=fixed_heat_source';
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });

async function check(page, label) {
  const { violations } = await new AxeBuilder({ page }).analyze();
  assert.deepEqual(violations.filter(item => item.impact === 'serious' || item.impact === 'critical')
    .map(item => ({ id: item.id, elements: item.nodes.map(node => node.target) })), [], label);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), label + ' page overflows');
  await page.screenshot({ path: join(SHOTS, label + '.png') });
  console.log(label + ' — zero serious or critical axe violations');
}

try {
  for (const [size, viewport] of [
    ['desktop', { width: 1440, height: 900 }],
    ['phone', { width: 390, height: 844 }]
  ]) {
    const context = await browser.newContext({ viewport, colorScheme: 'light', reducedMotion: 'reduce' });
    const page = await context.newPage();
    page.setDefaultTimeout(25_000);
    await page.goto(WEB + loadedPath);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.locator('#username').fill('state.admin');
    await page.locator('#password').fill('demo');
    await page.locator('#kc-login').click();

    const dashboard = page.locator('fimra-dashboard');
    const heading = page.getByRole('heading', { level: 1, name: 'Dashboard', exact: true });
    const active = dashboard.getByRole('link', { name: 'Active alerts', exact: true });
    const chart = dashboard.locator('fimra-class-chart');
    const tables = dashboard.locator('table');
    const total = tables.locator('tfoot td').first();
    await expect(active).toBeVisible();
    await expect(dashboard.locator('fimra-page-header')).toContainText('Units under ds.c1 · Inside forest · Past 144 hours');
    await expect(dashboard.locator('fimra-page-header time')).toContainText('IST');
    for (const theme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await dashboard.evaluate(el => { el.scrollTop = 0; });
      await expect(chart).toBeVisible();
      const count = (await total.innerText()).trim();
      assert.ok(Number(count) > 0);
      await expect(chart.locator('p span').last()).toHaveText(count);
      await check(page, 'loaded-' + size + '-' + theme);

      if (size === 'phone') {
        await dashboard.getByRole('button', { name: 'Filters', exact: true }).click();
        const sheet = page.getByRole('dialog', { name: 'Filters' });
        await expect(sheet.getByRole('textbox', { name: 'Unit path' })).toBeVisible();
        await check(page, 'filters-' + size + '-' + theme);
        await sheet.getByRole('button', { name: 'Done' }).click();
        await expect(dashboard.getByRole('button', { name: 'Filters', exact: true })).toBeFocused();
        await tables.scrollIntoViewIfNeeded();
        await page.screenshot({ path: join(SHOTS, 'loaded-details-phone-' + theme + '.png') });
      }

      await page.goto(WEB + emptyPath);
      await expect(dashboard.getByText('Zero detections recorded in this window')).toBeVisible();
      await expect(total).toHaveText('0');
      await expect(chart).toHaveCount(0);
      await check(page, 'empty-' + size + '-' + theme);
      if (size === 'phone') {
        await tables.scrollIntoViewIfNeeded();
        await page.screenshot({ path: join(SHOTS, 'empty-details-phone-' + theme + '.png') });
      }

      // Fail only the feed-health part so the seeded detection data stays available.
      const summary = url => url.origin === API && url.pathname.endsWith('/summary');
      await page.route(summary, async route => {
        const response = await route.fetch();
        await route.fulfill({ response, json: { ...await response.json(), feedHealth: null, feedHealthUnavailable: true } });
      });
      await page.goto(WEB + loadedPath);
      await expect(dashboard.getByText('Feed health could not load', { exact: true })).toBeVisible();
      await expect(total).toHaveText(count);
      await check(page, 'feed-error-' + size + '-' + theme);
      if (size === 'phone') {
        await dashboard.getByRole('heading', { name: 'Feed health' }).scrollIntoViewIfNeeded();
        await page.screenshot({ path: join(SHOTS, 'feed-error-details-phone-' + theme + '.png') });
      }
      await page.unroute(summary);
      await dashboard.getByRole('button', { name: 'Retry' }).click();
      await expect(dashboard.getByText('Feed health could not load', { exact: true })).toHaveCount(0);
    }

    const values = () => dashboard.locator('dd, td, time').allTextContents();
    const before = await values();
    const filters = dashboard.getByRole('button', { name: 'Filters', exact: true });
    if (size === 'phone') await filters.click();
    await page.getByRole('switch', { name: 'Low bandwidth' }).click();
    if (size === 'phone') await page.getByRole('button', { name: 'Done' }).click();
    await expect(chart).toHaveCount(0);
    assert.deepEqual(await values(), before);

    const countID = await active.getAttribute('aria-describedby');
    const count = dashboard.locator('#' + countID);
    const expected = Number(await count.innerText());
    assert.ok(expected > 0);
    await count.scrollIntoViewIfNeeded();
    const bounds = await count.boundingBox();
    const request = page.waitForResponse(response => {
      const url = new URL(response.url());
      return url.origin === API && url.pathname.endsWith('/alerts') && url.searchParams.get('group') === 'active';
    });
    await page.mouse.click(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
    await expect(page).toHaveURL(/\/monitor\/alerts\?/);
    const url = new URL(page.url());
    assert.equal(url.pathname, '/monitor/alerts');
    assert.deepEqual(Object.fromEntries(url.searchParams), {
      workspace: 'demo', hours: '144', class: 'inside', unitPath: 'ds.c1', queue: 'all', group: 'active'
    });
    const response = await request;
    assert.ok(response.ok(), await response.text());
    const records = await response.json();
    assert.equal(records.items.length, expected);
    const queue = page.getByRole('complementary', { name: 'Alert queue' });
    await expect(queue.locator('[data-alert-id]')).toHaveCount(expected);
    await page.goBack();
    await expect(heading).toBeVisible();
    await expect(chart).toHaveCount(0);
    await context.close();
  }
} finally {
  await browser.close();
}
