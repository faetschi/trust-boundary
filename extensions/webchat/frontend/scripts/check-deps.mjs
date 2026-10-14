// Dependency gate: exact version pins, the ignore-scripts policy, registry-only
// resolution, and no install-time code in the lockfile or in any installed
// package. Node built-ins only. Runs inside `npm run verify`.
//
// "Install script" means npm's definition: preinstall, install or postinstall
// (what the lockfile flag hasInstallScript records). `prepare` is not checked:
// npm runs it for git and local packages, not for registry tarballs.
//
// One platform-gated exception is allowed. fsevents is an optional peer of vite
// (macOS only, os: darwin) and has an install hook. npm does not install it on
// linux or win32, and ignore-scripts=true stops the hook everywhere. The gate
// accepts it only while its lock entry is darwin-only and it is not installed.
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const failures = [];
const installHooks = ['preinstall', 'install', 'postinstall'];
const platformGatedHooks = new Set(['node_modules/fsevents']);

const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const direct = { ...manifest.dependencies, ...manifest.devDependencies };
const exactVersion = /^\d+\.\d+\.\d+$/;
for (const [name, spec] of Object.entries(direct)) {
  if (!exactVersion.test(spec)) {
    failures.push(`package.json: ${name} is not an exact version pin (${spec})`);
  }
}

const npmrc = readFileSync(join(root, '.npmrc'), 'utf8');
if (!/^ignore-scripts=true$/m.test(npmrc)) {
  failures.push('.npmrc: ignore-scripts=true is missing');
}

const lock = JSON.parse(readFileSync(join(root, 'package-lock.json'), 'utf8'));
for (const [name, spec] of Object.entries(direct)) {
  const entry = lock.packages[`node_modules/${name}`];
  if (!entry) {
    failures.push(`package-lock.json: direct dependency ${name} is missing`);
  } else if (entry.version !== spec) {
    failures.push(`package-lock.json: ${name} is ${entry.version}, package.json pins ${spec}`);
  }
}

let installed = 0;
let gated = 0;
for (const [path, meta] of Object.entries(lock.packages)) {
  if (path === '') continue;
  if (!String(meta.resolved ?? '').startsWith('https://registry.npmjs.org/')) {
    failures.push(`package-lock.json: ${path} does not resolve from registry.npmjs.org (${meta.resolved ?? 'no resolved URL'})`);
  }
  const manifestPath = join(root, path, 'package.json');
  const isInstalled = existsSync(manifestPath);
  if (meta.hasInstallScript) {
    const gatedOnly = platformGatedHooks.has(path) && JSON.stringify(meta.os) === '["darwin"]' && !isInstalled;
    if (gatedOnly) {
      gated += 1;
    } else {
      failures.push(`package-lock.json: ${path} has an install script (hasInstallScript)`);
    }
  }
  if (isInstalled) {
    installed += 1;
    const installedManifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    for (const hook of installHooks) {
      if (installedManifest.scripts && installedManifest.scripts[hook] !== undefined) {
        failures.push(`${path}: installed package declares a "${hook}" script`);
      }
    }
  }
}

if (failures.length > 0) {
  for (const failure of failures) console.error(`check:deps: ${failure}`);
  console.error(`check:deps: FAILED (${failures.length} finding(s))`);
  process.exit(1);
}
const total = Object.keys(lock.packages).length - 1;
console.log(
  `check:deps: OK (${Object.keys(direct).length} direct, ${total} locked packages, ` +
    `${installed} installed manifests checked, 0 install scripts installed, ` +
    `${gated} platform-gated hook not installed here (fsevents, darwin-only), exact pins, registry-only)`,
);
