import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailableConversation } from '../src/lib/messaging';
import { unavailablePairing } from '../src/lib/pairing';
import { defaultAppPreferences } from '../src/lib/state/preferences';
import { contactSummary, messagePage, contactHint } from './contact-events';
import {
  relayCarryPrefix,
  type RelayChangeSnapshot,
} from '../src/lib/state/relay';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ',
  requestID = 'AgICAgICAgICAgICAgICAg';
const url = 'https://new-relay.example/map.json';
const carry = relayCarryPrefix + 'c3ludGhldGlj';
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const methods = new Map<number, string>();
for (const match of generated.matchAll(
  /export function (\w+)\([\s\S]*?\$Call\.ByID\((\d+)/g,
))
  methods.set(Number(match[2]), match[1]);
const idle = (): RelayChangeSnapshot => ({
  contactID,
  revision: 1,
  requestID: '',
  state: 'idle',
  currentServiceURL: 'https://relay.example/map.json',
  proposedServiceURL: '',
  proposedHost: '',
  localApproved: false,
  peerApproved: false,
  canPropose: true,
  canApprove: false,
});
const incoming = (): RelayChangeSnapshot => ({
  ...idle(),
  revision: 2,
  requestID,
  state: 'awaiting-approval',
  proposedServiceURL: url,
  proposedHost: 'new-relay.example',
  peerApproved: true,
  canPropose: false,
  canApprove: true,
});
// Uses generated HTTP binding routes with synthetic status/signature results.
// Go tests, not this fixture, establish signature, disk and transport authority.
async function desktop(
  page: Page,
  options: { offline?: boolean; incoming?: boolean } = {},
) {
  const calls: { name: string; args: unknown[] }[] = [],
    fetches: string[] = [];
  const state = {
    revision: 1,
    peer: options.offline ? 'offline' : 'connected',
    protocol: options.offline ? 0 : 4,
    controls: !options.offline,
    route: options.incoming ? incoming() : idle(),
  };
  const conversation = () => ({
    ...unavailableConversation,
    state: state.peer as 'offline' | 'connected' | 'peer-removed',
    revision: state.revision,
    contactID,
    peerFingerprint: 'b'.repeat(64),
    localName: 'Synthetic contact',
    peerProtocol: state.protocol as 0 | 3 | 4,
    attachmentsSupported: state.peer === 'connected' && state.protocol === 4,
    contactControlsSupported: state.peer === 'connected' && state.controls,
    canSend: state.peer !== 'peer-removed',
    canConnect: state.peer === 'offline',
    messages: [
      {
        id: 'AwMDAwMDAwMDAwMDAwMDAw',
        direction: 'incoming' as const,
        text: 'Synthetic history kept after removal',
        createdAt: '2026-11-14T12:00:00Z',
        delivered: true,
      },
    ],
  });
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
  });
  await page.route('**/wails/runtime', async (route) => {
    const body = route.request().postDataJSON(),
      name = methods.get(body.args?.methodID),
      args: unknown[] = body.args?.args ?? [];
    if (!name) throw new Error('Unknown generated relay fixture operation');
    calls.push({ name, args });
    let result: unknown = null;
    switch (name) {
      case 'GetStatus':
        result = {
          ...unavailableStatus,
          storageState: 'unlocked',
          identityFingerprint: 'a'.repeat(64),
          messagingAvailable: true,
          pairingAvailable: true,
        };
        break;
      case 'GetAppPreferences':
        result = defaultAppPreferences;
        break;
      case 'GetStorageSecurity':
        result = {
          requiresPassphrase: false,
          recoveryAvailable: true,
          busy: false,
        };
        break;
      case 'GetProfile':
        result = { displayName: '', shareDisplayName: false };
        break;
      case 'GetPairingStatus':
        result = { ...unavailablePairing, state: 'idle' };
        break;
      case 'GetContacts':
        result = [contactSummary(conversation())];
        break;
      case 'GetMessages':
        result = messagePage(conversation());
        break;
      case 'MarkRead':
        result = contactSummary(conversation());
        break;
      case 'GetRelayChange':
        result = state.route;
        break;
      case 'ProposeRelayChange':
        if (args[0] !== contactID || args[2] !== true)
          throw new Error('Missing exact proposal consent');
        state.route = {
          ...idle(),
          revision: state.route.revision + 1,
          requestID,
          state: 'proposed',
          proposedServiceURL: String(args[1]),
          proposedHost: new URL(String(args[1])).host,
          localApproved: true,
          canPropose: false,
        };
        fetches.push(String(args[1]));
        result = state.route;
        break;
      case 'ApproveRelayChange':
        if (args[1] !== state.route.requestID || args[2] !== true)
          throw new Error('Missing exact approval consent');
        fetches.push(state.route.proposedServiceURL);
        state.route = {
          ...state.route,
          revision: state.route.revision + 1,
          state: 'preparing',
          localApproved: true,
          peerApproved: true,
          canApprove: false,
        };
        result = state.route;
        break;
      case 'DeclineRelayChange':
      case 'CancelRelayChange':
        state.route = {
          ...idle(),
          revision: state.route.revision + 1,
          state: 'declined',
        };
        result = state.route;
        break;
      case 'ExportRelayChange':
        result = { text: carry };
        break;
      case 'ImportRelayChange':
        if (args[0] !== contactID || args[1] !== carry)
          throw new Error('Wrong signed inspection context');
        state.route = { ...incoming(), revision: state.route.revision + 1 };
        result = state.route;
        break;
      case 'SetAttachmentInputContact':
        break;
      default:
        throw new Error(`Unexpected relay fixture operation ${name}`);
    }
    await route.fulfill({ json: result });
  });
  return { state, calls, fetches };
}
async function settings(page: Page) {
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  await page.getByRole('button', { name: 'Contact', exact: true }).click();
  await expect(
    page.getByRole('heading', { name: 'Connection service', exact: true }),
  ).toBeVisible();
}

test('service preset fills only the empty field and consent stays separate before proposal', async ({
  page,
}) => {
  const { calls, fetches } = await desktop(page);
  await page.goto('/');
  await expect(
    page.getByText('Synthetic history kept after removal', { exact: true }),
  ).toBeVisible();
  await settings(page);
  const input = page.getByLabel('New HTTPS relay-map URL');
  await expect(input).toHaveValue('');
  await expect(input).toBeEnabled();
  const allow = page.getByRole('checkbox', {
    name: 'Allow this service to establish',
    exact: false,
  });
  await page
    .getByRole('button', { name: 'Use Tailcat public relay', exact: true })
    .click();
  await expect(input).toHaveValue('https://tailcat.dev/derpmap.json');
  await expect(allow).not.toBeChecked();
  expect(fetches).toEqual([]);
  await input.fill(url);
  await expect(
    page.getByRole('button', { name: 'Use Tailcat public relay', exact: true }),
  ).toBeDisabled();
  await allow.check();
  await page
    .getByRole('button', { name: 'Propose service change', exact: true })
    .click();
  await expect(
    page.getByText('Waiting for the other person to approve this service.', {
      exact: true,
    }),
  ).toBeVisible();
  expect(calls.find((x) => x.name === 'ProposeRelayChange')?.args).toEqual([
    contactID,
    url,
    true,
  ]);
  expect(fetches).toEqual([url]);
});
test('peer proposal opens an unchecked approval dialog before any service fetch', async ({
  page,
}) => {
  const { calls, fetches, state } = await desktop(page, { incoming: true });
  await page.goto('/');
  const dialog = page.getByRole('dialog', {
    name: 'Allow a connection service change?',
  });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(url, { exact: true })).toBeVisible();
  const allow = dialog.getByRole('checkbox', {
    name: 'Allow this service for encrypted connections',
    exact: true,
  });
  await expect(allow).not.toBeChecked();
  expect(fetches).toEqual([]);
  await expect(
    dialog.getByRole('button', { name: 'Allow service change', exact: true }),
  ).toBeDisabled();
  await allow.check();
  const reads = calls.filter((call) => call.name === 'GetRelayChange').length;
  state.route = { ...state.route, revision: state.route.revision + 1 };
  state.revision = state.route.revision;
  await contactHint(page, contactID, state.revision);
  await expect
    .poll(() => calls.filter((call) => call.name === 'GetRelayChange').length)
    .toBeGreaterThan(reads);
  await expect(allow).toBeChecked();
  await dialog
    .getByRole('button', { name: 'Allow service change', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(calls.find((x) => x.name === 'ApproveRelayChange')?.args).toEqual([
    contactID,
    requestID,
    true,
  ]);
  expect(fetches).toEqual([url]);
});
test('offline signed proposal inspection is local until separate approval and can export the signed reply', async ({
  page,
}) => {
  const { calls, fetches } = await desktop(page, { offline: true });
  await page.goto('/');
  await settings(page);
  await expect(page.getByLabel('New HTTPS relay-map URL')).toBeDisabled();
  await page
    .getByText('Advanced: exchange signed text', { exact: true })
    .click();
  await page
    .getByLabel('Signed text from this contact', { exact: true })
    .fill(carry);
  await page
    .getByRole('button', { name: 'Inspect signed text', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Allow a connection service change?',
  });
  await expect(dialog).toBeVisible();
  expect(fetches).toEqual([]);
  expect(calls.some((x) => x.name === 'ApproveRelayChange')).toBe(false);
  await dialog
    .getByRole('checkbox', {
      name: 'Allow this service for encrypted connections',
      exact: true,
    })
    .check();
  await dialog
    .getByRole('button', { name: 'Allow service change', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  expect(fetches).toEqual([url]);
  await page
    .getByRole('button', { name: 'Prepare signed text', exact: true })
    .click();
  await expect(
    page.getByLabel('Signed service-change text', { exact: true }),
  ).toHaveValue(carry);
  expect(calls.find((x) => x.name === 'ImportRelayChange')?.args).toEqual([
    contactID,
    carry,
  ]);
});
test('offline decline can export signed text after its live request token has been retired', async ({
  page,
}) => {
  const { calls, fetches } = await desktop(page, { offline: true });
  await page.goto('/');
  await settings(page);
  await page
    .getByText('Advanced: exchange signed text', { exact: true })
    .click();
  await page
    .getByLabel('Signed text from this contact', { exact: true })
    .fill(carry);
  await page
    .getByRole('button', { name: 'Inspect signed text', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Allow a connection service change?',
  });
  await dialog.getByRole('button', { name: 'Decline', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await page
    .getByRole('button', { name: 'Prepare signed text', exact: true })
    .click();
  await expect(
    page.getByLabel('Signed service-change text', { exact: true }),
  ).toHaveValue(carry);
  expect(calls.some((x) => x.name === 'ExportRelayChange')).toBe(true);
  expect(fetches).toEqual([]);
});
test('authenticated contact removal is terminal and retains local history', async ({
  page,
}) => {
  const { state } = await desktop(page);
  await page.goto('/');
  await expect(
    page.getByText('Synthetic history kept after removal', { exact: true }),
  ).toBeVisible();
  state.peer = 'peer-removed';
  state.protocol = 0;
  state.controls = false;
  state.revision++;
  await contactHint(page, contactID, state.revision);
  await expect(
    page.getByText(
      'This contact removed you. Messaging and automatic reconnect have stopped.',
      { exact: false },
    ),
  ).toBeVisible();
  await expect(
    page.getByText('Synthetic history kept after removal', { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Send message', exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole('button', { name: 'Connect', exact: true }),
  ).toHaveCount(0);
});
