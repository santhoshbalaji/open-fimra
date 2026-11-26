// Drives the field app's browser build as the demo beat guard, at phone size,
// in headless Chromium. Run it with `make mobile-walkthrough`. It points the
// app at the test API under Server settings, signs in through Keycloak with
// PKCE in a pop-up window, checks the alert list, opens the map and
// acknowledges an alert. Then the division officer wipes the phone through
// the API, and the app's next call deletes its data and returns to sign-in.
// Screenshots go to SHOTS, outside the repository.
import { chromium } from 'playwright';

const { WEB, API, ISSUER, SHOTS } = process.env;
const phone = { viewport: { width: 412, height: 915 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true };
const browser = await chromium.launch({ args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] });
const steps = [];
const tileStatuses = [];

function step(text) {
  steps.push(text);
  console.log('- ' + text);
}

async function shot(page, name, scale = 'device') {
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${SHOTS}/${name}.png`, scale });
}

async function themes(page, name) {
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    await shot(page, `${name}-${colorScheme}`);
  }
  await page.emulateMedia({ colorScheme: 'light' });
}

// Flutter puts some text in the page and some in accessibility labels.
function shown(page, text, exact = false) {
  return page.getByText(text, { exact }).or(page.getByLabel(text, { exact })).first();
}

async function scrollTo(page, target) {
  const { width, height } = page.viewportSize();
  await page.mouse.move(width / 2, height / 2);
  for (let attempt = 0; attempt < 8 && !await target.isVisible(); attempt++) {
    await page.mouse.wheel(0, 350);
    await page.waitForTimeout(200);
  }
  await target.scrollIntoViewIfNeeded();
}

async function typeInto(page, label, value) {
  const box = page.getByRole('textbox', { name: label });
  await box.click();
  // The semantics control can receive the click before Flutter attaches its HTML input.
  const editor = page.locator('input:focus, textarea:focus');
  await editor.fill(value);
}

async function openApp(context) {
  const page = await context.newPage();
  page.on('response', response => {
    if (response.url().includes('/tiles/')) tileStatuses.push(response.status());
  });
  page.on('pageerror', error => console.log('page error: ' + error.message));
  await page.goto(WEB);
  return page;
}

let current;
try {
  const context = await browser.newContext({
    ...phone,
    colorScheme: 'light',
    permissions: ['geolocation'],
    geolocation: { latitude: 16.347, longitude: 86.2264, accuracy: 20 },
  });
  const page = await openApp(context);
  current = page;
  const signIn = page.getByRole('button', { name: 'Sign in', exact: true });
  await signIn.waitFor({ timeout: 60_000 });
  await themes(page, '01-sign-in');
  step('The sign-in screen shows the logo, Sign in and Server settings.');

  await page.getByRole('button', { name: 'Server settings' }).click();
  await typeInto(page, 'Core API address', API);
  await typeInto(page, 'Sign-in address', ISSUER);
  await themes(page, '02-server-settings');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await page.getByRole('textbox', { name: 'Core API address' }).waitFor({ state: 'detached' });
  await shown(page, 'Server: ' + new URL(API).host).waitFor();
  await page.waitForTimeout(500);
  step(`Server settings point the app at ${API} and ${ISSUER}.`);

  const discovery = '**/.well-known/openid-configuration';
  await page.route(discovery, route => route.abort(), { times: 1 });
  await signIn.click();
  await shown(page, 'Cannot reach the server. Check the address or try again.').waitFor();
  await page.getByRole('button', { name: 'Technical details', exact: true }).waitFor();
  await themes(page, '03-server-unreachable');
  step('The sign-in error gives a next action and folds away the server details.');

  const popupPromise = context.waitForEvent('page');
  await page.route(/\/api\/v1\/workspaces\/demo\/alerts\?/, route => route.abort(), { times: 1 });
  await signIn.click();
  const popup = await popupPromise;
  await popup.waitForLoadState();
  const authorize = new URL(popup.url());
  if (authorize.searchParams.get('code_challenge_method') !== 'S256' || !authorize.searchParams.get('code_challenge')) {
    throw new Error('The sign-in request has no S256 proof key: ' + popup.url());
  }
  if (authorize.searchParams.get('client_id') !== 'open-fimra-mobile') throw new Error('Wrong client: ' + popup.url());
  await popup.locator('#username').fill('beat.guard');
  await popup.locator('#password').fill('demo');
  await popup.screenshot({ path: `${SHOTS}/03-keycloak.png` });
  await popup.locator('#kc-login').click();
  step('Keycloak opened for the open-fimra-mobile client with an S256 code challenge; the beat guard signed in.');

  const retry = page.getByRole('button', { name: 'Retry', exact: true });
  await shown(page, 'Could not load alerts').waitFor();
  await themes(page, '04-alerts-retry');
  await retry.click();
  await shown(page, '2 alerts in the past 7 days · Beat 01').waitFor({ timeout: 30_000 });
  await themes(page, '04-alerts');
  // Each alert card is one button whose label starts with its place.
  const cards = page.getByRole('button', { name: /^Beat 01 · Alert #\d+,/ });
  const places = await cards.count();
  const others = await page.getByRole('button', { name: /^Beat (0[2-9]|[1-9][0-9])/ }).count();
  if (others) throw new Error(`The beat guard sees ${others} alerts outside Beat 01`);
  if (places !== 2) throw new Error(`Expected two alerts in Beat 01, found ${places}`);
  step('Retry restored the list. Both Beat 01 cards name their alert number and status.');

  await page.getByRole('tab', { name: 'Map' }).or(page.getByRole('button', { name: 'Map' })).first().click();
  await page.waitForTimeout(8000);
  await themes(page, '05-map');
  const refused = tileStatuses.filter(status => status >= 400);
  if (tileStatuses.length === 0 || refused.length) {
    throw new Error(`Tile requests: ${tileStatuses.join(', ') || 'none'}`);
  }
  step(`The map loaded ${tileStatuses.length} tiles with the beat guard's token (HTTP ${[...new Set(tileStatuses)].join(', ')}).`);

  await page.getByRole('button', { name: 'Map key', exact: true }).click();
  await shown(page, 'Shape shows the forest class', true).waitFor();
  const closeKey = page.getByRole('button', { name: 'Close', exact: true });
  for (const colorScheme of ['light', 'dark']) {
    await page.emulateMedia({ colorScheme });
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      await page.setViewportSize(viewport);
      await closeKey.waitFor();
      await shot(page, `05-map-key-${colorScheme}-${viewport.width}`, 'css');
    }
  }
  await page.emulateMedia({ colorScheme: 'light' });
  await page.setViewportSize(phone.viewport);
  await closeKey.click();
  await closeKey.waitFor({ state: 'hidden' });
  step('The visible Close button dismisses the map key.');

  await page.getByRole('tab', { name: 'Alerts' }).or(page.getByRole('button', { name: 'Alerts' })).first().click();
  await page.setViewportSize({ width: 360, height: 800 });
  await cards.first().click();
  const acknowledge = page.getByRole('button', { name: 'Acknowledge', exact: true });
  const connected = shown(page, 'Connected', true);
  for (const target of [acknowledge, connected]) {
    await target.waitFor();
    const box = await target.boundingBox();
    if (!box || box.y < 0 || box.y + box.height > 800) throw new Error('Expected the action and connection above the fold');
  }
  await shown(page, 'IST').waitFor();
  await themes(page, '06-alert-360x800');
  const details = page.getByLabel(/^Details\b/).or(page.getByText('Details', { exact: true })).first();
  if (await shown(page, 'Hill Division (D1)').isVisible()) throw new Error('Detection details started expanded');
  await details.click();
  await shown(page, 'Hill Division (D1)').waitFor();
  await details.click();
  const showOnMap = page.getByRole('button', { name: 'Show on map', exact: true });
  await showOnMap.click();
  await shown(page, 'Alert map', true).waitFor();
  await page.getByRole('button', { name: 'Map key', exact: true }).waitFor();
  await page.getByRole('button', { name: 'Show my location', exact: true }).click();
  await page.locator('.maplibregl-user-location-dot').waitFor({ timeout: 15_000 });
  step('The map shows the phone location after location access is granted.');
  await themes(page, '06-alert-map');
  await page.getByRole('button', { name: 'Back', exact: true }).click();
  await showOnMap.waitFor();
  step('The alert shows its place, detection details and IST time. Show on map opens a focused map.');

  await acknowledge.click();
  await typeInto(page, 'Note (optional)', 'On my way.');
  await themes(page, '07-acknowledge');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await shown(page, 'Acknowledged recorded.').waitFor();
  await scrollTo(page, shown(page, 'On my way.'));
  await shown(page, 'Demo Beat Guard', false).waitFor();
  await themes(page, '08-acknowledged');
  step('Acknowledged was recorded; the timeline shows it with the beat guard\'s name and note.');

  const state = await context.storageState();
  const dark = await browser.newContext({ ...phone, colorScheme: 'dark', storageState: state });
  const darkPage = await openApp(dark);
  await shown(darkPage, 'alerts in the past 7 days', false).waitFor({ timeout: 60_000 });
  await shot(darkPage, '09-alerts-dark');
  step('After a reload the saved session opens the list again, here in the dark theme.');

  await darkPage.getByRole('button', { name: 'Account' }).click();
  await shown(darkPage, 'Notifications and phones').click();
  await shown(darkPage, 'Browser test build (this phone)').waitFor();
  await shown(darkPage, 'Notifications are not available here').waitFor();
  await shot(darkPage, '10-phones');
  step('Notifications and phones lists this registration; the browser build has no push.');

  // The division officer wipes the phone, as an administrator would for a lost one.
  const form = new URLSearchParams({ grant_type: 'password', client_id: 'open-fimra-dev-cli', username: 'division.officer', password: 'demo' });
  const token = (await (await fetch(`${ISSUER}/protocol/openid-connect/token`, { method: 'POST', body: form })).json()).access_token;
  const auth = { Authorization: `Bearer ${token}` };
  const area = await (await fetch(`${API}/api/v1/workspaces/demo/devices`, { headers: auth })).json();
  const phoneRow = area.items.find(d => d.platform === 'web' && d.status === 'active' && d.owner?.username === 'beat.guard');
  if (!phoneRow) throw new Error('The division officer does not see the beat guard\'s phone.');
  const wiped = await fetch(`${API}/api/v1/workspaces/demo/devices/${phoneRow.id}/wipe`, { method: 'POST', headers: auth });
  if (wiped.status !== 200) throw new Error(`Wipe request failed with HTTP ${wiped.status}`);
  await darkPage.reload();
  await shown(darkPage, 'An administrator asked this phone to delete its field data', false).waitFor({ timeout: 60_000 });
  await darkPage.getByRole('button', { name: 'Sign in', exact: true }).waitFor();
  await shot(darkPage, '11-wiped');
  const after = await (await fetch(`${API}/api/v1/workspaces/demo/devices`, { headers: auth })).json();
  if (after.items.find(d => d.id === phoneRow.id)?.status !== 'wiped') throw new Error('The phone did not report its wipe.');
  step('After the wipe request, the next start deleted the session and registration and showed why; the API records it as wiped.');
  console.log('Walkthrough passed.');
} catch (error) {
  if (current) {
    await current.screenshot({ path: `${SHOTS}/failure.png` }).catch(() => {});
    console.log(await current.locator('body').ariaSnapshot().catch(e => String(e)));
  }
  throw error;
} finally {
  await browser.close();
}
