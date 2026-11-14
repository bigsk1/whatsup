import { serveDefaultAppPreferences } from './contact-events';
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
  | 'GetContacts'
  | 'GetMessages'
  | 'MarkRead'
  | 'GetProfile'
  | 'ApprovePairing'
  | 'DenyPairing'
  | 'CreateInvitation'
  | 'JoinInvitation'
  | 'CancelInvitation'
  | 'InspectInvitation';

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
  'CreateInvitation',
  'JoinInvitation',
  'CancelInvitation',
  'InspectInvitation',
] as const) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing generated ${name} binding`);
  operations[Number(match[1])] = name;
}

const relayURL = 'https://relay.example.invalid/map.json';
const publicRelayMap = 'https://tailcat.dev/derpmap.json';
// Deliberately synthetic bearer text. This exercises UI transport only and is
// not a valid Go invitation or evidence of an actual public service connection.
const privateLink =
  'whatsup://pair/v2?id=synthetic&secret=SYNTHETIC-INVITATION-BEARER&owner=synthetic&endpoint=synthetic&expires=9999999999';
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
    inspect: undefined as
      undefined | (() => Promise<{ serviceURL: string; host: string }>),
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
    } else if (operation === 'GetProfile') {
      await route.fulfill({
        json: { displayName: '', shareDisplayName: false },
      });
      return;
    } else if (operation === 'GetContacts') {
      await route.fulfill({
        json: [],
      });
    } else if (operation === 'GetPairingStatus') {
      await route.fulfill({ json: state.pairing });
    } else if (operation === 'InspectInvitation') {
      await route.fulfill({
        json: state.inspect
          ? await state.inspect()
          : { serviceURL: relayURL, host: new URL(relayURL).host },
      });
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
  const serviceField = dialog.getByLabel('Connection service HTTPS URL');
  if (await serviceField.count()) await serviceField.fill(relayURL);
  else
    await dialog
      .getByRole('button', { name: 'Review invitation', exact: true })
      .click();
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

test('the public Tailcat relay URL is copied only explicitly without selecting it or connecting', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  await expect(dialog.getByText(publicRelayMap, { exact: true })).toBeVisible();
  await expect(dialog).toContainText('Tailcat public relay map · best effort');
  await expect(dialog).toContainText('Its operator sees your IP address');
  expect(
    await page.evaluate(
      () => (window as Window & { testClipboard?: string[] }).testClipboard,
    ),
  ).toEqual([]);
  for (const mode of ['create'] as const) {
    await dialog
      .getByRole('button', { name: 'Copy relay URL', exact: true })
      .click();
    await expect(dialog.getByRole('status')).toContainText(
      'Public relay URL copied.',
    );
    await expect(dialog.getByLabel('Connection service HTTPS URL')).toHaveValue(
      '',
    );
    await expect(dialog.getByRole('checkbox')).not.toBeChecked();
    await expect(
      dialog.getByRole('button', {
        name: 'Create private invitation',
        exact: true,
      }),
    ).toBeDisabled();
  }
  expect(
    await page.evaluate(
      () => (window as Window & { testClipboard?: string[] }).testClipboard,
    ),
  ).toEqual([publicRelayMap]);
  expect(
    calls.filter((call) =>
      ['CreateInvitation', 'JoinInvitation'].includes(call.operation),
    ),
  ).toEqual([]);
  expect(external).toEqual([]);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('a failed public relay URL copy shows a fixed local notice and keeps consent unchanged', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  await page.evaluate(() => {
    navigator.clipboard.writeText = async () => {
      throw new Error('SYNTHETIC-PRIVATE-CLIPBOARD-DETAIL');
    };
  });
  await dialog
    .getByRole('button', { name: 'Copy relay URL', exact: true })
    .click();
  await expect(dialog.getByRole('status')).toHaveText(
    'Copy is unavailable. Select the public relay URL and copy it.',
  );
  await expect(dialog.getByLabel('Connection service HTTPS URL')).toHaveValue(
    '',
  );
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  await expect(
    dialog.getByText('SYNTHETIC-PRIVATE-CLIPBOARD-DETAIL'),
  ).toHaveCount(0);
  expect(
    calls.filter((call) =>
      ['CreateInvitation', 'JoinInvitation'].includes(call.operation),
    ),
  ).toEqual([]);
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
  const settings = page.getByRole('region', { name: 'Settings', exact: true });
  await settings
    .getByRole('button', { name: 'Privacy & diagnostics', exact: true })
    .click();
  await settings.getByRole('button', { name: 'Go offline' }).click();
  await expect(
    settings.getByText('Locked on this device', { exact: true }),
  ).toBeVisible();
  release();
  await settings.getByRole('button', { name: 'Go online' }).click();
  await expect(
    page.getByRole('dialog', { name: 'Share your invitation' }),
  ).toHaveCount(0);
  await expect(page.getByLabel('Private invitation link')).toHaveCount(0);
  await settings
    .getByRole('button', { name: 'Back to chats', exact: true })
    .click();
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

test('the relay preset fills only an empty creation field without consent or a connection', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  const preset = dialog.getByRole('button', {
    name: 'Use Tailcat public relay',
    exact: true,
  });
  const field = dialog.getByLabel('Connection service HTTPS URL');
  await preset.click();
  await expect(field).toHaveValue(publicRelayMap);
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  await expect(preset).toBeDisabled();
  await field.fill(relayURL);
  await expect(preset).toBeDisabled();
  await expect(field).toHaveValue(relayURL);
  expect(
    calls.some((call) =>
      ['CreateInvitation', 'JoinInvitation'].includes(call.operation),
    ),
  ).toBe(false);
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('the joining app reviews the carried service locally and requires fresh consent after a link edit', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  await dialog.getByRole('radio', { name: 'Paste invitation' }).check();
  const input = dialog.getByLabel('Private invitation', { exact: true });
  await input.fill(privateLink);
  await expect(dialog.getByRole('checkbox')).toBeDisabled();
  await dialog
    .getByRole('button', { name: 'Review invitation', exact: true })
    .click();
  await expect(
    dialog.getByText('This invitation uses relay.example.invalid. Allow?', {
      exact: true,
    }),
  ).toBeVisible();
  await expect(dialog.getByLabel('Invitation connection service')).toHaveText(
    relayURL,
  );
  await expect(dialog.getByLabel('Connection service HTTPS URL')).toHaveCount(
    0,
  );
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  expect(
    calls.filter((call) => call.operation === 'InspectInvitation'),
  ).toEqual([{ operation: 'InspectInvitation', args: [privateLink] }]);
  expect(calls.some((call) => call.operation === 'JoinInvitation')).toBe(false);
  await dialog.getByRole('checkbox').check();
  await input.fill(privateLink + '&changed=1');
  await expect(dialog.getByLabel('Invitation connection service')).toHaveCount(
    0,
  );
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  await expect(
    dialog.getByRole('button', { name: 'Open private invitation' }),
  ).toBeDisabled();
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('a late local inspection cannot restore a cancelled invitation or relay consent', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticInvitations(page);
  let release!: () => void;
  state.inspect = () =>
    new Promise((resolve) => {
      release = () =>
        resolve({ serviceURL: relayURL, host: new URL(relayURL).host });
    });
  await page.goto('/');
  const dialog = await openDialog(page);
  await dialog.getByRole('radio', { name: 'Paste invitation' }).check();
  await dialog
    .getByLabel('Private invitation', { exact: true })
    .fill(privateLink);
  await dialog
    .getByRole('button', { name: 'Review invitation', exact: true })
    .click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'InspectInvitation'))
    .toBe(true);
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  release();
  const reopened = await openDialog(page);
  await expect(reopened.getByLabel('Connection service HTTPS URL')).toHaveValue(
    '',
  );
  await expect(reopened.getByRole('checkbox')).not.toBeChecked();
  expect(calls.some((call) => call.operation === 'JoinInvitation')).toBe(false);
  expect(errors).toEqual([]);
});

test('the paste field preserves an 8192-byte invitation while the service field stays capped at 2048', async ({
  page,
}) => {
  const { calls, errors } = await syntheticInvitations(page);
  await page.goto('/');
  const dialog = await openDialog(page);
  await expect(
    dialog.getByLabel('Connection service HTTPS URL'),
  ).toHaveAttribute('maxlength', '2048');
  await expect(dialog.getByRole('checkbox')).toHaveAccessibleName(
    'I allow this connection service to learn my IP address and availability and relay encrypted connections when needed.',
  );
  await dialog.getByRole('radio', { name: 'Paste invitation' }).check();
  const prefix =
    'whatsup://pair/v2?service=https%3A%2F%2Frelay.example.invalid%2Fmap.json&padding=';
  const invitation = prefix + 'x'.repeat(8192 - prefix.length);
  const field = dialog.getByLabel('Private invitation', { exact: true });
  await expect(field).toHaveAttribute('maxlength', '8192');
  await field.fill(invitation);
  await expect(field).toHaveValue(invitation);
  await dialog
    .getByRole('button', { name: 'Review invitation', exact: true })
    .click();
  await expect
    .poll(
      () =>
        calls.find((call) => call.operation === 'InspectInvitation')?.args[0],
    )
    .toBe(invitation);
  await expect(dialog.getByRole('checkbox')).not.toBeChecked();
  expect(calls.some((call) => call.operation === 'JoinInvitation')).toBe(false);
  expect(errors).toEqual([]);
});
