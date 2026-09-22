import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import Docs from './ui/features/docs'
import ThemeToggle from './ui/features/theme-toggle'
import './ui/styles/index.css'

/**
 * The public docs page: its own entry, its own document, its own listener.
 *
 * Not a route of the admin app. The admin bundle carries every screen that
 * administers this gateway, and the point of this page is that it can be
 * published — so it is built separately and shares only the primitives and the
 * token set.
 */
const root = document.getElementById('root')
if (root === null) {
  throw new Error('claudication: #root is missing from docs.html')
}

createRoot(root).render(
  <StrictMode>
    <div className="mx-auto min-h-screen w-full max-w-[72rem] px-4 py-6 sm:px-6">
      <header className="mb-6 flex items-center gap-3">
        <h1 className="text-lg font-semibold text-on-surface">claudication</h1>
        <span className="text-sm text-on-surface-variant">— pointing a client at this gateway</span>
        <span className="flex-grow" />
        <ThemeToggle />
      </header>
      <Docs />
    </div>
  </StrictMode>,
)
