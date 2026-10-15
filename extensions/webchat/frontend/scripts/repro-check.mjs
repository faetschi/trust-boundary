// Reproducibility gate for the committed Vite output (internal/webchat/dist).
// The reference is the dist/ committed at git HEAD. It is read from git's object
// store before any build runs, so `npm run verify && npm run repro` cannot
// compare a build with itself: verify's build overwrites dist/, and that copy is
// never the reference.
//
// Steps: (1) read the committed dist/ from HEAD (git ls-tree and cat-file, so the
// bytes are the stored blobs, not a working-tree checkout); (2) note whether the
// working-tree dist/ differs from HEAD before this run (a note, not a failure);
// (3) build twice from a clean dist/ and compare the two builds; (4) compare the
// rebuild with HEAD. Any difference fails. Node built-ins and git only.
//
// Exit 0: the two builds are identical and the rebuild equals HEAD's dist/.
// Exit 1: HEAD's dist/ cannot be read or is missing, a build fails, the two builds
// differ, or the rebuild differs from HEAD. The rebuild is left in place so the
// difference can be reviewed and committed.
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { join, posix, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const dist = join(root, '..', 'internal', 'webchat', 'dist');
const isWindows = process.platform === 'win32';

const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');

function sorted(map) {
  return new Map([...map].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
}

function fileHashes(directory) {
  const hashes = new Map();
  const visit = (current) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const path = join(current, entry.name);
      if (entry.isDirectory()) {
        visit(path);
      } else if (entry.isFile()) {
        const name = relative(directory, path).split(sep).join('/');
        hashes.set(name, sha256(readFileSync(path)));
      } else {
        throw new Error(`unexpected non-regular entry in dist: ${path}`);
      }
    }
  };
  visit(directory);
  return sorted(hashes);
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

function git(args) {
  const result = spawnSync('git', args, { cwd: root, maxBuffer: 256 * 1024 * 1024 });
  if (result.error || result.status !== 0) {
    const reason = result.error?.message ?? result.stderr.toString('utf8').trim();
    throw new Error(`git ${args.join(' ')} failed: ${reason}`);
  }
  return result.stdout;
}

// The committed dist/ at HEAD: name -> sha256 of the stored blob.
function committedDist() {
  const prefix = git(['rev-parse', '--show-prefix']).toString('utf8').trim();
  const distFromTop = posix.normalize(`${prefix}../internal/webchat/dist`);
  const listing = git(['ls-tree', '-r', '-z', '--full-tree', '--full-name', 'HEAD', '--', distFromTop]).toString('utf8');
  const hashes = new Map();
  for (const record of listing.split('\0')) {
    if (record === '') continue;
    const tab = record.indexOf('\t');
    const [mode, type, blob] = record.slice(0, tab).split(' ');
    const path = record.slice(tab + 1);
    if (type !== 'blob' || (mode !== '100644' && mode !== '100755')) {
      throw new Error(`committed ${path} is not a regular file (mode ${mode}, type ${type})`);
    }
    hashes.set(path.slice(distFromTop.length + 1), sha256(git(['cat-file', 'blob', blob])));
  }
  return hashes.size > 0 ? sorted(hashes) : null;
}

function build(label) {
  rmSync(dist, { recursive: true, force: true });
  const result = spawnSync('npm', ['run', 'build'], { cwd: root, stdio: 'inherit', shell: isWindows });
  if (result.status !== 0) {
    console.error(`repro-check: ${label} build failed (exit ${result.status})`);
    process.exit(1);
  }
  return fileHashes(dist);
}

let committed;
let head;
try {
  committed = committedDist();
  head = git(['rev-parse', '--short', 'HEAD']).toString('utf8').trim();
} catch (error) {
  console.error(`repro-check: cannot read the committed dist/ from git HEAD: ${error.message}`);
  console.error('repro-check: FAILED (if git cannot read this checkout, for example a WSL worktree of a Windows checkout, set GIT_DIR and GIT_WORK_TREE)');
  process.exit(1);
}
if (!committed) {
  console.error('repro-check: no dist/ is committed at HEAD; commit the build before this gate can pass');
  console.error('repro-check: FAILED');
  process.exit(1);
}

if (existsSync(dist)) {
  const working = fileHashes(dist);
  const drift = differences(committed, working);
  if (drift.length > 0) {
    console.log(`repro-check: note: working-tree dist/ differs from HEAD before this run (${drift.join(', ')})`);
  } else {
    console.log('repro-check: note: working-tree dist/ matches HEAD before this run');
  }
}

const first = build('first');
const second = build('second');

const failures = [];
const buildDrift = differences(first, second);
if (buildDrift.length > 0) {
  failures.push(`two clean builds differ: ${buildDrift.join(', ')}`);
}
const rebuildDrift = differences(committed, first);
if (rebuildDrift.length > 0) {
  failures.push(`the clean rebuild differs from the committed dist/ at HEAD ${head}: ${rebuildDrift.join(', ')}`);
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`repro-check: ${failure}`);
  console.error('repro-check: FAILED');
  process.exit(1);
}
console.log(
  `repro-check: builds identical (${first.size} files, tree sha256 ${treeDigest(first)}); ` +
    `the rebuild equals the committed dist/ at HEAD ${head}`,
);
