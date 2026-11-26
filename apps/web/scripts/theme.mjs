import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

export const tokens = JSON.parse(readFileSync(new URL('../../../docs/design-system/tokens.json', import.meta.url), 'utf8'));
export const themes = ['light', 'dark'];
const modes = Object.keys(tokens.density).filter(name => name !== 'touchTargetMinPx');
const kebab = name => name.replace(/[A-Z]/g, letter => '-' + letter.toLowerCase());
const entries = (group, values, format = value => value) => Object.entries(values)
  .map(([key, value]) => [`${group}-${kebab(key)}`, format(value, key)]);
const numbered = (group, values) => values.map((value, i) => [`${group}-${i + 1}`, value]);
const variable = name => `var(--${name})`;

export function resolve(path, theme, density = 'comfortable') {
  const parts = path.split('.');
  if (parts[0] === 'map') parts.splice(1, 0, theme);
  if (parts[1] === 'palette') parts.splice(2, 0, theme);
  if (parts[0] === 'density' && parts[1] === 'controlMode') parts[1] = density;
  const value = parts.reduce((obj, key) => obj?.[key], tokens);
  if (value === undefined) throw new Error(`Unknown token: ${path} (${theme})`);
  return typeof value === 'string' && /^(primitives|semantic|density)\./.test(value) ? resolve(value, theme, density) : value;
}

function reference(path) {
  const [group, type, key] = path.split('.');
  if (group === 'map') return variable(`map-${kebab(type)}`);
  if (group === 'density') return variable(`density-${kebab(key)}`);
  const prefix = { color: 'role', palette: 'primitive-palette', status: 'role-status',
    radiusPx: 'role-radius', elevation: 'role-shadow', focusPx: 'focus', motionMs: 'motion' }[type];
  if (!prefix) throw new Error(`Cannot emit a CSS alias for ${path}`);
  const name = `${prefix}-${kebab(key)}`;
  if (group !== 'primitives' || prefix.startsWith('primitive-')) return variable(name);
  return variable(`primitive-${name.replace(/^role-/, '')}`);
}

function rule(selector, declarations) {
  return [`${selector} {`, ...declarations.map(([name, value]) => `  --${name}: ${value};`), '}'];
}

const palette = theme => [
  ...entries('primitive-palette', tokens.primitives.palette[theme]),
  ...entries('map', tokens.map[theme]),
  ...numbered('map-age', tokens.map.age[theme]),
  ...numbered('chart-series', tokens.chart.series[theme]),
];
const px = value => `${value}px`;
const rem = value => `${value / tokens.web.remBasePx}rem`;
const ms = value => `${value}ms`;
const componentEntries = Object.entries(tokens.components).flatMap(([name, values]) =>
  entries(kebab(name), values, reference));
const densityScoped = componentEntries.filter(([, value]) => value.startsWith('var(--density-'));

function css() {
  return [
    '/* Generated from tokens.json by scripts/theme.mjs. */',
    ...rule(':root', [
      ...palette('light'),
      ...entries('primitive-status', tokens.primitives.status),
      ...entries('primitive-radius', tokens.primitives.radiusPx, px),
      ...entries('primitive-focus', tokens.primitives.focusPx, px),
      ...entries('primitive-motion', tokens.primitives.motionMs, ms),
      ...entries('primitive-shadow', tokens.primitives.elevation),
      ...entries('role', tokens.semantic.color, reference),
      ...entries('role-status', tokens.semantic.status, reference),
      ...entries('role-radius', tokens.semantic.radiusPx, reference),
      ...entries('role-shadow', tokens.semantic.elevation, reference),
      ...entries('target', tokens.density.touchTargetMinPx, px),
      ['type-family', tokens.primitives.font.sans],
      ['page-max', rem(tokens.web.pageMaxPx)],
      ...entries('focus', tokens.semantic.focusPx, reference),
      ...entries('motion', tokens.semantic.motionMs, reference),
      ...entries('space', Object.fromEntries(tokens.primitives.spacePx.steps.map(step => [step, step])), px),
      ['map-forest-opacity', tokens.map.forestOpacity],
      ...componentEntries.filter(entry => !densityScoped.includes(entry)),
    ]),
    ...rule(':root[data-theme=dark]', palette('dark')),
    ...modes.flatMap(mode => rule(`${mode === 'comfortable' ? ':root, ' : ''}[data-density=${mode}]`, [
      ...entries('density', tokens.density[mode], px), ...densityScoped,
    ])),
    ...rule('@theme static', [
      ...entries('color', tokens.semantic.color, (_, key) => variable(`role-${kebab(key)}`)),
      ...entries('color', tokens.compatibility.color, reference),
      ...entries('color', tokens.components.status, (_, key) => variable(`status-${kebab(key)}`))
        .filter(([name]) => name !== 'color-success-text'),
      ...entries('color-logo', tokens.logo),
      ...numbered('color-fire-age', tokens.map.age.light.map((_, i) => variable(`map-age-${i + 1}`))),
      ...numbered('color-chart', tokens.chart.series.light.map((_, i) => variable(`chart-series-${i + 1}`))),
      ...entries('shadow', tokens.semantic.elevation, (_, key) => variable(`role-shadow-${kebab(key)}`)).filter(([name]) => !name.endsWith('-dp')),
      ...entries('shadow', tokens.compatibility.shadow, reference),
      ...entries('radius', tokens.semantic.radiusPx, (_, key) => variable(`role-radius-${kebab(key)}`)),
      ['font-sans', variable('type-family')],
      ['font-mono', variable('font-sans')],
      ...Object.entries(tokens.semantic.type).flatMap(([key, path]) => {
        const name = `text-${kebab(key)}`;
        const step = resolve(path, 'light');
        return [
          [name, rem(step.sizePx)],
          [`${name}--line-height`, rem(step.lineHeightPx)],
          [`${name}--font-weight`, step.weight],
          [`${name}--letter-spacing`, `${step.trackingEm}em`],
        ];
      }),
      ['spacing', px(tokens.primitives.spacePx.base)],
      ...entries('spacing-target', tokens.density.touchTargetMinPx, (_, key) => variable(`target-${kebab(key)}`)),
      ['default-transition-duration', variable('motion-control')],
    ]),
    '@media (prefers-reduced-motion: reduce) {',
    ...rule(':root', Object.keys(tokens.semantic.motionMs).filter(name => name !== 'reduced')
      .map(name => [`motion-${kebab(name)}`, variable('motion-reduced')])).map(line => '  ' + line),
    '}',
    '/* Paper is light, so printed pages always use the light colours. */',
    '@media print {',
    ...rule(':root[data-theme=dark]', palette('light')).map(line => '  ' + line),
    '}', '',
  ].join('\n');
}

function values() {
  const byTheme = fn => Object.fromEntries(themes.map(theme => [theme, fn(theme)]));
  const exports = {
    interfaceColours: byTheme(theme => Object.fromEntries(Object.entries(tokens.semantic.color)
      .map(([name, path]) => [name, resolve(path, theme)]))),
    mapColours: byTheme(theme => tokens.map[theme]),
    ageColours: byTheme(theme => tokens.map.age[theme]),
    ageLabels: tokens.map.age.steps,
    forestOpacity: tokens.map.forestOpacity,
    chartSeries: tokens.chart.series,
    typeScale: Object.fromEntries(Object.entries(tokens.semantic.type).map(([name, path]) => [name, resolve(path, 'light')])),
    radii: tokens.primitives.radiusPx,
    density: Object.fromEntries(modes.map(mode => [mode, tokens.density[mode]])),
  };
  return ['// Generated from tokens.json by scripts/theme.mjs.',
    ...Object.entries(exports).map(([name, value]) => `export const ${name} = ${JSON.stringify(value, null, 2)};`), ''].join('\n');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  for (const [name, output] of [['tokens.css', css()], ['token-values.ts', values()]]) {
    const file = new URL(`../src/${name}`, import.meta.url);
    if (process.argv.includes('--check')) {
      if (readFileSync(file, 'utf8') !== output) throw new Error(`Run node apps/web/scripts/theme.mjs to refresh ${name}`);
    } else writeFileSync(file, output);
  }
}
