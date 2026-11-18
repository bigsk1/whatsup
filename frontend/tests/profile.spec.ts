import { serveDefaultAppPreferences } from './contact-events';
import {
  contactSummary,
  messagePage,
  eventBackedState,
} from './contact-events';
import { readFileSync } from 'node:fs';
import { expect, test, type Page, type Locator } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import {
  unavailableConversation,
  type ConversationSnapshot,
} from '../src/lib/messaging';
import type { LocalProfile } from '../src/lib/profile';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const replacementID = 'AwMDAwMDAwMDAwMDAwMDAw';
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const names = [
  'GetStatus',
  'UnlockStorage',
  'LockStorage',
  'GetPairingStatus',
  'GetContacts',
  'GetMessages',
  'MarkRead',
  'GetProfile',
  'SetProfile',
  'SetContactName',
  'ForgetContact',
] as const;
type Operation = (typeof names)[number];
const operations: Record<number, Operation> = {};
for (const name of names) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing generated ${name} binding`);
  operations[Number(match[1])] = name;
}
function snapshot(
  overrides: Partial<ConversationSnapshot> = {},
): ConversationSnapshot {
  return {
    ...unavailableConversation,
    state: 'offline',
    contactID,
    peerFingerprint: 'b'.repeat(64),
    peerDisplayName: 'Synthetic peer',
    localName: '',
    canConnect: true,
    canSend: true,
    revision: 1,
    ...overrides,
  };
}
async function fixture(page: Page) {
  const state = eventBackedState(page, {
    storage: 'unlocked',
    profile: {
      displayName: 'Synthetic owner',
      shareDisplayName: false,
    } as LocalProfile,
    conversation: snapshot(),
    getProfile: undefined as undefined | (() => Promise<void>),
    setProfile: undefined as undefined | (() => Promise<void>),
    setName: undefined as undefined | (() => Promise<void>),
    profileFailure: false,
    nameFailure: false,
  });
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
    if (body.object !== 0 || body.method !== 0 || !operation)
      throw new Error('Unexpected profile fixture operation');
    const args = body.args?.args ?? [];
    calls.push({ operation, args });
    if (['GetStatus', 'UnlockStorage', 'LockStorage'].includes(operation)) {
      if (operation === 'LockStorage') state.storage = 'locked';
      if (operation === 'UnlockStorage') state.storage = 'unlocked';
      await route.fulfill({
        json: {
          ...unavailableStatus,
          storageState: state.storage,
          identityFingerprint:
            state.storage === 'unlocked' ? 'a'.repeat(64) : '',
          pairingAvailable: state.storage === 'unlocked',
        },
      });
    } else if (operation === 'GetPairingStatus')
      await route.fulfill({ json: { ...unavailablePairing, state: 'idle' } });
    else if (operation === 'GetContacts')
      await route.fulfill({
        json: state.conversation.contactID
          ? [contactSummary(state.conversation)]
          : [],
      });
    else if (operation === 'GetMessages')
      await route.fulfill({ json: messagePage(state.conversation) });
    else if (operation === 'MarkRead')
      await route.fulfill({ json: contactSummary(state.conversation) });
    else if (operation === 'GetProfile') {
      const result = { ...state.profile };
      const hold = state.getProfile;
      state.getProfile = undefined;
      if (hold) await hold();
      await route.fulfill({ json: result });
    } else if (operation === 'SetProfile') {
      const result = {
        displayName: args[0] as string,
        shareDisplayName: args[1] as boolean,
      };
      if (state.setProfile) await state.setProfile();
      if (state.profileFailure)
        await route.fulfill({
          status: 503,
          json: {
            kind: 'RuntimeError',
            message: 'SYNTHETIC-PRIVATE-PROFILE-ERROR',
          },
        });
      else {
        state.profile = result;
        await route.fulfill({ json: result });
      }
    } else if (operation === 'SetContactName') {
      const result = {
        ...state.conversation,
        localName: args[1] as string,
        revision: state.conversation.revision + 1,
      };
      if (state.setName) await state.setName();
      if (state.nameFailure)
        await route.fulfill({
          status: 503,
          json: {
            kind: 'RuntimeError',
            message: 'SYNTHETIC-PRIVATE-NAME-ERROR',
          },
        });
      else {
        state.conversation = result;
        await route.fulfill({ json: result });
      }
    } else if (operation === 'ForgetContact') {
      state.conversation = {
        ...unavailableConversation,
        state: 'none',
        revision: 1,
      };
      await route.fulfill({ json: state.conversation });
    }
  });
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'linux', Arch: 'amd64', Debug: false };
  });
  return { state, calls, errors, external };
}
async function openSettings(page: Page, selected = 'Profile') {
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  const workspace = page.getByRole('region', { name: 'Settings', exact: true });
  if (selected !== 'Profile')
    await workspace
      .getByRole('button', { name: selected, exact: true })
      .click();
  return workspace;
}

async function expectSelected(workspace: Locator, name: string) {
  const navigation = workspace.getByRole('navigation', {
    name: 'Settings pages',
    exact: true,
  });
  await expect(navigation.locator('[aria-current="page"]')).toHaveCount(1);
  await expect(navigation.locator('button.selected')).toHaveCount(1);
  await expect(
    navigation.getByRole('button', { name, exact: true }),
  ).toHaveAttribute('aria-current', 'page');
  await expect(
    navigation.getByRole('button', { name, exact: true }),
  ).toHaveClass(/selected/);
}

test('Contact settings shows separate quota meters from the scoped Go query and refreshes without changing names', async ({
  page,
}) => {
  const { errors, calls } = await fixture(page);
  const storageID = Number(
    generated.match(
      /export function GetContactStorage\([\s\S]*?\$Call\.ByID\((\d+)/,
    )?.[1],
  );
  const requests: unknown[][] = [];
  let attachmentsUsedBytes = 2 ** 30;
  await page.route('**/wails/runtime', async (route) => {
    const body = route.request().postDataJSON();
    if (body.args?.methodID !== storageID) {
      await route.fallback();
      return;
    }
    requests.push(body.args.args);
    await route.fulfill({
      json: {
        contactID: body.args.args[0],
        messagesUsedBytes: 2 ** 20,
        messagesQuotaBytes: 2 ** 30,
        attachmentsUsedBytes,
        attachmentsQuotaBytes: 4 * 2 ** 30,
      },
    });
  });
  await page.goto('/');
  const workspace = await openSettings(page, 'Contact');
  const card = workspace.getByRole('region', {
    name: 'Contact storage',
    exact: true,
  });
  await expect(card).toContainText('1.0 MiB of 1.00 GiB');
  await expect(card).toContainText('1.00 GiB of 4.00 GiB');
  await expect(
    card.getByRole('progressbar', { name: 'Message text storage' }),
  ).toHaveAttribute('value', String(2 ** 20));
  await expect(card).toContainText(
    'space reserved for queued or accepted files',
  );
  attachmentsUsedBytes = 4 * 2 ** 30;
  await card
    .getByRole('button', { name: 'Refresh storage usage', exact: true })
    .click();
  await expect(card).toContainText('Attachment storage full');
  expect(requests.length).toBeGreaterThanOrEqual(2);
  expect(
    requests.every((args) => args.length === 1 && args[0] === contactID),
  ).toBe(true);
  expect(calls.some((call) => call.operation === 'SetContactName')).toBe(false);
  await workspace
    .getByRole('button', { name: 'Back to chats', exact: true })
    .click();
  await page.getByRole('button', { name: 'Go offline', exact: true }).click();
  await expect(card).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('profile is restored locally, sharing stays off until saved, and names never enter browser storage', async ({
  page,
}) => {
  const { calls, errors, external } = await fixture(page);
  await page.goto('/');
  await expect(page.locator('.device-footer')).toContainText('Synthetic owner');
  const workspace = await openSettings(page);
  await expect(
    workspace.getByLabel('Display name', { exact: true }),
  ).toHaveValue('Synthetic owner');
  await expect(
    workspace.getByRole('checkbox', { name: 'Share my display name' }),
  ).not.toBeChecked();
  await workspace
    .getByLabel('Display name', { exact: true })
    .fill('New synthetic owner');
  expect(calls.filter((call) => call.operation === 'SetProfile')).toEqual([]);
  await workspace
    .getByRole('button', { name: 'Save profile', exact: true })
    .click();
  await expect(
    workspace.getByText('Profile saved on this device.', { exact: true }),
  ).toBeVisible();
  expect(
    calls.filter((call) => call.operation === 'SetProfile').at(-1)?.args,
  ).toEqual(['New synthetic owner', false]);
  await workspace
    .getByRole('checkbox', { name: 'Share my display name' })
    .check();
  await workspace
    .getByRole('button', { name: 'Save profile', exact: true })
    .click();
  await expect
    .poll(
      () =>
        calls.filter((call) => call.operation === 'SetProfile').at(-1)?.args,
    )
    .toEqual(['New synthetic owner', true]);
  await workspace
    .getByRole('button', { name: 'Back to chats', exact: true })
    .click();
  await expect(page.locator('.device-footer')).toContainText(
    'New synthetic owner',
  );
  await page.reload();
  await expect(page.locator('.device-footer')).toContainText(
    'New synthetic owner',
  );
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('private contact names override shared names, clear back to the shared name and render as inert text', async ({
  page,
}) => {
  const { calls, errors } = await fixture(page);
  await page.goto('/');
  await expect(page.locator('.conversation-header')).toContainText(
    'Synthetic peer',
  );
  const workspace = await openSettings(page, 'Contact');
  const nickname = '<b>Private laptop</b>';
  await workspace.getByLabel('Name on this device').fill(nickname);
  await workspace
    .getByRole('button', { name: 'Save contact name', exact: true })
    .click();
  await expect(
    workspace.getByRole('heading', { name: nickname, exact: true }),
  ).toBeVisible();
  expect(
    calls.filter((call) => call.operation === 'SetContactName').at(-1)?.args,
  ).toEqual([contactID, nickname]);
  expect(calls.filter((call) => call.operation === 'SetProfile')).toEqual([]);
  await workspace
    .getByRole('button', { name: 'Back to chats', exact: true })
    .click();
  await expect(page.locator('.conversation-header')).toContainText(nickname);
  await expect(page.locator('b')).toHaveCount(0);
  await page
    .getByRole('searchbox', { name: 'Filter contacts' })
    .fill('private laptop');
  await expect(page.locator('.approved-contact')).toBeVisible();
  await openSettings(page, 'Contact');
  await workspace.getByLabel('Name on this device').fill('');
  await workspace
    .getByRole('button', { name: 'Save contact name', exact: true })
    .click();
  await expect(
    workspace.getByRole('heading', { name: 'Synthetic peer', exact: true }),
  ).toBeVisible();
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('lock clears profile drafts and ignores a profile save completing afterward', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  let release: () => void = () => {};
  state.setProfile = () =>
    new Promise<void>((resolve) => {
      release = resolve;
    });
  await page.goto('/');
  const workspace = await openSettings(page);
  await workspace
    .getByLabel('Display name', { exact: true })
    .fill('Late synthetic profile');
  await workspace
    .getByRole('button', { name: 'Save profile', exact: true })
    .click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'SetProfile'))
    .toBe(true);
  await workspace
    .getByRole('button', { name: 'Privacy & diagnostics', exact: true })
    .click();
  await workspace
    .getByRole('button', { name: 'Go offline', exact: true })
    .click();
  await expect(
    workspace.getByText('Locked on this device', { exact: true }),
  ).toBeVisible();
  release();
  await workspace.getByRole('button', { name: 'Profile', exact: true }).click();
  await expect(
    workspace.getByLabel('Display name', { exact: true }),
  ).toBeDisabled();
  await expect(
    workspace.getByLabel('Display name', { exact: true }),
  ).toHaveValue('');
  await page.waitForTimeout(100);
  await expect(
    workspace.getByLabel('Display name', { exact: true }),
  ).toHaveValue('');
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('a replacement contact invalidates a delayed name save and clears its unsaved draft', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  let release: () => void = () => {};
  state.setName = () =>
    new Promise<void>((resolve) => {
      release = resolve;
    });
  await page.goto('/');
  const workspace = await openSettings(page, 'Contact');
  await workspace.getByLabel('Name on this device').fill('Old contact draft');
  await workspace
    .getByRole('button', { name: 'Save contact name', exact: true })
    .click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'SetContactName'))
    .toBe(true);
  state.conversation = snapshot({
    contactID: replacementID,
    peerFingerprint: 'c'.repeat(64),
    peerDisplayName: 'Replacement peer',
    revision: 20,
  });
  await expect(
    workspace.getByRole('heading', { name: 'Replacement peer', exact: true }),
  ).toBeVisible();
  await expect(workspace.getByLabel('Name on this device')).toHaveValue('');
  const replacement = { ...state.conversation };
  release();
  // The synthetic server intentionally returns the old contact; keep current polls
  // on the replacement so this checks the frontend generation guard only.
  await page.waitForTimeout(100);
  state.conversation = replacement;
  await expect(
    workspace.getByRole('heading', { name: 'Replacement peer', exact: true }),
  ).toBeVisible();
  await expect(workspace.getByLabel('Name on this device')).toHaveValue('');
  expect(errors).toEqual([]);
});

test('profile failures stay finite and malformed names cannot be submitted', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  state.profileFailure = true;
  await page.goto('/');
  const workspace = await openSettings(page);
  await workspace
    .getByLabel('Display name', { exact: true })
    .fill('Bad\u202ename');
  await expect(
    workspace.getByRole('button', { name: 'Save profile', exact: true }),
  ).toBeDisabled();
  expect(calls.filter((call) => call.operation === 'SetProfile')).toEqual([]);
  await workspace
    .getByLabel('Display name', { exact: true })
    .fill('Valid synthetic name');
  await workspace
    .getByRole('button', { name: 'Save profile', exact: true })
    .click();
  await expect(
    workspace.getByText(
      'Your profile could not be saved. Unlock storage and try again.',
      { exact: true },
    ),
  ).toBeVisible();
  await expect(workspace).not.toContainText('SYNTHETIC-PRIVATE-PROFILE-ERROR');
  expect(errors).toEqual([]);
});

test('event recovery and a shared-name update preserve unfinished profile and contact drafts', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  await page.goto('/');
  const workspace = await openSettings(page);
  await workspace
    .getByLabel('Display name', { exact: true })
    .fill('Unfinished profile draft');
  const initialPolls = calls.filter(
    (call) => call.operation === 'GetContacts',
  ).length;
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect
    .poll(() => calls.filter((call) => call.operation === 'GetContacts').length)
    .toBeGreaterThan(initialPolls);
  await expect(
    workspace.getByLabel('Display name', { exact: true }),
  ).toHaveValue('Unfinished profile draft');
  await workspace.getByRole('button', { name: 'Contact', exact: true }).click();
  await workspace
    .getByLabel('Name on this device')
    .fill('Unfinished private draft');
  state.conversation = {
    ...state.conversation,
    peerDisplayName: 'Changed shared name',
    revision: 10,
  };
  await expect(
    workspace.getByText('Changed shared name', { exact: true }).first(),
  ).toBeVisible();
  await expect(workspace.getByLabel('Name on this device')).toHaveValue(
    'Unfinished private draft',
  );
  await expect
    .poll(() => calls.filter((call) => call.operation === 'GetContacts').length)
    .toBeGreaterThan(initialPolls + 1);
  await expect(workspace.getByLabel('Name on this device')).toHaveValue(
    'Unfinished private draft',
  );
  expect(
    calls.filter(
      (call) =>
        call.operation === 'SetProfile' || call.operation === 'SetContactName',
    ),
  ).toEqual([]);
  expect(errors).toEqual([]);
});

test('Settings pages stay usable across desktop, short-window and light-theme layouts', async ({
  page,
}) => {
  const { errors, external } = await fixture(page);
  await page.setViewportSize({ width: 1164, height: 848 });
  await page.goto('/');
  const workspace = await openSettings(page);
  await expect(
    workspace.getByLabel('Display name', { exact: true }),
  ).toHaveValue('Synthetic owner');
  await expectSelected(workspace, 'Profile');
  await page.screenshot({
    path: '../.local/settings-006-profile.png',
    animations: 'disabled',
  });
  await workspace.getByRole('button', { name: 'Contact', exact: true }).click();
  await expect(
    workspace.getByRole('heading', { name: 'Synthetic peer', exact: true }),
  ).toBeVisible();
  await expectSelected(workspace, 'Contact');
  await page.screenshot({
    path: '../.local/settings-006-contact.png',
    animations: 'disabled',
  });
  await page.setViewportSize({ width: 520, height: 500 });
  await workspace
    .getByRole('button', { name: 'Privacy & diagnostics', exact: true })
    .click();
  const runtime = workspace.getByRole('heading', {
    name: 'Desktop runtime',
    exact: true,
  });
  await runtime.scrollIntoViewIfNeeded();
  await expect(runtime).toBeInViewport();
  await expect(
    workspace.getByRole('button', { name: 'Back to chats', exact: true }),
  ).toBeInViewport();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await expectSelected(workspace, 'Privacy & diagnostics');
  await page.screenshot({
    path: '../.local/settings-006-small.png',
    animations: 'disabled',
  });
  await page.setViewportSize({ width: 1164, height: 848 });
  await workspace
    .getByRole('button', { name: 'Appearance', exact: true })
    .click();
  await workspace.getByRole('radio', { name: 'Daybreak', exact: true }).check();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expectSelected(workspace, 'Appearance');
  await page.screenshot({
    path: '../.local/settings-006-light.png',
    animations: 'disabled',
  });
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('maximum-length names fit the chat header and settings without hiding connection controls', async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  const name = '界'.repeat(64);
  state.profile.displayName = name;
  state.conversation = snapshot({ peerDisplayName: name, localName: name });
  await page.goto('/');
  for (const viewport of [
    { width: 1164, height: 848 },
    { width: 520, height: 500 },
  ]) {
    await page.setViewportSize(viewport);
    await expect(page.locator('.conversation-header h2')).toHaveText(name);
    for (const label of ['Connect', 'Forget contact…', 'Go offline'])
      await expect(
        page
          .locator('.conversation-header')
          .getByRole('button', { name: label, exact: true }),
      ).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const workspace = await openSettings(page, 'Contact');
    await expect(
      workspace.getByRole('heading', { name, exact: true }),
    ).toBeVisible();
    await expect(workspace.getByLabel('Name on this device')).toHaveValue(name);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await workspace
      .getByRole('button', { name: 'Back to chats', exact: true })
      .click();
  }
  expect(errors).toEqual([]);
});

test('Settings shortcut dismisses About but does not bypass or cancel a Forget confirmation', async ({
  page,
}) => {
  const { calls, errors } = await fixture(page);
  await page.goto('/');
  await page
    .getByRole('button', { name: 'About this build', exact: true })
    .click();
  const about = page.getByRole('dialog', {
    name: 'About WhatsUp',
    exact: true,
  });
  await expect(about).toBeVisible();
  await page.keyboard.press('Control+,');
  const workspace = page.getByRole('region', { name: 'Settings', exact: true });
  await expect(workspace).toBeVisible();
  await expect(about).not.toBeVisible();
  await workspace.getByRole('button', { name: 'Contact', exact: true }).click();
  await workspace
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  const confirmation = page.getByRole('dialog', {
    name: 'Forget contact and delete conversation',
    exact: true,
  });
  await expect(confirmation).toBeVisible();
  await page.keyboard.press('Control+,');
  await expect(workspace).not.toBeVisible();
  await expect(confirmation).toBeVisible();
  expect(calls.filter((call) => call.operation === 'ForgetContact')).toEqual(
    [],
  );
  await confirmation
    .getByRole('button', { name: 'Cancel', exact: true })
    .click();
  expect(errors).toEqual([]);
});

test('locking clears a private contact-name search with the decrypted conversation', async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  state.conversation = snapshot({ localName: 'Synthetic private nickname' });
  await page.goto('/');
  const search = page.getByRole('searchbox', { name: 'Filter contacts' });
  await search.fill('Synthetic private nickname');
  await page
    .locator('.conversation-header')
    .getByRole('button', { name: 'Go offline', exact: true })
    .click();
  await expect(search).toHaveValue('');
  await expect(
    page.getByText('Synthetic private nickname', { exact: true }),
  ).toHaveCount(0);
  expect(errors).toEqual([]);
});
