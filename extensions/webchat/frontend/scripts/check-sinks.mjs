// Source gate for the CSP-bound client (script-src 'self', style-src 'self'). It
// reads every scanned file whole, so a sink split over several lines is found.
// Node built-ins only. `npm run check:sinks` runs the self-test first, then the
// scan.
//
// Scope. Scanned: every file under src/ whose name ends in .ts, .tsx, .js, .jsx,
// .mjs, .cjs, .css or .html, and index.html. Excluded, and printed in the output:
//   - files named *.test.<ext> (they assert on these strings);
//   - files under src/**/test-support/ (test fixtures and DOM shims, such as a
//     hostile-URL corpus). A guard fails the scan if any non-excluded file
//     imports from test-support, so this code cannot reach the client.
//
// Rules, each with a fixture in --self-test that it must flag:
//   html-sink          innerHTML, outerHTML, insertAdjacentHTML,
//                      createContextualFragment, setHTMLUnsafe, parseFromString,
//                      dangerouslySetInnerHTML. The DOM is built with
//                      createElement and createTextNode.
//   document-write     document.write, document.writeln
//   srcdoc             a srcdoc attribute or property
//   inline-style       style= (any quoting), style={...}, style: {...}, CSSOM
//                      writes (x.style.y, x.style = ...), cssText. In index.html,
//                      style= with an unquoted value too.
//   element-markup     <style>, <iframe>, <frame>, <object>, <embed>, <base>,
//                      <portal>, the same names passed to createElement, and
//                      <script> outside index.html
//   attribute-write    setAttribute with a name that is not a plain string
//                      literal, or with a style, srcdoc or on* name; and
//                      setAttributeNS, setAttributeNode, setNamedItem
//   script-eval        eval, new Function, a string passed to setTimeout or
//                      setInterval, execScript
//   script-url         javascript:, vbscript:, data:text/html, data:text/javascript
//   target-blank       target="_blank" with no rel containing noopener on the
//                      same element (the text from its '<' to its '>')
//   computed-sink-key  a computed member key built only from string literals
//                      joined with '+' that spells a sink, e.g. el['inner' + 'HTML']
//   index-script-src   index.html: a <script> whose src is missing or empty
//                      (quoted, unquoted or single-quoted), a <script> with a body,
//                      or a <script> that is never closed. data-src is not src.
//   index-attribute    index.html: style=, on*= attributes, <base>, and
//                      <meta http-equiv>
//
// Limit. A computed key that comes from a variable at run time is not analysed.
// Literal keys and keys built from literals are. This gate proves the rules
// above; review covers the rest.
import { readdirSync, readFileSync } from 'node:fs';
import { basename, extname, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const frontendRoot = fileURLToPath(new URL('..', import.meta.url));
const SOURCE_EXTENSIONS = new Set(['.ts', '.tsx', '.js', '.jsx', '.mjs', '.cjs', '.css', '.html']);
const TEST_FILE = /\.test\.\w+$/;
const TEST_SUPPORT_DIR = /(?:^|\/)test-support\//;
const TEST_SUPPORT_IMPORT = /\b(?:from|import)\s*\(?\s*['"][^'"\n]*\btest-support\b[^'"\n]*['"]/;

// A file is excluded when it is a test file or lives in a test-support directory.
// `label` is the path relative to frontend/, with forward slashes.
function isExcluded(label) {
  return TEST_FILE.test(label) || TEST_SUPPORT_DIR.test(label);
}
const STRING_LITERAL = String.raw`'[^'\\\n]*'|"[^"\\\n]*"|` + '`[^`\\\\\\n$]*`';
const SINK_KEYS = new Set([
  'innerHTML', 'outerHTML', 'insertAdjacentHTML', 'createContextualFragment', 'setHTMLUnsafe',
  'parseFromString', 'dangerouslySetInnerHTML', 'write', 'writeln', 'eval', 'Function',
  'setTimeout', 'setInterval', 'execScript', 'setAttribute', 'setAttributeNS',
  'setAttributeNode', 'setNamedItem', 'cssText', 'style', 'srcdoc', 'srcDoc',
]);

// Every rule is find(context, text) and returns hits: { index, detail }. The
// context is { name, shell }: shell is true only for index.html.
function matches(pattern) {
  return (_context, text) => [...text.matchAll(pattern)].map((m) => ({ index: m.index, detail: m[0].trim() }));
}

function findAttributeWrites(text) {
  const hits = [];
  for (const m of text.matchAll(/\b(setAttribute(?:NS)?|setAttributeNode(?:NS)?|setNamedItem)\s*\(/g)) {
    const rest = text.slice(m.index + m[0].length);
    // `name: type` as the first parameter is a method declaration, not a call.
    if (/^\s*[A-Za-z_$][\w$]*\??\s*:/.test(rest)) continue;
    if (m[1] !== 'setAttribute') {
      hits.push({ index: m.index, detail: `${m[1]}()` });
      continue;
    }
    const literal = new RegExp(`^\\s*(['"\`])([^'"\`\\\\\\n$]*)\\1`).exec(rest);
    if (!literal) {
      hits.push({ index: m.index, detail: 'setAttribute() with a name that is not a plain string literal' });
    } else if (/^(?:style|srcdoc|on.*)$/i.test(literal[2].trim())) {
      hits.push({ index: m.index, detail: `setAttribute('${literal[2]}')` });
    }
  }
  return hits;
}

function findTargetBlank(text) {
  const hits = [];
  for (const m of text.matchAll(/\btarget\s*=\s*(?:(["'])_blank\1|\{\s*(["'`])_blank\2\s*\})/g)) {
    const open = text.lastIndexOf('<', m.index);
    const close = text.indexOf('>', m.index + m[0].length);
    const element = open >= 0 && close >= 0 ? text.slice(open, close + 1) : m[0];
    const hasNoopener = /\brel\s*=\s*(?:(["'])[^"']*\bnoopener\b[^"']*\1|\{[^}]*\bnoopener\b[^}]*\})/.test(element);
    if (!hasNoopener) {
      hits.push({ index: m.index, detail: 'target="_blank" without rel="noopener" on the same element' });
    }
  }
  return hits;
}

function findComputedSinkKeys(text) {
  const hits = [];
  const computed = new RegExp(`\\[\\s*((?:${STRING_LITERAL})(?:\\s*\\+\\s*(?:${STRING_LITERAL}))*)\\s*\\]`, 'g');
  for (const m of text.matchAll(computed)) {
    const key = [...m[1].matchAll(/(['"`])([^'"`\\\n]*)\1/g)].map((part) => part[2]).join('');
    if (SINK_KEYS.has(key)) hits.push({ index: m.index, detail: `computed key '${key}'` });
  }
  return hits;
}

function findShellScripts(text) {
  const hits = [];
  const opened = [...text.matchAll(/<\s*script\b/gi)].length;
  let closed = 0;
  for (const m of text.matchAll(/<script\b((?:[^>"']|"[^"]*"|'[^']*')*)>([\s\S]*?)<\/script\s*>/gi)) {
    closed += 1;
    const [, attributes, body] = m;
    const src = /(?<![-\w])src\s*=\s*(?:"([^"]+)"|'([^']+)'|([^\s"'=<>`]+))/i.exec(attributes);
    if (!src) hits.push({ index: m.index, detail: '<script> without a non-empty src (an inline script)' });
    if (body.trim() !== '') hits.push({ index: m.index, detail: '<script> with a body' });
  }
  if (opened !== closed) {
    hits.push({ index: 0, detail: `${opened - closed} <script> element(s) that is not a closed src-only element` });
  }
  return hits;
}

const RULES = [
  { id: 'html-sink', find: matches(/\b(?:innerHTML|outerHTML|insertAdjacentHTML|createContextualFragment|setHTMLUnsafe|parseFromString|dangerouslySetInnerHTML)\b/g) },
  { id: 'document-write', find: matches(/\bdocument\s*\.\s*write(?:ln)?\s*\(/g) },
  { id: 'srcdoc', find: matches(/\bsrcdoc\b/gi) },
  {
    id: 'inline-style',
    find: (context, text) => (context.name.endsWith('.html')
      ? matches(/(?<![-\w])style\s*=/gi)(context, text)
      : matches(/\bstyle\s*=\s*[{"'`]|\bstyle\s*:\s*\{|\.style\s*(?:\.|\[|=(?!=))|\bcssText\b/g)(context, text)),
  },
  {
    id: 'element-markup',
    find: (context, text) => {
      const names = `style|iframe|frame|frameset|object|embed|applet|base|portal${context.shell ? '' : '|script'}`;
      const pattern = new RegExp(`<\\s*(?:${names})\\b|\\bcreateElement(?:NS)?\\s*\\((?:[^,)]*,\\s*)?['"\`](?:${names})['"\`]`, 'gi');
      return matches(pattern)(context, text);
    },
  },
  { id: 'attribute-write', find: (_context, text) => findAttributeWrites(text) },
  { id: 'script-eval', find: matches(/\beval\s*\(|\bnew\s+Function\b|(?<![\w$.])Function\s*\(|\bset(?:Timeout|Interval)\s*\(\s*['"`]|\bexecScript\b/g) },
  { id: 'script-url', find: matches(/\b(?:javascript|vbscript)\s*:|\bdata\s*:\s*text\/(?:html|javascript)/gi) },
  { id: 'target-blank', find: (_context, text) => findTargetBlank(text) },
  { id: 'computed-sink-key', find: (_context, text) => findComputedSinkKeys(text) },
];

const SHELL_RULES = [
  { id: 'index-script-src', find: (_context, text) => findShellScripts(text) },
  {
    id: 'index-attribute',
    find: matches(/(?<![-\w])(?:style|on[a-z]+)\s*=|<\s*base\b|<\s*meta\b[^>]*\bhttp-equiv\b/gi),
  },
];

// Each rule runs on the whole file, so a sink split over several lines is found.
function scanText(text, context) {
  const rules = context.shell ? [...RULES, ...SHELL_RULES] : RULES;
  const findings = [];
  for (const rule of rules) {
    for (const hit of rule.find(context, text)) {
      const line = text.slice(0, hit.index).split('\n').length;
      findings.push({ rule: rule.id, line, detail: hit.detail });
    }
  }
  return findings;
}

// Self-test fixtures. Each violating fixture must be flagged by its named rule;
// each clean fixture must produce no finding at all.
const VIOLATING = [
  { rule: 'html-sink', name: 'bad.tsx', text: "el.innerHTML = '<b>x</b>';" },
  { rule: 'html-sink', name: 'bad.ts', text: "const d = new DOMParser().parseFromString(s, 'text/html');" },
  { rule: 'document-write', name: 'bad.ts', text: "document.write('<p>x</p>');" },
  { rule: 'srcdoc', name: 'bad.tsx', text: 'export const f = <iframe srcDoc={html} />;' },
  { rule: 'inline-style', name: 'bad.tsx', text: "export const p = <p style={{ color: 'red' }}>x</p>;" },
  { rule: 'inline-style', name: 'bad.ts', text: "el.style.color = 'red';" },
  { rule: 'inline-style', name: 'bad.ts', text: "el.cssText = 'color:red';" },
  { rule: 'element-markup', name: 'bad.tsx', text: "const s = '<script>alert(1)</script>';" },
  { rule: 'element-markup', name: 'bad.ts', text: "const f = document.createElement('iframe');" },
  { rule: 'attribute-write', name: 'bad.ts', text: "el.setAttribute('style', 'color:red');" },
  { rule: 'attribute-write', name: 'bad.ts', text: 'el.setAttribute(name, value);' },
  { rule: 'script-eval', name: 'bad.ts', text: 'eval(code);' },
  { rule: 'script-eval', name: 'bad.ts', text: "const f = new Function('return 1');" },
  { rule: 'script-eval', name: 'bad.ts', text: "setTimeout('alert(1)', 0);" },
  { rule: 'script-url', name: 'bad.tsx', text: 'export const a = <a href="javascript:void(0)">x</a>;' },
  { rule: 'target-blank', name: 'bad.tsx', text: 'export const a = <a href={u} target="_blank">x</a>;' },
  { rule: 'computed-sink-key', name: 'bad.ts', text: "el['inner' + 'HTML'] = x;" },
  { rule: 'computed-sink-key', name: 'bad.ts', text: 'document[`write`]("x");' },
  { rule: 'index-script-src', name: 'index.html', shell: true, text: '<script type="module">alert(1)</script>' },
  { rule: 'index-script-src', name: 'index.html', shell: true, text: '<script type="module" data-src="/app/assets/app.js"></script>' },
  { rule: 'index-script-src', name: 'index.html', shell: true, text: '<script type="module" src="/app/assets/app.js">alert(1)</script>' },
  { rule: 'index-script-src', name: 'index.html', shell: true, text: '<script type="module" src="/app/assets/app.js"' },
  { rule: 'index-attribute', name: 'index.html', shell: true, text: '<div style="color:red"></div>' },
  { rule: 'index-attribute', name: 'index.html', shell: true, text: '<p onclick="run()">x</p>' },
  { rule: 'index-attribute', name: 'index.html', shell: true, text: '<base href="https://evil.example/">' },
  { rule: 'element-markup', name: 'index.html', shell: true, text: '<style>p{color:red}</style>' },
  { rule: 'script-url', name: 'index.html', shell: true, text: '<a href="javascript:run()">x</a>' },
];

const CLEAN = [
  {
    name: 'clean.tsx',
    text: [
      "import styles from './App.module.css';",
      '',
      'export function App({ version }: { version: string }) {',
      '  return (',
      '    <main className={styles.shell}>',
      '      <a href="https://example.com/" target="_blank" rel="noopener noreferrer">docs</a>',
      '      <p>build {version}</p>',
      '    </main>',
      '  );',
      '}',
      '',
      "const node = document.createElement('div');",
      "node.setAttribute('aria-label', 'status');",
      "node.appendChild(document.createTextNode('ok'));",
      '',
      'interface Node {',
      '  setAttribute(name: string, value: string): void;',
      '}',
      '',
    ].join('\n'),
  },
  {
    name: 'index.html',
    shell: true,
    text: [
      '<!doctype html>',
      '<html lang="en"><head><meta charset="UTF-8" /><title>x</title>',
      '<script type="module" crossorigin src="/app/assets/app.js"></script>',
      "<script type='module' src=/app/assets/other.js></script>",
      '<link rel="stylesheet" crossorigin href="/app/assets/app.css">',
      '</head><body><div id="root"></div></body></html>',
      '',
    ].join('\n'),
  },
];

function selfTest() {
  const failures = [];
  const ruleIds = [...RULES, ...SHELL_RULES].map((rule) => rule.id);
  for (const id of ruleIds) {
    if (!VIOLATING.some((fixture) => fixture.rule === id)) failures.push(`rule ${id} has no violating fixture`);
  }
  for (const fixture of VIOLATING) {
    const findings = scanText(fixture.text, { name: fixture.name, shell: fixture.shell === true });
    if (!findings.some((finding) => finding.rule === fixture.rule)) {
      failures.push(`${fixture.rule}: not flagged in ${fixture.name}: ${fixture.text}`);
    }
  }
  for (const fixture of CLEAN) {
    const findings = scanText(fixture.text, { name: fixture.name, shell: fixture.shell === true });
    for (const finding of findings) {
      failures.push(`clean fixture ${fixture.name} flagged by ${finding.rule} at line ${finding.line}: ${finding.detail}`);
    }
  }
  const exclusion = [
    ['src/App.test.tsx', true],
    ['src/App.tsx', false],
    ['src/markdown/testing.ts', false],
    ['src/markdown/test-support/corpus.ts', true],
    ['src/markdown/toDom.ts', false],
  ];
  for (const [label, excluded] of exclusion) {
    if (isExcluded(label) !== excluded) failures.push(`exclusion rule wrong for ${label}`);
  }
  const imports = [
    ["import { corpus } from './test-support/corpus';", true],
    ["import { parse } from '../markdown/parse';", false],
  ];
  for (const [text, violating] of imports) {
    if (TEST_SUPPORT_IMPORT.test(text) !== violating) failures.push(`test-support import guard wrong for: ${text}`);
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`check:sinks: self-test: ${failure}`);
    console.error(`check:sinks: self-test FAILED (${failures.length} finding(s))`);
    return 1;
  }
  console.log(
    `check:sinks: self-test OK (${ruleIds.length} rules, ${VIOLATING.length} violating fixtures flagged, ` +
      `${CLEAN.length} clean fixtures pass)`,
  );
  return 0;
}

function collect(directory, files = []) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      collect(path, files);
    } else if (SOURCE_EXTENSIONS.has(extname(entry.name))) {
      files.push(path);
    }
  }
  return files;
}

function scan() {
  const labelOf = (path) => relative(frontendRoot, path).split(sep).join('/');
  const all = collect(join(frontendRoot, 'src')).sort();
  const excluded = all.filter((path) => isExcluded(labelOf(path))).map(labelOf);
  const targets = all
    .filter((path) => !isExcluded(labelOf(path)))
    .map((path) => ({ label: labelOf(path), name: basename(path), shell: false, path }));
  targets.push({ label: 'index.html', name: 'index.html', shell: true, path: join(frontendRoot, 'index.html') });

  const failures = [];
  for (const target of targets) {
    const text = readFileSync(target.path, 'utf8');
    if (TEST_SUPPORT_IMPORT.test(text)) {
      failures.push(`${target.label}: imports test-support code, which is test-only and not scanned`);
    }
    for (const finding of scanText(text, { name: target.name, shell: target.shell })) {
      failures.push(`${target.label}:${finding.line}: ${finding.rule}: ${finding.detail}`);
    }
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`check:sinks: ${failure}`);
    console.error(`check:sinks: FAILED (${failures.length} finding(s))`);
    return 1;
  }
  console.log(
    `check:sinks: OK (${targets.length} files scanned including index.html, 0 findings; ` +
      `excluded: ${excluded.length > 0 ? excluded.join(', ') : 'none'})`,
  );
  return 0;
}

process.exitCode = process.argv.includes('--self-test') ? selfTest() : scan();
