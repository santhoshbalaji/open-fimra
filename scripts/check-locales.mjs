import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const read = path => JSON.parse(readFileSync(path, 'utf8'));
const web = 'apps/web/public/locales';
const mobile = 'apps/mobile/lib/src/l10n';
const english = read(join(web, 'en.json'));
const hindi = read(join(web, 'hi.json'));

function check(source, target, name) {
  for (const key of Object.keys(source)) {
    if (key.startsWith('@')) continue;
    if (!Object.hasOwn(target, key) || typeof target[key] !== 'string' || !target[key].trim()) {
      throw new Error(`${name} has no translation for ${JSON.stringify(key)}`);
    }
  }
}

check(english, hindi, 'Web Hindi catalog');
const placeholders = message => [...message.matchAll(/\{\d+\}/g)].map(match => match[0]).sort().join();
for (const key of Object.keys(english)) {
  if (placeholders(english[key]) !== placeholders(hindi[key])) {
    throw new Error(`Web Hindi catalog changed the placeholders for ${JSON.stringify(key)}`);
  }
}
check(read(join(mobile, 'app_en.arb')), read(join(mobile, 'app_hi.arb')), 'Field app Hindi catalog');

// The web uses English as the source message. Check literal messages used by the app
// as well as the catalog entries used for labels selected by code or read from the API.
const src = 'apps/web/src';
for (const file of readdirSync(src, { recursive: true })) {
  if (!/\.(html|ts)$/.test(file) || file.endsWith('.spec.ts') || file.startsWith('design-system/')) continue;
  const text = readFileSync(join(src, file), 'utf8');
  const calls = /\b(?:msg|translate)\(\s*'((?:\\.|[^'\\])*)'(?=\s*[,)])/g;
  for (const [, literal] of text.matchAll(calls)) {
    const message = literal.replace(/&quot;/g, '"').replace(/\\'/g, "'").replace(/\\n/g, '\n');
    if (message.trim() && !Object.hasOwn(english, message)) {
      throw new Error(`${file} uses ${JSON.stringify(message)} without an English catalog entry`);
    }
  }
}
console.log('English and Hindi catalogs are complete.');
