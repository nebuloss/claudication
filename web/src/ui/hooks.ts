import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, messageOf } from '../api/client'
import { panelFromHash, pathForTab, tabFromPath } from './route'

/** Close on Escape — same contract as the hook in singbox-admin. */
export function useEscapeKey(handler: () => void, enabled = true) {
  useEffect(() => {
    if (!enabled) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') handler()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [handler, enabled])
}

/**
 * Where we are: the screen in the path, the panel within it in the hash.
 *
 * Two levels, and they are addressed differently on purpose. `/usage` is a
 * place — it is what you bookmark, paste to someone, and land on from a link —
 * so it belongs in the path where a URL bar shows it plainly. `#chats` is
 * which panel of that place is open, which is a detail of the view rather than
 * a different page, and the hash is exactly what the web already means by
 * that. Together they make `/usage#chats` a link straight to one sub-tab,
 * which is the whole point.
 *
 * Still no router dependency. Two reads off `location` and one `pushState` is
 * the entire requirement, and a library for six screens would be more code
 * than this.
 *
 * The server already cooperates: an extension-less path that matches no asset
 * serves index.html, so a deep link loads the app rather than 404ing. See
 * staticHandler.
 */

/** The screen, from the first path segment. */
export function usePathTab<T extends string>(tabs: readonly T[], fallback: T) {
  const read = useCallback(
    (): T => tabFromPath(window.location.pathname, tabs, fallback),
    [tabs, fallback],
  )

  const [tab, setTab] = useState<T>(read)

  useEffect(() => {
    // Back and forward have to work, and they are the reason this is
    // pushState rather than assigning location: assigning reloads the whole
    // app to change a tab.
    const onPop = () => setTab(read())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [read])

  const select = useCallback(
    (t: T) => {
      // The hash belongs to the screen being left, so it goes with it. Keeping
      // it would land on Settings with #chats still in the URL, naming a panel
      // that is not there.
      const path = pathForTab(t, fallback)
      if (window.location.pathname + window.location.hash !== path) {
        window.history.pushState(null, '', path)
      }
      setTab(t)
      // A new screen starts at the top; carrying the scroll position across is
      // how you arrive halfway down a page you have not seen.
      window.scrollTo(0, 0)
    },
    [fallback],
  )

  return [tab, select] as const
}

/**
 * The panel within the screen, from the hash.
 *
 * replaceState rather than push: flipping between panels of one screen is not
 * six entries of history to walk back through, and the browser's Back should
 * leave the screen rather than step through its tabs.
 */
export function useHashPanel<T extends string>(panels: readonly T[], fallback: T) {
  const read = useCallback(
    (): T => panelFromHash(window.location.hash, panels, fallback),
    [panels, fallback],
  )

  const [panel, setPanel] = useState<T>(read)

  useEffect(() => {
    const onNav = () => setPanel(read())
    window.addEventListener('hashchange', onNav)
    window.addEventListener('popstate', onNav)
    return () => {
      window.removeEventListener('hashchange', onNav)
      window.removeEventListener('popstate', onNav)
    }
  }, [read])

  const select = useCallback((p: T) => {
    window.history.replaceState(null, '', `${window.location.pathname}#${p}`)
    setPanel(p)
  }, [])

  return [panel, select] as const
}

const LIVE_KEY = 'claudication.live'
const LIVE_EVENT = 'claudication:live'

function readLive(): boolean {
  try {
    // On by default: a dashboard that silently shows an hour-old number is
    // worse than one that costs a request every few seconds.
    return window.localStorage.getItem(LIVE_KEY) !== 'off'
  } catch {
    return true
  }
}

/**
 * Whether panels keep themselves up to date, as one setting shared by all of
 * them and remembered across reloads.
 *
 * The custom event is what makes it one setting rather than several: `storage`
 * only fires in *other* tabs, so without it the header toggle would update its
 * own label and leave every panel in this tab polling regardless.
 */
export function useLive(): [boolean, (on: boolean) => void] {
  const [live, setLiveState] = useState(readLive)

  useEffect(() => {
    const sync = () => setLiveState(readLive())
    window.addEventListener(LIVE_EVENT, sync)
    window.addEventListener('storage', sync)
    return () => {
      window.removeEventListener(LIVE_EVENT, sync)
      window.removeEventListener('storage', sync)
    }
  }, [])

  const setLive = useCallback((on: boolean) => {
    try {
      window.localStorage.setItem(LIVE_KEY, on ? 'on' : 'off')
    } catch {
      // Private browsing, or a storage quota. The setting still applies to
      // this tab; it just will not be remembered.
    }
    window.dispatchEvent(new Event(LIVE_EVENT))
  }, [])

  return [live, setLive]
}

/**
 * Load something, with the three states every panel needs.
 *
 * `load` is held in a ref so `reload` stays stable across renders — a panel can
 * hand it to a child as an onChanged callback without re-running the effect on
 * every render. `deps` is what actually re-fetches: pass the window size, the
 * filter, whatever the request is a function of.
 *
 * `loading` is only ever true for the first fetch. A manual refresh leaves the
 * previous data on screen rather than replacing a populated panel with a
 * spinner for a few hundred milliseconds.
 *
 * Pass `refreshMs` and the panel keeps itself current. Three things keep that
 * from being a nuisance:
 *
 *   - A hidden tab does not poll. Browsers throttle background timers anyway,
 *     and a laptop that wakes after a night asleep would otherwise fire a
 *     backlog of them at once; becoming visible re-fetches instead.
 *   - Only one request is ever in flight. A poll that outlives its interval —
 *     the usage report over a slow link — must not queue a second behind it.
 *   - A background failure does not replace the panel with an error. It keeps
 *     the last good figures on screen and lets the next tick recover, because
 *     one dropped poll is not worth losing what you were reading.
 */
export function useLoader<T>(
  load: () => Promise<T>,
  onUnauthenticated?: () => void,
  deps: unknown[] = [],
  refreshMs = 0,
) {
  const loadRef = useRef(load)
  loadRef.current = load
  const authRef = useRef(onUnauthenticated)
  authRef.current = onUnauthenticated
  const inFlight = useRef(false)

  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [updatedAt, setUpdatedAt] = useState(0)

  const [live] = useLive()

  const run = useCallback(async (background: boolean) => {
    if (inFlight.current) return
    inFlight.current = true
    // Cleared first, so a retry does not render the last failure while it is
    // in flight. Anything that reloads is saying "try again", and showing the
    // previous error until the new answer lands reads as the retry having
    // failed instantly. A background poll leaves it alone: it is not the user
    // asking, and clearing would flicker an error off and back on.
    if (background) setRefreshing(true)
    else setError('')
    try {
      setData(await loadRef.current())
      setError('')
      setUpdatedAt(Date.now())
    } catch (err) {
      // A 401 means the session lapsed while the tab was open. Only the shell
      // can show a sign-in screen, so it decides what happens next.
      if (authRef.current !== undefined && err instanceof ApiError && err.isUnauthenticated) {
        authRef.current()
        return
      }
      if (!background) setError(messageOf(err))
    } finally {
      inFlight.current = false
      setLoading(false)
      setRefreshing(false)
    }
  }, [])

  const reload = useCallback(() => run(false), [run])

  useEffect(() => {
    void run(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  useEffect(() => {
    if (refreshMs <= 0 || !live) return

    const tick = () => {
      if (document.visibilityState === 'visible') void run(true)
    }
    const timer = window.setInterval(tick, refreshMs)
    // Coming back to the tab should show current figures, not wait out an
    // interval that has been stalled for however long it was hidden.
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', tick)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshMs, live, run, ...deps])

  return { data, error, loading, reload, setError, refreshing, updatedAt, live }
}

/**
 * A clock that ticks only as often as something needs re-rendering.
 *
 * "Updated 12s ago" is a lie the moment it is painted unless something
 * re-renders it, but it is not worth a render a second either. One a second
 * for the first minute is what the labels need; past that they are counting
 * minutes and nobody is watching that closely.
 */
export function useNow(everyMs = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') setNow(Date.now())
    }, everyMs)
    return () => window.clearInterval(timer)
  }, [everyMs])
  return now
}

/** The entry script a copy of index.html loads, or '' when there is none. */
function entryScript(html: string): string {
  return /<script[^>]*type="module"[^>]*src="([^"]+)"/.exec(html)?.[1] ?? ''
}

/**
 * Whether the gateway now serves a newer UI than the one this tab is running.
 *
 * The admin UI is one page: live refresh re-fetches data, never the code, so a
 * tab opened before a deploy keeps running the old bundle and simply does not
 * have whatever the deploy added. That cost a hard reload and an "I don't see
 * it" on 2026-09-25.
 *
 * Compares the entry script this page was loaded with against the one
 * index.html names now. Asset names carry a content hash, so this changes
 * exactly when the UI does, and not on a gateway-only release. It fetches the
 * static page rather than an admin endpoint, so it does not count as someone
 * watching the usage figures; index.html is served no-cache with an ETag, so an
 * unchanged answer is a 304.
 *
 * Checked once a minute while the tab is visible and Live is on, and whenever
 * the tab becomes visible again, which is when someone is about to look.
 */
export function useUpdateAvailable(everyMs = 60_000): boolean {
  const [stale, setStale] = useState(false)
  const [live] = useLive()

  useEffect(() => {
    const running =
      document
        .querySelector<HTMLScriptElement>('script[type="module"][src]')
        ?.getAttribute('src') ?? ''
    if (running === '' || stale) return

    const check = async () => {
      if (document.visibilityState !== 'visible') return
      try {
        const res = await fetch('/', { cache: 'no-cache', credentials: 'same-origin' })
        if (!res.ok) return
        const served = entryScript(await res.text())
        if (served !== '' && served !== running) setStale(true)
      } catch {
        // Offline, or the gateway mid-restart. The next check will tell.
      }
    }

    const timer = live ? window.setInterval(() => void check(), everyMs) : 0
    const onVisible = () => void check()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      if (timer !== 0) window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [everyMs, live, stale])

  return stale
}
