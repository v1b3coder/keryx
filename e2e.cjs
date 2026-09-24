/**
 * PLAN_HEARTBEAT.md Task 8: the Firefox E2E for the registration heartbeat and
 * the foreground recovery, against the local relay harness.
 *
 * Phase 1 (wake-up heartbeat): drive the first-company enable flow, publish a
 * real wake-up on the harness, and assert the service worker's
 * POST /v1/registrations/{id}/heartbeat → 204 and the app's no-red-bar state.
 * Phase 2 (foreground recovery): restart the harness with an empty registry
 * DB, reload the PWA, and assert the foreground check's heartbeat → 404
 * followed by POST /v1/registrations → 200 with a fresh registration id.
 *
 * Run (from the repo root, with the app preview at localhost:4173):
 *   PW=$(ls -d ~/.npm/_npx/+/node_modules/playwright | head -1)
 *   NODE_PATH=$(dirname "$PW") node e2e.cjs
 */
'use strict';

const { firefox } = require('playwright');
const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const APP = process.env.APP ?? 'http://localhost:4173/';
const BASE = process.env.BASE ?? 'http://127.0.0.1:18099';
const RELAY_DIR = path.join(__dirname, 'relay');
const BIN = '/tmp/keryx-relay-harness';
const LOG = '/tmp/keryx-relay-harness-e2e.log';
const PROFILE = '/tmp/keryx-e2e-profile';
const VAPID = JSON.parse(fs.readFileSync('/tmp/keryx-e2e-vapid.json', 'utf8'));

let harness = null;

function log(text) {
  console.log(`[e2e] ${text}`);
}

function buildHarness() {
  const res = spawnSync('go', ['build', '-o', BIN, './cmd/relay-harness'], {
    cwd: RELAY_DIR,
    stdio: 'inherit',
  });
  if (res.status !== 0) throw new Error('relay-harness build failed');
}

function startHarness() {
  fs.writeFileSync(LOG, '');
  const out = fs.openSync(LOG, 'a');
  harness = spawn(BIN, ['-vapid-private', VAPID.private], {
    cwd: RELAY_DIR,
    stdio: ['ignore', out, out],
  });
}

async function stopHarness() {
  if (!harness) return;
  const child = harness;
  harness = null;
  child.kill('SIGTERM');
  await new Promise((resolve) => {
    const t = setTimeout(resolve, 5000);
    child.once('exit', () => {
      clearTimeout(t);
      resolve();
    });
  });
}

async function waitForInfo(timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${BASE}/test/info`);
      if (res.ok) return await res.json();
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('relay-harness did not become ready');
}

function logText() {
  return fs.readFileSync(LOG, 'utf8');
}

async function waitForLog(pattern, timeoutMs, from = 0) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const m = logText().slice(from).match(pattern);
    if (m) return m;
    await new Promise((r) => setTimeout(r, 1000));
  }
  throw new Error(`timed out waiting for ${pattern} in ${LOG}`);
}

async function publish() {
  const res = await fetch(`${BASE}/test/publish`, { method: 'POST' });
  if (!res.ok) throw new Error(`/test/publish: HTTP ${res.status}`);
}

async function registrations(page) {
  return page.evaluate(
    () =>
      new Promise((resolve, reject) => {
        const open = indexedDB.open('keryx');
        open.onsuccess = () => {
          const db = open.result;
          const tx = db.transaction('registrations', 'readonly');
          const all = tx.objectStore('registrations').getAll();
          all.onsuccess = () => resolve(all.result);
          all.onerror = () => reject(all.error);
        };
        open.onerror = () => reject(open.error);
      }),
  );
}

async function noRedBar(page) {
  return (await page.locator('.banner-danger').count()) === 0;
}

async function driveEnableFlow(page, joinUrl) {
  await page.getByRole('button', { name: 'Add a company' }).click();
  await page.getByRole('button', { name: 'Paste a link' }).click();
  await page.getByPlaceholder('Paste the company link or domain').fill(joinUrl);
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByText('Confirm this website').waitFor({ timeout: 15000 });
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('button', { name: 'Subscribe' }).waitFor({ timeout: 30000 });
  await page.getByRole('button', { name: 'Subscribe' }).click();
  await page.getByText('Turn on notifications').waitFor({ timeout: 30000 });
  await page.getByRole('button', { name: 'Turn on' }).click();
  await page.waitForFunction(
    () =>
      document.body.innerText.includes('Notifications are working') ||
      document.body.innerText.includes('The first test is still on its way'),
    null,
    { timeout: 30000 },
  );
  const cont = page.getByRole('button', { name: 'Continue' });
  if (await cont.count()) await cont.click();
  await page.locator('.companybar-origin').waitFor({ timeout: 20000 });
}

(async () => {
  await stopHarness();
  fs.rmSync(PROFILE, { recursive: true, force: true });
  for (const name of ['main', 'reg']) fs.rmSync(`/tmp/relay-harness-${name}.db`, { force: true });
  buildHarness();
  startHarness();
  const info = await waitForInfo();
  log(`harness ready: ${info.joinUrl}`);

  const browser = await firefox.launchPersistentContext(PROFILE, {
    headless: true,
    ignoreHTTPSErrors: true,
    viewport: { width: 420, height: 900 },
    firefoxUserPrefs: {
      'permissions.default.desktop-notification': 1,
      'dom.push.enabled': true,
      'dom.push.connection.enabled': true,
      'dom.push.serverURL': 'wss://push.services.mozilla.com/',
    },
  });
  const page = browser.pages()[0] ?? (await browser.newPage());
  page.on('pageerror', (err) => log(`pageerror: ${err.message}`));

  // --- phase 1: enable notifications, then a real wake-up heartbeat --------
  await page.goto(APP, { waitUntil: 'load' });
  await driveEnableFlow(page, info.joinUrl);
  const regs = await registrations(page);
  const oldId = regs[0]?.id;
  if (!oldId) throw new Error('no local registration after the enable flow');
  log(`enabled; registration ${oldId}`);

  let mark = logText().length;
  await publish();
  const hb = await waitForLog(
    /path=\/v1\/registrations\/([^ ]+)\/heartbeat status=204/,
    5 * 60 * 1000,
    mark,
  );
  log(`wake-up heartbeat 204 for ${hb[1]}`);
  if (hb[1] !== oldId) throw new Error(`heartbeat id ${hb[1]} != registration ${oldId}`);
  if (!(await noRedBar(page))) throw new Error('red bar after the wake-up heartbeat');
  log('no red bar after the wake-up heartbeat');

  // --- phase 2: restart the harness with an empty registry, then recover ----
  mark = logText().length;
  await publish(); // keep the registration fresh before the restart
  await new Promise((r) => setTimeout(r, 1500));
  await stopHarness();
  for (const suffix of ['', '-shm', '-wal']) {
    fs.rmSync(`/tmp/relay-harness-reg.db${suffix}`, { force: true });
  }
  startHarness();
  await waitForInfo();
  log('harness restarted with an empty registry');

  mark = logText().length;
  await page.reload({ waitUntil: 'load' });
  await waitForLog(
    /path=\/v1\/registrations\/[^ ]+\/heartbeat status=404/,
    60 * 1000,
    mark,
  );
  log('foreground heartbeat 404 observed');
  await waitForLog(/method=POST path=\/v1\/registrations status=200/, 60 * 1000, mark);
  log('fresh registration POST 200 observed');

  await page.locator('.companybar-origin').waitFor({ timeout: 20000 });
  const fresh = await registrations(page);
  const newId = fresh[0]?.id;
  if (!newId || newId === oldId) {
    throw new Error(`registration not refreshed: ${oldId} -> ${newId}`);
  }
  if (!(await noRedBar(page))) throw new Error('red bar after the foreground recovery');
  log(`recovered ${oldId} -> ${newId}; no red bar`);

  await browser.close();
  await stopHarness();
  log('E2E PASS');
})().catch(async (err) => {
  console.error('E2E FAIL:', err);
  await stopHarness();
  process.exit(1);
});
