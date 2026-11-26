// Copy this script beside playwright@1.63.0 and axe-core@4.10.3 in a scratch
// directory. Run in mcr.microsoft.com/playwright:v1.63.0-noble with host networking.
// Set WEB to the local guide server and SHOTS to a mounted output directory
// outside the checkout. No sign-in or API is needed.
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { isAbsolute, join } from 'node:path';
import axe from 'axe-core';
import { chromium } from 'playwright';

const { WEB, SHOTS } = process.env;
assert.ok(WEB && SHOTS && isAbsolute(SHOTS), 'Set WEB and an absolute SHOTS directory.');
await mkdir(SHOTS, { recursive: true });

const browser = await chromium.launch();
try {
  for (const theme of ['light', 'dark']) {
    for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
      const run = `${theme}-${viewport.width}`;
      const phone = viewport.width < 768;
      const page = await browser.newPage({ viewport, colorScheme: theme, reducedMotion: 'reduce' });
      const errors = [];
      page.setDefaultTimeout(90000);
      page.on('pageerror', error => errors.push(error.message));
      await page.goto(new URL('/design-system?preview', WEB).href, { waitUntil: 'domcontentloaded', timeout: 90000 });
      await page.getByRole('heading', { name: 'Design system', exact: true }).waitFor();
      await page.waitForFunction(mode => document.documentElement.dataset.theme === mode, theme);
      await page.evaluate(() => document.fonts.ready);
      await page.addScriptTag({ content: axe.source });

      const focused = locator => locator.and(page.locator(':focus')).waitFor();
      const button = name => page.getByRole('button', { name, exact: true });
      const heading = name => page.getByRole('heading', { name, exact: true });
      const save = (name, fullPage = false) => page.screenshot({
        path: join(SHOTS, `${run}-${name}.png`), fullPage, animations: 'disabled'
      });
      const scan = async label => {
        const results = await page.evaluate(async () => (await window.axe.run()).violations);
        const severe = results.filter(({ impact }) => ['serious', 'critical'].includes(impact));
        assert.deepEqual(severe.map(({ id, nodes }) => ({ id, nodes: nodes.map(({ target, failureSummary }) => ({
          target, failureSummary
        })) })), [], `${run} ${label}`);
        console.log(`${run} ${label} · 0 serious or critical violations`);
      };

      await button('Check fields').press('Enter');
      const summary = page.getByRole('group', { name: 'Check the following fields' });
      await focused(summary);
      const field = page.getByRole('textbox', { name: 'Unit code' });
      await summary.getByRole('link', { name: 'Enter a unit code.' }).press('Enter');
      await focused(field);
      assert.equal(await field.getAttribute('aria-describedby'), 'code-hint code-error');

      assert.equal(await button('Disabled').first().evaluate(node => getComputedStyle(node).cursor), 'default');
      await button('Save field note').press('Enter');
      await button('Save field note').and(page.locator('[aria-busy=true]')).waitFor();
      await focused(button('Save field note'));
      await save('saving');
      await page.getByRole('status').filter({ hasText: 'Field note saved.' }).waitFor();
      await page.getByText('Showing data from 14:05 UTC, refreshing.', { exact: true }).waitFor();
      await page.getByText('Demo beat · 2 detections', { exact: true }).filter({ visible: true }).waitFor();
      await scan('guide');
      await save('guide', true);

      const workspace = page.locator('#workspace-components');
      const comfortable = workspace.getByRole('article', { name: 'Comfortable' });
      const compact = workspace.getByRole('article', { name: 'Compact' });
      const alerts = comfortable.locator('fimra-data-table');
      const filters = comfortable.locator('fimra-filter-bar');
      const row = id => alerts.getByRole('button', { name: 'View ' + id });
      const workspaceButton = name => comfortable.getByRole('button', { name, exact: true });
      const dimensions = example => example.evaluate(node => {
        const style = getComputedStyle(node);
        return [style.getPropertyValue('--table-row-height').trim(), style.getPropertyValue('--filter-chip-height').trim()];
      });
      assert.deepEqual(await dimensions(comfortable), ['48px', '40px']);
      assert.deepEqual(await dimensions(compact), ['36px', '32px']);
      for (const [name, example] of [['comfortable', comfortable], ['compact', compact]]) {
        await example.screenshot({ path: join(SHOTS, `${run}-workspace-${name}.png`), animations: 'disabled' });
      }

      const bengali = alerts.locator(phone ? 'p[lang=bn]' : 'th[lang=bn]');
      await bengali.waitFor();
      if (phone) {
        const touch = await page.context().newCDPSession(page);
        await touch.send('Emulation.setTouchEmulationEnabled', { enabled: true });
        for (const name of ['Refresh alerts', 'Filters', 'Show all activity']) {
          const button = workspaceButton(name);
          const height = await button.evaluate(node => node.getBoundingClientRect().height);
          assert.ok(height >= 44, `${name} has a 44 px touch target (measured ${height} px).`);
        }
        assert.ok(await bengali.evaluate(node => {
          node.style.fontSize = '200%';
          node.style.lineHeight = '1.5';
          const box = node.getBoundingClientRect();
          const line = parseFloat(getComputedStyle(node).lineHeight);
          const wraps = box.height >= line * 2 && node.scrollWidth <= node.clientWidth;
          node.style.removeProperty('font-size');
          node.style.removeProperty('line-height');
          return wraps;
        }), 'The Bengali place label wraps at 200% text size.');
        assert.equal(await alerts.getByRole('table').count(), 0);
      } else {
        assert.equal(await alerts.locator('thead').evaluate(node => getComputedStyle(node).position), 'sticky');
        assert.equal(await alerts.getByRole('columnheader', { name: /Last seen/ }).getAttribute('aria-sort'), 'descending');
      }

      await alerts.locator('summary').press('Enter');
      const statusColumn = alerts.getByRole('checkbox', { name: 'Status', exact: true });
      await statusColumn.press('Space');
      assert.equal(await statusColumn.isChecked(), false);
      if (!phone) await alerts.getByRole('columnheader', { name: 'Status' }).waitFor({ state: 'hidden' });
      await scan('column choices');
      await statusColumn.press('Space');
      await alerts.locator('summary').press('Enter');

      const pick = alerts.getByRole('checkbox', { name: /Select Cedar ridge/ });
      await pick.press('Space');
      assert.equal(await pick.isChecked(), true);
      await workspaceButton('Next').press('Enter');
      await comfortable.getByText(/Showing data from .*IST, refreshing/).waitFor();
      assert.ok(await row('A-601').isVisible(), 'Prior rows stay visible during refresh.');
      await row('A-603').waitFor();
      await workspaceButton('Next').press('Enter');
      await row('A-605').waitFor();
      await workspaceButton('Previous').press('Enter');
      await row('A-603').waitFor();
      await workspaceButton('Previous').press('Enter');
      await row('A-601').waitFor();
      assert.equal(await pick.isChecked(), true);
      await comfortable.getByRole('button', { name: /Last seen.*sort ascending/ }).press('Enter');
      await row('A-606').waitFor();
      if (!phone) {
        assert.equal(await alerts.getByRole('columnheader', { name: /Last seen/ }).getAttribute('aria-sort'), 'ascending');
      }
      await comfortable.getByText('Sorted by Last seen, ascending', { exact: true }).waitFor();

      let filterFields = filters;
      if (phone) {
        await workspaceButton('Filters').press('Enter');
        filterFields = page.getByRole('dialog', { name: 'Filters' });
        await focused(filterFields.getByRole('heading', { name: 'Filters' }));
        await scan('filter sheet');
      }
      await filterFields.getByRole('searchbox', { name: 'Find a place' }).fill('Cedar');
      if (phone) {
        await page.keyboard.press('Escape');
        await focused(workspaceButton('Filters'));
      }
      await alerts.getByRole('button', { name: 'View A-601' }).waitFor();
      await filters.getByRole('button', { name: 'Remove filter Place: Cedar' }).press('Enter');
      await focused(filters.getByRole('button', { name: 'Remove filter Status: Open' }));
      await filters.getByRole('button', { name: 'Clear all filters' }).press('Enter');
      await filters.getByText('No active filters', { exact: true }).filter({ visible: true }).first().waitFor();

      await workspaceButton('Show all activity').press('Enter');
      assert.equal(await workspaceButton('Show milestones').getAttribute('aria-expanded'), 'true');
      await comfortable.getByText('Field note added', { exact: true }).waitFor();
      await scan('full activity');
      await workspaceButton('Show milestones').press('Enter');
      await comfortable.getByText('Field note added', { exact: true }).waitFor({ state: 'hidden' });

      await button('Open dialog').press('Enter');
      await focused(heading('Close this alert?'));
      await scan('dialog');
      await save('dialog');
      await page.keyboard.press('Escape');
      await focused(button('Open dialog'));

      const confirm = page.getByRole('alertdialog', { name: 'Close the demo alert?' });
      await button('Open confirmation').press('Enter');
      await focused(confirm.getByRole('button', { name: 'Cancel' }));
      await page.keyboard.press('Shift+Tab');
      await focused(confirm.getByRole('button', { name: 'Close alert' }));
      await page.keyboard.press('Tab');
      await focused(confirm.getByRole('button', { name: 'Cancel' }));
      await scan('confirmation');
      await save('confirmation');
      await page.keyboard.press('Escape');
      await focused(button('Open confirmation'));
      await button('Open confirmation').press('Enter');
      await confirm.getByRole('button', { name: 'Close alert' }).press('Enter');
      await focused(page.getByText('Alert closed', { exact: true }));

      await button('Open sheet').press('Enter');
      await focused(heading('Alert details'));
      await scan('sheet');
      await save('sheet');
      await page.getByRole('dialog', { name: 'Alert details' }).getByRole('button', { name: 'Close' }).press('Enter');
      await focused(button('Open sheet'));

      const panelType = viewport.width < 768 ? 'dialog' : 'region';
      await button('Open side panel').press('Enter');
      await focused(heading('Detection details'));
      const panel = page.getByRole(panelType, { name: 'Detection details' });
      const close = panel.getByRole('button', { name: 'Close', exact: true });
      assert.ok(await close.isVisible());
      if (panelType === 'dialog') {
        const box = await panel.locator('hlm-sheet-content').boundingBox();
        assert.ok(box && box.y + box.height === viewport.height, 'The phone panel docks at the bottom.');
      } else {
        const trigger = await button('Open side panel').boundingBox();
        const box = await panel.boundingBox();
        assert.ok(trigger && box && box.x > trigger.x + trigger.width, 'The desktop panel docks beside the example.');
      }
      await scan('side panel');
      await save('side-panel');
      await page.keyboard.press('Escape');
      await focused(button('Open side panel'));

      await button('Retry').press('Enter');
      await page.getByRole('status').filter({ hasText: 'Alert list loaded' }).waitFor();
      await page.getByText('Alert list loaded. Demo range · 1 alert', { exact: true }).waitFor();
      await button('Show toast').press('Enter');
      await page.getByText('Field note saved', { exact: true }).waitFor();
      assert.deepEqual(errors, []);
      await page.close();
    }
  }
} finally {
  await browser.close();
}
