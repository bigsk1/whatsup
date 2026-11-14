import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import { defaultAppPreferences } from '../src/lib/state/preferences';
import { nativeHint } from './contact-events';

const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const names = [
  'GetStatus',
  'UnlockStorage',
  'LockStorage',
  'UnlockWithPassphrase',
  'GetStorageSecurity',
  'ConfigurePassphrase',
  'ExportRecovery',
  'StageRecoveryImport',
  'CommitRecoveryImport',
  'CancelRecoveryImport',
  'GetProfile',
  'GetContacts',
  'GetPairingStatus',
  'GetAppPreferences',
  'SetAttachmentInputContact',
] as const;
type Operation = (typeof names)[number];
const methods = new Map<number, Operation>();
for (const name of names) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing ${name} binding`);
  methods.set(Number(match[1]), name);
}
const secret = '  synthetic 🔐 passphrase  ';
const token = 'D'.repeat(32);
async function desktop(page: Page, required = true) {
  const state = {
    storage: required ? 'locked' : 'unlocked',
    required,
    holdUnlock: undefined as Promise<void> | undefined,
    holdSecurity: undefined as Promise<void> | undefined,
    failUnlock: false,
  };
  const calls: { operation: Operation; args: unknown[] }[] = [];
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
  });
  const status = () => ({
    ...unavailableStatus,
    storageState: state.storage,
    identityFingerprint: state.storage === 'unlocked' ? 'a'.repeat(64) : '',
    requiresPassphrase: state.required,
    messagingAvailable: state.storage === 'unlocked',
    pairingAvailable: state.storage === 'unlocked',
  });
  await page.route('**/wails/runtime', async (route) => {
    const body = route.request().postDataJSON(),
      operation = methods.get(body.args?.methodID),
      args: unknown[] = body.args?.args ?? [];
    if (!operation) throw new Error('Unexpected security fixture operation');
    calls.push({ operation, args });
    let result: unknown = null;
    switch (operation) {
      case 'GetStatus':
        result = status();
        break;
      case 'UnlockStorage':
        state.storage = 'unlocked';
        result = status();
        break;
      case 'LockStorage':
        state.storage = 'locked';
        result = status();
        break;
      case 'UnlockWithPassphrase':
        if (state.failUnlock) {
          await route.fulfill({
            status: 500,
            json: { kind: 'RuntimeError', message: secret },
          });
          return;
        }
        state.storage = 'unlocked';
        result = status();
        if (state.holdUnlock) await state.holdUnlock;
        break;
      case 'GetStorageSecurity':
        if (state.holdSecurity) await state.holdSecurity;
        result = {
          requiresPassphrase: state.required,
          recoveryAvailable: state.storage === 'unlocked',
          busy: false,
        };
        break;
      case 'ConfigurePassphrase':
        state.required = args[2] === true;
        result = status();
        break;
      case 'ExportRecovery':
        result = { saved: true };
        break;
      case 'StageRecoveryImport':
        result = {
          handle: token,
          contacts: 2,
          messages: 10001,
          attachments: 4,
          sourceRequiresPassphrase: true,
          replaceRequired: true,
          credentialResetRequired: true,
        };
        break;
      case 'CommitRecoveryImport':
        state.storage = 'unlocked';
        state.required = args[2] === true;
        result = status();
        break;
      case 'GetProfile':
        result = { displayName: '', shareDisplayName: false };
        break;
      case 'GetContacts':
        result = [];
        break;
      case 'GetPairingStatus':
        result = { ...unavailablePairing, state: 'idle' };
        break;
      case 'GetAppPreferences':
        result = defaultAppPreferences;
        break;
    }
    await route.fulfill({ json: result });
  });
  return { state, calls };
}
async function securityPage(page: Page) {
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  await page
    .getByRole('button', { name: 'Security & recovery', exact: true })
    .click();
}

test('startup asks once, cancel stays offline and explicit Go online reopens the prompt', async ({
  page,
}) => {
  const { calls } = await desktop(page);
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: 'Go online', exact: true });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('App passphrase', { exact: true }).fill(secret);
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.locator('input[type=password]')).toHaveValue('');
  await page.waitForTimeout(200);
  await expect(dialog).not.toBeVisible();
  expect(
    calls.filter(
      (x) =>
        x.operation === 'UnlockStorage' ||
        x.operation === 'UnlockWithPassphrase',
    ),
  ).toEqual([]);
  await page
    .getByRole('button', { name: 'Go online', exact: true })
    .first()
    .click();
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByLabel('App passphrase', { exact: true }),
  ).toHaveValue('');
});
test('unlock keeps exact Unicode and spaces, clears fields before awaiting RPC and stores no secret', async ({
  page,
}) => {
  const { state, calls } = await desktop(page);
  let release!: () => void;
  state.holdUnlock = new Promise<void>((done) => (release = done));
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: 'Go online', exact: true });
  await dialog.getByLabel('App passphrase', { exact: true }).fill(secret);
  await dialog.getByRole('button', { name: 'Unlock and go online' }).click();
  await expect(
    dialog.getByLabel('App passphrase', { exact: true }),
  ).toHaveValue('');
  await expect
    .poll(() => calls.find((x) => x.operation === 'UnlockWithPassphrase')?.args)
    .toEqual([secret]);
  release();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage })),
  ).not.toContain(secret);
});
test('stale unlock after native Go offline cannot restore decrypted access', async ({
  page,
}) => {
  const { state, calls } = await desktop(page);
  let release!: () => void;
  state.holdUnlock = new Promise<void>((done) => (release = done));
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: 'Go online', exact: true });
  await dialog.getByLabel('App passphrase', { exact: true }).fill(secret);
  await dialog.getByRole('button', { name: 'Unlock and go online' }).click();
  await expect
    .poll(() => calls.some((x) => x.operation === 'UnlockWithPassphrase'))
    .toBe(true);
  state.storage = 'locked';
  await nativeHint(page, 'whatsup:storage-changed', {
    state: 'locked',
    requiresPassphrase: true,
  });
  release();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toHaveCount(0);
});
test('Settings enables and disables passphrase through explicit cleared secret forms', async ({
  page,
}) => {
  const { calls } = await desktop(page, false);
  await page.goto('/');
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  await securityPage(page);
  await page
    .getByRole('button', { name: 'Require app passphrase…', exact: true })
    .click();
  let dialog = page.getByRole('dialog', { name: 'Require an app passphrase' });
  await dialog.getByLabel('New app passphrase', { exact: true }).fill(secret);
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole('region', { name: 'Settings', exact: true }),
  ).toBeVisible();
  await page
    .getByRole('button', { name: 'Require app passphrase…', exact: true })
    .click();
  await expect(
    dialog.getByLabel('New app passphrase', { exact: true }),
  ).toHaveValue('');
  await dialog.getByLabel('New app passphrase', { exact: true }).fill(secret);
  await dialog
    .getByLabel('Confirm app passphrase', { exact: true })
    .fill(secret + ' mismatch');
  await expect(
    dialog.getByRole('button', { name: 'Enable passphrase', exact: true }),
  ).toBeDisabled();
  await dialog
    .getByLabel('Confirm app passphrase', { exact: true })
    .fill(secret);
  await dialog
    .getByRole('button', { name: 'Enable passphrase', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    calls.find((x) => x.operation === 'ConfigurePassphrase')?.args,
  ).toEqual(['', secret, true]);
  await page
    .getByRole('button', { name: 'Disable app passphrase…', exact: true })
    .click();
  dialog = page.getByRole('dialog', { name: 'Disable the app passphrase' });
  await dialog.getByLabel('Current app passphrase').fill(secret);
  await dialog
    .getByRole('button', { name: 'Disable passphrase', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    calls.filter((x) => x.operation === 'ConfigurePassphrase').at(-1)?.args,
  ).toEqual([secret, '', false]);
});

test('wrong unlock secrets leave empty fields and fixed notices without echoing backend data', async ({
  page,
}) => {
  const { state } = await desktop(page);
  state.failUnlock = true;
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: 'Go online', exact: true });
  await dialog.getByLabel('App passphrase', { exact: true }).fill(secret);
  await dialog.getByRole('button', { name: 'Unlock and go online' }).click();
  await expect(dialog.getByRole('alert')).toContainText(
    'The passphrase was not accepted',
  );
  await expect(
    dialog.getByLabel('App passphrase', { exact: true }),
  ).toHaveValue('');
  await expect(dialog.getByText(secret, { exact: true })).toHaveCount(0);
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
});
test('recovery export and authenticated import use native dialogs and opaque handles with explicit replacement', async ({
  page,
}) => {
  const { calls } = await desktop(page, false);
  await page.goto('/');
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  await securityPage(page);
  await page
    .getByRole('button', { name: 'Export recovery copy…', exact: true })
    .click();
  let dialog = page.getByRole('dialog', {
    name: 'Export an encrypted recovery copy',
  });
  await dialog
    .getByLabel('New recovery passphrase', { exact: true })
    .fill(secret);
  await dialog
    .getByLabel('Confirm recovery passphrase', { exact: true })
    .fill(secret);
  await dialog.getByRole('button', { name: 'Choose export location…' }).click();
  await expect(dialog).not.toBeVisible();
  expect(calls.find((x) => x.operation === 'ExportRecovery')?.args).toEqual([
    secret,
  ]);
  await page
    .getByRole('button', { name: 'Import recovery copy…', exact: true })
    .click();
  dialog = page.getByRole('dialog', {
    name: 'Import an encrypted recovery copy',
  });
  await dialog.getByLabel('Recovery passphrase', { exact: true }).fill(secret);
  await dialog.getByRole('button', { name: 'Choose recovery copy…' }).click();
  dialog = page.getByRole('dialog', { name: 'Restore this recovery copy?' });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByText('The encrypted copy has been authenticated.', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    dialog.getByRole('button', { name: 'Restore recovery copy', exact: true }),
  ).toBeDisabled();
  await dialog
    .getByRole('checkbox', {
      name: 'Replace existing local data and its OS credential',
      exact: false,
    })
    .check();
  await dialog
    .getByRole('checkbox', {
      name: 'Require an app passphrase on this device',
      exact: true,
    })
    .check();
  await dialog.getByLabel('New app passphrase', { exact: true }).fill(secret);
  await dialog
    .getByLabel('Confirm app passphrase', { exact: true })
    .fill(secret);
  await dialog
    .getByRole('button', { name: 'Restore recovery copy', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(
    calls.find((x) => x.operation === 'StageRecoveryImport')?.args,
  ).toEqual([secret]);
  expect(
    calls.find((x) => x.operation === 'CommitRecoveryImport')?.args,
  ).toEqual([token, secret, true, true]);
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage })),
  ).not.toContain(secret);
});

test('a late security-summary response preserves the current passphrase form without echoing or persisting it', async ({
  page,
}) => {
  const { state } = await desktop(page, false);
  await page.goto('/');
  await securityPage(page);
  let release!: () => void;
  state.holdSecurity = new Promise<void>((resolve) => (release = resolve));
  await page
    .getByRole('button', { name: 'Export recovery copy…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Export an encrypted recovery copy',
  });
  await dialog
    .getByLabel('New recovery passphrase', { exact: true })
    .fill(secret);
  const summaryReply = page.waitForResponse((response) => {
    if (!response.url().endsWith('/wails/runtime')) return false;
    const body = response.request().postDataJSON();
    return methods.get(body?.args?.methodID) === 'GetStorageSecurity';
  });
  release();
  state.holdSecurity = undefined;
  await summaryReply;
  await expect(
    dialog.getByLabel('New recovery passphrase', { exact: true }),
  ).toHaveValue(secret);
  await dialog
    .getByLabel('Confirm recovery passphrase', { exact: true })
    .fill(secret);
  await expect(
    dialog.getByRole('button', {
      name: 'Choose export location…',
      exact: true,
    }),
  ).toBeEnabled();
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage })),
  ).not.toContain(secret);
});
