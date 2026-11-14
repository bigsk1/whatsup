import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import wails from '@wailsio/runtime/plugins/vite';

export default defineConfig({
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [
    svelte(),
    tailwindcss(),
    wails('./bindings'),
    {
      name: 'whatsup-local-development-csp',
      transformIndexHtml: {
        order: 'pre',
        handler(html, context) {
          if (!context.server) return html;
          // Vite needs injected styles and a loopback HMR socket only in development.
          return html
            .replace("style-src 'self'", "style-src 'self' 'unsafe-inline'")
            .replace(
              "connect-src 'self'",
              "connect-src 'self' ws://127.0.0.1:9245",
            );
        },
      },
    },
  ],
  build: { target: 'es2022', sourcemap: false },
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
    experimental: { openTelemetry: { enabled: false } },
  },
});
