import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:9245',
    trace: 'retain-on-failure',
    launchOptions: {
      // Complement the pinned Chromium defaults: no background reporting,
      // crash uploader, reliability reports or hyperlink audit pings.
      args: [
        '--disable-background-networking',
        '--disable-breakpad',
        '--disable-domain-reliability',
        '--no-pings',
      ],
    },
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: 'npm run dev',
    url: 'http://127.0.0.1:9245',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
