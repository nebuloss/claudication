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
