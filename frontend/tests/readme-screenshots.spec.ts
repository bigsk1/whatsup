import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';
import { unavailablePairing } from '../src/lib/pairing';
import type { ContactSummary } from '../src/lib/state/bridge';
import type { ConversationMessage } from '../src/lib/messaging';
import { defaultAppPreferences } from '../src/lib/state/preferences';
import { serveDefaultAppPreferences } from './contact-events';

test.skip(
  !process.env.WHATSUP_README_SCREENSHOTS,
  'Set WHATSUP_README_SCREENSHOTS=1 to regenerate docs/images.',
);

const out = join(process.cwd(), '..', 'docs', 'images');
const sam = 'AQEBAQEBAQEBAQEBAQEBAQ';
const nana = 'AgICAgICAgICAgICAgICAg';
const jordan = 'BQUFBQUFBQUFBQUFBQUFBQ';
const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const operations = new Map<number, string>();
for (const match of generated.matchAll(
  /export function (\w+)\([\s\S]*?\$Call\.ByID\((\d+)/g,
))
  operations.set(Number(match[2]), match[1]);

function contact(
  contactID: string,
  localName: string,
  state: string,
  path: string,
  seed: string,
  unread = 0,
): ContactSummary {
  return {
    peerProtocol: 4,
    attachmentsSupported: true,
    contactControlsSupported: true,
    contactID,
    localName,
    unread,
    paused: false,
    state,
    path,
    revision: 1,
    // A realistic-looking but synthetic key fingerprint for a fictional contact.
    peerFingerprint: createHash('sha256')
      .update(`readme screenshot ${seed}`)
      .digest('hex'),
    peerDisplayName: '',
    canConnect: state !== 'connected',
    canSend: true,
  } as ContactSummary;
}
const lines: [string, 'incoming' | 'outgoing', string][] = [
  ['Are we still on for game night Friday? 🎮', 'incoming', '18:02'],
  ['Absolutely. I’m bringing the snacks 🍿', 'outgoing', '18:04'],
  ['Perfect. Can you bring the card game too?', 'incoming', '18:05'],
  ['Already packed it 😄', 'outgoing', '18:07'],
  ['Nana says hi, by the way ❤️', 'incoming', '18:09'],
];
const messages: ConversationMessage[] = lines.map(
  ([text, direction, time], index) => ({
    id: String(index).padStart(21, 'M') + 'A',
    text,
    direction,
    createdAt: `2026-11-13T${time}:00Z`,
    delivered: true,
  }),
);

async function setup(page: Page, look: Record<string, unknown>) {
  await page.setViewportSize({ width: 1180, height: 800 });
  // Original synthetic illustrations, drawn only in the screenshot fixture.
  // They contain no photos, personal information, SVG or remote resources.
  const portraits = await page.evaluate(() =>
    ['#0d9488', '#7c3aed', '#c2410c', '#2563eb'].map((background, index) => {
      const canvas = document.createElement('canvas');
      canvas.width = canvas.height = 128;
      const ctx = canvas.getContext('2d')!;
      ctx.fillStyle = background;
      ctx.fillRect(0, 0, 128, 128);
      ctx.fillStyle = ['#a7f3d0', '#ddd6fe', '#fed7aa', '#bfdbfe'][index];
      ctx.beginPath();
      ctx.arc(64, 139, 53, 0, Math.PI * 2);
      ctx.fill();
      ctx.fillStyle = ['#e7b894', '#b87a58', '#edc7a5', '#d59f7b'][index];
      ctx.beginPath();
      ctx.ellipse(64, 59, 29, 34, 0, 0, Math.PI * 2);
      ctx.fill();
      ctx.fillStyle = ['#30252a', '#262b34', '#ede5d6', '#513128'][index];
      ctx.beginPath();
      ctx.arc(64, 48, 31, Math.PI, Math.PI * 2);
      ctx.lineTo(88, 43);
      ctx.lineTo(72, 34);
      ctx.lineTo(49, 47);
      ctx.closePath();
      ctx.fill();
      ctx.fillStyle = '#292a32';
      for (const x of [54, 75]) {
        ctx.beginPath();
        ctx.arc(x, 60, 2.8, 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.strokeStyle = '#824c43';
      ctx.lineWidth = 3;
      ctx.beginPath();
      ctx.arc(64, 68, 9, 0.2, Math.PI - 0.2);
      ctx.stroke();
      return canvas.toDataURL('image/png').split(',')[1];
    }),
  );
  const portraitURLs = portraits.map(
    (_, index) => `/local-media/${String(index + 1).padStart(32, '0')}`,
  );
  await page.route('**/local-media/*', (route) => {
    const index = portraitURLs.indexOf(new URL(route.request().url()).pathname);
    if (index < 0) return route.fulfill({ status: 404 });
    return route.fulfill({
      contentType: 'image/png',
      headers: { 'X-Content-Type-Options': 'nosniff' },
      body: Buffer.from(portraits[index], 'base64'),
    });
  });
  const contacts = [
    contact(sam, 'Sam Rivera', 'connected', 'Direct', 'b'),
    contact(nana, 'Nana', 'offline', 'Unknown', 'c', 2),
    contact(jordan, 'Jordan', 'offline', 'Unknown', 'd'),
  ];
  await page.addInitScript((value) => {
    localStorage.setItem('whatsup.appearance.v2', JSON.stringify(value));
    const host = window as Window & {
      _wails?: { flags?: object; environment?: object };
    };
    host._wails ??= {};
    host._wails.flags = {};
    host._wails.environment = { OS: 'windows', Arch: 'amd64', Debug: false };
  }, look);
  await page.route('**/wails/runtime', async (route) => {
    const body = route.request().postDataJSON();
    const method = operations.get(body.args?.methodID) ?? '';
    const args = body.args?.args ?? [];
    if (method === 'GetAvatarState')
      return route.fulfill({
        json: {
          revision: 1,
          ownURL: portraitURLs[0],
          shareAvatar: true,
          showContactPhotos: true,
          contacts: [sam, nana, jordan].map((contactID, index) => ({
            contactID,
            url: portraitURLs[index + 1],
          })),
        },
      });
    // Voice and video calls switched on, so the conversation header shows
    // the call buttons as they look in everyday use.
    if (method === 'GetAppPreferences')
      return route.fulfill({
        json: { ...defaultAppPreferences, voiceCalls: true, videoCalls: true },
      });
    if (method === 'GetCall') {
      const peer = contacts.find((item) => item.contactID === args[0]);
      return route.fulfill({
        json: {
          contactID: args[0],
          state: 'idle',
          callID: '',
          peerRinging: false,
          micMuted: false,
          peerMicMuted: false,
          activeSince: 0,
          availability: peer?.state === 'connected' ? 'ready' : 'offline',
          lastEnd: '',
          lastOutgoing: false,
          lastConnected: false,
          video: false,
          cameraOn: false,
          peerCameraOn: false,
        },
      });
    }
    if (await serveDefaultAppPreferences(route)) return;
    const selected = contacts.find((item) => item.contactID === args[0]);
    switch (method) {
      case 'GetStatus':
        return route.fulfill({
          json: {
            ...unavailableStatus,
            storageState: 'unlocked',
            identityFingerprint: 'a'.repeat(64),
            pairingAvailable: true,
          },
        });
      case 'GetPairingStatus':
        return route.fulfill({
          json: { ...unavailablePairing, state: 'idle' },
        });
      case 'GetProfile':
        return route.fulfill({
          json: { displayName: 'Alex', shareDisplayName: true },
        });
      case 'GetContacts':
        return route.fulfill({ json: contacts });
      case 'GetMessages':
        return route.fulfill({
          json: {
            contactID: args[0],
            revision: 1,
            messages: args[0] === sam ? messages : [],
            hasMore: false,
            nextCursor: '',
          },
        });
      case 'MarkRead':
        return route.fulfill({ json: { ...selected, unread: 0 } });
      default:
        return route.fulfill({ json: null });
    }
  });
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Conversation with Sam Rivera' })
    .click();
  await page.waitForTimeout(500);
}

const looks = [
  ['bubbly-midnight', 'bubble', 'whatsup'],
  ['arcade-pocket-green', 'arcade', 'gameboy'],
  ['neon-github-dark', 'neon', 'github-dark'],
  ['terminal-mainframe', 'terminal', 'matrix'],
  ['bubbly-github-light', 'bubble', 'github-light'],
] as const;
for (const [file, skin, palette] of looks) {
  test(`readme ${file}`, async ({ page }) => {
    await setup(page, {
      skin,
      palette,
      lightPalette: 'daybreak',
      followSystem: false,
      textSize: 'comfortable',
      effects: true,
    });
    await page.screenshot({ path: join(out, `${file}.png`) });
  });
}
test('readme appearance settings', async ({ page }) => {
  await setup(page, {
    skin: 'bubble',
    palette: 'tokyo-night',
    lightPalette: 'daybreak',
    followSystem: false,
    textSize: 'comfortable',
    effects: true,
  });
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  await page.getByRole('button', { name: 'Appearance', exact: true }).click();
  await page.waitForTimeout(400);
  await page.screenshot({ path: join(out, 'appearance-settings.png') });
});
