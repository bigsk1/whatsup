import {
  serveDefaultNotificationActivation,
  serveDefaultAvatars,
} from './contact-events';
import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import type { ConversationMessage } from '../src/lib/messaging';
import { unavailablePairing } from '../src/lib/pairing';
import {
  defaultAppPreferences,
  type AppPreferences,
} from '../src/lib/state/preferences';
import {
  contactHint,
  nativeHint,
  serveDefaultRelayChange,
} from './contact-events';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const names = [
  'GetStatus',
  'LockStorage',
  'UnlockStorage',
  'GetProfile',
  'GetPairingStatus',
  'GetContacts',
  'GetMessages',
  'GetAppPreferences',
  'SetAppPreferences',
  'GetStorageSecurity',
  'MarkRead',
] as const;
type Operation = (typeof names)[number];
const operations = new Map<number, Operation>();
for (const name of names) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing ${name} binding`);
  operations.set(Number(match[1]), name);
}
async function fixture(page: Page) {
  const state = {
    storage: 'unlocked',
    preferences: { ...defaultAppPreferences } as AppPreferences,
    contact: {
      contactID,
      revision: 1,
      state: 'connected',
      peerFingerprint: 'b'.repeat(64),
      peerDisplayName: '',
      localName: '',
      canConnect: false,
      canSend: true,
      unread: 0,
      paused: false,
      path: 'Direct',
      peerProtocol: 0,
      attachmentsSupported: false,
      contactControlsSupported: false,
    },
    failSave: false,
    messages: [] as ConversationMessage[],
    holdStatus: undefined as undefined | (() => Promise<void>),
  };
  const calls: { operation: Operation; args: unknown[] }[] = [];
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
  });
  await page.route('**/wails/runtime', async (route) => {
    if (await serveDefaultAvatars(route)) return;
    if (await serveDefaultNotificationActivation(route)) return;
    if (await serveDefaultRelayChange(route)) return;
    const request = route.request().postDataJSON();
    const operation = operations.get(request.args?.methodID);
    if (!operation) throw new Error('Unexpected daily-use fixture operation');
    const args = request.args.args ?? [];
    calls.push({ operation, args });
    if (operation === 'GetStorageSecurity') {
      await route.fulfill({
        json: {
          requiresPassphrase: false,
          recoveryAvailable: true,
          busy: false,
        },
      });
      return;
    }
    if (['GetStatus', 'LockStorage', 'UnlockStorage'].includes(operation)) {
      if (operation === 'GetStatus' && state.holdStatus) {
        const hold = state.holdStatus;
        state.holdStatus = undefined;
        await hold();
      }
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
    } else if (operation === 'MarkRead') {
      state.contact = {
        ...state.contact,
        unread: 0,
        revision: state.contact.revision + 1,
      };
      await route.fulfill({ json: state.contact });
    } else if (operation === 'GetAppPreferences')
      await route.fulfill({ json: state.preferences });
    else if (operation === 'SetAppPreferences') {
      if (state.failSave)
        await route.fulfill({
          status: 503,
          json: {
            kind: 'RuntimeError',
            message: 'SYNTHETIC-PRIVATE-SETTINGS-ERROR',
          },
        });
      else {
        state.preferences = args[0];
        await route.fulfill({ json: state.preferences });
      }
    } else if (operation === 'GetProfile')
      await route.fulfill({
        json: { displayName: '', shareDisplayName: false },
      });
    else if (operation === 'GetPairingStatus')
      await route.fulfill({ json: { ...unavailablePairing, state: 'idle' } });
    else if (operation === 'GetContacts')
      await route.fulfill({ json: [state.contact] });
    else if (operation === 'GetMessages')
      await route.fulfill({
        json: {
          contactID,
          revision: state.contact.revision,
          messages: state.messages,
          nextCursor: '',
          hasMore: false,
        },
      });
  });
  return { state, calls, errors };
}

for (const viewport of [
  { width: 1166, height: 1032 },
  { width: 1105, height: 654 },
  { width: 520, height: 500 },
]) {
  test(`App behavior has separate accessible preference rows at ${viewport.width}×${viewport.height}`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize(viewport);
    const { state, calls, errors } = await fixture(page);
    await page.goto('/');
    await page
      .getByRole('button', { name: 'Open settings', exact: true })
      .click();
    const settings = page.getByRole('region', {
      name: 'Settings',
      exact: true,
    });
    await settings
      .getByRole('button', { name: 'App behavior', exact: true })
      .click();
    const card = settings.getByRole('region', {
      name: 'App behavior',
      exact: true,
    });
    const rows = card.locator('label');
    await expect(rows).toHaveCount(8);
    // Voice calls are off until switched on, and the ring and video options
    // wait for them.
    await expect(
      card.getByRole('checkbox', { name: 'Allow video calls', exact: true }),
    ).not.toBeChecked();
    await expect(
      card.getByRole('checkbox', { name: 'Allow video calls', exact: true }),
    ).toBeDisabled();
    await expect(
      card.getByRole('checkbox', { name: 'Allow voice calls', exact: true }),
    ).not.toBeChecked();
    await expect(
      card.getByRole('checkbox', { name: 'Allow voice calls', exact: true }),
    ).toBeEnabled();
    await expect(
      card.getByRole('checkbox', {
        name: 'Silence the ring sound',
        exact: true,
      }),
    ).toBeDisabled();
    await expect(
      card.getByRole('checkbox', {
        name: 'Show new-message notifications',
        exact: true,
      }),
    ).toBeChecked();
    await expect(
      card.getByRole('checkbox', {
        name: 'Show new-message notifications',
        exact: true,
      }),
    ).toBeEnabled();
    let previousBottom = -Infinity;
    let controlRight = 0;
    for (const row of await rows.all()) {
      await row.scrollIntoViewIfNeeded();
      const title = row.locator('strong');
      const help = row.locator('small');
      const control = row.getByRole('checkbox');
      await expect(title).toBeVisible();
      await expect(help).toBeVisible();
      // Capture one layout frame: async locator measurements can straddle
      // a Settings rerender/scroll and return a detached title's empty box.
      const { rowBox, titleBox, helpBox, controlBox, contentTop } =
        await row.evaluate((element) => {
          const box = (node: Element | null) => {
            if (!node || !node.isConnected)
              throw new Error('Preference row detached before measurement');
            const { x, y, width, height } = node.getBoundingClientRect();
            return { x, y, width, height };
          };
          const scroll = element.closest('.settings-scroll');
          if (!scroll) throw new Error('Missing settings scroll container');
          return {
            rowBox: box(element),
            titleBox: box(element.querySelector('strong')),
            helpBox: box(element.querySelector('small')),
            controlBox: box(element.querySelector('input')),
            contentTop: scroll.scrollTop,
          };
        });
      expect(rowBox.y + contentTop).toBeGreaterThanOrEqual(previousBottom - 1);
      previousBottom = rowBox.y + rowBox.height + contentTop;
      expect(helpBox.y).toBeGreaterThanOrEqual(
        titleBox.y + titleBox.height + 3,
      );
      expect(helpBox.y + helpBox.height).toBeLessThanOrEqual(
        rowBox.y + rowBox.height,
      );
      expect(controlBox.x).toBeGreaterThan(titleBox.x + titleBox.width + 8);
      expect(controlBox.x + controlBox.width).toBeLessThanOrEqual(
        rowBox.x + rowBox.width + 1,
      );
      expect(controlBox.y).toBeGreaterThanOrEqual(rowBox.y);
      expect(controlBox.y + controlBox.height).toBeLessThanOrEqual(
        rowBox.y + rowBox.height,
      );
      if (controlRight)
        expect(controlBox.x + controlBox.width).toBeCloseTo(controlRight, 1);
      controlRight = controlBox.x + controlBox.width;
      await expect(control).toHaveAccessibleName((await title.textContent())!);
      await expect(control).toHaveAccessibleDescription(
        (await help.textContent())!,
      );
      await expect(title).toBeInViewport({ ratio: 1 });
      await expect(help).toBeInViewport({ ratio: 1 });
      await expect(control).toBeInViewport({ ratio: 1 });
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    expect(calls.some((call) => call.operation === 'SetAppPreferences')).toBe(
      false,
    );
    await page.screenshot({ path: testInfo.outputPath('app-behavior.png') });
    const notifications = card.getByRole('checkbox', {
      name: 'Show new-message notifications',
      exact: true,
    });
    await notifications.focus();
    await page.keyboard.press('Space');
    await expect.poll(() => state.preferences.notifications).toBe(false);
    await expect(
      card.getByRole('checkbox', {
        name: 'Include contact names in notifications',
        exact: true,
      }),
    ).toBeDisabled();
    await expect(
      card.getByRole('checkbox', {
        name: 'Include message previews in notifications',
        exact: true,
      }),
    ).toBeDisabled();
    await expect(notifications).toBeEnabled();
    await notifications.focus();
    await page.keyboard.press('Space');
    await expect.poll(() => state.preferences.notifications).toBe(true);
    expect(state.preferences.notificationName).toBe(false);
    expect(state.preferences.notificationPreview).toBe(false);
    expect(errors).toEqual([]);
  });
}

test('daily-use defaults keep notifications generic and login opt-in; explicit changes stay in Go preferences', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  const settings = page.getByRole('region', { name: 'Settings', exact: true });
  await settings
    .getByRole('button', { name: 'App behavior', exact: true })
    .click();
  await expect(
    settings.getByLabel('Keep running when the window closes', {
      exact: false,
    }),
  ).toBeChecked();
  await expect(
    settings.getByLabel('Start WhatsUp when I sign in', { exact: false }),
  ).not.toBeChecked();
  await expect(
    settings.getByLabel('Show new-message notifications', { exact: false }),
  ).toBeChecked();
  const name = settings.getByLabel('Include contact names in notifications', {
    exact: false,
  });
  const preview = settings.getByLabel(
    'Include message previews in notifications',
    { exact: false },
  );
  await expect(name).not.toBeChecked();
  await expect(preview).not.toBeChecked();
  await name.check();
  await expect.poll(() => state.preferences.notificationName).toBe(true);
  await expect(preview).not.toBeChecked();
  expect(
    calls.find((call) => call.operation === 'SetAppPreferences')?.args,
  ).toEqual([{ ...defaultAppPreferences, notificationName: true }]);
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  await settings
    .getByLabel('Show new-message notifications', { exact: false })
    .uncheck();
  await expect(preview).toBeDisabled();
  expect(errors).toEqual([]);
});

test('preference failures show a fixed error and require reload before more changes', async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  state.failSave = true;
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  const settings = page.getByRole('region', { name: 'Settings', exact: true });
  await settings
    .getByRole('button', { name: 'App behavior', exact: true })
    .click();
  // A rejected save restores the unchecked value immediately. Click triggers
  // that attempt without check() waiting for a checked postcondition.
  await settings
    .getByLabel('Start WhatsUp when I sign in', { exact: false })
    .click();
  await expect(settings.getByRole('alert')).toContainText(
    'App preferences could not be saved.',
  );
  await expect(settings).not.toContainText('SYNTHETIC-PRIVATE-SETTINGS-ERROR');
  await expect(
    settings.getByLabel('Start WhatsUp when I sign in', { exact: false }),
  ).toBeDisabled();
  state.failSave = false;
  await settings
    .getByRole('button', { name: 'Reload app preferences', exact: true })
    .click();
  await expect(
    settings.getByLabel('Start WhatsUp when I sign in', { exact: false }),
  ).toBeEnabled();
  await expect(
    settings.getByLabel('Start WhatsUp when I sign in', { exact: false }),
  ).not.toBeChecked();
  expect(errors).toEqual([]);
});

test('live path display follows authoritative hints, resets when stale or offline, and clears on Go offline', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  await page.goto('/');
  const indicator = page.getByLabel('Conversation connection path', {
    exact: true,
  });
  await expect(indicator).toHaveText('· Direct');
  state.contact = { ...state.contact, revision: 2, path: 'Relayed' };
  await contactHint(page, contactID, 2);
  await expect(indicator).toHaveText('· Relayed');
  state.contact = { ...state.contact, revision: 3, path: 'Unknown' };
  await contactHint(page, contactID, 3);
  await expect(indicator).toHaveText('· Unknown');
  state.contact = {
    ...state.contact,
    revision: 4,
    state: 'offline',
    path: 'Direct',
  };
  await contactHint(page, contactID, 4);
  await expect(indicator).toHaveText('· Unknown');
  await page.getByRole('button', { name: 'Go offline', exact: true }).click();
  await expect(indicator).toHaveCount(0);
  await expect
    .poll(() => calls.filter((call) => call.operation === 'LockStorage').length)
    .toBe(1);
  expect(
    calls.filter((call) => call.operation === 'UnlockStorage'),
  ).toHaveLength(0);
  expect(errors).toEqual([]);
});

test('native tray offline immediately clears decrypted messages and drafts before the status RPC returns', async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  state.messages = [
    {
      id: 'AgICAgICAgICAgICAgICAg',
      direction: 'outgoing',
      text: 'Synthetic native visibility text',
      createdAt: '2026-11-14T12:00:00Z',
      delivered: true,
    },
  ];
  await page.goto('/');
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic native visibility text',
  );
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Synthetic tray draft');
  let release!: () => void;
  state.holdStatus = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  state.storage = 'locked';
  await nativeHint(page, 'whatsup:storage-changed', { state: 'locked' });
  await expect(
    page.getByText('Synthetic native visibility text', { exact: true }),
  ).toHaveCount(0);
  await expect(page.locator('.approved-contact')).toHaveCount(0);
  await expect(
    page.getByRole('textbox', {
      name: 'Message unavailable without an approved contact',
    }),
  ).toHaveValue('');
  await expect.poll(() => typeof release).toBe('function');
  release();
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test('native hidden windows do not mark new incoming messages read until shown', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  await page.goto('/');
  await expect(page.getByLabel('Conversation connection path')).toHaveText(
    '· Direct',
  );
  await nativeHint(page, 'whatsup:window-visible', false);
  state.messages = [
    {
      id: 'AgICAgICAgICAgICAgICAg',
      direction: 'incoming',
      text: 'Synthetic message received while hidden',
      createdAt: '2026-11-14T12:00:00Z',
      delivered: true,
    },
  ];
  state.contact = { ...state.contact, revision: 2, unread: 1 };
  await contactHint(page, contactID, 2);
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic message received while hidden',
  );
  expect(calls.filter((call) => call.operation === 'MarkRead')).toHaveLength(0);
  await nativeHint(page, 'whatsup:window-visible', true);
  await expect
    .poll(() => calls.filter((call) => call.operation === 'MarkRead').length)
    .toBe(1);
  expect(errors).toEqual([]);
});

test('a visible but unfocused native window leaves incoming messages unread until actual focus returns', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  await page.goto('/');
  await expect(page.getByLabel('Conversation connection path')).toHaveText(
    '· Direct',
  );
  await expect(page.locator('.app-shell')).toBeVisible();
  expect(await page.evaluate(() => document.hidden)).toBe(false);
  // Native window-visible carries foreground read eligibility. The OS window
  // may remain physically visible behind another app while this hint is false.
  await nativeHint(page, 'whatsup:window-visible', false);
  state.messages = [
    {
      id: 'AgICAgICAgICAgICAgICAg',
      direction: 'incoming',
      text: 'Synthetic background-window message',
      createdAt: '2026-11-14T12:00:00Z',
      delivered: true,
    },
  ];
  state.contact = { ...state.contact, revision: 2, unread: 1 };
  await contactHint(page, contactID, 2);
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic background-window message',
  );
  await expect(page.locator('.app-shell')).toBeVisible();
  expect(calls.filter((call) => call.operation === 'MarkRead')).toHaveLength(0);
  await nativeHint(page, 'whatsup:window-visible', true);
  await expect
    .poll(() => calls.filter((call) => call.operation === 'MarkRead').length)
    .toBe(1);
  expect(errors).toEqual([]);
});
