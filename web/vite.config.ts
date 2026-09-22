import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'node:path'

export default defineConfig({
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
    emptyOutDir: true,
    target: 'es2022',
    // Two documents, because there are two listeners. index.html is the admin
    // app and is served only where a session can be had; docs.html is the
    // public setup page and is served on its own address with no sign-in.
    // They share the primitives and the token set through the usual chunking,
    // and nothing that administers the gateway is reachable from the second.
    rollupOptions: {
      input: {
        index: resolve(import.meta.dirname, 'index.html'),
        docs: resolve(import.meta.dirname, 'docs.html'),
      },
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
})
