// Reproducibility gate for the committed Vite output (internal/webchat/dist).
// It builds from a clean dist/ twice and compares per-file SHA-256 maps. It then
// compares the rebuild with the dist/ that was committed before this run. The
// comparison is the pre-commit equivalent of `git diff --exit-code`. Node
// built-ins only.
//
// Exit 0: both builds are identical and match the committed copy (if any).
// Exit 1: a build differs, or the rebuild differs from the committed copy. The
// fresh build is left in place so the difference can be reviewed.
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const dist = join(root, '..', 'internal', 'webchat', 'dist');
const isWindows = process.platform === 'win32';

function fileHashes(directory) {
  const hashes = new Map();
  const visit = (current) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const path = join(current, entry.name);
      if (entry.isDirectory()) {
        visit(path);
      } else if (entry.isFile()) {
        const name = relative(directory, path).split(sep).join('/');
        hashes.set(name, createHash('sha256').update(readFileSync(path)).digest('hex'));
      } else {
        throw new Error(`unexpected non-regular entry in dist: ${path}`);
      }
    }
  };
  visit(directory);
  return new Map([...hashes].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
}

function treeDigest(hashes) {
  const hash = createHash('sha256');
  for (const [name, digest] of hashes) hash.update(`${name}\t${digest}\n`);
  return hash.digest('hex');
}

function differences(left, right) {
  const names = new Set([...left.keys(), ...right.keys()]);
  return [...names].sort().filter((name) => left.get(name) !== right.get(name));
}

function build(label) {
  rmSync(dist, { recursive: true, force: true });
  const result = spawnSync('npm', ['run', 'build'], {
    cwd: root,
    stdio: 'inherit',
    shell: isWindows,
  });
  if (result.status !== 0) {
    console.error(`repro-check: ${label} build failed (exit ${result.status})`);
    process.exit(1);
  }
  return fileHashes(dist);
}

const committed = existsSync(dist) ? fileHashes(dist) : null;
const first = build('first');
const second = build('second');

const failures = [];
const buildDrift = differences(first, second);
if (buildDrift.length > 0) {
  failures.push(`two clean builds differ: ${buildDrift.join(', ')}`);
}
if (committed) {
  const committedDrift = differences(committed, first);
  if (committedDrift.length > 0) {
    failures.push(`committed dist differs from a clean rebuild: ${committedDrift.join(', ')}`);
  }
} else {
  console.log('repro-check: no committed dist/ to compare against (first generation)');
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`repro-check: ${failure}`);
  console.error('repro-check: FAILED');
  process.exit(1);
}
console.log(`repro-check: builds identical (${first.size} files, tree sha256 ${treeDigest(first)})`);
if (committed) {
  console.log(`repro-check: committed dist/ identical to clean rebuild (tree sha256 ${treeDigest(committed)})`);
}
