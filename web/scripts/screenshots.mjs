// Loads every screen against the mock backend, asserts there are no console
// errors and saves screenshots.
//
//   pnpm build && node scripts/screenshots.mjs [outDir]
//
// Starts `mock/server.mjs` (:8080) and `vite preview` (:4173) itself unless
// SKIP_SERVERS=1 is set (then BASE_URL, default http://localhost:4173, is used).
import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const webDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const outDir = resolve(process.argv[2] || join(webDir, 'screenshots'));
const BASE = process.env.BASE_URL || 'http://localhost:4173';
const API = process.env.API_URL || 'http://localhost:8080';
mkdirSync(outDir, { recursive: true });

const procs = [];
async function waitFor(url, ms = 20000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`timeout waiting for ${url}`);
}

if (!process.env.SKIP_SERVERS) {
  procs.push(spawn(process.execPath, [join(webDir, 'mock/server.mjs')], { cwd: webDir, stdio: 'ignore' }));
  procs.push(
    spawn(join(webDir, 'node_modules/.bin/vite'), ['preview', '--port', '4173', '--strictPort'], {
      cwd: webDir,
      stdio: 'ignore',
    }),
  );
}
const cleanup = () => procs.forEach((p) => p.kill());
process.on('exit', cleanup);

const problems = [];
try {
  await waitFor(`${API}/api/health`);
  await waitFor(BASE);

  const runs = await (await fetch(`${API}/api/runs`)).json();
  const running = runs.find((r) => r.status === 'running');
  const failed = runs.find((r) => r.status === 'failed');
  const soak = runs.filter((r) => r.test_name === 'catalog-soak' && r.status === 'passed');
  const tests = await (await fetch(`${API}/api/tests`)).json();
  const login = tests.find((t) => t.name === 'login-flow') ?? tests[0];

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  let current = '';
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push(`[${current}] console.error: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`[${current}] pageerror: ${e.message}`));

  const shot = async (name, opts = {}) => {
    await page.waitForTimeout(opts.settle ?? 400);
    await page.screenshot({ path: join(outDir, name), fullPage: !!opts.fullPage });
    console.log('saved', name);
  };
  const go = async (label, path) => {
    current = label;
    await page.goto(BASE + path, { waitUntil: 'networkidle' });
  };

  await go('tests', '/');
  await page.waitForSelector('table.t tbody tr');
  await shot('01-tests.png');

  await go('editor-form', `/tests/${login.id}`);
  await page.waitForSelector('.step-card');
  await page.waitForSelector('.validation-summary');
  await shot('02-editor-form.png', { fullPage: true });

  current = 'editor-yaml';
  await page.click('.tabs button:has-text("YAML")');
  await page.waitForSelector('.code-editor textarea');
  await shot('03-editor-yaml.png');

  await go('run-start', '/');
  await page.waitForSelector('table.t tbody tr');
  await page.locator('tr', { hasText: 'spike' }).locator('button:has-text("Run")').click();
  await page.waitForSelector('.modal');
  await shot('04-run-start.png');

  if (running) {
    await go('live', `/runs/${running.id}`);
    await page.waitForSelector('.recharts-line');
    await page.waitForTimeout(2500);
    await shot('05-live.png');
  } else problems.push('no running run in mock — 05-live.png skipped');

  if (failed) {
    await go('result', `/runs/${failed.id}`);
    await page.waitForSelector('.banner');
    await page.waitForSelector('.recharts-line');
    await shot('06-result.png', { fullPage: true });
  }

  if (soak.length >= 2) {
    await go('compare', `/compare?a=${soak[soak.length - 1].id}&b=${soak[0].id}`);
    await page.waitForSelector('.recharts-line');
    await shot('07-compare.png', { fullPage: true });
  }

  await go('workers', '/workers');
  await page.waitForSelector('table.t tbody tr');
  await shot('08-workers.png');

  await go('runs', '/runs');
  await page.waitForSelector('table.t tbody tr');
  await shot('09-runs.png');

  await go('not-found', '/nope');
  await page.waitForSelector('.empty');

  // Dark mode spot-check.
  await page.emulateMedia({ colorScheme: 'dark' });
  if (failed) {
    await go('result-dark', `/runs/${failed.id}`);
    await page.waitForSelector('.recharts-line');
    await shot('10-result-dark.png');
  }

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
console.log(`\nOK — no console errors. Screenshots in ${outDir}`);
