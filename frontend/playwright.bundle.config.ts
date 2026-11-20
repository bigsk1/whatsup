import { defineConfig } from '@playwright/test';
import base from './playwright.config';

// The call tests again, this time against the production bundle in dist/ as
// the desktop app embeds it: bundled assets and the unmodified Content
// Security Policy from index.html. The development server serves worklet and
// media files as ordinary files and relaxes the policy, so it cannot show
// whether the built application can load them. Requires a prior build.
export default defineConfig({
  ...base,
  testMatch: 'calls.spec.ts',
  webServer: {
    command:
      'node node_modules/vite/bin/vite.js preview --host 127.0.0.1 --port 9245 --strictPort',
    url: 'http://127.0.0.1:9245',
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
