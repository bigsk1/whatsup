import { nativeHint } from './contact-events';
import { serveDefaultAppPreferences } from './contact-events';
import {
  contactSummary,
  messagePage,
  eventBackedState,
} from './contact-events';
import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import {
  unavailableConversation,
  type ConversationMessage,
  type ConversationSnapshot,
} from '../src/lib/messaging';
import { unavailablePairing, type PairingStatus } from '../src/lib/pairing';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const firstID = 'AgICAgICAgICAgICAgICAg';
const nextID = 'AwMDAwMDAwMDAwMDAwMDAw';
const peerFingerprint = 'b'.repeat(64);
const localFingerprint = 'a'.repeat(64);
const createdAt = '2026-11-14T12:00:00Z';
const incomingText = 'Synthetic saved conversation text';
const appVersion: string = JSON.parse(
  readFileSync(new URL('../package.json', import.meta.url), 'utf8'),
).version;

type Operation =
  | 'GetStatus'
  | 'UnlockStorage'
  | 'LockStorage'
  | 'GetPairingStatus'
  | 'GetContacts'
  | 'GetMessages'
  | 'MarkRead'
  | 'GetProfile'
  | 'ConnectContact'
  | 'ForgetContact'
  | 'ClearHistory'
  | 'DeleteMessage'
  | 'SendMessage';

// These tests drive the actual app and generated Wails HTTP bindings using
// synthetic status, identity and message fixtures. No OS credential or live
// peer/relay operation is performed by this browser fixture.
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
  'ConnectContact',
  'ForgetContact',
  'ClearHistory',
  'DeleteMessage',
  'SendMessage',
] as const) {
  const match = generated.match(
    new RegExp(`export function ${name}\\([\\s\\S]*?\\$Call\\.ByID\\((\\d+)`),
  );
  if (!match) throw new Error(`Missing generated ${name} binding`);
  operations[Number(match[1])] = name;
}

function approved(
  overrides: Partial<ConversationSnapshot> = {},
): ConversationSnapshot {
  return {
    peerProtocol: 0,
    attachmentsSupported: false,
    contactControlsSupported: false,
    state: 'offline',
    path: 'Unknown',
    revision: 1,
    contactID,
    peerFingerprint,
    peerDisplayName: '',
    localName: '',
    canConnect: true,
    canSend: true,
    messages: [],
    ...overrides,
  };
}

function message(
  id = firstID,
  text = incomingText,
  direction: 'incoming' | 'outgoing' = 'incoming',
  delivered = true,
): ConversationMessage {
  return { id, text, direction, createdAt, delivered };
}

function injectRuntime() {
  const host = window as Window & {
    _wails?: { flags?: object; environment?: object };
  };
  host._wails ??= {};
  host._wails.flags = {};
  host._wails.environment = { OS: 'linux', Arch: 'amd64', Debug: false };
}

async function syntheticMessaging(
  page: Page,
  initial: ConversationSnapshot = approved(),
) {
  const state = eventBackedState(page, {
    storage: 'unlocked',
    conversation: initial as unknown,
    pairing: { ...unavailablePairing, state: 'idle' } as PairingStatus,
    connect: undefined as undefined | (() => Promise<ConversationSnapshot>),
    send: undefined as
      undefined | ((text: string) => Promise<ConversationSnapshot>),
    sendFailure: false,
    clipboardFailure: false,
    lock: undefined as undefined | (() => Promise<void>),
    forget: undefined as undefined | (() => Promise<void>),
    forgetFailure: false,
    forgetUnchangedFailure: false,
    holdPoll: undefined as undefined | (() => Promise<void>),
    pollHeld: false,
    olderMessages: undefined as ConversationMessage[] | undefined,
  });
  const calls: { operation: Operation; args: unknown[] }[] = [];
  const errors: string[] = [];
  const external: string[] = [];
  const openedURLs: string[] = [];
  const clipboardWrites: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:9245/'))
      external.push(request.url());
  });
  await page.route('**/wails/runtime', async (route) => {
    const request = route.request().postDataJSON();
    if (request.object === 1 && request.method === 0) {
      if (state.clipboardFailure)
        await route.fulfill({
          status: 503,
          body: 'Synthetic clipboard unavailable',
        });
      else {
        clipboardWrites.push(request.args.text);
        await route.fulfill({ json: null });
      }
      return;
    }
    if (request.object === 9 && request.method === 0) {
      openedURLs.push(request.args.url);
      await route.fulfill({ json: null });
      return;
    }
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
      if (operation === 'UnlockStorage') {
        state.storage = 'unlocked';
        const current = state.conversation as ConversationSnapshot;
        if (current.contactID)
          state.conversation = {
            ...current,
            state: 'connecting',
            canConnect: false,
            revision: current.revision + 1,
          };
      }
      if (operation === 'LockStorage') {
        state.storage = 'locked';
        if (state.lock) await state.lock();
      }
      await route.fulfill({
        json: {
          ...unavailableStatus,
          version: appVersion,
          storageState: state.storage,
          identityFingerprint:
            state.storage === 'unlocked' ? localFingerprint : '',
          pairingAvailable: state.storage === 'unlocked',
          messagingAvailable: state.storage === 'unlocked',
        },
      });
    } else if (operation === 'GetPairingStatus') {
      await route.fulfill({ json: state.pairing });
    } else if (operation === 'GetProfile') {
      await route.fulfill({
        json: { displayName: '', shareDisplayName: false },
      });
      return;
    } else if (operation === 'GetContacts') {
      const snapshot = structuredClone(
        state.conversation,
      ) as ConversationSnapshot;
      const hold = state.holdPoll;
      state.holdPoll = undefined;
      if (hold) {
        state.pollHeld = true;
        await hold();
      }
      await route.fulfill({
        json: snapshot.contactID ? [contactSummary(snapshot)] : [],
      });
    } else if (operation === 'GetMessages') {
      const older = Boolean(args[1]);
      await route.fulfill({
        json: {
          ...messagePage(state.conversation as ConversationSnapshot),
          ...(state.olderMessages
            ? {
                messages: older
                  ? state.olderMessages
                  : (state.conversation as ConversationSnapshot).messages,
                nextCursor: older ? '' : '100',
                hasMore: !older,
              }
            : {}),
        },
      });
    } else if (operation === 'MarkRead') {
      await route.fulfill({
        json: contactSummary(state.conversation as ConversationSnapshot),
      });
    } else if (operation === 'ConnectContact') {
      const current = state.conversation as ConversationSnapshot;
      const result = state.connect
        ? await state.connect()
        : {
            ...current,
            state: 'connecting',
            revision: current.revision + 1,
            canConnect: false,
          };
      state.conversation = result;
      await route.fulfill({ json: result });
    } else if (operation === 'ClearHistory' || operation === 'DeleteMessage') {
      const current = state.conversation as ConversationSnapshot;
      state.conversation = {
        ...current,
        state: 'offline',
        revision: current.revision + 1,
        canConnect: true,
        canSend: true,
        messages:
          operation === 'ClearHistory'
            ? []
            : current.messages.filter((item) => item.id !== args[1]),
      };
      await route.fulfill({ json: state.conversation });
    } else if (operation === 'ForgetContact') {
      if (state.forget) await state.forget();
      if (state.forgetFailure || state.forgetUnchangedFailure) {
        if (!state.forgetUnchangedFailure) state.storage = 'locked';
        await route.fulfill({
          status: 503,
          json: {
            kind: 'RuntimeError',
            message: 'SYNTHETIC-PRIVATE-REMOVAL-FAILURE',
          },
        });
      } else {
        state.conversation = {
          ...unavailableConversation,
          state: 'none',
          revision: (state.conversation as ConversationSnapshot).revision + 1,
        };
        state.pairing = { ...unavailablePairing, state: 'idle' };
        await route.fulfill({ json: state.conversation });
      }
    } else if (operation === 'SendMessage' && state.sendFailure) {
      await route.fulfill({
        status: 503,
        json: {
          kind: 'RuntimeError',
          message: 'SYNTHETIC-PRIVATE-STORAGE-FAILURE',
        },
      });
    } else if (operation === 'SendMessage') {
      const current = state.conversation as ConversationSnapshot;
      const result = state.send
        ? await state.send(args[1] as string)
        : {
            ...current,
            revision: current.revision + 1,
            messages: [
              ...current.messages,
              message(nextID, args[1] as string, 'outgoing', false),
            ],
          };
      state.conversation = result;
      await route.fulfill({ json: result });
    }
  });
  await page.addInitScript(injectRuntime);
  return { state, calls, errors, external, openedURLs, clipboardWrites };
}

test('copy controls preserve URL and multiline text on both sides without loading links', async ({
  page,
}) => {
  await page.clock.install();
  const texts = [
    'https://example.invalid/private?value=synthetic&second=two',
    'An outgoing synthetic message\nwith a second line and https://example.invalid/outgoing',
  ];
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      value: undefined,
    });
  });
  const { errors, external, clipboardWrites } = await syntheticMessaging(
    page,
    approved({
      messages: [
        message(firstID, texts[0]),
        message(nextID, texts[1], 'outgoing'),
      ],
    }),
  );
  await page.goto('/');
  for (const [index, label] of ['Contact message', 'Your message'].entries()) {
    const bubble = page.getByRole('article', { name: label, exact: true });
    await bubble
      .getByRole('button', { name: 'Copy message', exact: true })
      .click();
    await expect(
      bubble.getByRole('button', { name: 'Copy message' }),
    ).toHaveText('Copied');
    expect(clipboardWrites).toEqual(texts.slice(0, index + 1));
    const status = page
      .getByRole('status')
      .filter({ hasText: 'Message copied.' });
    await expect(status).toHaveText('Message copied.');
    await page.clock.fastForward(2100);
    await expect(
      bubble.getByRole('button', { name: 'Copy message', exact: true }),
    ).toHaveText('Copy');
    await expect(status).toHaveCount(0);
  }
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('native clipboard failures keep message text and show a fixed copy fallback', async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined });
  });
  const { state, errors, external, clipboardWrites } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  state.clipboardFailure = true;
  await page.goto('/');
  const bubble = page.getByRole('article', {
    name: 'Contact message',
    exact: true,
  });
  await bubble
    .getByRole('button', { name: 'Copy message', exact: true })
    .click();
  await expect(page.getByRole('alert')).toContainText(
    'Copy is unavailable. Select the message text and press Ctrl+C to copy it.',
  );
  await expect(bubble.locator('.message-text')).toHaveText(incomingText);
  await expect(
    bubble.getByRole('button', { name: 'Copy message', exact: true }),
  ).toHaveText('Copy');
  expect(clipboardWrites).toEqual([]);
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('version opens the fixed repository through the native browser only when clicked', async ({
  page,
}) => {
  const { openedURLs, external, errors } = await syntheticMessaging(page);
  await page.goto('/');
  const version = page.getByRole('button', {
    name: 'Open WhatsUp on GitHub',
    exact: true,
  });
  await expect(version).toHaveText(`v${appVersion}`);
  expect(openedURLs).toEqual([]);
  await version.click();
  await expect
    .poll(() => openedURLs)
    .toEqual(['https://github.com/bigsk1/whatsup']);
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('messages follow the bottom for incoming and outgoing text while preserving a reader above it', async ({
  page,
}) => {
  const history = Array.from({ length: 35 }, (_, index) =>
    message(
      Buffer.alloc(16, index + 10).toString('base64url'),
      `Synthetic scroll message ${index + 1}\nSecond line of synthetic text.`,
    ),
  );
  const { state, errors } = await syntheticMessaging(
    page,
    approved({ state: 'connected', canConnect: false, messages: history }),
  );
  await page.goto('/');
  const log = page.getByRole('log', { name: 'Messages' });
  const distance = () =>
    log.evaluate(
      (view) => view.scrollHeight - view.clientHeight - view.scrollTop,
    );
  await expect(log.locator('.message-bubble')).toHaveCount(35);
  await expect.poll(distance).toBeLessThanOrEqual(1);
  state.conversation = approved({
    state: 'connected',
    canConnect: false,
    revision: 2,
    messages: [
      ...history,
      message(firstID, 'Synthetic incoming at the bottom'),
    ],
  });
  await expect(log.locator('.message-bubble')).toHaveCount(36);
  await expect.poll(distance).toBeLessThanOrEqual(1);
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Synthetic outgoing at the bottom');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(log.locator('.message-bubble')).toHaveCount(37);
  await expect.poll(distance).toBeLessThanOrEqual(1);

  await log.evaluate((view) => {
    view.scrollTop = 180;
  });
  await expect.poll(distance).toBeGreaterThan(100);
  // A real scroll event updates the reader's intent before a backend hint.
  await log.evaluate((view) => view.dispatchEvent(new Event('scroll')));
  const anchorIndex = await log.evaluate((view) => {
    const top = view.getBoundingClientRect().top;
    return Array.from(view.querySelectorAll('.message-bubble')).findIndex(
      (bubble) => bubble.getBoundingClientRect().bottom > top,
    );
  });
  const anchor = log.locator('.message-bubble').nth(anchorIndex);
  const anchorTop = (await anchor.boundingBox())!.y;
  const current = state.conversation as ConversationSnapshot;
  state.conversation = {
    ...current,
    revision: current.revision + 1,
    messages: [
      ...current.messages,
      message(
        Buffer.alloc(16, 99).toString('base64url'),
        'Synthetic incoming while reading earlier text',
      ),
    ],
  };
  await expect(log.locator('.message-bubble')).toHaveCount(38);
  await expect
    .poll(async () => (await anchor.boundingBox())!.y)
    .toBeCloseTo(anchorTop, 0);
  await expect.poll(distance).toBeGreaterThan(100);
  const cue = page.getByRole('button', {
    name: 'Show new messages',
    exact: true,
  });
  await expect(cue).toHaveText('New message ↓');
  await cue.click();
  await expect(cue).toHaveCount(0);
  await expect.poll(distance).toBeLessThanOrEqual(1);
  const resumed = state.conversation as ConversationSnapshot;
  state.conversation = {
    ...resumed,
    revision: resumed.revision + 1,
    messages: [
      ...resumed.messages,
      message(
        Buffer.alloc(16, 100).toString('base64url'),
        'Synthetic incoming after returning to the bottom',
      ),
    ],
  };
  await expect(log.locator('.message-bubble')).toHaveCount(39);
  await expect.poll(distance).toBeLessThanOrEqual(1);
  await page.setViewportSize({ width: 1024, height: 650 });
  await expect.poll(distance).toBeLessThanOrEqual(1);
  expect(errors).toEqual([]);
});

test('sending while reading older text reveals a successful send while a failed send keeps the draft and position', async ({
  page,
}) => {
  const history = Array.from({ length: 30 }, (_, index) =>
    message(
      Buffer.alloc(16, index + 10).toString('base64url'),
      `Synthetic earlier message ${index + 1}\nSecond line of synthetic text.`,
    ),
  );
  const { state, errors } = await syntheticMessaging(
    page,
    approved({ state: 'connected', canConnect: false, messages: history }),
  );
  await page.goto('/');
  const log = page.getByRole('log', { name: 'Messages' });
  const distance = () =>
    log.evaluate(
      (view) => view.scrollHeight - view.clientHeight - view.scrollTop,
    );
  await expect(log.locator('.message-bubble')).toHaveCount(30);
  await log.evaluate((view) => {
    view.scrollTop = 180;
    view.dispatchEvent(new Event('scroll'));
  });
  await expect.poll(distance).toBeGreaterThan(100);
  const anchorIndex = await log.evaluate((view) => {
    const top = view.getBoundingClientRect().top;
    return Array.from(view.querySelectorAll('.message-bubble')).findIndex(
      (bubble) => bubble.getBoundingClientRect().bottom > top,
    );
  });
  const anchor = log.locator('.message-bubble').nth(anchorIndex);
  const anchorTop = (await anchor.boundingBox())!.y;
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  await composer.fill('Synthetic send from earlier history');
  state.sendFailure = true;
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText(
    'The message could not be saved',
  );
  await expect(composer).toHaveValue('Synthetic send from earlier history');
  // The error notice changes the pane's height; preserve the visible text's
  // position rather than its raw scroll offset inside the resized pane.
  await expect
    .poll(async () => (await anchor.boundingBox())!.y)
    .toBeCloseTo(anchorTop, 0);
  await expect.poll(distance).toBeGreaterThan(100);
  state.sendFailure = false;
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(log.locator('.message-bubble')).toHaveCount(31);
  await expect(composer).toHaveValue('');
  await expect.poll(distance).toBeLessThanOrEqual(1);
  await expect(
    log.getByText('Synthetic send from earlier history', { exact: true }),
  ).toBeInViewport();
  expect(errors).toEqual([]);
});

test('loading older history keeps the existing first message at the same reading position', async ({
  page,
}) => {
  const recent = Array.from({ length: 25 }, (_, index) =>
    message(
      Buffer.alloc(16, index + 10).toString('base64url'),
      `Synthetic recent message ${index + 1}\nSecond line of synthetic text.`,
    ),
  );
  const { state, errors } = await syntheticMessaging(
    page,
    approved({ state: 'connected', canConnect: false, messages: recent }),
  );
  state.olderMessages = Array.from({ length: 12 }, (_, index) => ({
    ...message(
      Buffer.alloc(16, index + 60).toString('base64url'),
      `Synthetic older message ${index + 1}\nSecond line of synthetic text.`,
    ),
    createdAt: '2026-11-13T12:00:00Z',
  }));
  await page.goto('/');
  const log = page.getByRole('log', { name: 'Messages' });
  await expect(log.locator('.message-bubble')).toHaveCount(25);
  await log.evaluate((view) => {
    view.scrollTop = 0;
    view.dispatchEvent(new Event('scroll'));
  });
  const first = page.getByText(recent[0].text, { exact: true });
  const before = (await first.boundingBox())!.y;
  await log
    .getByRole('button', { name: 'Load older messages', exact: true })
    .click();
  await expect(log.locator('.message-bubble')).toHaveCount(37);
  await expect
    .poll(async () => (await first.boundingBox())!.y)
    .toBeCloseTo(before, 0);
  expect(errors).toEqual([]);
});

test('clear history is confirmed, keeps the contact and recovers from storage full', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticMessaging(
    page,
    approved({
      state: 'capacity-reached',
      canSend: false,
      messages: [message()],
    }),
  );
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Clear history…', exact: true })
    .click();
  let dialog = page.getByRole('dialog', { name: 'Clear local history?' });
  await expect(dialog).toContainText(
    'Messages still queued for delivery continue sending until acknowledged.',
  );
  await expect(dialog).toContainText('Unfinished attachments are cancelled.');
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  expect(calls.some((call) => call.operation === 'ClearHistory')).toBe(false);
  await page
    .getByRole('button', { name: 'Clear history…', exact: true })
    .click();
  dialog = page.getByRole('dialog', { name: 'Clear local history?' });
  await dialog
    .getByRole('button', { name: 'Clear history', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole('log', { name: 'Messages' })).not.toContainText(
    incomingText,
  );
  await expect(
    page.getByRole('textbox', { name: 'Message', exact: true }),
  ).toBeEnabled();
  expect(
    calls
      .filter((call) => call.operation === 'ClearHistory')
      .map((call) => call.args),
  ).toEqual([[contactID]]);
  expect(
    calls.some((call) =>
      ['ForgetContact', 'LockStorage', 'ConnectContact'].includes(
        call.operation,
      ),
    ),
  ).toBe(false);
  expect(errors).toEqual([]);
  expect(external).toEqual([]);
});

test('exhausted route handover explains the problem and permits explicit retry', async ({
  page,
}) => {
  const { calls, errors } = await syntheticMessaging(
    page,
    approved({
      state: 'route-unavailable',
      canConnect: true,
      canSend: true,
      messages: [message()],
    }),
  );
  await page.goto('/');
  await expect(page.getByRole('alert')).toContainText(
    'The approved connection route could not be reached.',
  );
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    incomingText,
  );
  await page.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect
    .poll(() =>
      calls
        .filter((call) => call.operation === 'ConnectContact')
        .map((call) => call.args),
    )
    .toEqual([[contactID]]);
  expect(errors).toEqual([]);
});

test('deleting one local message preserves the other messages and selected contact', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticMessaging(
    page,
    approved({
      messages: [message(), message(nextID, 'Another synthetic message')],
    }),
  );
  await page.goto('/');
  await page
    .getByRole('article', { name: 'Contact message' })
    .filter({ hasText: incomingText })
    .getByRole('button', { name: 'Delete local message', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Delete this local message?',
  });
  await dialog
    .getByRole('button', { name: 'Delete local message', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole('log', { name: 'Messages' })).not.toContainText(
    incomingText,
  );
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    'Another synthetic message',
  );
  expect(
    calls
      .filter((call) => call.operation === 'DeleteMessage')
      .map((call) => call.args),
  ).toEqual([[contactID, firstID]]);
  expect(calls.some((call) => call.operation === 'ForgetContact')).toBe(false);
  expect(errors).toEqual([]);
  expect(external).toEqual([]);
});

for (const state of ['authentication-failed', 'update-required'] as const) {
  test(`${state} explains authentication without losing local history`, async ({
    page,
  }) => {
    const { calls, errors, external } = await syntheticMessaging(
      page,
      approved({
        state,
        canConnect: state === 'update-required',
        messages: [message(firstID, incomingText, 'outgoing', false)],
      }),
    );
    await page.goto('/');
    const warning = page.getByRole('alert');
    await expect(warning).toContainText(
      state === 'update-required'
        ? 'Update required: update both devices to the latest WhatsUp version'
        : 'The saved contact could not authenticate.',
    );
    await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
      incomingText,
    );
    await expect(page.getByText('Queued', { exact: true })).toBeVisible();
    expect(calls.some((call) => call.operation === 'ConnectContact')).toBe(
      false,
    );
    if (state === 'update-required') {
      await page.getByRole('button', { name: 'Connect', exact: true }).click();
      await expect
        .poll(
          () =>
            calls.filter((call) => call.operation === 'ConnectContact').length,
        )
        .toBe(1);
    } else {
      await expect(warning).toContainText('WhatsUp will retry');
    }
    expect(errors).toEqual([]);
    expect(external).toEqual([]);
  });
}

for (const state of ['capacity-reached', 'peer-capacity-reached'] as const) {
  test(`${state} explains why delivery stopped while retaining queued text`, async ({
    page,
  }) => {
    const { calls, errors, external } = await syntheticMessaging(
      page,
      approved({
        state,
        canSend: false,
        canConnect: false,
        messages: [message(firstID, incomingText, 'outgoing', false)],
      }),
    );
    await page.goto('/');
    const warning = page.getByRole('alert');
    await expect(warning).toContainText(
      state === 'capacity-reached'
        ? "This device's local message storage is full."
        : "Your contact's message storage is full.",
    );
    await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
      incomingText,
    );
    await expect(page.getByText('Queued', { exact: true })).toBeVisible();
    await expect(
      page.getByRole('textbox', { name: 'Message', exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByRole('button', { name: 'Send message', exact: true }),
    ).toBeDisabled();
    expect(
      calls.some((call) =>
        ['ConnectContact', 'SendMessage'].includes(call.operation),
      ),
    ).toBe(false);
    expect(errors).toEqual([]);
    expect(external).toEqual([]);
  });
}

test('a cold-start approved contact can read history and queue while offline without connecting', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  await page.goto('/');
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    incomingText,
  );
  await expect(
    page.getByRole('button', { name: 'Connect', exact: true }),
  ).toBeEnabled();
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  await expect(composer).toBeEnabled();
  await composer.fill('Synthetic offline queued text');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(
    page.getByText('Synthetic offline queued text', { exact: true }),
  ).toBeVisible();
  await expect(page.getByText('Queued', { exact: true })).toBeVisible();
  await expect(composer).toHaveValue('');
  expect(calls.find((call) => call.operation === 'SendMessage')?.args).toEqual([
    contactID,
    'Synthetic offline queued text',
  ]);
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('Connect keeps retrying until the saved contact arrives without another invitation or approval popup', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(page);
  state.pairing = {
    ...unavailablePairing,
    state: 'reconnect-required',
    requestID: 'A'.repeat(22),
    localFingerprint,
    peerFingerprint,
  };
  await nativeHint(page, 'whatsup:pairing-changed', { revision: Date.now() });
  await page.goto('/');
  await page.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect(
    page.getByText('Waiting for their device.', { exact: false }),
  ).toBeVisible();
  expect(
    calls.find((call) => call.operation === 'ConnectContact')?.args,
  ).toEqual([contactID]);
  await expect(page.getByRole('button', { name: 'Discard setup' })).toHaveCount(
    0,
  );
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  state.conversation = approved({
    state: 'connected',
    revision: 3,
    canConnect: false,
    messages: [message()],
  });
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    incomingText,
  );
  await expect(page.locator('.conversation-header')).toContainText('Connected');
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  expect(
    calls.filter((call) => call.operation === 'ConnectContact'),
  ).toHaveLength(1);
  expect(errors).toEqual([]);
});

test('an unfinished saved record cannot reveal history or send before fresh authentication', async ({
  page,
}) => {
  const { state, errors } = await syntheticMessaging(
    page,
    approved({ canSend: false }),
  );
  await page.goto('/');
  await expect(
    page.getByRole('textbox', { name: 'Message', exact: true }),
  ).toBeDisabled();
  await expect(page.getByRole('log', { name: 'Messages' })).not.toContainText(
    incomingText,
  );
  await page.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect(
    page.getByText('Waiting for their device.', { exact: false }),
  ).toBeVisible();
  state.conversation = approved({
    state: 'connected',
    revision: 3,
    canConnect: false,
    messages: [message()],
  });
  await expect(
    page.getByRole('textbox', { name: 'Message', exact: true }),
  ).toBeEnabled();
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    incomingText,
  );
  expect(errors).toEqual([]);
});

test('an authenticated acknowledgement updates one queued message and older polls cannot undo it', async ({
  page,
}) => {
  const queued = message(firstID, 'Synthetic outbound text', 'outgoing', false);
  const { state, errors } = await syntheticMessaging(
    page,
    approved({ messages: [queued] }),
  );
  await page.goto('/');
  await expect(page.getByText('Queued', { exact: true })).toBeVisible();
  let release!: () => void;
  state.holdPoll = () => new Promise<void>((resolve) => (release = resolve));
  await expect.poll(() => state.pollHeld).toBe(true);
  state.connect = async () =>
    approved({
      state: 'connected',
      revision: 3,
      canConnect: false,
      messages: [{ ...queued, delivered: true }],
    });
  await page.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect(page.getByText('Delivered', { exact: true })).toBeVisible();
  release();
  await expect(page.getByText('Queued', { exact: true })).toHaveCount(0);
  await expect(page.getByRole('article', { name: 'Your message' })).toHaveCount(
    1,
  );
  await expect(page.getByText('Delivered', { exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test('Go offline clears messages and draft and rejects a send result arriving afterward', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  let release!: () => void;
  let releaseLock!: () => void;
  state.lock = () => new Promise<void>((resolve) => (releaseLock = resolve));
  state.send = async (text) => {
    await new Promise<void>((resolve) => (release = resolve));
    return approved({
      revision: 2,
      messages: [message(), message(nextID, text, 'outgoing', false)],
    });
  };
  await page.goto('/');
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Synthetic in-flight draft');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'SendMessage'))
    .toBe(true);
  await page.getByRole('button', { name: 'Go offline', exact: true }).click();
  await expect
    .poll(() => calls.some((call) => call.operation === 'LockStorage'))
    .toBe(true);
  // Plaintext disappears before the backend finishes closing its connection
  // and vault, not merely after the Lock RPC has returned.
  await expect(page.getByText(incomingText, { exact: true })).toHaveCount(0);
  releaseLock();
  await expect(
    page.getByRole('button', { name: 'Go online', exact: true }),
  ).toBeVisible();
  release();
  await expect(page.getByText(incomingText, { exact: true })).toHaveCount(0);
  await expect(
    page.getByText('Synthetic in-flight draft', { exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole('textbox', {
      name: 'Message unavailable without an approved contact',
    }),
  ).toHaveValue('');
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('a failed queue save preserves the draft and displays only a fixed error', async ({
  page,
}) => {
  const { state, errors } = await syntheticMessaging(page);
  state.sendFailure = true;
  await page.goto('/');
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  await composer.fill('Synthetic retryable draft');
  await composer.press('Enter');
  await expect(page.getByRole('alert')).toContainText(
    'The message could not be saved to the local queue.',
  );
  await expect(composer).toHaveValue('Synthetic retryable draft');
  await expect(page.getByText('SYNTHETIC-PRIVATE-STORAGE-FAILURE')).toHaveCount(
    0,
  );
  await expect(page.getByRole('article', { name: 'Your message' })).toHaveCount(
    0,
  );
  expect(errors).toEqual([]);
});

for (const entry of ['header', 'settings'] as const) {
  test(`explicit Go online from ${entry} resumes the saved contact once`, async ({
    page,
  }) => {
    const { state, calls, errors } = await syntheticMessaging(page);
    state.storage = 'locked';
    await page.goto('/');
    await expect(
      page.getByText('Local storage locked', { exact: true }),
    ).toBeVisible();
    expect(calls.some((call) => call.operation === 'ConnectContact')).toBe(
      false,
    );
    if (entry === 'settings')
      await page
        .getByRole('button', { name: 'Open settings', exact: true })
        .click();
    const surface =
      entry === 'settings'
        ? page.getByRole('region', { name: 'Settings', exact: true })
        : page.locator('.conversation-header');
    await surface
      .getByRole('button', { name: 'Go online', exact: true })
      .click();
    await expect
      .poll(
        () => calls.filter((call) => call.operation === 'UnlockStorage').length,
      )
      .toBe(1);
    if (entry === 'settings')
      await surface
        .getByRole('button', { name: 'Back to chats', exact: true })
        .click();
    await expect(page.locator('.conversation-header')).toContainText(
      'Waiting for their device',
    );
    await expect
      .poll(
        () => calls.filter((call) => call.operation === 'GetContacts').length,
      )
      .toBeGreaterThan(1);
    expect(
      calls.filter((call) => call.operation === 'UnlockStorage'),
    ).toHaveLength(1);
    expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
      [],
    );
    const firstUnlock = calls.findIndex(
      (call) => call.operation === 'UnlockStorage',
    );
    const firstConversation = calls.findIndex(
      (call) => call.operation === 'GetContacts',
    );
    expect(firstConversation).toBeGreaterThan(firstUnlock);
    expect(errors).toEqual([]);
  });
}

test('a contact arriving through an event hint never starts a new connection from the frontend', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(page, {
    ...unavailableConversation,
    state: 'none',
  });
  await page.goto('/');
  await expect(
    page.getByRole('button', { name: 'Start a conversation', exact: true }),
  ).toBeEnabled();
  state.conversation = approved({ revision: 2 });
  await expect(page.locator('.approved-contact')).toBeVisible();
  await expect
    .poll(() => calls.filter((call) => call.operation === 'GetContacts').length)
    .toBeGreaterThan(1);
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  expect(errors).toEqual([]);
});

test('locking during Go online invalidates its delayed saved-contact response', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(page);
  state.storage = 'locked';
  let release!: () => void;
  state.holdPoll = () => new Promise<void>((resolve) => (release = resolve));
  await page.goto('/');
  await page.getByRole('button', { name: 'Go online', exact: true }).click();
  await expect.poll(() => state.pollHeld).toBe(true);
  await page.getByRole('button', { name: 'Go offline', exact: true }).click();
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  release();
  await expect(page.locator('.approved-contact')).toHaveCount(0);
  await expect(
    page.getByRole('textbox', {
      name: 'Message unavailable without an approved contact',
      exact: true,
    }),
  ).toBeDisabled();
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  expect(errors).toEqual([]);
});

test('forget contact defaults to Cancel and Escape and Cancel never call deletion', async ({
  page,
}) => {
  const { calls, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Forget contact and delete conversation',
    exact: true,
  });
  await expect(
    dialog.getByRole('button', { name: 'Cancel', exact: true }),
  ).toBeFocused();
  await expect(dialog).toContainText('Your device identity stays the same.');
  await expect(dialog).toContainText(
    "The other person's history is untouched.",
  );
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  await page
    .getByRole('region', { name: 'Settings', exact: true })
    .getByRole('button', { name: 'Contact', exact: true })
    .click();
  await page
    .getByRole('region', { name: 'Settings', exact: true })
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  await expect(
    page.getByRole('region', { name: 'Settings', exact: true }),
  ).not.toBeVisible();
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByText(incomingText, { exact: true })).toBeVisible();
  expect(calls.filter((call) => call.operation === 'ForgetContact')).toEqual(
    [],
  );
  expect(errors).toEqual([]);
});

test('a changed contact invalidates the open forget confirmation instead of deleting its replacement', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(page);
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Forget contact and delete conversation',
    exact: true,
  });
  await expect(dialog).toBeVisible();
  state.conversation = approved({
    contactID: nextID,
    peerFingerprint: 'c'.repeat(64),
    revision: 2,
  });
  await expect(dialog).not.toBeVisible();
  await expect(page.locator('.approved-contact code')).toHaveText(
    `${'c'.repeat(12)}…`,
  );
  expect(calls.filter((call) => call.operation === 'ForgetContact')).toEqual(
    [],
  );
  expect(errors).toEqual([]);
});

test('confirmed forget uses the exact contact, clears memory before completion and rejects old history before a fresh invitation', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  let releasePoll!: () => void;
  let releaseForget!: () => void;
  await page.goto('/');
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Synthetic draft to forget');
  state.holdPoll = () =>
    new Promise<void>((resolve) => (releasePoll = resolve));
  await expect.poll(() => state.pollHeld).toBe(true);
  state.forget = () =>
    new Promise<void>((resolve) => (releaseForget = resolve));
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Forget contact and delete conversation',
    exact: true,
  });
  await dialog
    .getByRole('button', {
      name: 'Forget contact and delete conversation',
      exact: true,
    })
    .click();
  await expect
    .poll(
      () => calls.filter((call) => call.operation === 'ForgetContact').length,
    )
    .toBe(1);
  await expect(page.getByText(incomingText, { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole('textbox', {
      name: 'Message unavailable without an approved contact',
      exact: true,
    }),
  ).toHaveValue('');
  await expect(
    dialog.getByRole('button', { name: 'Forgetting…', exact: true }),
  ).toBeDisabled();
  releaseForget();
  await expect(dialog).not.toBeVisible();
  releasePoll();
  await expect(
    page.getByRole('button', { name: 'Start a conversation', exact: true }),
  ).toBeEnabled();
  await expect
    .poll(() => calls.filter((call) => call.operation === 'GetContacts').length)
    .toBeGreaterThan(2);
  await expect(page.getByText(incomingText, { exact: true })).toHaveCount(0);
  await expect(page.locator('.approved-contact')).toHaveCount(0);
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  expect(calls.filter((call) => call.operation === 'ForgetContact')).toEqual([
    { operation: 'ForgetContact', args: [contactID] },
  ]);
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  await page
    .getByRole('button', { name: 'Start a conversation', exact: true })
    .click();
  await expect(
    page.getByRole('dialog', { name: 'Start a conversation', exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('textbox', {
      name: 'Connection service HTTPS URL',
      exact: true,
    }),
  ).toHaveValue('');
  expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
  expect(errors).toEqual([]);
});

test('a Forget failure before mutation restores the unchanged unlocked contact from a fresh backend read', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  state.forgetUnchangedFailure = true;
  await page.goto('/');
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Discarded uncertain-removal draft');
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Forget contact and delete conversation',
    exact: true,
  });
  await dialog
    .getByRole('button', {
      name: 'Forget contact and delete conversation',
      exact: true,
    })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole('alert')).toContainText(
    'Contact removal could not be confirmed.',
  );
  await expect(
    page.getByText('Local storage unlocked', { exact: true }),
  ).toBeVisible();
  await expect(page.locator('.approved-contact')).toBeVisible();
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(
    incomingText,
  );
  await expect(
    page.getByRole('textbox', { name: 'Message', exact: true }),
  ).toHaveValue('');
  expect(
    calls.filter((call) => call.operation === 'GetContacts').length,
  ).toBeGreaterThan(1);
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  // Retrying the confirmed action remains possible without locking/reopening.
  state.forgetUnchangedFailure = false;
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  await dialog
    .getByRole('button', {
      name: 'Forget contact and delete conversation',
      exact: true,
    })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(page.locator('.approved-contact')).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('forget failure shows a fixed error, clears the draft and verifies locked storage without a false success', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  state.forgetFailure = true;
  await page.goto('/');
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Synthetic uncertain deletion draft');
  await page
    .getByRole('button', { name: 'Forget contact…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Forget contact and delete conversation',
    exact: true,
  });
  await dialog
    .getByRole('button', {
      name: 'Forget contact and delete conversation',
      exact: true,
    })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole('alert')).toContainText(
    'Contact removal could not be confirmed.',
  );
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  await expect(page.getByText(incomingText, { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole('textbox', {
      name: 'Message unavailable without an approved contact',
      exact: true,
    }),
  ).toHaveValue('');
  await expect(page.getByText('SYNTHETIC-PRIVATE-REMOVAL-FAILURE')).toHaveCount(
    0,
  );
  expect(
    calls.filter((call) => call.operation === 'ForgetContact'),
  ).toHaveLength(1);
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  expect(errors).toEqual([]);
});

test('Unicode messages preserve emoji and shaping while isolating direction from delivery labels', async ({
  page,
}) => {
  const incoming = 'می\u200cروم 👨\u200d👩\u200d👧\u200d👦 ❤️\u200d🔥';
  const outgoing =
    'می\u200cروم क्\u200dष co\u00adoperate 🏴\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}';
  const { calls, errors, external } = await syntheticMessaging(
    page,
    approved({ messages: [message(firstID, incoming)] }),
  );
  await page.goto('/');
  const received = page
    .getByRole('article', { name: 'Contact message', exact: true })
    .locator('.message-text');
  await expect(received).toHaveText(incoming);
  await expect(received).toHaveAttribute('dir', 'auto');
  await expect(received).toHaveCSS('direction', 'rtl');
  await expect(received).toHaveCSS('unicode-bidi', 'isolate');
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  await composer.fill(outgoing);
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  const sent = page.getByRole('article', { name: 'Your message', exact: true });
  await expect(sent.locator('.message-text')).toHaveText(outgoing);
  await expect(sent.locator('.message-text')).toHaveCSS('direction', 'rtl');
  await expect(sent.locator('.message-text')).toHaveCSS(
    'unicode-bidi',
    'isolate',
  );
  await expect(sent.getByText('Queued', { exact: true })).toHaveCSS(
    'direction',
    'ltr',
  );
  expect(calls.find((call) => call.operation === 'SendMessage')?.args).toEqual([
    contactID,
    outgoing,
  ]);
  await expect(page.locator('.conversation-header')).toHaveCSS(
    'direction',
    'ltr',
  );
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('hidden emoji tags cannot be sent outside recognized complete flags', async ({
  page,
}) => {
  const { calls, errors, external } = await syntheticMessaging(page);
  await page.goto('/');
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  const send = page.getByRole('button', { name: 'Send message', exact: true });
  for (const text of [
    'Synthetic\u{e0061}hidden text',
    'Synthetic 🏴\u{e0073}\u{e0065}\u{e0063}\u{e0072}\u{e0065}\u{e0074}\u{e007f}',
    'Incomplete 🏴\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}',
    'Trailing 🏴\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}\u{e0061}',
  ]) {
    await composer.fill(text);
    await expect(send).toBeDisabled();
  }
  expect(calls.some((call) => call.operation === 'SendMessage')).toBe(false);
  const valid =
    'Flags 🏴\u{e0067}\u{e0062}\u{e0073}\u{e0063}\u{e0074}\u{e007f} 🏴\u{e0067}\u{e0062}\u{e0077}\u{e006c}\u{e0073}\u{e007f}';
  await composer.fill(valid);
  await expect(send).toBeEnabled();
  await send.click();
  await expect(
    page
      .getByRole('article', { name: 'Your message', exact: true })
      .locator('.message-text'),
  ).toHaveText(valid);
  expect(calls.find((call) => call.operation === 'SendMessage')?.args).toEqual([
    contactID,
    valid,
  ]);
  expect(errors).toEqual([]);
  expect(external).toEqual([]);
});

test('message HTML is inert text and never loads a remote asset', async ({
  page,
}) => {
  const text =
    '<img src="https://example.invalid/private.png" onerror="alert(1)">';
  const { external, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message(firstID, text)] }),
  );
  await page.goto('/');
  await expect(page.getByRole('log', { name: 'Messages' })).toContainText(text);
  await expect(page.locator('img')).toHaveCount(0);
  expect(external).toEqual([]);
  expect(errors).toEqual([]);
});

test('invalid conversation DTOs close the composer and remove previously displayed history', async ({
  page,
}) => {
  const { state, errors } = await syntheticMessaging(
    page,
    approved({ messages: [message()] }),
  );
  await page.goto('/');
  await expect(page.getByText(incomingText, { exact: true })).toBeVisible();
  state.conversation = {
    ...approved({ revision: 2 }),
    messages: [message(firstID, 'x'.repeat(16 * 1024 + 1))],
  };
  await expect(page.getByRole('alert')).toContainText(
    'Conversation history could not be verified.',
  );
  await expect(page.getByText(incomingText, { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole('button', { name: 'Send message', exact: true }),
  ).toBeDisabled();
  expect(errors).toEqual([]);
});

test('an identity verification failure stops connecting without replacing or reapproving the saved contact', async ({
  page,
}) => {
  const { state, calls, errors } = await syntheticMessaging(
    page,
    approved({ state: 'connecting', canConnect: false }),
  );
  await page.goto('/');
  await expect(
    page.getByText('Waiting for their device.', { exact: false }),
  ).toBeVisible();
  state.conversation = approved({
    state: 'identity-changed',
    revision: 2,
    canConnect: false,
  });
  await expect(page.getByRole('alert')).toHaveText(
    "Your contact's identity could not be verified. The connection stopped; your saved contact was not changed.",
  );
  await expect(
    page.getByRole('button', { name: 'Connect', exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole('dialog', { name: 'Pairing request' }),
  ).toHaveCount(0);
  await expect(page.locator('.approved-contact code')).toHaveText(
    `${peerFingerprint.slice(0, 12)}…`,
  );
  // A saved draft can still be durably queued locally, but it cannot cause a
  // new connection attempt or implicitly approve the rejected peer.
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .fill('Synthetic private text kept offline');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.getByText('Queued', { exact: true })).toBeVisible();
  expect(calls.filter((call) => call.operation === 'ConnectContact')).toEqual(
    [],
  );
  expect(errors).toEqual([]);
});
