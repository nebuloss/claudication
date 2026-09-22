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
 */
export function useLoader<T>(
  load: () => Promise<T>,
  onUnauthenticated?: () => void,
  deps: unknown[] = [],
) {
  const loadRef = useRef(load)
  loadRef.current = load
  const authRef = useRef(onUnauthenticated)
  authRef.current = onUnauthenticated

  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  const reload = useCallback(async () => {
    // Cleared first, so a retry does not render the last failure while it is
    // in flight. Anything that reloads is saying "try again", and showing the
    // previous error until the new answer lands reads as the retry having
    // failed instantly.
    setError('')
    try {
      setData(await loadRef.current())
      setError('')
    } catch (err) {
      // A 401 means the session lapsed while the tab was open. Only the shell
      // can show a sign-in screen, so it decides what happens next.
      if (authRef.current !== undefined && err instanceof ApiError && err.isUnauthenticated) {
        authRef.current()
        return
      }
      setError(messageOf(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, error, loading, reload, setError }
}
