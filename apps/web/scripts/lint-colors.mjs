// Check strings in map styles and inline templates as well as external stylesheets.
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const literal = /#(?:[a-f\d]{8}|[a-f\d]{6}|[a-f\d]{4}|[a-f\d]{3})\b|\b(?:rgba?|hsla?)\s*\(/i;
function check(directory) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const file = join(directory, entry.name);
    if (entry.isDirectory()) check(file);
    else if (/\.(css|html|ts)$/.test(file) && file !== 'src/tokens.css') {
      readFileSync(file, 'utf8').split('\n').forEach((line, index) => {
        if (!literal.test(line)) return;
        console.error(`${file}:${index + 1} Use a theme token for this colour.`);
        process.exitCode = 1;
      });
    }
  }
}
check('src');
