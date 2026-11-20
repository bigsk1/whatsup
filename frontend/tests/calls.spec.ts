import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import {
  defaultAppPreferences,
  type AppPreferences,
} from '../src/lib/state/preferences';
import {
  nativeHint,
  serveDefaultAvatars,
  serveDefaultNotificationActivation,
  serveDefaultRelayChange,
} from './contact-events';

// These tests run the real interface, generated bindings, call store, audio
// worklets and Opus encoder in Chromium with its built-in fake microphone.
// The desktop is synthetic: no Go, no network, no real device. They show what
// the interface does with call state, not that a call works between machines.

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const callID = 'CQkJCQkJCQkJCQkJCQkJCQ';
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
  'GetCall',
  'StartCall',
  'AnswerCall',
  'EndCall',
  'SetCallMuted',
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

// The call view DTO, restated here so this Node-side test file does not pull
// the browser-only audio modules into its type check.
interface CallView {
  contactID: string;
  state: 'idle' | 'outgoing' | 'incoming' | 'connecting' | 'active';
  callID: string;
  peerRinging: boolean;
  micMuted: boolean;
  peerMicMuted: boolean;
  activeSince: number;
  availability: 'ready' | 'calls-off' | 'offline' | 'peer-update' | 'busy';
  lastEnd: string;
  lastOutgoing: boolean;
  lastConnected: boolean;
}

function idle(patch: Partial<CallView> = {}): CallView {
  return {
    contactID,
    state: 'idle',
    callID: '',
    peerRinging: false,
    micMuted: false,
    peerMicMuted: false,
    activeSince: 0,
    availability: 'ready',
    lastEnd: '',
    lastOutgoing: false,
    lastConnected: false,
    ...patch,
  };
}

interface Microphone {
  requests: number;
  live: number;
  stopped: number;
}

async function fixture(page: Page, voiceCalls: boolean) {
  const state = {
    preferences: { ...defaultAppPreferences, voiceCalls } as AppPreferences,
    call: idle({ availability: voiceCalls ? 'ready' : 'calls-off' }),
    contact: {
      contactID,
      revision: 1,
      state: 'connected',
      peerFingerprint: 'b'.repeat(64),
      peerDisplayName: 'Synthetic Friend',
      localName: '',
      canConnect: false,
      canSend: true,
      unread: 0,
      paused: false,
      path: 'Direct',
      peerProtocol: 4,
      attachmentsSupported: false,
      contactControlsSupported: false,
    },
  };
  const calls: { operation: Operation; args: unknown[] }[] = [];
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
      __microphone?: { requests: number; live: number; stopped: number };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
    // Count every microphone request and every track that is still live.
    const microphone = { requests: 0, live: 0, stopped: 0 };
    host.__microphone = microphone;
    const original = navigator.mediaDevices.getUserMedia.bind(
      navigator.mediaDevices,
    );
    navigator.mediaDevices.getUserMedia = async (constraints) => {
      microphone.requests += 1;
      const stream = await original(constraints);
      for (const track of stream.getTracks()) {
        microphone.live += 1;
        const stop = track.stop.bind(track);
        track.stop = () => {
          if (track.readyState === 'live') {
            microphone.live -= 1;
            microphone.stopped += 1;
          }
          stop();
        };
      }
      return stream;
    };
  });
  await page.route('**/wails/runtime', async (route) => {
    if (await serveDefaultAvatars(route)) return;
    if (await serveDefaultNotificationActivation(route)) return;
    if (await serveDefaultRelayChange(route)) return;
    const request = route.request().postDataJSON();
    const operation = operations.get(request.args?.methodID);
    if (!operation) throw new Error('Unexpected call fixture operation');
    const args = request.args.args ?? [];
    calls.push({ operation, args });
    switch (operation) {
      case 'GetStorageSecurity':
        return route.fulfill({
          json: {
            requiresPassphrase: false,
            recoveryAvailable: true,
            busy: false,
          },
        });
      case 'GetStatus':
      case 'LockStorage':
      case 'UnlockStorage':
        return route.fulfill({
          json: {
            ...unavailableStatus,
            storageState: 'unlocked',
            identityFingerprint: 'a'.repeat(64),
            pairingAvailable: true,
            messagingAvailable: true,
          },
        });
      case 'MarkRead':
        return route.fulfill({ json: state.contact });
      case 'GetAppPreferences':
        return route.fulfill({ json: state.preferences });
      case 'SetAppPreferences':
        state.preferences = args[0] as AppPreferences;
        state.call = idle({
          availability: state.preferences.voiceCalls ? 'ready' : 'calls-off',
        });
        return route.fulfill({ json: state.preferences });
      case 'GetProfile':
        return route.fulfill({
          json: { displayName: '', shareDisplayName: false },
        });
      case 'GetPairingStatus':
        return route.fulfill({
          json: { ...unavailablePairing, state: 'idle' },
        });
      case 'GetContacts':
        return route.fulfill({ json: [state.contact] });
      case 'GetMessages':
        return route.fulfill({
          json: {
            contactID,
            revision: state.contact.revision,
            messages: [],
            nextCursor: '',
            hasMore: false,
          },
        });
      case 'GetCall':
        return route.fulfill({ json: state.call });
      case 'StartCall':
        state.call = idle({ state: 'outgoing', callID });
        return route.fulfill({ json: state.call });
      case 'AnswerCall':
        state.call = idle({ state: 'connecting', callID });
        return route.fulfill({ json: state.call });
      case 'EndCall':
        state.call = idle({ lastEnd: 'hung-up' });
        return route.fulfill({ json: state.call });
      case 'SetCallMuted':
        state.call = { ...state.call, micMuted: args[2] as boolean };
        return route.fulfill({ json: state.call });
    }
  });
  const push = async (view: CallView) => {
    state.call = view;
    await nativeHint(page, 'whatsup:call-changed', { contactID });
  };
  const microphone = () =>
    page.evaluate(
      () => (window as Window & { __microphone?: Microphone }).__microphone!,
    );
  const count = (operation: Operation) =>
    calls.filter((call) => call.operation === operation).length;
  return { state, calls, errors, push, microphone, count };
}

test('voice calls are off by default: nothing asks the desktop about calls until they are switched on from the phone button', async ({
  page,
}) => {
  const { state, count, microphone, errors } = await fixture(page, false);
  await page.goto('/');
  const phone = page.getByRole('button', {
    name: 'Voice call (switched off)',
    exact: true,
  });
  await expect(phone).toBeEnabled();
  expect(count('GetCall')).toBe(0);
  await phone.click();
  const dialog = page.getByRole('dialog', {
    name: 'Voice calls are switched off',
  });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  expect(count('StartCall')).toBe(0);
  await dialog.getByRole('button', { name: 'Turn on voice calls' }).click();
  await expect.poll(() => state.preferences.voiceCalls).toBe(true);
  await expect(
    page.getByRole('button', { name: 'Call Synthetic Friend', exact: true }),
  ).toBeEnabled();
  expect(count('GetCall')).toBeGreaterThan(0);
  expect(count('StartCall')).toBe(0);
  expect((await microphone()).requests).toBe(0);
  expect(errors).toEqual([]);
});

test('an outgoing call rings without the microphone, captures only once answered, and releases it on hang-up', async ({
  page,
}) => {
  const { push, microphone, count, calls, errors } = await fixture(page, true);
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Call Synthetic Friend', exact: true })
    .click();
  const confirm = page.getByRole('dialog', { name: 'Call Synthetic Friend?' });
  await expect(confirm).toContainText(
    'Your microphone turns on only after they answer',
  );
  expect(count('StartCall')).toBe(0);
  await confirm.getByRole('button', { name: 'Call', exact: true }).click();

  const bar = page.getByRole('region', { name: 'Voice call', exact: true });
  await expect(bar).toContainText('Calling…');
  await push(idle({ state: 'outgoing', callID, peerRinging: true }));
  await expect(bar).toContainText('Ringing…');
  await expect(bar.getByRole('button', { name: /mute/i })).toHaveCount(0);
  expect((await microphone()).requests).toBe(0);
  // While a call is in progress the phone button cannot start another.
  await expect(
    page.getByRole('button', { name: 'You are already in a call' }),
  ).toBeDisabled();

  await push(idle({ state: 'connecting', callID }));
  await expect(bar).toContainText('Connecting…');
  await expect(bar).toContainText('Microphone on');
  await expect.poll(async () => (await microphone()).live).toBe(1);
  expect((await microphone()).requests).toBe(1);

  await push(idle({ state: 'active', callID, activeSince: Date.now() }));
  await expect(bar).toContainText(/00:0\d · Direct/);
  await bar.getByRole('button', { name: 'Mute microphone' }).click();
  await expect(bar).toContainText('Muted');
  expect(calls.find((c) => c.operation === 'SetCallMuted')?.args).toEqual([
    contactID,
    callID,
    true,
  ]);

  await bar.getByRole('button', { name: 'Hang up' }).click();
  await expect(bar).toHaveCount(0);
  await expect.poll(async () => (await microphone()).live).toBe(0);
  expect((await microphone()).stopped).toBe(1);
  expect((await microphone()).requests).toBe(1);
  // The audio chain ran without failing: no call notice appeared.
  await expect(
    page.getByRole('status').filter({ hasText: /audio|microphone/i }),
  ).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('an incoming call cannot be answered by accident, opens the microphone only on Accept, and releases it when the caller hangs up', async ({
  page,
}) => {
  const { push, microphone, count, errors } = await fixture(page, true);
  await page.goto('/');
  await expect(
    page.getByRole('button', { name: 'Call Synthetic Friend', exact: true }),
  ).toBeEnabled();
  await push(idle({ state: 'incoming', callID }));
  const dialog = page.getByRole('dialog', { name: 'Incoming voice call' });
  await expect(dialog).toContainText('Synthetic Friend');
  await expect(dialog.getByRole('button', { name: 'Decline' })).toBeFocused();
  // Escape neither answers nor declines.
  await page.keyboard.press('Escape');
  await expect(dialog).toBeVisible();
  expect(count('AnswerCall')).toBe(0);
  expect(count('EndCall')).toBe(0);
  expect((await microphone()).requests).toBe(0);

  await dialog.getByRole('button', { name: 'Accept' }).click();
  await expect(dialog).toBeHidden();
  const bar = page.getByRole('region', { name: 'Voice call', exact: true });
  await expect(bar).toContainText('Microphone on');
  await expect.poll(async () => (await microphone()).live).toBe(1);
  expect(count('AnswerCall')).toBe(1);

  await push(idle({ lastEnd: 'peer-hung-up', lastConnected: true }));
  await expect(bar).toHaveCount(0);
  await expect(page.getByText('Call ended.', { exact: true })).toBeVisible();
  await expect.poll(async () => (await microphone()).live).toBe(0);
  expect((await microphone()).requests).toBe(1);
  expect(errors).toEqual([]);
});

test('declining an incoming call never touches the microphone', async ({
  page,
}) => {
  const { push, microphone, calls, errors } = await fixture(page, true);
  await page.goto('/');
  await expect(
    page.getByRole('button', { name: 'Call Synthetic Friend', exact: true }),
  ).toBeEnabled();
  await push(idle({ state: 'incoming', callID }));
  const dialog = page.getByRole('dialog', { name: 'Incoming voice call' });
  await dialog.getByRole('button', { name: 'Decline' }).click();
  await expect(dialog).toBeHidden();
  expect(calls.find((c) => c.operation === 'EndCall')?.args).toEqual([
    contactID,
    callID,
  ]);
  expect(await microphone()).toEqual({ requests: 0, live: 0, stopped: 0 });
  expect(errors).toEqual([]);
});
