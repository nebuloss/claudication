import { useCallback, useEffect, useState } from 'react'
import Accounts from './features/accounts'
import AccountPage from './features/account-page'
import Activity from './features/activity'
import Keys, { KeyPage } from './features/keys'
import { KeyConfigs } from './features/key-configs'
import Overview from './features/overview'
import Settings from './features/settings'
import SignIn from './features/sign-in'
import ThemeToggle from './features/theme-toggle'
import { usePath, useLive, useLoader, useUpdateAvailable } from './hooks'
import { nameOf, useNames } from './names'
import { navLevel, pathOf, reloadOnNextNavigation, tabOf, trailOf } from './route'
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
  Breadcrumbs,
  NavRail,
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
// Every page is a path, and the path is the tree: route.ts lists it, and
// completes a path that names a branch (/accounts/<id>, /settings) to the
// branch's first page. The model aliases are a section of Settings, and old
// paths (/models, /chats, /requests, /usage) are moved to where they went.

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
  // Where we are, completed to a page: [tab, item or page, page, section].
  const path = usePath()
  const here = pathOf(...path)
  const tab = tabOf(here)
  const [, item = '', page = '', section = ''] = path
  // The trail and the rail name an account or a key once its page has said
  // what it is called.
  useNames()
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
  // A key being shown — new, re-issued, or asked for again — lives here, not in
  // the panel that asked: panels unmount on a tab change and on Back, and the
  // dialog should outlive both.
  const [minted, setMinted] = useState<{ key: ApiKey; plaintext: string } | null>(null)
  // A deploy since this tab loaded is taken without asking, at the first
  // moment it loses nothing: at once when nothing is being typed or shown, or
  // else on the next move to another page, or when the tab is left with
  // nothing open. What it must never throw away is a form half filled in or a
  // key on screen being copied.
  const updated = useUpdateAvailable()
  useEffect(() => {
    if (!updated) return
    reloadOnNextNavigation()
    const safe = () =>
      minted === null &&
      document.querySelector('form, [role="dialog"], [aria-modal="true"]') === null
    if (safe()) {
      window.location.reload()
      return
    }
    const onHide = () => {
      if (document.visibilityState === 'hidden' && safe()) window.location.reload()
    }
    document.addEventListener('visibilitychange', onHide)
    return () => document.removeEventListener('visibilitychange', onHide)
  }, [updated, minted])

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
  const level = navLevel(here, nameOf)
  const trail = trailOf(here, nameOf)

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
          {/* One line, the height of the controls beside it: the name, and the
              running build, which is asked for right after a deploy. */}
          <div className="flex min-w-0 items-center gap-3">
            <Mark />
            <h1 className="m-0 flex min-w-0 items-baseline gap-2 text-lg leading-10 font-normal whitespace-nowrap text-on-surface">
              claudication gateway
              <Version />
            </h1>
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

        {/* Where this page is: every level above it a link. The tabs that
            used to be here are the rail's top level now. */}
        <div className={`mx-auto ${measure} px-4 pt-3 pb-3 sm:px-6`}>
          <Breadcrumbs trail={trail} />
        </div>
      </header>

      {/* One navigation for every page: the trail above, and beside the page
          the choices of its own level with the way up. A row above the page
          on a narrow screen. */}
      {/* The top padding is the row's, once, and both sides start at the
          row's top: padded separately, the rail's panel sat 6px below the
          page's first card. */}
      <div className={`mx-auto ${measure} px-4 sm:px-6 md:flex md:items-start md:gap-8 md:pt-6`}>
        <aside className="hidden w-52 shrink-0 md:sticky md:top-32 md:block">
          <NavRail level={level} />
        </aside>
        <div className="pt-4 md:hidden">
          <NavRail level={level} row />
        </div>
        <main className="min-w-0 flex-1 pt-6 pb-16 md:pt-0">
          {tab === 'overview' && <Overview onExpired={expired} docsURL={docsURL} />}
          {/* Keyed by the item, so moving from one account's page to another's
              starts clean rather than showing the last one's state. */}
          {tab === 'accounts' &&
            (item === '' ? (
              <Accounts onExpired={expired} />
            ) : (
              <AccountPage key={item} id={item} page={page} section={section} onExpired={expired} />
            ))}
          {tab === 'keys' &&
            (item === '' ? (
              <Keys onExpired={expired} onMinted={setMinted} />
            ) : (
              <KeyPage
                key={item}
                id={item}
                page={page}
                section={section}
                onExpired={expired}
                onMinted={setMinted}
              />
            ))}
          {tab === 'activity' && <Activity page={item} onExpired={expired} />}
          {tab === 'settings' && (
            <Settings
              section={item}
              session={state.session}
              onSessionChanged={() => void probe()}
              config={config}
              onConfigChanged={() => void config.reload()}
            />
          )}
        </main>
      </div>

      {minted !== null && (
        <Modal title={minted.key.name} size="lg" onClose={() => setMinted(null)}>
          <Banner className="mb-4">
            Copy the key, or download a client&rsquo;s config with it inside. It is kept sealed
            with the gateway&rsquo;s state, so its settings page can show it and its configs again
            later.
          </Banner>
          <CopyField label="API key" value={minted.plaintext} />
          <KeyConfigs plaintext={minted.plaintext} config={config.data} keyId={minted.key.id} />
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
    <span className="truncate font-mono text-xs text-on-surface-variant">{data.version}</span>
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
