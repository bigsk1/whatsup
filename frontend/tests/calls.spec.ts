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
const otherCallID = 'CgoKCgoKCgoKCgoKCgoKCg';
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
  'SetCallCamera',
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
  availability:
    'ready' | 'calls-off' | 'offline' | 'peer-update' | 'busy' | 'blocked';
  lastEnd: string;
  lastOutgoing: boolean;
  lastConnected: boolean;
  video: boolean;
  cameraOn: boolean;
  peerCameraOn: boolean;
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
    video: false,
    cameraOn: false,
    peerCameraOn: false,
    ...patch,
  };
}

interface Microphone {
  requests: number;
  live: number;
  stopped: number;
}

// The camera, counted separately from the microphone.
interface Camera {
  requests: number;
  live: number;
  stopped: number;
}

// A one-pixel PNG standing in for a contact's shared profile picture.
const photoURL = '/local-media/' + 'b'.repeat(32);
const photoBytes = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

async function fixture(
  page: Page,
  voiceCalls: boolean,
  videoCalls = false,
  sharedPhoto = false,
) {
  const state = {
    preferences: {
      ...defaultAppPreferences,
      voiceCalls,
      videoCalls,
    } as AppPreferences,
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
      __camera?: { requests: number; live: number; stopped: number };
      __pictures?: { attached: number; detachedLive: number };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
    // Count every microphone request and every track that is still live.
    const microphone = { requests: 0, live: 0, stopped: 0 };
    const camera = { requests: 0, live: 0, stopped: 0 };
    host.__microphone = microphone;
    host.__camera = camera;
    // Every time a media element is given a stream, and every time it lets go
    // of one whose camera is still running. Some engines lock up on the
    // second, so the application must stop a camera before detaching it.
    const pictures = { attached: 0, detachedLive: 0 };
    host.__pictures = pictures;
    const slot = Object.getOwnPropertyDescriptor(
      HTMLMediaElement.prototype,
      'srcObject',
    )!;
    Object.defineProperty(HTMLMediaElement.prototype, 'srcObject', {
      configurable: true,
      enumerable: slot.enumerable,
      get(this: HTMLMediaElement) {
        return slot.get!.call(this);
      },
      set(this: HTMLMediaElement, value: MediaProvider | null) {
        const held = slot.get!.call(this) as MediaStream | null;
        if (held && held !== value) {
          const running = held
            .getVideoTracks()
            .some((track) => track.readyState === 'live');
          if (running) pictures.detachedLive += 1;
        }
        if (value && value !== held) pictures.attached += 1;
        slot.set!.call(this, value);
      },
    });
    const original = navigator.mediaDevices.getUserMedia.bind(
      navigator.mediaDevices,
    );
    navigator.mediaDevices.getUserMedia = async (constraints) => {
      const device = constraints?.video ? camera : microphone;
      device.requests += 1;
      const stream = await original(constraints);
      for (const track of stream.getTracks()) {
        device.live += 1;
        const stop = track.stop.bind(track);
        track.stop = () => {
          if (track.readyState === 'live') {
            device.live -= 1;
            device.stopped += 1;
          }
          stop();
        };
      }
      return stream;
    };
  });
  await page.route('**/local-media/*', (route) =>
    route.fulfill({ contentType: 'image/png', body: photoBytes }),
  );
  await page.route('**/wails/runtime', async (route) => {
    if (
      await serveDefaultAvatars(
        route,
        sharedPhoto ? [{ contactID, url: photoURL }] : [],
      )
    )
      return;
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
        state.call = idle({
          state: 'outgoing',
          callID,
          video: args[1] === true,
        });
        return route.fulfill({ json: state.call });
      case 'AnswerCall':
        state.call = idle({
          state: 'connecting',
          callID,
          video: args[2] === true && state.call.video,
        });
        return route.fulfill({ json: state.call });
      case 'SetCallCamera':
        state.call = { ...state.call, cameraOn: args[2] === true };
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
  const camera = () =>
    page.evaluate(() => (window as Window & { __camera?: Camera }).__camera!);
  const pictures = () =>
    page.evaluate(
      () =>
        (
          window as Window & {
            __pictures?: { attached: number; detachedLive: number };
          }
        ).__pictures!,
    );
  const count = (operation: Operation) =>
    calls.filter((call) => call.operation === operation).length;
  return { state, calls, errors, push, microphone, camera, pictures, count };
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
  // The details readout shows the microphone really being captured and
  // encoded, and the sound output really running. Counts only.
  const details = bar.getByRole('button', { name: 'Details' });
  await expect(details).toHaveAttribute('aria-expanded', 'false');
  await details.click();
  await expect(bar).toContainText(/microphone [1-9]\d* · sent [1-9]\d*/, {
    timeout: 10_000,
  });
  await expect(bar).toContainText(/received 0 · decoded 0 · played 0\s+s/);
  await expect(bar).toContainText(/Sound output\s*running/);
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

test('a video call is asked for by name, opens the camera only once answered, shows both pictures areas and releases the camera on hang-up', async ({
  page,
}) => {
  const { push, microphone, camera, pictures, count, calls, state, errors } =
    await fixture(page, true, true);
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Video call Synthetic Friend', exact: true })
    .click();
  const confirm = page.getByRole('dialog', {
    name: 'Video call Synthetic Friend?',
  });
  await expect(confirm).toContainText(
    'Your microphone and camera turn on only after they answer',
  );
  expect(count('StartCall')).toBe(0);
  await confirm
    .getByRole('button', { name: 'Video call', exact: true })
    .click();
  await expect
    .poll(() => calls.find((c) => c.operation === 'StartCall')?.args)
    .toEqual([contactID, true]);

  const bar = page.getByRole('region', { name: 'Video call', exact: true });
  await expect(bar).toContainText('Calling…');
  // Ringing: no picture area, no camera, no microphone.
  await expect(bar.getByLabel('Their video')).toHaveCount(0);
  expect((await camera()).requests).toBe(0);
  expect((await microphone()).requests).toBe(0);
  expect(count('SetCallCamera')).toBe(0);

  await push({ ...state.call, state: 'connecting' });
  // Answered: the desktop is told the camera is on, then the camera opens.
  await expect.poll(async () => (await camera()).live).toBe(1);
  expect(calls.find((c) => c.operation === 'SetCallCamera')?.args).toEqual([
    contactID,
    callID,
    true,
    false,
  ]);
  await expect.poll(async () => (await microphone()).live).toBe(1);
  await expect(bar.getByLabel('Your camera')).toBeVisible();
  // Their camera is off: the area shows who they are, with the state below,
  // and no picture frame. No photo is shared here, so it is their initial.
  const placeholder = bar.locator('.video-placeholder');
  await expect(placeholder.locator('.avatar')).toBeVisible();
  await expect(placeholder.locator('.avatar-letter')).toHaveText('S');
  await expect(placeholder).toContainText('Their camera is off');
  await expect(bar.getByLabel('Their video')).toBeHidden();
  const avatarBox = await placeholder.locator('.avatar').boundingBox();
  const statusBox = await placeholder.getByRole('status').boundingBox();
  const areaBox = await bar.locator('.call-video').boundingBox();
  expect(avatarBox && statusBox && areaBox).toBeTruthy();
  // Centred in the area, with the text beneath the avatar.
  expect(
    Math.abs(
      avatarBox!.x + avatarBox!.width / 2 - (areaBox!.x + areaBox!.width / 2),
    ),
  ).toBeLessThan(3);
  expect(statusBox!.y).toBeGreaterThanOrEqual(avatarBox!.y + avatarBox!.height);

  await push({
    ...state.call,
    state: 'active',
    activeSince: Date.now(),
    peerCameraOn: true,
  });
  // Their camera is on but no picture has arrived yet: still the avatar.
  await expect(placeholder).toContainText('Starting video…');
  await expect(placeholder.locator('.avatar')).toBeVisible();
  // The details readout shows pictures really being captured and encoded.
  await bar.getByRole('button', { name: 'Details' }).click();
  await expect(bar).toContainText(
    /camera on · captured [1-9]\d* · sent\s+[1-9]\d* · keyframes [1-9]\d*/,
    { timeout: 15_000 },
  );
  await expect(bar).toContainText(/received 0 · keyframes\s+0 · decoded 0/);

  // The call screen has refreshed many times by now: the readout, their
  // camera, the call becoming active. The camera was attached once to the
  // element pictures are taken from and once to the preview, and neither has
  // let go of it.
  await push({ ...state.call, micMuted: true });
  await push({ ...state.call, micMuted: false });
  await page.waitForTimeout(300);
  expect(await pictures()).toEqual({ attached: 2, detachedLive: 0 });

  // Camera off releases it and tells the desktop; the call carries on.
  await bar.getByRole('button', { name: 'Turn camera off' }).click();
  await expect.poll(async () => (await camera()).live).toBe(0);
  await expect(
    bar.getByRole('button', { name: 'Turn camera on' }),
  ).toBeVisible();
  // The camera is released first and the desktop is told straight after.
  await expect
    .poll(
      () => calls.filter((c) => c.operation === 'SetCallCamera').at(-1)?.args,
    )
    .toEqual([contactID, callID, false, false]);
  expect((await microphone()).live).toBe(1);
  await expect(bar.getByLabel('Your camera')).toBeHidden();
  // And on again. It counts as on once its picture has been seen to change.
  await bar.getByRole('button', { name: 'Turn camera on' }).click();
  await expect.poll(async () => (await camera()).live).toBe(1);
  await expect(bar.getByLabel('Your camera')).toBeVisible();

  await bar.getByRole('button', { name: 'Hang up' }).click();
  await expect(bar).toHaveCount(0);
  await expect.poll(async () => (await camera()).live).toBe(0);
  await expect.poll(async () => (await microphone()).live).toBe(0);
  await expect(
    page.getByRole('status').filter({ hasText: /camera|video|audio/i }),
  ).toHaveCount(0);
  // Camera off, camera on again and hang-up: a camera was always stopped
  // before anything let go of its picture.
  expect(await pictures()).toEqual({ attached: 4, detachedLive: 0 });
  expect(errors).toEqual([]);
});

test('a camera whose picture never changes is switched off and named, and the call carries on with voice', async ({
  page,
}) => {
  const { push, microphone, camera, pictures, calls, state, errors } =
    await fixture(page, true, true);
  // An engine that shows a camera's first picture and then nothing new: the
  // element never reports another picture.
  await page.addInitScript(() => {
    (
      HTMLVideoElement.prototype as HTMLVideoElement & {
        requestVideoFrameCallback: (callback: () => void) => number;
      }
    ).requestVideoFrameCallback = () => 0;
  });
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Video call Synthetic Friend', exact: true })
    .click();
  await page
    .getByRole('dialog', { name: 'Video call Synthetic Friend?' })
    .getByRole('button', { name: 'Video call', exact: true })
    .click();
  const bar = page.getByRole('region', { name: 'Video call', exact: true });
  await expect(bar).toContainText('Calling…');
  await push({ ...state.call, state: 'connecting' });
  await expect.poll(async () => (await camera()).live).toBe(1);
  await expect(
    page
      .getByRole('status')
      .filter({ hasText: 'camera picture is not updating' }),
  ).toBeVisible({ timeout: 15_000 });
  // The camera is released, the desktop is told, nothing is being sent, and
  // the call itself is untouched.
  await expect.poll(async () => (await camera()).live).toBe(0);
  await expect
    .poll(
      () => calls.filter((c) => c.operation === 'SetCallCamera').at(-1)?.args,
    )
    .toEqual([contactID, callID, false, false]);
  await expect(
    bar.getByRole('button', { name: 'Turn camera on' }),
  ).toBeVisible();
  expect((await microphone()).live).toBe(1);
  expect(calls.filter((c) => c.operation === 'EndCall')).toHaveLength(0);
  expect((await pictures()).detachedLive).toBe(0);
  expect(errors).toEqual([]);
});

test('an incoming video call can be accepted with or without video, and without video the camera is never asked for', async ({
  page,
}) => {
  const { push, microphone, camera, calls, count, state, errors } =
    await fixture(page, true, true);
  await page.goto('/');
  await expect(
    page.getByRole('button', {
      name: 'Video call Synthetic Friend',
      exact: true,
    }),
  ).toBeEnabled();
  await push(idle({ state: 'incoming', callID, video: true }));
  const dialog = page.getByRole('dialog', { name: 'Incoming video call' });
  await expect(dialog.getByRole('button', { name: 'Decline' })).toBeFocused();
  expect((await camera()).requests).toBe(0);
  await dialog.getByRole('button', { name: 'Accept without video' }).click();
  await expect(dialog).toBeHidden();
  await expect
    .poll(() => calls.find((c) => c.operation === 'AnswerCall')?.args)
    .toEqual([contactID, callID, false]);
  const bar = page.getByRole('region', { name: 'Voice call', exact: true });
  await expect(bar).toContainText('Microphone on');
  await expect.poll(async () => (await microphone()).live).toBe(1);
  await expect(bar.getByRole('button', { name: /camera/i })).toHaveCount(0);
  await expect(bar.getByLabel('Their video')).toHaveCount(0);
  expect((await camera()).requests).toBe(0);
  expect(count('SetCallCamera')).toBe(0);
  await push(idle({ lastEnd: 'peer-hung-up', lastConnected: true }));
  await expect(bar).toHaveCount(0);

  // A second incoming video call, accepted with video this time.
  await push(idle({ state: 'incoming', callID: otherCallID, video: true }));
  await dialog.getByRole('button', { name: 'Accept with video' }).click();
  await expect
    .poll(() => calls.filter((c) => c.operation === 'AnswerCall').at(-1)?.args)
    .toEqual([contactID, otherCallID, true]);
  const videoBar = page.getByRole('region', {
    name: 'Video call',
    exact: true,
  });
  await expect.poll(async () => (await camera()).live).toBe(1);
  await expect(videoBar.locator('.video-placeholder')).toContainText(
    'Their camera is off',
  );
  await push(idle({ lastEnd: 'peer-hung-up', lastConnected: true }));
  await expect.poll(async () => (await camera()).live).toBe(0);
  expect(state.call.video).toBe(false);
  expect(errors).toEqual([]);
});

test('with video calls off the video button offers to switch them on and nothing else happens', async ({
  page,
}) => {
  const { state, camera, count, errors } = await fixture(page, true, false);
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Video call (switched off)', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Video calls are switched off',
  });
  await expect(dialog).toContainText('microphone and camera');
  expect(count('StartCall')).toBe(0);
  await dialog.getByRole('button', { name: 'Turn on video calls' }).click();
  await expect.poll(() => state.preferences.videoCalls).toBe(true);
  expect(state.preferences.voiceCalls).toBe(true);
  await expect(
    page.getByRole('button', {
      name: 'Video call Synthetic Friend',
      exact: true,
    }),
  ).toBeEnabled();
  expect(count('StartCall')).toBe(0);
  expect((await camera()).requests).toBe(0);
  expect(errors).toEqual([]);
});

test('their shared profile picture stands in for the picture while their camera is off', async ({
  page,
}) => {
  const { push, errors } = await fixture(page, true, true, true);
  await page.goto('/');
  await expect(
    page.getByRole('button', {
      name: 'Video call Synthetic Friend',
      exact: true,
    }),
  ).toBeEnabled();
  await push(
    idle({ state: 'active', callID, activeSince: Date.now(), video: true }),
  );
  const bar = page.getByRole('region', { name: 'Video call', exact: true });
  const placeholder = bar.locator('.video-placeholder');
  const photo = placeholder.locator('img.avatar-photo');
  await expect(photo).toBeVisible();
  await expect(photo).toHaveAttribute('src', photoURL);
  await expect(placeholder.locator('.avatar-letter')).toHaveCount(0);
  await expect(placeholder).toContainText('Their camera is off');
  await expect(bar.getByLabel('Their video')).toBeHidden();
  expect(errors).toEqual([]);
});
