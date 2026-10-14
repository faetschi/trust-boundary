// Opt-in browser CSP smoke for the web chat gateway. It is NOT part of `go test`,
// `make test` or any core gate. Node built-ins only; Node >= 22 for the global
// WebSocket. It drives headless Chrome, Chromium or Edge over the DevTools protocol.
//
// Each run, in order:
//   1. Observer self-test (the negative control). This script serves a canary
//      page with the gateway's exact Content-Security-Policy. The page has an
//      inline <script> and an inline style attribute, which that policy must
//      refuse. The browser must report at least one CSP violation for it. If it
//      reports none, the observer is blind: the run stops with exit 5 before any
//      gateway page is judged.
//   2. Header probes (plain HTTP). For /, /api/v1/contract, /app/, a missing /app/
//      asset (404), POST /app/ (405), a foreign Host (403) and a foreign Origin
//      (403), the Content-Security-Policy header must equal the policy exactly. The
//      policy is read from contentSecurityPolicy in internal/webchat/handler.go, the
//      single source; app_test.go pins that constant to the shipped string.
//   3. Browser audit of /app/ (React) and / (vanilla UI). For each page, the Document
//      response's Content-Security-Policy as the browser received it must equal the
//      policy. There must be zero CSP violations, zero console errors and zero
//      uncaught exceptions, except one favicon.ico 404; zero style attributes; and
//      zero on* attributes. /app/ must render its banner with a CSS Modules class
//      whose computed background differs from a default probe, and a linked
//      stylesheet. / must have its title, the claim strip, the chat view, and the
//      chat and trajectory tabs.
//
// Usage, from extensions/webchat/:
//   node scripts/csp-smoke.mjs
//       builds the gateway with `go` from PATH, which must be go1.27.x (the pinned
//       toolchain; its version line is printed), and starts it with --demo on a free
//       loopback port (never 8791)
//   node scripts/csp-smoke.mjs --url http://127.0.0.1:PORT
//       checks a demo gateway that is already running (it must be 127.0.0.1); no build
//   --chrome PATH, or CHROME_BIN, selects the browser. Otherwise the usual Chrome,
//   Chromium and Edge install paths and PATH are searched.
//
// Exit codes: 0 PASS. 1 FAIL: a check failed. 2 usage error. 3 NOT VERIFIED: no
// browser. 4 NOT VERIFIED: gateway, toolchain or DevTools unavailable or timed out.
// 5 FAIL: the observer is blind (the canary produced no CSP violation).
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { createServer as createHttpServer, request as httpRequest } from 'node:http';
import { createServer as createNetServer } from 'node:net';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const CODE = { pass: 0, fail: 1, usage: 2, noBrowser: 3, notVerified: 4, blind: 5 };
const STALE_PORT = 8791;
const webchatRoot = fileURLToPath(new URL('..', import.meta.url));
const HANDLER_SOURCE = join(webchatRoot, 'internal', 'webchat', 'handler.go');
const isWindows = process.platform === 'win32';
const exe = (name) => (isWindows ? `${name}.exe` : name);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const APP_TITLE = 'tbound web chat';
const VANILLA_TITLE = 'tbound · Pi chat workspace';

class Stop extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

// Every cleanup runs once, in reverse order, on normal exit, on a failure, and on
// SIGINT, SIGTERM or SIGHUP. Each cleanup is synchronous, so the 'exit' event can
// run them too.
const cleanups = [];
function runCleanups() {
  while (cleanups.length > 0) {
    try {
      cleanups.pop()();
    } catch (error) {
      console.error(`csp-smoke: cleanup failed: ${error.message}`);
    }
  }
}
process.on('exit', runCleanups);
for (const [signal, code] of [['SIGINT', 130], ['SIGTERM', 143], ['SIGHUP', 129]]) {
  process.once(signal, () => process.exit(code));
}

const GATEWAY_TARGETS = [
  { kind: 'app', path: '/app/', label: '/app/ (React)' },
  { kind: 'vanilla', path: '/', label: '/ (vanilla)' },
];

// Plain HTTP probes. Host and Origin are set by hand, so the gateway's refusals
// are reached and checked, not just the 200 responses.
const PROBES = [
  { label: 'GET /', path: '/', status: 200 },
  { label: 'GET /api/v1/contract', path: '/api/v1/contract', status: 200 },
  { label: 'GET /app/', path: '/app/', status: 200 },
  { label: 'GET /app/assets/missing.js', path: '/app/assets/missing.js', status: 404 },
  { label: 'POST /app/', method: 'POST', path: '/app/', status: 405 },
  { label: 'GET / with a foreign Host', path: '/', headers: { host: 'evil.example' }, status: 403 },
  { label: 'GET /app/ with a foreign Origin', path: '/app/', headers: { origin: 'http://evil.example' }, status: 403 },
];

// Installed before any page script runs. It only listens, so it cannot change
// the behaviour that is being measured.
const CSP_HOOK = `(() => {
  window.__tboundCsp = [];
  document.addEventListener('securitypolicyviolation', (event) => {
    window.__tboundCsp.push({ directive: event.violatedDirective, blocked: event.blockedURI });
  }, true);
})();`;

// The negative control. Both elements must be refused by the gateway's policy
// (script-src 'self' and style-src 'self', with no 'unsafe-inline').
const CANARY_PAGE = [
  '<!doctype html>',
  '<html lang="en"><head><meta charset="utf-8"><title>csp canary</title></head><body>',
  '<p style="color: rgb(1, 2, 3)">inline style attribute: style-src must refuse it</p>',
  '<script>window.__canaryRan = true;</script>',
  '</body></html>',
].join('\n');

const AUDIT = `(() => {
  const all = Array.from(document.querySelectorAll('*'));
  const banner = document.querySelector('header[aria-label="Workspace header"]');
  let scaffold = null;
  if (banner) {
    const probe = document.createElement('header');
    document.body.appendChild(probe);
    const defaultBackground = getComputedStyle(probe).borderBottomWidth;
    probe.remove();
    scaffold = {
      className: banner.getAttribute('class') || '',
      background: getComputedStyle(banner).borderBottomWidth,
      defaultBackground,
      text: banner.textContent || '',
    };
  }
  const tabs = ['tab-chat', 'tab-trajectory'].map((id) => {
    const element = document.getElementById(id);
    return element ? { id, role: element.getAttribute('role') } : null;
  });
  return {
    title: document.title,
    elements: all.length,
    inlineStyles: all.filter((el) => el.hasAttribute('style')).length,
    eventAttributes: all.reduce((n, el) => n + Array.from(el.attributes).filter((a) => a.name.toLowerCase().startsWith('on')).length, 0),
    cspCaptured: (window.__tboundCsp || []).length,
    stylesheetLinks: document.querySelectorAll('link[rel="stylesheet"]').length,
    scaffold,
    vanilla: {
      claimStrip: Boolean(document.getElementById('claim-strip')),
      chatView: Boolean(document.getElementById('chat-view')),
      tabs,
    },
  };
})()`;

// The one policy every response must carry. It is read from handler.go, so the
// smoke cannot drift from the gateway by a copied string.
function expectedPolicy() {
  let source;
  try {
    source = readFileSync(HANDLER_SOURCE, 'utf8');
  } catch (error) {
    throw new Stop(CODE.notVerified, `cannot read ${HANDLER_SOURCE}: ${error.message}`);
  }
  const match = /^const contentSecurityPolicy = "([^"\n]+)"$/m.exec(source);
  if (!match) throw new Stop(CODE.notVerified, 'cannot find contentSecurityPolicy in internal/webchat/handler.go');
  for (const directive of ["default-src 'none'", "script-src 'self'", "style-src 'self'"]) {
    if (!match[1].includes(directive)) throw new Stop(CODE.notVerified, `the policy in handler.go lacks "${directive}"`);
  }
  return match[1];
}

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
    const server = createNetServer();
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

// The cleanup path is synchronous, because it also runs from the 'exit' event.
function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

// Waits until a process id is gone. On Windows the launcher can exit while its
// browser child still runs, so `child.exitCode` alone does not prove the tree is down.
function waitForExit(pid, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0);
    } catch {
      return;
    }
    sleepSync(100);
  }
}

// Kills the child's tree. It always runs: taskkill and the process-group kill are
// harmless when the process has already gone.
function killTree(child) {
  if (!child) return;
  if (isWindows) {
    spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' });
  } else {
    try {
      process.kill(-child.pid, 'SIGKILL');
    } catch {
      // the group is already gone
    }
  }
  waitForExit(child.pid, 15000);
}

// Windows keeps a browser's files locked for a moment after taskkill, and an orphaned
// browser process can outlive its launcher. This kills whatever still names the
// profile directory. The profile is passed in the environment, so this PowerShell
// command line does not match itself.
function killProfileProcesses(profile) {
  spawnSync(
    'powershell',
    [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      'Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($env:TBOUND_CSP_PROFILE) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }',
    ],
    { stdio: 'ignore', env: { ...process.env, TBOUND_CSP_PROFILE: profile }, timeout: 30000 },
  );
}

// Removes a directory that a browser may still be releasing. A failure to remove
// it after 15 s is reported by runCleanups, with the directory's path.
function removeTree(directory) {
  const deadline = Date.now() + 15000;
  for (;;) {
    try {
      rmSync(directory, { recursive: true, force: true });
      return;
    } catch (error) {
      if (Date.now() >= deadline) throw new Error(`could not remove ${directory} (${error.code ?? error.message}); remove it by hand`);
      sleepSync(250);
    }
  }
}

// The Go toolchain is the pinned go1.27.x. Its version line is returned for the
// report, and any other version stops the build before it starts.
function goToolchain() {
  let line;
  try {
    line = execFileSync('go', ['version'], { encoding: 'utf8', timeout: 30000 }).trim();
  } catch (error) {
    throw new Stop(CODE.notVerified, `cannot run go (${error.code ?? error.message}); the gateway build needs the pinned Go 1.27.x on PATH, or pass --url`);
  }
  const match = /^go version go(\S+)/.exec(line);
  if (!match || !/^1\.27(?:\.\d+)?$/.test(match[1])) {
    throw new Stop(CODE.notVerified, `the gateway build needs the pinned Go 1.27.x; PATH has "${line}"`);
  }
  return line;
}

async function startGateway() {
  const toolchain = goToolchain();
  const dir = mkdtempSync(join(tmpdir(), 'tbound-csp-bin-'));
  cleanups.push(() => removeTree(dir));
  const binary = join(dir, exe('tbound-webchat'));
  execFileSync('go', ['build', '-mod=readonly', '-o', binary, './cmd/tbound-webchat'], {
    cwd: webchatRoot,
    stdio: 'inherit',
    timeout: 300000,
  });
  const port = await freePort();
  const child = spawn(binary, ['--demo', '--addr', `127.0.0.1:${port}`], { stdio: 'ignore', detached: !isWindows });
  cleanups.push(() => killTree(child));
  const url = `http://127.0.0.1:${port}`;
  if (!(await poll(() => getJson(`${url}/api/v1/contract`), 30000, 250))) {
    throw new Stop(CODE.notVerified, `demo gateway did not answer on ${url}`);
  }
  return { url, toolchain };
}

async function launchBrowser(binary) {
  const profile = mkdtempSync(join(tmpdir(), 'tbound-csp-profile-'));
  // Pushed before the browser, so it runs after the browser is killed (LIFO).
  cleanups.push(() => removeTree(profile));
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
  const child = spawn(binary, args, { stdio: 'ignore', detached: !isWindows });
  cleanups.push(() => {
    killTree(child);
    if (isWindows) killProfileProcesses(profile);
  });
  const base = `http://127.0.0.1:${port}`;
  const version = await poll(() => getJson(`${base}/json/version`), 25000, 300);
  if (!version) throw new Stop(CODE.notVerified, `browser at ${binary} did not open a DevTools endpoint`);
  return { base, version };
}

async function pageSocketUrl(base) {
  const targets = await getJson(`${base}/json/list`);
  const page = targets.find((target) => target.type === 'page' && target.webSocketDebuggerUrl);
  if (page) return page.webSocketDebuggerUrl;
  const created = await fetch(`${base}/json/new?about:blank`, { method: 'PUT', signal: AbortSignal.timeout(5000) }).then((r) => r.json());
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
      this.socket.addEventListener('close', () => this.failPending('the DevTools socket closed'));
    });
  }

  failPending(reason) {
    for (const [id, waiter] of this.pending) {
      this.pending.delete(id);
      waiter.reject(new Stop(CODE.notVerified, `${waiter.method}: ${reason}`));
    }
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

  // Every command has a deadline, so a browser that stops answering ends the run
  // as NOT VERIFIED instead of hanging it.
  send(method, params = {}, timeoutMs = 15000) {
    const id = this.nextId;
    this.nextId += 1;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Stop(CODE.notVerified, `DevTools ${method} did not answer within ${timeoutMs} ms`));
      }, timeoutMs);
      this.pending.set(id, {
        method,
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
      try {
        this.socket.send(JSON.stringify({ id, method, params }));
      } catch (error) {
        this.pending.delete(id);
        clearTimeout(timer);
        reject(new Stop(CODE.notVerified, `DevTools ${method} could not be sent: ${error.message}`));
      }
    });
  }

  on(method, handler) {
    this.handlers.set(method, [...(this.handlers.get(method) ?? []), handler]);
  }

  // Resolves with the next event of this method. Rejects after timeoutMs.
  once(method, timeoutMs) {
    return new Promise((resolve, reject) => {
      const remove = () => this.handlers.set(method, (this.handlers.get(method) ?? []).filter((h) => h !== handler));
      const handler = (params) => {
        clearTimeout(timer);
        remove();
        resolve(params);
      };
      const timer = setTimeout(() => {
        remove();
        reject(new Stop(CODE.notVerified, `timed out waiting for ${method}`));
      }, timeoutMs);
      this.on(method, handler);
    });
  }

  close() {
    this.socket.close();
  }
}

function isAllowedFavicon404(entry) {
  return entry.source === 'network' && /\/favicon\.ico$/.test(entry.url ?? '') && /status of 404/.test(entry.text ?? '');
}

function headerValue(headers, name) {
  const entry = Object.entries(headers ?? {}).find(([key]) => key.toLowerCase() === name);
  return entry ? String(entry[1]) : null;
}

// Loads each target in one page and records what the browser saw. The state is
// reset per target, so one page's errors cannot count against another.
async function auditTargets(base, targets) {
  const cdp = new Cdp(await pageSocketUrl(base));
  await cdp.open();
  try {
    const state = { csp: [], errors: [], exceptions: [], favicon404: 0, documents: [] };
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
    cdp.on('Network.responseReceived', ({ type, response }) => {
      if (type === 'Document') state.documents.push({ url: response.url, csp: headerValue(response.headers, 'content-security-policy') });
    });

    for (const method of ['Page.enable', 'Runtime.enable', 'Log.enable', 'Network.enable']) await cdp.send(method);
    await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: CSP_HOOK });

    const results = [];
    for (const target of targets) {
      Object.assign(state, { csp: [], errors: [], exceptions: [], favicon404: 0, documents: [] });
      const loaded = cdp.once('Page.loadEventFired', 20000);
      const navigation = await cdp.send('Page.navigate', { url: target.url });
      if (navigation.errorText) throw new Stop(CODE.notVerified, `navigation to ${target.url} failed: ${navigation.errorText}`);
      await loaded;
      if (target.kind === 'app') {
        await poll(async () => (await evaluate(cdp, 'Boolean(document.querySelector("header[aria-label=\\"Workspace header\\"]"))')) === true, 10000);
      }
      await sleep(target.kind === 'vanilla' ? 1500 : 500);
      const audit = await evaluate(cdp, AUDIT);
      const documentEntry = [...state.documents].reverse().find((entry) => entry.url === target.url);
      results.push({
        target,
        audit,
        documentCsp: documentEntry ? documentEntry.csp : undefined,
        state: { csp: [...state.csp], errors: [...state.errors], exceptions: [...state.exceptions], favicon404: state.favicon404 },
      });
    }
    return results;
  } finally {
    cdp.close();
  }
}

async function evaluate(cdp, expression) {
  const result = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (result.exceptionDetails) throw new Error(`page evaluation failed: ${result.exceptionDetails.text}`);
  return result.result.value;
}

// The canary is served with the gateway's policy, so the browser must refuse it
// the same way it refuses a gateway page. Its own headers are not judged here.
function startCanary(policy) {
  const server = createHttpServer((request, response) => {
    const found = request.url === '/canary';
    response.writeHead(found ? 200 : 404, {
      'Content-Type': 'text/html; charset=utf-8',
      'Content-Security-Policy': policy,
      'Cache-Control': 'no-store',
    });
    response.end(found ? CANARY_PAGE : 'not found');
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      cleanups.push(() => {
        server.closeAllConnections();
        server.close();
      });
      resolve(`http://127.0.0.1:${server.address().port}/canary`);
    });
  });
}

function probe(gatewayUrl, { method = 'GET', path, headers = {} }) {
  const { hostname, port } = new URL(gatewayUrl);
  return new Promise((resolve, reject) => {
    const request = httpRequest({ hostname, port, path, method, headers, timeout: 5000 }, (response) => {
      response.resume();
      response.on('end', () => resolve({ status: response.statusCode, csp: response.headers['content-security-policy'] ?? null }));
    });
    request.on('timeout', () => request.destroy(new Error(`${method} ${path} timed out`)));
    request.on('error', reject);
    request.end();
  });
}

async function checkHeaders(gatewayUrl, policy) {
  const failures = [];
  for (const item of PROBES) {
    const { status, csp } = await probe(gatewayUrl, item);
    if (csp !== policy) {
      failures.push(`${item.label}: Content-Security-Policy is ${csp === null ? 'absent' : JSON.stringify(csp)}, not the gateway policy`);
    }
    if (status !== item.status) failures.push(`${item.label}: status ${status}, expected ${item.status}`);
  }
  return failures;
}

function judgeCanary({ audit, state }) {
  return state.csp.length + audit.cspCaptured;
}

function judgePage({ target, audit, state, documentCsp }, policy) {
  const failures = [];
  if (documentCsp === undefined) {
    failures.push('no Document response was observed for this page');
  } else if (documentCsp !== policy) {
    failures.push(`document Content-Security-Policy is ${documentCsp === null ? 'absent' : JSON.stringify(documentCsp)}, not the gateway policy`);
  }
  if (state.csp.length > 0) failures.push(`${state.csp.length} CSP violation log entr(ies): ${state.csp.join(' | ')}`);
  if (audit.cspCaptured > 0) failures.push(`${audit.cspCaptured} securitypolicyviolation event(s) captured`);
  if (state.errors.length > 0) failures.push(`console errors: ${state.errors.join(' | ')}`);
  if (state.exceptions.length > 0) failures.push(`uncaught exceptions: ${state.exceptions.join(' | ')}`);
  if (audit.inlineStyles > 0) failures.push(`${audit.inlineStyles} element(s) with a style attribute`);
  if (audit.eventAttributes > 0) failures.push(`${audit.eventAttributes} on* attribute(s)`);
  if (audit.elements < 2) failures.push(`page rendered ${audit.elements} element(s)`);
  if (target.kind === 'app') {
    if (audit.title !== APP_TITLE) failures.push(`title is ${JSON.stringify(audit.title)}, expected ${JSON.stringify(APP_TITLE)}`);
    if (!audit.scaffold) {
      failures.push('no header[aria-label="Workspace header"] element on /app/ (the React shell did not render)');
    } else {
      if (audit.scaffold.className === '') failures.push('header has no class (CSS Modules not applied)');
      if (audit.scaffold.background === audit.scaffold.defaultBackground) {
        failures.push(`header bottom border ${audit.scaffold.background} equals the unstyled default: CSS Module not applied`);
      }
      if (!/G1/.test(audit.scaffold.text) || !/H1/.test(audit.scaffold.text)) {
        failures.push('the header does not show the claim boundary (G1 / H1)');
      }
    }
    if (audit.stylesheetLinks < 1) failures.push('no stylesheet link on /app/');
  } else {
    if (audit.title !== VANILLA_TITLE) failures.push(`title is ${JSON.stringify(audit.title)}, expected ${JSON.stringify(VANILLA_TITLE)}`);
    if (!audit.vanilla.claimStrip) failures.push('no #claim-strip (the claim boundary strip)');
    if (!audit.vanilla.chatView) failures.push('no #chat-view');
    for (const id of ['tab-chat', 'tab-trajectory']) {
      if (!audit.vanilla.tabs.some((tab) => tab !== null && tab.id === id && tab.role === 'tab')) {
        failures.push(`no #${id} with role="tab"`);
      }
    }
  }
  return failures;
}

function summarise({ target, audit, state }) {
  const parts = [
    `${state.csp.length + audit.cspCaptured} CSP violations`,
    `${state.errors.length + state.exceptions.length} console errors/exceptions (favicon 404 allowed: ${state.favicon404})`,
    `${audit.inlineStyles} style attributes`,
    `${audit.eventAttributes} on* attributes`,
  ];
  if (target.kind === 'app' && audit.scaffold) {
    parts.push(`CSS Module class "${audit.scaffold.className}" applied (header bottom border ${audit.scaffold.background}, unstyled default ${audit.scaffold.defaultBackground})`);
  }
  return parts.join('; ');
}

async function main(argv) {
  try {
    const options = parseArgs(argv);
    const policy = expectedPolicy();
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
    let toolchain = 'not checked: the gateway was supplied with --url';
    if (!gatewayUrl) {
      ({ url: gatewayUrl, toolchain } = await startGateway());
    } else if (!(await poll(() => getJson(`${gatewayUrl}/api/v1/contract`), 3000, 250))) {
      throw new Stop(CODE.notVerified, `no gateway answered at ${gatewayUrl}`);
    }

    const browser = await launchBrowser(binary);
    console.log(`csp-smoke: browser ${browser.version.Browser} (${binary})`);
    console.log(`csp-smoke: toolchain ${toolchain}`);
    console.log(`csp-smoke: gateway ${gatewayUrl} (demo fixture, read-only); policy from handler.go`);

    const [canary] = await auditTargets(browser.base, [{ kind: 'canary', url: await startCanary(policy) }]);
    const observed = judgeCanary(canary);
    if (observed === 0) {
      throw new Stop(CODE.blind, 'observer is blind: the canary page (an inline script and an inline style attribute under the gateway policy) produced 0 CSP violations');
    }
    console.log(`csp-smoke: observer live: the canary produced ${observed} CSP violation(s)`);

    const probeFailures = await checkHeaders(gatewayUrl, policy);
    for (const failure of probeFailures) console.log(`FAIL header probe: ${failure}`);
    if (probeFailures.length === 0) console.log(`PASS header probes: ${PROBES.length} responses carry the gateway policy with the expected status`);

    const results = await auditTargets(
      browser.base,
      GATEWAY_TARGETS.map((target) => ({ ...target, url: `${gatewayUrl}${target.path}` })),
    );
    let failed = probeFailures.length > 0 ? 1 : 0;
    for (const result of results) {
      const failures = judgePage(result, policy);
      if (failures.length === 0) {
        console.log(`PASS ${result.target.label}: ${summarise(result)}`);
      } else {
        failed += 1;
        console.log(`FAIL ${result.target.label}:`);
        for (const failure of failures) console.log(`  - ${failure}`);
      }
    }
    if (failed > 0) {
      console.log(`csp-smoke: FAILED (${failed} check group(s) failed)`);
      return CODE.fail;
    }
    console.log(`csp-smoke: PASS (${results.length} pages, ${PROBES.length} header probes, observer live, ${browser.version.Browser})`);
    return CODE.pass;
  } catch (error) {
    if (error instanceof Stop) {
      if (error.code === CODE.usage) console.error(`csp-smoke: ${error.message}`);
      else if (error.code === CODE.blind) console.error(`FAIL: ${error.message}`);
      else console.error(`NOT VERIFIED: ${error.message}`);
      return error.code;
    }
    console.error(`NOT VERIFIED: ${error instanceof Error ? error.message : String(error)}`);
    return CODE.notVerified;
  } finally {
    runCleanups();
  }
}

process.exitCode = await main(process.argv.slice(2));
