import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import { unavailableConversation } from '../src/lib/messaging';
import {
  contactSummary,
  contactHint,
  messagePage,
  serveDefaultAppPreferences,
} from './contact-events';
import type {
  AttachmentDraft,
  AttachmentItem,
} from '../src/lib/state/attachments';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const secondContactID = 'BAQEBAQEBAQEBAQEBAQEBA';
const fileID = 'AgICAgICAgICAgICAgICAg';
const token = 'S'.repeat(32);
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const operations = new Map<number, string>();
for (const match of generated.matchAll(
  /export function (\w+)\([\s\S]*?\$Call\.ByID\((\d+)/g,
))
  operations.set(Number(match[2]), match[1]);
const draft: AttachmentDraft = {
  handle: token,
  name: 'Synthetic large.pdf',
  mediaType: 'application/pdf',
  size: 140 * 2 ** 20,
  previewURL: '',
  requiresRelayWarning: true,
};
const item: AttachmentItem = {
  id: fileID,
  name: 'Synthetic received.pdf',
  mediaType: 'application/pdf',
  size: 42,
  direction: 'incoming',
  state: 'offered',
  transferredBytes: 0,
  previewURL: '',
  warning: '',
};

// This fixture exercises generated Wails bindings and the browser UI. Native
// file access, OS dialogs and transport are verified separately in Go/native QA.
async function desktop(
  page: Page,
  initial: AttachmentItem[] = [],
  peer: 'offline' | 'connected' | 'peer-removed' = 'offline',
  protocol: 0 | 3 | 4 = 0,
  second?: { items: AttachmentItem[]; holdTarget?: () => Promise<void> },
) {
  const calls: { name: string; args: unknown[] }[] = [],
    errors: string[] = [],
    external: string[] = [];
  const state = {
    revision: 1,
    storage: 'unlocked',
    items: initial,
    drafts: [] as AttachmentDraft[],
    autoAcceptImages: false,
    peer,
    protocol,
  };
  let draftSequence = 0;
  const nextDraftHandle = () =>
    String.fromCharCode(83 + draftSequence++).repeat(32);
  const conversation = (selected = contactID) => ({
    ...unavailableConversation,
    state: state.peer,
    path: 'Relayed' as const,
    revision: state.revision,
    contactID: selected,
    peerFingerprint: (selected === contactID ? 'b' : 'c').repeat(64),
    localName: second
      ? selected === contactID
        ? 'Synthetic first'
        : 'Synthetic second'
      : '',
    peerProtocol: state.protocol,
    attachmentsSupported: state.peer === 'connected' && state.protocol === 4,
    contactControlsSupported:
      state.peer === 'connected' && state.protocol === 4,
    canSend: state.peer !== 'peer-removed',
    canConnect: state.peer === 'offline',
  });
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:9245/'))
      external.push(request.url());
  });
  await page.route('**/local-media/*', (route) =>
    route.fulfill({
      status: 200,
      headers: {
        'content-type': 'video/mp4',
        'x-content-type-options': 'nosniff',
        'content-security-policy': "default-src 'none'; sandbox",
      },
      body: Buffer.from('synthetic video placeholder'),
    }),
  );
  await page.route('**/wails/runtime', async (route) => {
    if (await serveDefaultAppPreferences(route)) return;
    const body = route.request().postDataJSON(),
      name = operations.get(body.args?.methodID),
      args: unknown[] = body.args?.args ?? [];
    if (!name || body.object !== 0 || body.method !== 0)
      throw new Error('Unexpected synthetic desktop operation');
    calls.push({ name, args });
    let result: unknown = null;
    switch (name) {
      case 'GetStatus':
      case 'UnlockStorage':
      case 'LockStorage':
        if (name === 'LockStorage') state.storage = 'locked';
        if (name === 'UnlockStorage') state.storage = 'unlocked';
        result = {
          ...unavailableStatus,
          storageState: state.storage,
          identityFingerprint:
            state.storage === 'unlocked' ? 'a'.repeat(64) : '',
          messagingAvailable: state.storage === 'unlocked',
          pairingAvailable: state.storage === 'unlocked',
          attachmentsAvailable: state.storage === 'unlocked',
        };
        break;
      case 'GetContacts':
        result =
          state.storage === 'unlocked'
            ? [
                contactSummary(conversation()),
                ...(second
                  ? [contactSummary(conversation(secondContactID))]
                  : []),
              ]
            : [];
        break;
      case 'GetMessages':
        result = messagePage(conversation(args[0] as string));
        break;
      case 'MarkRead':
        result = contactSummary(conversation(args[0] as string));
        break;
      case 'GetProfile':
        result = { displayName: '', shareDisplayName: false };
        break;
      case 'GetPairingStatus':
        result = { ...unavailablePairing, state: 'idle' };
        break;
      case 'SetAttachmentInputContact':
        if (args[0] === secondContactID) await second?.holdTarget?.();
        break;
      case 'GetAttachments':
      case 'GetAttachmentHistory':
        result = {
          contactID: args[0],
          revision: state.revision,
          items:
            args[0] === secondContactID ? (second?.items ?? []) : state.items,
          drafts: args[0] === secondContactID ? [] : state.drafts,
          autoAcceptImages:
            args[0] === secondContactID ? false : state.autoAcceptImages,
          nextCursor: '',
          hasMore: false,
        };
        break;
      case 'ChooseAttachment':
        state.drafts = [{ ...draft, handle: nextDraftHandle() }];
        state.revision++;
        result = state.drafts[0];
        break;
      case 'PasteAttachment':
        state.drafts = [
          {
            ...draft,
            handle: nextDraftHandle(),
            name: 'pasted-image.png',
            mediaType: 'image/png',
            size: 100,
            requiresRelayWarning: false,
          },
        ];
        state.revision++;
        result = state.drafts[0];
        break;
      case 'DiscardAttachment':
        state.drafts = [];
        state.revision++;
        break;
      case 'SendAttachment':
        state.drafts = [];
        state.items = [
          {
            ...item,
            name: draft.name,
            size: draft.size,
            direction: 'outgoing',
            state: 'queued',
          },
        ];
        state.revision++;
        break;
      case 'AcceptAttachment':
        state.items = state.items.map((value) => ({
          ...value,
          state: 'complete',
          transferredBytes: value.size,
        }));
        state.revision++;
        break;
      case 'DeclineAttachment':
        state.items = [];
        state.revision++;
        break;
      case 'SetAutoAcceptImages':
        state.autoAcceptImages = args[1] as boolean;
        break;
      case 'SaveAttachment':
        result = { handle: token };
        break;
      case 'ShowSavedAttachment':
        break;
      case 'PreviewAttachment':
        result = '/local-media/' + token;
        break;
      default:
        throw new Error('Unhandled synthetic desktop operation: ' + name);
    }
    await route.fulfill({ json: result });
  });
  await page.addInitScript(() => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'linux', Arch: 'amd64', Debug: false };
  });
  await page.goto('/');
  await expect(
    page.getByRole('region', { name: 'Attachments', exact: true }),
  ).toBeVisible();
  return { state, calls, errors, external };
}

async function expandAttachments(page: Page) {
  await page
    .getByRole('button', { name: 'Expand attachments', exact: true })
    .click();
  await expect(
    page.getByRole('button', { name: 'Collapse attachments', exact: true }),
  ).toHaveAttribute('aria-expanded', 'true');
}

async function attachmentUpdate(page: Page, revision: number) {
  const loaded = page.waitForResponse((response) => {
    if (!response.url().endsWith('/wails/runtime')) return false;
    return (
      operations.get(response.request().postDataJSON().args?.methodID) ===
      'GetAttachments'
    );
  });
  await contactHint(page, contactID, revision);
  await (await loaded).finished();
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
      }),
  );
}

test('file selection remains a draft and a large relayed send requires explicit confirmation', async ({
  page,
}) => {
  const { calls, errors, external } = await desktop(page);
  await expect(
    page.getByRole('button', { name: 'Expand attachments', exact: true }),
  ).toHaveAttribute('aria-expanded', 'false');
  await page
    .getByRole('button', { name: 'Choose attachment', exact: true })
    .click();
  await expect(
    page.getByRole('article', { name: 'Attachment preview' }),
  ).toContainText(draft.name);
  expect(calls.filter((call) => call.name === 'SendAttachment')).toEqual([]);
  await expect(
    page.getByRole('button', { name: 'Send file', exact: true }),
  ).toBeDisabled();
  await page
    .getByRole('checkbox', {
      name: 'This large transfer may use relay bandwidth.',
    })
    .check();
  await page.getByRole('button', { name: 'Send file', exact: true }).click();
  await expect(
    page.getByRole('article', { name: 'Sent attachment' }),
  ).toContainText('queued');
  expect(calls.find((call) => call.name === 'SendAttachment')?.args).toEqual([
    contactID,
    token,
    true,
  ]);
  expect(errors).toEqual([]);
  expect(external).toEqual([]);
});

test('receiving requires approval and only Save and Show in folder are offered', async ({
  page,
}) => {
  const { calls, errors } = await desktop(page, [item]);
  await expect(
    page.getByRole('button', { name: 'Save…', exact: true }),
  ).toHaveCount(0);
  await page.getByRole('button', { name: 'Accept', exact: true }).click();
  await expect(
    page.getByRole('button', { name: 'Collapse attachments', exact: true }),
  ).toHaveAttribute('aria-expanded', 'true');
  await expect(
    page.getByRole('button', { name: 'Save…', exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Show in folder', exact: true }),
  ).toHaveCount(0);
  await page.getByRole('button', { name: 'Save…', exact: true }).click();
  await page
    .getByRole('button', { name: 'Show in folder', exact: true })
    .click();
  await expect
    .poll(() => calls.find((call) => call.name === 'ShowSavedAttachment')?.args)
    .toEqual([contactID, token]);
  await expect(
    page.getByRole('button', { name: 'Open', exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole('checkbox', { name: 'Automatically accept images' }),
  ).not.toBeChecked();
  expect(errors).toEqual([]);
});

test('saved attachment history starts collapsed and its keyboard toggle preserves Save access and data', async ({
  page,
}) => {
  const completed = {
    ...item,
    state: 'complete' as const,
    transferredBytes: item.size,
  };
  const { state, calls, errors } = await desktop(page, [completed]);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  const toggle = panel.locator('.attachment-toggle');
  await expect(toggle).toContainText('1 file');
  await expect(toggle).toHaveAccessibleName('Expand attachments');
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  const contentID = await toggle.getAttribute('aria-controls');
  expect(contentID).toBeTruthy();
  await expect(panel.locator('.attachment-content')).toHaveAttribute(
    'id',
    contentID!,
  );
  await expect(panel.locator('.attachment-content')).toBeHidden();
  await expect(panel.getByRole('article')).toHaveCount(0);
  expect((await panel.boundingBox())!.height).toBeLessThan(80);

  await toggle.focus();
  await page.keyboard.press('Enter');
  await expect(toggle).toHaveAccessibleName('Collapse attachments');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(
    panel.getByRole('button', { name: 'Save…', exact: true }),
  ).toBeVisible();
  await page.keyboard.press('Space');
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await expect(panel.locator('.attachment-content')).toBeHidden();
  await expect(toggle).toHaveAttribute('aria-controls', contentID!);
  expect(state.items).toEqual([completed]);
  expect(
    calls.some((call) =>
      ['SaveAttachment', 'DeclineAttachment', 'DiscardAttachment'].includes(
        call.name,
      ),
    ),
  ).toBe(false);

  await page.keyboard.press('Enter');
  await panel.getByRole('button', { name: 'Save…', exact: true }).click();
  await expect(
    panel.getByRole('button', { name: 'Show in folder', exact: true }),
  ).toBeVisible();
  expect(state.items).toEqual([completed]);
  expect(errors).toEqual([]);
});

test('manual collapse survives progress and completion while a new incoming offer reopens it', async ({
  page,
}) => {
  const { state, errors } = await desktop(page, [item]);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  const toggle = panel.locator('.attachment-toggle');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');

  for (const [transferState, progress] of [
    ['accepted', 16],
    ['accepted', 32],
    ['complete', item.size],
  ] as const) {
    state.items = [
      { ...item, state: transferState, transferredBytes: progress },
    ];
    await attachmentUpdate(page, ++state.revision);
    await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  }
  state.items = [
    state.items[0],
    {
      ...item,
      id: 'AwMDAwMDAwMDAwMDAwMDAw',
      name: 'Synthetic new offer.pdf',
    },
  ];
  await attachmentUpdate(page, ++state.revision);
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(
    panel.getByRole('button', { name: 'Accept', exact: true }),
  ).toBeVisible();
  await panel.getByRole('button', { name: 'Accept', exact: true }).click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(
    panel.getByRole('button', { name: 'Save…', exact: true }),
  ).toHaveCount(2);
  expect(errors).toEqual([]);
});

test('composer paste and a new file draft reveal previews after manual collapse', async ({
  page,
}) => {
  const { state, calls, errors } = await desktop(page, [
    { ...item, state: 'complete', transferredBytes: item.size },
  ]);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  const toggle = panel.locator('.attachment-toggle');
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await page
    .getByRole('textbox', { name: 'Message', exact: true })
    .evaluate((element) => {
      const data = new DataTransfer();
      data.items.add(
        new File(['synthetic browser paste intent'], 'synthetic.png', {
          type: 'image/png',
        }),
      );
      element.dispatchEvent(
        new ClipboardEvent('paste', {
          clipboardData: data,
          bubbles: true,
          cancelable: true,
        }),
      );
    });
  await expect(
    panel.getByRole('article', { name: 'Attachment preview' }),
  ).toContainText('pasted-image.png');
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  expect(calls.find((call) => call.name === 'PasteAttachment')?.args).toEqual([
    contactID,
  ]);
  await toggle.click();
  state.drafts = state.drafts.map((value) => ({ ...value }));
  await attachmentUpdate(page, ++state.revision);
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await page
    .getByRole('button', { name: 'Choose attachment', exact: true })
    .click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(
    panel.getByRole('article', { name: 'Attachment preview' }),
  ).toContainText(draft.name);
  expect(calls.some((call) => call.name === 'SendAttachment')).toBe(false);
  expect(errors).toEqual([]);
});

test('switching from pending attachments to another contact keeps restored history collapsed and scoped', async ({
  page,
}) => {
  let releaseTarget!: () => void;
  const targetHeld = new Promise<void>((resolve) => {
    releaseTarget = resolve;
  });
  const completed: AttachmentItem = {
    ...item,
    id: 'BQUFBQUFBQUFBQUFBQUFBQ',
    name: 'Synthetic second saved.pdf',
    state: 'complete',
    transferredBytes: item.size,
  };
  const { calls, errors } = await desktop(page, [item], 'offline', 0, {
    items: [completed],
    holdTarget: () => targetHeld,
  });
  await page
    .getByRole('button', { name: 'Choose attachment', exact: true })
    .click();
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  await expect(
    panel.getByRole('article', { name: 'Attachment preview' }),
  ).toBeVisible();
  await expect(
    panel.getByRole('button', { name: 'Accept', exact: true }),
  ).toBeVisible();
  await page
    .getByRole('button', {
      name: 'Conversation with Synthetic second',
      exact: true,
    })
    .click();
  await expect
    .poll(() =>
      calls.some(
        (call) =>
          call.name === 'SetAttachmentInputContact' &&
          call.args[0] === secondContactID,
      ),
    )
    .toBe(true);
  await expect(panel.locator('.attachment-toggle')).toHaveAttribute(
    'aria-expanded',
    'false',
  );
  await expect(panel.locator('.attachment-toggle')).toContainText('0 files');
  await expect(panel.getByRole('article')).toHaveCount(0);
  releaseTarget();
  await expect
    .poll(() =>
      calls.some(
        (call) =>
          call.name === 'GetAttachments' && call.args[0] === secondContactID,
      ),
    )
    .toBe(true);
  await expect(panel.locator('.attachment-toggle')).toContainText('1 file');
  await expect(panel.locator('.attachment-toggle')).toHaveAttribute(
    'aria-expanded',
    'false',
  );
  await expandAttachments(page);
  await expect(
    panel.getByRole('article', { name: 'Received attachment' }),
  ).toContainText(completed.name);
  await expect(
    panel.getByRole('article', { name: 'Attachment preview' }),
  ).toHaveCount(0);
  await expect(
    panel.getByRole('button', { name: 'Accept', exact: true }),
  ).toHaveCount(0);
  await panel.getByRole('button', { name: 'Save…', exact: true }).click();
  await expect
    .poll(() => calls.find((call) => call.name === 'SaveAttachment')?.args)
    .toEqual([secondContactID, completed.id]);
  expect(errors).toEqual([]);
});

test('completed video does not create a player until Play is clicked, and Go offline clears it', async ({
  page,
}) => {
  const { calls, errors } = await desktop(page, [
    {
      ...item,
      name: 'Synthetic video.mp4',
      mediaType: 'video/mp4',
      state: 'complete',
      transferredBytes: item.size,
    },
  ]);
  await expect(page.locator('video')).toHaveCount(0);
  await expandAttachments(page);
  await expect(
    page.getByRole('button', { name: 'Save…', exact: true }),
  ).toBeVisible();
  expect(calls.some((call) => call.name === 'PreviewAttachment')).toBe(false);
  await page.getByRole('button', { name: 'Play video', exact: true }).click();
  await expect(page.locator('video')).toHaveAttribute(
    'src',
    '/local-media/' + token,
  );
  await expect(page.locator('video')).not.toHaveAttribute('autoplay');
  await page.getByRole('button', { name: 'Go offline', exact: true }).click();
  await expect(page.locator('video')).toHaveCount(0);
  await expect(
    page.getByRole('region', { name: 'Attachments', exact: true }),
  ).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('a connected protocol-3 peer blocks file transfer while saved files and offline drafts remain usable', async ({
  page,
}) => {
  const { calls, state, errors } = await desktop(
    page,
    [{ ...item, state: 'complete', transferredBytes: item.size }],
    'connected',
    3,
  );
  await expandAttachments(page);
  await expect(
    page.getByText('Text messaging works with this device.', { exact: false }),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Save…', exact: true }).click();
  await expect(
    page.getByRole('button', { name: 'Show in folder', exact: true }),
  ).toBeVisible();
  await page.getByRole('button', { name: 'Choose file…', exact: true }).click();
  await page
    .getByRole('checkbox', {
      name: 'This large transfer may use relay bandwidth.',
    })
    .check();
  await expect(
    page.getByRole('button', { name: 'Send file', exact: true }),
  ).toBeDisabled();
  expect(calls.some((call) => call.name === 'SendAttachment')).toBe(false);
  state.peer = 'offline';
  state.protocol = 0;
  state.revision++;
  await contactHint(page, contactID, state.revision);
  await expect(
    page.getByRole('button', { name: 'Send file', exact: true }),
  ).toBeEnabled();
  await page.getByRole('button', { name: 'Send file', exact: true }).click();
  await expect(
    page.getByRole('article', { name: 'Sent attachment' }),
  ).toContainText('queued');
  expect(errors).toEqual([]);
});

test('a removed contact keeps completed files saveable and prevents new drafts', async ({
  page,
}) => {
  const { calls, errors } = await desktop(
    page,
    [{ ...item, state: 'complete', transferredBytes: item.size }],
    'peer-removed',
  );
  await expandAttachments(page);
  await expect(
    page.getByRole('button', { name: 'Choose file…', exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole('button', { name: 'Paste image', exact: true }),
  ).toBeDisabled();
  await page.getByRole('button', { name: 'Save…', exact: true }).click();
  await expect(
    page.getByRole('button', { name: 'Show in folder', exact: true }),
  ).toBeVisible();
  expect(calls.some((call) => call.name === 'SaveAttachment')).toBe(true);
  expect(calls.some((call) => call.name === 'ChooseAttachment')).toBe(false);
  expect(errors).toEqual([]);
});
