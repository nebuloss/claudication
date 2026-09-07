import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, messageOf } from '../api/client'

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
 * Tab state kept in location.hash, so every tab is linkable and survives a
 * reload — cheaper than pulling in a router for five panels.
 */
export function useHashTab<T extends string>(tabs: readonly T[], fallback: T) {
  const read = useCallback((): T => {
    const h = window.location.hash.replace(/^#/, '').split('&')[0]
    return (tabs as readonly string[]).includes(h) ? (h as T) : fallback
  }, [tabs, fallback])

  const [tab, setTab] = useState<T>(read)

  useEffect(() => {
    const onHash = () => setTab(read())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [read])

  const select = useCallback((t: T) => {
    window.location.hash = t
    setTab(t)
  }, [])

  return [tab, select] as const
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
