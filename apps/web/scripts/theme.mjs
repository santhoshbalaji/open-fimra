import { readFileSync, writeFileSync } from 'node:fs';

const tokens = JSON.parse(readFileSync('../../docs/design-system/tokens.json', 'utf8'));
const kebab = name => name.replace(/[A-Z]/g, letter => '-' + letter.toLowerCase());
const vars = (group, values, unit = '') => Object.entries(values)
  .filter(([key]) => key !== 'comment')
  .map(([key, value]) => `  --${group}-${kebab(key)}: ${value}${unit};`);
const palette = mode => [
  ...vars('color', tokens.color[mode]),
  ...['fireAge', 'chart'].flatMap(group => tokens.color[group][mode].map((value, i) =>
    `  --color-${kebab(group)}-${i + 1}: ${value};`))
];
const font = tokens.font;
const css = [
  '/* Generated from tokens.json by scripts/theme.mjs. */',
  '@theme static {', ...palette('light'), ...vars('color', tokens.color.status),
  ...vars('color-logo', tokens.color.logo), ...vars('shadow', tokens.shadow),
  ...vars('font', { sans: font.sans, mono: font.mono }), ...vars('text', font.sizePx, 'px'),
  ...Object.keys(font.sizePx).flatMap(key => [
    `  --text-${key}--line-height: ${font.lineHeightPx[key]}px;`,
    `  --text-${key}--font-weight: ${font.weight[key]};`,
    `  --text-${key}--letter-spacing: ${font.trackingEm[key]}em;`
  ]),
  `  --spacing: ${tokens.space.unitPx}px;`, ...vars('radius', tokens.radiusPx, 'px'),
  ...vars('spacing-target', tokens.touchTargetMinPx, 'px'), '}',
  ':root[data-theme=dark] {', ...palette('dark'), '}', ''
].join('\n');
const file = 'src/tokens.css';
if (process.argv.includes('--check')) {
  if (readFileSync(file, 'utf8') !== css) throw new Error('Run node scripts/theme.mjs to refresh tokens.css');
} else writeFileSync(file, css);
