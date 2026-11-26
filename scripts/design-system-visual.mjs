// Run in the Playwright container against a development stack with demo detections.
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { chromium } from 'playwright';

const { WEB, SHOTS } = process.env;
assert.ok(WEB && SHOTS, 'Set WEB to the local web address and SHOTS to a folder outside the repository.');
await mkdir(SHOTS, { recursive: true });
const browser = await chromium.launch({ args: ['--enable-unsafe-swiftshader'] });
try {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 }, colorScheme: 'light', reducedMotion: 'reduce'
  });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const ready = theme => page.waitForFunction(mode => {
    if (document.documentElement.dataset.theme !== mode || !document.fonts.check('14px "Inter Variable"')) return false;
    const style = getComputedStyle(document.body);
    const canvas = document.createElement('canvas').getContext('2d');
    canvas.fillStyle = style.backgroundColor;
    return canvas.fillStyle === style.getPropertyValue('--color-background').trim();
  }, theme);
  const save = async (name, fullPage = false) => {
    await page.screenshot({ path: join(SHOTS, name + '.png'), fullPage, animations: 'disabled' });
    console.log('Saved', name);
  };

  await page.goto(new URL('/design-system?preview', WEB).href);
  await page.getByRole('heading', { name: 'Design system', exact: true }).waitFor();
  const backgrounds = new Set();
  for (const theme of ['light', 'dark']) {
    if (theme === 'dark') await page.getByRole('button', { name: 'Use dark theme' }).click();
    await ready(theme);
    assert.match(await page.locator('body').evaluate(e => getComputedStyle(e).fontFamily), /Inter Variable/);
    backgrounds.add(await page.locator('body').evaluate(e => getComputedStyle(e).backgroundColor));
    await save('guide-' + theme, true);
    for (const [trigger, title] of [['Open dialog', 'Close this alert?'], ['Open sheet', 'Alert details']]) {
      await page.getByRole('button', { name: trigger, exact: true }).click();
      await page.getByRole('heading', { name: title }).waitFor();
      await page.keyboard.press('Escape');
    }
    await page.getByRole('button', { name: 'Open menu' }).click();
    await page.getByRole('menuitem', { name: 'View details' }).click();
    const notice = page.getByText('Details requested', { exact: true });
    await notice.waitFor();
    await notice.waitFor({ state: 'hidden' });
  }
  assert.equal(backgrounds.size, 2, 'The two palettes should have different surfaces.');

  await page.goto(WEB);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.locator('#username').fill('division.officer');
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
  await page.locator('.maplibregl-canvas').waitFor();
  await page.locator('.alert-table .row-open').first().waitFor();
  for (const theme of ['dark', 'light']) {
    const toggle = page.getByRole('button', { name: 'Dark theme' });
    if ((await toggle.getAttribute('aria-pressed') === 'true') !== (theme === 'dark')) await toggle.click();
    await ready(theme);
    await page.waitForLoadState('networkidle');
    await save('map-' + theme);
  }
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}
