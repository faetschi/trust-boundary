// Opt-in browser CSP smoke for the web chat gateway. It is NOT part of `go test`,
// `make test` or any core gate. Node built-ins only; Node >= 22 for the global
// WebSocket. It drives headless Chrome/Chromium (or Edge) over the DevTools
// protocol and checks two pages: /app/ (React) and / (vanilla UI).
//
// For each page it asserts:
//   - zero Content-Security-Policy violations (securitypolicyviolation events and
//     security-source console entries),
//   - zero console errors and uncaught exceptions, except the favicon.ico 404,
//   - no element has a style attribute and no element has an on* attribute,
//   - for /app/, a CSS Modules class is applied: the banner's computed background
//     differs from a default probe element, and a stylesheet is linked.
//
// Usage, from extensions/webchat/:
//   node scripts/csp-smoke.mjs
//       builds the gateway with the pinned Go toolchain and starts it with
//       --demo on a free loopback port (never 8791)
//   node scripts/csp-smoke.mjs --url http://127.0.0.1:PORT
//       checks a demo gateway that is already running (must be 127.0.0.1)
//   --chrome PATH, or CHROME_BIN, selects the browser. Otherwise the common
//   Chrome, Chromium and Edge install paths and PATH are searched.
//
// Exit codes: 0 PASS; 1 a check failed; 2 usage error; 3 NOT VERIFIED: no
// browser; 4 NOT VERIFIED: gateway or DevTools connection unavailable.
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const CODE = { pass: 0, fail: 1, usage: 2, noBrowser: 3, notVerified: 4 };
const STALE_PORT = 8791;
const webchatRoot = fileURLToPath(new URL('..', import.meta.url));
const isWindows = process.platform === 'win32';
const exe = (name) => (isWindows ? `${name}.exe` : name);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

class Stop extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

const TARGETS = [
  { path: '/app/', label: '/app/ (React)', react: true },
  { path: '/', label: '/ (vanilla)', react: false },
];

// Installed before any page script runs. It only listens, so it cannot change
// the behaviour that is being measured.
const CSP_HOOK = `(() => {
  window.__tboundCsp = [];
  document.addEventListener('securitypolicyviolation', (event) => {
    window.__tboundCsp.push({ directive: event.violatedDirective, blocked: event.blockedURI });
  }, true);
})();`;

const AUDIT = `(() => {
  const all = Array.from(document.querySelectorAll('*'));
  const banner = document.querySelector('[data-scaffold="banner"]');
  let scaffold = null;
  if (banner) {
    const probe = document.createElement('section');
    document.body.appendChild(probe);
    const defaultBackground = getComputedStyle(probe).backgroundColor;
    probe.remove();
    scaffold = {
      className: banner.getAttribute('class') || '',
      background: getComputedStyle(banner).backgroundColor,
      defaultBackground,
      text: banner.textContent || '',
    };
  }
  return {
    title: document.title,
    elements: all.length,
    inlineStyles: all.filter((el) => el.hasAttribute('style')).length,
    eventAttributes: all.reduce((n, el) => n + Array.from(el.attributes).filter((a) => a.name.toLowerCase().startsWith('on')).length, 0),
    cspCaptured: (window.__tboundCsp || []).length,
    stylesheetLinks: document.querySelectorAll('link[rel="stylesheet"]').length,
    scaffold,
  };
})()`;

function parseArgs(argv) {
  const options = { url: undefined, chrome: undefined };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--url' || arg === '--chrome') {
      const value = argv[i + 1];
      if (value === undefined || value.startsWith('--')) {
        throw new Stop(CODE.usage, `${arg} needs a value`);
      }
      options[arg.slice(2)] = value;
      i += 1;
    } else {
      throw new Stop(CODE.usage, `unknown argument ${arg}`);
    }
  }
  return options;
}

function browserCandidates(explicit) {
  if (explicit) return [explicit];
  const candidates = [];
  if (process.env.CHROME_BIN) candidates.push(process.env.CHROME_BIN);
  if (isWindows) {
    for (const base of [process.env.PROGRAMFILES, process.env['PROGRAMFILES(X86)'], process.env.LOCALAPPDATA]) {
      if (!base) continue;
      candidates.push(join(base, 'Google', 'Chrome', 'Application', 'chrome.exe'));
      candidates.push(join(base, 'Microsoft', 'Edge', 'Application', 'msedge.exe'));
    }
  } else if (process.platform === 'darwin') {
    candidates.push(
      '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
      '/Applications/Chromium.app/Contents/MacOS/Chromium',
      '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
    );
  } else {
    candidates.push(
      '/usr/bin/google-chrome',
      '/usr/bin/google-chrome-stable',
      '/usr/bin/chromium',
      '/usr/bin/chromium-browser',
      '/snap/bin/chromium',
    );
  }
  for (const dir of (process.env.PATH ?? '').split(delimiter).filter(Boolean)) {
    for (const name of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser', 'chrome', 'msedge']) {
      candidates.push(join(dir, exe(name)));
    }
  }
  return candidates;
}

function findBrowser(explicit) {
  return browserCandidates(explicit).find((candidate) => existsSync(candidate)) ?? null;
}

function freePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => {
        if (port === STALE_PORT) {
          freePort().then(resolve, reject);
        } else {
          resolve(port);
        }
      });
    });
  });
}

async function poll(check, timeoutMs, intervalMs = 200) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const value = await check();
      if (value) return value;
    } catch {
      // not ready yet
    }
    await sleep(intervalMs);
  }
  return null;
}

async function getJson(url) {
  const response = await fetch(url, { signal: AbortSignal.timeout(2000) });
  if (!response.ok) throw new Error(`${url} answered ${response.status}`);
  return response.json();
}

function killTree(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  if (isWindows) {
    spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' });
  } else {
    child.kill('SIGKILL');
  }
}

async function startGateway(cleanups) {
  const dir = mkdtempSync(join(tmpdir(), 'tbound-csp-bin-'));
  cleanups.push(() => rmSync(dir, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }));
  const binary = join(dir, exe('tbound-webchat'));
  execFileSync('go', ['build', '-mod=readonly', '-o', binary, './cmd/tbound-webchat'], {
    cwd: webchatRoot,
    stdio: 'inherit',
  });
  const port = await freePort();
  const child = spawn(binary, ['--demo', '--addr', `127.0.0.1:${port}`], { stdio: ['ignore', 'ignore', 'ignore'] });
  cleanups.push(() => killTree(child));
  const url = `http://127.0.0.1:${port}`;
  const ready = await poll(() => getJson(`${url}/api/v1/contract`), 30000, 250);
  if (!ready) throw new Stop(CODE.notVerified, `demo gateway did not answer on ${url}`);
  return url;
}

async function launchBrowser(binary, cleanups) {
  const profile = mkdtempSync(join(tmpdir(), 'tbound-csp-profile-'));
  cleanups.push(() => rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 300 }));
  const port = await freePort();
  const args = [
    '--headless=new',
    '--disable-gpu',
    '--disable-extensions',
    '--disable-background-networking',
    '--disable-sync',
    '--no-first-run',
    '--no-default-browser-check',
    '--remote-debugging-address=127.0.0.1',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
  ];
  if (process.platform === 'linux' && process.getuid?.() === 0) args.push('--no-sandbox');
  args.push('about:blank');
  const child = spawn(binary, args, { stdio: 'ignore' });
  cleanups.push(() => killTree(child));
  const base = `http://127.0.0.1:${port}`;
  const version = await poll(() => getJson(`${base}/json/version`), 25000, 300);
  if (!version) throw new Stop(CODE.notVerified, `browser at ${binary} did not open a DevTools endpoint`);
  return { base, version };
}

async function pageSocketUrl(base) {
  const targets = await getJson(`${base}/json/list`);
  const page = targets.find((target) => target.type === 'page' && target.webSocketDebuggerUrl);
  if (page) return page.webSocketDebuggerUrl;
  const created = await fetch(`${base}/json/new?about:blank`, { method: 'PUT' }).then((r) => r.json());
  return created.webSocketDebuggerUrl;
}

class Cdp {
  constructor(url) {
    this.socket = new WebSocket(url);
    this.nextId = 1;
    this.pending = new Map();
    this.handlers = new Map();
  }

  open() {
    return new Promise((resolve, reject) => {
      this.socket.addEventListener('open', () => resolve(), { once: true });
      this.socket.addEventListener('error', () => reject(new Error('DevTools socket error')), { once: true });
      this.socket.addEventListener('message', (event) => this.dispatch(event.data));
    });
  }

  dispatch(raw) {
    const message = JSON.parse(String(raw));
    if (message.id !== undefined) {
      const waiter = this.pending.get(message.id);
      if (!waiter) return;
      this.pending.delete(message.id);
      if (message.error) waiter.reject(new Error(`${waiter.method}: ${message.error.message}`));
      else waiter.resolve(message.result);
      return;
    }
    for (const handler of this.handlers.get(message.method) ?? []) handler(message.params ?? {});
  }

  send(method, params = {}) {
    const id = this.nextId;
    this.nextId += 1;
    this.socket.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject, method }));
  }

  on(method, handler) {
    this.handlers.set(method, [...(this.handlers.get(method) ?? []), handler]);
  }

  close() {
    this.socket.close();
  }
}

function isAllowedFavicon404(entry) {
  return entry.source === 'network' && /\/favicon\.ico$/.test(entry.url ?? '') && /status of 404/.test(entry.text ?? '');
}

function waitForEvent(cdp, method, timeoutMs) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`timed out waiting for ${method}`)), timeoutMs);
    cdp.on(method, () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

async function auditPages(base, gatewayUrl) {
  const cdp = new Cdp(await pageSocketUrl(base));
  await cdp.open();

  const state = { csp: [], errors: [], exceptions: [], favicon404: 0 };
  cdp.on('Log.entryAdded', ({ entry }) => {
    if (entry.level !== 'error') return;
    if (entry.source === 'security') state.csp.push(entry.text);
    else if (isAllowedFavicon404(entry)) state.favicon404 += 1;
    else state.errors.push(`${entry.source}: ${entry.text} ${entry.url ?? ''}`.trim());
  });
  cdp.on('Runtime.consoleAPICalled', ({ type, args }) => {
    if (type === 'error') state.errors.push(`console.error: ${(args ?? []).map((a) => a.value ?? a.description ?? '').join(' ')}`);
  });
  cdp.on('Runtime.exceptionThrown', ({ exceptionDetails }) => {
    state.exceptions.push(exceptionDetails?.exception?.description ?? exceptionDetails?.text ?? 'exception');
  });

  for (const method of ['Page.enable', 'Runtime.enable', 'Log.enable']) await cdp.send(method);
  await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: CSP_HOOK });

  const results = [];
  for (const target of TARGETS) {
    state.csp = [];
    state.errors = [];
    state.exceptions = [];
    state.favicon404 = 0;
    const url = `${gatewayUrl}${target.path}`;
    const loaded = waitForEvent(cdp, 'Page.loadEventFired', 20000);
    const navigation = await cdp.send('Page.navigate', { url });
    if (navigation.errorText) throw new Stop(CODE.notVerified, `navigation to ${url} failed: ${navigation.errorText}`);
    await loaded;
    if (target.react) {
      await poll(async () => (await evaluate(cdp, 'Boolean(document.querySelector("[data-scaffold]"))')) === true, 10000);
    } else {
      await sleep(1500);
    }
    await sleep(500);
    const audit = await evaluate(cdp, AUDIT);
    results.push({ target, audit, state: { ...state, csp: [...state.csp], errors: [...state.errors], exceptions: [...state.exceptions] } });
  }
  cdp.close();
  return results;
}

async function evaluate(cdp, expression) {
  const result = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (result.exceptionDetails) throw new Error(`page evaluation failed: ${result.exceptionDetails.text}`);
  return result.result.value;
}

function judge({ target, audit, state }) {
  const failures = [];
  if (state.csp.length > 0) failures.push(`${state.csp.length} CSP violation log entr(ies): ${state.csp.join(' | ')}`);
  if (audit.cspCaptured > 0) failures.push(`${audit.cspCaptured} securitypolicyviolation event(s) captured`);
  if (state.errors.length > 0) failures.push(`console errors: ${state.errors.join(' | ')}`);
  if (state.exceptions.length > 0) failures.push(`uncaught exceptions: ${state.exceptions.join(' | ')}`);
  if (audit.inlineStyles > 0) failures.push(`${audit.inlineStyles} element(s) with a style attribute`);
  if (audit.eventAttributes > 0) failures.push(`${audit.eventAttributes} on* attribute(s)`);
  if (audit.elements < 2) failures.push(`page rendered ${audit.elements} element(s)`);
  if (target.react) {
    if (!audit.scaffold) {
      failures.push('no [data-scaffold="banner"] element on /app/');
    } else {
      if (audit.scaffold.className === '') failures.push('banner has no class (CSS Modules not applied)');
      if (audit.scaffold.background === audit.scaffold.defaultBackground) {
        failures.push(`banner background ${audit.scaffold.background} equals the default: CSS Module not applied`);
      }
      if (!audit.scaffold.text.includes('React scaffold (Phase 1, not the product UI)')) {
        failures.push('banner text is not the Phase 1 scaffold heading');
      }
    }
    if (audit.stylesheetLinks < 1) failures.push('no stylesheet link on /app/');
  }
  return failures;
}

function summarise(result) {
  const { target, audit, state } = result;
  const parts = [
    `${state.csp.length + audit.cspCaptured} CSP violations`,
    `${state.errors.length + state.exceptions.length} console errors/exceptions (favicon 404 allowed: ${state.favicon404})`,
    `${audit.inlineStyles} style attributes`,
    `${audit.eventAttributes} on* attributes`,
  ];
  if (target.react && audit.scaffold) {
    parts.push(`CSS Module class "${audit.scaffold.className}" applied (background ${audit.scaffold.background}, default ${audit.scaffold.defaultBackground})`);
  }
  return parts.join('; ');
}

async function main(argv) {
  const cleanups = [];
  try {
    const options = parseArgs(argv);
    let gatewayUrl = options.url;
    if (gatewayUrl) {
      const parsed = new URL(gatewayUrl);
      if (parsed.hostname !== '127.0.0.1') {
        throw new Stop(CODE.usage, '--url must be http://127.0.0.1:<port> (the gateway binds a literal loopback address)');
      }
      if (Number(parsed.port) === STALE_PORT) {
        throw new Stop(CODE.usage, `refusing port ${STALE_PORT}: that is a stale old build; start a demo gateway on a fresh port`);
      }
    }
    if (typeof WebSocket !== 'function') {
      throw new Stop(CODE.notVerified, 'this Node has no global WebSocket; Node >= 22 is required');
    }
    const binary = findBrowser(options.chrome ?? null);
    if (!binary) {
      console.error('NOT VERIFIED: no browser (set CHROME_BIN or pass --chrome to a Chrome or Chromium binary)');
      return CODE.noBrowser;
    }
    if (!gatewayUrl) {
      gatewayUrl = await startGateway(cleanups);
    } else if (!(await poll(() => getJson(`${gatewayUrl}/api/v1/contract`), 3000, 250))) {
      throw new Stop(CODE.notVerified, `no gateway answered at ${gatewayUrl}`);
    }

    const browser = await launchBrowser(binary, cleanups);
    console.log(`csp-smoke: browser ${browser.version.Browser} (${binary})`);
    console.log(`csp-smoke: gateway ${gatewayUrl} (demo fixture, read-only)`);
    const results = await auditPages(browser.base, gatewayUrl);

    let failed = 0;
    for (const result of results) {
      const failures = judge(result);
      if (failures.length === 0) {
        console.log(`PASS ${result.target.label}: ${summarise(result)}`);
      } else {
        failed += 1;
        console.log(`FAIL ${result.target.label}:`);
        for (const failure of failures) console.log(`  - ${failure}`);
      }
    }
    if (failed > 0) {
      console.log(`csp-smoke: FAILED (${failed} of ${results.length} page(s))`);
      return CODE.fail;
    }
    console.log(`csp-smoke: PASS (${results.length} pages, ${browser.version.Browser})`);
    return CODE.pass;
  } catch (error) {
    if (error instanceof Stop) {
      if (error.code === CODE.usage) console.error(`csp-smoke: ${error.message}`);
      else console.error(`NOT VERIFIED: ${error.message}`);
      return error.code;
    }
    console.error(`NOT VERIFIED: ${error instanceof Error ? error.message : String(error)}`);
    return CODE.notVerified;
  } finally {
    for (const cleanup of cleanups) {
      try {
        await cleanup();
      } catch {
        // best effort: a locked profile is reported by the OS, not here
      }
    }
  }
}

process.exitCode = await main(process.argv.slice(2));
