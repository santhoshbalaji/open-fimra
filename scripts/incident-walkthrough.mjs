import assert from 'node:assert/strict';
import { join } from 'node:path';
import AxeBuilder from '@axe-core/playwright';
import { chromium, expect as playwrightExpect } from 'playwright/test';

const { WEB, API, SHOTS } = process.env;
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader'] });
const expect = playwrightExpect.configure({ timeout: 25_000 });
const viewport = {
  desktop: { width: 1920, height: 1080 },
  laptop: { width: 1366, height: 768 },
  tablet: { width: 768, height: 1024 },
  phone: { width: 390, height: 844 }
};
const listRequest = url => url.origin === API && url.pathname.endsWith('/incidents');
const incidentLink = page => page.locator('[data-incident-id]');
const results = {};
let step;

async function capture(page, label) {
  const { violations } = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'best-practice'])
    .include('main').include('[role=dialog]').analyze();
  const serious = violations.filter(v => v.impact === 'serious' || v.impact === 'critical');
  assert.deepEqual(serious.map(v => ({ id: v.id, elements: v.nodes.map(node => node.target) })), [], label);
  await page.screenshot({ path: join(SHOTS, label + '.png') });
  console.log(label + ' — zero serious or critical axe violations');
}

async function open(page, row) {
  const id = await row.getAttribute('data-incident-id');
  await row.press('Enter');
  const heading = page.getByRole('heading', { name: 'Incident #' + id, exact: true });
  await expect(heading).toBeFocused();
  await expect(page.getByRole('heading', { name: /^Member alerts/ })).toBeVisible();
  return heading;
}

async function checkPhoneCards(page) {
  const list = page.getByRole('region', { name: 'Incident list' });
  const cards = list.getByRole('list', { name: 'Incidents', exact: true });
  await expect(cards.getByRole('listitem').first()).toBeVisible();
  await expect(list.getByRole('table')).toHaveCount(0);
  assert.ok(await list.evaluate(element =>
    [document.documentElement, element, ...element.querySelectorAll('li')].every(node =>
      node.scrollWidth <= node.clientWidth + 1)), 'The phone cards fit without sideways scrolling');
}

async function checkMapRoundTrip(page, row, size) {
  const incidentID = await row.getAttribute('data-incident-id');
  const incidentTitle = 'Incident #' + incidentID;
  const incidentHeading = page.getByRole('heading', { name: incidentTitle, exact: true });
  const panel = size === 'phone' ? page.getByRole('dialog', { name: incidentTitle, exact: true })
    : page.locator('fimra-side-panel.incident-panel > section');
  if (size === 'phone') await checkPhoneCards(page);
  const listURL = page.url();
  await open(page, row);
  const member = panel.getByRole('button', { name: /Alert #\d+/ }).first();
  await expect(member).toBeVisible();
  const memberId = (await member.textContent()).trim().match(/#(\d+)$/)[1];
  await member.press('Enter');
  await expect(member).toHaveAttribute('aria-expanded', 'true');
  const detailURL = page.url();
  const link = panel.getByRole('link', { name: 'Show alert #' + memberId + ' on map', exact: true });
  const [selected] = await Promise.all([
    page.waitForResponse(response => response.request().method() === 'GET' && response.url().endsWith('/alerts/' + memberId)),
    link.press('Enter')
  ]);
  assert.equal(selected.status(), 200, 'The selected member alert must load');
  await expect(page).toHaveURL(url => url.pathname === '/monitor/map'
    && url.searchParams.get('alert') === memberId && url.searchParams.get('incident') === incidentID);
  const alertTitle = 'Alert #' + memberId;
  if (size === 'phone') {
    // The sheet makes the Map heading inaccessible while it is open.
    const dialog = page.getByRole('dialog', { name: alertTitle, exact: true });
    await expect(dialog.getByRole('heading', { name: alertTitle, exact: true })).toBeVisible();
  } else {
    await expect(page.getByRole('heading', { level: 1, name: 'Map' })).toBeVisible();
    await expect(page.getByRole('heading', { name: alertTitle, exact: true })).toBeVisible();
  }
  // A map with an open phone sheet still updates its legend and tile state.
  const map = page.locator('.map-pane');
  await expect(map.getByText('Incident member', { exact: true })).toBeAttached();
  await expect(map.locator('p').filter({ hasText: /^(Loading fire detections|Some map data is missing)$/ })).toHaveCount(0);

  await page.goBack();
  await expect(page).toHaveURL(detailURL);
  await expect(incidentHeading).toBeFocused();
  await expect(member).toHaveAttribute('aria-expanded', 'true');
  await page.goBack();
  await expect(member).toHaveAttribute('aria-expanded', 'false');
  await page.goBack();
  await expect(page).toHaveURL(listURL);
  await expect(row).toBeFocused();
  if (size === 'phone') await checkPhoneCards(page);
}

async function tabTo(page, target) {
  await expect(target).toBeEnabled();
  for (let i = 0; i < 16 && !await target.evaluate(element => document.activeElement === element); i++) {
    await page.keyboard.press('Tab');
  }
  await expect(target).toBeFocused();
}

async function checkKeyboardAction(page, context, incident, listResponse) {
  const workspace = new URL(listResponse.url()).pathname.replace(/\/incidents$/, '');
  const headers = await listResponse.request().allHeaders();
  const auth = { authorization: headers.authorization };
  const response = await context.request.get(API + workspace + '/alerts?incident=' + incident.id, { headers: auth });
  assert.equal(response.status(), 200);
  const { items } = await response.json();
  // A multi-member fixture lets the same flow check both moved and skipped
  // alerts. Keep one fresh member eligible to move with the incident.
  const staged = items.length > 1 ? items.find(item => item.status === 'new') : undefined;
  if (staged) {
    const closed = await context.request.post(API + workspace + '/alerts/' + staged.id + '/steps',
      { headers: auth, data: { step: 'false_alarm' } });
    assert.equal(closed.status(), 200);
  }
  await page.setViewportSize(viewport.tablet);
  const row = page.locator('[data-incident-id="' + incident.id + '"]');
  await open(page, row);
  const workflow = page.locator('fimra-workflow');
  const action = workflow.getByRole('button', { name: 'Acknowledge alert', exact: true });
  await tabTo(page, action);
  await page.keyboard.press('Enter');
  await expect(workflow.getByRole('heading', { name: /Record step.*Acknowledged/ })).toBeFocused();
  await expect(workflow.getByText('Will move from New to Acknowledged.').first()).toBeVisible();
  if (staged) await expect(workflow.getByText('Will stay at False alarm. Already closed.')).toBeVisible();
  for (const theme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme: theme });
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
    await capture(page, 'division.officer-tablet-' + theme + '-impact-preview');
  }
  const save = workflow.getByRole('button', { name: 'Save Acknowledged', exact: true });
  await tabTo(page, save);
  const [alertsMoved] = await Promise.all([
    page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/incidents/' + incident.id + '/steps'))
      .then(async response => {
        assert.equal(response.status(), 200);
        const result = await response.json();
        return result.alertsMoved;
      }),
    page.keyboard.press('Enter')
  ]);
  await expect(workflow.getByText('Saved: Acknowledged.').locator('..')).toBeFocused();
  assert.equal(alertsMoved, items.filter(item => item.status === 'new' && item.id !== staged?.id).length);
  await expect(workflow.getByText(alertsMoved + (alertsMoved === 1 ? ' alert moved.' : ' alerts moved.'), { exact: true })).toBeVisible();
  await expect(workflow.getByText('Moved from New with the incident.').first()).toBeVisible();
  if (staged) {
    const link = workflow.getByRole('link', { name: 'Open alert #' + staged.id, exact: true });
    await expect(link).toHaveAttribute('href', new RegExp('[?&]alert=' + staged.id + '(&|$)'));
    await expect(workflow.getByText('Stayed at False alarm. Already closed.')).toBeVisible();
    await tabTo(page, link);
  }
  await capture(page, 'division.officer-tablet-dark-impact-result');
}

try {
  for (const user of ['state.admin', 'division.officer']) {
    step = `${user} — sign in and load incidents`;
    const context = await browser.newContext({ viewport: viewport.desktop, reducedMotion: 'reduce' });
    const page = await context.newPage();
    await page.goto(WEB + '/monitor/incidents?hours=168');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.locator('#username').fill(user);
    await page.locator('#password').fill('demo');
    let firstListBody;
    await page.route(listRequest, async route => {
      if (route.request().method() !== 'GET') return route.continue();
      const response = await route.fetch();
      if (response.ok()) firstListBody ??= await response.json();
      await route.fulfill({ response });
    });
    const [firstList] = await Promise.all([
      page.waitForResponse(response => response.request().method() === 'GET' && listRequest(new URL(response.url()))),
      page.locator('#kc-login').click()
    ]);
    assert.equal(firstList.status(), 200, 'The incident list must load after sign in');
    await expect(incidentLink(page).first()).toBeVisible();
    await page.waitForLoadState('networkidle');
    await page.unroute(listRequest);
    assert.ok(firstListBody, 'The initial incident response must have a body');
    results[user] = firstListBody.items;

    for (const [size, dimensions] of Object.entries(viewport)) {
      await page.setViewportSize(dimensions);
      for (const theme of ['light', 'dark']) {
        const prefix = `${user}-${size}-${theme}`;
        step = `${prefix} — render the incident list`;
        await page.emulateMedia({ colorScheme: theme });
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        const row = incidentLink(page).first();
        if (size === 'desktop') {
          const pager = page.getByRole('group', { name: 'Incidents pages' });
          const list = page.getByRole('region', { name: 'Incident list' });
          await expect(pager).toBeVisible();
          const [pagerBox, listBox] = await Promise.all([pager.boundingBox(), list.boundingBox()]);
          assert.ok(listBox.y + listBox.height - pagerBox.y - pagerBox.height <= 20,
            'The pager stays at the bottom of the list');
        }
        await capture(page, prefix + '-loaded');
        step = `${prefix} — open and dismiss incident details`;
        await open(page, row);
        await expect(page.getByRole('heading', { name: 'Timeline', exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: /Alert #\d+/ }).first()).toBeVisible();
        if (size === 'phone') {
          const sheet = page.getByRole('dialog', { name: /^Incident #/ }).locator('hlm-sheet-content');
          await expect(sheet).toBeVisible();
          assert.equal((await sheet.boundingBox()).height, dimensions.height, 'The phone detail fills the viewport');
        }
        await capture(page, prefix + '-detail');
        await page.keyboard.press('Escape');
        await expect(page).not.toHaveURL(/incident=/);
        await expect(row).toBeFocused();

        step = `${prefix} — render the empty incident list`;
        await page.route(listRequest, route => route.fulfill({
          json: { items: [], total: 0 }, headers: { 'Access-Control-Allow-Origin': WEB }
        }));
        await page.reload();
        await expect(page.getByText('No incidents', { exact: true })).toBeVisible();
        await page.waitForLoadState('networkidle');
        await capture(page, prefix + '-empty');
        await page.unroute(listRequest);

        step = `${prefix} — render the incident list error and retry`;
        await page.route(listRequest, route => route.abort());
        await page.reload();
        await expect(page.getByRole('paragraph').filter({ hasText: 'Could not load incidents' })).toBeVisible();
        await page.waitForLoadState('networkidle');
        await capture(page, prefix + '-error');
        await page.unroute(listRequest);
        await page.getByRole('button', { name: 'Retry', exact: true }).click();
        await expect(row).toBeVisible();

        step = `${prefix} — follow the member alert to the map and restore history`;
        await checkMapRoundTrip(page, row, size);
        console.log(prefix + ' — map round trip, member selection and list restored');
      }
    }

    step = `${user} — check cards at narrow and wide phone sizes`;
    for (const dimensions of [{ width: 360, height: 800 }, { width: 412, height: 915 }]) {
      await page.setViewportSize(dimensions);
      await checkPhoneCards(page);
      await capture(page, user + '-phone-dark-' + dimensions.width);
    }
    if (user === 'division.officer') {
      step = 'division.officer — preview and save an incident action by keyboard on tablet';
      const candidates = results[user].filter(row => row.status === 'new');
      const incident = candidates.find(row => row.alert_count > 1) ?? candidates[0];
      assert.ok(incident, 'Seed a new incident for the keyboard walkthrough');
      await checkKeyboardAction(page, context, incident, firstList);
      await page.keyboard.press('Escape');
      await expect(page).not.toHaveURL(/incident=/);
    }
    await page.setViewportSize(viewport.phone);
    step = `${user}-phone-dark — wrap long place names at 200% text size`;
    const row = incidentLink(page).first();
    const incidentID = await row.getAttribute('data-incident-id');
    const panel = page.getByRole('dialog', { name: 'Incident #' + incidentID, exact: true });

    // Long synthetic place names exercise wrapping on the same list and sheet.
    const labels = ['উত্তর পাহাড় সংরক্ষিত বনাঞ্চলের পূর্ব প্রান্তের পর্যবেক্ষণ কেন্দ্র',
      'उत्तरी पहाड़ी आरक्षित वन क्षेत्र का पूर्वी निगरानी केंद्र'];
    await page.route(url => listRequest(url) || url.origin === API && url.pathname.endsWith('/incidents/' + incidentID), async route => {
      if (route.request().method() !== 'GET') return route.continue();
      const response = await route.fetch();
      const data = await response.json();
      for (const record of data.items ?? [data]) {
        record.beat_names = [labels[0]];
        record.division_names = [labels[1]];
      }
      await route.fulfill({ response, json: data });
    });
    await page.reload();
    await page.addStyleTag({ content: 'html { font-size: 200%; }' });
    await expect(row).toContainText(labels.join(' · '));
    await open(page, row);
    const place = panel.getByText(labels.join(' · '), { exact: true });
    await expect(place).toBeVisible();
    assert.ok(await place.evaluate(element => {
      const box = element.getBoundingClientRect();
      return box.height >= parseFloat(getComputedStyle(element).lineHeight) * 2 && element.scrollWidth <= element.clientWidth;
    }), 'Long place names wrap in the sheet at 200% text size');
    await page.waitForLoadState('networkidle');
    await capture(page, user + '-phone-dark-large-text');
    await context.close();
  }

  step = 'compare incident membership for both roles';
  const adminRows = new Map(results['state.admin'].map(row => [row.id, row.alert_count]));
  const officerRows = results['division.officer'];
  for (const row of officerRows) assert.ok(row.alert_count <= adminRows.get(row.id), 'Officer membership exceeds admin visibility');
  assert.ok(adminRows.size > officerRows.length || officerRows.some(row => row.alert_count < adminRows.get(row.id)),
    'Synthetic data must exercise narrower officer scope');
} catch (cause) {
  throw new Error(`Incident walkthrough failed during ${step}`, { cause });
} finally {
  await browser.close();
}
