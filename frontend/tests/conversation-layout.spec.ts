import { readFileSync } from 'node:fs';
import { expect, test, type Locator, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailableConversation } from '../src/lib/messaging';
import { unavailablePairing } from '../src/lib/pairing';
import type { AttachmentItem } from '../src/lib/state/attachments';
import { skins } from '../src/lib/themes/catalog';
import {
  contactSummary,
  messagePage,
  serveDefaultAppPreferences,
} from './contact-events';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const operations = new Map<number, string>();
for (const match of generated.matchAll(
  /export function (\w+)\([\s\S]*?\$Call\.ByID\((\d+)/g,
))
  operations.set(Number(match[2]), match[1]);

async function crowdedConversation(
  page: Page,
  os: string,
  attachmentName = 'Synthetic layout attachment 1.pdf',
) {
  const conversation = {
    ...unavailableConversation,
    state: 'connected' as const,
    path: 'Direct' as const,
    revision: 1,
    contactID,
    peerFingerprint: 'b'.repeat(64),
    peerProtocol: 4 as const,
    attachmentsSupported: true,
    contactControlsSupported: true,
    canSend: true,
    canConnect: false,
    messages: Array.from({ length: 12 }, (_, index) => ({
      id: Buffer.alloc(16, index + 2).toString('base64url'),
      text: `Synthetic layout message ${index + 1}\nSaved text remains readable while messages and files fill their own scroll areas.`,
      direction: index % 2 ? ('outgoing' as const) : ('incoming' as const),
      createdAt: '2026-11-14T12:00:00Z',
      delivered: true,
    })),
  };
  const items: AttachmentItem[] = Array.from({ length: 13 }, (_, index) => ({
    id: Buffer.alloc(16, index + 40).toString('base64url'),
    name:
      index === 0
        ? attachmentName
        : `Synthetic layout attachment ${index + 1}.pdf`,
    mediaType: 'application/pdf',
    size: 647,
    direction: 'incoming',
    state: 'complete',
    transferredBytes: 647,
    previewURL: '',
    warning: '',
  }));
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/wails/runtime', async (route) => {
    if (await serveDefaultAppPreferences(route)) return;
    const body = route.request().postDataJSON();
    const operation = operations.get(body.args?.methodID);
    if (!operation || body.object !== 0 || body.method !== 0)
      throw new Error('Unexpected synthetic layout operation');
    let result: unknown = null;
    switch (operation) {
      case 'GetStatus':
        result = {
          ...unavailableStatus,
          storageState: 'unlocked',
          identityFingerprint: 'a'.repeat(64),
          messagingAvailable: true,
          pairingAvailable: true,
          attachmentsAvailable: true,
        };
        break;
      case 'GetContacts':
        result = [contactSummary(conversation)];
        break;
      case 'GetMessages':
        result = messagePage(conversation);
        break;
      case 'MarkRead':
        result = contactSummary(conversation);
        break;
      case 'GetProfile':
        result = { displayName: '', shareDisplayName: false };
        break;
      case 'GetPairingStatus':
        result = { ...unavailablePairing, state: 'idle' };
        break;
      case 'GetAttachments':
      case 'GetAttachmentHistory':
        result = {
          contactID,
          revision: 1,
          items,
          drafts: [],
          autoAcceptImages: false,
          nextCursor: '',
          hasMore: false,
        };
        break;
      case 'SetAttachmentInputContact':
        break;
      default:
        throw new Error('Unhandled synthetic layout operation: ' + operation);
    }
    await route.fulfill({ json: result });
  });
  await page.addInitScript((os) => {
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: os, Arch: 'amd64', Debug: false };
  }, os);
  await page.goto('/');
  await expect(page.locator('.message-bubble')).toHaveCount(12);
  await expect(
    page.getByRole('button', { name: 'Expand attachments', exact: true }),
  ).toContainText('13 files');
  return errors;
}

async function box(locator: Locator) {
  const result = await locator.boundingBox();
  expect(result).not.toBeNull();
  return result!;
}

async function anchors(page: Page) {
  return {
    header: await box(page.locator('.conversation-header')),
    composer: await box(page.locator('.composer-area')),
    footer: await box(page.locator('.network-footer')),
  };
}

async function expectAnchorsUnmoved(
  page: Page,
  before: Awaited<ReturnType<typeof anchors>>,
) {
  const after = await anchors(page);
  for (const name of ['header', 'composer', 'footer'] as const) {
    expect(after[name].x).toBeCloseTo(before[name].x, 1);
    expect(after[name].y).toBeCloseTo(before[name].y, 1);
    expect(after[name].height).toBeCloseTo(before[name].height, 1);
  }
  expect(await page.evaluate(() => window.scrollY)).toBe(0);
}

async function reachAttachmentControl(
  page: Page,
  pane: Locator,
  control: Locator,
) {
  const bounds = await box(pane);
  // Wheel over the pane padding, away from its separately scrollable file
  // list. Font wrapping can put each control on a different scroll position.
  await page.mouse.move(bounds.x + 8, bounds.y + 8);
  for (let step = 0; step < 12; step++) {
    const target = await box(control);
    if (
      target.y >= bounds.y &&
      target.y + target.height <= bounds.y + bounds.height
    )
      break;
    const before = await pane.evaluate((element) => element.scrollTop);
    await page.mouse.wheel(0, target.y < bounds.y ? -48 : 48);
    await expect
      .poll(() => pane.evaluate((element) => element.scrollTop))
      .not.toBe(before);
  }
  await expect(control).toBeInViewport({ ratio: 1 });
}

for (const { label, width, height, os } of [
  { label: 'Windows desktop', width: 1168, height: 773, os: 'windows' },
  { label: 'Ubuntu desktop', width: 1024, height: 720, os: 'linux' },
  { label: 'minimum native window', width: 760, height: 560, os: 'windows' },
  { label: 'compact window', width: 520, height: 500, os: 'linux' },
]) {
  test(`keeps the composer visible with independently scrollable history and files in a ${label}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    const errors = await crowdedConversation(page, os);
    const original = await anchors(page);

    // Bounding boxes are inspected before any click, focus, wheel or locator
    // scroll. Playwright must not rescue the clipped native composer for us.
    for (const [name, bounds] of Object.entries(original)) {
      expect(
        bounds.y,
        `${name} starts inside the window`,
      ).toBeGreaterThanOrEqual(0);
      expect(
        bounds.y + bounds.height,
        `${name} ends inside the window`,
      ).toBeLessThanOrEqual(height + 1);
    }
    await expect(
      page.getByRole('textbox', { name: 'Message', exact: true }),
    ).toBeInViewport({ ratio: 1 });
    await expect(
      page.getByRole('button', { name: 'Send message', exact: true }),
    ).toBeInViewport({ ratio: 1 });

    const history = page.getByRole('log', { name: 'Messages' });
    const attachments = page.getByRole('region', {
      name: 'Attachments',
      exact: true,
    });
    await expect(
      attachments.getByRole('button', {
        name: 'Expand attachments',
        exact: true,
      }),
    ).toHaveAttribute('aria-expanded', 'false');
    expect((await box(attachments)).height).toBeLessThan(80);
    await attachments
      .getByRole('button', { name: 'Expand attachments', exact: true })
      .click();
    await expect(attachments.locator('.attachment-list article')).toHaveCount(
      13,
    );
    await expectAnchorsUnmoved(page, original);
    const attachmentBody = attachments.locator('.attachment-content');
    expect(
      await history.evaluate(
        (element) => element.scrollHeight > element.clientHeight,
      ),
    ).toBe(true);
    await history.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    await expect(
      history.locator('.message-bubble footer').last(),
    ).toBeInViewport({ ratio: 1 });
    await expectAnchorsUnmoved(page, original);
    expect(await attachmentBody.evaluate((element) => element.scrollTop)).toBe(
      0,
    );
    const historyPosition = await history.evaluate(
      (element) => element.scrollTop,
    );

    const list = attachments.locator('.attachment-list');
    await list.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    await attachmentBody.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    await expect(
      list.getByRole('button', { name: 'Save…', exact: true }).last(),
    ).toBeInViewport({ ratio: 1 });
    await expectAnchorsUnmoved(page, original);
    expect(await history.evaluate((element) => element.scrollTop)).toBe(
      historyPosition,
    );

    // Each control must be reachable through ordinary pane scrolling. They
    // need not fit simultaneously in the compact window on every system font.
    await attachmentBody.evaluate((element) => {
      element.scrollTop = 0;
    });
    for (const control of [
      attachments.getByRole('button', { name: 'Choose file…', exact: true }),
      attachments.getByRole('button', { name: 'Paste image', exact: true }),
      attachments.getByRole('checkbox', {
        name: 'Send as file (keep original bytes)',
        exact: true,
      }),
      attachments.locator('.image-permission'),
    ]) {
      await reachAttachmentControl(page, attachmentBody, control);
      await expectAnchorsUnmoved(page, original);
      expect(await history.evaluate((element) => element.scrollTop)).toBe(
        historyPosition,
      );
    }
    await expect(
      attachments.getByRole('checkbox', {
        name: 'Automatically accept images up to 25 MB from this contact',
      }),
    ).toBeInViewport({ ratio: 1 });
    await attachments
      .getByRole('button', { name: 'Collapse attachments', exact: true })
      .click();
    await expect(attachmentBody).toBeHidden();
    await expectAnchorsUnmoved(page, original);
    expect(errors).toEqual([]);
  });
}

for (const skin of skins) {
  test(`collapses the sidebar at a reachable desktop width with a smaller new-conversation button in ${skin}`, async ({
    page,
  }) => {
    await page.addInitScript(
      ({ key, value }) => {
        localStorage.setItem(key, JSON.stringify(value));
      },
      { key: 'whatsup.appearance.v2', value: { skin } },
    );
    await page.setViewportSize({ width: 1024, height: 720 });
    const errors = await crowdedConversation(page, 'windows');
    const sidebar = page.locator('.contacts-panel');
    const newConversation = page.getByRole('button', {
      name: 'New conversation',
      exact: true,
    });
    await expect(page.locator('.wordmark')).toBeVisible();
    expect((await box(sidebar)).width).toBeGreaterThan(200);
    const button = await box(newConversation);
    const rootFont = await page.evaluate(() =>
      parseFloat(getComputedStyle(document.documentElement).fontSize),
    );
    expect(button.width).toBeCloseTo(rootFont * 1.8, 1);
    expect(button.height).toBeCloseTo(rootFont * 1.8, 1);
    expect(button.width).toBeGreaterThanOrEqual(24);
    await expect(newConversation).toBeInViewport({ ratio: 1 });

    const attachments = page.getByRole('region', {
      name: 'Attachments',
      exact: true,
    });
    await attachments
      .getByRole('button', { name: 'Expand attachments', exact: true })
      .click();
    await expect(attachments.locator('.drop-hint')).toBeVisible();
    const panelMargins = () =>
      attachments.evaluate((element) => {
        const style = getComputedStyle(element);
        return [parseFloat(style.marginLeft), parseFloat(style.marginRight)];
      });
    for (const margin of await panelMargins())
      expect(margin).toBeCloseTo(rootFont * 1.25, 1);

    // A tall window ensures the compact-width rule, rather than the separate
    // short-height rule, is what hides the large file-drop hint.
    await page.setViewportSize({ width: 760, height: 720 });
    await expect(attachments.locator('.drop-hint')).toBeHidden();
    for (const margin of await panelMargins())
      expect(margin).toBeCloseTo(rootFont * 0.75, 1);

    await page.setViewportSize({ width: 760, height: 560 });
    await expect(page.locator('.wordmark')).toBeHidden();
    await expect(page.locator('.search-box')).toBeHidden();
    await expect(page.locator('.contact-details')).toBeHidden();
    expect((await box(sidebar)).width).toBeLessThan(100);
    await expect(newConversation).toBeInViewport({ ratio: 1 });
    await expect(
      page.getByRole('button', {
        name: 'Conversation with Approved contact',
        exact: true,
      }),
    ).toBeInViewport({ ratio: 1 });
    await expect(
      page.getByRole('button', { name: 'Open settings', exact: true }),
    ).toBeInViewport({ ratio: 1 });
    await expect(
      page.getByRole('textbox', { name: 'Message', exact: true }),
    ).toBeInViewport({ ratio: 1 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(760);
    expect(errors).toEqual([]);
  });

  test(`keeps the attachment-deletion explanation padded and readable in a short ${skin} window`, async ({
    page,
  }) => {
    await page.addInitScript(
      ({ key, value }) => {
        localStorage.setItem(key, JSON.stringify(value));
      },
      { key: 'whatsup.appearance.v2', value: { skin } },
    );
    await page.setViewportSize({ width: 520, height: 500 });
    const filename = `${'LongSyntheticAttachmentName'.repeat(7)}.pdf`;
    const errors = await crowdedConversation(page, 'linux', filename);
    await page
      .getByRole('button', { name: 'Expand attachments', exact: true })
      .click();
    await page
      .getByRole('region', { name: 'Attachments', exact: true })
      .getByRole('button', { name: 'Delete attachment…', exact: true })
      .first()
      .click();
    const dialog = page.getByRole('dialog', {
      name: 'Delete this attachment from WhatsUp?',
      exact: true,
    });
    await expect(dialog).toBeVisible();
    const bounds = await box(dialog);
    const explanation = await box(
      dialog.locator('#delete-attachment-description'),
    );
    const name = await box(dialog.locator('.attachment-name'));
    const leftInset = explanation.x - bounds.x;
    const rightInset =
      bounds.x + bounds.width - explanation.x - explanation.width;
    expect(leftInset).toBeGreaterThan(16);
    expect(rightInset).toBeCloseTo(leftInset, 1);
    expect(name.x).toBeCloseTo(explanation.x, 1);
    expect(name.width).toBeCloseTo(explanation.width, 1);
    expect(
      await dialog.evaluate(
        (element) => element.scrollWidth - element.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
    await expect(
      dialog.getByRole('button', { name: 'Cancel', exact: true }),
    ).toBeInViewport({ ratio: 0.99 });
    await expect(
      dialog.getByRole('button', { name: 'Delete from WhatsUp', exact: true }),
    ).toBeInViewport({ ratio: 0.99 });
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(dialog).toBeHidden();
    expect(errors).toEqual([]);
  });
}
