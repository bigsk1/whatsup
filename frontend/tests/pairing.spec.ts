import { nativeHint } from './contact-events';
import { serveDefaultAppPreferences } from './contact-events';
import { groupedFingerprint } from '../src/lib/state/fingerprint';
import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing, type PairingStatus } from '../src/lib/pairing';

type Operation =
  | 'GetStatus'
  | 'UnlockStorage'
  | 'LockStorage'
  | 'GetPairingStatus'
  | 'GetContacts'
  | 'GetMessages'
  | 'MarkRead'
  | 'GetProfile'
  | 'ApprovePairing'
  | 'DenyPairing';

// Exercise the real generated bindings/Wails HTTP transport. Responses below
// are synthetic UI fixtures, not OS credentials or two-device pairing evidence.
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const operations: Record<number, Operation> = {};
for (const name of [
  'GetStatus',
  'UnlockStorage',
  'LockStorage',
  'GetPairingStatus',
  'GetContacts',
  'GetMessages',
  'MarkRead',
  'GetProfile',
  'ApprovePairing',
  'DenyPairing',
] as const) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing generated ${name} binding`);
  operations[Number(match[1])] = name;
}

const requestA = 'A'.repeat(22);
const requestB = 'B'.repeat(21) + 'A';
const localFingerprint = 'a'.repeat(64);
const peerA = 'b'.repeat(64);
const peerB = 'c'.repeat(64);

function pending(
  selectedRequest = requestA,
  peerFingerprint = peerA,
): PairingStatus {
  return {
    state: 'awaiting-approval',
    requestID: selectedRequest,
    localFingerprint,
    peerFingerprint,
    expiresAt: new Date(Date.now() + 600_000).toISOString(),
  };
}

function injectRuntime() {
  const host = window as Window & {
    _wails?: { flags?: object; environment?: object };
  };
  host._wails ??= {};
  host._wails.flags = {};
  host._wails.environment = { OS: 'linux', Arch: 'amd64', Debug: false };
}

async function syntheticPairing(
  page: Page,
  initialPairing: unknown = { ...unavailablePairing, state: 'idle' },
  initialStorage = 'unlocked',
) {
  const state = {
    pairing: initialPairing,
    storage: initialStorage,
    holdPoll: undefined as undefined | (() => Promise<void>),
    approve: undefined as
      undefined | ((args: unknown[]) => Promise<PairingStatus>),
  };
  const calls: { operation: Operation; args: unknown[] }[] = [];
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/wails/runtime', async (route) => {
    if (await serveDefaultAppPreferences(route)) return;
    const body = route.request().postDataJSON();
    const operation = operations[body.args?.methodID];
    if (
      route.request().method() !== 'POST' ||
      body.object !== 0 ||
      body.method !== 0 ||
      !operation
    )
      throw new Error('Unexpected native runtime operation');
    const args = body.args?.args ?? [];
    calls.push({ operation, args });
    if (
      operation === 'GetStatus' ||
      operation === 'UnlockStorage' ||
      operation === 'LockStorage'
    ) {
      if (operation === 'UnlockStorage') state.storage = 'unlocked';
      if (operation === 'LockStorage') {
        state.storage = 'locked';
        state.pairing = { ...unavailablePairing };
      }
      await route.fulfill({
        json: {
          ...unavailableStatus,
          storageState: state.storage,
          identityFingerprint:
            state.storage === 'unlocked' ? localFingerprint : '',
        },
      });
      return;
    }
    if (operation === 'GetProfile') {
      await route.fulfill({
        json: { displayName: '', shareDisplayName: false },
      });
      return;
    } else if (operation === 'GetContacts') {
      await route.fulfill({
        json: [],
      });
      return;
    }
    if (operation === 'GetPairingStatus') {
      const snapshot = state.pairing;
      const hold = state.holdPoll;
      state.holdPoll = undefined;
      if (hold) await hold();
      await route.fulfill({ json: snapshot });
      return;
    }
    if (operation === 'ApprovePairing') {
      if (state.approve) {
        await route.fulfill({ json: await state.approve(args) });
      } else {
        state.pairing = {
          ...(state.pairing as PairingStatus),
          state: 'waiting-for-peer',
        };
        await route.fulfill({ json: state.pairing });
      }
      return;
    }
    state.pairing = { ...unavailablePairing, state: 'canceled' };
    await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
    await route.fulfill({ json: state.pairing });
  });
  await page.addInitScript(injectRuntime);
  return { state, calls, errors };
}

test('shows only a reserved request, defaults to Deny and waits after approval', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticPairing(page);
  await page.goto('/');
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  state.pairing = pending();
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  const dialog = page.getByRole('dialog', { name: 'Pairing request' });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByText(groupedFingerprint(peerA), { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByText(groupedFingerprint(localFingerprint), { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole('button', { name: 'Deny', exact: true }),
  ).toBeFocused();
  await dialog.getByRole('button', { name: 'Approve', exact: true }).click();
  const waiting = page.getByRole('dialog', {
    name: 'Waiting for the other person',
  });
  await expect(waiting).toBeVisible();
  await expect(
    waiting.getByRole('button', { name: 'Cancel pairing' }),
  ).toBeEnabled();
  expect(
    calls.find((call) => call.operation === 'ApprovePairing')?.args,
  ).toEqual([requestA, peerA]);
  await expect(
    page.getByRole('button', { name: 'Send message unavailable' }),
  ).toBeDisabled();
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('Escape denies and a later reserved request can be denied with its button', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticPairing(page, pending());
  await page.goto('/');
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  await expect(
    page.getByText(groupedFingerprint(peerA), { exact: true }),
  ).toHaveCount(0);
  state.pairing = pending(requestB, peerB);
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  const next = page.getByRole('dialog', { name: 'Pairing request' });
  await expect(
    next.getByText(groupedFingerprint(peerB), { exact: true }),
  ).toBeVisible();
  await next.getByRole('button', { name: 'Deny', exact: true }).click();
  await expect(next).toHaveCount(0);
  expect(
    calls
      .filter((call) => call.operation === 'DenyPairing')
      .map((call) => call.args),
  ).toEqual([[requestA], [requestB]]);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('rejects invalid and unexpected pairing DTO fields without displaying identity', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticPairing(page, {
    ...pending(),
    peerFingerprint: peerA + '\u202e',
  });
  await page.goto('/');
  await expect
    .poll(
      () =>
        calls.filter((call) => call.operation === 'GetPairingStatus').length,
    )
    .toBeGreaterThan(0);
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  state.pairing = { ...pending(), bearer: 'synthetic-unexpected-field' };
  const previousCalls = calls.length;
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await expect.poll(() => calls.length).toBeGreaterThan(previousCalls);
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  state.pairing = pending();
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test('pairing stays idle without polling and reloads immediately from a revision hint', async ({
  page,
}) => {
  const { state, calls } = await syntheticPairing(page);
  await page.goto('/');
  await expect
    .poll(
      () =>
        calls.filter((call) => call.operation === 'GetPairingStatus').length,
    )
    .toBeGreaterThan(0);
  await page.waitForTimeout(100);
  const count = calls.filter(
    (call) => call.operation === 'GetPairingStatus',
  ).length;
  await page.waitForTimeout(2100);
  expect(
    calls.filter((call) => call.operation === 'GetPairingStatus').length,
  ).toBe(count);
  state.pairing = pending();
  await nativeHint(page, 'whatsup:pairing-changed', { revision: 50 });
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
});

test('a delayed old approval response cannot replace a newer request', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticPairing(page, pending());
  let release: () => void = () => {};
  state.approve = () =>
    new Promise<PairingStatus>((resolve) => {
      release = () => resolve({ ...pending(), state: 'waiting-for-peer' });
    });
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: 'Pairing request' });
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Approve', exact: true }).click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'ApprovePairing'))
    .toBe(true);
  state.pairing = pending(requestB, peerB);
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await expect(
    dialog.getByText(groupedFingerprint(peerB), { exact: true }),
  ).toBeVisible();
  await expect(
    dialog.getByRole('button', { name: 'Approve', exact: true }),
  ).toBeEnabled();
  const oldResponse = page.waitForResponse((response) => {
    const body = response.request().postDataJSON();
    return operations[body?.args?.methodID] === 'ApprovePairing';
  });
  release();
  await oldResponse;
  await page.waitForTimeout(100);
  await expect(
    dialog.getByText(groupedFingerprint(peerB), { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Waiting for the other person' }),
  ).toHaveCount(0);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('Deny remains available during approval and a late response cannot restore it', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticPairing(page, pending());
  let release: () => void = () => {};
  state.approve = () =>
    new Promise<PairingStatus>((resolve) => {
      release = () => resolve({ ...pending(), state: 'waiting-for-peer' });
    });
  await page.goto('/');
  const dialog = page.getByRole('dialog', { name: 'Pairing request' });
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: 'Approve', exact: true }).click();
  await expect(
    dialog.getByRole('button', { name: 'Approving…' }),
  ).toBeDisabled();
  await expect(
    dialog.getByRole('button', { name: 'Deny', exact: true }),
  ).toBeEnabled();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  const oldResponse = page.waitForResponse((response) => {
    const body = response.request().postDataJSON();
    return operations[body?.args?.methodID] === 'ApprovePairing';
  });
  release();
  await oldResponse;
  await page.waitForTimeout(100);
  await expect(
    page.getByRole('dialog', { name: 'Waiting for the other person' }),
  ).toHaveCount(0);
  await expect(
    page.getByText(groupedFingerprint(peerA), { exact: true }),
  ).toHaveCount(0);
  expect(calls.find((call) => call.operation === 'DenyPairing')?.args).toEqual([
    requestA,
  ]);
  expect(errors).toEqual([]);
});

test('locking storage ignores an in-flight request and stops polling', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticPairing(page);
  await page.goto('/');
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  let release: () => void = () => {};
  let held = false;
  state.pairing = pending();
  state.holdPoll = () =>
    new Promise<void>((resolve) => {
      held = true;
      release = resolve;
    });
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await expect.poll(() => held).toBe(true);
  await page.keyboard.press('Control+,');
  const settings = page.getByRole('region', { name: 'Settings', exact: true });
  await expect(settings).toBeVisible();
  await settings
    .getByRole('button', { name: 'Privacy & diagnostics', exact: true })
    .click();
  await settings.getByRole('button', { name: 'Go offline' }).click();
  await expect(
    settings.getByText('Locked on this device', { exact: true }),
  ).toBeVisible();
  release();
  await page.waitForTimeout(100);
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  const count = calls.filter(
    (call) => call.operation === 'GetPairingStatus',
  ).length;
  await page.waitForTimeout(1100);
  expect(
    calls.filter((call) => call.operation === 'GetPairingStatus'),
  ).toHaveLength(count);
  await settings.getByRole('button', { name: 'Go online' }).click();
  await expect(
    settings.getByText('Unlocked on this device', { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('restored pending setup stays inert and can be discarded without an approval popup', async ({
  page,
}) => {
  const { calls, errors } = await syntheticPairing(page, {
    ...pending(),
    state: 'pending',
    expiresAt: '',
  });
  await page.goto('/');
  await expect(
    page.getByText('Contact setup is incomplete.', { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  await page.getByRole('button', { name: 'Discard setup' }).click();
  await expect(page.getByRole('button', { name: 'Discard setup' })).toHaveCount(
    0,
  );
  expect(calls.find((call) => call.operation === 'DenyPairing')?.args).toEqual([
    requestA,
  ]);
  await expect(
    page.getByRole('button', { name: 'Send message unavailable' }),
  ).toBeDisabled();
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('expired requests remove the fingerprint and approval controls', async ({
  page,
}) => {
  const { state, errors } = await syntheticPairing(page, pending());
  await page.goto('/');
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
  state.pairing = { ...unavailablePairing, state: 'expired' };
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  await expect(
    page.getByText(groupedFingerprint(peerA), { exact: true }),
  ).toHaveCount(0);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('completed pairing closes approval and reports approval without messaging readiness', async ({
  page,
}) => {
  const { state, errors } = await syntheticPairing(page, pending());
  await page.goto('/');
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
  state.pairing = { ...unavailablePairing, state: 'completed' };
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await expect(
    page.getByText('Contact approved. Waiting for the saved conversation.', {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  await expect(
    page.getByText(groupedFingerprint(peerA), { exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole('button', { name: 'Send message unavailable' }),
  ).toBeDisabled();
  expect(errors).toEqual([]);
});

test('restored committed setup requires reconnection and offers inert discard only', async ({
  page,
}) => {
  const { calls, errors } = await syntheticPairing(page, {
    ...pending(),
    state: 'reconnect-required',
    expiresAt: '',
  });
  await page.goto('/');
  await expect(
    page.getByText('This saved setup cannot reconnect yet.', { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  await page.getByRole('button', { name: 'Discard setup' }).click();
  await expect(page.getByRole('button', { name: 'Discard setup' })).toHaveCount(
    0,
  );
  expect(calls.find((call) => call.operation === 'DenyPairing')?.args).toEqual([
    requestA,
  ]);
  await expect(
    page.getByRole('button', { name: 'Send message unavailable' }),
  ).toBeDisabled();
  expect(errors).toEqual([]);
});
