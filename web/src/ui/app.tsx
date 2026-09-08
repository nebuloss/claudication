import { useCallback, useEffect, useState } from 'react'
import Accounts from './features/accounts'
import Keys from './features/keys'
import Overview from './features/overview'
import Settings from './features/settings'
import SignIn from './features/sign-in'
import ThemeToggle from './features/theme-toggle'
import UsagePanel from './features/usage'
import { useHashTab } from './hooks'
import { ApiError, api, type ApiKey, type Session } from '../api/client'
import { Banner, CopyField, FilledButton, Modal, Spinner, TextButton } from './primitives'

type State =
  | { phase: 'probing' }
  | { phase: 'setup'; minLength: number }
  | { phase: 'out' }
  | { phase: 'in'; session: Session }

const TABS = ['overview', 'accounts', 'keys', 'usage', 'settings'] as const
type Tab = (typeof TABS)[number]

const TAB_LABELS: Record<Tab, string> = {
  overview: 'Overview',
  accounts: 'Claude accounts',
  keys: 'API keys',
  usage: 'Usage',
  settings: 'Settings',
}

/**
 * Top-level controller: probes for a session, then shows the one screen that
 * fits. Nothing below it needs to know whether a session exists.
 *
 * A 401 is not enough on its own — a gateway nobody has set up yet answers the
 * same way as one whose session lapsed, and the two want opposite screens. So
 * the unauthenticated case asks /admin/setup which of the two it is.
 */
export default function App() {
  const [state, setState] = useState<State>({ phase: 'probing' })
  const [tab, setTab] = useHashTab<Tab>(TABS, 'overview')
  // A freshly minted API key lives here, not in the Keys panel: panels unmount
  // on a tab change, and the tabs are hash routes, so Back unmounts one too.
  // The plaintext exists nowhere else — the store keeps only its hash — so
  // losing it to a stray click means the key is gone for good.
  const [minted, setMinted] = useState<{ key: ApiKey; plaintext: string } | null>(null)

  const probe = useCallback(async () => {
    try {
      setState({ phase: 'in', session: await api.whoAmI() })
      return
    } catch (err) {
      if (!(err instanceof ApiError) || !err.isUnauthenticated) {
        // Worth knowing about, but a sign-in screen is still where to land.
        console.warn('session probe failed:', err)
      }
    }
    try {
      const status = await api.setupStatus()
      setState(
        status.needs_setup
          ? { phase: 'setup', minLength: status.min_password_len }
          : { phase: 'out' },
      )
    } catch (err) {
      // If even that is unreachable, sign-in is the screen that can report it.
      console.warn('setup probe failed:', err)
      setState({ phase: 'out' })
    }
  }, [])

  useEffect(() => {
    void probe()
  }, [probe])

  if (state.phase === 'probing') {
    return (
      <div className="grid min-h-dvh place-items-center text-on-surface-variant">
        <Spinner className="size-6" />
      </div>
    )
  }
  if (state.phase === 'setup') {
    return <SignIn mode="setup" minLength={state.minLength} onSignedIn={() => void probe()} />
  }
  if (state.phase === 'out') {
    return <SignIn mode="sign-in" onSignedIn={() => void probe()} />
  }

  const signOut = async () => {
    try {
      await api.signOut()
    } catch {
      // Signing out locally matters more than the round trip succeeding.
    }
    void probe()
  }
  const expired = () => void probe()

  return (
    <div className="min-h-dvh">
      <header className="sticky top-0 z-10 bg-surface/85 backdrop-blur-md">
        <div className="mx-auto flex max-w-4xl items-center justify-between gap-4 px-4 pt-4 sm:px-6">
          <div className="flex items-center gap-3">
            <Mark />
            <div>
              <h1 className="m-0 text-xl leading-6 font-normal text-on-surface">claudication</h1>
              <p className="mt-0.5 mb-0 text-xs text-on-surface-variant">Claude API gateway</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <ThemeToggle />
            <TextButton onClick={() => void signOut()}>Sign out</TextButton>
          </div>
        </div>

        <nav className="mx-auto max-w-4xl overflow-x-auto px-4 sm:px-6">
          <div className="flex min-w-max border-b border-outline-variant">
            {TABS.map((id) => (
              <a
                key={id}
                href={`#${id}`}
                onClick={(e) => {
                  e.preventDefault()
                  setTab(id)
                }}
                className={`state-layer relative px-4 py-3.5 text-sm font-medium whitespace-nowrap ${
                  tab === id ? 'text-primary' : 'text-on-surface-variant'
                }`}
              >
                {TAB_LABELS[id]}
                {tab === id && (
                  <span className="absolute inset-x-2 bottom-0 h-[3px] rounded-t-full bg-primary" />
                )}
              </a>
            ))}
          </div>
        </nav>
      </header>

      <main className="mx-auto max-w-4xl px-4 pt-6 pb-16 sm:px-6">
        {tab === 'overview' && <Overview onExpired={expired} onGoTo={setTab} />}
        {tab === 'accounts' && <Accounts onExpired={expired} />}
        {tab === 'keys' && <Keys onExpired={expired} onMinted={setMinted} />}
        {tab === 'usage' && <UsagePanel onExpired={expired} />}
        {tab === 'settings' && (
          <Settings session={state.session} onSessionChanged={() => void probe()} />
        )}
      </main>

      {minted !== null && (
        <Modal title={minted.key.name} size="lg" onClose={() => setMinted(null)}>
          <Banner tone="warn" className="mb-4">
            This is the only time the key is shown. Nothing can retrieve it afterwards — only the
            hash is stored. Copy it now.
          </Banner>
          <CopyField label="API key" value={minted.plaintext} />
          <div className="mt-6 flex justify-end">
            <FilledButton type="button" onClick={() => setMinted(null)}>
              Done
            </FilledButton>
          </div>
        </Modal>
      )}
    </div>
  )
}

/** The favicon's stenosis, at header size. */
function Mark() {
  return (
    <span className="grid size-10 shrink-0 place-items-center rounded-[var(--radius-md3-m)] bg-primary-container text-on-primary-container">
      <svg viewBox="0 0 32 32" className="size-5" aria-hidden>
        <g fill="none" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round">
          <path d="M3 8c6.5 0 6.5 6 10 6s3.5-6 10-6" />
          <path d="M3 24c6.5 0 6.5-6 10-6s3.5 6 10 6" />
        </g>
        <circle cx="27.5" cy="16" r="2.75" fill="currentColor" />
      </svg>
    </span>
  )
}
