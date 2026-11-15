import { readFileSync } from 'node:fs';
import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import { unavailableConversation } from '../src/lib/messaging';
import {
  contactSummary,
  contactHint,
  nativeHint,
  messagePage,
  serveDefaultAppPreferences,
  fileDragTakeID,
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
    hasMore: false,
    peer,
    protocol,
    nativeTarget: '',
    dragHint: { active: false, revision: 0 },
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
    if (route.request().postDataJSON().args?.methodID === fileDragTakeID) {
      await route.fulfill({ json: state.dragHint });
      return;
    }
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
        state.nativeTarget = args[0] as string;
        break;
      case 'ClearHistory':
        state.items = [];
        state.drafts = [];
        state.nativeTarget = '';
        state.revision++;
        result = conversation(args[0] as string);
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
          nextCursor: state.hasMore ? '1' : '',
          hasMore: state.hasMore,
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
      case 'DeleteAttachment':
        state.items = state.items.filter((value) => value.id !== args[1]);
        state.revision++;
        break;
      case 'CancelAttachment':
        state.items = state.items.map((value) => ({
          ...value,
          state: 'paused',
        }));
        state.revision++;
        break;
      case 'WithdrawAttachment':
        state.items = state.items.map((value) => ({
          ...value,
          state: 'declined',
          warning: 'cancellation-pending',
        }));
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

test('pause is resumable while cancel permanently withdraws the exact transfer', async ({
  page,
}) => {
  const { calls, errors } = await desktop(
    page,
    [{ ...item, direction: 'outgoing', state: 'queued' }],
    'connected',
    4,
  );
  await expandAttachments(page);
  await page
    .getByRole('button', { name: 'Pause transfer', exact: true })
    .click();
  await expect(
    page.getByRole('button', { name: 'Resume', exact: true }),
  ).toBeVisible();
  await page
    .getByRole('button', { name: 'Cancel transfer', exact: true })
    .click();
  await expect(
    page.getByRole('article', { name: 'Sent attachment' }),
  ).toContainText('declined');
  await expect(
    page.getByRole('status').filter({ hasText: 'Cancelled on this device' }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Resume', exact: true }),
  ).toHaveCount(0);
  expect(
    calls.filter((call) =>
      ['CancelAttachment', 'WithdrawAttachment'].includes(call.name),
    ),
  ).toEqual([
    { name: 'CancelAttachment', args: [contactID, fileID] },
    { name: 'WithdrawAttachment', args: [contactID, fileID] },
  ]);
  expect(errors).toEqual([]);
});

test('full image loads only after View image, supports fit and size controls, and retires on offline', async ({
  page,
}) => {
  let fullLoads = 0;

  const { state, calls, errors } = await desktop(page, [
    {
      ...item,
      state: 'complete',
      mediaType: 'image/png',
      name: 'Synthetic screenshot.png',
      transferredBytes: item.size,
    },
  ]);
  await page.route('**/local-media/*', async (route) => {
    fullLoads++;
    await route.fulfill({
      contentType: 'image/png',
      body: Buffer.from(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ1sAAAAASUVORK5CYII=',
        'base64',
      ),
    });
  });
  await expandAttachments(page);
  expect(fullLoads).toBe(0);
  await page.getByRole('button', { name: 'View image', exact: true }).click();
  const viewer = page.getByRole('dialog', { name: 'Image preview' });
  await expect(viewer).toBeVisible();
  await expect.poll(() => fullLoads).toBe(1);
  await expect(viewer.getByRole('img')).toHaveAttribute(
    'src',
    '/local-media/' + token,
  );
  await viewer
    .getByRole('button', { name: 'Actual size', exact: true })
    .click();
  await expect(
    viewer.getByRole('button', { name: 'Fit image', exact: true }),
  ).toHaveAttribute('aria-pressed', 'true');
  await page.keyboard.press('Escape');
  await expect(viewer).not.toBeVisible();
  await page.getByRole('button', { name: 'View image', exact: true }).click();
  await expect(viewer).toBeVisible();
  state.storage = 'locked';
  await nativeHint(page, 'whatsup:storage-changed', { state: 'locked' });
  await expect(viewer).not.toBeVisible();
  expect(
    calls
      .filter((call) => call.name === 'PreviewAttachment')
      .map((call) => call.args),
  ).toEqual([
    [contactID, fileID],
    [contactID, fileID],
  ]);
  expect(errors).toEqual([]);
});

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

test('tall attachment history uses one scroll area for file actions and older history', async ({
  page,
}) => {
  const completed = Array.from({ length: 12 }, (_, index): AttachmentItem => ({
    ...item,
    id: Buffer.alloc(16, index + 20).toString('base64url'),
    name: `Synthetic saved file ${index + 1}.pdf`,
    state: 'complete',
    transferredBytes: item.size,
  }));
  const { state, calls, errors } = await desktop(page, completed);
  state.hasMore = true;
  await attachmentUpdate(page, ++state.revision);
  await expandAttachments(page);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  const content = panel.locator('.attachment-content');
  const geometry = await panel.evaluate((element) => {
    const content = element.querySelector('.attachment-content')!;
    const list = element.querySelector('.attachment-list')!;
    return {
      panelHeight: element.getBoundingClientRect().height,
      contentOverflows: content.scrollHeight > content.clientHeight,
      listOverflows: list.scrollHeight > list.clientHeight,
      scrollingAreas: [...element.querySelectorAll('*')].filter((child) => {
        const overflow = getComputedStyle(child).overflowY;
        return (
          ['auto', 'scroll'].includes(overflow) &&
          child.scrollHeight > child.clientHeight
        );
      }).length,
    };
  });
  expect(geometry.panelHeight).toBeLessThanOrEqual(330);
  expect(geometry.contentOverflows).toBe(true);
  expect(geometry.listOverflows).toBe(false);
  expect(geometry.scrollingAreas).toBe(1);

  const older = panel.getByRole('button', {
    name: 'Load older attachments',
    exact: true,
  });
  await older.scrollIntoViewIfNeeded();
  await expect(older).toBeInViewport();
  expect(
    await content.evaluate((element) => element.scrollTop),
  ).toBeGreaterThan(0);
  await panel
    .getByRole('button', { name: 'Save…', exact: true })
    .last()
    .click();
  await expect
    .poll(() => calls.find((call) => call.name === 'SaveAttachment')?.args)
    .toEqual([contactID, completed.at(-1)!.id]);
  const choose = panel.getByRole('button', {
    name: 'Choose file…',
    exact: true,
  });
  await choose.scrollIntoViewIfNeeded();
  await expect(choose).toBeInViewport();
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

for (const mediaType of ['video/mp4', 'video/webm']) {
  test(`completed ${mediaType} does not create a player until Play is clicked, and Go offline clears it`, async ({
    page,
  }) => {
    const { calls, errors } = await desktop(page, [
      {
        ...item,
        name: 'Synthetic video.' + (mediaType === 'video/mp4' ? 'mp4' : 'webm'),
        mediaType,
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
}

test('an MP4 offer requires completion while an explicit file-mode MP4 stays save-only', async ({
  page,
}) => {
  const saveOnly = {
    ...item,
    id: 'AwMDAwMDAwMDAwMDAwMDAw',
    name: 'Synthetic original.mp4',
    mediaType: 'application/octet-stream',
    state: 'complete',
    transferredBytes: item.size,
  };
  const { calls, errors } = await desktop(page, [
    { ...item, name: 'Synthetic approved video.mp4', mediaType: 'video/mp4' },
    saveOnly,
  ]);
  await expect(
    page.getByRole('button', { name: 'Play video', exact: true }),
  ).toHaveCount(0);
  await expect(page.locator('video')).toHaveCount(0);
  expect(calls.some((call) => call.name === 'PreviewAttachment')).toBe(false);
  await page.getByRole('button', { name: 'Accept', exact: true }).click();
  await expect(
    page.getByRole('button', { name: 'Play video', exact: true }),
  ).toHaveCount(1);
  const file = page
    .getByRole('article', { name: 'Received attachment' })
    .filter({ hasText: saveOnly.name });
  await expect(
    file.getByRole('button', { name: 'Play video', exact: true }),
  ).toHaveCount(0);
  await expect(
    file.getByRole('button', { name: 'View image', exact: true }),
  ).toHaveCount(0);
  await file.getByRole('button', { name: 'Save…', exact: true }).click();
  await expect
    .poll(() => calls.find((call) => call.name === 'SaveAttachment')?.args)
    .toEqual([contactID, saveOnly.id]);
  expect(calls.some((call) => call.name === 'PreviewAttachment')).toBe(false);
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

test('single attachment deletion needs confirmation, removes only its local card and keeps the contact', async ({
  page,
}) => {
  const { calls, errors } = await desktop(page, [
    { ...item, state: 'complete', transferredBytes: item.size },
  ]);
  await expandAttachments(page);
  await page
    .getByRole('button', { name: 'Delete attachment…', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'Delete this attachment from WhatsUp?',
    exact: true,
  });
  await expect(dialog).toContainText('Copies you saved elsewhere');
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  expect(calls.some((call) => call.name === 'DeleteAttachment')).toBe(false);
  await page
    .getByRole('button', { name: 'Delete attachment…', exact: true })
    .click();
  await dialog
    .getByRole('button', { name: 'Delete from WhatsUp', exact: true })
    .click();
  await expect(dialog).not.toBeVisible();
  await expect(
    page.getByRole('article', { name: 'Received attachment' }),
  ).toHaveCount(0);
  expect(
    calls
      .filter((call) => call.name === 'DeleteAttachment')
      .map((call) => call.args),
  ).toEqual([[contactID, fileID]]);
  await expect(
    page.getByRole('button', { name: 'Go offline', exact: true }),
  ).toBeVisible();
  expect(calls.some((call) => call.name === 'ForgetContact')).toBe(false);
  expect(errors).toEqual([]);
});

test('native drag hints stay within the attachment panel and clear on leave, Settings and lock without selecting any path', async ({
  page,
}) => {
  const { calls, state, errors } = await desktop(page);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  const hint = panel.getByText('Drop a file to prepare an attachment.', {
    exact: true,
  });
  state.dragHint = { active: true, revision: 1 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  await expect(hint).toBeVisible();
  await expect(panel).toHaveAttribute('data-drop-active', 'true');
  await expect(page.locator('.file-drag-highlight')).toHaveCount(0);
  await expect(
    page.getByRole('textbox', { name: 'Message', exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Open settings', exact: true }),
  ).toBeVisible();
  expect(
    await panel.evaluate((node) => getComputedStyle(node).position),
  ).not.toBe('fixed');
  expect(
    calls.some(
      (call) =>
        call.name === 'ChooseAttachment' || call.name === 'SendAttachment',
    ),
  ).toBe(false);
  state.dragHint = { active: false, revision: 2 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  await expect(hint).toHaveCount(0);
  await nativeHint(page, 'whatsup:file-drag', { active: true, revision: 1 });
  await expect(hint).toHaveCount(0);
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  state.dragHint = { active: true, revision: 3 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  await expect(hint).toHaveCount(0);
  await page
    .getByRole('button', { name: 'Back to chats', exact: true })
    .click();
  state.dragHint = { active: true, revision: 4 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  await expect(hint).toBeVisible();
  state.storage = 'locked';
  await nativeHint(page, 'whatsup:storage-changed', { state: 'locked' });
  await expect(hint).toHaveCount(0);
  state.dragHint = { active: true, revision: 5 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  await expect(hint).toHaveCount(0);
  expect(errors).toEqual([]);
});

test('Clear history rebinds native drop input for the same selected contact and its staged draft appears through a hint', async ({
  page,
}) => {
  const { state, calls, errors } = await desktop(page, [item], 'connected', 4);
  await expect.poll(() => state.nativeTarget).toBe(contactID);
  const before = calls.filter(
    (call) => call.name === 'SetAttachmentInputContact',
  ).length;
  await page
    .getByRole('button', { name: 'Clear history…', exact: true })
    .click();
  const confirmation = page.getByRole('dialog', {
    name: 'Clear local history?',
    exact: true,
  });
  await confirmation
    .getByRole('button', { name: 'Clear history', exact: true })
    .click();
  await expect(confirmation).not.toBeVisible();
  await expect
    .poll(
      () =>
        calls.filter((call) => call.name === 'SetAttachmentInputContact')
          .length,
    )
    .toBeGreaterThan(before);
  await expect.poll(() => state.nativeTarget).toBe(contactID);
  // Model the opaque draft and revision hint emitted after Go stages the native
  // drop. Real filesystem staging is exercised separately by Go tests.
  state.dragHint = { active: true, revision: 1 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  await expect(panel).toHaveAttribute('data-drop-active', 'true');
  state.drafts = [{ ...draft, size: 42, requiresRelayWarning: false }];
  state.revision++;
  state.dragHint = { active: false, revision: 2 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  await contactHint(page, contactID, state.revision);
  await expect(
    panel.getByRole('article', { name: 'Attachment preview', exact: true }),
  ).toBeVisible();
  await expect(panel).toHaveAttribute('data-drop-active', 'false');
  expect(
    calls.some(
      (call) =>
        call.name === 'ChooseAttachment' || call.name === 'SendAttachment',
    ),
  ).toBe(false);
  expect(errors).toEqual([]);
});

test('drag feedback waits for the exact selected contact target acknowledgement and then shows the current native hint', async ({
  page,
}) => {
  let release!: () => void;
  const pending = new Promise<void>((done) => {
    release = done;
  });
  const { state, errors } = await desktop(page, [], 'connected', 4, {
    items: [],
    holdTarget: () => pending,
  });
  await expect.poll(() => state.nativeTarget).toBe(contactID);
  await page
    .getByRole('button', {
      name: 'Conversation with Synthetic second',
      exact: true,
    })
    .click();
  state.dragHint = { active: true, revision: 1 };
  await nativeHint(page, 'whatsup:file-drag', state.dragHint);
  const panel = page.getByRole('region', { name: 'Attachments', exact: true });
  await expect(panel).toHaveAttribute('data-drop-active', 'false');
  release();
  await expect.poll(() => state.nativeTarget).toBe(secondContactID);
  await expect(panel).toHaveAttribute('data-drop-active', 'true');
  await expect(
    panel.getByText('Drop a file to prepare an attachment.', { exact: true }),
  ).toBeVisible();
  expect(errors).toEqual([]);
});

test('completed media appears inside the conversation with its local timestamp and click-only video', async ({
  page,
}) => {
  const recordedAt = '2026-11-14T12:01:00.123456789Z';
  const { calls, errors } = await desktop(page, [
    {
      ...item,
      state: 'complete',
      mediaType: 'video/mp4',
      recordedAt,
      transferredBytes: item.size,
    },
  ]);
  const timeline = page.getByRole('log', { name: 'Messages', exact: true });
  await expect(
    timeline.getByRole('article', { name: 'Received attachment' }),
  ).toBeVisible();
  await expect(timeline.locator('time')).toHaveAttribute(
    'datetime',
    recordedAt,
  );
  await expect(timeline.locator('video')).toHaveCount(0);
  expect(calls.some((call) => call.name === 'PreviewAttachment')).toBe(false);
  await timeline
    .getByRole('button', { name: 'Play video', exact: true })
    .click();
  await expect(timeline.locator('video')).toHaveAttribute(
    'src',
    '/local-media/' + token,
  );
  await expect(timeline.locator('video')).not.toHaveAttribute('autoplay');
  await expandAttachments(page);
  await expect(
    page
      .getByRole('region', { name: 'Attachments', exact: true })
      .getByRole('article', { name: 'Received attachment' }),
  ).toHaveCount(0);
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
