import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import Docs from './ui/features/docs'
import ThemeToggle from './ui/features/theme-toggle'
import './ui/styles/docs.css'

/**
 * The public docs page: its own entry, its own document, its own mark.
 *
 * Not a route of the admin app. The admin bundle carries every screen that
 * administers this gateway, and the point of this page is that it can be
 * published — so it is built separately and shares only the primitives and the
 * token set, recoloured.
 *
 * The shell is deliberately thin: a bar that says where you are and gets out
 * of the way, and a measure wide enough for a contents column beside the
 * prose. Everything that is actually documentation lives in the one component
 * below it, so the page reads as a document rather than as an app that happens
 * to contain one.
 */
const root = document.getElementById('root')
if (root === null) {
  throw new Error('claudication: #root is missing from docs.html')
}

createRoot(root).render(
  <StrictMode>
    <div className="min-h-dvh">
      {/* Sticky, because the contents column beside it is sticky too and a
          heading that scrolls away under nothing looks like a mistake. The
          same z-30 the admin shell uses, for the same reason: this is chrome
          and the page scrolls under it. */}
      <header className="sticky top-0 z-30 border-b border-outline-variant bg-surface/85 backdrop-blur-md">
        <div className="mx-auto flex max-w-[76rem] items-center gap-3 px-4 py-3 sm:px-6">
          <svg viewBox="0 0 32 32" aria-hidden className="size-6 shrink-0">
            <g fill="none" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" className="text-primary">
              <path d="M3 8c6.5 0 6.5 6 10 6s3.5-6 10-6" />
              <path d="M3 24c6.5 0 6.5-6 10-6s3.5 6 10 6" />
            </g>
            <circle cx="27.5" cy="16" r="2.75" className="fill-primary" />
          </svg>
          <span className="font-semibold text-on-surface">claudication</span>
          <span className="text-sm text-on-surface-variant">Docs</span>
          <span className="flex-grow" />
          <ThemeToggle />
        </div>
      </header>

      <main className="mx-auto max-w-[76rem] px-4 py-10 sm:px-6">
        <Docs />
      </main>
    </div>
  </StrictMode>,
)
