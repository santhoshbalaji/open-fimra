import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect as playwrightExpect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const expect = playwrightExpect.configure({ timeout: 25_000 });
const browser = await chromium.launch();
const viewports = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };

async function capture(page, name) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']).analyze();
  const serious = violations.filter(item => item.impact === 'serious' || item.impact === 'critical');
  assert.deepEqual(serious.map(item => ({ id: item.id, elements: item.nodes.map(node => node.target) })), [], name);
  await page.screenshot({ path: join(SHOTS, name + '.png') });
  console.log(name + ' — zero serious or critical axe violations');
}

try {
  const context = await browser.newContext({ viewport: viewports.desktop, reducedMotion: 'reduce' });
  const page = await context.newPage();
  await page.goto(WEB + '/reports/situation?period=hours&reportHours=168');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.locator('#username').fill('state.admin');
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
  const heading = page.getByRole('heading', { level: 1, name: 'Situation report' });
  const show = page.getByRole('button', { name: 'Show report', exact: true });
  const edit = page.getByRole('button', { name: 'Edit scope' });
  const print = page.getByRole('button', { name: 'Print', exact: true });
  const pdf = page.getByRole('button', { name: /PDF/ });
  const table = page.getByRole('table', { name: 'Markers shown on the map' });

  await expect(show).toBeEnabled();
  await capture(page, 'desktop-light-scope-form');
  const reportURL = url => url.origin === API && url.pathname.endsWith('/situation-report') &&
    url.searchParams.get('snapshot') === '1';
  let report;
  await page.route(reportURL, async route => {
    const response = await route.fetch();
    assert.ok(response.ok(), 'Show report should return a saved view');
    report = await response.json();
    await route.fulfill({ response });
  });
  await show.press('Enter');
  await expect(heading).toBeFocused();
  await page.unroute(reportURL);
  await expect(heading).toHaveCount(1);
  await expect(page.locator('form')).toHaveCount(0);
  await expect(page.getByText('India Standard Time', { exact: true })).toBeVisible();
  await expect(page.getByText(/Waiting for a report: \d+/)).toBeVisible();
  await expect(page.getByText(/Awaiting review: \d+/)).toBeVisible();
  await expect(table).toBeVisible();
  assert.equal(await page.locator('.report-map .map-alert').count(), report.map.alertsShown);
  assert.ok(report.map.alertsShown > 0, 'The synthetic map needs alert markers');
  const rows = await table.getByRole('row').all();
  assert.equal(rows.length, 6);
  assert.equal((await Promise.all(rows.slice(1, 5).map(async row =>
    Number(await row.getByRole('cell').last().textContent())))).reduce((sum, count) => sum + count), report.map.alertsShown);

  const request = url => url.origin === API && url.pathname.endsWith('/situation-reports');
  let releaseFailure;
  const fail = new Promise(resolve => { releaseFailure = resolve; });
  await page.route(request, async route => {
    assert.deepEqual(route.request().postDataJSON(), { snapshotId: report.snapshotId });
    await fail;
    await route.fulfill({ status: 503, json: { error: 'The PDF could not be prepared. Try again.' },
      headers: { 'Access-Control-Allow-Origin': WEB } });
  });
  await expect(pdf).toHaveText('Prepare PDF');
  await pdf.press('Enter');
  await expect(pdf).toHaveText('Preparing PDF…');
  await expect(pdf).toBeFocused();
  await capture(page, 'desktop-light-pdf-pending');
  releaseFailure();
  await expect(pdf).toHaveText('Retry PDF');
  await expect(page.getByRole('alert')).toContainText('The PDF could not be prepared');
  await capture(page, 'desktop-light-pdf-failed');
  await page.unroute(request);
  await pdf.press('Enter');
  await expect(pdf).toHaveText(/Download PDF/);
  const downloaded = page.waitForEvent('download');
  await pdf.press('Enter');
  assert.match((await downloaded).suggestedFilename(), /^open-fimra-.*\.pdf$/);
  await expect(pdf).toHaveText(/Download PDF/);

  for (const [size, viewport] of Object.entries(viewports)) {
    await page.setViewportSize(viewport);
    for (const theme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme: theme, media: 'screen' });
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      assert.ok(await page.locator('main').evaluate(main => main.scrollWidth <= main.clientWidth),
        'The report must fit the viewport');
      await capture(page, `${size}-${theme}-pdf-ready`);
      await page.emulateMedia({ media: 'print', colorScheme: theme });
      await expect(edit).toBeHidden();
      await expect(pdf).toBeHidden();
      await expect(print).toBeHidden();
      await expect(page.locator('.topbar, .desktop-destinations').filter({ visible: true })).toHaveCount(0);
      await expect(page.getByText('Closed alert or incident (slash)', { exact: true })).toBeVisible();
      await expect(table).toBeVisible();
      await capture(page, `${size}-${theme}-print`);
    }
  }

  await page.pdf({ path: join(SHOTS, 'browser-print.pdf'), preferCSSPageSize: true });
  await page.emulateMedia({ media: 'screen' });
  await edit.press('Enter');
  await expect(page.locator('#report-unit')).toBeFocused();
  await expect(page.getByRole('spinbutton', { name: 'Hours before now' })).toHaveValue('168');
} finally {
  await browser.close();
}
