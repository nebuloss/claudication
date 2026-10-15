import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import Docs from './ui/features/docs'
import ThemeToggle from './ui/features/theme-toggle'
import { BrandMark } from './ui/primitives'
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
      <header className="sticky top-0 z-30 bg-surface/85 backdrop-blur-md">
        <div className="mx-auto flex max-w-[76rem] items-center gap-3 px-4 py-3 sm:px-6">
          <BrandMark />
          <h1 className="m-0 flex min-w-0 items-baseline gap-2 truncate text-lg leading-10 font-normal whitespace-nowrap text-on-surface">
            claudication gateway
            <span className="hidden text-sm text-on-surface-variant sm:inline">documentation</span>
          </h1>
          <span className="flex-grow" />
          <ThemeToggle />
        </div>
      </header>

      <main className="mx-auto max-w-[76rem] px-4 pt-6 pb-16 sm:px-6">
        <Docs />
      </main>
    </div>
  </StrictMode>,
)
