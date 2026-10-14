import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import pkg from './package.json' with { type: 'json' };

// The built output is committed and embedded by the Go gateway
// (internal/webchat/dist). The gateway derives its /app/ route table from
// .vite/manifest.json, so every choice here is part of the served surface:
//   - base '/app/': the app is served under /app/ and nowhere else.
//   - assetsInlineLimit 0: no data: URLs are generated.
//   - modulePreload.polyfill false: no inline <script> in index.html.
//   - sourcemap false: no .map files are served.
//   - __APP_VERSION__ comes from package.json, never a timestamp, so the output
//     is reproducible.
export default defineConfig({
  base: '/app/',
  plugins: [react()],
  define: {
    __APP_VERSION__: JSON.stringify(pkg.version),
  },
  build: {
    outDir: '../internal/webchat/dist',
    emptyOutDir: true,
    manifest: true,
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    sourcemap: false,
  },
});
