import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import Welcome from './ui/features/welcome'
import ThemeToggle from './ui/features/theme-toggle'
import './ui/styles/welcome.css'

/**
 * The welcome page: its own entry, its own document, its own build.
 *
 * Not a route of the admin app. It is served on the relay's address, where
 * nothing administers anything and anyone may look, so it is built on its own
 * (see vite.config.ts) and shares only the primitives and the token set,
 * recoloured — the admin screens, and the API client that names every admin
 * path, are not in its bundle to be read.
 *
 * A page rather than a card: the name and the theme switch in a thin header, the content
 * on a grid that uses a desktop's width. The soft disc behind the
 * hero is the accent container at low opacity — a token, not a new colour —
 * and is decoration only, so it is hidden from assistive technology.
 */
const root = document.getElementById('root')
if (root === null) {
  throw new Error('claudication: #root is missing from welcome.html')
}

createRoot(root).render(
  <StrictMode>
    <div className="relative isolate flex min-h-dvh flex-col overflow-hidden">
      <div
        aria-hidden
        className="pointer-events-none absolute -top-48 left-1/2 -z-10 size-[48rem] -translate-x-1/2 rounded-full bg-primary-container opacity-40 blur-3xl lg:left-1/4"
      />
      <header className="mx-auto flex w-full max-w-6xl items-center justify-between gap-4 px-4 pt-4 sm:px-8">
        <span className="text-sm font-medium tracking-wide text-on-surface-variant">claudication</span>
        <ThemeToggle />
      </header>
      <main className="mx-auto flex w-full max-w-6xl flex-1 items-center px-4 py-6 sm:px-8">
        <Welcome />
      </main>
    </div>
  </StrictMode>,
)
