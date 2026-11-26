// Run after shell-walkthrough against the same synthetic workspace.
import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect as playwrightExpect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const expect = playwrightExpect.configure({ timeout: 25_000 });
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });

async function check(page, name) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']).analyze();
  assert.deepEqual(violations.filter(v => ['serious', 'critical'].includes(v.impact))
    .map(v => ({ id: v.id, targets: v.nodes.map(node => node.target) })), [], name);
  await page.screenshot({ path: join(SHOTS, 'map-' + name + '.png') });
  console.log(name + ' — zero serious or critical axe violations');
}

async function checkLargeLabels(page) {
  const labels = [
    'प्रदर्शन वन प्रबंधन क्षेत्र की सीमा और स्थानीय भागों का नाम',
    'নমুনা বন ব্যবস্থাপনা এলাকার সীমানা এবং স্থানীয় অংশের নাম'
  ];
  await page.route(url => url.origin === API && url.pathname.endsWith('/workspaces/demo'), async route => {
    if (route.request().method() !== 'GET') return route.continue();
    const response = await route.fetch();
    const map = await response.json();
    labels.forEach((label, i) => { map.levels[i].labels.en = label; });
    await route.fulfill({ response, json: map });
  });
  await page.reload();
  await expect(page.locator('#detection-picker')).toBeEnabled();
  await page.evaluate(() => {
    const style = getComputedStyle(document.documentElement);
    for (const size of ['label', 'body', 'subhead', 'title']) {
      for (const suffix of ['', '--line-height']) {
        const token = '--text-' + size + suffix;
        document.documentElement.style.setProperty(token, 2 * parseFloat(style.getPropertyValue(token)) + 'px');
      }
    }
  });
  await page.getByRole('button', { name: 'Layers and legend', exact: true }).press('Enter');
  const options = page.locator('.layer-options');
  for (const label of labels) {
    const checkbox = options.getByRole('checkbox', { name: new RegExp(label) });
    await expect(checkbox).toBeVisible();
    await checkbox.press('Space');
    await expect(checkbox).not.toBeChecked();
  }
  assert.ok(await options.evaluate(element => element.scrollWidth <= element.clientWidth));
  await check(page, 'large-labels-phone');
}

try {
  for (const [size, viewport] of [
    ['desktop', { width: 1440, height: 900 }],
    ['phone', { width: 390, height: 844 }]
  ]) {
    const context = await browser.newContext({ viewport, reducedMotion: 'reduce' });
    const page = await context.newPage();
    page.setDefaultTimeout(30_000);
    const tileHeaders = { 'access-control-allow-origin': WEB, 'content-type': 'application/vnd.mapbox-vector-tile' };
    let tiles = 'live';
    const tileRequests = new Map();
    await page.route(url => url.origin === API && /\/tiles\/(reference|detections)\//.test(url.pathname), route => {
      if (route.request().method() !== 'GET') return route.continue();
      const source = new URL(route.request().url()).pathname.includes('/tiles/reference/') ? 'reference' : 'detections';
      tileRequests.set(source, (tileRequests.get(source) ?? 0) + 1);
      if (tiles === 'error') return route.fulfill({ status: 503, headers: tileHeaders });
      if (tiles === 'empty' && source === 'detections') return route.fulfill({ status: 204, headers: tileHeaders });
      return route.continue();
    });

    await page.goto(WEB + '/monitor/map');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.locator('#username').fill('state.admin');
    await page.locator('#password').fill('demo');
    await page.locator('#kc-login').click();

    const picker = page.locator('#detection-picker');
    await expect(picker).toBeEnabled();
    const layersButton = page.getByRole('button', { name: 'Layers and legend', exact: true });
    const canvas = page.locator('.maplibregl-canvas');
    const alertRows = page.locator('.alert-panel .row-open');
    const detail = page.locator('fimra-side-panel.record-detail > section, hlm-sheet-content:has(#observation-heading)');
    const key = page.getByRole('region', { name: 'Map legend', exact: true });
    for (const theme of ['light', 'dark']) {
      tiles = 'live';
      await page.emulateMedia({ colorScheme: theme });
      await page.goto(WEB + '/monitor/map');
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await expect(picker).toBeEnabled();
      await expect(key.getByRole('list', { name: 'Detection age' })).toBeVisible();
      await check(page, `loaded-${size}-${theme}`);

      await layersButton.press('Enter');
      const options = page.locator('.layer-options');
      const legend = options.getByRole('list', { name: 'Visible layers' });
      await expect(legend).toContainText('Forest areas');
      await options.getByRole('checkbox', { name: 'Forest areas' }).press('Space');
      await expect(legend).not.toContainText('Forest areas');
      const incidentResponse = page.waitForResponse(r => r.request().method() === 'GET' && r.url().includes('/incidents?'));
      await options.getByRole('checkbox', { name: 'Reported incidents' }).press('Space');
      const response = await incidentResponse;
      assert.equal(response.status(), 200, 'The incident layer must load.');
      await expect(legend).toContainText('Reported incidents');
      await expect(options.getByRole('checkbox', { name: 'Reported incidents' })).toBeChecked();
      await page.keyboard.press('Escape');
      await expect(options).toBeHidden();
      await expect(layersButton).toBeFocused();
      await expect(key.getByText('Reported incident', { exact: true })).toBeVisible();

      await picker.press('Space');
      await expect(page.getByRole('option').first()).toBeVisible();
      await page.keyboard.press('Home');
      await page.keyboard.press('Enter');
      await expect(detail.getByRole('heading', { name: 'Detected observation' })).toBeVisible();
      const detection = new URL(page.url()).searchParams.get('detection');
      assert.match(detection, /^\d+$/);
      await expect(picker).toContainText('Detection #' + detection);

      // On a phone the modal sheet gives way to the selected map point.
      if (size === 'phone') {
        await detail.getByRole('link', { name: 'Show on map', exact: true }).click();
        await expect(detail).toBeHidden();
        await expect(canvas).toBeFocused();
      }
      // Zoom about the selection so the map stays centred there.
      await page.getByRole('button', { name: 'Zoom in', exact: true }).click();
      if (size === 'desktop') {
        await detail.getByRole('button', { name: 'Close map details' }).press('Escape');
        await expect(detail).toBeHidden();
        await expect(picker).toBeFocused();
      }
      const box = await canvas.boundingBox();
      assert.ok(box);
      await expect(async () => {
        await canvas.click({ position: { x: box.width / 2, y: box.height / 2 }, timeout: 1000 });
        await expect(detail).toBeVisible({ timeout: 500 });
      }).toPass({ timeout: 10_000 });
      await expect(picker).toContainText('Detection #' + detection);
      assert.equal(new URL(page.url()).searchParams.get('detection'), detection);
      await expect(detail.getByRole('heading', { name: /^Alert #/ })).toBeVisible();
      await expect(detail.getByRole('link', { name: /^Open incident #/ })).toBeVisible();
      if (size === 'desktop') await expect(alertRows.and(page.locator('[aria-current=true]'))).toBeVisible();
      await check(page, `selected-${size}-${theme}`);

      await page.goto(WEB + '/monitor/alerts');
      const row = alertRows.first();
      await expect(row).toBeVisible();
      const alert = await row.getAttribute('data-alert-id');
      const place = (await row.textContent()).split(' · Alert #')[0].trim();
      const heading = detail.getByRole('heading', { name: 'Alert #' + alert, exact: true });
      await row.press('Enter');
      await expect(heading).toBeFocused();
      await expect(detail.getByText(place, { exact: false }).first()).toBeVisible();
      await expect(detail.locator('fimra-timeline li').first()).toBeVisible();
      const location = await detail.getByText('Location', { exact: true }).locator('xpath=following-sibling::dd[1]').textContent();
      await expect(detail.locator('fimra-timeline')).not.toContainText(location);
      if (size === 'desktop') {
        assert.ok((await detail.boundingBox()).x > (await row.boundingBox()).x, 'Alert detail docks beside the list');
      }
      await check(page, `alert-detail-${size}-${theme}`);
      await page.reload();
      await expect(heading).toBeFocused();
      await detail.getByRole('link', { name: 'Show on map', exact: true }).click();
      await expect(page).toHaveURL(/\/monitor\/map\?.*#map$/);
      await expect(detail).toBeHidden();
      await expect(canvas).toBeFocused();
      assert.equal(new URL(page.url()).searchParams.get('alert'), alert);
      await page.goBack();
      await expect(heading).toBeFocused();
      await detail.getByRole('button', { name: 'Close alert details' }).press('Escape');
      await expect(row).toBeFocused();

      tiles = 'empty';
      await page.goto(WEB + '/monitor/map');
      await expect(page.getByText('No detections in view', { exact: true })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Retry', exact: true })).toHaveCount(0);
      await check(page, `empty-${size}-${theme}`);

      tiles = 'error';
      await page.reload();
      await expect(page.getByText('Some map data is missing', { exact: true })).toBeVisible();
      const status = page.getByRole('region', { name: 'Detection map' }).locator('fimra-state-region');
      await expect(status).toContainText('Reference tiles');
      await expect(status).toContainText('Fire detection tiles');
      await expect(page.getByText('No detections in view', { exact: true })).toHaveCount(0);
      if (size === 'desktop') {
        await expect(alertRows.first()).toBeVisible();
        await alertRows.first().click();
        await expect(detail.getByRole('heading', { name: /^Alert #/ })).toBeVisible();
        await detail.getByRole('button', { name: 'Close map details' }).click();
      } else {
        await page.getByRole('button', { name: 'Menu', exact: true }).click();
        await page.getByRole('dialog', { name: 'Destinations' }).getByRole('link', { name: 'Alerts', exact: true }).click();
        await expect(alertRows.first()).toBeVisible();
        await page.goBack();
        await expect(status).toContainText('Fire detection tiles');
      }
      await check(page, `tile-error-${size}-${theme}`);

      const failed = new Map(tileRequests);
      tiles = 'live';
      await status.getByRole('button', { name: 'Retry' }).click();
      for (const source of ['reference', 'detections']) {
        await expect.poll(() => tileRequests.get(source) ?? 0).toBeGreaterThan(failed.get(source) ?? 0);
      }
      await expect(picker).toBeEnabled();
      await expect(status.getByRole('button', { name: 'Retry' })).toHaveCount(0);
    }
    if (size === 'phone') await checkLargeLabels(page);
    await context.close();
  }
} finally {
  await browser.close();
}
