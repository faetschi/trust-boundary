// Opt-in browser QA for the React /app/ client: the final browser pass of Phase 4. It is
// NOT part of `go test`, `make test` or any core gate. Node built-ins only. Run it with the
// pinned Node 24.15.0 (Node >= 22 is needed for the global WebSocket). It drives headless
// Chrome, Chromium or Edge over the DevTools protocol, against a demo gateway that is
// already running on a fresh loopback port (never 8791, the stale old build).
//
// Usage, from extensions/webchat/:
//   node scripts/ui-qa.mjs --url http://127.0.0.1:PORT [--chrome PATH] [--only A,B,...]
//       [--run PREFIX] [--gateway-sha256 HEX] [--gateway-stop PS_COMMAND --gateway-start PS_COMMAND]
//   --chrome PATH, or CHROME_BIN, selects the browser (as in csp-smoke.mjs).
//   --gateway-stop and --gateway-start are PowerShell commands that stop and restart the
//   gateway for check G. Without both, G is NOT VERIFIED. --gateway-sha256 is recorded in
//   the receipt as the gateway binary's SHA-256 (computed by the operator; not verified here).
//
// Checks. Each prints PASS, FAIL or NOT VERIFIED with its evidence. The JSON receipt and
// the PNG files are written to docs/webchat/qa/ as PREFIX-ui-qa-receipt.json and PREFIX-*.png.
// PREFIX is --run (default p5, the redesign run). The p4 files are the pre-redesign run and are not rewritten:
//   A  csp-smoke.mjs passes with no favicon allowance (run as a child process).
//   B  GET / ends on /app/ with the React shell; the retired /legacy/ is 404;
//      HEAD / is 302 to /app/; the served /app/assets files equal the committed dist.
//   C  keyboard-only walk on /app/ at 1440 px: skip link, header, tabs, filters, tool cards,
//      trajectory ledger, focus rings, no traps.
//   D  responsive: the 1024 px drawer; no horizontal overflow at 390 px; scrollY 0 on first
//      load; screenshots at 1440, 1024 and 390 px in light and dark, both views.
//   E  theme: the toggle persists across a reload; a throwing localStorage falls back to
//      system; the first paint matches the theme.
//   F  live behaviour with an injected stream (a fake EventSource; the snapshot is real).
//   G  reconnect and offline (needs --gateway-stop and --gateway-start).
//   H  forced colours and reduced motion.
//   I  console and network: no CSP violation, no console error or warning (the G outage
//      window is reported separately), no request off the gateway origin.
//   J  (redesign) the user's three points at 1440, 1180, 1024, 768, 390 and 360 px, light and
//      dark: the Sessions rail, the Pi identity and composer, the governance tabs and drawer;
//      keyboard, persistence, the icon rail, the composer's inert behaviour and copy.
//   K  (redesign) the phone dock: a Tab and a Shift+Tab walk at 360x640, 390x844, 412x915 and
//      844x390; no focused element under the dock; the last message and footer not covered; the
//      composer's reason and placeholder read in full (not clipped), and a phone dock is at most 40%
//      of the viewport height, at load and with the status line shown.
//   L  (redesign) the claim boundary: the lossy-success wording on the Chat card, the ledger
//      row and the inspector; absent versus zero counts, via a snapshot rewritten in the page.
//
// Exit codes: 0 PASS (every selected check passed). 1 FAIL: a check failed. 2 usage error.
// 3 NOT VERIFIED: no browser. 4 NOT VERIFIED: the gateway, DevTools or a check's
// precondition was unavailable and no check failed. A PASS is printed only when every
// selected check passed.
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { request as httpRequest } from 'node:http';
import { createServer as createNetServer } from 'node:net';
import { tmpdir } from 'node:os';
import { delimiter, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const CODE = { pass: 0, fail: 1, usage: 2, noBrowser: 3, notVerified: 4 };
const CHECK_IDS = ['A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L'];
const STALE_PORT = 8791;
const webchatRoot = fileURLToPath(new URL('..', import.meta.url));
const QA_DIR = fileURLToPath(new URL('../docs/webchat/qa/', import.meta.url));
const DIST_DIR = join(webchatRoot, 'internal', 'webchat', 'dist');
// The output prefix for the receipt and the PNGs (--run). Set once in main() before any check runs.
let RUN = 'p5';
const isWindows = process.platform === 'win32';
const exe = (name) => (isWindows ? `${name}.exe` : name);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');
const sha256File = (path) => sha256(readFileSync(path));
const HEIGHT = 900;
const APP_HEADER = 'header[aria-label="Workspace header"]';

class Stop extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

// Every cleanup runs once, in reverse order, on normal exit, on a failure, and on
// SIGINT, SIGTERM or SIGHUP (as in csp-smoke.mjs).
const cleanups = [];
function runCleanups() {
  while (cleanups.length > 0) {
    try {
      cleanups.pop()();
    } catch (error) {
      console.error(`ui-qa: cleanup failed: ${error.message}`);
    }
  }
}
process.on('exit', runCleanups);
for (const [signal, code] of [['SIGINT', 130], ['SIGTERM', 143], ['SIGHUP', 129]]) {
  process.once(signal, () => process.exit(code));
}

// ---------- launcher (the same approach as csp-smoke.mjs) ----------

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
    candidates.push('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/Applications/Chromium.app/Contents/MacOS/Chromium');
  } else {
    candidates.push('/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/usr/bin/chromium', '/usr/bin/chromium-browser');
  }
  for (const dir of (process.env.PATH ?? '').split(delimiter).filter(Boolean)) {
    for (const name of ['google-chrome', 'chromium', 'chrome', 'msedge']) candidates.push(join(dir, exe(name)));
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
        if (port === STALE_PORT) freePort().then(resolve, reject);
        else resolve(port);
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

// Kills whatever still names this run's temporary profile (Windows keeps files locked
// for a moment, and an orphaned browser can outlive its launcher). The profile path is
// passed in the environment, so the PowerShell command line does not match itself.
function killProfileProcesses(profile) {
  spawnSync(
    'powershell',
    [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      'Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($env:TBOUND_QA_PROFILE) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }',
    ],
    { stdio: 'ignore', env: { ...process.env, TBOUND_QA_PROFILE: profile }, timeout: 30000 },
  );
}

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

async function launchBrowser(binary) {
  const profile = mkdtempSync(join(tmpdir(), 'tbound-ui-qa-profile-'));
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

// ---------- DevTools client (as csp-smoke.mjs; every command has a deadline) ----------

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
      this.socket.addEventListener('error', () => reject(new Stop(CODE.notVerified, 'DevTools socket error')), { once: true });
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

// ---------- run state: what the browser reported, tagged with the check that was running ----------

let cdp = null;
let ORIGIN = '';
let currentCheck = 'setup';
const observed = {
  console: [], // { phase, level, source, text, url }
  csp: [], // { phase, text }
  requests: [], // { phase, id, url, method, type, t }
  failed: [], // { phase, url, type, errorText, t }
  responses: [], // { phase, url, type, status, csp, t }
  timeline: [], // { kind: request|response|failed|badge, phase, at, url | status | state }, in receipt order
};
const requestUrls = new Map();
const screenshots = [];
const results = [];
const gatewayCommands = { stop: undefined, start: undefined };

function noteConsole(level, source, text, url) {
  const entry = { phase: currentCheck, level, source, text: String(text).slice(0, 400), url: url ?? null };
  observed.console.push(entry);
  if (source === 'security' && level === 'error') observed.csp.push({ phase: currentCheck, text: entry.text });
}

// Installed before any page script runs. It only listens; the page's behaviour is unchanged.
const CSP_HOOK = `(() => {
  window.__tboundCsp = [];
  document.addEventListener('securitypolicyviolation', (event) => {
    window.__tboundCsp.push({ directive: event.violatedDirective, blocked: event.blockedURI });
  }, true);
})();`;

async function setUpPage(base) {
  cdp = new Cdp(await pageSocketUrl(base));
  await cdp.open();
  cdp.on('Log.entryAdded', ({ entry }) => {
    if (entry.level === 'error' || entry.level === 'warning') noteConsole(entry.level, entry.source, entry.text, entry.url);
  });
  cdp.on('Runtime.consoleAPICalled', ({ type, args }) => {
    if (type === 'error' || type === 'warning' || type === 'assert') {
      noteConsole(type === 'warning' ? 'warning' : 'error', 'console', (args ?? []).map((a) => a.value ?? a.description ?? '').join(' '));
    }
  });
  cdp.on('Runtime.exceptionThrown', ({ exceptionDetails }) => {
    noteConsole('exception', 'exception', exceptionDetails?.exception?.description ?? exceptionDetails?.text ?? 'exception');
  });
  // The timeline keeps every request, response, failure and badge change in the order this script received them.
  cdp.on('Network.requestWillBeSent', ({ requestId, request, type, timestamp }) => {
    requestUrls.set(requestId, request.url);
    observed.requests.push({ phase: currentCheck, id: requestId, url: request.url, method: request.method, type, t: timestamp });
    observed.timeline.push({ kind: 'request', phase: currentCheck, url: request.url, at: performance.now() });
  });
  cdp.on('Network.responseReceived', ({ requestId, type, response, timestamp }) => {
    observed.responses.push({
      phase: currentCheck,
      url: response.url,
      type,
      status: response.status,
      csp: headerValue(response.headers, 'content-security-policy'),
      t: timestamp,
    });
    observed.timeline.push({ kind: 'response', phase: currentCheck, url: response.url, status: response.status, at: performance.now() });
    requestUrls.set(requestId, response.url);
  });
  cdp.on('Network.loadingFailed', ({ requestId, type, errorText, timestamp }) => {
    const url = requestUrls.get(requestId) ?? null;
    observed.failed.push({ phase: currentCheck, url, type, errorText, t: timestamp });
    observed.timeline.push({ kind: 'failed', phase: currentCheck, url, at: performance.now() });
  });
  cdp.on('Runtime.bindingCalled', ({ name, payload }) => {
    if (name !== '__qaBadge') return;
    observed.timeline.push({ kind: 'badge', phase: currentCheck, state: JSON.parse(payload).state, at: performance.now() });
  });
  for (const method of ['Page.enable', 'Runtime.enable', 'Log.enable', 'Network.enable']) await cdp.send(method);
  await cdp.send('Runtime.addBinding', { name: '__qaBadge' });
  await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
  await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: CSP_HOOK });
}

function headerValue(headers, name) {
  const entry = Object.entries(headers ?? {}).find(([key]) => key.toLowerCase() === name);
  return entry ? String(entry[1]) : null;
}

// Pulls the CSP violations the page-side hook saw since the last flush.
async function flushCspHook() {
  const count = await evaluate('(window.__tboundCsp || []).length').catch(() => 0);
  if (count > 0) {
    const events = await evaluate('window.__tboundCsp.splice(0).map((e) => e.directive + " " + e.blocked)');
    for (const text of events) observed.csp.push({ phase: currentCheck, text: `securitypolicyviolation: ${text}` });
  }
}

async function evaluate(expression) {
  const result = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (result.exceptionDetails) {
    const detail = result.exceptionDetails.exception?.description ?? result.exceptionDetails.text;
    throw new Error(`page evaluation failed: ${detail}`);
  }
  return result.result.value;
}

async function waitFor(expression, timeoutMs = 10000) {
  return poll(() => evaluate(expression), timeoutMs, 100);
}

// Navigates and waits for the load event. /app/ must then show its header.
async function navigate(path, { app = true } = {}) {
  await flushCspHook();
  const loaded = cdp.once('Page.loadEventFired', 20000);
  const navigation = await cdp.send('Page.navigate', { url: `${ORIGIN}${path}` });
  if (navigation.errorText) throw new Stop(CODE.notVerified, `navigation to ${path} failed: ${navigation.errorText}`);
  await loaded;
  if (app) await waitFor(`Boolean(document.querySelector(${JSON.stringify(APP_HEADER)}))`, 10000);
  await sleep(300);
  freshLoad = true;
}

async function viewport(width, height = HEIGHT) {
  await cdp.send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });
  await sleep(150);
}

async function emulateMedia(features) {
  await cdp.send('Emulation.setEmulatedMedia', { media: 'screen', features });
  await sleep(100);
}

// Writes a viewport PNG into docs/webchat/qa/ as <RUN>-<name> and records its SHA-256 for the receipt.
async function capture(name) {
  const file = `${RUN}-${name}`;
  const { data } = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
  const bytes = Buffer.from(data, 'base64');
  writeFileSync(join(QA_DIR, file), bytes);
  screenshots.push({ file, sha256: sha256(bytes), bytes: bytes.length, check: currentCheck });
  return file;
}

const KEY_DEFS = {
  Tab: { key: 'Tab', code: 'Tab', keyCode: 9, text: '' },
  Enter: { key: 'Enter', code: 'Enter', keyCode: 13, text: '\r' },
  ' ': { key: ' ', code: 'Space', keyCode: 32, text: ' ' },
  Escape: { key: 'Escape', code: 'Escape', keyCode: 27, text: '' },
  ArrowUp: { key: 'ArrowUp', code: 'ArrowUp', keyCode: 38, text: '' },
  ArrowDown: { key: 'ArrowDown', code: 'ArrowDown', keyCode: 40, text: '' },
  ArrowLeft: { key: 'ArrowLeft', code: 'ArrowLeft', keyCode: 37, text: '' },
  ArrowRight: { key: 'ArrowRight', code: 'ArrowRight', keyCode: 39, text: '' },
  Home: { key: 'Home', code: 'Home', keyCode: 36, text: '' },
  End: { key: 'End', code: 'End', keyCode: 35, text: '' },
};

// A real key press through the input pipeline: keyDown then keyUp. shift adds the Shift modifier.
async function press(name, { shift = false } = {}) {
  const def = KEY_DEFS[name];
  if (!def) throw new Error(`no key definition for ${name}`);
  const base = {
    key: def.key,
    code: def.code,
    windowsVirtualKeyCode: def.keyCode,
    nativeVirtualKeyCode: def.keyCode,
    modifiers: shift ? 8 : 0,
  };
  await cdp.send('Input.dispatchKeyEvent', { type: def.text ? 'keyDown' : 'rawKeyDown', ...base, text: def.text || undefined, unmodifiedText: def.text || undefined });
  await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
  await sleep(80);
}

// Gateway control for check G. The commands are PowerShell, run from this script on Windows.
// stdio is ignored so that a process the command starts cannot hold this call open.
function runGatewayCommand(label, command) {
  const result = spawnSync('powershell', ['-NoProfile', '-NonInteractive', '-Command', command], { stdio: 'ignore', timeout: 60000 });
  return { label, status: result.status, ok: result.status === 0 };
}

// The same command, run without blocking the event loop. Check G reads DevTools events while the stop runs. A synchronous call
// held those events back, so the outage's first attempts were stamped in one burst and their gap read as 0 to 126 ms (a
// measurement artefact; the scheduler's floor is 250 ms). Round 4 (the G investigation) measured it at a 4 s stop: 0 ms for
// HEAD and the restyle alike; asynchronously, 784 to 864 ms.
function runGatewayCommandAsync(label, command) {
  return new Promise((resolve) => {
    const child = spawn('powershell', ['-NoProfile', '-NonInteractive', '-Command', command], { stdio: 'ignore' });
    const timer = setTimeout(() => child.kill(), 60000);
    const done = (status) => {
      clearTimeout(timer);
      resolve({ label, status, ok: status === 0 });
    };
    child.on('exit', (code) => done(code));
    child.on('error', () => done(null));
  });
}

// ---------- the fixtures every check reads ----------

function distTree() {
  const files = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else files.push(full);
    }
  };
  walk(DIST_DIR);
  files.sort();
  const hashes = files.map((file) => [relative(DIST_DIR, file).split(sep).join('/'), sha256File(file)]);
  return {
    path: 'internal/webchat/dist',
    files: files.length,
    sha256: sha256(hashes.map(([name, hash]) => `${name} ${hash}`).join('\n')),
    hashes: Object.fromEntries(hashes),
  };
}

function gitHead() {
  const result = spawnSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: webchatRoot, encoding: 'utf8' });
  return result.status === 0 ? result.stdout.trim() : null;
}

// ---------- check plumbing ----------

// A check reports through ctx. fail() records a product defect; unverified() records a
// missing precondition (it never turns a defect into a pass). A thrown Stop with the
// notVerified code is also reported as NOT VERIFIED; any other throw is a FAIL.
function newContext() {
  const ctx = {
    evidence: {},
    failures: [],
    unverified: [],
    expect(condition, message) {
      if (!condition) ctx.failures.push(message);
      return Boolean(condition);
    },
    fail(message) {
      ctx.failures.push(message);
    },
    unverify(message) {
      ctx.unverified.push(message);
    },
    note(key, value) {
      ctx.evidence[key] = value;
    },
  };
  return ctx;
}

async function runCheck(check) {
  currentCheck = check.id;
  const ctx = newContext();
  const started = Date.now();
  try {
    await check.run(ctx);
  } catch (error) {
    if (error instanceof Stop && error.code === CODE.notVerified) ctx.unverify(error.message);
    else ctx.fail(`check threw: ${error instanceof Error ? error.message : String(error)}`);
  }
  await flushCspHook().catch(() => {});
  let status = 'PASS';
  if (ctx.failures.length > 0) status = 'FAIL';
  else if (ctx.unverified.length > 0) status = 'NOT VERIFIED';
  const summary = typeof ctx.evidence.summary === 'string' ? ctx.evidence.summary : '';
  const result = {
    id: check.id,
    name: check.name,
    status,
    ms: Date.now() - started,
    summary,
    failures: ctx.failures,
    unverified: ctx.unverified,
    evidence: ctx.evidence,
  };
  results.push(result);
  const label = `${status} ${check.id} ${check.name}`;
  console.log(summary ? `${label}: ${summary}` : label);
  for (const failure of ctx.failures) console.log(`  - ${failure}`);
  for (const reason of ctx.unverified) console.log(`  - NOT VERIFIED: ${reason}`);
  return result;
}

// ---------- A: csp-smoke with no favicon allowance ----------

let browserBinary = null;

async function checkA(ctx) {
  const script = join(webchatRoot, 'scripts', 'csp-smoke.mjs');
  const run = spawnSync(process.execPath, [script, '--url', ORIGIN, '--chrome', browserBinary], { encoding: 'utf8', timeout: 300000, cwd: webchatRoot });
  const output = `${run.stdout ?? ''}${run.stderr ?? ''}`;
  writeFileSync(join(QA_DIR, `${RUN}-csp-smoke-output.txt`), output);
  ctx.note('command', `node scripts/csp-smoke.mjs --url ${ORIGIN} --chrome <browser>`);
  ctx.note('exitCode', run.status);
  ctx.note('output', `${RUN}-csp-smoke-output.txt`);
  ctx.expect(run.status === 0, `csp-smoke exited ${run.status}; its output is in ${RUN}-csp-smoke-output.txt`);
  const clean = output.split('\n').filter((line) => line.startsWith('PASS ') && /0 console errors\/exceptions/.test(line));
  for (const label of ['PASS / (redirect', 'PASS /app/ (React)']) {
    ctx.expect(clean.some((line) => line.startsWith(label)), `csp-smoke has no PASS line with 0 console errors for ${label}`);
  }
  ctx.expect(!/favicon/i.test(output), 'csp-smoke output still mentions a favicon allowance');
  ctx.note('summary', `csp-smoke exit ${run.status}; ${clean.length} pages with 0 console errors/exceptions; no favicon allowance in its output`);
}

// ---------- B: redirect, retired legacy path, served assets ----------

function probe(method, path) {
  const { hostname, port } = new URL(ORIGIN);
  return new Promise((resolve, reject) => {
    const request = httpRequest({ hostname, port, path, method, timeout: 5000 }, (response) => {
      response.resume();
      response.on('end', () =>
        resolve({
          status: response.statusCode,
          location: response.headers.location ?? null,
          csp: response.headers['content-security-policy'] ?? null,
        }),
      );
    });
    request.on('timeout', () => request.destroy(new Error(`${method} ${path} timed out`)));
    request.on('error', reject);
    request.end();
  });
}

async function checkB(ctx) {
  const head = await probe('HEAD', '/');
  const get = await probe('GET', '/');
  ctx.note('HEAD /', { status: head.status, location: head.location });
  ctx.note('GET /', { status: get.status, location: get.location });
  ctx.expect(head.status === 302 && head.location === '/app/', `HEAD / answered ${head.status} with Location ${JSON.stringify(head.location)}, expected 302 to /app/`);
  ctx.expect(get.status === 302 && get.location === '/app/', `GET / answered ${get.status} with Location ${JSON.stringify(get.location)}, expected 302 to /app/`);

  await navigate('/');
  const landed = await evaluate('location.pathname');
  const shell = await evaluate(`({ header: Boolean(document.querySelector(${JSON.stringify(APP_HEADER)})), title: document.title })`);
  ctx.note('GET / in the browser', { endedOn: landed, title: shell.title, reactHeader: shell.header });
  ctx.expect(landed === '/app/', `GET / ended on ${landed}, expected /app/`);
  ctx.expect(shell.header, 'no workspace header after GET / (the React shell did not render)');

  const legacy = await probe('GET', '/legacy/');
  ctx.note('GET /legacy/', { status: legacy.status });
  ctx.expect(legacy.status === 404, `GET /legacy/ answered ${legacy.status}, expected 404 (the legacy client is retired)`);

  // The served shell and its assets must be the committed build, byte for byte.
  const shellBytes = Buffer.from(await (await fetch(`${ORIGIN}/app/`)).arrayBuffer());
  ctx.expect(sha256(shellBytes) === sha256File(join(DIST_DIR, 'index.html')), 'the served /app/ HTML differs from internal/webchat/dist/index.html');
  const refs = [...new Set([...shellBytes.toString('utf8').matchAll(/(?:src|href)="(\/app\/assets\/[^"]+)"/g)].map((m) => m[1]))];
  ctx.expect(refs.length >= 2, `the served /app/ HTML names ${refs.length} asset(s), expected a script and a stylesheet`);
  const served = {};
  for (const ref of refs) {
    const bytes = Buffer.from(await (await fetch(`${ORIGIN}${ref}`)).arrayBuffer());
    served[ref] = sha256(bytes);
    ctx.expect(sha256(bytes) === sha256File(join(DIST_DIR, ref.slice('/app/'.length))), `served ${ref} differs from the committed dist`);
  }
  ctx.note('servedAssetSha256', served);
  ctx.note('summary', `HEAD / 302 to /app/; GET / ended on ${landed} with the React header; GET /legacy/ is 404; ${refs.length} served assets equal dist`);
}

// ---------- C: keyboard-only walk on /app/ at 1440 px ----------

const DESCRIBE_ACTIVE = `(() => {
  const el = document.activeElement;
  if (!el || el === document.body || el === document.documentElement) return { tag: 'BODY' };
  const cs = getComputedStyle(el);
  const rect = el.getBoundingClientRect();
  const attr = (name) => el.getAttribute(name);
  return {
    tag: el.tagName.toLowerCase(),
    id: el.id || null,
    role: attr('role'),
    label: attr('aria-label'),
    href: attr('href'),
    text: (el.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 70),
    pressed: attr('aria-pressed'),
    selected: attr('aria-selected'),
    tabindex: attr('tabindex'),
    open: el.tagName === 'SUMMARY' && el.parentElement ? el.parentElement.open : null,
    inViewport: rect.top >= 0 && rect.bottom <= window.innerHeight && rect.width > 0 && rect.height > 0,
    outlineStyle: cs.outlineStyle,
    outlineWidth: cs.outlineWidth,
    boxShadow: cs.boxShadow,
    ring: (cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0) || cs.boxShadow !== 'none',
    focusVisible: el.matches(':focus-visible'),
  };
})()`;

const PRESSED_IN = (label) => `Array.from(document.querySelectorAll('[role="toolbar"][aria-label="${label}"] button')).map((b) => [b.textContent.trim(), b.getAttribute('aria-pressed')])`;

const describe = () => evaluate(DESCRIBE_ACTIVE);

function shortStop(s) {
  if (s.tag === 'BODY') return 'BODY';
  const parts = [s.tag + (s.id ? `#${s.id}` : '')];
  if (s.role) parts.push(`role=${s.role}`);
  if (s.label) parts.push(`label="${s.label.slice(0, 40)}"`);
  if (s.text) parts.push(`"${s.text.slice(0, 30)}"`);
  if (s.pressed !== null) parts.push(`pressed=${s.pressed}`);
  if (s.selected !== null) parts.push(`selected=${s.selected}`);
  if (s.open !== null) parts.push(`open=${s.open}`);
  return parts.join(' ');
}

// Headless Chrome has no address bar. After a navigation the first Tab can land on the document
// (BODY), where a browser's address bar would take that press. The walk presses again in that case
// and counts it (swallowedFirstTabs); the order itself is still read from the page.
let freshLoad = false;
let swallowedFirstTabs = 0;
async function tabPress() {
  const first = freshLoad;
  freshLoad = false;
  await press('Tab');
  let stop = await describe();
  if (first && stop.tag === 'BODY') {
    swallowedFirstTabs += 1;
    await press('Tab');
    stop = await describe();
  }
  return stop;
}

// Presses Tab until focus leaves the document (BODY), comes back to the first stop (a full
// cycle), or the limit is reached. The walk records how it ended in `end`. A stop that repeats
// the one before it means focus did not move: a trap.
// Round 3: openDisclosure names a summary that the walk opens with Enter when it reaches it. The header's "More options"
// disclosure holds the theme switch, which is not in the Tab order until it opens. The walk then continues in the same pass.
async function tabWalk(limit = 80, { openDisclosure = null } = {}) {
  let opened = false;
  const stops = [];
  let end = 'limit reached';
  for (let i = 0; i < limit; i += 1) {
    const stop = await tabPress();
    stops.push(stop);
    if (openDisclosure && !opened && stop.label === openDisclosure) {
      opened = true;
      await press('Enter');
    }
    if (stop.tag === 'BODY') {
      end = 'document end';
      break;
    }
    if (stops.length > 1 && shortStop(stop) === shortStop(stops[stops.length - 2])) {
      end = 'stuck: focus did not move';
      break;
    }
    if (stops.length > 1 && shortStop(stop) === shortStop(stops[0])) {
      stops.pop();
      end = 'cycled to the first stop';
      break;
    }
  }
  return { stops, end };
}

async function tabTo(count) {
  let stop = null;
  for (let i = 0; i < count; i += 1) stop = await tabPress();
  return stop;
}

// Presses Tab (or Shift+Tab) until a stop satisfies predicate, within the limit.
async function tabUntil(predicate, { limit = 40, shift = false } = {}) {
  let stop = null;
  for (let i = 0; i < limit; i += 1) {
    if (shift) {
      await press('Tab', { shift: true });
      stop = await describe();
    } else {
      stop = await tabPress();
    }
    if (stop.tag !== 'BODY' && predicate(stop)) return stop;
  }
  return stop;
}

async function checkC(ctx) {
  await viewport(1440);
  await emulateMedia([{ name: 'prefers-color-scheme', value: 'light' }]);
  swallowedFirstTabs = 0;

  // C1: the skip link is the first stop, is visible when focused, and Enter moves focus to its target.
  await navigate('/app/');
  const skip = await tabTo(1);
  ctx.note('skipLink', shortStop(skip));
  ctx.expect(skip.tag === 'a' && skip.href === '#view-panel-chat' && skip.inViewport, `the first Tab stop is not the visible skip link (got ${shortStop(skip)})`);
  ctx.expect(skip.ring, 'the skip link shows no focus ring');
  await press('Enter');
  const skipTarget = await describe();
  ctx.note('skipTarget', shortStop(skipTarget));
  ctx.expect(skipTarget.id === 'view-panel-chat', `Enter on the skip link left focus on ${shortStop(skipTarget)}, expected #view-panel-chat`);

  // C2: the full Tab order from the top of the page, to the end of the document.
  await navigate('/app/');
  // Contract change (round 3): the theme switch sits in the header's "More options" disclosure, closed by default. The walk opens
  // it at its stop (Enter), so the theme options are reached in their place in the Tab order.
  const chatWalk = await tabWalk(80, { openDisclosure: 'More options' });
  const chatStops = chatWalk.stops;
  ctx.note('chatTabOrder', chatStops.map(shortStop));
  const chatRingless = chatStops.filter((s) => s.tag !== 'BODY' && !s.ring).map(shortStop);
  ctx.expect(['document end', 'cycled to the first stop'].includes(chatWalk.end), `the chat Tab walk ended as "${chatWalk.end}" after ${chatStops.length} stops (a trap or a stuck focus)`);
  ctx.expect(chatRingless.length === 0, `focus stops without a visible ring: ${chatRingless.join(' | ')}`);
  // Contract change (redesign): the header's Governance toggle and the rail's collapse toggle are Tab stops too, so the
  // order is read by identity rather than by position: theme, then the Chat tab, the filters, and the tool cards.
  // Contract change (round 3, the dark default): the theme toolbar's one Tab stop is the pressed option, which is Dark by default.
  const themeAt = chatStops.findIndex((s) => ['System', 'Light', 'Dark'].includes(s.text) && s.pressed === 'true');
  const chatTabAt = chatStops.findIndex((s) => s.id === 'view-tab-chat');
  const allAt = chatStops.findIndex((s) => s.pressed === 'true' && /^All/.test(s.text ?? ''));
  // Contract change (round 3): the header's disclosure is a summary too. A tool card is a summary that is not the disclosure.
  // Contract change (design pass): the claim strip and the rail footnotes are disclosures too, so a tool card is a summary that is none of those.
  // A run of three or more tool actions is one closed group line, a button, and its cards are hidden until it opens.
  const firstCard = chatStops.findIndex((s) => (s.tag === 'summary' && s.label !== 'More options' && !/^(Presentation only|Read-only view)/.test(s.text ?? '')) || (s.tag === 'button' && /tool actions/.test(s.text ?? '')));
  ctx.note('chatStopIndexes', { theme: themeAt, chatTab: chatTabAt, allFilter: allAt, firstToolCard: firstCard });
  ctx.expect(themeAt > 0 && chatStops[themeAt]?.tabindex === '0', "the theme toolbar's stop is " + shortStop(chatStops[themeAt] ?? {}) + ", expected the pressed theme option with tabindex 0");
  ctx.expect(chatTabAt > themeAt, `the Chat tab (stop ${chatTabAt}) does not follow the theme toolbar (stop ${themeAt})`);
  ctx.expect(allAt > chatTabAt, `the pressed All filter (stop ${allAt}) does not follow the Chat tab (stop ${chatTabAt})`);
  ctx.expect(firstCard > allAt, 'no tool-card summary is reachable by Tab after the filters');
  ctx.note('summaryStops', chatStops.filter((s) => s.tag === 'summary').length);

  // C3: the theme toolbar has one Tab stop; its other buttons are reached with the arrow keys.
  await navigate('/app/');
  // Contract change (round 3): open the header's "More options" disclosure first (Enter on its summary); the theme options follow it.
  const onMore = await tabUntil((s) => s.label === 'More options');
  ctx.expect(onMore.label === 'More options', `Tab did not reach the More options disclosure (got ${shortStop(onMore)})`);
  await press('Enter');
  // Contract change (round 3, the dark default): the theme's Tab stop is the pressed option, Dark. The arrow keys wrap from it:
  // ArrowRight from Dark goes to System, then ArrowRight to Light, and Home goes to System.
  const onStop = await tabUntil((s) => s.text === 'Dark' && s.pressed === 'true');
  ctx.expect(onStop.text === 'Dark', 'Tab did not reach the pressed Dark option (got ' + shortStop(onStop) + ')');
  await press('ArrowRight');
  const onSystem = await describe();
  ctx.expect(onSystem.text === 'System', 'ArrowRight on the theme toolbar left focus on ' + shortStop(onSystem) + ', expected System (wrapping from Dark)');
  await press('ArrowRight');
  const onLight = await describe();
  ctx.expect(onLight.text === 'Light', 'ArrowRight on System left focus on ' + shortStop(onLight) + ', expected Light');
  await press('Home');
  const onHome = await describe();
  ctx.expect(onHome.text === 'System', 'Home on the theme toolbar left focus on ' + shortStop(onHome) + ', expected System');
  ctx.note('themeToolbarArrows', { arrowRight: onSystem.text, twice: onLight.text, home: onHome.text, pressedAfterArrows: await evaluate(PRESSED_IN('Theme')) });

  // C4: the view tabs. Arrows and Home/End move focus; the selected tab follows (automatic or with Enter).
  await navigate('/app/');
  const onChatTab = await tabUntil((s) => s.id === 'view-tab-chat');
  ctx.expect(onChatTab.id === 'view-tab-chat', `Tab did not reach the Chat tab (got ${shortStop(onChatTab)})`);
  await press('ArrowRight');
  const onTrajTab = await describe();
  const selectedAfterArrow = await evaluate(`document.getElementById('view-tab-trajectory').getAttribute('aria-selected')`);
  ctx.expect(onTrajTab.id === 'view-tab-trajectory', `ArrowRight on the Chat tab left focus on ${shortStop(onTrajTab)}`);
  if (selectedAfterArrow !== 'true') await press('Enter');
  const panelVisible = await evaluate(`getComputedStyle(document.getElementById('view-panel-trajectory')).display !== 'none'`);
  ctx.note('tabs', { arrowMovesFocusTo: onTrajTab.id, selectedAfterArrow, selectedAfterEnter: await evaluate(`document.getElementById('view-tab-trajectory').getAttribute('aria-selected')`), trajectoryPanelVisible: panelVisible });
  ctx.expect(panelVisible, 'the Trajectory panel is not shown after selecting its tab');
  await press('Home');
  const homeTab = await describe();
  ctx.expect(homeTab.id === 'view-tab-chat', `Home on the Trajectory tab left focus on ${shortStop(homeTab)}`);
  await press('End');
  const endTab = await describe();
  ctx.expect(endTab.id === 'view-tab-trajectory', `End on the Chat tab left focus on ${shortStop(endTab)}`);
  await press('Home');
  await press('Enter');
  const chatBack = await evaluate(`document.getElementById('view-tab-chat').getAttribute('aria-selected')`);
  ctx.expect(chatBack === 'true', 'Home then Enter did not select the Chat tab again');

  // C5: the chat filter toolbar. Arrows move focus and leave aria-pressed alone; Enter presses.
  await navigate('/app/');
  const onAll = await tabUntil((s) => s.pressed === 'true' && /^All/.test(s.text ?? ''));
  ctx.expect(onAll.pressed === 'true' && /^All/.test(onAll.text), `Tab did not reach the pressed All filter (got ${shortStop(onAll)})`);
  const pressedBefore = await evaluate(PRESSED_IN('Timeline filters'));
  await press('ArrowRight');
  const onDenied = await describe();
  const pressedAfterArrow = await evaluate(PRESSED_IN('Timeline filters'));
  ctx.expect(/^Denied/.test(onDenied.text), `ArrowRight on the filters left focus on ${shortStop(onDenied)}, expected Denied`);
  ctx.expect(JSON.stringify(pressedAfterArrow) === JSON.stringify(pressedBefore), `ArrowRight changed aria-pressed: ${JSON.stringify(pressedBefore)} -> ${JSON.stringify(pressedAfterArrow)}`);
  await press('Enter');
  const pressedAfterEnter = await evaluate(PRESSED_IN('Timeline filters'));
  ctx.expect(pressedAfterEnter.find(([name]) => /^Denied/.test(name))?.[1] === 'true', `Enter on Denied did not press it: ${JSON.stringify(pressedAfterEnter)}`);
  const countText = await evaluate(`(Array.from(document.querySelectorAll('#view-panel-chat *')).map((el) => el.childElementCount === 0 ? el.textContent.trim() : '').find((t) => /element\\(s\\)/.test(t)) || null)`);
  ctx.note('filters', { before: pressedBefore, afterArrow: pressedAfterArrow, afterEnter: pressedAfterEnter, countAfterDenied: countText });
  await press('Home');
  await press('Enter');
  const restored = await evaluate(PRESSED_IN('Timeline filters'));
  ctx.expect(restored.find(([name]) => /^All/.test(name))?.[1] === 'true', 'Home then Enter did not restore the All filter');

  // C6: tool cards are native disclosures: Enter and Space toggle them.
  await navigate('/app/');
  const firstSummaryStop = await tabUntil((s) => s.tag === 'summary');
  ctx.expect(firstSummaryStop.tag === 'summary', `Tab after the filters did not reach a tool card (got ${shortStop(firstSummaryStop)})`);
  const openBefore = firstSummaryStop.open;
  await press('Enter');
  const openAfterEnter = (await describe()).open;
  await press(' ');
  const openAfterSpace = (await describe()).open;
  ctx.note('toolCard', { label: firstSummaryStop.text.slice(0, 50), openBefore, openAfterEnter, openAfterSpace });
  ctx.expect(openAfterEnter !== openBefore, 'Enter did not toggle the tool card');
  ctx.expect(openAfterSpace === openBefore, 'Space did not toggle the tool card back');

  // C7: the trajectory view: grouping toolbar, search, the ledger's roving tab stop, selection, and focus return.
  await navigate('/app/');
  await tabUntil((s) => s.id === 'view-tab-chat');
  await press('ArrowRight');
  await press('Enter');
  await press('Tab');
  const onTurn = await describe();
  ctx.expect(onTurn.text === 'Turn' && onTurn.pressed === 'true', `the first trajectory stop is ${shortStop(onTurn)}, expected the pressed Turn grouping button`);
  await press('ArrowRight');
  const onTool = await describe();
  const groupArrow = await evaluate(PRESSED_IN('Group records by'));
  ctx.expect(/^Tool/.test(onTool.text), `ArrowRight on the grouping toolbar left focus on ${shortStop(onTool)}, expected Tool`);
  ctx.expect(groupArrow.find(([name]) => /^Turn/.test(name))?.[1] === 'true', `ArrowRight changed the grouping pressed state: ${JSON.stringify(groupArrow)}`);
  await press('Enter');
  const groupedByTool = await evaluate(PRESSED_IN('Group records by'));
  await press('Home');
  await press('Enter');
  const groupRestored = await evaluate(PRESSED_IN('Group records by'));
  ctx.note('grouping', { afterArrow: groupArrow, afterEnterTool: groupedByTool, restored: groupRestored });
  await press('Tab');
  const onSearch = await describe();
  ctx.expect(onSearch.tag === 'input', `Tab from the grouping toolbar reached ${shortStop(onSearch)}, expected the search input`);
  await press('Tab');
  // The rail's folded "About this axis" note sits between the search and the ledger; step over it.
  if ((await describe()).tag === 'summary') await press('Tab');
  const onRow1 = await describe();
  ctx.expect(/^User record 1/.test(onRow1.label ?? ''), `Tab from the search reached ${shortStop(onRow1)}, expected the first ledger row`);
  await press('ArrowDown');
  const row2 = await describe();
  await press('ArrowDown');
  const row3 = await describe();
  await press('End');
  const rowEnd = await describe();
  await press('Home');
  const rowHome = await describe();
  ctx.note('ledgerKeys', { down1: row2.label, down2: row3.label, end: rowEnd.label, home: rowHome.label });
  ctx.expect(/^Assistant record 2/.test(row2.label ?? ''), `ArrowDown left focus on ${row2.label}, expected Assistant record 2`);
  ctx.expect(/^Tool record 3/.test(row3.label ?? ''), `second ArrowDown left focus on ${row3.label}, expected Tool record 3`);
  ctx.expect(/^Assistant record 18/.test(rowEnd.label ?? ''), `End left focus on ${rowEnd.label}, expected the last record`);
  ctx.expect(/^User record 1/.test(rowHome.label ?? ''), `Home left focus on ${rowHome.label}, expected the first record`);
  await press('ArrowDown');
  await press('Enter');
  const selected = await describe();
  const inspector = await evaluate(`(() => {
    const buttons = Array.from(document.querySelectorAll('button')).filter((b) => /close|back|dismiss/i.test((b.getAttribute('aria-label') || '') + ' ' + b.textContent));
    return { closeButtons: buttons.map((b) => (b.getAttribute('aria-label') || b.textContent.trim()).slice(0, 60)), dialogs: Array.from(document.querySelectorAll('[role="dialog"], dialog')).map((d) => d.getAttribute('aria-label') || d.tagName) };
  })()`);
  ctx.note('ledgerSelect', { focusedAfterEnter: shortStop(selected), pressed: selected.pressed, inspector });
  ctx.expect(selected.pressed === 'true', `Enter on the ledger row did not select it (aria-pressed ${selected.pressed})`);
  await press('Escape');
  const afterEscape = await describe();
  ctx.note('afterEscape', shortStop(afterEscape));
  ctx.expect(afterEscape.label === selected.label, `Escape left focus on ${shortStop(afterEscape)}, expected the selected row`);

  // C7b: ArrowUp, the inspector's Close button, and a Tab walk with a record selected (the inspector's controls join the order).
  await navigate('/app/');
  await tabUntil((s) => s.id === 'view-tab-chat');
  await press('ArrowRight');
  await press('Enter');
  await press('Tab');
  await press('Tab');
  await press('Tab');
  if ((await describe()).tag === 'summary') await press('Tab');
  await press('ArrowDown');
  await press('ArrowDown');
  await press('ArrowUp');
  const upRow = await describe();
  ctx.expect(/^Assistant record 2/.test(upRow.label ?? ''), `ArrowUp from record 3 left focus on ${upRow.label}, expected Assistant record 2`);
  await press('Enter');
  let closeStop = null;
  for (let i = 0; i < 12 && closeStop === null; i += 1) {
    await press('Tab');
    const stop = await describe();
    if (/^Close record/.test(stop.text ?? '')) closeStop = stop;
  }
  ctx.expect(closeStop !== null, 'Tab from the selected record did not reach the "Close record" button within 12 presses');
  if (closeStop !== null) {
    await press('Enter');
    const afterClose = await describe();
    ctx.note('closeRecord', { focusedAfterClose: shortStop(afterClose), pressed: afterClose.pressed });
    ctx.expect(/^Assistant record 2/.test(afterClose.label ?? '') && afterClose.pressed === 'false', `Close record left focus on ${shortStop(afterClose)}, expected the record 2 row, unselected`);
  }
  await press('Enter');
  const selectedAgain = await describe();
  ctx.expect(selectedAgain.pressed === 'true', 'Enter on the row did not select it again before the Tab walk');
  const selectedWalk = await tabWalk();
  ctx.note('selectedTabOrder', selectedWalk.stops.map(shortStop));
  ctx.expect(['document end', 'cycled to the first stop'].includes(selectedWalk.end), `the Tab walk with a record selected ended as "${selectedWalk.end}" after ${selectedWalk.stops.length} stops`);
  ctx.expect(selectedWalk.stops.some((s) => /^Close record/.test(s.text ?? '')), 'the Close button is not in the Tab order with a record selected');
  const selectedRingless = selectedWalk.stops.filter((s) => s.tag !== 'BODY' && !s.ring).map(shortStop);
  ctx.expect(selectedRingless.length === 0, `stops without a visible ring with a record selected: ${selectedRingless.join(' | ')}`);

  // C8: the skip link follows the Trajectory view; then a Tab walk of that view, to the end of the document.
  await navigate('/app/');
  await tabUntil((s) => s.id === 'view-tab-chat');
  await press('ArrowRight');
  await press('Enter');
  const trajSkip = await tabUntil((s) => s.tag === 'a' && /^#view-panel/.test(s.href ?? ''), { shift: true, limit: 12 });
  ctx.note('trajectorySkipLink', shortStop(trajSkip));
  ctx.expect(trajSkip.tag === 'a' && trajSkip.href === '#view-panel-trajectory', `the skip link in the Trajectory view targets ${trajSkip.href}, expected #view-panel-trajectory`);
  await press('Enter');
  const trajTarget = await describe();
  ctx.expect(trajTarget.id === 'view-panel-trajectory', `Enter on the Trajectory skip link left focus on ${shortStop(trajTarget)}`);
  const trajWalk = await tabWalk();
  const trajStops = trajWalk.stops;
  ctx.note('trajectoryTabOrder', trajStops.map(shortStop));
  ctx.expect(['document end', 'cycled to the first stop'].includes(trajWalk.end), `the trajectory Tab walk ended as "${trajWalk.end}" after ${trajStops.length} stops (a trap or a stuck focus)`);
  const trajRingless = trajStops.filter((s) => s.tag !== 'BODY' && !s.ring).map(shortStop);
  ctx.expect(trajRingless.length === 0, `trajectory focus stops without a visible ring: ${trajRingless.join(' | ')}`);
  ctx.note('swallowedFirstTabs', swallowedFirstTabs);
  ctx.note('summary', `chat Tab order: ${chatStops.length} stops, ${chatWalk.end}; trajectory: ${trajStops.length} stops, ${trajWalk.end}; ${chatRingless.length + trajRingless.length} stops without a ring; a first Tab after load landed on the document ${swallowedFirstTabs} time(s)`);
}


// ---------- shared selectors and page-side expressions (D to I) ----------

const THEME_TOOLBAR = '[role="toolbar"][aria-label="Theme"]';
const THEME_KEY = 'tbound-webchat-theme';
const DRAWER_TOGGLE = 'button[aria-controls="governance-inspector"]';
const WIRE = 'tbound-webchat-observation/v1';
const LIGHT_OS = [{ name: 'prefers-color-scheme', value: 'light' }];
const DARK_OS = [{ name: 'prefers-color-scheme', value: 'dark' }];
const NO_OVERRIDE = [];
const MOTION_OFF = [{ name: 'prefers-reduced-motion', value: 'no-preference' }];
const READ_STORAGE_FAILS_FLAG = 'storage blocked by the QA harness';

const THEME_STATE = `(() => {
  let stored;
  try {
    stored = localStorage.getItem('${THEME_KEY}');
  } catch (error) {
    stored = 'throws: ' + error.name;
  }
  const probe = document.createElement('div');
  probe.style.background = 'var(--background)';
  document.body.appendChild(probe);
  const canvas = getComputedStyle(probe).backgroundColor;
  probe.remove();
  return {
    dataTheme: document.documentElement.getAttribute('data-theme'),
    stored,
    pressed: Array.from(document.querySelectorAll('${THEME_TOOLBAR} button')).map((b) => b.textContent.trim() + '=' + b.getAttribute('aria-pressed')),
    canvas,
  };
})()`;

const DRAWER_STATE = `(() => {
  const toggle = document.querySelector('${DRAWER_TOGGLE}');
  const aside = document.getElementById('governance-inspector');
  const header = document.querySelector('header[aria-label="Workspace header"]');
  const active = document.activeElement;
  const t = toggle ? toggle.getBoundingClientRect() : null;
  const a = aside ? aside.getBoundingClientRect() : null;
  const h = header ? header.getBoundingClientRect() : null;
  return {
    innerWidth: window.innerWidth,
    clientWidth: document.documentElement.clientWidth,
    toggleVisible: Boolean(t && t.width > 0 && t.height > 0),
    expanded: toggle ? toggle.getAttribute('aria-expanded') : null,
    inert: aside ? aside.hasAttribute('inert') : null,
    ariaHidden: aside ? aside.getAttribute('aria-hidden') : null,
    dataOpen: aside ? aside.getAttribute('data-open') : null,
    asideLeft: a ? Math.round(a.left) : null,
    asideTop: a ? Math.round(a.top) : null,
    headerBottom: h ? Math.round(h.bottom) : null,
    activeIsToggle: active === toggle,
    activeText: active ? (active.textContent || active.tagName).trim().slice(0, 30) : null,
  };
})()`;

const HEADER_HIT = `(() => Array.from(document.querySelectorAll('${THEME_TOOLBAR} button')).map((b) => {
  const r = b.getBoundingClientRect();
  const el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  return { label: b.textContent.trim(), hit: Boolean(el && (el === b || b.contains(el))) };
}))()`;

const TOGGLE_HIT = `(() => {
  const t = document.querySelector('${DRAWER_TOGGLE}');
  if (!t) return false;
  const r = t.getBoundingClientRect();
  const el = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  return Boolean(el && (el === t || t.contains(el)));
})()`;

// Chips inside the closed drawer are inert and off-canvas by design, so they are not on screen and are skipped.
const PHONE_STATE = `(() => {
  const de = document.documentElement;
  const visible = Array.from(document.querySelectorAll('span[data-tone][class*="_chip_"]')).filter((c) => !c.closest('[inert]'));
  const overflowing = visible.filter((c) => c.getBoundingClientRect().right > de.clientWidth + 0.5).map((c) => {
    let p = c.parentElement;
    let scroller = null;
    while (p && p !== document.body) {
      const cs = getComputedStyle(p);
      if (cs.overflowX !== 'visible') {
        scroller = { class: (p.getAttribute('class') || '').slice(0, 40), overflowX: cs.overflowX, clientWidth: p.clientWidth, scrollWidth: p.scrollWidth };
        break;
      }
      p = p.parentElement;
    }
    return { text: c.textContent.trim(), right: Math.round(c.getBoundingClientRect().right), scroller };
  });
  return {
    innerWidth: window.innerWidth,
    clientWidth: de.clientWidth,
    scrollWidth: de.scrollWidth,
    scrollY: window.scrollY,
    chips: visible.length,
    overflowingChips: overflowing,
  };
})()`;

const DOT_SHAPES = (tones) => `(() => {
  const region = document.querySelector('[role="region"][aria-label="Record ledger"]');
  const out = {};
  for (const tone of ${JSON.stringify(tones)}) {
    const dot = region ? region.querySelector('span[data-tone="' + tone + '"]') : null;
    if (!dot) {
      out[tone] = null;
      continue;
    }
    const cs = getComputedStyle(dot);
    // The centred mark of a bullseye is a pseudo-element (::after). Its computed box is part of the shape.
    const mark = getComputedStyle(dot, '::after');
    out[tone] = {
      borderStyle: cs.borderTopStyle,
      borderWidth: cs.borderTopWidth,
      radius: cs.borderTopLeftRadius,
      clipPath: cs.clipPath,
      boxShadow: cs.boxShadow,
      background: cs.backgroundColor,
      mark: mark.content === 'none' || mark.content === 'normal' ? 'none' : [mark.position, mark.width, mark.backgroundColor].join(' '),
    };
  }
  return out;
})()`;

const MOTION_STATE = `(() => {
  const aside = document.getElementById('governance-inspector');
  const animated = Array.from(document.querySelectorAll('*')).filter((el) => {
    const cs = getComputedStyle(el);
    return cs.animationName !== 'none' || cs.transitionDuration.split(',').some((d) => parseFloat(d) > 0);
  });
  const asideCs = aside ? getComputedStyle(aside) : null;
  return {
    runningAnimations: document.getAnimations().filter((a) => a.playState === 'running').length,
    animatedElements: animated.length,
    animatedSample: animated.slice(0, 4).map((el) => el.tagName.toLowerCase() + (el.id ? '#' + el.id : '')),
    asideTransition: asideCs ? asideCs.transitionDuration : null,
    asideTransform: asideCs ? asideCs.transform : null,
    dataOpen: aside ? aside.getAttribute('data-open') : null,
  };
})()`;

const ALL_COUNT = `(() => {
  const b = Array.from(document.querySelectorAll('[role="toolbar"][aria-label="Timeline filters"] button')).find((x) => /^All/.test(x.textContent.trim()));
  return b ? Number((b.textContent.match(/\\d+/) || [0])[0]) : null;
})()`;

const CHIPS = `Array.from(document.querySelectorAll('#view-panel-chat span[data-tone][class*="_chip_"]')).map((c) => ({ text: c.textContent.trim(), tone: c.getAttribute('data-tone') }))`;
const NOTICE_GAP = `Array.from(document.querySelectorAll('[role="note"][aria-label="Continuity gap"]')).map((n) => n.textContent.replace(/\\s+/g, ' ').trim())`;
const LOG_TEXT = `(document.querySelector('[role="log"][aria-label="Transcript"]') || {}).textContent || ''`;
const LEDGER_LABELS = `Array.from(document.querySelectorAll('[role="region"][aria-label="Record ledger"] button[data-record-row]')).map((b) => b.getAttribute('aria-label'))`;
const PILL = `(Array.from(document.querySelectorAll('button')).map((b) => b.textContent.replace(/\\s+/g, ' ').trim()).find((t) => /Jump to latest/.test(t)) || null)`;
// The badge's visible words are the contract: its data-connection value is an internal state name (connected shows as "open").
const badgeText = `((document.querySelector('[data-connection]') || { textContent: '' }).textContent || '').trim()`;
const BADGE_CONNECTED = `${badgeText} === 'connected'`;
const BADGE_OFFLINE = `${badgeText} === 'offline'`;

// Watches the connection badge and reports every change through a DevTools binding.
const BADGE_WATCH = `(() => {
  let last = null;
  const read = () => {
    const badge = document.querySelector('[data-connection]');
    return badge ? badge.getAttribute('data-connection') + '|' + badge.textContent.trim() : null;
  };
  const check = () => {
    const now = read();
    if (now !== last) {
      last = now;
      window.__qaBadge(JSON.stringify({ state: now }));
    }
  };
  new MutationObserver(check).observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true });
  check();
  return true;
})()`;

// Replaces window.EventSource with a controllable fake. The snapshot is still the gateway's.
// The fake only listens; the app's own parsing and admission run on what it delivers.
const FAKE_STREAM = `(() => {
  const instances = [];
  window.__fakeStreams = instances;
  class FakeEventSource {
    constructor(url) {
      this.url = String(url);
      this.readyState = 0;
      this.withCredentials = false;
      this.onopen = null;
      this.onerror = null;
      this.onmessage = null;
      this.listeners = {};
      instances.push(this);
      setTimeout(() => {
        if (this.readyState === 2) return;
        this.readyState = 1;
        this.dispatch('open', new Event('open'));
      }, 0);
    }
    addEventListener(type, listener) {
      (this.listeners[type] = this.listeners[type] || []).push(listener);
    }
    removeEventListener(type, listener) {
      this.listeners[type] = (this.listeners[type] || []).filter((l) => l !== listener);
    }
    dispatch(type, event) {
      const handler = this['on' + type];
      if (typeof handler === 'function') handler.call(this, event);
      for (const listener of this.listeners[type] || []) listener.call(this, event);
    }
    close() {
      this.readyState = 2;
    }
  }
  FakeEventSource.CONNECTING = 0;
  FakeEventSource.OPEN = 1;
  FakeEventSource.CLOSED = 2;
  window.EventSource = FakeEventSource;
  window.__emitStream = (name, data) => {
    const source = instances[instances.length - 1];
    if (!source) throw new Error('no fake EventSource exists');
    source.dispatch(name, new MessageEvent(name, { data: JSON.stringify(data) }));
    return instances.length;
  };
})();`;

// Records the background at the first frames and at DOMContentLoaded, before any app script runs.
const FIRST_FRAME_HOOK = `(() => {
  window.__firstFrames = [];
  const sample = (tag) => {
    window.__firstFrames.push({
      tag,
      readyState: document.readyState,
      dataTheme: document.documentElement.getAttribute('data-theme'),
      html: getComputedStyle(document.documentElement).backgroundColor,
    });
  };
  document.addEventListener('DOMContentLoaded', () => sample('DOMContentLoaded'), { once: true });
  let frames = 0;
  const tick = () => {
    frames += 1;
    if (frames === 1) sample('firstFrame');
    if (frames < 3) requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
})();`;

// localStorage that throws on every use, installed before the page's scripts run.
const STORAGE_THROWS = `(() => {
  const boom = () => {
    throw new DOMException('${READ_STORAGE_FAILS_FLAG}', 'SecurityError');
  };
  for (const name of ['getItem', 'setItem', 'removeItem', 'clear', 'key']) {
    Object.defineProperty(Storage.prototype, name, { value: boom, configurable: true, writable: true });
  }
})();`;

let fakeScriptId = null;
let storageScriptId = null;

async function installFakeStream() {
  const { identifier } = await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: FAKE_STREAM });
  fakeScriptId = identifier;
}

async function removeFakeStream() {
  if (fakeScriptId !== null) await cdp.send('Page.removeScriptToEvaluateOnNewDocument', { identifier: fakeScriptId });
  fakeScriptId = null;
}

const FAKE_READY = `Boolean(window.__fakeStreams && window.__fakeStreams.length > 0 && window.__fakeStreams[window.__fakeStreams.length - 1].readyState === 1)`;

async function emit(name, data) {
  return evaluate(`window.__emitStream(${JSON.stringify(name)}, ${JSON.stringify(data)})`);
}

// One wire item, in the shape handler.go writes and core/validate.ts admits.
function itemFrame(epoch, sequence, fields) {
  return {
    schema_version: WIRE,
    cursor: `${epoch}:${sequence}`,
    kind: 'item',
    item: {
      schema_version: WIRE,
      sequence,
      occurred_at: new Date().toISOString(),
      authority: 'diagnostic',
      verification_scope: 'unverified-input',
      turn_id: 'turn-01',
      ...fields,
    },
  };
}

async function clickIn(scope, text) {
  const clicked = await evaluate(`(() => {
    const root = document.querySelector(${JSON.stringify(scope)});
    const button = root && Array.from(root.querySelectorAll('button')).find((b) => b.textContent.trim() === ${JSON.stringify(text)});
    if (!button) return false;
    button.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`no button "${text}" in ${scope}`);
  await sleep(200);
}

async function clickSelector(selector) {
  const clicked = await evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return false;
    el.click();
    return true;
  })()`);
  if (!clicked) throw new Error(`no element ${selector}`);
  await sleep(300);
}

async function gatewayAnswers() {
  return getJson(`${ORIGIN}/api/v1/contract`).then(
    () => true,
    () => false,
  );
}

// ---------- D: responsive ----------

// The open drawer's scrim: whether it is drawn, whether the header is inert, and how many header controls it covers.
const SCRIM_STATE = `(() => {
  const scrim = document.querySelector('[data-testid="scrim"]');
  const header = document.querySelector('header[aria-label="Workspace header"]');
  // Contract change (round 3): the theme switch is in the header's closed "More options" disclosure, so its buttons are not drawn
  // and are not header controls at this point. Only the drawn header buttons are counted, and each must sit under the scrim.
  const controls = header ? Array.from(header.querySelectorAll('button')).filter((b) => b.checkVisibility()) : [];
  const under = controls.filter((b) => {
    const r = b.getBoundingClientRect();
    return document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2) === scrim;
  });
  return { present: Boolean(scrim), headerInert: header ? header.hasAttribute('inert') : null, headerControls: controls.length, coveredByScrim: under.length };
})()`;

// A real pointer press at viewport coordinates (the scrim closes the drawer on a press, as Shell.tsx:236 says).
async function pointerPress(x, y) {
  await cdp.send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
  await cdp.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
  await sleep(400);
}

async function checkD(ctx) {
  await emulateMedia(LIGHT_OS);
  await navigate('/app/');
  await evaluate('localStorage.clear()');
  await viewport(1024);
  await navigate('/app/');
  const closed = await evaluate(DRAWER_STATE);
  ctx.note('drawer1024Closed', closed);
  ctx.expect(closed.toggleVisible && closed.expanded === 'false', `1024 px: the Governance toggle is ${closed.toggleVisible ? 'visible' : 'hidden'} with aria-expanded ${closed.expanded}`);
  ctx.expect(closed.inert === true && closed.ariaHidden === 'true' && closed.dataOpen === 'false', `1024 px: the closed drawer is not inert and aria-hidden (inert ${closed.inert}, aria-hidden ${closed.ariaHidden}, data-open ${closed.dataOpen})`);
  ctx.expect(closed.asideLeft !== null && closed.asideLeft >= closed.clientWidth, `1024 px: the closed drawer starts at x=${closed.asideLeft}, inside the ${closed.clientWidth} px client area`);

  await clickSelector(DRAWER_TOGGLE);
  await sleep(400);
  const open = await evaluate(DRAWER_STATE);
  ctx.note('drawer1024Open', open);
  ctx.expect(open.expanded === 'true' && open.inert !== true && open.dataOpen === 'true', `1024 px: the toggle did not open the drawer (aria-expanded ${open.expanded}, inert ${open.inert}, data-open ${open.dataOpen})`);
  ctx.expect(open.asideTop !== null && open.headerBottom !== null && open.asideTop >= open.headerBottom - 1, `1024 px: the drawer top ${open.asideTop} is above the header bottom ${open.headerBottom}`);
  // Contract change (redesign): the open drawer is modal. A scrim covers the page, the header included, and the header is
  // inert (Shell.tsx:233-237). The p4 expectation that header controls stay hit-testable no longer holds by design.
  const hits = await evaluate(HEADER_HIT);
  ctx.note('headerHitTestWhileOpen', hits);
  const scrimOpen = await evaluate(SCRIM_STATE);
  ctx.note('scrimWhileOpen', scrimOpen);
  ctx.expect(scrimOpen.present && scrimOpen.headerInert === true && scrimOpen.coveredByScrim === scrimOpen.headerControls, `1024 px: with the drawer open the scrim is ${scrimOpen.present ? 'present' : 'missing'}, the header inert is ${scrimOpen.headerInert}, and ${scrimOpen.coveredByScrim} of ${scrimOpen.headerControls} header controls sit under the scrim`);
  ctx.expect(hits.every((h) => !h.hit) && !(await evaluate(TOGGLE_HIT)), `1024 px: a header control is hit-testable while the modal drawer is open: ${JSON.stringify(hits.filter((h) => h.hit))}`);
  await capture('drawer-1024-open.png');

  await press('Escape');
  await sleep(400);
  const escaped = await evaluate(DRAWER_STATE);
  ctx.expect(escaped.expanded === 'false' && escaped.inert === true, 'Escape did not close the drawer');
  ctx.expect(escaped.activeIsToggle === true, `Escape did not return focus to the Governance toggle (focus on ${escaped.activeText})`);

  // A press on the scrim (not on the drawer) closes the drawer and returns focus to the Governance toggle.
  await clickSelector(DRAWER_TOGGLE);
  await sleep(300);
  const scrimAt = await evaluate(`(() => { const r = document.querySelector('[data-testid="scrim"]').getBoundingClientRect(); return { x: Math.round(r.left + 60), y: Math.round(r.top + 300) }; })()`);
  await pointerPress(scrimAt.x, scrimAt.y);
  const scrimClosed = await evaluate(DRAWER_STATE);
  ctx.note('scrimPress', { at: scrimAt, expanded: scrimClosed.expanded, inert: scrimClosed.inert, activeIsToggle: scrimClosed.activeIsToggle });
  ctx.expect(scrimClosed.expanded === 'false' && scrimClosed.inert === true && scrimClosed.activeIsToggle === true, `a press on the scrim did not close the drawer and return focus to the toggle (expanded ${scrimClosed.expanded}, inert ${scrimClosed.inert}, focus on ${scrimClosed.activeText})`);

  await clickSelector(DRAWER_TOGGLE);
  await sleep(300);
  await viewport(1440);
  await sleep(400);
  await viewport(1024);
  await sleep(400);
  const narrowAgain = await evaluate(DRAWER_STATE);
  ctx.expect(narrowAgain.expanded === 'false' && narrowAgain.inert === true, `widening and then narrowing reopened the drawer (aria-expanded ${narrowAgain.expanded})`);

  await viewport(390);
  await navigate('/app/');
  const phoneChat = await evaluate(PHONE_STATE);
  ctx.note('phoneFirstLoadChat', phoneChat);
  ctx.expect(phoneChat.scrollWidth <= phoneChat.clientWidth, `390 px: horizontal overflow (scrollWidth ${phoneChat.scrollWidth} > clientWidth ${phoneChat.clientWidth})`);
  ctx.expect(phoneChat.scrollY === 0, `390 px: the first load of the chat view has scrollY ${phoneChat.scrollY}`);
  ctx.expect(phoneChat.overflowingChips.length === 0, `390 px: chips overflow the viewport: ${JSON.stringify(phoneChat.overflowingChips)}`);
  await clickSelector('#view-tab-trajectory');
  const phoneTraj = await evaluate(PHONE_STATE);
  ctx.note('phoneFirstShowTrajectory', phoneTraj);
  ctx.expect(phoneTraj.scrollY === 0, `390 px: the trajectory view has scrollY ${phoneTraj.scrollY} when first shown`);
  ctx.expect(phoneTraj.scrollWidth <= phoneTraj.clientWidth, `390 px: horizontal overflow in the trajectory view (scrollWidth ${phoneTraj.scrollWidth})`);
  ctx.expect(phoneTraj.overflowingChips.length === 0, `390 px trajectory: chips overflow the viewport: ${JSON.stringify(phoneTraj.overflowingChips)}`);

  // Screenshots: 1440, 1024 and 390 px, light and dark, both views.
  const shots = [];
  for (const theme of ['Light', 'Dark']) {
    await viewport(1440);
    await navigate('/app/');
    await clickIn(THEME_TOOLBAR, theme);
    for (const width of [1440, 1024, 390]) {
      await viewport(width);
      await navigate('/app/');
      shots.push(await capture(`${width}-${theme.toLowerCase()}-chat.png`));
      await clickSelector('#view-tab-trajectory');
      shots.push(await capture(`${width}-${theme.toLowerCase()}-trajectory.png`));
    }
  }
  await evaluate('localStorage.clear()');
  ctx.note('screenshots', shots);
  ctx.note('summary', `1024 px drawer: aria-expanded and inert track the state, hit-testable header, Escape returns focus; 390 px: no overflow, scrollY 0; ${shots.length} screenshots`);
}

// ---------- E: theme ----------

async function checkE(ctx) {
  await viewport(1440);
  await emulateMedia(LIGHT_OS);
  await navigate('/app/');
  await evaluate('localStorage.clear()');

  // E1: each choice is saved, survives a reload, and System restores the OS choice.
  await clickIn(THEME_TOOLBAR, 'Dark');
  const dark = await evaluate(THEME_STATE);
  ctx.expect(dark.stored === 'dark' && dark.dataTheme === 'dark', `choosing Dark stored ${JSON.stringify(dark.stored)} with data-theme ${dark.dataTheme}`);
  await navigate('/app/');
  const darkReloaded = await evaluate(THEME_STATE);
  ctx.expect(darkReloaded.dataTheme === 'dark' && darkReloaded.pressed.includes('Dark=true'), `Dark did not survive a reload (${JSON.stringify(darkReloaded.pressed)})`);
  await clickIn(THEME_TOOLBAR, 'Light');
  await navigate('/app/');
  const lightReloaded = await evaluate(THEME_STATE);
  ctx.expect(lightReloaded.stored === 'light' && lightReloaded.dataTheme === 'light' && lightReloaded.pressed.includes('Light=true'), `Light did not survive a reload (${JSON.stringify(lightReloaded)})`);
  await clickIn(THEME_TOOLBAR, 'System');
  await navigate('/app/');
  const systemReloaded = await evaluate(THEME_STATE);
  ctx.expect(systemReloaded.dataTheme === null && systemReloaded.pressed.includes('System=true'), `System did not restore the OS choice after a reload (data-theme ${systemReloaded.dataTheme})`);
  ctx.note('choices', { dark: darkReloaded, light: lightReloaded, system: systemReloaded });

  // E2: localStorage throws on every use. The page must still render, default to system, and apply a choice for the session.
  const beforeBlocked = observed.console.length;
  ({ identifier: storageScriptId } = await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: STORAGE_THROWS }));
  await navigate('/app/');
  const blocked = await evaluate(THEME_STATE);
  const blockedShell = await evaluate(`({ header: Boolean(document.querySelector('${APP_HEADER}')), chat: Boolean(document.getElementById('view-panel-chat')) })`);
  ctx.note('blockedStorage', { shell: blockedShell, theme: blocked });
  ctx.expect(blockedShell.header && blockedShell.chat, 'the page did not render with localStorage throwing');
  // Contract change (round 3, the dark default): with nothing usable stored, the page renders on dark (theme.ts DEFAULT_THEME and
  // public/theme-boot.js). System is no longer the default, so the expectation is Dark.
  ctx.expect(blocked.dataTheme === 'dark' && blocked.pressed.includes('Dark=true'), `with storage blocked the page did not default to dark (data-theme ${blocked.dataTheme}, ${JSON.stringify(blocked.pressed)})`);
  await clickIn(THEME_TOOLBAR, 'Dark');
  const blockedDark = await evaluate(THEME_STATE);
  ctx.expect(blockedDark.dataTheme === 'dark', 'choosing Dark with storage blocked did not apply for the session');
  const blockedErrors = observed.console.slice(beforeBlocked).filter((e) => ['error', 'warning', 'exception'].includes(e.level));
  ctx.expect(blockedErrors.length === 0, `blocked storage produced ${blockedErrors.length} console error(s) or warning(s): ${blockedErrors.map((e) => e.text).join(' | ')}`);
  await cdp.send('Page.removeScriptToEvaluateOnNewDocument', { identifier: storageScriptId });
  storageScriptId = null;
  // The override is still on this document's Storage.prototype, so a fresh document is needed before E3 writes storage.
  await navigate('/app/');

  // E3: no flash. The first frames and DOMContentLoaded must already show the final background.
  const hook = await cdp.send('Page.addScriptToEvaluateOnNewDocument', { source: FIRST_FRAME_HOOK });
  const cases = [
    { label: 'stored light, OS dark', stored: 'light', os: DARK_OS },
    { label: 'stored dark, OS light', stored: 'dark', os: LIGHT_OS },
    { label: 'nothing stored, OS dark', stored: null, os: DARK_OS },
    { label: 'nothing stored, OS light', stored: null, os: LIGHT_OS },
  ];
  const TRIALS = 5;
  const flashes = [];
  for (const item of cases) {
    let flashed = 0;
    let finalCanvas = null;
    const samples = [];
    for (let trial = 0; trial < TRIALS; trial += 1) {
      await emulateMedia(item.os);
      await evaluate(item.stored === null ? `localStorage.removeItem('${THEME_KEY}')` : `localStorage.setItem('${THEME_KEY}', '${item.stored}')`);
      await navigate('/app/');
      const frames = await evaluate('window.__firstFrames');
      const final = await evaluate(THEME_STATE);
      finalCanvas = final.canvas;
      const first = frames.find((f) => f.tag === 'firstFrame');
      const loaded = frames.find((f) => f.tag === 'DOMContentLoaded');
      const wrongFirst = Boolean(first && first.html !== final.canvas);
      const wrongLoaded = Boolean(loaded && loaded.html !== final.canvas);
      if (wrongFirst || wrongLoaded) flashed += 1;
      samples.push(frames.map((f) => `${f.tag}=${f.html}`).join(' '));
    }
    flashes.push({ case: item.label, final: finalCanvas, trials: TRIALS, flashed, samples });
    ctx.expect(flashed === 0, `${item.label}: ${flashed} of ${TRIALS} loads painted a background other than the final ${finalCanvas} before the theme applied (${samples[0]})`);
  }
  ctx.note('firstPaintSamples', flashes);
  await cdp.send('Page.removeScriptToEvaluateOnNewDocument', { identifier: hook.identifier });
  await emulateMedia(LIGHT_OS);
  await evaluate('localStorage.clear()');
  await navigate('/app/');
  ctx.note('summary', `Dark, Light and System persist across reloads; with storage blocked the page renders on system; first frames match the final canvas in ${cases.length} cases`);
}

// ---------- F: live behaviour with an injected stream ----------

async function checkF(ctx) {
  const snapshot = await getJson(`${ORIGIN}/api/v1/snapshot`);
  const epoch = snapshot.epoch;
  const last = snapshot.items[snapshot.items.length - 1];
  ctx.note('snapshot', { epoch, cursor: snapshot.cursor, source: snapshot.source, items: snapshot.items.length, lastSequence: last.sequence });
  await viewport(1440);
  await emulateMedia(LIGHT_OS);
  await navigate('/app/');
  await evaluate('localStorage.clear()');
  await installFakeStream();
  try {
    await navigate('/app/');
    ctx.expect(await waitFor(FAKE_READY, 8000), 'the fake EventSource did not open');
    ctx.note('countAtStart', await evaluate(ALL_COUNT));

    // Phase 1: admission of LOST, lossy success, DENY and an UNKNOWN verdict.
    await emit('item', itemFrame(epoch, 19, { id: 'qa-res-19', type: 'result', tool: 'bash', tool_call_id: 'qa-call-19', state: 'lost', summary: 'QA outcome not recorded' }));
    await emit('item', itemFrame(epoch, 20, { id: 'qa-res-20', type: 'result', tool: 'bash', tool_call_id: 'qa-call-20', state: 'succeeded', lossy: true, summary: 'QA lossy output' }));
    await emit('item', itemFrame(epoch, 21, { id: 'qa-dec-21', type: 'decision', tool: 'bash', tool_call_id: 'qa-call-21', verdict: 'DENY', reason_code: 'policy_rule_deny', summary: 'QA denied' }));
    await emit('item', itemFrame(epoch, 22, { id: 'qa-dec-22', type: 'decision', tool: 'bash', tool_call_id: 'qa-call-22', verdict: 'UNKNOWN', reason_code: 'policy_unknown', summary: 'QA unknown verdict' }));
    await sleep(500);
    const afterAdmit = await evaluate(ALL_COUNT);
    const chips = await evaluate(CHIPS);
    ctx.note('chatChips', [...new Set(chips.map((c) => `${c.text}:${c.tone}`))]);
    ctx.expect(afterAdmit === 22, `after four admissions the count is ${afterAdmit}, expected 22`);
    ctx.expect(chips.some((c) => c.text === 'lost' && c.tone === 'lost'), 'the LOST result has no lost chip in the chat view');
    ctx.expect(chips.some((c) => c.text === 'DENY' && c.tone === 'deny'), 'the DENY decision has no deny chip');
    ctx.expect(chips.some((c) => c.text === 'UNKNOWN' && c.tone === 'unknown'), 'the UNKNOWN verdict has no unknown chip');
    ctx.expect(chips.some((c) => /lossy/.test(c.text) && c.tone !== 'ok'), 'the lossy success has no lossy chip that avoids the success tone');
    const okTone = await evaluate(`document.querySelectorAll('[data-tone="ok"]').length`);
    ctx.expect(okTone === 0, `${okTone} element(s) carry the success tone on an unverified fixture projection`);
    const allowChip = chips.find((c) => c.text === 'ALLOW');
    ctx.expect(allowChip && allowChip.tone === 'allow', `the ALLOW chip has tone ${allowChip ? allowChip.tone : 'missing'}, expected its own tone (not success)`);

    await evaluate(`window.scrollTo(0, document.documentElement.scrollHeight)`);
    await sleep(300);
    await capture('states-light.png');
    await clickIn(THEME_TOOLBAR, 'Dark');
    await sleep(300);
    await capture('states-dark.png');
    await clickIn(THEME_TOOLBAR, 'Light');

    await clickSelector('#view-tab-trajectory');
    const labels = await evaluate(LEDGER_LABELS);
    const lostRow = labels.find((l) => /Result record 19/.test(l)) ?? null;
    const lossyRow = labels.find((l) => /Result record 20/.test(l)) ?? null;
    ctx.note('ledgerRows', { lost: lostRow, lossy: lossyRow });
    ctx.expect(lostRow !== null && /LOST, not success/.test(lostRow), `the LOST row does not read "LOST, not success": ${lostRow}`);
    ctx.expect(lossyRow !== null && /output lossy/.test(lossyRow), `the lossy row does not say "output lossy": ${lossyRow}`);
    ctx.expect(lossyRow !== null && /output lossy, not counted as confirmed/.test(lossyRow), `the lossy row reads "${lossyRow}", not "output lossy, not counted as confirmed". That phrase is the default branch at src/core/trajectory.ts:160 (a lossy success with the unknown tone); this unverified fixture takes the reported branch at trajectory.ts:152`);
    const shapes = await evaluate(DOT_SHAPES(['lost', 'deny', 'unknown', 'allow', 'reported', 'pending']));
    ctx.note('dotShapes', shapes);
    ctx.expect(/polygon/.test(shapes.lost?.clipPath ?? ''), 'LOST is not drawn as a diamond (no polygon clip-path on its dot)');
    ctx.expect(shapes.deny !== null && shapes.lost !== null && (shapes.deny.radius !== shapes.lost.radius || shapes.deny.clipPath !== shapes.lost.clipPath), 'the LOST dot does not differ from the DENY square');
    const laneTones = await evaluate(`Array.from(document.querySelectorAll('[role="region"][aria-label="Record ledger"] span[data-tone]')).map((s) => s.getAttribute('data-tone'))`);
    ctx.expect(!laneTones.includes('ok'), 'the ledger carries the success tone for an unverified fixture');
    // The observation streams are in the details card (hidden until the Details control opens it, but in the page).
    const rail = await evaluate(`(document.getElementById('helper-bar') || {}).textContent || ''`);
    ctx.expect(/unavailable/.test(rail), 'no "unavailable" reading for the production supervisor projection');
    await clickSelector('#view-tab-chat');

    // Phase 2: a verified-looking observed item is refused (a fixture projection), with a recovery snapshot.
    const refusedAt = performance.now();
    await emit('item', itemFrame(epoch, 23, { id: 'qa-res-23', type: 'result', tool: 'bash', tool_call_id: 'qa-call-23', state: 'succeeded', lossy: false, authority: 'observed', verification_scope: 'structural-audit-only', summary: 'QA observed claim' }));
    const refusalSnapshot = await poll(async () => observed.timeline.some((e) => e.kind === 'request' && e.at >= refusedAt && /\/api\/v1\/snapshot/.test(e.url)), 8000, 100);
    ctx.expect(refusalSnapshot, 'the refused observed item did not trigger a recovery snapshot');
    const refusalNotice = await waitFor(`${NOTICE_GAP}.some((t) => /invalid item/.test(t))`, 8000);
    ctx.expect(refusalNotice, `no "Continuity gap (invalid item)" notice after the refusal: ${JSON.stringify(await evaluate(NOTICE_GAP))}`);
    const countAfterRefusal = await waitFor(`(${ALL_COUNT}) === ${snapshot.items.length}`, 8000);
    ctx.expect(countAfterRefusal, `after the recovery snapshot the count is ${await evaluate(ALL_COUNT)}, expected ${snapshot.items.length}`);
    ctx.expect(!(await evaluate(LOG_TEXT)).includes('QA observed claim'), 'the refused observed item was applied');
    ctx.expect(await waitFor(FAKE_READY, 8000), 'the stream did not reopen after the recovery snapshot');
    ctx.note('refusal', { notices: await evaluate(NOTICE_GAP), count: await evaluate(ALL_COUNT) });

    // Phase 3: an exact duplicate changes nothing; a gapped sequence is not applied, and triggers one recovery.
    const beforeDup = await evaluate(ALL_COUNT);
    const dupAt = performance.now();
    await emit('item', { schema_version: WIRE, cursor: `${epoch}:${last.sequence}`, kind: 'item', item: last });
    await sleep(1500);
    const dupSnapshots = observed.timeline.filter((e) => e.kind === 'request' && e.at >= dupAt && /\/api\/v1\/snapshot/.test(e.url)).length;
    const afterDup = await evaluate(ALL_COUNT);
    ctx.expect(afterDup === beforeDup && dupSnapshots === 0, `a duplicate changed the count (${beforeDup} -> ${afterDup}) or caused ${dupSnapshots} snapshot fetch(es)`);
    const gapAt = performance.now();
    await emit('item', itemFrame(epoch, last.sequence + 3, { id: 'qa-gap-21', type: 'result', tool: 'bash', tool_call_id: 'qa-call-gap', state: 'succeeded', lossy: false, summary: 'QA gapped record' }));
    const gapSnapshot = await poll(async () => observed.timeline.some((e) => e.kind === 'request' && e.at >= gapAt && /\/api\/v1\/snapshot/.test(e.url)), 10000, 100);
    ctx.expect(gapSnapshot, 'the gapped sequence did not trigger a recovery snapshot');
    const gapNotice = await waitFor(`${NOTICE_GAP}.some((t) => /sequence gap/.test(t))`, 10000);
    ctx.expect(gapNotice, `no "Continuity gap (sequence gap)" notice: ${JSON.stringify(await evaluate(NOTICE_GAP))}`);
    const gapNotices = await evaluate(NOTICE_GAP);
    ctx.expect(gapNotices.length === 1, `${gapNotices.length} Continuity gap notices, expected one`);
    ctx.expect(!(await evaluate(LOG_TEXT)).includes('QA gapped record'), 'the gapped record was applied');
    ctx.expect(await waitFor(`(${ALL_COUNT}) === ${snapshot.items.length}`, 8000), `the count after the gap is ${await evaluate(ALL_COUNT)}, expected ${snapshot.items.length}`);
    ctx.expect((await evaluate(`document.querySelectorAll('[data-tone="ok"]').length`)) === 0, 'a success tone appeared after the gap (a success was invented)');
    ctx.expect(await waitFor(FAKE_READY, 8000), 'the stream did not reopen after the gap recovery');
    ctx.note('gap', { notices: gapNotices, count: await evaluate(ALL_COUNT), duplicateSnapshotFetches: dupSnapshots });

    // Phase 4: three new records while the reader is away from the tail. Contract change (redesign): at 1440 px the page
    // does not scroll; the transcript is its own scroll container. The reader goes to the tail, then away from it, and the
    // count is measured from that point (a reader who never reached the tail would count every record since load).
    const LOG = `document.querySelector('[role="log"][aria-label="Transcript"]')`;
    // Contract change (design pass): a run of three or more tool actions is one closed group line, so the demo transcript is
    // short. Open the group, so the transcript is long enough to scroll away from.
    await clickSelector('[data-group] > button');
    await sleep(200);
    await evaluate(`(() => { const l = ${LOG}; l.scrollTop = l.scrollHeight; return true; })()`);
    await sleep(300);
    await evaluate(`(() => { const l = ${LOG}; l.scrollTop = 0; return true; })()`);
    await sleep(300);
    const scrollable = await evaluate(`(() => { const l = ${LOG}; return l.scrollHeight - l.clientHeight; })()`);
    ctx.expect(scrollable > 200, `the transcript is too short to scroll away (${scrollable} px of scroll)`);
    const scrollBefore = await evaluate(`${LOG}.scrollTop`);
    await evaluate(`(() => {
      window.__announce = [];
      const read = () => (document.querySelector('[role="status"][aria-live="polite"]') || {}).textContent ?? null;
      let previous = read();
      new MutationObserver(() => {
        const now = read();
        if (now !== previous) {
          previous = now;
          window.__announce.push(now);
        }
      }).observe(document.body, { subtree: true, childList: true, characterData: true });
      return true;
    })()`);
    await emit('item', itemFrame(epoch, last.sequence + 1, { id: 'qa-pill-19', type: 'message', role: 'assistant', text: 'QA new record one' }));
    await sleep(60);
    await emit('item', itemFrame(epoch, last.sequence + 2, { id: 'qa-pill-20', type: 'message', role: 'assistant', text: 'QA new record two' }));
    await sleep(60);
    await emit('item', itemFrame(epoch, last.sequence + 3, { id: 'qa-pill-21', type: 'message', role: 'assistant', text: 'QA new record three' }));
    await sleep(1500);
    const pill = await evaluate(PILL);
    const scrollAfter = await evaluate(`${LOG}.scrollTop`);
    const announced = (await evaluate('window.__announce')).filter((t) => t && /new record/.test(t));
    ctx.note('pill', { text: pill, scrollBefore, scrollAfter, announcements: announced });
    ctx.expect(pill === '3 new records · Jump to latest', `the pill reads ${JSON.stringify(pill)}, expected "3 new records · Jump to latest"`);
    ctx.expect(scrollAfter === scrollBefore, `the transcript scroll moved from ${scrollBefore} to ${scrollAfter} while scrolled away`);
    ctx.expect(announced.length === 1 && /3 new records below/.test(announced[0]), `the announcer said ${JSON.stringify(announced)}, expected one "3 new records below" announcement`);
    await capture('pill-away.png');
    await evaluate(`(Array.from(document.querySelectorAll('button')).find((b) => /Jump to latest/.test(b.textContent)) || { click() {} }).click()`);
    await sleep(500);
    const jumped = await evaluate(`(() => { const l = ${LOG}; return { y: l.scrollTop, h: l.scrollHeight, inner: l.clientHeight, focusRole: document.activeElement ? document.activeElement.getAttribute('role') : null, pill: Array.from(document.querySelectorAll('button')).some((b) => /Jump to latest/.test(b.textContent)) }; })()`);
    ctx.note('afterJump', jumped);
    ctx.expect(jumped.y + jumped.inner >= jumped.h - 40 && !jumped.pill, `Jump did not reach the bottom of the transcript or remove the pill (${JSON.stringify(jumped)})`);
    ctx.expect(jumped.focusRole === 'log', `Jump left focus on role ${jumped.focusRole}, expected the transcript log`);
    ctx.note('summary', `LOST chip and "LOST, not success" row; lossy row reads "output lossy, not counted as confirmed"; DENY, UNKNOWN and ALLOW tones distinct; observed item refused (invalid item) with recovery; duplicate no-op; gap recovers once; pill "3 new records" with scroll kept, one announcement, Jump works`);
  } finally {
    await removeFakeStream();
  }
}

// ---------- G: reconnect and offline ----------

async function checkG(ctx) {
  if (!gatewayCommands.stop || !gatewayCommands.start) {
    ctx.unverify('--gateway-stop and --gateway-start were not supplied, so the outage, the restart and the offline run were not performed');
    return;
  }
  await viewport(1440);
  await emulateMedia(LIGHT_OS);
  await navigate('/app/');
  ctx.note('badgeAtStart', await evaluate(`(document.querySelector('[data-connection]') || {}).textContent || null`));
  ctx.expect(await waitFor(BADGE_CONNECTED, 15000), 'the badge did not reach connected before the outage');
  await evaluate(BADGE_WATCH);
  try {
    // Outage: the gateway is stopped. The client retries with growing, jittered delays, capped at 10 s.
    const stopAt = performance.now();
    const stop = await runGatewayCommandAsync('stop', gatewayCommands.stop);
    ctx.note('stopCommandStatus', stop.status);
    const stopped = await poll(async () => !(await gatewayAnswers()), 20000, 200);
    ctx.expect(stopped, 'the gateway still answers after the stop command');
    await sleep(32000);
    // An attempt starts with its request and ends with its failure. The backoff is the gap from one attempt's end to the next start.
    const snapshotEvents = observed.timeline.filter((e) => e.at >= stopAt && /\/api\/v1\/snapshot/.test(e.url ?? '') && e.kind !== 'badge');
    const starts = snapshotEvents.filter((e) => e.kind === 'request');
    const ends = snapshotEvents.filter((e) => e.kind !== 'request');
    const attempts = starts.map((s) => {
      const end = ends.find((e) => e.at >= s.at);
      return { start: s.at, end: end ? end.at : null };
    });
    const backoffs = attempts.slice(0, -1).map((a, i) => (a.end === null ? null : Math.round(attempts[i + 1].start - a.end)));
    const durations = attempts.map((a) => (a.end === null ? null : Math.round(a.end - a.start)));
    ctx.note('retryAttempts', attempts.length);
    ctx.note('backoffMs', backoffs);
    ctx.note('attemptDurationMs', durations);
    // Base 500 ms, factor 2, cap 10 s, jitter in [50%, 100%] (backoff.ts:37-39). Step n waits 500 ms x 2^n, capped
    // at 10 s and scaled into [50%, 100%]. A drop schedules step 0 (250-500 ms) before the first request, and no gap
    // is measured before that request. Each failed retry then schedules the next step, so gap k (0-based) is step k+1,
    // and the bands below start at step 1 (500-1000 ms). They allow 150 ms below and 350 ms above.
    const bound = (step) => Math.min(10000, 500 * 2 ** step);
    const outside = backoffs
      .map((g, k) => ({ k, g, lo: Math.round(bound(k + 1) * 0.5 - 150), hi: bound(k + 1) + 350 }))
      .filter(({ g, lo, hi }) => g !== null && (g < lo || g > hi));
    ctx.expect(attempts.length >= 4, `only ${attempts.length} retry attempt(s) in 32 s of outage`);
    ctx.expect(outside.length === 0, `retry backoff outside the jittered band (base 500 ms, factor 2, cap 10 s): ${JSON.stringify(outside)}`);
    const outageBadges = observed.timeline.filter((e) => e.kind === 'badge' && e.at >= stopAt);
    ctx.note('outageBadgeStates', [...new Set(outageBadges.map((e) => e.state))]);
    ctx.expect(outageBadges.some((e) => /reconnecting/.test(e.state)), 'the badge never showed reconnecting during the outage');
    ctx.expect(!outageBadges.some((e) => /\|connected/.test(e.state)), 'the badge showed connected during the outage');

    // Restart on the same port. 'connected' may only appear after the event stream answered.
    const restartAt = performance.now();
    const restart = runGatewayCommand('start', gatewayCommands.start);
    ctx.note('startCommandStatus', restart.status);
    ctx.expect(await poll(gatewayAnswers, 30000, 300), 'the restarted gateway does not answer');
    ctx.expect(await waitFor(BADGE_CONNECTED, 25000), 'the badge did not return to connected after the restart');
    const after = observed.timeline.filter((e) => e.at >= restartAt);
    const eventsIdx = after.findIndex((e) => e.kind === 'response' && /\/api\/v1\/events/.test(e.url) && e.status === 200);
    const connIdx = after.findIndex((e) => e.kind === 'badge' && /\|connected/.test(e.state));
    const snapshotsAfter = after.filter((e) => e.kind === 'request' && /\/api\/v1\/snapshot/.test(e.url)).length;
    ctx.note('recovery', { snapshotFetchesAfterRestart: snapshotsAfter, eventsResponseIndex: eventsIdx, connectedBadgeIndex: connIdx });
    ctx.expect(snapshotsAfter >= 1 && eventsIdx >= 0 && connIdx > eventsIdx, `'connected' appeared at index ${connIdx}, not after the event stream's 200 at index ${eventsIdx}`);

    // Offline: no requests while offline; an immediate retry when back online.
    await cdp.send('Network.emulateNetworkConditions', { offline: true, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    const offlineAt = performance.now();
    ctx.expect(await waitFor(BADGE_OFFLINE, 8000), 'the badge did not show offline');
    await sleep(6000);
    const whileOffline = observed.timeline.filter((e) => e.kind === 'request' && e.at > offlineAt + 500 && e.url.startsWith(ORIGIN));
    ctx.note('requestsWhileOffline', whileOffline.length);
    ctx.expect(whileOffline.length === 0, `${whileOffline.length} request(s) to the gateway while offline (expected 0)`);
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    const onlineAt = performance.now();
    const retry = await poll(async () => observed.timeline.find((e) => e.kind === 'request' && e.at >= onlineAt && /\/api\/v1\/(snapshot|events)/.test(e.url)), 4000, 50);
    ctx.note('firstRequestAfterOnlineMs', retry ? Math.round(retry.at - onlineAt) : null);
    ctx.expect(retry, 'no retry within 4 s of coming back online');
    ctx.expect(await waitFor(BADGE_CONNECTED, 15000), 'the badge did not return to connected after coming back online');
    ctx.note('summary', `${attempts.length} retry attempts, backoffs ${backoffs.join(', ')} ms; connected only after the event stream answered; ${whileOffline.length} requests while offline; first request ${retry ? Math.round(retry.at - onlineAt) : '?'} ms after online`);
  } finally {
    await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
    if (!(await gatewayAnswers())) runGatewayCommand('start', gatewayCommands.start);
  }
}

// ---------- H: forced colours and reduced motion ----------

async function checkH(ctx) {
  const snapshot = await getJson(`${ORIGIN}/api/v1/snapshot`);
  const TONES = ['allow', 'deny', 'reported', 'unknown', 'lost', 'pending'];
  await viewport(1440);
  await emulateMedia(LIGHT_OS);
  await installFakeStream();
  try {
    await navigate('/app/');
    ctx.expect(await waitFor(FAKE_READY, 8000), 'the fake EventSource did not open');
    await emit('item', itemFrame(snapshot.epoch, snapshot.items[snapshot.items.length - 1].sequence + 1, { id: 'qa-res-h', type: 'result', tool: 'bash', tool_call_id: 'qa-call-h', state: 'lost', summary: 'QA outcome not recorded' }));
    await sleep(300);
    await clickSelector('#view-tab-trajectory');
    const normal = await evaluate(DOT_SHAPES(TONES));
    await emulateMedia([{ name: 'forced-colors', value: 'active' }, { name: 'prefers-reduced-motion', value: 'reduce' }]);
    await sleep(400);
    const forced = await evaluate(DOT_SHAPES(TONES));
    const media = await evaluate(`({ forcedColors: matchMedia('(forced-colors: active)').matches, reducedMotion: matchMedia('(prefers-reduced-motion: reduce)').matches })`);
    ctx.note('emulationActive', media);
    if (!media.forcedColors || !media.reducedMotion) ctx.unverify(`the page does not report the emulation (forced-colors ${media.forcedColors}, reduced-motion ${media.reducedMotion})`);
    const signature = (s) => (s ? [s.borderStyle, s.borderWidth, s.radius, s.clipPath, s.mark].join(' | ') : 'missing');
    const forcedSignatures = Object.fromEntries(TONES.map((t) => [t, signature(forced[t])]));
    ctx.note('normalShapes', Object.fromEntries(TONES.map((t) => [t, signature(normal[t])])));
    ctx.note('forcedShapes', forcedSignatures);
    const distinct = new Set(Object.values(forcedSignatures)).size;
    ctx.expect(!Object.values(forcedSignatures).includes('missing') && distinct === TONES.length, `under forced colours the ${TONES.length} ledger states share shapes (${distinct} distinct): ${JSON.stringify(forcedSignatures)}`);
    ctx.expect(forced.allow?.mark !== 'none' && forced.pending?.mark === 'none', `under forced colours ALLOW must keep its centred dot and PENDING must not have one: allow=${forced.allow?.mark} pending=${forced.pending?.mark}`);
    await capture('forced-colors-trajectory.png');

    // Reduced motion: the drawer transition must not run. A control without the preference must show one.
    await viewport(1024);
    await emulateMedia([{ name: 'prefers-reduced-motion', value: 'reduce' }]);
    await navigate('/app/');
    await clickSelector(DRAWER_TOGGLE);
    const reduced = await evaluate(MOTION_STATE);
    ctx.note('reducedMotion', reduced);
    ctx.expect(reduced.runningAnimations === 0 && reduced.animatedElements === 0, `under reduced motion ${reduced.runningAnimations} animation(s) ran and ${reduced.animatedElements} element(s) carry a transition or animation (${reduced.animatedSample.join(', ')})`);
    await emulateMedia(MOTION_OFF);
    await navigate('/app/');
    await clickSelector(DRAWER_TOGGLE);
    const control = await evaluate(MOTION_STATE);
    ctx.note('motionControl', control);
    if (!control.asideTransition || !/0\.3s|300ms/.test(control.asideTransition)) {
      ctx.unverify(`the control without reduced motion shows the drawer transition as ${control.asideTransition}, so the measurement cannot be trusted`);
    }
    ctx.note('summary', `forced colours: ${distinct} of ${TONES.length} ledger shapes distinct; reduced motion: ${reduced.runningAnimations} running animation(s), control transition ${control.asideTransition}`);
  } finally {
    await emulateMedia(NO_OVERRIDE);
    await removeFakeStream();
  }
}

// ---------- I: console, CSP and network across the whole run ----------

async function checkI(ctx) {
  const issues = observed.console.filter((e) => ['error', 'warning', 'exception'].includes(e.level));
  const outsideOutage = issues.filter((e) => e.phase !== 'G');
  const outage = issues.filter((e) => e.phase === 'G');
  const external = observed.requests.filter((r) => !r.url.startsWith(ORIGIN) && !/^(data|blob):/.test(r.url));
  ctx.note('requests', { total: observed.requests.length, toGateway: observed.requests.filter((r) => r.url.startsWith(ORIGIN)).length, external: external.length });
  ctx.note('consoleErrorsAndWarnings', { outsideGOutage: outsideOutage.length, inGOutage: outage.length, inGOutageSamples: [...new Set(outage.map((e) => e.text))].slice(0, 4) });
  ctx.note('cspViolations', observed.csp.length);
  ctx.expect(observed.csp.length === 0, `${observed.csp.length} CSP violation(s): ${observed.csp.slice(0, 3).map((c) => c.text).join(' | ')}`);
  ctx.expect(outsideOutage.length === 0, `${outsideOutage.length} console error(s) or warning(s) outside the G outage: ${outsideOutage.slice(0, 5).map((e) => `[${e.phase}] ${e.level} ${e.text}`).join(' | ')}`);
  ctx.expect(external.length === 0, `${external.length} request(s) off the gateway origin: ${external.slice(0, 3).map((r) => r.url).join(' | ')}`);
  ctx.note('summary', `${observed.csp.length} CSP violations; ${outsideOutage.length} console errors or warnings outside check G; ${outage.length} inside G's outage and offline window; ${external.length} requests off the gateway origin (${observed.requests.length} requests in all)`);
}


// ---------- J: the user's three points, at six widths, light and dark ----------

const POINT_SIZES = [
  [1440, 900],
  [1180, 800],
  [1024, 768],
  [768, 1024],
  [390, 844],
  [360, 640],
];
const SESSIONS_NOTE = 'Other Pi sessions are not listed. This page receives one session from the supervisor. A session list needs the supervisor session service, which is not available in this build.';
const COMPOSER_SECTION = 'Message Pi: not available in this build';
const PLACEHOLDER = 'Submitting turns is not available in this build.';
const PREFS_KEY = 'tbound.webchat.ui.v1';
const TEXT_FN = `const t = (el) => (el ? (el.textContent || '').replace(/\\s+/g, ' ').trim() : '');`;
const VISIBLE_FN = `const visible = (el) => { if (!el) return false; const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; };`;

// Sessions rail, Pi identity, message chrome, the composer and the footer, as the page renders them now.
const POINT_STATE = `(() => {
  ${TEXT_FN}
  ${VISIBLE_FN}
  const nav = document.querySelector('nav[aria-label="Sessions and streams"]');
  const list = document.querySelector('ul[aria-label="Sessions"]');
  const rows = list ? Array.from(list.querySelectorAll(':scope > li')) : [];
  // The desktop toggle is in the header's brand row, and the phone's is the strip in the nav (Shell.tsx, Rail.tsx). Round 3: J read
  // the header only, so the phone strip was missed (aria-expanded null). Either one, whichever is drawn.
  const railToggle = Array.from(document.querySelectorAll('header[aria-label="Workspace header"] button[aria-controls="rail-body"], nav[aria-label="Sessions and streams"] button[aria-controls="rail-body"]')).find(visible) || null;
  const body = document.getElementById('rail-body');
  // The Pi identity and the status are in the details card (hidden until opened, but in the page); the chat's first row is its toolbar.
  const conv = document.getElementById('helper-bar');
  const toolbar = document.querySelector('[class*="_toolbar_"]');
  // The session title is the workspace heading, in the header's title row (Shell.tsx). The chat head is the conversation block.
  const head = document.querySelector('header[aria-label="Workspace header"] h2');
  const header = document.querySelector('header[aria-label="Workspace header"]');
  const labels = Array.from(document.querySelectorAll('button')).map((b) => t(b) + ' | ' + (b.getAttribute('aria-label') || ''));
  const log = document.querySelector('[role="log"][aria-label="Transcript"]');
  const logSpans = Array.from(document.querySelectorAll('[role="log"] span')).filter((s) => s.childElementCount === 0).map((s) => t(s));
  const times = Array.from(document.querySelectorAll('[role="log"] time'));
  const composer = document.querySelector('[data-testid="composer"]');
  const ta = composer ? composer.querySelector('textarea') : null;
  const send = composer ? composer.querySelector('button') : null;
  return {
    rail: {
      navVisible: visible(nav),
      headingSessions: Array.from(document.querySelectorAll('nav h2')).some((h) => t(h) === 'Sessions'),
      rows: rows.length,
      current: rows.length === 1 ? rows[0].getAttribute('aria-current') : null,
      interactiveInRow: rows.length === 1 ? rows[0].querySelectorAll('button, a, input, [tabindex]').length : null,
      note: t(conv ? conv.querySelector('[role="note"]') : null),
      toggleName: railToggle ? railToggle.getAttribute('aria-label') || t(railToggle) : null,
      toggleExpanded: railToggle ? railToggle.getAttribute('aria-expanded') : null,
      bodyFound: Boolean(body),
      bodyVisible: visible(body),
      railWidth: nav ? Math.round(nav.getBoundingClientRect().width) : null,
    },
    head: {
      title: t(head),
      conversation: t(conv),
      // The chat head (the conversation block) starts this far below the header. Its title is in the header, so the block is measured.
      chatHeadBelowHeader: toolbar && header ? Math.round(toolbar.getBoundingClientRect().top - header.getBoundingClientRect().bottom) : null,
    },
    messages: {
      times: times.length,
      timesWithDatetime: times.filter((x) => x.getAttribute('datetime')).length,
      copy: Array.from(document.querySelectorAll('button')).filter((b) => (b.getAttribute('aria-label') || '').startsWith('Copy message')).length,
      records: Array.from(document.querySelectorAll('[role="log"] button')).map((b) => t(b)).filter((x) => /^record \\d+$/.test(x)).length,
      piLabels: logSpans.filter((x) => x === 'Pi').length,
      userLabels: logSpans.filter((x) => x === 'User').length,
    },
    composer: {
      section: composer ? composer.getAttribute('aria-label') : null,
      inForm: composer ? composer.closest('form') !== null : null,
      forms: document.querySelectorAll('form').length,
      textarea: ta ? { readOnly: ta.readOnly, ariaDisabled: ta.getAttribute('aria-disabled'), placeholder: ta.getAttribute('placeholder'), value: ta.value } : null,
      chips: composer ? Array.from(composer.querySelectorAll('[data-fact]')).map((s) => t(s)) : [],
      send: send ? { ariaDisabled: send.getAttribute('aria-disabled'), type: send.getAttribute('type'), label: send.getAttribute('aria-label') } : null,
    },
    footer: Array.from((document.querySelector('[role="group"][aria-label="Session statistics"]') || { children: [] }).children).map((x) => t(x)).filter((x) => !x.startsWith('build ')).join(' · '),
    forbidden: labels.filter((x) => /^\\+ \\||Stop|Cancel|Model|Mode|Permission|New session|Attach/i.test(x)),
    readWriteEditBash: /read, write, edit, bash/.test(document.documentElement.outerHTML),
    newSession: /New session/i.test(document.body.innerText),
    overflow: { scrollWidth: document.documentElement.scrollWidth, clientWidth: document.documentElement.clientWidth },
    logVisible: visible(log),
  };
})()`;

// The governance inspector: the inline column at 1181 px and up, the drawer below.
const GOV_STATE = `(() => {
  ${TEXT_FN}
  ${VISIBLE_FN}
  const aside = document.getElementById('governance-inspector');
  const tabs = Array.from(document.querySelectorAll('[role="tablist"][aria-label="Governance panels"] [role="tab"]'));
  const panels = Array.from(document.querySelectorAll('#governance-panel'));
  const rail = document.querySelector('[role="toolbar"][aria-label="Governance panels (collapsed)"]');
  const headerToggle = document.querySelector('header[aria-label="Workspace header"] button[aria-controls="governance-inspector"]');
  const collapse = aside ? Array.from(aside.querySelectorAll('button')).find((b) => /^(Collapse|Expand) governance panel$/.test(b.getAttribute('aria-label') || '')) : null;
  const ar = aside ? aside.getBoundingClientRect() : null;
  const active = document.activeElement;
  return {
    asideVisible: visible(aside),
    asideLeft: ar ? Math.round(ar.left) : null,
    asideWidth: ar ? Math.round(ar.width) : null,
    dataOpen: aside ? aside.getAttribute('data-open') : null,
    inert: aside ? aside.hasAttribute('inert') : null,
    ariaHidden: aside ? aside.getAttribute('aria-hidden') : null,
    headerToggle: headerToggle ? { expanded: headerToggle.getAttribute('aria-expanded'), label: headerToggle.getAttribute('aria-label') || t(headerToggle) } : null,
    activeIsHeaderToggle: active === headerToggle,
    collapse: collapse ? collapse.getAttribute('aria-label') : null,
    tabs: tabs.map((x) => ({ id: x.id, name: x.getAttribute('aria-label') || t(x), selected: x.getAttribute('aria-selected'), tabindex: x.getAttribute('tabindex') })),
    panels: panels.length,
    panelLabelledBy: panels.length ? panels[0].getAttribute('aria-labelledby') : null,
    iconRail: rail ? { width: Math.round(rail.getBoundingClientRect().width), names: Array.from(rail.querySelectorAll('button')).map((b) => b.getAttribute('aria-label')) } : null,
    activeId: active ? active.id : null,
    activeLabel: active ? (active.getAttribute('aria-label') || t(active)).slice(0, 60) : null,
    prefs: localStorage.getItem('${PREFS_KEY}'),
  };
})()`;

const TAB_ORDER = ['Decisions', 'Provenance', 'Publication', 'Recovery', 'Qualification'];
const tabNames = (gv) => gv.tabs.map((x) => x.name.split(',')[0].trim());

async function clickByLabel(label) {
  const ok = await evaluate(`(() => { const b = Array.from(document.querySelectorAll('button')).find((x) => x.getAttribute('aria-label') === ${JSON.stringify(label)}); if (!b) return false; b.click(); return true; })()`);
  if (!ok) throw new Error(`no button labelled "${label}"`);
  await sleep(300);
}

async function pressCtrlEnter() {
  const base = { key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13, modifiers: 2 };
  await cdp.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...base });
  await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', ...base });
  await sleep(80);
}

async function checkJ(ctx) {
  const snapshot = await getJson(`${ORIGIN}/api/v1/snapshot`);
  const s = snapshot.session;
  const g = snapshot.panels.governance;
  const turns = `${s.turn_count} ${s.turn_count === 1 ? 'turn' : 'turns'}`;
  const expectedFooter = `${turns} · ${snapshot.items.length} records retained · policy verdicts ${g.allow_count} allow / ${g.deny_count} deny / ${g.unknown_count} unknown · tokens unavailable · timing unavailable`;
  const expectedChips = [s.pi_version ? `Pi ${s.pi_version} · tools unavailable` : 'Pi version unavailable · tools unavailable', 'Observed only · no approvals'];
  const shots = [];

  await viewport(1440, 900);
  await navigate('/app/');
  await evaluate('localStorage.clear()');
  for (const theme of ['light', 'dark']) {
    for (const [width, height] of POINT_SIZES) {
      const key = `${width}-${theme}`;
      const p = `${width}px ${theme}`;
      await viewport(width, height);
      await emulateMedia(theme === 'light' ? LIGHT_OS : DARK_OS);
      await navigate('/app/');
      // Each pass starts from the defaults: the governance column opened by an earlier pass is a stored choice.
      await evaluate(`localStorage.removeItem(${JSON.stringify(PREFS_KEY)}); localStorage.setItem(${JSON.stringify(THEME_KEY)}, ${JSON.stringify(theme)})`);
      await navigate('/app/');
      const st = await evaluate(POINT_STATE);
      ctx.note(`state ${key}`, { rail: st.rail, head: st.head, messages: st.messages, composer: st.composer, footer: st.footer });

      // (a) the Sessions rail
      if (width > 760) {
        ctx.expect(st.rail.navVisible && st.rail.headingSessions && st.rail.rows === 1 && st.rail.current === 'true' && st.rail.interactiveInRow === 0, `${p}: the Sessions rail is not one non-interactive current row (rows ${st.rail.rows}, current ${st.rail.current}, interactive ${st.rail.interactiveInRow})`);
        ctx.expect(st.rail.note === SESSIONS_NOTE, `${p}: the "Other sessions" note reads "${st.rail.note}"`);
        ctx.expect(/Collapse sessions panel/.test(st.rail.toggleName ?? '') && st.rail.toggleExpanded === 'true', `${p}: the rail toggle is "${st.rail.toggleName}" with aria-expanded ${st.rail.toggleExpanded}`);
      } else {
        ctx.expect(st.rail.navVisible && st.rail.toggleExpanded === 'false' && st.rail.bodyVisible === false, `${p}: the phone rail is not a collapsed disclosure strip (aria-expanded ${st.rail.toggleExpanded}, body visible ${st.rail.bodyVisible})`);
        ctx.expect(st.head.chatHeadBelowHeader !== null && st.head.chatHeadBelowHeader <= 160, `${p}: the chat head starts ${st.head.chatHeadBelowHeader} px below the header (plan: within 160 px)`);
      }

      // (c) Pi identity
      ctx.expect(st.head.title === s.title, `${p}: the conversation head reads "${st.head.title}", expected "${s.title}"`);
      ctx.expect(st.head.conversation.includes(`Pi ${s.pi_version} · ${s.session_id} · ${s.workflow_id}`) && st.head.conversation.includes('completed · reported, not verified'), `${p}: the head does not show "Pi ${s.pi_version} · ${s.session_id} · ${s.workflow_id}" and "completed · reported, not verified"`);
      ctx.expect(st.messages.times > 0 && st.messages.timesWithDatetime === st.messages.times, `${p}: ${st.messages.times} message time(s), ${st.messages.timesWithDatetime} with a datetime attribute`);
      ctx.expect(st.messages.copy >= 1 && st.messages.piLabels >= 1 && st.messages.userLabels >= 1, `${p}: ${st.messages.copy} Copy button(s), ${st.messages.piLabels} "Pi" and ${st.messages.userLabels} "User" label(s)`);
      const c = st.composer;
      ctx.expect(c.section === COMPOSER_SECTION && c.inForm === false && c.forms === 0, `${p}: composer section "${c.section}", inside a form ${c.inForm}, ${c.forms} form(s) on the page`);
      ctx.expect(c.textarea !== null && c.textarea.readOnly && c.textarea.ariaDisabled === 'true' && c.textarea.placeholder === PLACEHOLDER && c.textarea.value === '', `${p}: the prompt field is not read-only and aria-disabled with the exact placeholder (${JSON.stringify(c.textarea)})`);
      ctx.expect(JSON.stringify(c.chips) === JSON.stringify(expectedChips), `${p}: the chips read ${JSON.stringify(c.chips)}, expected ${JSON.stringify(expectedChips)}`);
      // Contract change (minimal pass): the composer has one grey, aria-disabled send mark, named for what it is.
      ctx.expect(c.send !== null && c.send.ariaDisabled === 'true' && c.send.label === 'Send: not available in this build', `${p}: the composer's send mark is not aria-disabled and named (${JSON.stringify(c.send)})`);
      ctx.expect(st.forbidden.length === 0 && !st.readWriteEditBash && !st.newSession, `${p}: forbidden controls ${JSON.stringify(st.forbidden)}, "read, write, edit, bash" present ${st.readWriteEditBash}, "New session" present ${st.newSession}`);
      ctx.expect(st.footer === expectedFooter, `${p}: the footer reads "${st.footer}", expected "${expectedFooter}"`);
      ctx.expect(st.overflow.scrollWidth <= st.overflow.clientWidth, `${p}: horizontal overflow (${JSON.stringify(st.overflow)})`);
      ctx.expect(st.logVisible, `${p}: the transcript is not visible`);
      shots.push(await capture(`points-${key}.png`));

      // (b) the governance panel
      if (width > 1180) {
        // The column starts collapsed (uiPrefs.ts): the header control says so, and one press opens it.
        const startsClosed = await evaluate(GOV_STATE);
        ctx.expect(startsClosed.headerToggle?.expanded === 'false', `${p}: the governance column does not start collapsed (aria-expanded ${startsClosed.headerToggle?.expanded})`);
        await clickSelector(DRAWER_TOGGLE);
        await sleep(300);
        const gv = await evaluate(GOV_STATE);
        ctx.expect(gv.asideVisible && gv.panels === 1 && tabNames(gv).join('|') === TAB_ORDER.join('|'), `${p}: the governance tabs are ${JSON.stringify(tabNames(gv))} with ${gv.panels} panel(s)`);
        ctx.expect(gv.tabs.find((x) => x.selected === 'true')?.id === 'governance-tab-decisions' && gv.tabs.filter((x) => x.tabindex === '0').length === 1, `${p}: the selected Decisions tab is not the only roving tab stop (${JSON.stringify(gv.tabs.map((x) => [x.selected, x.tabindex]))})`);
        ctx.expect(gv.headerToggle?.expanded === 'true', `${p}: the header Governance toggle reads aria-expanded ${gv.headerToggle?.expanded}`);
      } else {
        const closed = await evaluate(GOV_STATE);
        ctx.expect(closed.dataOpen === 'false' && closed.inert === true && closed.ariaHidden === 'true', `${p}: the closed drawer reads data-open ${closed.dataOpen}, inert ${closed.inert}, aria-hidden ${closed.ariaHidden}`);
        await clickSelector(DRAWER_TOGGLE);
        await sleep(400);
        const open = await evaluate(GOV_STATE);
        ctx.expect(open.dataOpen === 'true' && open.inert === false && open.panels === 1 && tabNames(open).join('|') === TAB_ORDER.join('|'), `${p}: the drawer did not open with the five tabs in order and one panel (data-open ${open.dataOpen}, ${open.panels} panel(s), tabs ${JSON.stringify(tabNames(open))})`);
        shots.push(await capture(`drawer-${key}.png`));
        await press('Escape');
        await sleep(400);
        const escaped = await evaluate(GOV_STATE);
        ctx.expect(escaped.dataOpen === 'false' && escaped.activeIsHeaderToggle === true, `${p}: Escape did not close the drawer and return focus to the Governance toggle (data-open ${escaped.dataOpen}, focus "${escaped.activeLabel}")`);
      }
    }
  }
  ctx.note('summary', `${POINT_SIZES.length} widths x 2 themes checked for the rail, the head, messages, the composer, the footer and the governance tabs or drawer`);

  // Keyboard, persistence and the icon rail, at 1440 px light.
  await viewport(1440, 900);
  await emulateMedia(LIGHT_OS);
  await navigate('/app/');
  await evaluate('localStorage.clear()');
  await navigate('/app/');
  // The governance column starts collapsed (uiPrefs.ts): open it, as a reader would, before the tab keys are tried.
  await clickSelector(DRAWER_TOGGLE);
  await sleep(300);
  const focusTab = async (id) => {
    await evaluate(`document.getElementById(${JSON.stringify(id)}).focus()`);
    await sleep(100);
  };
  await focusTab('governance-tab-decisions');
  await press('ArrowRight');
  let gv = await evaluate(GOV_STATE);
  ctx.expect(gv.activeId === 'governance-tab-provenance' && gv.tabs.find((x) => x.id === 'governance-tab-provenance')?.selected === 'true', `ArrowRight on Decisions left focus on ${gv.activeId}`);
  await press('ArrowLeft');
  await press('ArrowLeft');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.activeId === 'governance-tab-qualification' && gv.tabs.find((x) => x.id === 'governance-tab-qualification')?.selected === 'true', `ArrowLeft from Decisions did not wrap to Qualification (focus ${gv.activeId})`);
  await press('ArrowRight');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.activeId === 'governance-tab-decisions', `ArrowRight from Qualification did not wrap to Decisions (focus ${gv.activeId})`);
  await press('End');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.activeId === 'governance-tab-qualification', `End left focus on ${gv.activeId}`);
  await press('Home');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.activeId === 'governance-tab-decisions', `Home left focus on ${gv.activeId}`);
  ctx.note('governanceKeys', 'ArrowRight, ArrowLeft (wrap), End, Home checked at 1440 px');

  await press('End');
  await press('ArrowLeft');
  await navigate('/app/');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.tabs.find((x) => x.selected === 'true')?.id === 'governance-tab-recovery', `the selected tab did not persist across a reload (selected ${gv.tabs.find((x) => x.selected === 'true')?.id})`);
  ctx.expect(/"panel":"recovery"/.test(gv.prefs ?? ''), `the stored preferences do not hold the panel key: ${gv.prefs}`);
  await focusTab('governance-tab-recovery');
  await press('Home');

  // Collapse to the icon rail, select from it, and keep the collapsed state across a reload.
  await clickByLabel('Collapse governance panel');
  gv = await evaluate(GOV_STATE);
  ctx.note('iconRail', gv.iconRail);
  ctx.expect(gv.iconRail !== null && gv.iconRail.width > 0 && gv.iconRail.width <= 60 && gv.iconRail.names.filter((n) => /^Open .+ panel/.test(n ?? '')).length === 5, `the collapsed inspector has no five-button icon rail (${JSON.stringify(gv.iconRail)})`);
  const qualIcon = gv.iconRail?.names.find((n) => /^Open Qualification panel/.test(n ?? '')) ?? null;
  ctx.expect(qualIcon !== null, `no icon names the Qualification panel with its badge (${JSON.stringify(gv.iconRail?.names)})`);
  if (qualIcon !== null) await clickByLabel(qualIcon);
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.tabs.find((x) => x.id === 'governance-tab-qualification')?.selected === 'true' && gv.collapse !== null && /Collapse/.test(gv.collapse), `clicking the Qualification icon did not expand and select it (collapse ${gv.collapse}, selected ${gv.tabs.find((x) => x.selected === 'true')?.id})`);
  ctx.expect(gv.activeId === 'governance-tab-qualification', `the icon click did not move focus to the Qualification tab (focus ${gv.activeId})`);
  await clickByLabel('Collapse governance panel');
  await navigate('/app/');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.iconRail !== null, 'the collapsed inspector did not stay collapsed across a reload');
  await clickByLabel('Expand governance panel');
  await sleep(200);

  // The drawer at 1024 px (keyboard): focus stays inside the open drawer for 24 Tab presses, and Escape returns focus.
  await viewport(1024, 768);
  await navigate('/app/');
  await clickSelector(DRAWER_TOGGLE);
  await sleep(400);
  const trapEscapes = [];
  for (let i = 0; i < 24; i += 1) {
    await press('Tab', { shift: i % 2 === 1 });
    const inside = await evaluate(`(() => { const a = document.getElementById('governance-inspector'); return Boolean(a && a.contains(document.activeElement)); })()`);
    if (!inside) trapEscapes.push(i);
  }
  ctx.note('drawerTrapEscapes1024', trapEscapes);
  ctx.expect(trapEscapes.length === 0, `focus left the open drawer on Tab presses ${trapEscapes.join(', ')} at 1024 px`);
  await press('Escape');
  await sleep(300);
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.dataOpen === 'false' && gv.activeIsHeaderToggle === true, `Escape after the trap did not close the drawer and return focus (data-open ${gv.dataOpen})`);

  // The drawer's tablist on a phone: the arrows move focus and selection (390 px).
  await viewport(390, 844);
  await navigate('/app/');
  await clickSelector(DRAWER_TOGGLE);
  await sleep(400);
  await evaluate(`document.getElementById('governance-tab-decisions').focus()`);
  await press('ArrowRight');
  gv = await evaluate(GOV_STATE);
  ctx.expect(gv.activeId === 'governance-tab-provenance' && gv.tabs.find((x) => x.id === 'governance-tab-provenance')?.selected === 'true', `the drawer tablist at 390 px: ArrowRight left focus on ${gv.activeId}`);
  await press('Escape');
  await sleep(300);

  // The Sessions rail's collapse toggle: by keyboard and by pointer, kept across a reload (1440 px); the phone strip (390 px).
  const railToggleExpr = `Array.from(document.querySelectorAll('header[aria-label="Workspace header"] button[aria-controls="rail-body"], nav[aria-label="Sessions and streams"] button[aria-controls="rail-body"]')).find((x) => x.getBoundingClientRect().width > 0)`;
  await viewport(1440, 900);
  await navigate('/app/');
  // The rail's state is read after it settles: the React update and the stored preference follow the key or the click.
  const railNow = async () => (await evaluate(POINT_STATE)).rail;
  await evaluate(`(() => { ${railToggleExpr}.focus(); return true; })()`);
  await press('Enter');
  const collapsedByKey = await waitFor(`(() => { const b = ${railToggleExpr}; return Boolean(b && b.getAttribute('aria-expanded') === 'false'); })()`, 3000);
  let rs = await railNow();
  ctx.expect(collapsedByKey && rs.toggleExpanded === 'false' && rs.bodyVisible === false, `Enter on the rail toggle left aria-expanded ${rs.toggleExpanded} and the body visible ${rs.bodyVisible}`);
  ctx.expect(/"railCollapsed":true/.test((await evaluate(`localStorage.getItem('${PREFS_KEY}')`)) ?? ''), 'the collapsed rail is not in the stored preferences');
  await navigate('/app/');
  rs = await railNow();
  ctx.expect(rs.toggleExpanded === 'false' && rs.bodyVisible === false, `the collapsed rail did not survive a reload (aria-expanded ${rs.toggleExpanded})`);
  await evaluate(`(() => { ${railToggleExpr}.focus(); return true; })()`);
  await press(' ');
  await waitFor(`(() => { const b = ${railToggleExpr}; return Boolean(b && b.getAttribute('aria-expanded') === 'true'); })()`, 3000);
  rs = await railNow();
  ctx.expect(rs.toggleExpanded === 'true' && rs.bodyVisible === true && rs.rows === 1, `Space on the rail toggle left aria-expanded ${rs.toggleExpanded} and the body visible ${rs.bodyVisible}`);
  await clickSelector('header[aria-label="Workspace header"] button[aria-controls="rail-body"], nav[aria-label="Sessions and streams"] button[aria-controls="rail-body"]');
  await waitFor(`(() => { const b = ${railToggleExpr}; return Boolean(b && b.getAttribute('aria-expanded') === 'false'); })()`, 3000);
  rs = await railNow();
  ctx.expect(rs.toggleExpanded === 'false' && rs.bodyVisible === false, `a pointer press on the rail toggle left aria-expanded ${rs.toggleExpanded}`);
  await viewport(390, 844);
  await navigate('/app/');
  rs = await railNow();
  ctx.expect(rs.toggleExpanded === 'false' && rs.bodyVisible === false, `the phone strip is not collapsed on first load (aria-expanded ${rs.toggleExpanded})`);
  await clickSelector('nav[aria-label="Sessions and streams"] button[aria-controls="rail-body"]');
  await waitFor(`(() => { const b = ${railToggleExpr}; return Boolean(b && b.getAttribute('aria-expanded') === 'true'); })()`, 3000);
  rs = await railNow();
  ctx.expect(rs.toggleExpanded === 'true' && rs.bodyVisible === true, `the phone strip did not expand (aria-expanded ${rs.toggleExpanded}, body visible ${rs.bodyVisible})`);
  await clickSelector('nav[aria-label="Sessions and streams"] button[aria-controls="rail-body"]');
  await viewport(1440, 900);
  await navigate('/app/');

  // The record inspector: a record opens in the inspector; Close record returns focus to the selected governance tab (1440 px),
  // and at 1024 px the drawer stays open after Close record (the plan's rule: Close record never closes the drawer).
  const openRecord = async (number) => evaluate(`(() => { const b = Array.from(document.querySelectorAll('[role="log"] button')).find((x) => x.textContent.trim() === 'record ${number}'); if (!b) return false; b.click(); return true; })()`);
  // Close record returns focus to the governance tab that was selected before the record opened.
  const selectedTab = async () => (await evaluate(GOV_STATE)).tabs.find((x) => x.selected === 'true')?.id ?? null;
  const beforeRecord = await selectedTab();
  ctx.expect(await openRecord(2), 'no "record 2" button in the transcript');
  await sleep(400);
  const recordOpen = await evaluate(`Boolean(document.querySelector('button[aria-label="Close record 2"]'))`);
  ctx.expect(recordOpen, 'selecting record 2 did not show its "Close record 2" control');
  await clickByLabel('Close record 2');
  gv = await evaluate(GOV_STATE);
  ctx.note('closeRecordAt1440', { selectedBefore: beforeRecord, focusAfter: gv.activeId });
  ctx.expect(gv.activeId === beforeRecord, `Close record 2 left focus on ${gv.activeId}, expected the governance tab selected before the record (${beforeRecord})`);
  await viewport(1024, 768);
  await navigate('/app/');
  const beforeDrawerRecord = await selectedTab();
  await openRecord(2);
  await sleep(400);
  if ((await evaluate(GOV_STATE)).dataOpen !== 'true') {
    await clickSelector(DRAWER_TOGGLE);
    await sleep(400);
  }
  const drawerRecord = await evaluate(`Boolean(document.querySelector('button[aria-label="Close record 2"]'))`);
  ctx.expect(drawerRecord, 'at 1024 px the record 2 inspector has no "Close record 2" control in the open drawer');
  await clickByLabel('Close record 2');
  gv = await evaluate(GOV_STATE);
  ctx.note('closeRecordAt1024', { selectedBefore: beforeDrawerRecord, dataOpen: gv.dataOpen, activeId: gv.activeId });
  ctx.expect(gv.dataOpen === 'true' && gv.activeId === beforeDrawerRecord, `at 1024 px Close record 2 left the drawer data-open ${gv.dataOpen} and focus on ${gv.activeId}, expected open with focus on ${beforeDrawerRecord}`);
  await press('Escape');
  await sleep(300);
  await viewport(1440, 900);
  await navigate('/app/');
  const recordButtons = await evaluate(`Array.from(document.querySelectorAll('[role="log"] button')).map((b) => b.textContent.trim()).filter((x) => /^record \\d+$/.test(x)).length`);
  ctx.note('recordNumberLabels', { labelled: recordButtons, records: snapshot.items.length });

  // Composer and copy, at 1440 px light: no request, no change to the value, the explanation on focus, and the copy paths.
  await viewport(1440, 900);
  await navigate('/app/');
  await evaluate(`(() => { window.__clip = []; const orig = navigator.clipboard.writeText.bind(navigator.clipboard); navigator.clipboard.writeText = (text) => { window.__clip.push(String(text)); return orig(text); }; return true; })()`);
  await evaluate(`document.querySelector('[data-testid="composer"] textarea').focus()`);
  const focusedTa = await describe();
  ctx.expect(focusedTa.tag === 'textarea', `focusing the composer field did not focus the textarea (focus ${shortStop(focusedTa)})`);
  const reqBefore = observed.requests.length;
  await cdp.send('Input.insertText', { text: 'a stray draft' }).catch(() => {});
  await press('Enter');
  await pressCtrlEnter();
  const centreOf = (selector) => evaluate(`(() => { const r = document.querySelector('[data-testid="composer"] ${selector}').getBoundingClientRect(); return { x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2) }; })()`);
  const fieldAt = await centreOf('textarea');
  await pointerPress(fieldAt.x, fieldAt.y);
  await evaluate(`document.querySelector('[data-testid="composer"] textarea').focus()`);
  await press('Enter');
  await press(' ');
  // The Send mark is a real, aria-disabled button: a click, Enter and Space on it send nothing.
  const sendAt = await centreOf('button');
  await pointerPress(sendAt.x, sendAt.y);
  await evaluate(`document.querySelector('[data-testid="composer"] button').focus()`);
  const focusedSend = await describe();
  ctx.expect(focusedSend.tag === 'button' && /^Send/.test(focusedSend.label ?? ''), `focusing the Send mark did not focus the Send button (focus ${shortStop(focusedSend)})`);
  await press('Enter');
  await press(' ');
  await sleep(300);
  const composerRequests = observed.requests.slice(reqBefore).filter((r) => r.url.startsWith(ORIGIN) && !/\/api\/v1\/events/.test(r.url));
  const after = await evaluate(POINT_STATE);
  ctx.note('composerActions', { requests: composerRequests.map((r) => r.url.replace(ORIGIN, '')), value: after.composer.textarea?.value ?? null });
  ctx.expect(composerRequests.length === 0, `${composerRequests.length} request(s) after typing, Enter, Ctrl+Enter, a click on the field, and a click and Enter/Space on Send: ${composerRequests.map((r) => r.url.replace(ORIGIN, '')).join(', ')}`);
  ctx.expect(after.composer.textarea?.value === '', `the read-only prompt field took the typed text: "${after.composer.textarea?.value}"`);
  const statusText = await evaluate(`(() => { const c = document.querySelector('[data-testid="composer"]'); return c ? c.textContent : ''; })()`);
  ctx.expect(/Not available/.test(statusText), 'pressing the read-only field did not show the "Not available" explanation');

  await evaluate(`document.querySelector('[data-testid="composer"] textarea').focus()`);
  // Tab order inside the composer: the field, then the Send button, then out of the composer.
  await press('Tab');
  const afterTa = await describe();
  ctx.expect(afterTa.tag === 'button' && /^Send/.test(afterTa.label ?? ''), `Tab from the composer field reached ${shortStop(afterTa)}, expected the Send button`);
  await press('Tab');
  const afterSend = await describe();
  const insideComposer = await evaluate(`!!document.activeElement && !!document.activeElement.closest('[data-testid="composer"]')`);
  ctx.expect(!insideComposer, `Tab from the Send button stayed inside the composer (went to ${shortStop(afterSend)}), expected a stop outside it`);

  const clipText = snapshot.items[0].text;
  await evaluate(`(() => { const b = document.querySelector('[role="log"] button[aria-label^="Copy message"]'); b.click(); return true; })()`);
  await sleep(300);
  const clip = await evaluate(`(() => { const word = (x) => (x.getAttribute('aria-label') || '').split(': ')[0]; const b = Array.from(document.querySelectorAll('[role="log"] button')).find((x) => /^Copied$|^Copy/.test(word(x))); return { clip: window.__clip.slice(), label: b ? word(b) : null }; })()`);
  ctx.note('copy', { wrote: clip.clip, label: clip.label });
  ctx.expect(clip.clip.length === 1 && clip.clip[0] === clipText, `the first Copy wrote ${JSON.stringify(clip.clip)}, expected the projected text "${clipText}"`);
  ctx.expect(clip.label === 'Copied', `after a copy the button reads "${clip.label}", expected "Copied"`);
  await evaluate(`(() => { navigator.clipboard.writeText = () => Promise.reject(new DOMException('denied by the QA harness', 'NotAllowedError')); return true; })()`);
  const beforeDenied = observed.console.length;
  await evaluate(`(() => { const b = document.querySelector('[role="log"] button[aria-label^="Copy message"]'); b.click(); return true; })()`);
  await sleep(500);
  const denied = await evaluate(`(() => { const word = (x) => (x.getAttribute('aria-label') || '').split(': ')[0]; const b = Array.from(document.querySelectorAll('[role="log"] button')).find((x) => /^Copy/.test(word(x))); return b ? word(b) : null; })()`);
  const deniedIssues = observed.console.slice(beforeDenied).filter((e) => ['error', 'exception'].includes(e.level));
  ctx.note('copyDenied', { label: denied, consoleErrors: deniedIssues.length });
  ctx.expect(denied === 'Copy unavailable', `a denied copy reads "${denied}", expected "Copy unavailable"`);
  ctx.expect(deniedIssues.length === 0, `a denied copy logged ${deniedIssues.length} console error(s): ${deniedIssues.map((e) => e.text).join(' | ')}`);
  await evaluate(`localStorage.clear()`);
}

// ---------- K: the phone dock: a Tab and Shift+Tab walk, with every stop measured against the dock ----------

// The focused element against the sticky composer dock: overlapped by the dock's box, or with its centre area hit by the dock.
const DOCK_COVER = `(() => {
  const el = document.activeElement;
  const dock = document.querySelector('[class*="_dock_"]');
  if (!el || el === document.body || !dock) return null;
  if (dock.contains(el)) return { covered: false, inDock: true };
  const r = el.getBoundingClientRect();
  const d = dock.getBoundingClientRect();
  const overlap = r.width > 0 && r.height > 0 && r.bottom > d.top + 0.5 && r.top < d.bottom - 0.5 && r.right > d.left && r.left < d.right;
  const dockHit = [[0.5, 0.5], [0.25, 0.5], [0.75, 0.5], [0.5, 0.25], [0.5, 0.75]].some(([fx, fy]) => {
    const h = document.elementFromPoint(r.left + r.width * fx, r.top + r.height * fy);
    return Boolean(h && dock.contains(h));
  });
  return { covered: overlap || dockHit, inDock: false, rect: [Math.round(r.top), Math.round(r.bottom)], dock: [Math.round(d.top), Math.round(d.bottom)] };
})()`;

// The end of the page: the last message, the footer and the dock. Up to 760 px the page scrolls, and the dock is sticky.
// From 761 px the Workspace (main) and the transcript scroll, and the dock is not sticky: it is reachable at their ends.
const DOCK_END = `(() => {
  const dock = document.querySelector('[class*="_dock_"]');
  const d = dock ? dock.getBoundingClientRect() : null;
  const turns = Array.from(document.querySelectorAll('[role="log"] section[class*="_turn_"]'));
  const last = turns.length ? turns[turns.length - 1].getBoundingClientRect() : null;
  const footer = document.querySelector('[role="group"][aria-label="Session statistics"]');
  const f = footer ? footer.getBoundingClientRect() : null;
  const overlap = (r) => Boolean(r && d && r.width > 0 && r.height > 0 && r.bottom > d.top + 0.5 && r.top < d.bottom - 0.5);
  const main = document.querySelector('main[aria-label="Workspace"]');
  return {
    scrollY: Math.round(window.scrollY),
    maxScroll: Math.round(document.documentElement.scrollHeight - window.innerHeight),
    mainScrollTop: main ? Math.round(main.scrollTop) : null,
    mainMaxScroll: main ? Math.round(main.scrollHeight - main.clientHeight) : null,
    dock: d ? [Math.round(d.top), Math.round(d.bottom)] : null,
    dockFullyVisible: Boolean(d && d.top >= 0 && d.bottom <= window.innerHeight),
    lastMessage: last ? [Math.round(last.top), Math.round(last.bottom)] : null,
    lastMessageCovered: overlap(last),
    footer: f ? [Math.round(f.top), Math.round(f.bottom)] : null,
    footerCovered: overlap(f),
    pageScrolls: document.documentElement.scrollHeight > window.innerHeight,
    overflowX: document.documentElement.scrollWidth - document.documentElement.clientWidth,
  };
})()`;

// The composer's reason and its placeholder must read in full, and a phone dock must stay a sane share of the viewport. A
// textarea does not report the lines of its placeholder, so the placeholder is wrapped in a mirror of the field's text box
// and the mirror's height is compared with the field's visible text box. The status line is read too: the dock is measured
// in its tallest state, with the status line shown.
const PHONE_DOCK_SHARE = 0.4;
const COMPOSER_FIT = `(() => {
  const composer = document.querySelector('[data-testid="composer"]');
  // The composer's readable facts (the reason itself is for assistive technology and is in the details card).
  const reason = composer ? composer.querySelector('[data-fact]') : null;
  const status = document.getElementById('composer-status');
  const ta = composer ? composer.querySelector('textarea') : null;
  const dock = document.querySelector('[class*="_dock_"]');
  if (!composer || !reason || !status || !ta || !dock) return null;
  const rs = getComputedStyle(reason);
  const ts = getComputedStyle(ta);
  const padX = parseFloat(ts.paddingLeft) + parseFloat(ts.paddingRight);
  const padY = parseFloat(ts.paddingTop) + parseFloat(ts.paddingBottom);
  const mirror = document.createElement('div');
  mirror.style.position = 'absolute';
  mirror.style.left = '-10000px';
  mirror.style.visibility = 'hidden';
  mirror.style.whiteSpace = 'pre-wrap';
  mirror.style.overflowWrap = 'break-word';
  mirror.style.width = (ta.clientWidth - padX) + 'px';
  for (const p of ['fontFamily', 'fontSize', 'fontWeight', 'letterSpacing', 'lineHeight']) mirror.style[p] = ts[p];
  mirror.textContent = ta.placeholder;
  document.body.appendChild(mirror);
  const need = mirror.getBoundingClientRect().height;
  mirror.remove();
  return {
    reason: { scrollWidth: reason.scrollWidth, clientWidth: reason.clientWidth, textOverflow: rs.textOverflow, whiteSpace: rs.whiteSpace },
    placeholder: { need: Math.round(need), have: Math.round(ta.clientHeight - padY) },
    status: status.textContent.trim(),
    dock: Math.round(dock.getBoundingClientRect().height),
    viewport: window.innerHeight,
  };
})()`;

// Presses Tab (or Shift+Tab) until focus leaves the document, and checks every stop against the dock. Headless Chrome
// can also wrap from the first control straight to the last (no BODY stop between), so a return to the first stop ends
// the walk as a cycle, as tabWalk does; without that the backward walk would repeat every stop.
async function dockWalk(backward) {
  const stops = [];
  const labels = [];
  const covered = [];
  let measured = 0;
  let end = 'limit reached';
  let first = true;
  for (let i = 0; i < 150; i += 1) {
    let stop;
    if (backward) {
      await press('Tab', { shift: true });
      stop = await describe();
      if (first && stop.tag === 'BODY') {
        await press('Tab', { shift: true });
        stop = await describe();
      }
    } else {
      stop = await tabPress();
    }
    first = false;
    if (stop.tag === 'BODY') {
      end = 'document end';
      break;
    }
    const label = shortStop(stop);
    if (labels.length > 0 && label === labels[0]) {
      end = 'cycled to the first stop';
      break;
    }
    labels.push(label);
    stops.push(stop);
    const cover = await evaluate(DOCK_COVER);
    if (cover) measured += 1;
    if (cover && cover.covered) covered.push({ stop: label, rect: cover.rect, dock: cover.dock });
  }
  return { stops: stops.length, measured, list: labels.map((s) => s.slice(0, 40)), covered, end };
}

async function checkK(ctx) {
  const sizes = [
    [360, 640],
    [390, 844],
    [412, 915],
    [844, 390],
  ];
  const rows = [];
  for (const [width, height] of sizes) {
    const key = `${width}x${height}`;
    await viewport(width, height);
    await emulateMedia(LIGHT_OS);
    await navigate('/app/');
    const loadFit = await evaluate(COMPOSER_FIT);
    const forward = await dockWalk(false);
    const backward = await dockWalk(true);
    await evaluate('window.scrollTo(0, 0)');
    await sleep(200);
    await capture(`phone-${key}-dock-top.png`);
    const wide = width > 760;
    // Narrow: the page is the scroller. Wide: the Workspace and then the transcript are the scrollers.
    await evaluate(wide
      ? `(() => { document.querySelector('main[aria-label="Workspace"]').scrollTop = 1e9; document.querySelector('[role="log"][aria-label="Transcript"]').scrollTop = 1e9; return true; })()`
      : 'window.scrollTo(0, document.documentElement.scrollHeight)');
    await sleep(300);
    const end = await evaluate(DOCK_END);
    await capture(`phone-${key}-dock-end.png`);
    const focusFit = await evaluate(COMPOSER_FIT);
    ctx.note(`composer ${key}`, { load: loadFit, statusShown: focusFit });
    const covered = [...forward.covered, ...backward.covered];
    rows.push({ size: key, forward: { stops: forward.stops, measured: forward.measured, end: forward.end, list: forward.list }, backward: { stops: backward.stops, measured: backward.measured, end: backward.end, list: backward.list }, covered: covered.length, end });
    ctx.expect(forward.measured === forward.stops && backward.measured === backward.stops && end.dock !== null, `${key}: ${forward.measured} of ${forward.stops} forward and ${backward.measured} of ${backward.stops} backward stops were measured against the dock (dock found: ${end.dock !== null})`);
    ctx.expect(covered.length === 0, `${key}: focused elements under the dock: ${JSON.stringify(covered.slice(0, 4))}`);
    const clean = (end) => end === 'document end' || end === 'cycled to the first stop';
    ctx.expect(clean(forward.end) && clean(backward.end) && forward.stops === backward.stops, `${key}: the walks were ${forward.stops} stops ("${forward.end}") forward and ${backward.stops} stops ("${backward.end}") backward`);
    ctx.expect(!end.lastMessageCovered, `${key}: the dock covers the last message (${JSON.stringify(end.lastMessage)} against the dock ${JSON.stringify(end.dock)})`);
    ctx.expect(!end.footerCovered, `${key}: the dock covers the footer (${JSON.stringify(end.footer)} against the dock ${JSON.stringify(end.dock)})`);
    for (const [when, fit] of [['at load', loadFit], ['with the status line shown', focusFit]]) {
      const r = fit ? fit.reason : null;
      ctx.expect(r !== null && r.scrollWidth <= r.clientWidth && r.textOverflow !== 'ellipsis' && r.whiteSpace !== 'nowrap', `${key} ${when}: the reason is clipped (scrollWidth ${r?.scrollWidth}, clientWidth ${r?.clientWidth}, text-overflow ${r?.textOverflow}, white-space ${r?.whiteSpace})`);
      ctx.expect(fit !== null && fit.placeholder.need <= fit.placeholder.have + 0.5, `${key} ${when}: the placeholder needs ${fit?.placeholder.need} px and the field shows ${fit?.placeholder.have} px, so its last line is cut off`);
      if (width <= 760) ctx.expect(fit !== null && fit.dock <= PHONE_DOCK_SHARE * fit.viewport, `${key} ${when}: the dock is ${fit?.dock} px, more than ${PHONE_DOCK_SHARE * 100}% of the ${fit?.viewport} px viewport`);
    }
    ctx.expect(focusFit !== null && focusFit.status !== '', `${key}: the status line was empty when the dock was measured, so the tallest state was not measured`);
    ctx.expect(end.overflowX === 0, `${key}: horizontal overflow ${end.overflowX} px`);
    if (wide) {
      ctx.expect(end.mainScrollTop > 0 && end.dockFullyVisible, `${key}: after the Workspace scrolls to its end the dock is ${end.dockFullyVisible ? 'visible' : 'not fully visible'} (dock ${JSON.stringify(end.dock)}, Workspace scrolled ${end.mainScrollTop} of ${end.mainMaxScroll})`);
    } else {
      ctx.expect(end.pageScrolls && end.scrollY > 0, `${key}: the page does not scroll to its end (scrollY ${end.scrollY} of ${end.maxScroll})`);
    }
  }
  ctx.note('walks', rows);
  ctx.note('summary', rows.map((r) => `${r.size}: ${r.forward.stops} stops forward and ${r.backward.stops} backward, ${r.covered} under the dock`).join('; '));
}

// ---------- L: the claim boundary and absent data ----------

const LOSSY_PHRASE = 'output lossy, not counted as confirmed';
const SURFACE_LOSSY = (text) => (text ? Array.from(new Set((text.match(/[^.;|]*output lossy[^.;|]*/g) ?? []).map((p) => p.replace(/\s+/g, ' ').trim()))) : []);

// The snapshot as the page receives it. The gateway's own snapshot is read, then one field set is rewritten.
let snapshotOverride = null;
let snapshotHandler = false;
async function installSnapshotOverride() {
  if (!snapshotHandler) {
    cdp.on('Fetch.requestPaused', async ({ requestId, request }) => {
      try {
        if (!snapshotOverride || !/\/api\/v1\/snapshot/.test(request.url)) {
          await cdp.send('Fetch.continueRequest', { requestId });
          return;
        }
        const real = await getJson(`${ORIGIN}/api/v1/snapshot`);
        const body = JSON.stringify(snapshotOverride(structuredClone(real)));
        await cdp.send('Fetch.fulfillRequest', {
          requestId,
          responseCode: 200,
          responseHeaders: [
            { name: 'Content-Type', value: 'application/json' },
            { name: 'Cache-Control', value: 'no-store' },
          ],
          body: Buffer.from(body).toString('base64'),
        });
      } catch {
        // the request is answered by the page's own retry path
      }
    });
    snapshotHandler = true;
  }
  await cdp.send('Fetch.enable', { patterns: [{ urlPattern: '*/api/v1/snapshot*', requestStage: 'Request' }] });
}

const ABSENT_VARIANTS = [
  {
    id: 'true-zero-counts',
    label: 'a true zero: policy counts 0, digest present, no verdict records',
    mutate: (s) => {
      // A zero count beside a verdict record contradicts the zero (panels.ts decisionsState), so the tool records go. The
      // validator needs sequences 1..N and a cursor on the last one, so the messages are renumbered and the cursor set.
      const g = s.panels.governance;
      g.allow_count = 0;
      g.deny_count = 0;
      g.unknown_count = 0;
      const messages = s.items.filter((item) => item.verdict === undefined && item.tool_call_id === undefined);
      s.items = messages.map((item, index) => ({ ...item, sequence: index + 1 }));
      s.cursor = `${s.epoch}:${s.items.length}`;
      return s;
    },
    expect: (r) => (r.footer.includes('policy verdicts 0 allow / 0 deny / 0 unknown') ? null : `the footer reads "${r.footer}", expected "policy verdicts 0 allow / 0 deny / 0 unknown"`),
  },
  {
    id: 'absent-as-gateway-writes-it',
    label: 'absent governance as the gateway writes it: counts omitted, digest empty',
    mutate: (s) => {
      const g = s.panels.governance;
      g.policy_digest = '';
      delete g.allow_count;
      delete g.deny_count;
      delete g.unknown_count;
      return s;
    },
    expect: (r) => (r.footer.includes('policy verdicts unavailable') && !r.footer.includes('0 allow') ? null : `the footer reads "${r.footer}", expected "policy verdicts unavailable" (no 0 counts)`),
  },
  {
    id: 'true-zero-turns',
    label: 'a true zero turn count',
    mutate: (s) => {
      s.session.turn_count = 0;
      return s;
    },
    expect: (r) => (r.footer.startsWith('0 turns') && r.rail.includes('Pi 0.87.1 · 0 turns') ? null : `the footer reads "${r.footer.slice(0, 40)}…" and the rail "${r.rail.match(/Pi [^·]+· [^c]+/)?.[0] ?? ''}", expected "0 turns"`),
  },
  {
    id: 'absent-omitted',
    label: 'absent fields omitted from the JSON (policy and turn count)',
    mutate: (s) => {
      const g = s.panels.governance;
      delete g.policy_digest;
      delete g.allow_count;
      delete g.deny_count;
      delete g.unknown_count;
      delete s.session.turn_count;
      return s;
    },
    expect: (r) => (r.footer.includes('turns unavailable') && r.footer.includes('policy verdicts unavailable') && !/\b0 (turns?|allow|deny|unknown)\b/.test(r.footer) ? null : `the footer reads "${r.footer}", expected "turns unavailable" and "policy verdicts unavailable" with no 0`),
  },
];

async function checkL(ctx) {
  const snapshot = await getJson(`${ORIGIN}/api/v1/snapshot`);
  const epoch = snapshot.epoch;
  const seq = snapshot.items[snapshot.items.length - 1].sequence + 1;
  await viewport(1440, 900);
  await emulateMedia(LIGHT_OS);
  await navigate('/app/');
  await evaluate('localStorage.clear()');
  await installFakeStream();
  try {
    await navigate('/app/');
    ctx.expect(await waitFor(FAKE_READY, 8000), 'the fake EventSource did not open');
    await emit('item', itemFrame(epoch, seq, { id: 'qa-l-lossy', type: 'result', tool: 'bash', tool_call_id: 'qa-call-l-lossy', state: 'succeeded', lossy: true, summary: 'QA lossy success for L' }));
    await sleep(500);

    // The Chat card: the transcript element that holds the summary, and its nearest card.
    const chatCard = await evaluate(`(() => {
      const leaf = Array.from(document.querySelectorAll('[role="log"] *')).find((e) => e.childElementCount === 0 && /QA lossy success for L/.test(e.textContent));
      if (!leaf) return null;
      const card = leaf.closest('details') || leaf.closest('section') || leaf.parentElement;
      return card.textContent.replace(/\\s+/g, ' ').trim();
    })()`);
    await clickSelector('#view-tab-trajectory');
    const ledgerRow = await evaluate(`(() => {
      const b = Array.from(document.querySelectorAll('[role="region"][aria-label="Record ledger"] button[data-record-row]')).find((x) => new RegExp('Result record ${seq}\\\\b').test(x.getAttribute('aria-label') || ''));
      if (!b) return null;
      b.click();
      return { label: b.getAttribute('aria-label'), text: b.textContent.replace(/\\s+/g, ' ').trim() };
    })()`);
    await sleep(400);
    const inspector = await evaluate(`(() => { const a = document.querySelector('aside'); return a ? a.textContent.replace(/\\s+/g, ' ').trim() : null; })()`);
    const surfaces = {
      chatCard: SURFACE_LOSSY(chatCard),
      ledgerRowLabel: SURFACE_LOSSY(ledgerRow?.label),
      ledgerRowText: SURFACE_LOSSY(ledgerRow?.text),
      inspector: SURFACE_LOSSY(inspector),
    };
    ctx.note('lossyWording', surfaces);
    ctx.note('lossyChatCard', chatCard);
    ctx.note('lossyLedgerRow', ledgerRow);
    for (const name of ['chatCard', 'ledgerRowLabel', 'inspector']) {
      ctx.expect(surfaces[name].some((p) => p.includes(LOSSY_PHRASE)), `the ${name} does not read "${LOSSY_PHRASE}": ${JSON.stringify(surfaces[name])}`);
    }
    ctx.expect(ledgerRow !== null, 'the injected lossy success has no ledger row');
    ctx.expect((await evaluate(`document.querySelectorAll('[data-tone="ok"]').length`)) === 0, 'a success tone appeared for the unverified lossy success');
    await capture('claims-lossy-ledger.png');
  } finally {
    await removeFakeStream();
  }

  // Absent versus zero, read from the page after the snapshot is rewritten in the page (never in the gateway). The
  // event stream's session event carries the same session and governance, so it is replaced by the fake EventSource:
  // then the snapshot is the only source the page has.
  await installSnapshotOverride();
  await installFakeStream();
  try {
    for (const variant of ABSENT_VARIANTS) {
      snapshotOverride = variant.mutate;
      await navigate('/app/');
      await waitFor(`Boolean(document.querySelector('[role="group"][aria-label="Session statistics"]'))`, 8000);
      const read = await evaluate(`(() => ({
        footer: (document.querySelector('[role="group"][aria-label="Session statistics"]') || {}).textContent || '',
        rail: (document.querySelector('nav[aria-label="Sessions and streams"]') || {}).textContent || '',
        decisionsTab: (document.getElementById('governance-tab-decisions') || {}).getAttribute ? document.getElementById('governance-tab-decisions').getAttribute('aria-label') : null,
      }))()`);
      const problem = variant.expect(read);
      ctx.note(`absent-zero ${variant.id}`, { label: variant.label, footer: read.footer, decisionsTab: read.decisionsTab, railText: read.rail.slice(0, 160) });
      ctx.expect(problem === null, `${variant.label}: ${problem}`);
    }
  } finally {
    snapshotOverride = null;
    await cdp.send('Fetch.disable').catch(() => {});
    await removeFakeStream();
    await navigate('/app/');
  }
  ctx.note('summary', 'lossy wording read on the Chat card, the ledger row and the inspector; four absent and zero snapshots read from the page');
}

// ---------- main ----------

function parseArgs(argv) {
  const flags = {
    '--url': 'url',
    '--chrome': 'chrome',
    '--only': 'only',
    '--gateway-sha256': 'gatewaySha256',
    '--gateway-stop': 'gatewayStop',
    '--gateway-start': 'gatewayStart',
    '--run': 'run',
  };
  const options = {};
  for (let i = 0; i < argv.length; i += 1) {
    if (!Object.hasOwn(flags, argv[i])) throw new Stop(CODE.usage, `unknown argument ${argv[i]}`);
    const value = argv[i + 1];
    if (value === undefined || value.startsWith('--')) throw new Stop(CODE.usage, `${argv[i]} needs a value`);
    options[flags[argv[i]]] = value;
    i += 1;
  }
  return options;
}

function gatewayOrigin(raw) {
  if (!raw) throw new Stop(CODE.usage, '--url is required: start a demo gateway on a fresh port and pass http://127.0.0.1:PORT');
  const parsed = new URL(raw);
  if (parsed.protocol !== 'http:' || parsed.hostname !== '127.0.0.1' || !parsed.port) {
    throw new Stop(CODE.usage, '--url must be http://127.0.0.1:<port> (the gateway binds a literal loopback address)');
  }
  if (Number(parsed.port) === STALE_PORT) throw new Stop(CODE.usage, `refusing port ${STALE_PORT}: that is a stale old build; start a demo gateway on a fresh port`);
  return parsed.origin;
}

function selectedChecks(only) {
  if (!only) return CHECK_IDS;
  const ids = only.split(',').map((id) => id.trim().toUpperCase()).filter(Boolean);
  for (const id of ids) {
    if (!CHECK_IDS.includes(id)) throw new Stop(CODE.usage, `unknown check ${id}; the checks are ${CHECK_IDS.join(', ')}`);
  }
  return ids;
}

function writeReceipt(report) {
  writeFileSync(receiptPath(), `${JSON.stringify(report, null, 2)}\n`);
}

function receiptPath() {
  return join(QA_DIR, `${RUN}-ui-qa-receipt.json`);
}

async function main(argv) {
  let exitCode = CODE.pass;
  try {
    const options = parseArgs(argv);
    ORIGIN = gatewayOrigin(options.url);
    const selected = selectedChecks(options.only);
    if (options.gatewaySha256 !== undefined && !/^[0-9a-f]{64}$/.test(options.gatewaySha256)) {
      throw new Stop(CODE.usage, '--gateway-sha256 must be 64 lowercase hex digits');
    }
    if (options.run !== undefined) {
      if (!/^[a-z][a-z0-9-]{0,15}$/.test(options.run)) throw new Stop(CODE.usage, '--run must be a short lowercase prefix such as p5');
      RUN = options.run;
    }
    if (typeof WebSocket !== 'function') throw new Stop(CODE.notVerified, 'this Node has no global WebSocket; Node >= 22 is required');
    const binary = findBrowser(options.chrome ?? null);
    if (!binary) {
      console.error('NOT VERIFIED: no browser (set CHROME_BIN or pass --chrome to a Chrome or Chromium binary)');
      return CODE.noBrowser;
    }
    if (!(await poll(() => getJson(`${ORIGIN}/api/v1/contract`), 3000, 250))) {
      throw new Stop(CODE.notVerified, `no gateway answered at ${ORIGIN}`);
    }
    browserBinary = binary;
    gatewayCommands.stop = options.gatewayStop;
    gatewayCommands.start = options.gatewayStart;
    mkdirSync(QA_DIR, { recursive: true });
    const browser = await launchBrowser(binary);
    await setUpPage(browser.base);
    console.log(`ui-qa: browser ${browser.version.Browser} (${binary})`);
    console.log(`ui-qa: gateway ${ORIGIN}; checks ${selected.join(',')}`);

    const checks = CHECKS.filter((check) => selected.includes(check.id));
    for (const check of checks) await runCheck(check);

    const failed = results.filter((r) => r.status === 'FAIL');
    const unverified = results.filter((r) => r.status === 'NOT VERIFIED');
    const outcome = failed.length > 0 ? 'FAIL' : unverified.length > 0 ? 'NOT VERIFIED' : 'PASS';
    exitCode = outcome === 'PASS' ? CODE.pass : outcome === 'FAIL' ? CODE.fail : CODE.notVerified;
    writeReceipt({
      receipt: `${RUN}-ui-qa`,
      generatedAt: new Date().toISOString(),
      outcome,
      selectedChecks: selected,
      git: { head: gitHead() },
      script: { path: 'extensions/webchat/scripts/ui-qa.mjs', sha256: sha256File(fileURLToPath(import.meta.url)) },
      cspSmoke: { path: 'extensions/webchat/scripts/csp-smoke.mjs', sha256: sha256File(join(webchatRoot, 'scripts', 'csp-smoke.mjs')) },
      node: process.version,
      platform: `${process.platform} ${process.arch}`,
      browser: { product: browser.version.Browser, userAgent: browser.version.userAgent, binary, binarySha256: sha256File(binary) },
      gateway: { origin: ORIGIN, binarySha256: options.gatewaySha256 ?? null, binarySha256Source: options.gatewaySha256 ? 'supplied by the operator with --gateway-sha256 (sha256sum in WSL); not verified by this script' : null },
      dist: distTree(),
      checks: results,
      screenshots,
      observation: {
        timelineG: observed.timeline
          .filter((e) => e.phase === 'G')
          .map((e) => ({ kind: e.kind, at: Math.round(e.at), url: e.url ? e.url.replace(ORIGIN, '') : undefined, status: e.status, state: e.state })),
        requests: observed.requests.length,
        gatewayRequests: observed.requests.filter((r) => r.url.startsWith(ORIGIN)).length,
        externalRequests: observed.requests.filter((r) => !r.url.startsWith(ORIGIN) && !/^(data|blob):/.test(r.url)).map((r) => r.url),
        consoleErrorsAndWarnings: observed.console.filter((e) => ['error', 'warning', 'exception'].includes(e.level)),
        cspViolations: observed.csp,
        failedRequests: observed.failed,
      },
    });
    console.log(`ui-qa: receipt ${receiptPath()}`);
    if (outcome === 'PASS') console.log(`ui-qa: PASS (${results.length} checks, ${screenshots.length} screenshots, ${browser.version.Browser})`);
    else console.log(`ui-qa: ${outcome} (${failed.length} failed, ${unverified.length} not verified)`);
    cdp.close();
    return exitCode;
  } catch (error) {
    if (error instanceof Stop) {
      if (error.code === CODE.usage) console.error(`ui-qa: ${error.message}`);
      else console.error(`NOT VERIFIED: ${error.message}`);
      return error.code;
    }
    console.error(`NOT VERIFIED: ${error instanceof Error ? error.message : String(error)}`);
    return CODE.notVerified;
  } finally {
    runCleanups();
  }
}

const CHECKS = [
  { id: 'A', name: 'csp-smoke without the favicon allowance', run: checkA },
  { id: 'B', name: 'redirect, legacy notice, served assets', run: checkB },
  { id: 'C', name: 'keyboard-only walk at 1440', run: checkC },
  { id: 'D', name: 'responsive: drawer, overflow, screenshots', run: checkD },
  { id: 'E', name: 'theme: persistence, blocked storage, first paint', run: checkE },
  { id: 'F', name: 'live stream: admission, refusal, gaps, pill', run: checkF },
  { id: 'G', name: 'reconnect and offline', run: checkG },
  { id: 'H', name: 'forced colours and reduced motion', run: checkH },
  { id: 'I', name: 'console, CSP and network', run: checkI },
  { id: 'J', name: "the user's three points at six widths, light and dark", run: checkJ },
  { id: 'K', name: 'phone dock: Tab and Shift+Tab walk at four sizes', run: checkK },
  { id: 'L', name: 'claim boundary: lossy wording per surface; absent versus zero', run: checkL },
];

process.exitCode = await main(process.argv.slice(2));
