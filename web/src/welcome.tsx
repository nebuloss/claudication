import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import Welcome from './ui/features/welcome'
import ThemeToggle from './ui/features/theme-toggle'
import { AppTitle } from './ui/primitives'
import './ui/styles/welcome.css'

/**
 * The welcome page: its own entry, its own document, its own mark.
 *
 * Not a route of the admin app. It is served on the relay's address, where
 * nothing administers anything, so it is built separately and shares only the
 * primitives and the token set, recoloured — the admin screens are not in its
 * bundle to be read.
 *
 * The shell is the sign-in screen's: the mark and name, the theme on the
 * right, one card in the middle. Somebody who has seen either page should
 * recognise the other as the same product.
 */
const root = document.getElementById('root')
if (root === null) {
  throw new Error('claudication: #root is missing from welcome.html')
}

createRoot(root).render(
  <StrictMode>
    <div className="flex min-h-dvh flex-col">
      <header className="mx-auto flex w-full max-w-[110rem] items-center justify-between gap-4 px-4 pt-4 sm:px-6">
        <AppTitle />
        <ThemeToggle />
      </header>
      <main className="mx-auto flex w-full max-w-lg flex-1 flex-col justify-center px-4 py-10">
        <Welcome />
      </main>
    </div>
  </StrictMode>,
)
