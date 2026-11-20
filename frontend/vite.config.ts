import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import wails from '@wailsio/runtime/plugins/vite';
import advisoryException from '../config/advisory-exception.json' with { type: 'json' };

const reviewedTooling = Object.keys(advisoryException.findings);

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
      name: 'whatsup-keep-reviewed-tooling-out-of-bundles',
      apply: 'build',
      generateBundle(_, bundle) {
        for (const output of Object.values(bundle)) {
          if (output.type !== 'chunk') continue;
          const ids = [
            ...Object.keys(output.modules),
            ...output.imports,
            ...output.dynamicImports,
          ];
          for (const raw of ids) {
            const id = raw.replaceAll('\\', '/');
            const name = reviewedTooling.find(
              (name) =>
                id.includes(`/node_modules/${name}/`) ||
                id === name ||
                id.startsWith(`${name}/`),
            );
            if (name)
              this.error(
                `Reviewed build tooling must stay out of the app bundle: ${name}`,
              );
          }
        }
      },
    },
    {
      // The page's Content Security Policy allows data: URLs for images only.
      // Vite packs small imported assets into the bundle as data: URLs, which
      // the built application then cannot load (a worklet, a sound). The
      // development server never does this, so only the build can catch it.
      name: 'whatsup-no-inlined-scripts-or-media',
      apply: 'build',
      generateBundle(_, bundle) {
        for (const [file, output] of Object.entries(bundle)) {
          const text =
            output.type === 'chunk'
              ? output.code
              : typeof output.source === 'string'
                ? output.source
                : '';
          const inlined = text.match(
            /data:(?:text\/javascript|application\/javascript|audio\/|video\/|font\/|application\/font)[^"'`\s]{0,40}/,
          );
          if (inlined)
            this.error(
              `${file} carries an inlined asset the Content Security Policy refuses (${inlined[0].split(/[;,]/)[0]}). Import it with ?url&no-inline.`,
            );
        }
      },
    },
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
