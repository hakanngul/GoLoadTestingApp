// Screens for the Quick / Advanced editor modes and scenario auth. Same
// approach as screenshots-auth.mjs: starts the mock and a tiny static server
// for dist/ itself, drives the UI, fails on console errors.
//
//   pnpm build && node scripts/screenshots-modes.mjs [outDir]
//
// Ports: mock :28481, web :28482 (override with MOCK_PORT / WEB_PORT).
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
const API_PORT = Number(process.env.MOCK_PORT || 28481);
const WEB_PORT = Number(process.env.WEB_PORT || 28482);
const BASE = `http://localhost:${WEB_PORT}`;
const API = `http://localhost:${API_PORT}`;
mkdirSync(outDir, { recursive: true });

const procs = [
  spawn(process.execPath, [join(webDir, 'mock/server.mjs'), '--port', String(API_PORT)], {
    cwd: webDir,
    stdio: 'ignore',
  }),
];

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
  const tests = await (await fetch(`${API}/api/tests`)).json();
  const orders = tests.find((t) => t.name === 'orders-api');
  const login = tests.find((t) => t.name === 'login-flow');
  if (!orders || !login) throw new Error('seeded tests missing');

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  let current = '';
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push(`[${current}] console.error: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`[${current}] pageerror: ${e.message}`));
  const shot = async (name, opts = {}) => {
    await page.waitForTimeout(opts.settle ?? 500);
    await page.screenshot({ path: join(outDir, name), fullPage: !!opts.fullPage });
    console.log('saved', name);
  };
  const validated = () => page.waitForSelector('.validation-summary', { timeout: 5000 });

  // 30. Chooser.
  current = 'chooser';
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.click('button:has-text("New test")');
  await page.waitForSelector('.mode-choices');
  await shot('30-new-test-chooser.png');

  // 31. Quick, empty.
  current = 'quick-empty';
  await page.click('.mode-choice:has-text("Quick test")');
  await page.waitForSelector('.quick-form');
  await validated();
  await shot('31-quick-empty.png');

  // 32. Quick, filled with login auth + CSV hint + preview.
  current = 'quick-login';
  await page.fill('#q-name', 'product-search');
  await page.fill('#q-url', 'https://api.example.com/v1/products?page=1');
  await page.fill('#q-target', '40');
  await page.check('.label-check input');
  await page.click('.quick-card:has-text("Authentication") .segmented button:has-text("Login request")');
  await page.fill('#q-login-path', '/api/login');
  await page.click('.chip:has-text("data.access_token")');
  await page.waitForTimeout(700);
  const y32 = await page.evaluate(() => document.title);
  void y32;
  await page.evaluate(() => window.scrollTo(0, 0));
  await shot('32-quick-filled-login.png', { fullPage: true });

  // Check the generated YAML of the quick form.
  await page.click('.tabs button:has-text("YAML")');
  const genYaml = await page.locator('.code-editor textarea').inputValue();
  for (const want of [
    'base_url: https://api.example.com',
    'path: /v1/products?page=1',
    'type: login',
    'scope: per_user',
    'path: /api/login',
    'expr: data.access_token',
    'file: users.csv',
    'model: closed',
  ])
    if (!genYaml.includes(want)) problems.push(`quick YAML lacks "${want}"`);
  await page.click('.tabs button:has-text("Quick form")');

  // 33. Existing quick-representable test opened in Advanced: auth section.
  current = 'advanced-auth';
  await page.goto(BASE + `/tests/${orders.id}`, { waitUntil: 'networkidle' });
  await page.waitForSelector('.quick-form'); // representable -> opens in Quick
  await page.click('.mode-switch button:has-text("Advanced")');
  await page.waitForSelector('#sec-auth');
  await page.click('.section-nav button:has-text("Auth")');
  await page.waitForTimeout(800);
  await shot('33-advanced-auth-section.png');
  const stored = await page.evaluate((id) => localStorage.getItem(`goload.editorMode.${id}`), orders.id);
  if (stored !== 'advanced') problems.push(`mode not stored (got ${stored})`);

  // 35. YAML tab with auth (same test).
  current = 'yaml';
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.click('.tabs button:has-text("YAML")');
  await page.waitForSelector('.code-editor');
  await shot('35-yaml-with-auth.png');
  // Switch back to Quick works (representable).
  await page.click('.mode-switch button:has-text("Quick")');
  await page.click('.tabs button:has-text("Quick form")');
  await page.waitForSelector('.quick-form');
  if ((await page.inputValue('#q-token-path')) !== 'data.access_token') problems.push('quick did not load token path');

  // 34. Multi-step test: Quick is disabled; hover shows why.
  current = 'disabled-quick';
  await page.goto(BASE + `/tests/${login.id}`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#sec-target');
  if (!(await page.isDisabled('.mode-switch button:has-text("Quick")'))) problems.push('Quick not disabled for login-flow');
  await page.hover('.mode-switch .tip-wrap');
  await page.waitForSelector('.tip', { state: 'visible' });
  await shot('34-advanced-disabled-quick-tooltip.png');

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
