import { readFileSync } from 'node:fs';
import { gzipSync } from 'node:zlib';

const limit = 150_000;
const dist = new URL('../dist/web/', import.meta.url);
const browser = new URL('browser/', dist);
const { outputs } = JSON.parse(readFileSync(new URL('browser-stats.json', dist), 'utf8'));
const html = readFileSync(new URL('index.html', browser));
const chunks = new Set();

function addChunk(path) {
  if (chunks.has(path)) return;
  const chunk = outputs[path];
  if (!chunk) throw new Error(`Initial chunk missing from build stats: ${path}`);
  chunks.add(path);
  for (const dependency of chunk.imports) {
    if (!dependency.external && ['import-statement', 'import-rule'].includes(dependency.kind)) {
      addChunk(dependency.path);
    }
  }
}

// Start with the emitted HTML, then count static imports once per download.
for (const [, path] of html.toString().matchAll(/<(?:script|link)\b[^>]*\b(?:src|href)="([^"]+\.(?:js|css))"/g)) {
  addChunk(path.replace(/^\.?\//, ''));
}
if (!chunks.size) throw new Error('No initial chunks found in the built index.html');

let total = gzipSync(html).length;
for (const path of chunks) total += gzipSync(readFileSync(new URL(path, browser))).length;
console.log(`Initial download (HTML, JavaScript and CSS): ${(total / 1000).toFixed(2)} kB gzip, budget ${limit / 1000} kB`);
if (total > limit) process.exitCode = 1;
