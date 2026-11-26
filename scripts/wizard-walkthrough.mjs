// Headless walkthrough of the first-run wizard, run by `make wizard-walkthrough`
// in the Playwright container. It signs in as the demo administrator on an
// empty install, completes every step with the synthetic demo files, and saves
// a screenshot of each screen to SHOTS (outside the repository).
//
// Environment: WEB (web app address), MAIL (mail catcher address), SEED (the
// seed/demo-state folder), SHOTS (output folder), SETUP_CODE and FIRMS_KEY
// (the fake FIRMS server's key).
import assert from 'node:assert/strict';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect } from 'playwright/test';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const { WEB, MAIL, SEED, SHOTS, SETUP_CODE, FIRMS_KEY } = process.env;
const desktop = { width: 1440, height: 900 };
const phone = { width: 390, height: 844 };
mkdirSync(SHOTS, { recursive: true });
let shot = 0;
const save = async (page, name) => {
  shot += 1;
  const file = join(SHOTS, `${String(shot).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: true });
  console.log('Saved', file);
};

// Correct the deliberate defects in the demo beats, as an officer would after
// reading the review list: two missing codes and the B17 bulge into B20.
function repairedBeats() {
  const collection = JSON.parse(readFileSync(join(SEED, 'beats.geojson'), 'utf8'));
  const rings = {};
  collection.features.forEach((feature, i) => {
    const p = feature.properties;
    if (!p.code) p.code = `B${String(i + 1).padStart(2, '0')}`;
    if (!p.parent_code) p.parent_code = `R${Math.floor(i / 3) + 1}`;
    rings[p.code] = feature.geometry.coordinates[0];
  });
  rings.B17.forEach((a, i) => {
    for (const b of rings.B20) {
      if (a[1] === b[1] && a[0] - b[0] > 0.00399 && a[0] - b[0] < 0.00401) rings.B17[i] = b;
    }
  });
  const file = '/tmp/beats-corrected.geojson';
  writeFileSync(file, JSON.stringify(collection));
  return file;
}

async function signIn(browser, username, viewport = desktop) {
  const context = await browser.newContext({ viewport, colorScheme: 'light', reducedMotion: 'reduce' });
  const page = await context.newPage();
  await page.goto(WEB);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.locator('#username').fill(username);
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
  return page;
}

async function captureStep(page, name, progress, nextAction) {
  shot += 1;
  for (const [size, viewport] of [['desktop', desktop], ['phone', phone]]) {
    await page.setViewportSize(viewport);
    for (const theme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme: theme });
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      await page.locator('fimra-setup-wizard, fimra-bootstrap').evaluate(element => { element.scrollTop = 0; });
      if (size === 'phone') {
        await expect(page.getByText(progress, { exact: true })).toBeInViewport();
        await expect(nextAction).toBeInViewport();
      }
      const { violations } = await new AxeBuilder({ page }).include('main')
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice']).analyze();
      const serious = violations.filter(item => item.impact === 'serious' || item.impact === 'critical');
      const label = `${name} ${size} ${theme}`;
      assert.deepEqual(serious.map(item => ({ id: item.id, elements: item.nodes.map(node => ({ target: node.target, summary: node.failureSummary })) })), [], label);
      await page.screenshot({ path: join(SHOTS, `${String(shot).padStart(2, '0')}-${name}-${size}-${theme}.png`) });
      console.log(label, '— zero serious or critical axe violations');
    }
  }
  await page.emulateMedia({ colorScheme: 'light' });
}

const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
try {
  const page = await signIn(browser, 'state.admin', phone);
  const button = name => page.getByRole('button', { name, exact: true });
  const continueButton = button('Continue');
  const step = async name => {
    const heading = page.locator('#step-heading');
    await expect(heading).toHaveText(name, { timeout: 30_000 });
    await expect(heading).toBeFocused();
    await expect(heading).toBeInViewport();
  };
  await page.getByRole('heading', { name: 'Set up Open FIMRA' }).waitFor();
  await page.getByLabel('Setup code').fill(SETUP_CODE);
  await page.getByLabel('Workspace name').fill('Demo State');
  await page.getByLabel('State code').fill('DS');
  await captureStep(page, 'bootstrap', 'Step 1 of 7: Workspace', button('Start setup'));
  await button('Start setup').click();

  await step('Hierarchy');
  const progress = page.locator('.setup-progress summary');
  await progress.focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('.setup-progress').getByRole('button')).toHaveCount(7);
  await expect(page.locator('.setup-progress').getByRole('button', { name: 'Workspace Done' })).toBeVisible();
  await page.keyboard.press('Enter');
  await page.getByLabel('Hindi label for level 5').fill('बीट (वन)');
  await captureStep(page, 'hierarchy-preset', 'Step 2 of 7: Hierarchy', button('Save hierarchy'));
  await button('Save hierarchy').click();

  await step('Layers');
  await captureStep(page, 'layers-empty', 'Step 3 of 7: Layers', page.getByRole('link', { name: 'Go to State' }));
  const layers = [
    ['level:0', 'state'], ['level:1', 'circles'], ['level:2', 'divisions'], ['level:3', 'ranges'],
    ['level:4', 'beats'], ['forest', 'forest'], ['exclusion', 'exclusions']
  ];
  for (const [key, stem] of layers) {
    const row = page.locator('.layer-checklist > li').filter({ has: page.locator(`[id="file-${key}"]`) });
    const upload = async file => {
      await page.locator(`[id="file-${key}"]`).setInputFiles(file);
      await row.getByRole('button', { name: /^Upload/ }).click();
    };
    await upload(join(SEED, `${stem}.geojson`));
    if (stem === 'beats') {
      await row.getByText('Correct these problems', { exact: false }).waitFor({ timeout: 60_000 });
      await page.reload();
      await step('Layers');
      const summary = page.getByRole('group', { name: 'Layer problems to fix' });
      const blocker = summary.getByRole('link').first();
      await expect(blocker).toBeVisible({ timeout: 30_000 });
      await captureStep(page, 'beats-blocking-problems', 'Step 3 of 7: Layers', page.getByRole('link', { name: /^Go to Beat/ }));
      await blocker.focus();
      await page.keyboard.press('Enter');
      await expect(row.getByRole('heading')).toBeFocused();
      await expect(row.getByRole('button', { name: 'Upload replacement' })).toBeInViewport();
      await page.screenshot({ path: join(SHOTS, 'beats-repair-phone-light.png') });
      await upload(repairedBeats());
    }
    await row.getByRole('button', { name: 'Publish' }).waitFor({ timeout: 60_000 });
    await row.getByRole('button', { name: 'Publish' }).click();
    await page.locator('.layer-checklist > li[data-published]').filter({ has: page.locator(`[id="file-${key}"]`) })
      .waitFor({ timeout: 30_000 });
  }
  await captureStep(page, 'layers-published', 'Step 3 of 7: Layers', continueButton);
  await continueButton.click();

  await step('NASA FIRMS');
  await captureStep(page, 'firms-empty', 'Step 4 of 7: NASA FIRMS', button('Save and test'));
  await page.getByLabel('Map key').fill(FIRMS_KEY);
  await page.getByRole('button', { name: 'Save and test' }).click();
  await page.getByText('FIRMS answered').waitFor();
  await captureStep(page, 'firms-saved', 'Step 4 of 7: NASA FIRMS', continueButton);
  await continueButton.click();

  await step('Starter forms');
  await page.locator('.pack-forms > li').first().waitFor();
  await captureStep(page, 'starter-forms', 'Step 5 of 7: Starter forms', button('Switch on the starter forms'));
  await button('Switch on the starter forms').click();
  await page.getByText('The starter forms are on.', { exact: true }).waitFor();
  await captureStep(page, 'starter-forms-on', 'Step 5 of 7: Starter forms', continueButton);
  await continueButton.click();

  await step('Invite officers');
  const choose = async (label, option) => {
    await page.getByLabel(label, { exact: true }).click();
    await page.getByRole('option', { name: option, exact: true }).click();
  };
  for (const [email, role, unit] of [
    ['division.officer@example.invalid', 'Division officer', 'Hill Division (Division D1)'],
    ['beat.guard@example.invalid', 'Field staff', 'Beat 05 (Beat B05)']
  ]) {
    await page.getByLabel('Email').fill(email);
    await choose('Role', role);
    await choose('Unit', unit);
    await page.getByRole('button', { name: 'Invite', exact: true }).click();
    await page.getByText('Invitation saved.').waitFor();
  }
  await page.getByRole('button', { name: 'Revoke' }).first().waitFor();
  await captureStep(page, 'invitations', 'Step 6 of 7: Invite officers', continueButton);
  await continueButton.click();

  await step('Finish');
  const finish = button('Finish and open the alert map');
  await captureStep(page, 'finish', 'Step 7 of 7: Finish', finish);
  await finish.click();
  await expect(page).toHaveURL(/\/monitor\/map/);
  await page.goto(WEB + '/settings/setup?workspace=demo-state');
  await button('Reopen setup').click();
  await step('Finish');
  await finish.click();
  await expect(page).toHaveURL(/\/monitor\/map/);
  await page.setViewportSize(desktop);
  await page.locator('.alert-table tbody tr').first().waitFor({ timeout: 60_000 });
  await page.waitForTimeout(2000);
  await save(page, 'alert-map-admin');

  const officer = await signIn(browser, 'division.officer');
  await officer.locator('.alert-table tbody tr').first().waitFor({ timeout: 60_000 });
  await officer.waitForTimeout(2000);
  await save(officer, 'alert-map-division-officer');

  const guard = await signIn(browser, 'beat.guard');
  await guard.locator('.alert-table tbody tr').first().waitFor({ timeout: 60_000 });
  await guard.waitForTimeout(2000);
  await save(guard, 'alert-map-beat-guard');

  const mail = await browser.newPage({ viewport: { width: 1280, height: 860 } });
  await mail.goto(MAIL);
  await mail.getByText('You are invited to Open FIMRA').first().click();
  await mail.getByText('Sign in at').first().waitFor();
  await save(mail, 'invitation-email');
} finally {
  await browser.close();
}
