import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import { unavailableConversation } from '../src/lib/messaging';
import {
  contactSummary,
  messagePage,
  contactHint,
  serveDefaultAppPreferences,
} from './contact-events';
import type { AvatarSnapshot } from '../src/lib/state/avatars';

const id = 'AQEBAQEBAQEBAQEBAQEBAQ';
const own = '/local-media/' + 'a'.repeat(32),
  peer = '/local-media/' + 'b'.repeat(32),
  changed = '/local-media/' + 'c'.repeat(32);
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const operations = new Map<number, string>();
for (const match of generated.matchAll(
  /export function (\w+)\([\s\S]*?\$Call\.ByID\((\d+)/g,
))
  operations.set(Number(match[2]), match[1]);
const initial = (): AvatarSnapshot => ({
  revision: 1,
  ownURL: '',
  shareAvatar: false,
  showContactPhotos: true,
  contacts: [{ contactID: id, url: peer }],
});
const picture = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aT1kAAAAASUVORK5CYII=',
  'base64',
);

async function fixture(page: Page) {
  const state = {
    avatar: initial() as unknown,
    chosen: undefined as unknown,
    choiceFailure: false,
    replay: undefined as unknown,
  };
  const calls: { method: string; args: unknown[] }[] = [],
    errors: string[] = [],
    external: string[] = [];
  const conversation = {
    ...unavailableConversation,
    state: 'offline' as const,
    contactID: id,
    peerFingerprint: 'b'.repeat(64),
    peerDisplayName: 'Synthetic peer',
    canSend: true,
    canConnect: true,
    revision: 1,
  };
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:9245/'))
      external.push(request.url());
  });
  await page.route('**/local-media/*', (route) =>
    route.fulfill({
      body: picture,
      contentType: 'image/png',
      headers: { 'X-Content-Type-Options': 'nosniff' },
    }),
  );
  await page.route('**/wails/runtime', async (route) => {
    const body = route.request().postDataJSON(),
      method = operations.get(body.args?.methodID) ?? '',
      args = body.args?.args ?? [];
    if (
      [
        'GetAvatarState',
        'ChooseProfileAvatar',
        'RemoveProfileAvatar',
        'SetAvatarPreferences',
      ].includes(method)
    ) {
      calls.push({ method, args });
      if (method === 'GetAvatarState') {
        await route.fulfill({ json: state.replay ?? state.avatar });
        return;
      }
      if (method === 'ChooseProfileAvatar') {
        if (state.choiceFailure) {
          await route.fulfill({
            status: 503,
            json: {
              kind: 'RuntimeError',
              message: 'SYNTHETIC-PRIVATE-IMAGE-METADATA',
            },
          });
          return;
        }
        state.avatar = state.chosen ?? {
          ...(state.avatar as AvatarSnapshot),
          revision: 2,
          ownURL: own,
        };
      } else if (method === 'RemoveProfileAvatar')
        state.avatar = {
          ...(state.avatar as AvatarSnapshot),
          revision: (state.avatar as AvatarSnapshot).revision + 1,
          ownURL: '',
        };
      else
        state.avatar = {
          ...(state.avatar as AvatarSnapshot),
          revision: (state.avatar as AvatarSnapshot).revision + 1,
          shareAvatar: args[0],
          showContactPhotos: args[1],
          contacts: [{ contactID: id, url: args[1] ? peer : '' }],
        };
      await route.fulfill({ json: state.avatar });
      return;
    }
    if (await serveDefaultAppPreferences(route)) return;
    if (method === 'GetStatus') {
      await route.fulfill({
        json: {
          ...unavailableStatus,
          storageState: 'unlocked',
          identityFingerprint: 'a'.repeat(64),
          pairingAvailable: true,
        },
      });
      return;
    }
    if (method === 'GetPairingStatus') {
      await route.fulfill({ json: { ...unavailablePairing, state: 'idle' } });
      return;
    }
    if (method === 'GetProfile') {
      await route.fulfill({
        json: { displayName: 'Synthetic owner', shareDisplayName: false },
      });
      return;
    }
    if (method === 'GetContacts') {
      await route.fulfill({ json: [contactSummary(conversation)] });
      return;
    }
    if (method === 'GetMessages') {
      await route.fulfill({ json: messagePage(conversation) });
      return;
    }
    if (method === 'MarkRead') {
      await route.fulfill({ json: contactSummary(conversation) });
      return;
    }
    throw new Error('Unexpected avatar fixture operation: ' + method);
  });
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
  });
  return { state, calls, errors, external };
}
async function settings(page: Page) {
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  return page.getByRole('region', { name: 'Settings', exact: true });
}

test('profile pictures use the Go picker, independent sharing and local-only display preferences across all avatar positions', async ({
  page,
}) => {
  const { calls, errors, external } = await fixture(page);
  await page.goto('/');
  await expect(page.locator('.contact-list img.avatar-photo')).toHaveAttribute(
    'src',
    peer,
  );
  await expect(
    page.locator('.header-identity img.avatar-photo'),
  ).toHaveAttribute('src', peer);
  const workspace = await settings(page);
  const share = workspace.getByRole('checkbox', {
    name: 'Share my profile picture',
    exact: false,
  });
  await expect(share).not.toBeChecked();
  await workspace
    .getByRole('button', { name: 'Choose profile picture…', exact: true })
    .click();
  await expect(workspace.locator('img.avatar-photo')).toHaveAttribute(
    'src',
    own,
  );
  await expect(page.locator('.device-footer img.avatar-photo')).toHaveAttribute(
    'src',
    own,
  );
  await expect(share).not.toBeChecked();
  await share.check();
  await expect
    .poll(
      () => calls.find((call) => call.method === 'SetAvatarPreferences')?.args,
    )
    .toEqual([true, true]);
  await share.uncheck();
  await workspace
    .getByRole('checkbox', { name: 'Show contact photos', exact: false })
    .uncheck();
  await expect(page.locator('.contact-list img.avatar-photo')).toHaveCount(0);
  await expect(page.locator('.header-identity img.avatar-photo')).toHaveCount(
    0,
  );
  await expect(page.locator('.device-footer img.avatar-photo')).toHaveAttribute(
    'src',
    own,
  );
  await workspace.getByRole('button', { name: 'Contact', exact: true }).click();
  await expect(workspace.locator('img.avatar-photo')).toHaveCount(0);
  await workspace.getByRole('button', { name: 'Profile', exact: true }).click();
  await workspace
    .getByRole('button', { name: 'Remove profile picture', exact: true })
    .click();
  await expect(page.locator('.device-footer img.avatar-photo')).toHaveCount(0);
  expect(
    calls
      .filter(
        (call) =>
          call.method === 'ChooseProfileAvatar' ||
          call.method === 'RemoveProfileAvatar',
      )
      .every((call) => call.args.length === 0),
  ).toBe(true);
  expect(errors).toEqual([]);
  expect(external).toEqual([]);
});

test('malformed, metadata-bearing and oversized picture sources never render or reach the network', async ({
  page,
}) => {
  const { state, calls, errors, external } = await fixture(page);
  await page.goto('/');
  const workspace = await settings(page);
  await expect(
    workspace.getByRole('button', { name: 'Choose profile picture…' }),
  ).toBeEnabled();
  for (const ownURL of [
    'https://peer.example/photo.png',
    own + '?metadata=GPS',
    'data:image/png;base64,' + 'A'.repeat(300000),
    '/local-media/not-a-handle',
  ]) {
    state.chosen = { ...initial(), ownURL };
    const choices = calls.filter(
      (call) => call.method === 'ChooseProfileAvatar',
    ).length;
    await workspace
      .getByRole('button', { name: 'Choose profile picture…', exact: true })
      .click();
    await expect
      .poll(
        () =>
          calls.filter((call) => call.method === 'ChooseProfileAvatar').length,
      )
      .toBe(choices + 1);
    await expect(
      workspace.getByRole('button', {
        name: 'Choose profile picture…',
        exact: true,
      }),
    ).toBeEnabled();
    await expect(
      workspace.getByText('The profile picture could not be changed.', {
        exact: false,
      }),
    ).toBeVisible();
    await expect(page.locator('.device-footer img.avatar-photo')).toHaveCount(
      0,
    );
  }
  state.choiceFailure = true;
  const choices = calls.filter(
    (call) => call.method === 'ChooseProfileAvatar',
  ).length;
  await workspace
    .getByRole('button', { name: 'Choose profile picture…', exact: true })
    .click();
  await expect
    .poll(
      () =>
        calls.filter((call) => call.method === 'ChooseProfileAvatar').length,
    )
    .toBe(choices + 1);
  await expect(
    workspace.getByRole('button', {
      name: 'Choose profile picture…',
      exact: true,
    }),
  ).toBeEnabled();
  await expect(
    workspace.getByText('The profile picture could not be changed.', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(page.getByText('SYNTHETIC-PRIVATE-IMAGE-METADATA')).toHaveCount(
    0,
  );
  expect(errors).toEqual([]);
  expect(external).toEqual([]);
});

test('replayed snapshots cannot restore an old photo and authoritative contact clear returns to a letter', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  await page.goto('/');
  const workspace = await settings(page);
  state.chosen = { ...initial(), revision: 5, ownURL: changed };
  await workspace
    .getByRole('button', { name: 'Choose profile picture…', exact: true })
    .click();
  await expect(page.locator('.device-footer img.avatar-photo')).toHaveAttribute(
    'src',
    changed,
  );
  state.replay = { ...initial(), revision: 1, ownURL: own };
  await workspace
    .getByRole('button', { name: 'Refresh pictures', exact: true })
    .click();
  await expect(
    workspace.getByRole('button', { name: 'Refresh pictures', exact: true }),
  ).toBeEnabled();
  await expect(page.locator('.device-footer img.avatar-photo')).toHaveAttribute(
    'src',
    changed,
  );
  state.replay = undefined;
  state.avatar = {
    ...initial(),
    revision: 6,
    ownURL: changed,
    contacts: [{ contactID: id, url: '' }],
  };
  const before = calls.filter(
    (call) => call.method === 'GetAvatarState',
  ).length;
  await contactHint(page, id, 20);
  await expect
    .poll(() => calls.filter((call) => call.method === 'GetAvatarState').length)
    .toBeGreaterThan(before);
  await expect(page.locator('.contact-list img.avatar-photo')).toHaveCount(0);
  await expect(page.locator('.header-identity img.avatar-photo')).toHaveCount(
    0,
  );
  await expect(page.locator('.device-footer img.avatar-photo')).toHaveAttribute(
    'src',
    changed,
  );
  expect(errors).toEqual([]);
});
