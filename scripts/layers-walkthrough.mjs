// Run with shell-walkthrough.sh against an isolated, seeded stack with a geo worker.
import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect } from 'playwright/test';

const { WEB, SHOTS } = process.env;
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
const boundary = [[86.25, 16.35], [86.27, 16.35], [86.27, 16.37], [86.25, 16.37], [86.25, 16.35]];

function file(name, geometry) {
  return {
    name: name + '.geojson', mimeType: 'application/geo+json',
    buffer: Buffer.from(JSON.stringify({
      type: 'FeatureCollection',
      features: [{ type: 'Feature', properties: { code: 'demo-1' }, geometry }]
    }))
  };
}

async function capture(page, label) {
  const { violations } = await new AxeBuilder({ page }).include('main').include('.cdk-overlay-container').analyze();
  assert.deepEqual(violations.filter(v => v.impact === 'critical' || v.impact === 'serious')
    .map(v => ({ id: v.id, nodes: v.nodes.map(node => ({ target: node.target, summary: node.failureSummary })) })), [], label);
  await page.screenshot({ path: join(SHOTS, label + '.png') });
  console.log(label + ' — zero serious or critical axe violations in Layers and its dialogs');
}

try {
  for (const [size, viewport] of [
    ['desktop', { width: 1440, height: 900 }],
    ['phone', { width: 390, height: 844 }]
  ]) {
    for (const theme of ['light', 'dark']) {
      const label = size + '-' + theme;
      const context = await browser.newContext({ viewport, colorScheme: theme, reducedMotion: 'reduce' });
      const page = await context.newPage();
      page.on('pageerror', error => console.error(error));
      page.setDefaultTimeout(45_000);
      await page.goto(WEB + '/settings/layers');
      await page.getByRole('button', { name: 'Sign in', exact: true }).click();
      await page.locator('#username').fill('state.admin');
      await page.locator('#password').fill('demo');
      await page.locator('#kc-login').click();
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await expect(page.getByRole('heading', { level: 1, name: 'Layers', exact: true })).toBeVisible();
      const row = page.locator('fimra-data-table').getByRole('button', { name: / for Forest$/ });
      await expect(row).toBeVisible();
      await capture(page, label + '-catalog');
      await row.click();
      const review = page.locator(size === 'phone' ? 'hlm-sheet-content' : 'fimra-side-panel');
      const current = await review.locator('dd').first().innerText();
      const currentVersion = Number(current.match(/^Version (\d+)$/)?.[1]);
      assert.ok(currentVersion >= 1, 'Expected an existing published version');

      const startUpload = review.getByRole('button', { name: /^(Upload new version|Replace file)$/ });
      await startUpload.click();
      await review.getByLabel('Layer file', { exact: true }).setInputFiles(file('broken-' + label, {
        type: 'Point', coordinates: boundary[0]
      }));
      const upload = page.waitForResponse(response => response.request().method() === 'POST' &&
        response.url().endsWith('/layer-imports'));
      await review.getByRole('button', { name: /^(Upload for review|Upload replacement)$/ }).click();
      assert.equal((await upload).status(), 202);
      await page.reload();
      await page.getByRole('button', { name: /^(View progress|Fix problems) for Forest$/ }).click();
      const blocker = review.getByRole('link', { name: /area drawn as a polygon/ });
      await expect(blocker).toBeVisible({ timeout: 60_000 });
      await blocker.click();
      const check = review.locator(await blocker.getAttribute('href'));
      await expect(check).toBeFocused();
      await expect(check).toContainText('Export this layer as polygons.');
      await expect(review.getByRole('button', { name: 'Publish layer', exact: true })).toHaveCount(0);
      await capture(page, label + '-repair');

      await review.getByRole('button', { name: 'Replace file', exact: true }).click();
      await expect(review.getByLabel('Layer file', { exact: true })).toBeFocused();
      await review.getByLabel('Layer file', { exact: true }).setInputFiles(file('corrected-' + label, {
        type: 'Polygon', coordinates: [boundary]
      }));
      await review.getByLabel('Code field').fill('code');
      await review.getByRole('button', { name: 'Upload replacement', exact: true }).click();
      await expect(review.getByText('Checks passed', { exact: true })).toBeVisible({ timeout: 60_000 });
      const reviewedVersion = review.locator('#layer-review');
      await expect(reviewedVersion).toHaveText(/^\s*Version \d+\s*$/);
      const draftLabel = (await reviewedVersion.innerText()).trim();
      assert.ok(Number(draftLabel.split(' ')[1]) >= currentVersion + 2);
      const geometry = page.waitForResponse(response => response.request().method() === 'GET' &&
        /layer-imports\/[^/]+\/map$/.test(response.url()));
      await review.getByRole('button', { name: 'Run preview', exact: true }).click();
      assert.equal((await geometry).status(), 200);
      await expect(review.getByLabel(/Draft map\. Use arrow keys/)).toBeInViewport({ ratio: 0.75 });
      await expect(review.getByText(/detections checked over 30 days/)).toBeVisible();
      await capture(page, label + '-preview');

      const publish = review.getByRole('button', { name: 'Publish layer', exact: true });
      await publish.click();
      const dialog = page.getByRole('alertdialog');
      await expect(dialog).toContainText('Demo State · Forest eligibility');
      await expect(dialog).toContainText('replace Forest version ' + currentVersion + ' in this scope');
      await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
      await capture(page, label + '-confirm');
      await page.keyboard.press('Escape');
      await expect(publish).toBeFocused();
      await publish.click();
      await dialog.getByRole('button', { name: 'Confirm and publish' }).click();
      await expect(review.getByRole('heading', { name: draftLabel, exact: true })).toBeVisible();
      await expect(review.getByText(/detections? checked\. \d+ new alerts?/)).toBeVisible({ timeout: 60_000 });
      await expect(review.getByText('Forest version ' + currentVersion, { exact: true })).toBeVisible();
      await capture(page, label + '-published');

      await page.reload();
      await row.click();
      await expect(review.getByText('Forest version ' + currentVersion, { exact: true })).toBeVisible();
      await expect(review.getByText('Demo State · Forest eligibility', { exact: true })).toBeVisible();
      await context.close();
    }
  }
} finally {
  await browser.close();
}
