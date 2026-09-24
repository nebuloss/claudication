import { useCallback, useEffect, useState } from 'react'
import Accounts from './features/accounts'
import Keys from './features/keys'
import Overview from './features/overview'
import Settings from './features/settings'
import RequestsPanel from './features/requests'
import SignIn from './features/sign-in'
import ThemeToggle from './features/theme-toggle'
import UsagePanel from './features/usage'
import { usePathTab, useLive, useLoader } from './hooks'
// OverviewData, because Overview is already the panel above.
import {
  ApiError,
  api,
  type ApiKey,
  type GatewayConfig,
  type Overview as OverviewData,
  type Session,
} from '../api/client'
import {
  Banner,
  CopyField,
  FilledButton,
  IconLink,
  Modal,
  Spinner,
  TextButton,
  TextLink,
} from './primitives'

type State =
  | { phase: 'probing' }
  | { phase: 'setup'; minLength: number }
  | { phase: 'out' }
  | { phase: 'in'; session: Session }

// Setup is not here any more. Every tab is something you administer; setup was
// the one that helped you *use* the gateway, and it was the one screen a
// person configuring a client needed and could not reach without the admin
// password. It is a page of its own now, on its own listener, linked from the
// header — see the doc icon beside GitHub.
//
// Requests sits beside Usage rather than inside it: usage is what was spent
// and groups by key, model and account, while the log is what arrived —
// including requests refused before they had a key to group under.
const TABS = ['overview', 'accounts', 'keys', 'usage', 'requests', 'settings'] as const
type Tab = (typeof TABS)[number]

/**
 * Screens that earn more than the reading width.
 *
 * 56rem is right for prose and for four columns of numbers, and wrong for a
 * table of chats: a name, a client, a model list and four figures do not fit,
 * so the interesting column is the one that gets truncated. These get the width
 * of the window instead, capped so a very wide monitor does not stretch a table
 * across a metre of glass.
 *
 * One measure for every screen, not two. It used to be per screen — the wide
 * one for Usage and Requests, a reading measure for the rest — which meant the
 * page changed width as you moved along the tab strip, and everything on it
 * jumped sideways. A tab strip is one place; it should not resize under you.
 *
 * What the reading measure was protecting is protected where it belongs
 * instead: Card caps the paragraphs inside it, so a card can span the window
 * while its sentences stay at a length someone can read.
 */
const MEASURE = 'max-w-[110rem]'

const TAB_LABELS: Record<Tab, string> = {
  overview: 'Overview',
  accounts: 'Claude accounts',
  keys: 'API keys',
  usage: 'Usage',
  requests: 'Requests',
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
  const [tab, setTab] = usePathTab<Tab>(TABS, 'overview')
  // The configuration, loaded once for the whole signed-in app: the header
  // reads the docs link out of it and Settings renders the rest of it. One
  // copy, because a switch on that screen decides whether the header shows a
  // link, and with two the card knew it had changed and the header did not.
  //
  // Declared with the other hooks so the order never changes across the early
  // returns below, and allowed to fail — signed out this 401s, and no link is
  // the right outcome then anyway.
  //
  // Keyed on the phase, which is the part that bit: without it this ran once,
  // on mount, while the shell was still probing or showing a sign-in screen —
  // so it 401'd, set an error, and never tried again once a session existed.
  // Settings then rendered "Could not read the configuration" at a signed-in
  // operator forever.
  const config = useLoader<GatewayConfig>(() => api.config(), undefined, [state.phase])
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
  // One value for the header, the nav and the page, so the tab underline stays
  // over its tab when the measure changes.
  const measure = MEASURE
  // Empty unless a public docs page is configured, published and switched on,
  // in which case the header links out to it. Allowed to fail silently: a
  // missing link is a missing link, not a reason to fail the shell.
  const docsURL = config.data?.docs_enabled === true ? (config.data.docs_url ?? '') : ''

  return (
    <div className="min-h-dvh">
      {/* z-30 rather than z-10: this is the app's chrome and everything on a
          screen scrolls under it. At z-10 it tied with the sticky line-number
          gutter in a code block and with a capped table's sticky heading, and
          a tie is won by whichever comes later in the document — which is
          always the content, so both rode over the tab bar. */}
      <header className="sticky top-0 z-30 bg-surface/85 backdrop-blur-md">
        <div
          className={`mx-auto flex ${measure} items-center justify-between gap-4 px-4 pt-4 sm:px-6`}
        >
          <div className="flex items-center gap-3">
            <Mark />
            <div>
              <h1 className="m-0 text-xl leading-6 font-normal text-on-surface">claudication</h1>
              <p className="mt-0.5 mb-0 text-xs text-on-surface-variant">
                Claude API gateway
                <Version />
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {docsURL !== '' && (
              // Opens in a tab of its own: it is a different page on a
              // different address, and the person reading it is usually
              // copying out of it while doing something else here.
              <TextLink href={docsURL}>Docs</TextLink>
            )}
            <IconLink label="Source on GitHub" href={REPO}>
              <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
            </IconLink>
            <LiveToggle />
            <ThemeToggle />
            <TextButton onClick={() => void signOut()}>Sign out</TextButton>
          </div>
        </div>

        <nav className={`mx-auto ${measure} overflow-x-auto px-4 sm:px-6`}>
          <div className="flex min-w-max border-b border-outline-variant">
            {TABS.map((id) => (
              <a
                key={id}
                href={id === 'overview' ? '/' : `/${id}`}
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

      <main className={`mx-auto ${measure} px-4 pt-6 pb-16 sm:px-6`}>
        {tab === 'overview' && <Overview onExpired={expired} onGoTo={setTab} docsURL={docsURL} />}
                {tab === 'accounts' && <Accounts onExpired={expired} />}
        {tab === 'keys' && <Keys onExpired={expired} onMinted={setMinted} />}
        {tab === 'usage' && <UsagePanel onExpired={expired} />}
        {tab === 'requests' && <RequestsPanel onExpired={expired} />}
        {tab === 'settings' && (
          <Settings
            session={state.session}
            onSessionChanged={() => void probe()}
            config={config}
            onConfigChanged={() => void config.reload()}
          />
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

const REPO = 'https://github.com/nebuloss/claudication'

/**
 * The running build, beside the name.
 *
 * In the header rather than on the Settings tab, because the question it
 * answers — "is this the version I just deployed?" — is asked from whichever
 * screen happens to be open, and usually right after a deploy.
 *
 * Its own loader, so it mounts only once there is a session to load it with,
 * and renders nothing at all until it has one: an empty gap is better than a
 * header that reflows the moment the request lands.
 */
function Version() {
  const { data } = useLoader<OverviewData>(() => api.overview())
  if (data === null) return null
  return (
    <>
      {' · '}
      <span className="font-mono">{data.version}</span>
    </>
  )
}

/**
 * One switch for every panel's polling.
 *
 * Worth having rather than assuming: the gateway may be reached over a metered
 * link or a tunnel someone is paying for by the byte, and a dashboard left open
 * on a spare monitor would otherwise talk all day. Turning it off leaves every
 * panel's Refresh button, so nothing becomes unreachable — it only stops being
 * automatic.
 */
function LiveToggle() {
  const [live, setLive] = useLive()
  return (
    <button
      type="button"
      onClick={() => setLive(!live)}
      aria-pressed={live}
      title={
        live
          ? 'Panels are keeping themselves up to date. Click to stop.'
          : 'Panels only update when you ask. Click to resume.'
      }
      className={`state-layer inline-flex h-10 items-center gap-2 rounded-[var(--radius-md3-full)] border border-outline px-3 text-sm font-medium ${
        live ? 'text-primary' : 'text-on-surface-variant'
      }`}
    >
      <span
        className={`size-1.5 shrink-0 rounded-full ${live ? 'bg-primary' : 'bg-on-surface-variant/50'}`}
        aria-hidden
      />
      Live
    </button>
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
