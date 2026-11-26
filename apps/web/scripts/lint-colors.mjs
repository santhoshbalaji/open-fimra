// Check styles, inline templates and map styles in the shipped web and field app code.
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const adapters = new Set([
  'apps/web/src/tokens.css',
  'apps/web/src/token-values.ts',
  'apps/web/src/helm-theme.css',
  'apps/mobile/lib/src/theme/tokens.g.dart',
  'apps/mobile/lib/src/theme/app_theme.dart',
]);
const cssLiteral = /#(?:[a-f\d]{8}|[a-f\d]{6}|[a-f\d]{4}|[a-f\d]{3})\b|\b(?:rgba?|hsl[a]?|hwb|lab|lch|oklab|oklch|color)\s*\(/i;
const dartLiteral = /\bColor\s*\(\s*(?:0x[\da-f]+|\d)|\bColor\.from(?:ARGB|RGBO)?\s*\(|\bColors\.(?!transparent\b)\w+/i;
const primitive = /--primitive-|\b(?:text|bg|border|ring|outline|fill|stroke)-primitive-|\b(?:tokens\s*\.\s*primitives|design-system[\\/]tokens\.json)\b/;
const names = `
aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown
burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan
darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid
darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet
deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro
ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki
lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray
lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue
lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple
mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream
mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod
palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple
red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue
slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white
whitesmoke yellow yellowgreen`.trim().split(/\s+/).join('|');
const namedLiteral = new RegExp(
  `(?:color|background|fill|stroke|border|outline|box-shadow|text-shadow)['"]?\\s*[:=]\\s*['"]?(?:[\\d.]+(?:px|rem|em)? |solid |dashed )*(?:${names})(?![\\w-])` +
  `|(?:text|bg|border|ring|outline|fill|stroke)-\\[(?:${names})\\]`,
  'i',
);

export function problem(file, line) {
  if (adapters.has(file)) return undefined;
  if (primitive.test(line)) return 'Use a semantic or component token instead of a primitive.';
  const raw = file.endsWith('.dart') ? dartLiteral.test(line) || cssLiteral.test(line.replace(/\bColor\s*\(/g, ''))
    : cssLiteral.test(line);
  if (raw || namedLiteral.test(line)) return 'Use a theme token for this colour.';
  return undefined;
}

function check(directory) {
  for (const entry of readdirSync(join(root, directory), { withFileTypes: true })) {
    const file = directory + '/' + entry.name;
    if (entry.isDirectory()) check(file);
    else if (/\.(css|scss|html|ts|tsx|dart)$/.test(file) && !file.endsWith('.spec.ts')) {
      const lines = readFileSync(join(root, file), 'utf8').split('\n');
      lines.forEach((line, index) => {
        const source = file.endsWith('.dart') && /\bColor\s*\(\s*$/.test(line) ? line + (lines[index + 1] ?? '') : line;
        const error = problem(file, source);
        if (!error) return;
        console.error(`${file}:${index + 1} ${error}`);
        process.exitCode = 1;
      });
    }
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  check('apps/web/src');
  check('apps/mobile/lib');
}
