import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'node:path'

export default defineConfig(({ mode }) => ({
  // The UI is the origin root. The API keeps its own prefixes (/v1, /admin,
  // /health), so nothing collides and every asset
  // reference has to be prefixed or the browser asks for /assets/… and gets the
  // API instead.
  base: '/',
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': resolve(import.meta.dirname, './src'),
      // The example client configs are plain files under configs/clients,
      // linked from the README and readable on their own. The UI imports them
      // as text at build time so there is one copy rather than a second set
      // pasted into a component. The fs.allow below is what lets it: Vite
      // treats web/ as the workspace root because the lockfile is here, and
      // would otherwise refuse to serve a file from outside it in dev.
      '#configs': resolve(import.meta.dirname, '../configs'),
    },
  },
  build: {
    outDir: 'dist',
    // The welcome pass writes beside the admin pass's output, so only the
    // first one may clear the directory.
    emptyOutDir: mode !== 'welcome',
    target: 'es2022',
    // Two documents, because there are two listeners, and two builds, because
    // one of the documents is public.
    //
    // index.html is the admin app and is served only where a session can be
    // had; welcome.html is the root of a relay with no admin beside it, and
    // anyone who finds the address reads it. Built together they would share
    // chunks, and Rollup puts a module both entries reach into the shared
    // chunk whole: the welcome page reached the API client through the
    // primitives' hooks, so its bundle carried every admin path and the names
    // of the providers and clients the gateway serves. Built on its own, its
    // graph is tree-shaken against itself alone, and nothing the admin app
    // uses is left in what it loads. scripts/check-welcome.mjs proves it on
    // every build.
    rollupOptions: {
      input:
        mode === 'welcome'
          ? { welcome: resolve(import.meta.dirname, 'welcome.html') }
          : { index: resolve(import.meta.dirname, 'index.html') },
    },
  },
  server: {
    port: 5173,
    fs: { allow: [resolve(import.meta.dirname, '..')] },
    // `npm run dev` serves the UI while the gateway runs separately. The proxy
    // keeps both on one origin so the admin session cookie is sent.
    proxy: {
      '/admin': 'http://localhost:8317',
      '/v1': 'http://localhost:8317',
    },
  },
}))
