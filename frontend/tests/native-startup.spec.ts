import { expect, test, type Page } from '@playwright/test';
import { unavailableStatus } from '../src/lib/backend';

type Platform = 'windows' | 'linux';
type Operation = 'GetStatus' | 'UnlockStorage' | 'LockStorage';

// IDs are the public contract emitted by the pinned binding generator. Route the
// real generated bindings and Wails HTTP transport; do not replace requestStatus.
const operations: Record<number, Operation> = {
  3129447825: 'GetStatus',
  4289999910: 'UnlockStorage',
  18574999: 'LockStorage',
};

function injectRuntime(platform: Platform) {
  const host = window as Window & {
    _wails?: { flags?: object; environment?: object };
  };
  host._wails ??= {};
  host._wails.flags = {};
  host._wails.environment = { OS: platform, Arch: 'amd64', Debug: false };
  // Match Wails Core: expose configuration first, then notify on a microtask.
  void Promise.resolve().then(() => {
    window.dispatchEvent(new Event('wails:runtime-config-ready'));
  });
}

async function syntheticDesktop(page: Page, failFirstStatus = false) {
  const calls: Operation[] = [];
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.route('**/wails/runtime', async (route) => {
    const request = route.request();
    const body = request.postDataJSON();
    const operation = operations[body.args?.methodID];
    if (
      request.method() !== 'POST' ||
      body.object !== 0 ||
      body.method !== 0 ||
      !operation
    ) {
      throw new Error('Unexpected native runtime operation');
    }
    calls.push(operation);
    if (failFirstStatus && calls.length === 1) {
      await route.fulfill({
        status: 503,
        json: { kind: 'RuntimeError', message: 'Synthetic status failure' },
      });
      return;
    }
    await route.fulfill({
      json: {
        ...unavailableStatus,
        storageState: operation === 'UnlockStorage' ? 'unlocked' : 'locked',
        identityFingerprint:
          operation === 'UnlockStorage' ? 'SYNTHETIC-IDENTITY-FINGERPRINT' : '',
      },
    });
  });
  return { calls, errors };
}

for (const platform of ['windows', 'linux'] as const) {
  test(`activates desktop storage after late ${platform} runtime injection`, async ({
    page,
  }) => {
    const { calls, errors } = await syntheticDesktop(page);
    await page.goto('/');
    await expect(
      page.getByText('Browser preview · desktop actions unavailable'),
    ).toBeVisible();
    await page
      .getByRole('button', { name: 'Open settings', exact: true })
      .click();
    const dialog = page.getByRole('dialog', { name: 'Settings', exact: true });
    await expect(
      dialog.getByRole('button', { name: 'Unlock storage' }),
    ).toBeDisabled();

    // An empty runtime object and an early/spurious event must grant no actions
    // or consume the real readiness notification that arrives later.
    await page.evaluate(() => {
      (window as Window & { _wails?: object })._wails = {};
      window.dispatchEvent(new Event('wails:runtime-config-ready'));
    });
    await expect(
      dialog.getByRole('button', { name: 'Unlock storage' }),
    ).toBeDisabled();
    expect(calls).toEqual([]);

    await page.evaluate(injectRuntime, platform);
    await expect(page.getByText('Desktop development build')).toBeVisible();
    await expect(
      dialog.getByText('Locked on this device', { exact: true }),
    ).toBeVisible();
    await expect(
      dialog.getByRole('button', { name: 'Unlock storage' }),
    ).toBeEnabled();
    expect(calls).toEqual(['GetStatus']);

    await page.evaluate(() => {
      for (let index = 0; index < 3; index++)
        window.dispatchEvent(new Event('wails:runtime-config-ready'));
    });
    await dialog.getByRole('button', { name: 'Unlock storage' }).click();
    await expect(
      dialog.getByText('Unlocked on this device', { exact: true }),
    ).toBeVisible();
    await expect(dialog.locator('#identity-fingerprint')).toHaveText(
      'SYNTHETIC-IDENTITY-FINGERPRINT',
    );
    await dialog.getByRole('button', { name: 'Lock storage' }).click();
    await expect(
      dialog.getByText('Locked on this device', { exact: true }),
    ).toBeVisible();
    await expect(dialog.locator('#identity-fingerprint')).toHaveCount(0);
    expect(calls).toEqual(['GetStatus', 'UnlockStorage', 'LockStorage']);
    expect(await page.evaluate(() => ({ ...localStorage }))).toEqual({});
    expect(errors).toEqual([]);
  });
}

test('detects runtime configuration already present before the app mounts', async ({
  page,
}) => {
  const { calls, errors } = await syntheticDesktop(page);
  await page.addInitScript(injectRuntime, 'windows' as Platform);
  await page.goto('/');
  await expect(page.getByText('Desktop development build')).toBeVisible();
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  expect(calls).toEqual(['GetStatus']);
  expect(errors).toEqual([]);
});

test('keeps desktop retry available after the first status request fails', async ({
  page,
}) => {
  const { calls, errors } = await syntheticDesktop(page, true);
  await page.goto('/');
  await expect(
    page.getByText('Browser preview · desktop actions unavailable'),
  ).toBeVisible();
  await page.evaluate(injectRuntime, 'linux' as Platform);
  await expect(page.getByRole('alert')).toContainText(
    'The desktop could not report its status.',
  );
  await expect(page.getByText('Desktop development build')).toBeVisible();
  await page.getByRole('button', { name: 'Try again', exact: true }).click();
  await expect(
    page.getByText('Local storage locked', { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
  expect(calls).toEqual(['GetStatus', 'GetStatus']);
  expect(errors).toEqual([]);
});
