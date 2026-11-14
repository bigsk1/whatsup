import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import {
  unavailablePairing,
  type InvitationShare,
  type PairingStatus,
} from '../src/lib/pairing';

type Operation =
  | 'GetStatus'
  | 'UnlockStorage'
  | 'LockStorage'
  | 'GetPairingStatus'
  | 'ApprovePairing'
  | 'DenyPairing'
  | 'CreateInvitation'
  | 'JoinInvitation'
  | 'CancelInvitation';

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
  'ApprovePairing',
  'DenyPairing',
  'CreateInvitation',
  'JoinInvitation',
  'CancelInvitation',
] as const) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing generated ${name} binding`);
  operations[Number(match[1])] = name;
}

const relayURL = 'https://relay.example.invalid/map.json';
// Deliberately synthetic bearer text. This exercises UI transport only and is
// not a valid Go invitation or evidence of an actual public service connection.
const privateLink =
  'whatsup://pair/v1?id=synthetic&secret=SYNTHETIC-INVITATION-BEARER&owner=synthetic&endpoint=synthetic&expires=9999999999';
const reserved: PairingStatus = {
  state: 'awaiting-approval',
  requestID: 'A'.repeat(22),
  localFingerprint: 'a'.repeat(64),
  peerFingerprint: 'b'.repeat(64),
  expiresAt: new Date(Date.now() + 600_000).toISOString(),
};

function injectRuntime() {
  const host = window as Window & {
    _wails?: { flags?: object; environment?: object };
  };
  host._wails ??= {};
  host._wails.flags = {};
  host._wails.environment = { OS: 'linux', Arch: 'amd64', Debug: false };
  const clipboard: string[] = [];
  (window as Window & { testClipboard?: string[] }).testClipboard = clipboard;
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: {
      writeText: async (value: string) => {
        clipboard.push(value);
      },
    },
  });
}

async function syntheticInvitations(page: Page, storage = 'unlocked') {
  const state = {
    storage,
    pairing: { ...unavailablePairing, state: 'idle' } as PairingStatus,
    create: undefined as undefined | (() => Promise<InvitationShare>),
    join: undefined as undefined | (() => Promise<PairingStatus>),
    cancelFailure: false,
    expiry: () => new Date(Date.now() + 600_000).toISOString(),
  };
  const calls: { operation: Operation; args: unknown[] }[] = [];
  const errors: string[] = [];
  const external: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:9245/'))
      external.push(request.url());
  });
  await page.route('**/wails/runtime', async (route) => {
    const body = route.request().postDataJSON();
    const operation = operations[body.args?.methodID];
    if (
      route.request().method() !== 'POST' ||
      body.object !== 0 ||
      body.method !== 0 ||
      !operation
    )
      throw new Error('Unexpected native runtime operation');
    const args: unknown[] = body.args?.args ?? [];
    calls.push({ operation, args });
    if (['GetStatus', 'UnlockStorage', 'LockStorage'].includes(operation)) {
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
            state.storage === 'unlocked' ? 'a'.repeat(64) : '',
          pairingAvailable: state.storage === 'unlocked',
        },
      });
    } else if (operation === 'GetPairingStatus') {
      await route.fulfill({ json: state.pairing });
    } else if (operation === 'CreateInvitation') {
      const result = state.create
        ? await state.create()
        : { link: privateLink, expiresAt: state.expiry() };
      await route.fulfill({ json: result });
    } else if (operation === 'JoinInvitation') {
      const result = state.join
        ? await state.join()
        : { ...unavailablePairing, state: 'connecting' };
      state.pairing = result as PairingStatus;
      await route.fulfill({ json: result });
    } else if (operation === 'ApprovePairing') {
      state.pairing = { ...state.pairing, state: 'waiting-for-peer' };
      await route.fulfill({ json: state.pairing });
    } else if (operation === 'CancelInvitation' && state.cancelFailure) {
      await route.fulfill({
        status: 503,
        json: {
          kind: 'RuntimeError',
          message: 'SYNTHETIC-PRIVATE-PERSISTENCE-DETAIL',
        },
      });
    } else {
      state.pairing = { ...unavailablePairing, state: 'canceled' };
      await route.fulfill({ json: state.pairing });
    }
  });
  await page.addInitScript(injectRuntime);
  return { state, calls, errors, external };
}

async function openDialog(page: Page) {
  await page
    .getByRole('button', { name: 'Start a conversation', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Start a conversation',
    exact: true,
  });
  await expect(dialog).toBeVisible();
  return dialog;
}

async function allowService(dialog: ReturnType<Page['getByRole']>) {
  await dialog.getByLabel('Connection service HTTPS URL').fill(relayURL);
  await dialog.getByRole('checkbox').check();
}

test('opening invitations makes no public request and requires URL plus explicit relay consent', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  const create = dialog.getByRole('button', {
    name: 'Create private invitation',
  });
  await expect(dialog.getByLabel('Connection service HTTPS URL')).toHaveValue(
    '',
  );
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  await expect(create).toBeDisabled();
  await dialog.getByLabel('Connection service HTTPS URL').fill(relayURL);
  await expect(create).toBeDisabled();
  await dialog.getByRole('checkbox').check();
  await expect(create).toBeEnabled();
  await dialog
    .getByLabel('Connection service HTTPS URL')
    .fill('http://relay.example.invalid/map.json');
  await expect(create).toBeDisabled();
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  expect(
    calls.filter((call) =>
      ['CreateInvitation', 'JoinInvitation'].includes(call.operation),
    ),
  ).toEqual([]);
  expect(external).toEqual([]);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('private link is shared only on explicit creation and copied only on explicit Copy', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  const originalURL = page.url();
  await allowService(await openDialog(page));
  await page.getByRole('button', { name: 'Create private invitation' }).click();
  const share = page.getByRole('dialog', { name: 'Share your invitation' });
  await expect(share.getByLabel('Private invitation link')).toHaveValue(
    privateLink,
  );
  expect(
    calls.find((call) => call.operation === 'CreateInvitation')?.args,
  ).toEqual([relayURL, true]);
  expect(
    await page.evaluate(
      () => (window as Window & { testClipboard?: string[] }).testClipboard,
    ),
  ).toEqual([]);
  await share.getByRole('button', { name: 'Copy invitation' }).click();
  expect(
    await page.evaluate(
      () => (window as Window & { testClipboard?: string[] }).testClipboard,
    ),
  ).toEqual([privateLink]);
  await page.keyboard.press('Escape');
  await expect(page.locator('.invitation-dialog')).not.toBeVisible();
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  expect(
    calls.filter((call) => call.operation === 'CancelInvitation'),
  ).toHaveLength(1);
  expect(page.url()).toBe(originalURL);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('joining requires consent, clears pasted bearer and hands reservation to the approval popup', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  await dialog.getByRole('radio', { name: 'Paste invitation' }).check();
  await dialog
    .getByLabel('Private invitation', { exact: true })
    .fill(privateLink);
  const join = dialog.getByRole('button', { name: 'Open private invitation' });
  await expect(join).toBeDisabled();
  await allowService(dialog);
  await join.click();
  await expect(
    dialog.getByText('Opening the private invitation.', { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByLabel('Private invitation', { exact: true }),
  ).toHaveCount(0);
  // The waiting UI renders before the generated binding reaches the HTTP
  // route. Observe the real synthetic RPC and its initial state before
  // replacing that state with the reserved peer's response.
  await expect
    .poll(() => ({
      args: calls.find((call) => call.operation === 'JoinInvitation')?.args,
      state: state.pairing.state,
    }))
    .toEqual({ args: [privateLink, relayURL, true], state: 'connecting' });
  state.pairing = { ...reserved, expiresAt: state.expiry() };
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
  await expect(dialog).toHaveCount(0);
  expect(
    calls.filter((call) => call.operation === 'CancelInvitation'),
  ).toHaveLength(0);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('reservation hides the shared bearer without canceling its pairing channel', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticInvitations(page);
  await page.goto('/');
  await allowService(await openDialog(page));
  await page.getByRole('button', { name: 'Create private invitation' }).click();
  await expect(
    page.getByRole('dialog', { name: 'Share your invitation' }),
  ).toBeVisible();
  state.pairing = { ...reserved, expiresAt: state.expiry() };
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toBeVisible();
  await expect(
    page.getByRole('dialog', { name: 'Share your invitation' }),
  ).toHaveCount(0);
  await expect(page.locator('.invitation-dialog')).not.toBeVisible();
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  expect(
    calls.filter((call) => call.operation === 'CancelInvitation'),
  ).toHaveLength(0);
  expect(errors).toEqual([]);
});

test('locking storage clears a late-created bearer and prevents it reappearing after unlock', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticInvitations(page);
  let release: () => void = () => {};
  state.create = () =>
    new Promise<InvitationShare>((resolve) => {
      release = () => resolve({ link: privateLink, expiresAt: state.expiry() });
    });
  await page.goto('/');
  await allowService(await openDialog(page));
  await page.getByRole('button', { name: 'Create private invitation' }).click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'CreateInvitation'))
    .toBe(true);
  await page.keyboard.press('Control+,');
  const settings = page.getByRole('dialog', { name: 'Settings', exact: true });
  await settings.getByRole('button', { name: 'Lock storage' }).click();
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  release();
  await settings.getByRole('button', { name: 'Unlock storage' }).click();
  await expect(
    page.getByRole('dialog', { name: 'Share your invitation' }),
  ).toHaveCount(0);
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  await settings.getByRole('button', { name: 'Done', exact: true }).click();
  const again = await openDialog(page);
  await expect(again.getByLabel('Connection service HTTPS URL')).toHaveValue(
    '',
  );
  await expect(again.getByRole('checkbox')).not.toBeChecked();
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('Cancel clears a creation response that arrives late', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticInvitations(page);
  let release: () => void = () => {};
  state.create = () =>
    new Promise<InvitationShare>((resolve) => {
      release = () => resolve({ link: privateLink, expiresAt: state.expiry() });
    });
  await page.goto('/');
  const dialog = await openDialog(page);
  await allowService(dialog);
  await dialog
    .getByRole('button', { name: 'Create private invitation' })
    .click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'CreateInvitation'))
    .toBe(true);
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).toHaveCount(0);
  release();
  await page.waitForTimeout(100);
  await expect(
    page.getByRole('dialog', { name: 'Share your invitation' }),
  ).toHaveCount(0);
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  expect(
    calls.filter((call) => call.operation === 'CancelInvitation'),
  ).toHaveLength(1);
  expect(errors).toEqual([]);
});

test('share expiry clears bearer text and cancels the invitation', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticInvitations(page);
  state.expiry = () => new Date(Date.now() + 350).toISOString();
  await page.goto('/');
  await allowService(await openDialog(page));
  await page.getByRole('button', { name: 'Create private invitation' }).click();
  await expect
    .poll(
      () =>
        calls.filter((call) => call.operation === 'CancelInvitation').length,
    )
    .toBe(1);
  await expect(
    page.getByRole('dialog', { name: 'Share your invitation' }),
  ).toHaveCount(0);
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('a rejected cancellation clears the bearer and keeps a finite retry error visible', async ({
  page,
}) => {
  const { state, calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  await allowService(await openDialog(page));
  await page.getByRole('button', { name: 'Create private invitation' }).click();
  await expect(page.getByLabel('Private invitation link')).toHaveValue(
    privateLink,
  );
  state.cancelFailure = true;
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  const dialog = page.locator('.invitation-dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('alert')).toHaveText(
    'Cancellation could not be confirmed. Try Cancel again or close WhatsUp.',
  );
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  await expect(
    page.getByText('SYNTHETIC-PRIVATE-PERSISTENCE-DETAIL'),
  ).toHaveCount(0);
  state.cancelFailure = false;
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(
    calls.filter((call) => call.operation === 'CancelInvitation'),
  ).toHaveLength(2);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

for (const terminal of ['failed', 'expired'] as const) {
  test(`a joining attempt leaves its waiting screen when polling reports ${terminal}`, async ({
    page,
  }) => {
    const { state, errors } = await syntheticInvitations(page);
    await page.goto('/');
    const dialog = await openDialog(page);
    await dialog.getByRole('radio', { name: 'Paste invitation' }).check();
    await dialog
      .getByLabel('Private invitation', { exact: true })
      .fill(privateLink);
    await allowService(dialog);
    await dialog
      .getByRole('button', { name: 'Open private invitation' })
      .click();
    await expect(
      dialog.getByText('Opening the private invitation.', { exact: false }),
    ).toBeVisible();
    await expect.poll(() => state.pairing.state).toBe('connecting');
    state.pairing = { ...unavailablePairing, state: terminal };
    await expect(dialog.getByRole('alert')).toContainText(
      terminal === 'expired'
        ? 'This invitation expired.'
        : 'The invitation connection failed.',
    );
    await expect(
      dialog.getByText('Opening the private invitation.', { exact: false }),
    ).toHaveCount(0);
    await expect(
      page.getByLabel('Private invitation', { exact: true }),
    ).toHaveCount(0);
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await expect(
      (await openDialog(page)).getByLabel('Connection service HTTPS URL'),
    ).toHaveValue('');
    expect(errors).toEqual([]);
  });
}

test('an asynchronously failed share clears its bearer immediately', async ({
  page,
}) => {
  const { state, errors } = await syntheticInvitations(page);
  await page.goto('/');
  await allowService(await openDialog(page));
  await page.getByRole('button', { name: 'Create private invitation' }).click();
  await expect(page.getByLabel('Private invitation link')).toHaveValue(
    privateLink,
  );
  state.pairing = { ...unavailablePairing, state: 'failed' };
  const dialog = page.locator('.invitation-dialog');
  await expect(dialog.getByRole('alert')).toContainText(
    'The invitation connection failed.',
  );
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  await expect(
    dialog.getByRole('button', { name: 'Copy invitation' }),
  ).toHaveCount(0);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});
