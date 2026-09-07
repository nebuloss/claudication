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
  resolve: { alias: { '@': resolve(import.meta.dirname, './src') } },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
  },
  server: {
    port: 5173,
    // `npm run dev` serves the UI while the gateway runs separately. The proxy
    // keeps both on one origin so the admin session cookie is sent.
    proxy: {
      '/admin': 'http://localhost:8317',
      '/v1': 'http://localhost:8317',
    },
  },
})
