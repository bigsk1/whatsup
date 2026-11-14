import { serveDefaultAppPreferences } from './contact-events';
import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import type { ContactSummary } from '../src/lib/state/bridge';
import type { ConversationMessage } from '../src/lib/messaging';
import {
  contactHint,
  nativeHint,
  notificationActivationID,
} from './contact-events';

const first = 'AQEBAQEBAQEBAQEBAQEBAQ';
const second = 'AgICAgICAgICAgICAgICAg';
const messageID = 'AwMDAwMDAwMDAwMDAwMDAw';
const earlierID = 'BAQEBAQEBAQEBAQEBAQEBA';
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const methods = [
  'GetStatus',
  'LockStorage',
  'GetPairingStatus',
  'GetProfile',
  'GetContacts',
  'GetMessages',
  'MarkRead',
  'ForgetContact',
] as const;
type Method = (typeof methods)[number];
const operations = new Map<number, Method>();
for (const method of methods) {
  const match = generated.match(
    new RegExp(`export function ${method}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing generated ${method} binding`);
  operations.set(Number(match[1]), method);
}

function contact(
  contactID: string,
  localName: string,
  unread = 0,
): ContactSummary {
  return {
    peerProtocol: 0,
    attachmentsSupported: false,
    contactControlsSupported: false,
    contactID,
    localName,
    unread,
    state: 'offline',
    path: 'Unknown',
    revision: 1,
    peerFingerprint: (contactID === first ? 'b' : 'c').repeat(64),
    peerDisplayName: '',
    canConnect: true,
    canSend: true,
  };
}
function message(id: string, text: string): ConversationMessage {
  return {
    id,
    text,
    direction: 'outgoing',
    createdAt: '2026-11-14T12:00:00Z',
    delivered: true,
  };
}
async function fixture(
  page: Page,
  activation = '',
  holdContacts?: () => Promise<void>,
) {
  const state = {
    contacts: [
      contact(first, 'Synthetic first'),
      contact(second, 'Synthetic second', 3),
    ],
    older: false,
    activation,
  };
  const calls: { method: Method; args: unknown[] }[] = [];
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
    if (
      route.request().postDataJSON().args?.methodID === notificationActivationID
    ) {
      const target = state.activation;
      state.activation = '';
      await route.fulfill({ json: target });
      return;
    }
    if (await serveDefaultAppPreferences(route)) return;
    const body = route.request().postDataJSON();
    const method = operations.get(body.args?.methodID);
    if (!method) throw new Error('Unexpected contact fixture operation');
    const args = body.args.args ?? [];
    calls.push({ method, args });
    if (method === 'GetStatus' || method === 'LockStorage') {
      await route.fulfill({
        json: {
          ...unavailableStatus,
          storageState: method === 'GetStatus' ? 'unlocked' : 'locked',
          identityFingerprint: method === 'GetStatus' ? 'a'.repeat(64) : '',
          pairingAvailable: method === 'GetStatus',
        },
      });
    } else if (method === 'GetPairingStatus') {
      await route.fulfill({ json: { ...unavailablePairing, state: 'idle' } });
    } else if (method === 'GetProfile') {
      await route.fulfill({
        json: { displayName: '', shareDisplayName: false },
      });
    } else if (method === 'GetContacts') {
      await holdContacts?.();
      await route.fulfill({ json: state.contacts });
    } else if (method === 'GetMessages') {
      const contact = state.contacts.find(
        (item) => item.contactID === args[0],
      )!;
      const old = args[1] !== '';
      await route.fulfill({
        json: {
          contactID: contact.contactID,
          revision: contact.revision,
          messages: [
            old
              ? message(earlierID, 'Synthetic older text')
              : message(messageID, `${contact.localName} history`),
          ],
          hasMore: state.older && !old,
          nextCursor: state.older && !old ? '101' : '',
        },
      });
    } else if (method === 'MarkRead') {
      const contact = state.contacts.find(
        (item) => item.contactID === args[0],
      )!;
      contact.unread = 0;
      await route.fulfill({ json: contact });
    } else if (method === 'ForgetContact') {
      state.contacts = state.contacts.filter(
        (item) => item.contactID !== args[0],
      );
      await route.fulfill({
        json: {
          state: 'none',
          path: 'Unknown',
          revision: 2,
          contactID: '',
          peerFingerprint: '',
          peerDisplayName: '',
          localName: '',
          canConnect: false,
          canSend: false,
          messages: [],
        },
      });
    }
  });
  return { state, calls, errors };
}

test('early notification waits for saved contacts and selects only its approved conversation', async ({
  page,
}) => {
  let release!: () => void;
  const held = new Promise<void>((done) => (release = done));
  const { calls, errors } = await fixture(page, second, () => held);
  await page.goto('/');
  await expect
    .poll(() => calls.some((call) => call.method === 'GetContacts'))
    .toBe(true);
  expect(calls.some((call) => call.method === 'MarkRead')).toBe(false);
  release();
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic second history',
  );
  expect(calls.some((call) => call.method === 'LockStorage')).toBe(false);
  expect(errors).toEqual([]);
});

test('notification activation leaves Settings for the saved conversation and ignores unknown contacts', async ({
  page,
}) => {
  const { state, errors } = await fixture(page);
  await page.goto('/');
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic first history',
  );
  await page.keyboard.press('Control+,');
  await expect(
    page.getByRole('region', { name: 'Settings', exact: true }),
  ).toBeVisible();
  state.activation = second;
  await nativeHint(page, 'whatsup:notification-activate', {
    contactID: second,
  });
  await expect(
    page.getByRole('region', { name: 'Settings', exact: true }),
  ).not.toBeVisible();
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic second history',
  );
  state.activation = 'AwMDAwMDAwMDAwMDAwMDAw';
  await nativeHint(page, 'whatsup:notification-activate', {
    contactID: state.activation,
  });
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic second history',
  );
  expect(errors).toEqual([]);
});

test('separate contacts keep drafts and unread counts independent, and Forget preserves the other conversation', async ({
  page,
}) => {
  const { calls, errors } = await fixture(page);
  await page.goto('/');
  await expect(
    page.getByRole('button', {
      name: 'Conversation with Synthetic first',
      exact: true,
    }),
  ).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByLabel('3 unread messages')).toBeVisible();
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  await composer.fill('First private draft');
  await page
    .getByRole('button', {
      name: 'Conversation with Synthetic second',
      exact: true,
    })
    .click();
  await expect(composer).toHaveValue('');
  await composer.fill('Second private draft');
  await page
    .getByRole('button', {
      name: 'Conversation with Synthetic first',
      exact: true,
    })
    .click();
  await expect(composer).toHaveValue('First private draft');
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  await page
    .getByRole('dialog')
    .getByRole('button', {
      name: 'Forget contact and delete conversation',
      exact: true,
    })
    .click();
  await expect(
    page.getByRole('button', {
      name: 'Conversation with Synthetic first',
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByRole('button', {
      name: 'Conversation with Synthetic second',
      exact: true,
    }),
  ).toHaveAttribute('aria-pressed', 'true');
  await expect(composer).toHaveValue('Second private draft');
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic second history',
  );
  await expect
    .poll(() => calls.filter((item) => item.method === 'ForgetContact'))
    .toEqual([{ method: 'ForgetContact', args: [first] }]);
  expect(errors).toEqual([]);
});

test('paged history uses opaque sequence cursors and missed-event recovery does not poll history', async ({
  page,
}) => {
  const { state, calls, errors } = await fixture(page);
  state.older = true;
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Load older messages', exact: true })
    .click();
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Synthetic older text',
  );
  expect(
    calls
      .filter((item) => item.method === 'GetMessages')
      .map((item) => item.args),
  ).toEqual([
    [first, '', 100],
    [first, '101', 100],
  ]);
  const pagesBefore = calls.filter(
    (item) => item.method === 'GetMessages',
  ).length;
  await page.waitForTimeout(1200);
  expect(calls.filter((item) => item.method === 'GetMessages')).toHaveLength(
    pagesBefore,
  );
  state.contacts[1] = { ...state.contacts[1], revision: 9, unread: 7 };
  await contactHint(page, second, 9);
  await expect(page.getByLabel('7 unread messages')).toBeVisible();
  await expect(page.locator('.conversation-header')).toContainText(
    'Synthetic first',
  );
  // Simulate a missed hint. Foregrounding fetches authoritative summaries.
  state.contacts[1] = { ...state.contacts[1], revision: 12, unread: 8 };
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect(page.getByLabel('8 unread messages')).toBeVisible();
  expect(
    calls.some((item) => item.method === 'MarkRead' && item.args[0] === second),
  ).toBe(false);
  expect(errors).toEqual([]);
});
