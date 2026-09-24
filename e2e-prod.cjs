/**
 * PLAN_HEARTBEAT.md Task 8 Step 6: the production Firefox run (AGENTS.md
 * "Production run"): drive the deployed PWA against the production relay,
 * publish a wake-up with `bin/pub notify`, and assert the SW's heartbeat.
 *
 * Run: APP=https://v1b3coder.github.io/keryx/ JOIN=https://keryx-demo.github.io/join.txt \
 *      BASE=https://keryx-relay.fly.dev NODE_PATH=... node e2e-prod.cjs
 */
'use strict';

const { firefox } = require('playwright');
const { spawnSync, execSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const APP = process.env.APP ?? 'https://v1b3coder.github.io/keryx/';
const JOIN = process.env.JOIN ?? 'https://keryx-demo.github.io/join.txt';
const BASE = process.env.BASE ?? 'https://keryx-relay.fly.dev';
const REPO_DIR = process.env.REPO_DIR ?? path.join(__dirname, '..', 'keryx-demo');
const KEYSTORE = process.env.KEYSTORE ?? path.join(__dirname, '..', 'keryx-demo-keys');
const PROFILE = '/tmp/keryx-e2e-prod-profile';

function log(text) {
  console.log(`[prod] ${text}`);
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

function flyLogs() {
  try {
    return execSync(`fly logs -a keryx-relay --no-tail 2>/dev/null`, {
      encoding: 'utf8',
      timeout: 60000,
    });
  } catch {
    return '';
  }
}

async function waitForHeartbeat(id, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const m = flyLogs().match(
      new RegExp(`path=/v1/registrations/${id}/heartbeat status=(\\d+)`),
    );
    if (m) return Number(m[1]);
    await new Promise((r) => setTimeout(r, 5000));
  }
  return null;
}

(async () => {
  fs.rmSync(PROFILE, { recursive: true, force: true });
  const browser = await firefox.launchPersistentContext(PROFILE, {
    headless: true,
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

  await page.goto(APP, { waitUntil: 'load' });
  // join.txt is a one-line redirect to the real join URL
  const joinUrl = (await (await fetch(JOIN)).text()).trim();
  log(`join url: ${joinUrl.slice(0, 60)}…`);
  await page.getByRole('button', { name: 'Add a company' }).click();
  await page.getByRole('button', { name: 'Paste a link' }).click();
  await page.getByPlaceholder('Paste the company link or domain').fill(joinUrl);
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByText('Confirm this website').waitFor({ timeout: 20000 });
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
  const regs = await registrations(page);
  const id = regs.find((r) => r.baseUrl === BASE)?.id;
  if (!id) throw new Error(`no production registration for ${BASE}`);
  log(`registered ${id}`);
  if ((await page.locator('.banner-danger').count()) !== 0) {
    throw new Error('red bar before the production wake-up');
  }
  log('no red bar after the enable flow');

  const notify = spawnSync(
    path.join(__dirname, 'bin', 'pub'),
    [
      'notify',
      '--repo', path.join(REPO_DIR, 'keryx'),
      '--channel', 'security',
      '--company', 'keryx-demo.github.io',
      '--relay', BASE,
      '--keystore', KEYSTORE,
    ],
    { encoding: 'utf8', timeout: 360000 },
  );
  process.stdout.write(notify.stdout ?? '');
  process.stderr.write(notify.stderr ?? '');
  if (notify.status !== 0) throw new Error(`pub notify failed: ${notify.status}`);

  const status = await waitForHeartbeat(id, 3 * 60 * 1000);
  if (status !== 204) throw new Error(`heartbeat for ${id}: ${status}`);
  log(`production wake-up heartbeat 204 for ${id}`);
  if ((await page.locator('.banner-danger').count()) !== 0) {
    throw new Error('red bar after the production wake-up');
  }
  log('no red bar after the production wake-up');

  await browser.close();
  log('PROD E2E PASS');
})().catch((err) => {
  console.error('PROD E2E FAIL:', err);
  process.exit(1);
});
