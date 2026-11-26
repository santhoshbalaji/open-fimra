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
import AxeBuilder from '@axe-core/playwright';
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

const viewports = [{ width: 1440, height: 900 }, { width: 390, height: 844 }];
const themes = ['light', 'dark'];
const activate = locator => locator.press('Enter');

async function setTheme(page, theme) {
  await page.emulateMedia({ colorScheme: theme });
  await page.locator(`html[data-theme="${theme}"]`).waitFor({ state: 'attached' });
}

async function checkView(page, label, selector) {
  const axe = new AxeBuilder({ page });
  const result = await (selector ? axe.include(selector) : axe).analyze();
  const violations = result.violations.filter(v => v.impact === 'serious' || v.impact === 'critical');
  check(!violations.length, label + ': ' + JSON.stringify(violations.map(v => ({
    id: v.id, nodes: v.nodes.map(n => ({ target: n.target, summary: n.failureSummary }))
  }))));
  console.log(label + ' has zero serious or critical axe violations');
  await save(page, label);
}

async function checkFocus(locator, message) {
  try {
    await locator.and(locator.page().locator(':focus')).waitFor({ timeout: 3000 });
  } catch {
    throw new Error(message);
  }
}

async function typeText(input, value) {
  await input.press('Control+a');
  await input.pressSequentially(value);
  await input.press('Tab');
}

async function extendAndPublish(page, viewport, theme) {
  await page.setViewportSize(viewport);
  await setTheme(page, theme);
  const phone = viewport.width === 390;
  const label = `${viewport.width}-${theme}`;
  const id = 'demo-fire-report-' + label;
  const title = 'Demo fire report';
  const byName = (name, root = page) => root.getByRole('button', { name, exact: true });
  const nationalCard = page.getByRole('article', { name: 'National report' });
  const designer = page.locator('fimra-form-designer');
  const outline = page.getByRole('region', { name: 'Form outline' });
  const sheet = page.getByRole('dialog');
  const add = byName('Add', designer);
  const properties = byName('Properties', designer);
  await nationalCard.getByText('1 report', { exact: true }).waitFor();
  await activate(byName('Extend version 1 of National report', nationalCard));
  await typeText(page.locator('#start-id'), id);
  await typeText(page.locator('#start-title'), title);
  await activate(byName('Open in the designer'));
  await outline.waitFor();
  const lockedField = page.locator('#form-row-0-0');
  check(await lockedField.locator('fimra-status-badge svg[data-icon=lock]').count() === 1, 'the inherited question is missing its lock badge');
  check(await page.locator('fimra-status-badge svg[data-icon=file-pen-line]').count() === 1, 'the draft is missing its status icon');
  check(await designer.locator('fimra-status-badge svg[data-icon=check]').count() === 1, 'validation success is missing its check icon');

  if (phone) {
    const box = await outline.getByRole('heading', { name: 'Form outline' }).boundingBox();
    const selected = await outline.getByText('Selected Form settings', { exact: true }).boundingBox();
    const firstRow = await lockedField.boundingBox();
    check(box.y >= 0 && selected.y + selected.height < viewport.height && firstRow.y < viewport.height,
      'the outline and selection should be reachable on the first phone screen');
    check(await designer.locator('#form-palette').count() === 0, 'the field choices are inline on a phone');
    check(await designer.locator('#form-title').count() === 0, 'the properties are inline on a phone');
    check(await page.locator('fimra-forms-screen').evaluate(screen => screen.scrollWidth <= screen.clientWidth),
      'the designer has horizontal overflow on a phone');
  }
  await checkView(page, 'designer-' + label);

  if (phone) {
    await activate(add);
    await sheet.getByRole('heading', { name: 'Add to form' }).waitFor();
    await checkView(page, 'add-sheet-' + label);
  }
  await activate(byName('Section', phone ? sheet : designer));
  if (phone) {
    await sheet.waitFor({ state: 'hidden' });
    await checkFocus(add, 'Add did not regain focus');
    await page.keyboard.press('Enter');
  }
  await activate(byName('Text', phone ? sheet : designer));
  if (phone) {
    await sheet.waitFor({ state: 'hidden' });
    await checkFocus(add, 'Add did not regain focus after adding a question');
    await page.keyboard.press('Tab');
    await checkFocus(properties, 'Properties should follow Add in the tab order');
    await page.keyboard.press('Enter');
    await sheet.getByRole('heading', { name: 'Properties', exact: true }).waitFor();
    await checkView(page, 'properties-sheet-' + label);
  }
  const question = page.locator('#prop-text-en');
  await question.fill('');
  await question.blur();
  if (phone) {
    await page.keyboard.press('Escape');
    await sheet.waitFor({ state: 'hidden' });
  }
  const publish = byName('Publish…');
  await activate(publish);
  const summary = designer.locator('fimra-field-error-summary');
  const newRow = page.locator('#form-row-2-1');
  await checkFocus(summary, 'Publish did not take focus to the validation summary');
  await checkView(page, 'validation-' + label);
  await activate(summary.locator('a[href="#form-row-2-1"]'));
  await checkFocus(newRow, 'the validation link did not focus the question');
  if (phone) await activate(properties);
  await typeText(question, 'Range office');
  if (phone) {
    await page.keyboard.press('Escape');
    await sheet.waitFor({ state: 'hidden' });
    await checkFocus(properties, 'Properties did not regain focus');
    await outline.getByText('Selected Range office', { exact: true }).waitFor();
  }

  await page.locator('#form-row-2').press('Alt+ArrowUp');
  await page.getByText('Moved "New section" up to place 2.').waitFor({ state: 'attached' });
  const moved = page.locator('#form-row-1');
  check((await moved.textContent()).includes('New section'), 'the new section did not move up');
  await checkFocus(moved, 'focus did not follow the moved section');

  if (!phone && theme === 'light') {
    const source = designer.locator('#form-palette').getByRole('button', { name: 'Number', exact: true });
    const target = page.locator('#form-list-1');
    await source.evaluate(element => element.scrollIntoView({ block: 'center' }));
    await source.hover();
    const from = await source.boundingBox();
    const to = await target.boundingBox();
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
    await page.mouse.down();
    await page.mouse.move(from.x + from.width / 2 + 20, from.y + from.height / 2 + 10, { steps: 5 });
    await page.mouse.move(to.x + to.width / 2, to.y + to.height - 4, { steps: 20 });
    await page.mouse.up();
    await page.getByText(/Added number "New question" to section "New section"/).waitFor({ state: 'attached' });
  }

  await activate(lockedField);
  if (phone) await activate(properties);
  await page.locator('#properties-heading').getByText('Team reached the site at').waitFor();
  check(await page.locator('#prop-text-en').isDisabled(), 'a locked question can be edited');
  if (phone) {
    await page.keyboard.press('Escape');
    await sheet.waitFor({ state: 'hidden' });
  }

  await activate(byName('Preview'));
  await page.getByText('Shown when Likely cause is Lightning.').waitFor();
  await checkView(page, 'preview-' + label);
  if (theme === 'light') {
    await activate(byName('Save as draft'));
    await byName('Save draft').waitFor();
  }
  await activate(publish);
  const dialog = page.getByRole('alertdialog', { name: 'Publish version 1?' });
  await dialog.getByText(/Reports already filled in keep the version they used/).waitFor();
  await checkFocus(byName('Cancel', dialog), 'Cancel should receive initial focus');
  await checkView(page, 'publish-confirm-' + label);
  await page.keyboard.press('Escape');
  await dialog.waitFor({ state: 'hidden' });
  await checkFocus(publish, 'focus did not return to Publish');
  await page.keyboard.press('Enter');
  await dialog.waitFor();
  await checkFocus(byName('Cancel', dialog), 'Cancel should receive initial focus on reopen');
  await page.keyboard.press('Tab');
  await checkFocus(byName('Publish', dialog), 'Tab did not move to Publish');
  await page.keyboard.press('Enter');
  await page.getByText('Published versions never change.').waitFor();
  await checkView(page, 'published-' + label);

  await activate(byName('Back to forms'));
  await nationalCard.getByText('1 report', { exact: true }).waitFor();
  const state = await get('/forms/' + id + '/versions/1', 'state.admin');
  check(state.status === 'published' && state.extends?.form === 'national-fire-report' && state.extends?.version === 1,
    'the state form is not published from the template: ' + JSON.stringify(state));
  check(state.template.locked.includes('basics') && state.template.locked.includes('crew_size'), 'the state form lost the national locks');
  check(state.definition.sections[1].items.some(item => item.label?.en === 'Range office'), 'the edited and reordered question was not published');
}

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: viewports[0], reducedMotion: 'reduce' });
const page = await context.newPage();
try {
  await page.goto(WEB + '/settings/forms?workspace=demo');
  await activate(page.getByRole('button', { name: 'Sign in', exact: true }));
  await page.locator('#username').fill('state.admin');
  await page.locator('#password').fill('demo');
  await activate(page.locator('#kc-login'));
  await page.getByRole('heading', { name: 'National templates' }).waitFor();
  for (const viewport of viewports) {
    for (const theme of themes) await extendAndPublish(page, viewport, theme);
  }

  const report = await get('/submissions/' + submission, 'division.officer');
  check(report.form === 'national-fire-report' && report.version === 1, 'the earlier report changed version: ' + JSON.stringify(report));
  // Publish a changed version of the same form. Its rule would hide the
  // crew answer on the earlier report if the viewer used the latest version.
  const national = await get('/forms/national-fire-report/versions/1', 'state.admin');
  const newer = structuredClone(national.definition);
  newer.version = 2;
  newer.title.en = 'Updated national report';
  const crew = newer.sections[1].items[1].items[0];
  crew.showWhen.value = 'people';
  const added = await fetch(API + '/api/v1/workspaces/demo/forms', {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + await token('state.admin'), 'Content-Type': 'application/json' },
    body: JSON.stringify({ level: 'national', definition: newer, template: national.template })
  });
  check(added.status === 201, 'the new version did not publish: ' + await added.text());

  await page.goto(WEB + '/reports/field?workspace=demo&report=' + submission);
  const presentation = page.locator('fimra-submission-view fimra-form-presentation');
  await presentation.getByRole('heading', { name: 'National report', exact: true }).waitFor();
  const originalAnswer = presentation.getByText('4 people', { exact: true });
  check(await presentation.getByText('Updated national report').count() === 0, 'the earlier report shows the newer form title');
  await originalAnswer.waitFor();
  const reportHeading = page.getByRole('heading', { level: 2, name: 'National report', exact: true });
  const reportRow = page.locator(`[data-report-id="${submission}"]`).first();
  const review = page.getByRole('region', { name: 'Review actions' });
  const timeline = page.locator('fimra-submission-view fimra-timeline');
  const reportUrl = page.url();
  await page.locator('fimra-submission-view .report-alert-link').click();
  await page.getByRole('heading', { level: 2, name: /^Alert #/ }).waitFor();
  await page.goBack();
  check(page.url() === reportUrl, 'Back must restore the selected report');
  await checkFocus(reportHeading, 'Back must focus the report heading');
  for (const viewport of viewports) {
    await page.setViewportSize(viewport);
    for (const theme of themes) {
      await setTheme(page, theme);
      await page.reload();
      await reportHeading.waitFor();
      await checkFocus(reportHeading, 'focus must move to the report heading');
      await page.locator('.submission-header').getByText(/Beat 05/).waitFor();
      await timeline.getByText(/Submitted by/).waitFor();
      if (viewport.width >= 768) {
        const [list, detail] = await Promise.all([
          page.getByRole('region', { name: 'Field report queue' }).boundingBox(),
          page.locator('.report-panel').boundingBox()
        ]);
        check(list.x + list.width <= detail.x, 'the report panel must dock beside the list');
      }
      await review.waitFor();
      const actions = await review.boundingBox();
      check(actions.y + actions.height <= viewport.height, 'review actions must be reachable without scrolling');
      await originalAnswer.scrollIntoViewIfNeeded();
      await checkView(page, `historical-report-${viewport.width}-${theme}`);
      await page.keyboard.press('Escape');
      await reportRow.waitFor();
      await checkFocus(reportRow, 'closing a report must return focus to its row');
      await reportRow.press('Enter');
    }
  }
  console.log('The earlier report shows the crew size using version 1 after version 2 changed its visibility rule.');
} catch (error) {
  await save(page, 'failure');
  console.error(await page.locator('.designer-problems').textContent().catch(() => ''));
  throw error;
} finally {
  await browser.close();
}
