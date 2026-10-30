import { useCallback, useEffect, useState } from 'react'
import Accounts from './features/accounts'
import AccountPage from './features/accounts/page'
import Activity from './features/activity'
import Clients, { ClientPage } from './features/clients'
import Overview from './features/overview'
import Settings from './features/settings'
import SignIn from './features/sign-in'
import ThemeToggle from './features/theme-toggle'
import { usePath, useLoader, useUpdateAvailable } from './hooks'
import { nameOf, useNames } from './names'
import { SessionProvider } from './session'
import {
  ACCOUNT_PAGES,
  ACTIVITY_PAGES,
  CLIENT_PAGES,
  SETTINGS_PAGES,
  navLevel,
  pageIdOf,
  pathOf,
  reloadOnNextNavigation,
  tabOf,
  trailOf,
} from './route'
// OverviewData, because Overview is already the panel above.
import {
  ApiError,
  api,
  type GatewayConfig,
  type Overview as OverviewData,
  type Session,
} from '../api/client'
import {
  AppTitle,
  Breadcrumbs,
  Button,
  ConfirmProvider,
  IconLink,
  NavRail,
  Spinner,
  VersionTag,
} from './primitives'
import { READS } from '../api/live'
import { NoticeBell } from './features/notices'

type State =
  | { phase: 'probing' }
  | { phase: 'setup'; minLength: number; version: string }
  | { phase: 'out'; version: string }
  | { phase: 'in'; session: Session }

// Every page is a path, and the path is the tree: route.ts lists it, and
// completes a path that names a branch (/activity) to the branch's first page.

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
  // Where we are, completed to a page: [tab, item or page, page].
  const path = usePath()
  const here = pathOf(...path)
  const tab = tabOf(here)
  const [, item = '', page = ''] = path
  // The trail and the rail name an account or a key once its page has said
  // what it is called.
  useNames()
  // The configuration, loaded once for the whole signed-in app and handed to
  // Settings, which renders it and reloads it after a switch.
  //
  // Declared with the other hooks so the order never changes across the early
  // returns below, and allowed to fail — signed out this 401s.
  //
  // Keyed on the phase, which is the part that bit: without it this ran once,
  // on mount, while the shell was still probing or showing a sign-in screen —
  // so it 401'd, set an error, and never tried again once a session existed.
  // Settings then rendered "Could not read the configuration" at a signed-in
  // operator forever.
  const config = useLoader<GatewayConfig>((signal) => api.gateway.config(signal), {
    deps: [state.phase],
    on: state.phase === 'in' ? READS.config : [],
  })
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
  }, [updated])

  const probe = useCallback(async () => {
    try {
      setState({ phase: 'in', session: await api.session.whoAmI() })
      return
    } catch (err) {
      if (!(err instanceof ApiError) || !err.isUnauthenticated) {
        // Worth knowing about, but a sign-in screen is still where to land.
        console.warn('session probe failed:', err)
      }
    }
    try {
      const status = await api.session.setupStatus()
      setState(
        status.needs_setup
          ? { phase: 'setup', minLength: status.min_password_len, version: status.version ?? '' }
          : { phase: 'out', version: status.version ?? '' },
      )
    } catch (err) {
      // If even that is unreachable, sign-in is the screen that can report it.
      console.warn('setup probe failed:', err)
      setState({ phase: 'out', version: '' })
    }
  }, [])

  useEffect(() => {
    void probe()
  }, [probe])

  // A lapsed session, from anywhere signed in, re-probes: that lands on the
  // sign-in screen, or on setup if the account was deleted from elsewhere.
  // Stable, so the provider installs it once rather than on every render.
  const expired = useCallback(() => void probe(), [probe])

  if (state.phase === 'probing') {
    return (
      <div className="grid min-h-dvh place-items-center text-on-surface-variant">
        <Spinner className="size-6" />
      </div>
    )
  }
  if (state.phase === 'setup') {
    return <SignIn
        mode="setup"
        minLength={state.minLength}
        version={state.version}
        onSignedIn={() => void probe()}
      />
  }
  if (state.phase === 'out') {
    return <SignIn
        mode="sign-in"
        version={state.version}
        onSignedIn={() => void probe()}
      />
  }

  const signOut = async () => {
    try {
      await api.session.signOut()
    } catch {
      // Signing out locally matters more than the round trip succeeding.
    }
    void probe()
  }
  // One value for the header, the nav and the page, so the tab underline stays
  // over its tab when the measure changes.
  const measure = MEASURE
  const level = navLevel(here)
  const trail = trailOf(here, nameOf)

  return (
    <SessionProvider onExpired={expired}>
      <ConfirmProvider>
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
              <AppTitle version={<Version />} />
              {/* Wraps onto a second row on a phone rather than shrink. */}
              <div className="flex min-w-0 flex-wrap items-center justify-end gap-2 whitespace-nowrap">
                <IconLink label="Source on GitHub" href={REPO}>
                  <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
                </IconLink>
                <NoticeBell />
                <ThemeToggle />
                <Button variant="text" icon="logout" onClick={() => void signOut()}>
                  Sign out
                </Button>
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
              {tab === 'overview' && <Overview />}
              {/* Keyed by the item, so moving from one account's page to another's
                  starts clean rather than showing the last one's state. */}
              {tab === 'accounts' &&
                (item === '' ? (
                  <Accounts />
                ) : (
                  <AccountPage key={item} id={item} page={pageIdOf(ACCOUNT_PAGES, page)} />
                ))}
              {tab === 'clients' &&
                (item === '' ? (
                  <Clients />
                ) : (
                  <ClientPage key={item} id={item} page={pageIdOf(CLIENT_PAGES, page)} config={config.data} />
                ))}
              {tab === 'activity' && <Activity page={pageIdOf(ACTIVITY_PAGES, item)} />}
              {tab === 'settings' && (
                <Settings
                  page={pageIdOf(SETTINGS_PAGES, item)}
                  session={state.session}
                  onSessionChanged={() => void probe()}
                  config={config}
                  onConfigChanged={() => void config.reload()}
                />
              )}
            </main>
          </div>

        </div>
      </ConfirmProvider>
    </SessionProvider>
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
  const { data } = useLoader<OverviewData>((signal) => api.gateway.overview(signal))
  if (data === null) return null
  return <VersionTag>{data.version}</VersionTag>
}
