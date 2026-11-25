// Headless walkthrough of the first-run wizard, run by `make wizard-walkthrough`
// in the Playwright container. It signs in as the demo administrator on an
// empty install, completes every step with the synthetic demo files, and saves
// a screenshot of each screen to SHOTS (outside the repository).
//
// Environment: WEB (web app address), MAIL (mail catcher address), SEED (the
// seed/demo-state folder), SHOTS (output folder), SETUP_CODE and FIRMS_KEY
// (the fake FIRMS server's key).
import { chromium } from 'playwright';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const { WEB, MAIL, SEED, SHOTS, SETUP_CODE, FIRMS_KEY } = process.env;
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

async function signIn(browser, username) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 860 } });
  const page = await context.newPage();
  await page.goto(WEB);
  await page.locator('#username').fill(username);
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
  return page;
}

const browser = await chromium.launch();
try {
  const page = await signIn(browser, 'state.admin');
  await page.getByRole('heading', { name: 'Set up Open FIMRA' }).waitFor();
  await page.getByLabel('Setup code').fill(SETUP_CODE);
  await page.getByLabel('Workspace name').fill('Demo State');
  await page.getByLabel('State code').fill('DS');
  await save(page, 'bootstrap');
  await page.getByRole('button', { name: 'Start setup' }).click();

  const step = name => page.locator('#step-heading', { hasText: name }).waitFor();
  await step('Hierarchy');
  await page.getByLabel('Hindi label for level 5').fill('बीट (वन)');
  await save(page, 'hierarchy-preset');
  await page.getByRole('button', { name: 'Save hierarchy' }).click();

  await step('Layers');
  await save(page, 'layers-empty');
  const layers = [
    ['level:0', 'state'], ['level:1', 'circles'], ['level:2', 'divisions'], ['level:3', 'ranges'],
    ['level:4', 'beats'], ['forest', 'forest'], ['exclusion', 'exclusions']
  ];
  for (const [key, stem] of layers) {
    const row = page.locator('.layer-checklist > li').filter({ has: page.locator(`[id="file-${key}"]`) });
    const upload = async file => {
      await page.locator(`[id="file-${key}"]`).setInputFiles(file);
      await row.getByRole('button', { name: 'Upload' }).click();
    };
    await upload(join(SEED, `${stem}.geojson`));
    if (stem === 'beats') {
      await row.getByText('Correct these problems').waitFor({ timeout: 60_000 });
      await save(page, 'beats-blocking-problems');
      await upload(repairedBeats());
    }
    await row.getByRole('button', { name: 'Publish' }).waitFor({ timeout: 60_000 });
    await row.getByRole('button', { name: 'Publish' }).click();
    await row.locator('.layer-state.is-done').waitFor({ timeout: 30_000 });
  }
  await save(page, 'layers-published');
  await page.getByRole('button', { name: 'Continue' }).click();

  await step('NASA FIRMS');
  await save(page, 'firms-empty');
  await page.getByLabel('Map key').fill(FIRMS_KEY);
  await page.getByRole('button', { name: 'Save and test' }).click();
  await page.getByText('FIRMS answered').waitFor();
  await save(page, 'firms-saved');
  await page.getByRole('button', { name: 'Continue' }).click();

  await step('Starter forms');
  await save(page, 'starter-forms');
  await page.getByRole('button', { name: 'Continue' }).click();

  await step('Invite officers');
  await page.locator('[name=unitPath] option').nth(2).waitFor({ state: 'attached' });
  for (const [email, role, unit] of [
    ['division.officer@example.invalid', 'division_officer', 'ds.c1.d1'],
    ['beat.guard@example.invalid', 'field_staff', 'ds.c1.d1.r2.b05']
  ]) {
    await page.getByLabel('Email').fill(email);
    await page.getByLabel('Role').selectOption(role);
    await page.getByLabel('Unit').selectOption(unit);
    await page.getByRole('button', { name: 'Invite', exact: true }).click();
    await page.getByText('Invitation saved.').waitFor();
  }
  await page.getByText('Email sent').nth(1).waitFor({ timeout: 30_000 }).catch(() => {});
  await page.getByRole('button', { name: 'Revoke' }).first().waitFor();
  await save(page, 'invitations');
  await page.setViewportSize({ width: 390, height: 844 });
  await save(page, 'invitations-phone');
  await page.setViewportSize({ width: 1280, height: 860 });
  await page.getByRole('button', { name: 'Continue' }).click();

  await step('Finish');
  await save(page, 'finish');
  await page.getByRole('button', { name: 'Finish and open the alert map' }).click();
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
