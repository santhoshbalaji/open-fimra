import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect as playwrightExpect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
const desktop = { width: 1440, height: 900 };
const phone = { width: 390, height: 844 };
const common = ['Map', 'Alerts', 'Incidents', 'Field reports', 'Situation report', 'Dashboard', 'GIS link', 'Field phones'];
const expect = playwrightExpect.configure({ timeout: 25_000 });

async function signIn(page, username, path = '/') {
  page.setDefaultTimeout(60_000);
  await page.goto(WEB + path);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.locator('#username').fill(username);
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
}

async function axe(page, label, selectors) {
  const builder = new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']);
  for (const selector of selectors) builder.include(selector);
  const { violations } = await builder.analyze();
  const serious = violations.filter(item => item.impact === 'serious' || item.impact === 'critical');
  assert.deepEqual(serious.map(v => ({ id: v.id, elements: v.nodes.map(node => ({ target: node.target, summary: node.failureSummary })) })), [], label);
  console.log(label + ' — zero serious or critical axe violations');
}

async function openDrawer(page) {
  await page.getByRole('button', { name: 'Menu', exact: true }).click();
  const drawer = page.getByRole('dialog', { name: 'Destinations' });
  await expect(drawer.getByRole('heading', { name: 'Destinations' })).toBeVisible();
  return drawer;
}

try {
  for (const [username, extra, start, heading] of [
    ['state.admin', ['Layers', 'Form templates', 'Setup'], '/', 'Map'],
    ['division.officer', ['Form templates'], '/reports/field?decision=approved', 'Field reports'],
    ['beat.guard', [], '/monitor/map?unitPath=ds.c2', 'Map'],
    ['no.role', [], '/settings/layers', 'Restricted access']
  ]) {
    console.log('Checking ' + username);
    const context = await browser.newContext({ viewport: desktop, colorScheme: 'light', reducedMotion: 'reduce' });
    const page = await context.newPage();
    await signIn(page, username, start);
    await expect(page.getByRole('main').getByRole('heading', { level: 1, name: heading })).toBeFocused();
    await expect(page).toHaveTitle(heading + ' · Open FIMRA');
    if (username === 'no.role') {
      await expect(page.getByText('Ask your workspace administrator', { exact: false })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Switch account' })).toBeVisible();
      await expect(page.locator('fimra-demo-map')).toHaveCount(0);
      for (const [size, viewport] of [['desktop', desktop], ['phone', phone]]) {
        await page.setViewportSize(viewport);
        for (const theme of ['light', 'dark']) {
          await page.emulateMedia({ colorScheme: theme });
          await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
          await axe(page, `Restricted access ${size} ${theme}`, ['main']);
          await page.screenshot({ path: join(SHOTS, `restricted-${size}-${theme}.png`) });
        }
      }
      await page.getByRole('button', { name: 'Switch account' }).click();
      await expect(page.locator('#username')).toBeVisible();
    } else {
      const nav = page.getByRole('navigation', { name: 'Destinations' });
      await expect(nav.getByRole('link')).toHaveCount(common.length + extra.length);
      for (const label of [...common, ...extra]) await expect(nav.getByRole('link', { name: label, exact: true })).toBeVisible();
      await expect(page.locator('.workspace-scope')).not.toBeEmpty();
      const account = page.locator('.account-trigger');
      const visibleName = await account.locator('.user-name').textContent();
      const initials = await account.locator('.account-initials').textContent();
      await expect(account).toHaveAccessibleName(`Account for ${visibleName.trim()}, ${initials.trim()}`);
      if (username !== 'state.admin') {
        await page.goto(WEB + '/settings/layers');
        await expect(page.getByRole('heading', { name: 'Restricted access' })).toBeVisible();
        await expect(page.locator('fimra-layers')).toHaveCount(0);
      }
      if (username === 'beat.guard') {
        await page.keyboard.press('Control+k');
        const dialog = page.getByRole('dialog', { name: 'Search destinations' });
        await expect(dialog).toBeVisible();
        await expect(dialog.getByRole('button', { name: /Form templates|Layers|Setup/ })).toHaveCount(0);
        await dialog.getByRole('textbox').fill('reports');
        await page.keyboard.press('ArrowDown');
        await page.keyboard.press('Enter');
        await expect(page).toHaveURL(/\/reports\/situation/);
        await expect(dialog).toBeHidden();
        await expect(page.getByRole('heading', { level: 1, name: 'Situation report' })).toBeFocused();
      }
    }
    await context.close();
  }

  const context = await browser.newContext({ viewport: desktop, colorScheme: 'light', reducedMotion: 'reduce' });
  const page = await context.newPage();
  await signIn(page, 'state.admin');
  await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeVisible();
  await expect(page.locator('.maplibregl-canvas')).toBeVisible();
  const skip = page.getByRole('link', { name: 'Skip to content' });
  await skip.focus();
  const originalURL = page.url();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main')).toBeFocused();
  await expect(page).toHaveURL(originalURL);
  const alerts = page.getByRole('navigation', { name: 'Destinations' }).getByRole('link', { name: 'Alerts', exact: true });
  await alerts.press('Enter');
  await expect(page.getByRole('heading', { level: 1, name: 'Alerts' })).toBeFocused();
  await expect(page).toHaveTitle('Alerts · Open FIMRA');
  await expect(page.getByText('Alerts page opened.', { exact: true })).toHaveAttribute('role', 'status');
  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Map', level: 1 })).toBeFocused();
  for (const [size, viewport] of [['desktop', desktop], ['phone', phone]]) {
    await page.setViewportSize(viewport);
    for (const theme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await expect(page.locator('.account-trigger')).toBeVisible();
      await axe(page, `Shell ${size} ${theme}`, ['.topbar', '.desktop-destinations', '.workspace-controls']);
      await page.screenshot({ path: join(SHOTS, `shell-${size}-${theme}.png`) });
      if (size === 'phone') {
        const account = page.locator('.account-trigger');
        await expect(account.locator('.account-initials')).toBeVisible();
        await expect(account.locator('.user-name')).toBeHidden();
        await openDrawer(page);
        await axe(page, `Drawer ${theme}`, ['.shell-drawer']);
        await page.screenshot({ path: join(SHOTS, `drawer-phone-${theme}.png`) });
        await page.keyboard.press('Escape');
        await expect(page.getByRole('button', { name: 'Menu', exact: true })).toBeFocused();
      }
      const search = page.getByRole('button', { name: /^Search\b/ });
      await search.click();
      const palette = page.getByRole('dialog', { name: 'Search destinations' });
      await expect(palette.getByRole('textbox')).toBeFocused();
      await axe(page, `Palette ${size} ${theme}`, ['.shell-palette']);
      await page.keyboard.press('Escape');
      await expect(palette).toBeHidden();
      await expect(search).toBeFocused();
    }
  }

  console.log('Checking map detection history');
  const detailHeading = page.getByRole('heading', { level: 2, name: /^(Alert|Detection) #\d+$/ });
  await page.setViewportSize(desktop);
  await expect(page.locator('#detection-picker')).toBeEnabled();
  await page.locator('#detection-picker').press('Space');
  await page.getByRole('option').first().click();
  await expect(page).toHaveURL(/detection=/);
  await expect(detailHeading).toBeFocused();
  await page.reload();
  await expect(detailHeading).toBeFocused();
  await page.goBack();
  await expect(detailHeading).toBeHidden();

  // Force small API pages so the real cursor and detail endpoints can be tested
  // with the small synthetic dataset used by the development stack.
  await page.route(url => url.origin === API && url.pathname.endsWith('/alerts') && url.searchParams.has('queue'), async route => {
    const url = new URL(route.request().url());
    url.searchParams.set('limit', '2');
    await route.continue({ url: url.href });
  });
  console.log('Checking alert list history');
  await page.setViewportSize({ width: 390, height: 300 });
  const drawer = await openDrawer(page);
  await drawer.getByRole('link', { name: 'Alerts', exact: true }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Alerts' })).toBeFocused();
  await page.getByRole('button', { name: 'Filters', exact: true }).click();
  await page.locator('#hours').evaluate(input => {
    input.value = '144';
    input.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await expect(page).toHaveURL(/hours=144/);
  await page.getByRole('button', { name: 'Filters', exact: true }).click();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page).toHaveURL(/cursor=/);
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  const listURL = page.url();
  const list = page.locator('.alert-panel');
  const row = list.locator('.row-open').last();
  await expect(row).toBeVisible();
  await list.evaluate(element => { element.scrollTop = 36; });
  // Use the position at selection, after the row has been brought into view.
  await row.scrollIntoViewIfNeeded();
  const offset = await list.evaluate(element => element.scrollTop);
  assert.ok(offset > 0, 'The list must scroll to exercise position restoration.');
  const selection = (await row.innerText()).trim();
  assert.match(selection, /^.+ · Alert #\d+$/);
  await row.click();
  await expect(page).toHaveURL(/alert=/);
  await expect(detailHeading).toBeFocused();
  await page.reload();
  await expect(detailHeading).toBeFocused();
  assert.match(page.url(), /hours=144/);
  assert.match(page.url(), /cursor=/);
  await page.goBack();
  await expect(page).toHaveURL(listURL);
  await expect(list.getByRole('button', { name: selection, exact: true })).toBeVisible();
  await expect.poll(() => list.evaluate(element => element.scrollTop)).toBe(offset);
  console.log('The alert selection, filter, cursor and list position survived reload and Back.');
  await page.locator('.account-trigger').click();
  await page.getByRole('menuitem', { name: 'Sign out' }).click();
  await expect(page.getByRole('heading', { name: 'You signed out' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
} finally {
  await browser.close();
}
