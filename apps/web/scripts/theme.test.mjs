import assert from 'node:assert/strict';
import { test } from 'node:test';
import { problem } from './lint-colors.mjs';
import { resolve, themes, tokens } from './theme.mjs';

const rgb = hex => hex.match(/[\da-f]{2}/gi).map(channel => parseInt(channel, 16));
const composite = (foreground, background, opacity) =>
  background.map((channel, i) => foreground[i] * opacity + channel * (1 - opacity));
const luminance = rgb => rgb.reduce((value, component, i) => {
  const channel = component / 255;
  const linear = channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  return value + linear * [0.2126, 0.7152, 0.0722][i];
}, 0);

function passes(label, foreground, background, minimum) {
  const lum = [foreground, background].map(value => luminance(typeof value === 'string' ? rgb(value) : value));
  const ratio = (Math.max(...lum) + 0.05) / (Math.min(...lum) + 0.05);
  assert.ok(ratio >= minimum, `${label}: ${ratio.toFixed(2)}:1, expected at least ${minimum}:1`);
}

for (const theme of themes) {
  test(`${theme}: interface text, focus and control boundaries`, () => {
    const role = name => resolve(`semantic.color.${name}`, theme);
    const overlay = resolve('components.overlay.background', theme);
    for (const [name, surface] of [
      ['canvas', role('canvas')], ['input surface', role('surface')], ['raised overlay', overlay], ['selection', role('selected')],
    ]) {
      passes(`Text on ${name}`, role('text'), surface, 4.5);
      passes(`Secondary text on ${name}`, role('textSecondary'), surface, 4.5);
      passes(`Large action text on ${name}`, role('action'), surface, 3);
      passes(`Control boundary on ${name}`, role('controlBoundary'), surface, 3);
      passes(`Focus ring on ${name}`, role('focus'), surface, 3);
      passes(`Validation success on ${name}`, resolve('components.status.successText', theme), surface, 4.5);
      passes(`Validation error on ${name}`, role('danger'), surface, 4.5);
    }
    passes('Action label', role('actionText'), role('action'), 4.5);
  });

  test(`${theme}: oldest map symbol outline and fill`, () => {
    const map = tokens.map[theme];
    const forest = composite(rgb(map.forest), rgb(map.land), tokens.map.forestOpacity);
    const selected = composite(rgb(map.selection), forest, 0.2);
    passes('Oldest age fill against its outline', tokens.map.age[theme].at(-1), map.ageOutline, 3);
    for (const [name, surface] of Object.entries({ canvas: map.canvas, land: map.land, forest, selected })) {
      passes(`Age outline against ${name}`, map.ageOutline, surface, 3);
      passes(`Selection halo against ${name}`, map.selection, surface, 3);
    }
    assert.equal(tokens.chart.series[theme].length, 8);
    assert.equal(tokens.map.age[theme].length, tokens.map.age.steps.length);
  });
}

test('layout roles resolve independently of colour theme', () => {
  for (const theme of themes) {
    for (const [mode, control, row] of [['comfortable', 40, 48], ['compact', 32, 36]]) {
      assert.equal(resolve('components.tableRow.height', theme, mode), row);
      assert.equal(resolve('components.filterChip.height', theme, mode), control);
      assert.equal(resolve('components.mapControl.height', theme, mode), control);
    }
    assert.equal(resolve('semantic.focusPx.width', theme), 3);
    assert.equal(resolve('semantic.focusPx.offset', theme), 2);
  }
});

test('the lint rejects raw colours and primitive references in feature code', () => {
  for (const source of [
    'fill: #abc;', 'color: lab(50 30 0);', 'outline: 2px solid rebeccapurple;',
    'class="bg-[white]"', 'var(--primitive-palette-ink)', 'class="text-primitive-palette-ink"',
    "import tokens from '../../../docs/design-system/tokens.json';",
  ]) {
    assert.ok(problem('apps/web/src/feature.ts', source), source);
  }
  for (const source of ['const Color(0xFF000000)', 'Colors.red', 'Color.fromRGBO(0,0,0,0.5)', "'rgba(0,0,0,0)'"]) {
    assert.ok(problem('apps/mobile/lib/feature.dart', source), source);
  }
  assert.equal(problem('apps/web/src/feature.css', 'color: var(--color-focus)'), undefined);
  assert.equal(problem('apps/mobile/lib/feature.dart', 'canvas.drawColor(Colors.white, BlendMode.src); // fixed-colour: signature paper'), undefined);
  assert.ok(problem('apps/mobile/lib/feature.dart', 'canvas.drawColor(Colors.white, BlendMode.src); // fixed-colour:'));
  assert.equal(problem('apps/mobile/lib/feature.dart', 'Colors.transparent'), undefined);
  assert.equal(problem('apps/web/src/tokens.css', '--primitive-palette-ink: #111111'), undefined);
});
