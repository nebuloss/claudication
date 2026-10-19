import { useCallback, useEffect, useRef, useState } from 'react'
import { messageOf } from '../api/client'
import { canonical, segments } from './route'
import { Runs } from './runs'
import { useSession } from './session'

/**
 * Where we are: the path's segments, after canonical() has completed it to a
 * page — so /accounts/<id> reads as /accounts/<id>/usage, and the address bar
 * is corrected to say so (replaceState: the short form is the same page, not
 * a step of history to walk back through).
 *
 * Still no router dependency. A read off location, one listener and one
 * pushState is the whole requirement.
 */
export function usePath(): string[] {
  const read = useCallback(() => {
    const { pathname, search, hash } = window.location
    const want = canonical(pathname)
    if (want !== pathname) window.history.replaceState(null, '', want + search + hash)
    return want
  }, [])
  const [path, setPath] = useState(read)
  useEffect(() => {
    const onPop = () => setPath(read())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [read])
  return segments(path)
}

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
 *     A change of `deps` or a reload is different: it is a new question, so
 *     it aborts whatever is in flight and asks it (see runs.ts).
 *   - A background failure does not replace the panel with an error. It keeps
 *     the last good figures on screen and lets the next tick recover, because
 *     one dropped poll is not worth losing what you were reading.
 */
export function useLoader<T>(
  load: (signal: AbortSignal) => Promise<T>,
  { deps = [], refreshMs = 0 }: { deps?: unknown[]; refreshMs?: number } = {},
) {
  const loadRef = useRef(load)
  loadRef.current = load
  const session = useSession()
  const [runs] = useState(() => new Runs())

  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [updatedAt, setUpdatedAt] = useState(0)

  const [live] = useLive()

  const run = useCallback(async (background: boolean) => {
    const ticket = runs.begin(background ? 'background' : 'replace')
    if (ticket === null) return
    // Cleared first, so a retry does not render the last failure while it is
    // in flight. Anything that reloads is saying "try again", and showing the
    // previous error until the new answer lands reads as the retry having
    // failed instantly. A background poll leaves it alone: it is not the user
    // asking, and clearing would flicker an error off and back on.
    if (background) setRefreshing(true)
    else setError('')
    try {
      const value = await loadRef.current(ticket.signal)
      // A newer run has the turn, so this answer is to a question nobody is
      // asking any more.
      if (!runs.owns(ticket)) return
      setData(value)
      setError('')
      setUpdatedAt(Date.now())
    } catch (err) {
      if (!runs.owns(ticket)) return
      // A 401 means the session lapsed while the tab was open. Only the shell
      // can show a sign-in screen, so it decides what happens next.
      if (session.report(err)) return
      if (!background) setError(messageOf(err))
    } finally {
      // Only the run that still holds the turn settles the panel: one that was
      // pre-empted must not take the spinner off its successor.
      if (runs.end(ticket)) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [runs, session])

  const reload = useCallback(() => run(false), [run])

  useEffect(() => {
    void run(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  useEffect(() => () => runs.cancel(), [runs])

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
 * A write, with the two states every button that makes one needs: which one
 * is in flight, and what went wrong.
 *
 * About fifteen mutations wrote this out by hand and each got a different part
 * of it wrong. Some reset the busy state only on the error path, which is fine
 * until success re-renders rather than unmounts — Change password sat on
 * "Changing…" for ever that way, with both passwords still on screen. Some
 * spelled the message their own way rather than through messageOf, and only
 * one of them sent a lapsed session to the sign-in screen instead of printing
 * "session has expired" in a banner.
 *
 * So `run(key, fn)` always clears the busy state, however `fn` ends; reports a
 * failure through messageOf; and hands a lapsed session to the shell. It says
 * whether `fn` succeeded, for the callers that undo an optimistic change.
 *
 * `key` names which of a panel's actions is running, so one row can show a
 * spinner while the others are merely disabled; a panel with one action
 * passes any name and reads `pending`.
 *
 * `asksPassword` is for the two writes that check the password again. Their
 * 401 means it was mistyped, and signing the operator out over a typo would be
 * the wrong answer to it.
 */
export function useAction<K extends string = string>({
  asksPassword = false,
}: { asksPassword?: boolean } = {}) {
  const session = useSession()
  const [busy, setBusy] = useState<K | ''>('')
  const [error, setError] = useState('')

  const run = useCallback(
    async (key: K, fn: () => Promise<unknown>): Promise<boolean> => {
      setBusy(key)
      setError('')
      try {
        await fn()
        return true
      } catch (err) {
        if (!asksPassword && session.report(err)) return false
        setError(messageOf(err))
        return false
      } finally {
        setBusy('')
      }
    },
    [asksPassword, session],
  )

  return { busy, pending: busy !== '', error, setError, run }
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
