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
    ['wall', { width: 1920, height: 1080 }],
    ['desktop', { width: 1366, height: 768 }],
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
    await expect(dashboard.locator('fimra-page-header')).toContainText(/\(Circle\) · Inside forest · Last 6 days/);
    await expect(dashboard.locator('fimra-page-header time')).toContainText('IST');
    await expect(dashboard.locator('fimra-stat-tile time')).toHaveCount(0);
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
        await expect(sheet.getByRole('button', { name: /^Unit / })).toBeVisible();
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

    const tiles = dashboard.locator('fimra-stat-tile a');
    for (const label of await tiles.allTextContents()) {
      const link = dashboard.getByRole('link', { name: label.trim(), exact: true });
      const countID = await link.getAttribute('aria-describedby');
      const expected = Number((await dashboard.locator('#' + countID).innerText()).replaceAll(',', ''));
      const target = new URL(await link.getAttribute('href'), WEB);
      const fieldReports = label.trim() === 'Awaiting review';
      const overdue = label.trim() === 'Overdue alerts';
      assert.equal(target.pathname, fieldReports ? '/reports/field' : '/monitor/alerts');
      for (const [key, value] of Object.entries({ workspace: 'demo', hours: '144', class: 'inside', unitPath: 'ds.c1' })) {
        assert.equal(target.searchParams.get(key), overdue && key !== 'workspace' ? null : value, label);
      }
      if (overdue) assert.equal(target.searchParams.get('alertSort'), 'overdue');
      if (fieldReports) assert.equal(target.searchParams.get('decision'), 'pending');
      if (label.trim() === 'Waiting for a report') assert.equal(target.searchParams.get('group'), 'pending_reports');
      const matchesList = url => {
        if (url.origin !== API || !url.pathname.endsWith(fieldReports ? '/submissions' : '/alerts')) return false;
        if (fieldReports && url.searchParams.get('limit') === '1') return false;
        if (overdue && url.searchParams.get('sort') !== 'overdue') return false;
        return ['hours', 'class', 'unitPath', 'decision', 'queue', 'group', 'status'].every(key => {
          const value = target.searchParams.get(key);
          return url.searchParams.get(key) === (key === 'queue' && value === 'all' ? null : value);
        });
      };
      let records;
      await page.route(matchesList, async route => {
        const response = await route.fetch();
        assert.ok(response.ok(), `${response.status()} for ${response.url()}`);
        records = await response.json();
        await route.fulfill({ response });
      });
      const request = page.waitForResponse(response => matchesList(new URL(response.url())));
      await Promise.all([request, link.click()]);
      await page.unroute(matchesList);
      target.searchParams.sort();
      await expect(page).toHaveURL(url => {
        url.searchParams.sort();
        return url.href === target.href;
      });
      const queue = target.searchParams.get('queue');
      const total = fieldReports ? (records.counts.pending ?? 0) : queue === 'all'
        ? Object.values(records.counts).reduce((sum, count) => sum + count, 0) : (records.counts[queue] ?? 0);
      assert.equal(total, expected, label);
      if (records.items.length) {
        const list = fieldReports ? page.locator('fimra-submissions') : page.getByRole('complementary', { name: 'Alert queue' });
        await expect(list.locator('fimra-data-table tbody tr, fimra-data-table .result-card')).toHaveCount(records.items.length);
      }
      if (overdue) {
        const deadlines = records.items.map(row => Date.parse(row.due_at));
        assert.deepEqual(deadlines, [...deadlines].sort((a, b) => a - b));
        await page.getByRole('button', { name: 'Export', exact: true }).click();
        const dialog = page.getByRole('dialog', { name: 'Export', exact: true });
        await expect(dialog.getByText('Overdue alerts', { exact: true })).toBeVisible();
        await expect(dialog.getByText('Any observation date', { exact: true })).toBeVisible();
        await expect(dialog.getByRole('status')).toHaveText(expected.toLocaleString('en-IN'));
        await expect(dialog.getByRole('button', { name: 'Prepare export as Excel workbook' })).toBeEnabled();
        await page.keyboard.press('Escape');
      }
      if (fieldReports || label.trim() === 'Waiting for a report') {
        if (fieldReports) {
          await expect(page.getByRole('list', { name: 'Active filters' })
            .getByRole('button', { name: 'Remove filter Awaiting review' })).toBeVisible();
        } else {
          await expect(page.getByRole('complementary', { name: 'Alert queue' })
            .getByText('Waiting for a report', { exact: true })).toBeVisible();
        }
        for (const theme of ['light', 'dark']) {
          await page.emulateMedia({ colorScheme: theme });
          await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
          await check(page, (fieldReports ? 'awaiting-review' : 'waiting-for-report') + '-' + size + '-' + theme);
        }
        console.log(label.trim() + ' — ' + expected + ' in both the tile and its destination list');
      }
      await page.goBack();
      await expect(heading).toBeVisible();
      await expect(active).toBeVisible();
      await expect(chart).toHaveCount(0);
    }
    console.log(size + ' — every linked dashboard count matches its destination list');
    await context.close();
  }
} finally {
  await browser.close();
}
