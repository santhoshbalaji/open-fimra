import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect as playwrightExpect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
const expect = playwrightExpect.configure({ timeout: 25_000 });
const viewport = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
const listRequest = url => url.origin === API && url.pathname.endsWith('/incidents');
const incidentLink = page => page.locator('[data-incident-id]');
const results = {};

async function capture(page, label) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'])
    .include('main').include('[role=dialog]').analyze();
  const serious = violations.filter(v => v.impact === 'serious' || v.impact === 'critical');
  assert.deepEqual(serious.map(v => ({ id: v.id, elements: v.nodes.map(node => node.target) })), [], label);
  await page.screenshot({ path: join(SHOTS, label + '.png') });
  console.log(label + ' — zero serious or critical axe violations');
}

async function open(page, row) {
  const id = await row.getAttribute('data-incident-id');
  await row.press('Enter');
  const heading = page.getByRole('heading', { name: 'Incident #' + id, exact: true });
  await expect(heading).toBeFocused();
  await expect(page.getByRole('heading', { name: /^Member alerts/ })).toBeVisible();
  return heading;
}

try {
  for (const user of ['state.admin', 'division.officer']) {
    const context = await browser.newContext({ viewport: viewport.desktop, reducedMotion: 'reduce' });
    const page = await context.newPage();
    await page.goto(WEB + '/monitor/incidents?hours=168');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.locator('#username').fill(user);
    await page.locator('#password').fill('demo');
    const firstList = page.waitForResponse(response => response.request().method() === 'GET' && listRequest(new URL(response.url())) && response.ok());
    await page.locator('#kc-login').click();
    results[user] = (await (await firstList).json()).items;
    await expect(incidentLink(page).first()).toBeVisible();

    for (const [size, dimensions] of Object.entries(viewport)) {
      await page.setViewportSize(dimensions);
      for (const theme of ['light', 'dark']) {
        const prefix = `${user}-${size}-${theme}`;
        await page.emulateMedia({ colorScheme: theme });
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        const row = incidentLink(page).first();
        if (size === 'desktop') {
          const pager = page.getByRole('group', { name: 'Incidents pages' });
          const list = page.getByRole('region', { name: 'Incident list' });
          await expect(pager).toBeVisible();
          const [pagerBox, listBox] = await Promise.all([pager.boundingBox(), list.boundingBox()]);
          assert.ok(listBox.y + listBox.height - pagerBox.y - pagerBox.height <= 20,
            'The pager stays at the bottom of the list');
        }
        await capture(page, prefix + '-loaded');
        await open(page, row);
        await expect(page.getByRole('heading', { name: 'Timeline', exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: /Alert #\d+/ }).first()).toBeVisible();
        if (size === 'phone') {
          const sheet = page.getByRole('dialog', { name: /^Incident #/ }).locator('hlm-sheet-content');
          await expect(sheet).toBeVisible();
          assert.equal((await sheet.boundingBox()).height, dimensions.height, 'The phone detail fills the viewport');
        }
        await capture(page, prefix + '-detail');
        await page.keyboard.press('Escape');
        await expect(page).not.toHaveURL(/incident=/);
        await expect(row).toBeFocused();

        await page.route(listRequest, route => route.fulfill({
          json: { items: [], total: 0 }, headers: { 'Access-Control-Allow-Origin': WEB }
        }));
        await page.reload();
        await expect(page.getByText('No incidents', { exact: true })).toBeVisible();
        await capture(page, prefix + '-empty');
        await page.unroute(listRequest);

        await page.route(listRequest, route => route.abort());
        await page.reload();
        await expect(page.getByRole('paragraph').filter({ hasText: 'Could not load incidents' })).toBeVisible();
        await capture(page, prefix + '-error');
        await page.unroute(listRequest);
        await page.getByRole('button', { name: 'Retry', exact: true }).click();
        await expect(row).toBeVisible();
      }
    }

    // The table overflows horizontally on a phone. Save a nonzero position
    // before a member opens the Map, then return through browser history.
    const row = incidentLink(page).first();
    const incidentID = await row.getAttribute('data-incident-id');
    await row.focus();
    const table = page.getByRole('region', { name: 'Incidents, scrollable table' });
    await table.evaluate(element => { element.scrollLeft = 24; });
    const position = await table.evaluate(element => element.scrollLeft);
    assert.ok(position > 0, 'The table must scroll for the history check');
    const listURL = page.url();
    await page.keyboard.press('Enter');
    const panel = page.getByRole('dialog', { name: 'Incident #' + incidentID });
    await expect(panel.getByRole('heading', { name: 'Incident #' + incidentID })).toBeFocused();
    const member = panel.getByRole('button', { name: /Alert #\d+/ }).first();
    const memberId = (await member.textContent()).trim().match(/#(\d+)$/)[1];
    await member.press('Enter');
    await expect(member).toHaveAttribute('aria-expanded', 'true');
    const detailURL = page.url();
    const link = panel.getByRole('link', { name: 'Show alert #' + memberId + ' on map' });
    const selected = page.waitForResponse(response => response.request().method() === 'GET' && response.url().endsWith('/alerts/' + memberId) && response.ok());
    await link.press('Enter');
    await expect(page).toHaveURL(/\/monitor\/map\?/);
    const query = new URL(page.url()).searchParams;
    assert.equal(query.get('alert'), memberId);
    assert.equal(query.get('incident'), incidentID);
    await selected;
    await expect(page.getByRole('heading', { level: 1, name: 'Map' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Alert #' + memberId, exact: true })).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(detailURL);
    await expect(panel.getByRole('heading', { name: 'Incident #' + incidentID })).toBeFocused();
    await expect(member).toHaveAttribute('aria-expanded', 'true');
    await page.goBack();
    await expect(member).toHaveAttribute('aria-expanded', 'false');
    await page.goBack();
    await expect(page).toHaveURL(listURL);
    await expect(row).toBeFocused();
    await expect.poll(() => table.evaluate(element => element.scrollLeft)).toBe(position);
    console.log(user + ' — map round trip, member selection, row focus and table position restored');

    // Long synthetic place names exercise wrapping on the same list and sheet.
    const labels = ['উত্তর পাহাড় সংরক্ষিত বনাঞ্চলের পূর্ব প্রান্তের পর্যবেক্ষণ কেন্দ্র',
      'उत्तरी पहाड़ी आरक्षित वन क्षेत्र का पूर्वी निगरानी केंद्र'];
    await page.route(url => listRequest(url) || url.origin === API && url.pathname.endsWith('/incidents/' + incidentID), async route => {
      if (route.request().method() !== 'GET') return route.continue();
      const response = await route.fetch();
      const data = await response.json();
      for (const record of data.items ?? [data]) {
        record.beat_names = [labels[0]];
        record.division_names = [labels[1]];
      }
      await route.fulfill({ response, json: data });
    });
    await page.reload();
    await page.addStyleTag({ content: 'html { font-size: 200%; }' });
    await expect(row).toContainText(labels.join(' · '));
    await open(page, row);
    const place = panel.getByText(labels.join(' · '), { exact: true });
    await expect(place).toBeVisible();
    assert.ok(await place.evaluate(element => {
      const box = element.getBoundingClientRect();
      return box.height >= parseFloat(getComputedStyle(element).lineHeight) * 2 && element.scrollWidth <= element.clientWidth;
    }), 'Long place names wrap in the sheet at 200% text size');
    await capture(page, user + '-phone-dark-large-text');
    await context.close();
  }

  const adminRows = new Map(results['state.admin'].map(row => [row.id, row.alert_count]));
  const officerRows = results['division.officer'];
  for (const row of officerRows) assert.ok(row.alert_count <= adminRows.get(row.id), 'Officer membership exceeds admin visibility');
  assert.ok(adminRows.size > officerRows.length || officerRows.some(row => row.alert_count < adminRows.get(row.id)),
    'Synthetic data must exercise narrower officer scope');
} finally {
  await browser.close();
}
