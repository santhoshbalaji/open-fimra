// Headless walkthrough of the form designer, run by `make form-designer-walkthrough`
// in the Playwright container. The API harness has added a synthetic national
// template and filled in one report on it. The demo state administrator
// extends the template in the designer, by keyboard and by drag and drop,
// publishes the state's form, and checks that the earlier report still shows
// with the version it used. Screenshots go to SHOTS (outside the repository).
//
// Environment: WEB (web app address), API (core API address), ISSUER (sign-in
// issuer) and SHOTS (output folder).
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';
import { join } from 'node:path';

const { WEB, API, ISSUER, SHOTS } = process.env;
const submission = '7e3a1a7e-0000-4000-8000-00000000f0a1';
mkdirSync(SHOTS, { recursive: true });
let shot = 0;
const save = async (page, name) => {
  shot += 1;
  const file = join(SHOTS, `${String(shot).padStart(2, '0')}-${name}.png`);
  await page.screenshot({ path: file, fullPage: true });
  console.log('Saved', file);
};
const check = (ok, message) => { if (!ok) throw new Error(message); };

async function token(username) {
  const response = await fetch(ISSUER + '/protocol/openid-connect/token', {
    method: 'POST',
    body: new URLSearchParams({ grant_type: 'password', client_id: 'open-fimra-dev-cli', username, password: 'demo' })
  });
  check(response.ok, `sign-in for ${username} returned HTTP ${response.status}`);
  return (await response.json()).access_token;
}

async function get(path, username) {
  const response = await fetch(API + '/api/v1/workspaces/demo' + path, { headers: { Authorization: 'Bearer ' + await token(username) } });
  check(response.ok, `GET ${path} returned HTTP ${response.status}`);
  return response.json();
}

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
const page = await context.newPage();
try {
  await page.goto(WEB);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.locator('#username').fill('state.admin');
  await page.locator('#password').fill('demo');
  await page.locator('#kc-login').click();
  await page.getByRole('link', { name: 'Form templates', exact: true }).click();
  await page.getByRole('heading', { name: 'National templates' }).waitFor();
  const nationalCard = page.getByRole('article', { name: 'National report' });
  await nationalCard.getByText('1 report', { exact: true }).waitFor();
  await save(page, 'forms-by-level');

  // Extend the national template as the state's own form.
  await page.getByRole('button', { name: 'Extend version 1 of National report' }).click();
  await page.locator('#start-id').fill('demo-fire-report');
  await page.locator('#start-title').fill('Demo State fire report');
  await page.getByRole('button', { name: 'Open in the designer' }).click();
  await page.getByText('Extends national-fire-report version 1').first().waitFor();
  check(await page.locator('#form-row-0').getByText('Locked by template').count() === 1, 'the locked section is not marked');

  // Keyboard only: add a section and a question, rename it and move the section up.
  await page.getByRole('button', { name: 'Section', exact: true }).focus();
  await page.keyboard.press('Enter');
  await page.getByRole('button', { name: 'Text', exact: true }).focus();
  await page.keyboard.press('Enter');
  await page.locator('#prop-text-en').fill('Range office');
  await page.keyboard.press('Tab');
  await page.locator('#form-row-2').focus();
  await page.keyboard.press('Alt+ArrowUp');
  await page.getByText('Moved "New section" up to place 2.').waitFor({ state: 'attached' });
  check((await page.locator('#form-row-1').textContent()).includes('New section'), 'the new section did not move up');

  // Drag and drop: a number field from the palette into the new section.
  const source = page.locator('#form-palette').getByRole('button', { name: 'Number', exact: true });
  const target = page.locator('#form-list-1');
  const from = await source.boundingBox();
  const to = await target.boundingBox();
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(from.x + from.width / 2 + 20, from.y + from.height / 2 + 10, { steps: 5 });
  await page.mouse.move(to.x + to.width / 2, to.y + to.height - 4, { steps: 20 });
  await page.mouse.up();
  await page.getByText(/Added number "New question" to section "New section"/).waitFor({ state: 'attached' });

  // A locked question cannot be edited.
  await page.locator('#form-row-0-0').click();
  await page.locator('#properties-heading').getByText('Team reached the site at').waitFor();
  check(await page.locator('#prop-text-en').isDisabled(), 'a locked question can be edited');
  await page.getByText('No problems found').waitFor();
  await save(page, 'designer-extending-national-template');

  await page.getByRole('button', { name: 'Preview' }).click();
  await save(page, 'preview');
  await page.getByRole('button', { name: 'Design' }).click();

  await page.getByRole('button', { name: 'Save as draft' }).click();
  await page.getByRole('button', { name: 'Save draft' }).waitFor();
  await page.getByRole('button', { name: 'Publish…' }).click();
  await page.getByRole('button', { name: 'Publish', exact: true }).click();
  await page.getByText('Published versions never change.').waitFor();
  await save(page, 'published-state-form');

  // The list shows the state's form and the national template's report.
  await page.getByRole('button', { name: 'Back to forms' }).click();
  const stateCard = page.getByRole('article', { name: 'Demo State fire report' });
  await stateCard.getByText('extends national-fire-report version 1').waitFor();
  await nationalCard.getByText('1 report', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'View version 1 of National report' }).click();
  await page.getByText('1 report filled in on this version').waitFor();
  await save(page, 'national-version-with-its-report');
  await page.setViewportSize({ width: 390, height: 844 });
  const overflow = await page.evaluate(() => [...document.querySelectorAll('fimra-forms-screen *')]
    .filter(element => element.getBoundingClientRect().right > window.innerWidth + 1).length);
  check(overflow === 0, `${overflow} elements are wider than a phone screen`);
  await save(page, 'phone-width');

  // The earlier report still shows with the version it used, and the state's form is published.
  const report = await get('/submissions/' + submission, 'division.officer');
  check(report.form === 'national-fire-report' && report.version === 1, 'the earlier report changed version: ' + JSON.stringify(report));
  const state = await get('/forms/demo-fire-report/versions/1', 'state.admin');
  check(state.status === 'published' && state.extends?.form === 'national-fire-report' && state.extends?.version === 1,
    'the state form is not published from the template: ' + JSON.stringify(state));
  check(state.template.locked.includes('basics') && state.template.locked.includes('crew_size'), 'the state form lost the national locks');
  console.log('The state extended the national template and published it; the earlier report still shows with national-fire-report version 1.');
} catch (error) {
  await save(page, 'failure');
  console.error(await page.locator('.designer-problems').textContent().catch(() => ''));
  throw error;
} finally {
  await browser.close();
}
