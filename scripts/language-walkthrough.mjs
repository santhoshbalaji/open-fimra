import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
const desktop = { width: 1366, height: 768 };
const phone = { width: 360, height: 800 };

async function signIn(page, label) {
  await page.getByRole('button', { name: label, exact: true }).click();
  await page.locator('#username').fill('state.admin');
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
}

async function checkPage(page, slug, size) {
  await page.evaluate(() => document.fonts.ready);
  const overflow = await page.evaluate(() => Math.max(
    document.documentElement.scrollWidth - window.innerWidth,
    document.querySelector('.topbar').scrollWidth - window.innerWidth,
  ));
  assert.ok(overflow <= 1, `${slug} ${size} overflows the viewport by ${overflow}px`);
  const { violations } = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']).analyze();
  assert.deepEqual(violations.filter(item => ['serious', 'critical'].includes(item.impact))
    .map(item => [item.id, item.nodes.map(node => node.target)]), [], `${slug} ${size}`);
  await page.screenshot({ path: join(SHOTS, `${slug}-${size}.png`) });
  console.log(`${slug} ${size}: no viewport overflow or serious/critical axe violations`);
}

try {
  const context = await browser.newContext({ viewport: desktop, locale: 'en-IN', reducedMotion: 'reduce' });
  const page = await context.newPage();
  await page.goto(WEB);
  const profile = page.waitForRequest(request => request.url().startsWith(API) && request.url().endsWith('/me'));
  await signIn(page, 'Sign in');
  await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeVisible();
  const request = await profile;
  const headers = await request.allHeaders();
  const workspace = new URL(page.url()).searchParams.get('workspace');
  const starter = await context.request.post(`${API}/api/v1/workspaces/${workspace}/starter-pack`, {
    headers: { authorization: headers.authorization },
  });
  assert.ok(starter.ok(), 'Enable the starter forms for this synthetic workspace');

  await page.locator('.account-trigger').click();
  await page.locator('[lang="hi"]').click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'hi');
  await expect(page.getByRole('heading', { name: 'नक्शा', level: 1 })).toBeVisible();
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'hi');

  const screens = [
    ['map', '/monitor/map', 'नक्शा'],
    ['alerts', '/monitor/alerts', 'अलर्ट'],
    ['incidents', '/monitor/incidents', 'घटनाएँ'],
    ['field-reports', '/reports/field', 'फ़ील्ड रिपोर्टें'],
    ['situation', '/reports/situation', 'स्थिति रिपोर्ट'],
    ['dashboard', '/dashboard', 'डैशबोर्ड'],
    ['forms', '/settings/forms', 'फ़ॉर्म के नमूने'],
    ['layers', '/settings/layers', 'परतें'],
    ['gis', '/settings/gis', 'जीआईएस लिंक'],
    ['devices', '/settings/devices', 'फ़ील्ड फ़ोन'],
  ];
  for (const [slug, path, title] of screens) {
    await page.goto(WEB + path);
    await expect(page.getByRole('heading', { name: title, level: 1, exact: true })).toBeVisible();
    if (slug === 'map') await expect(page.locator('.maplibregl-canvas')).toBeVisible();
    if (slug === 'situation') {
      await expect(page.locator('#report-alerts-title')).toBeVisible();
      const period = page.locator('.report header dt').filter({ hasText: 'अवधि' }).locator('..').locator('dd');
      await expect(period).toContainText(/\d{1,2}\s+[\u0900-\u097f]+/u);
    }
    for (const [name, viewport] of [['desktop', desktop], ['phone', phone]]) {
      await page.setViewportSize(viewport);
      await checkPage(page, slug, name);
    }
  }
  await page.goto(WEB + '/settings/forms');
  const firstReport = page.getByRole('article', { name: 'पहली रिपोर्ट', exact: true });
  await firstReport.getByRole('button', { name: /संस्करण देखें/ }).click();
  await page.getByRole('button', { name: 'झलक', exact: true }).click();
  await expect(page.getByRole('combobox', { name: 'भाषा', exact: true })).toHaveValue('hi');
  await expect(page.getByText('दल के मौके पर पहुँचने का समय', { exact: false }).first()).toBeVisible();
  for (const [name, viewport] of [['desktop', desktop], ['phone', phone]]) {
    await page.setViewportSize(viewport);
    await checkPage(page, 'starter-first-report', name);
  }
  await context.close();

  const browserDefault = await browser.newContext({ viewport: phone, locale: 'hi-IN' });
  const hindiSignIn = await browserDefault.newPage();
  await hindiSignIn.goto(WEB);
  await expect(hindiSignIn.getByRole('button', { name: 'साइन इन करें', exact: true })).toBeVisible();
  await expect(hindiSignIn.locator('html')).toHaveAttribute('lang', 'hi');
  await browserDefault.close();
  console.log('Hindi choice persisted after reload and the browser default selected Hindi.');
} finally {
  await browser.close();
}
