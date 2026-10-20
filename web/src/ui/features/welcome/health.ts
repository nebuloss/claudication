import { useEffect, useState } from 'react'

/**
 * The two fields of /health the welcome page reads, and its own way of asking.
 *
 * Not the admin app's API client or its loader hook. Those modules carry every
 * admin path and are what the welcome bundle must not contain: this page is
 * public, and whatever it imports is readable by anyone who opens it. A fetch
 * of one URL is a few lines; keeping it here is what keeps the import graph of
 * the public page down to React, the primitives and this folder.
 */
export type HealthState =
  | { kind: 'checking' }
  | { kind: 'unreachable'; at: number }
  | { kind: 'answered'; ok: boolean; accepting: boolean; at: number }

/**
 * How often the status is asked again. A status only as fresh as the page load
 * says "accepting" about a gateway that was switched off an hour ago, and
 * /health costs nothing to answer.
 */
export const REFRESH_MS = 30_000

async function ask(signal: AbortSignal): Promise<HealthState> {
  try {
    const res = await fetch('/health', { signal, cache: 'no-store', headers: { Accept: 'application/json' } })
    if (!res.ok) return { kind: 'unreachable', at: Date.now() }
    const body = (await res.json()) as { status?: unknown; accepting?: unknown }
    return {
      kind: 'answered',
      ok: body.status === 'ok',
      // Absent from a gateway older than the switch, which always accepted.
      accepting: body.accepting !== false,
      at: Date.now(),
    }
  } catch {
    return { kind: 'unreachable', at: Date.now() }
  }
}

/**
 * The gateway's state, asked on load and every REFRESH_MS while the tab is
 * visible, and again as soon as it is shown: a background tab polling all day
 * tells nobody anything.
 */
export function useHealth(): HealthState {
  const [state, setState] = useState<HealthState>({ kind: 'checking' })

  useEffect(() => {
    let live = new AbortController()
    const run = () => {
      live.abort()
      live = new AbortController()
      const mine = live
      void ask(mine.signal).then((s) => {
        if (!mine.signal.aborted) setState(s)
      })
    }
    run()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') run()
    }, REFRESH_MS)
    const onShow = () => {
      if (document.visibilityState === 'visible') run()
    }
    document.addEventListener('visibilitychange', onShow)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onShow)
      live.abort()
    }
  }, [])

  return state
}
