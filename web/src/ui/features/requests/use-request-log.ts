import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, messageOf, type RequestRow } from '../../../api/client'
import { usePoll } from '../../hooks'
import { Runs } from '../../runs'
import { useSession } from '../../session'
import type { RequestFilter } from './filter'
import { PAGE, RequestLogPager } from './pager'
import { RequestRecord } from './record'

/** The activity list's whole subject is what just happened, so it polls often. */
const ACTIVITY_REFRESH_MS = 10_000

/**
 * The request log for a filter: its pager, the three ways it is loading, and
 * the two things an operator can ask of it.
 *
 * Its requests take turns through Runs, like every loader's: a first page —
 * a new filter, or Refresh — pre-empts whatever is in flight, because the old
 * filter's rows are not wanted any more; Load more pre-empts only a poll; a
 * poll never jumps the queue. Polling is usePoll's, so a hidden tab and Live
 * off behave here as they do on every other panel.
 */
export function useRequestLog(filter: RequestFilter, onPolled: () => void) {
  const session = useSession()
  const [runs] = useState(() => new Runs())
  useEffect(() => () => runs.cancel(), [runs])

  const [pager, setPager] = useState(() => new RequestLogPager<RequestRow>())
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [updatedAt, setUpdatedAt] = useState(0)

  // Keyed by the query, not the object: a parent re-rendering hands down an
  // equal filter that is a new value, and that is not a new question.
  const query = filter.query()
  const params = useMemo(() => filter.params(), [query])

  const load = useCallback(
    async (after: string) => {
      const first = after === ''
      const run = runs.begin(first ? 'replace' : 'append')
      if (run === null) return
      // Whatever this pre-empted will not settle its own spinner.
      setRefreshing(false)
      if (first) {
        setLoading(true)
        setLoadingMore(false)
      } else {
        setLoadingMore(true)
      }
      setError('')
      try {
        const page = await api.recentRequests(PAGE, after, params, run.signal)
        if (!runs.owns(run)) return
        // Replaced when refreshing, so Refresh cannot leave a stale tail
        // below freshly loaded rows; appended when paging.
        setPager((p) => (first ? RequestLogPager.first(page) : p.more(page)))
        setUpdatedAt(Date.now())
      } catch (err) {
        if (!runs.owns(run)) return
        if (session.report(err)) return
        setError(messageOf(err))
      } finally {
        if (runs.end(run)) {
          setLoading(false)
          setLoadingMore(false)
        }
      }
    },
    [session, params, runs],
  )

  const poll = useCallback(async () => {
    const run = runs.begin('background')
    if (run === null) return
    setRefreshing(true)
    try {
      const page = await api.recentRequests(PAGE, '', params, run.signal)
      // Checked before merging: a poll that a new filter overtook holds the
      // old filter's rows, and merging them would put them under the new
      // filter's chips.
      if (!runs.owns(run)) return
      setPager((p) => p.merged(page))
      setUpdatedAt(Date.now())
      onPolled()
    } catch {
      // Keep what is on screen; the next tick tries again. Only a refresh the
      // operator asked for is worth reporting as a failure.
    } finally {
      if (runs.end(run)) setRefreshing(false)
    }
  }, [params, runs, onPolled])

  const live = usePoll(() => void poll(), ACTIVITY_REFRESH_MS)

  useEffect(() => {
    // From the first page: the cursor belongs to the previous filter, and
    // paging on with it would append rows from a list nobody asked for.
    setPager(new RequestLogPager<RequestRow>())
    void load('')
  }, [load])

  const records = useMemo(() => pager.rows.map((r) => new RequestRecord(r)), [pager])

  return {
    records,
    cursor: pager.cursor,
    error,
    loading,
    loadingMore,
    refreshing,
    updatedAt,
    live,
    refresh: () => void load(''),
    more: () => void load(pager.cursor),
  }
}
