import { useEffect, useState } from 'react'

/**
 * The availability report: GET /status.json, which the public welcome page and
 * the admin Overview both draw.
 *
 * Fetched here with a few lines of its own rather than through the admin API
 * client, because the welcome page imports this file and must not import that
 * one: the client names every admin path, and whatever the public page
 * imports, anyone can read. See internal/service/uptime for what the fields
 * mean and docs/status-page.md for why each may be public.
 */

export type UptimeState = 'up' | 'degraded' | 'paused' | 'down' | 'none'

export interface Bucket {
  /** Unix milliseconds, the start of a UTC hour or day. */
  start: number
  /** The worst state in it. */
  state: UptimeState
  /** The share of it that was up, out of the time it was not paused. */
  up: number | null
}

export interface Incident {
  start: number
  /** Null while it goes on. */
  end: number | null
  state: 'degraded' | 'down'
}

export interface StatusReport {
  state: 'up' | 'degraded' | 'paused'
  uptime: { '24h': number | null; '7d': number | null; '90d': number | null }
  hours: Bucket[]
  days: Bucket[]
  incidents: Incident[]
  generated: number
}

export type StatusResult =
  | { kind: 'checking' }
  | { kind: 'unreachable'; at: number }
  | { kind: 'answered'; report: StatusReport; at: number }

/**
 * How often the report is asked again: the server rebuilds it once a minute
 * and says so to caches, so asking more often would only read the same answer.
 */
export const REFRESH_MS = 60_000

async function ask(signal: AbortSignal): Promise<StatusResult> {
  try {
    const res = await fetch('/status.json', { signal, headers: { Accept: 'application/json' } })
    if (!res.ok) return { kind: 'unreachable', at: Date.now() }
    return { kind: 'answered', report: (await res.json()) as StatusReport, at: Date.now() }
  } catch {
    return { kind: 'unreachable', at: Date.now() }
  }
}

/**
 * The report, asked on load, every REFRESH_MS while the tab is visible, and as
 * soon as it is shown again: a background tab polling all day tells nobody
 * anything. A failed ask after a good one keeps the last report on screen and
 * says it could not refresh, rather than blanking the history.
 */
export function useStatus(): { result: StatusResult; last: StatusReport | null } {
  const [result, setResult] = useState<StatusResult>({ kind: 'checking' })
  const [last, setLast] = useState<StatusReport | null>(null)

  useEffect(() => {
    let live = new AbortController()
    const run = () => {
      live.abort()
      live = new AbortController()
      const mine = live
      void ask(mine.signal).then((r) => {
        if (mine.signal.aborted) return
        setResult(r)
        if (r.kind === 'answered') setLast(r.report)
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

  return { result, last }
}

/**
 * A share as a percentage, rounded down: 99.996% up is not 100%, and a status
 * page that rounds an outage away is the one thing it must not do.
 */
export function percent(f: number | null): string {
  if (f === null) return '—'
  if (f >= 1) return '100%'
  const p = Math.floor(f * 10000) / 100
  return `${p.toFixed(p >= 10 ? 2 : 1)}%`
}

/** How long a stretch lasted, in the largest two units that matter. */
export function lasted(ms: number): string {
  const m = Math.max(1, Math.round(ms / 60_000))
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 === 0 ? `${h} h` : `${h} h ${m % 60} min`
  const d = Math.floor(h / 24)
  return h % 24 === 0 ? `${d} d` : `${d} d ${h % 24} h`
}
