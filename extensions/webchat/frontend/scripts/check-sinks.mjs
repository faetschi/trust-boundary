// Source gate for the CSP-bound client: no HTML-injection sinks and no inline
// style props or inline script/style elements in the app source. Node built-ins
// only. Test files are excluded because they assert on these strings.
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const frontendRoot = fileURLToPath(new URL('..', import.meta.url));

const forbiddenLines = [
  /dangerouslySetInnerHTML/,
  /innerHTML/,
  /outerHTML/,
  /insertAdjacentHTML/,
  /document\.write/,
  /style=\{/,
  /\bstyle=["']/,
  /<style\b/i,
  /\bnew Function\b|\beval\(/,
];

function collect(directory, files) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      collect(path, files);
    } else if (/\.(ts|tsx|css)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      files.push(path);
    }
  }
  return files;
}

const files = collect(join(frontendRoot, 'src'), []);
const html = join(frontendRoot, 'index.html');
const failures = [];

for (const file of files) {
  readFileSync(file, 'utf8')
    .split('\n')
    .forEach((line, index) => {
      for (const pattern of forbiddenLines) {
        if (pattern.test(line)) {
          failures.push(`${relative(frontendRoot, file)}:${index + 1}: forbidden pattern ${pattern}`);
        }
      }
    });
}

// index.html: every <script> must be an external module with a src; no <style>
// and no style attributes.
const shell = readFileSync(html, 'utf8');
if (/<script\b(?![^>]*\bsrc=)/i.test(shell)) {
  failures.push('index.html: inline <script> (every script must have src)');
}
if (/<style\b/i.test(shell) || /\sstyle=/i.test(shell)) {
  failures.push('index.html: inline <style> or style= attribute');
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`check:sinks: ${failure}`);
  console.error(`check:sinks: FAILED (${failures.length} finding(s))`);
  process.exit(1);
}
console.log(`check:sinks: OK (${files.length} source files and index.html scanned, 0 sinks)`);
