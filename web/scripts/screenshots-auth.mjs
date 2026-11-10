// Screens for auth, queued runs and the worker fleet (Phase 6). Same approach
// as screenshots.mjs, but the mock runs with MOCK_TOKEN (auth on) and
// MOCK_PROVISIONER=0 (so asking for too many workers returns 409).
//
//   pnpm build && node scripts/screenshots-auth.mjs [outDir]
//
// Uses its own ports (mock :28471, static dist + /api proxy :28472; override with MOCK_PORT / WEB_PORT) so it can
// run next to a real master on :8080.
import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync } from 'node:fs';
import { createServer, request as httpRequest } from 'node:http';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const webDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const outDir = resolve(process.argv[2] || join(webDir, 'screenshots'));
const API_PORT = Number(process.env.MOCK_PORT || 28471);
const WEB_PORT = Number(process.env.WEB_PORT || 28472);
const BASE = `http://localhost:${WEB_PORT}`;
const API = `http://localhost:${API_PORT}`;
const TOKEN = 'mock-s3cret-token';
mkdirSync(outDir, { recursive: true });

const procs = [
  spawn(process.execPath, [join(webDir, 'mock/server.mjs'), '--port', String(API_PORT)], {
    cwd: webDir,
    stdio: 'ignore',
    env: { ...process.env, MOCK_TOKEN: TOKEN, MOCK_PROVISIONER: '0' },
  }),
];

// Minimal `vite preview`: dist/ with SPA fallback, /api proxied to the mock.
const dist = join(webDir, 'dist');
const types = { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml' };
const web = createServer((req, res) => {
  if (req.url.startsWith('/api/')) {
    const up = httpRequest(
      { host: 'localhost', port: API_PORT, path: req.url, method: req.method, headers: req.headers },
      (r) => {
        res.writeHead(r.statusCode, r.headers);
        r.pipe(res);
      },
    );
    up.on('error', () => res.writeHead(502).end());
    req.pipe(up);
    res.on('close', () => up.destroy());
    return;
  }
  const path = req.url.split('?')[0];
  let file = join(dist, path);
  if (!file.startsWith(dist) || !existsSync(file) || path === '/') file = join(dist, 'index.html');
  res.writeHead(200, { 'Content-Type': types[file.slice(file.lastIndexOf('.'))] || 'application/octet-stream' });
  res.end(readFileSync(file));
}).listen(WEB_PORT);

const cleanup = () => {
  procs.forEach((p) => p.kill());
  web.close();
};
process.on('exit', cleanup);

async function waitFor(url, ms = 20000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`timeout waiting for ${url}`);
}

const problems = [];
try {
  await waitFor(`${API}/api/health`);
  await waitFor(BASE);
  const auth = { headers: { Authorization: `Bearer ${TOKEN}` } };
  if ((await fetch(`${API}/api/tests`)).status !== 401) problems.push('mock did not require the token');
  const runs = await (await fetch(`${API}/api/runs`, auth)).json();
  const queued = runs.find((r) => r.status === 'queued');

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  let current = '';
  page.on('console', (m) => {
    // The 401s that trigger the sign-in screen and the provoked 409 are expected.
    if (m.type() === 'error' && !/status of (401|409)/.test(m.text())) problems.push(`[${current}] console.error: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`[${current}] pageerror: ${e.message}`));
  const shot = async (name, settle = 400) => {
    await page.waitForTimeout(settle);
    await page.screenshot({ path: join(outDir, name) });
    console.log('saved', name);
  };

  // 1. No token -> sign-in screen; a wrong token shows an error.
  current = 'signin';
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.waitForSelector('.signin-card');
  await page.fill('#access-token', 'wrong-token');
  await page.click('.signin-submit');
  await page.waitForSelector('.signin-card .alert.bad');
  await shot('20-signin.png');

  // 2. Correct token -> app; token persisted.
  await page.fill('#access-token', TOKEN);
  await page.click('.signin-submit');
  await page.waitForSelector('table.t tbody tr');
  const stored = await page.evaluate(() => localStorage.getItem('goload.token'));
  if (stored !== TOKEN) problems.push(`token not stored (got ${stored})`);
  if (!(await page.isVisible('.nav-btn:has-text("Sign out")'))) problems.push('Sign out not visible');

  // 3. Queued run.
  if (queued) {
    current = 'queued';
    await page.goto(BASE + `/runs/${queued.id}`, { waitUntil: 'networkidle' });
    await page.waitForSelector('.queued-state');
    await page.waitForSelector('.worker-slots');
    await shot('21-queued.png');
  } else problems.push('no queued run in mock');

  // 4. Run start dialog: hint, then a 409 "not enough idle workers".
  current = 'run-start';
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.waitForSelector('table.t tbody tr');
  await page.locator('tr', { hasText: 'spike' }).locator('button:has-text("Run")').click();
  await page.waitForSelector('.modal');
  await page.fill('#workers', '4');
  await page.waitForSelector('text=idle remote workers connected');
  await page.click('.modal button:has-text("Start run")');
  await page.waitForSelector('.modal .alert.warn');
  await shot('22-run-start.png');

  // 5. Workers.
  current = 'workers';
  await page.keyboard.press('Escape');
  await page.goto(BASE + '/workers', { waitUntil: 'networkidle' });
  await page.waitForSelector('tr.row-master');
  await shot('23-workers.png');

  // 6. Sign out -> sign-in screen, token cleared.
  current = 'signout';
  await page.click('.nav-btn:has-text("Sign out")');
  await page.waitForSelector('.signin-card');
  const after = await page.evaluate(() => localStorage.getItem('goload.token'));
  if (after !== null) problems.push('token not cleared on sign out');

  await browser.close();
} catch (e) {
  problems.push(`fatal: ${e.stack || e}`);
} finally {
  cleanup();
}

if (problems.length) {
  console.error('\nProblems:\n' + problems.join('\n'));
  process.exit(1);
}
console.log(`\nOK. Screenshots in ${outDir}`);
