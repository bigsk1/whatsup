import { expect, test } from '@playwright/test';

test('starts with empty conversations and unavailable delivery actions', async ({
  page,
}) => {
  const externalRequests: string[] = [];
  page.on('request', (request) => {
    if (!request.url().startsWith('http://127.0.0.1:9245/'))
      externalRequests.push(request.url());
  });
  await page.goto('/');
  await expect(
    page.getByRole('heading', { name: 'Your people go here' }),
  ).toBeVisible();
  await expect(page.getByText('No network session')).toBeVisible();
  await expect(
    page.getByText('Nothing is sent or queued.', { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByRole('button', { name: 'Start a conversation' }),
  ).toBeDisabled();
  await expect(
    page.getByRole('button', { name: 'Send message unavailable' }),
  ).toBeDisabled();
  await expect(
    page.getByRole('button', { name: 'Attach file unavailable' }),
  ).toBeDisabled();
  await expect(
    page.getByRole('textbox', {
      name: 'Message unavailable without an approved contact',
    }),
  ).toBeDisabled();
  await expect(
    page.getByText('Browser preview · desktop actions unavailable'),
  ).toBeVisible();
  expect(externalRequests).toEqual([]);
});

test('opens settings by keyboard, retains focus, and saves appearance only', async ({
  page,
}) => {
  await page.goto('/');
  const trigger = page.getByRole('button', {
    name: 'Open settings',
    exact: true,
  });
  await trigger.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('region', { name: 'Settings', exact: true });
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByRole('button', { name: 'Unlock and connect' }),
  ).toBeDisabled();
  await dialog.getByRole('button', { name: 'Appearance', exact: true }).click();
  await page.getByRole('radio', { name: 'Light', exact: true }).check();
  await page.getByRole('radio', { name: 'Rose', exact: true }).check();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'rose');
  await page.keyboard.press('Escape');
  await expect(dialog).not.toBeVisible();
  await expect(trigger).toBeFocused();
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'rose');
  const stored = await page.evaluate(() => ({ ...localStorage }));
  expect(stored).toEqual({
    'whatsup.appearance.v1': '{"theme":"light","accent":"rose"}',
  });
});

test('follows system appearance and ignores malformed stored preferences', async ({
  page,
}) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await page.addInitScript(() =>
    localStorage.setItem('whatsup.appearance.v1', '{invalid'),
  );
  await page.goto('/');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  await page.getByRole('button', { name: 'Appearance', exact: true }).click();
  await page.getByRole('radio', { name: 'System', exact: true }).check();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
});

test('renders untrusted search text as text without remote assets or HTML', async ({
  page,
}) => {
  await page.goto('/');
  const payload =
    '<img src="https://example.invalid/peer.png" onerror="alert(1)">';
  await page.getByRole('searchbox', { name: 'Search contacts' }).fill(payload);
  await expect(
    page.getByText(`No conversations match “${payload}”.`, { exact: true }),
  ).toBeVisible();
  await expect(page.locator('img')).toHaveCount(0);
  await page
    .getByRole('button', { name: 'Clear search', exact: true })
    .first()
    .click();
  await expect(
    page.getByRole('heading', { name: 'Your people go here' }),
  ).toBeVisible();
});

test('handles an unavailable preference store without breaking the interface', async ({
  page,
}) => {
  await page.addInitScript(() => {
    Storage.prototype.setItem = () => {
      throw new DOMException('Synthetic storage failure');
    };
  });
  await page.goto('/');
  await page
    .getByRole('button', { name: 'Open settings', exact: true })
    .click();
  await page.getByRole('button', { name: 'Appearance', exact: true }).click();
  await page.getByRole('radio', { name: 'Blue', exact: true }).check();
  await page
    .getByRole('button', { name: 'Back to chats', exact: true })
    .click();
  await expect(page.getByRole('alert')).toContainText(
    'This device could not save the preference.',
  );
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'blue');
});

test('keeps its main controls visible at a narrow window size', async ({
  page,
}) => {
  await page.setViewportSize({ width: 520, height: 780 });
  await page.goto('/');
  await expect(
    page.getByRole('button', { name: 'Open settings', exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole('heading', { name: 'Less noise. More connection.' }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

for (const viewport of [
  { width: 1280, height: 900 },
  { width: 520, height: 500 },
]) {
  test(`Settings occupies the workspace and keeps navigation and scrolling reachable at ${viewport.width}×${viewport.height}`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto('/');
    await page
      .getByRole('button', { name: 'Open settings', exact: true })
      .click();
    const dialog = page.getByRole('region', { name: 'Settings', exact: true });
    await expect(dialog).toBeVisible();
    const box = await dialog.boundingBox();
    expect(box).not.toBeNull();
    if (!box) return;
    expect(box).toEqual({
      x: 0,
      y: 0,
      width: viewport.width,
      height: viewport.height,
    });
    await expect(
      page.getByRole('dialog', { name: 'Settings', exact: true }),
    ).toHaveCount(0);
    await dialog
      .getByRole('button', { name: 'Privacy & diagnostics', exact: true })
      .click();
    const runtime = dialog.getByRole('heading', {
      name: 'Desktop runtime',
      exact: true,
    });
    await runtime.scrollIntoViewIfNeeded();
    await expect(runtime).toBeInViewport();
    const done = dialog.getByRole('button', {
      name: 'Back to chats',
      exact: true,
    });
    await expect(done).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await done.click();
    await expect(dialog).not.toBeVisible();
    await expect(
      page.getByRole('button', { name: 'Open settings', exact: true }),
    ).toBeFocused();
  });
}

test('discloses local app diagnostics and the Windows runtime exception in About', async ({
  page,
}) => {
  await page.goto('/');
  await page
    .getByRole('button', { name: 'About this build', exact: true })
    .click();
  const dialog = page.getByRole('dialog', {
    name: 'About WhatsUp',
    exact: true,
  });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('redacted troubleshooting logs locally');
  await expect(dialog).toContainText('separate from Edge');
  await expect(dialog).toContainText(
    'required Microsoft diagnostics may still occur',
  );
  await expect(dialog).toContainText('Linux uses WebKitGTK');
});
