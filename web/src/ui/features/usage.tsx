import { useCallback, useEffect, useMemo, useState } from 'react'
import { ApiError, api, messageOf, type RequestRow, type Usage } from '../../api/client'
import { RankedBars } from '../charts'
import { Traffic } from './traffic'
import { useLoader } from '../hooks'
import {
  Banner,
  ErrorModal,
  ErrorState,
  Card,
  CardTitle,
  Chip,
  Empty,
  KeyValue,
  Spinner,
  Segmented,
  Stat,
  SubNav,
  Table,
  type Column,
  TonalButton,
  TextButton,
  Verbatim,
  compact,
  copyText,
} from '../primitives'

const WINDOWS = [1, 7, 30] as const

type View = 'requests' | 'day' | 'model' | 'account' | 'key'

const VIEW_LABELS: Record<View, string> = {
  requests: 'Requests',
  day: 'Over time',
  model: 'Model',
  account: 'Account',
  key: 'Key',
}

/**
 * What the proxy has actually been doing: totals, a breakdown, and the last
 * few requests as they happened.
 *
 * The gateway sits between a client and a metered upstream, so "what did that
 * cost and which key spent it" is the question it is uniquely placed to
 * answer. Before this it only ever reached the log.
 */
export default function UsagePanel({ onExpired }: { onExpired: () => void }) {
  const [days, setDays] = useState<number>(7)
  const [chosen, setChosen] = useState<View>('requests')
  // Bumped to remount the activity list, which is what refreshing it means.
  const [reloads, setReloads] = useState(0)
  const { data, error, loading, reload } = useLoader<Usage>(() => api.usage(days), onExpired, [days])

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '') {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }
  if (data !== null && !data.enabled) {
    return (
      <Card>
        <CardTitle>Usage</CardTitle>
        <Empty>
          Per-request history is turned off. Set <code>usage.retention-days</code> above 0 to record
          it.
        </Empty>
      </Card>
    )
  }

  const report = data?.report
  const totals = report?.totals

  // Only the views with something in them, so a quiet window does not offer
  // four empty tabs. Requests is always there — "nothing yet" is an answer.
  const views: View[] = ['requests']
  if (report !== undefined) {
    if (report.by_day.length > 0) views.push('day')
    if (report.by_model.length > 0) views.push('model')
    if (report.by_account.length > 0) views.push('account')
    if (report.by_key.length > 0) views.push('key')
  }
  // Narrowing the window can take the chosen tab away with it; fall back
  // rather than render a panel that is no longer on the bar.
  const view = views.includes(chosen) ? chosen : 'requests'

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardTitle
          aside={
            <div className="flex items-center gap-2">
              <Segmented
                label="Reporting window"
                value={String(days)}
                onChange={(v) => setDays(Number(v))}
                options={WINDOWS.map((d) => ({
                  id: String(d),
                  label: `${d} days`,
                  content: `${d}d`,
                }))}
              />
              <TextButton onClick={() => void reload()}>Refresh</TextButton>
            </div>
          }
        >
          Totals
        </CardTitle>

        {totals === undefined || totals.requests === 0 ? (
          <Empty>Nothing proxied in this window yet.</Empty>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat
                label="Requests"
                value={compact(totals.requests)}
                hint={`${totals.errors} failed`}
                tone={totals.errors > 0 ? 'error' : undefined}
              />
              <Stat label="Input" value={compact(totals.input_tokens)} hint="tokens" />
              <Stat label="Output" value={compact(totals.output_tokens)} hint="tokens" />
              <Stat
                label="Latency"
                value={`${totals.median_ms}ms`}
                hint={`p95 ${totals.p95_ms}ms`}
              />
            </div>
            <p className="mt-3 mb-0 text-xs text-on-surface-variant">
              Cache: {compact(totals.cache_read_tokens)} read,{' '}
              {compact(totals.cache_write_tokens)} written. Kept for{' '}
              {data?.retention_days ?? 0} days.
            </p>
          </>
        )}
      </Card>

      {/* One panel at a time. Stacked, the breakdowns and the activity list
          were four screens of scrolling to compare two numbers that were never
          both in view anyway. */}
      <Card>
        <SubNav
          label="Usage view"
          value={view}
          onChange={setChosen}
          options={views.map((id) => ({ id, label: VIEW_LABELS[id] }))}
          aside={
            view === 'requests' ? (
              <TextButton onClick={() => setReloads((n) => n + 1)}>Refresh</TextButton>
            ) : undefined
          }
        />

        <div className="mt-4">
          {view === 'requests' && (
            // Remounting is the refresh: the list starts from the first page
            // and drops the cursor, which is exactly what reloading it means.
            <RecentRequests key={reloads} onExpired={onExpired} />
          )}

          {view === 'day' && report !== undefined && (
            <Traffic cross={report.cross} days={days} />
          )}

          {view === 'model' && report !== undefined && (
            <RankedBars
              format={compact}
              rows={report.by_model.map((b) => ({
                label: b.label,
                value: b.input_tokens + b.output_tokens + b.cache_tokens,
                right: `${compact(b.input_tokens + b.output_tokens + b.cache_tokens)} tok`,
              }))}
            />
          )}

          {view === 'account' && report !== undefined && (
            <>
              <RankedBars
                format={compact}
                rows={report.by_account.map((b) => ({
                  label: b.label,
                  value: b.input_tokens + b.output_tokens + b.cache_tokens,
                  right: `${compact(b.requests)} req`,
                }))}
              />
              <p className="mt-3 mb-0 text-xs text-on-surface-variant">
                What this gateway sent to each account. The subscription's own 5-hour and 7-day
                utilisation is on the Claude accounts tab — it counts every client, not only this
                one.
              </p>
            </>
          )}

          {view === 'key' && report !== undefined && (
            <RankedBars
              format={compact}
              rows={report.by_key.map((b) => ({
                label: b.label,
                value: b.requests,
                right: `${compact(b.requests)} req`,
              }))}
            />
          )}
        </div>
      </Card>
    </div>
  )
}

const PAGE = 50

/**
 * The activity list, paged.
 *
 * Not useLoader: this is the one panel that accumulates rather than replaces,
 * so it holds its own rows and the cursor for the next page. Fifty rows is
 * about seven minutes of history on a busy gateway, which made "recent" the
 * only thing this could ever answer.
 */
function RecentRequests({ onExpired }: { onExpired: () => void }) {
  const [rows, setRows] = useState<RequestRow[]>([])
  const [cursor, setCursor] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [shown, setShown] = useState<RequestRow | null>(null)
  const [sort, setSort] = useState<Sort>({ key: 'at', dir: 'desc' })

  // Clicking the column already in effect turns it around; clicking a new one
  // starts at the end people mean first — newest, biggest, worst — except for
  // the two text columns, where A-Z is what "sorted" means.
  const sortBy = (key: SortKey) =>
    setSort((prev) =>
      prev.key === key
        ? { key, dir: prev.dir === 'asc' ? 'desc' : 'asc' }
        : { key, dir: key === 'model' || key === 'key_name' ? 'asc' : 'desc' },
    )

  const column = (label: string, key: SortKey): Column => ({
    label,
    onSort: () => sortBy(key),
    sorted: sort.key === key ? sort.dir : undefined,
  })

  const load = useCallback(
    async (after: string) => {
      const first = after === ''
      first ? setLoading(true) : setLoadingMore(true)
      setError('')
      try {
        const page = await api.recentRequests(PAGE, after)
        // Append when paging, replace when refreshing, so Refresh cannot leave
        // a stale tail below freshly loaded rows.
        setRows((prev) => (first ? page.rows : [...prev, ...page.rows]))
        setCursor(page.nextCursor)
      } catch (err) {
        if (err instanceof ApiError && err.isUnauthenticated) {
          onExpired()
          return
        }
        setError(messageOf(err))
      } finally {
        first ? setLoading(false) : setLoadingMore(false)
      }
    },
    [onExpired],
  )

  useEffect(() => {
    void load('')
  }, [load])

  // Sorted here rather than by the server: the cursor is a keyset on (at, id),
  // so a different order would need a different cursor. This orders the rows
  // that have been loaded — press Load more to sort over more of them.
  const data = useMemo(() => sortRows(rows, sort), [rows, sort])

  // No card and no title of its own: it is the body of a tab now, and the tab
  // is already called Requests.
  return (
    <>
      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      {loading ? (
        <p className="flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      ) : data === null || data.length === 0 ? (
        <Empty>No requests recorded yet.</Empty>
      ) : (
        <Table
          cap
          head={[
            // Not Date and not Time: the cell is a time on today's rows, a
            // date and a time on older ones, and an epoch once copied.
            column('Timestamp', 'at'),
            column('Model', 'model'),
            column('Key', 'key_name'),
            column('Status', 'status'),
            column('Tokens', 'tokens'),
            column('Duration', 'duration'),
            '',
          ]}
        >
          {data.map((r, i) => (
            <tr key={`${r.at}-${i}`} className="border-b border-outline-variant last:border-0">
              <td
                title={r.at}
                className="px-2 py-2 tabular-nums whitespace-nowrap text-on-surface-variant"
              >
                {stamp(r.at)}
              </td>
              <td className="px-2 py-2 whitespace-nowrap">{dash(r.model)}</td>
              <td className="px-2 py-2 whitespace-nowrap text-on-surface-variant">
                {dash(r.key_name)}
              </td>
              <td className="px-2 py-2">
                <Chip tone={statusTone(r)}>
                  {r.status === 0 ? 'failed' : r.status}
                  {r.streaming && r.status === 200 ? ' ·' : ''}
                </Chip>
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">
                {r.input_tokens + r.output_tokens > 0
                  ? `${compact(r.input_tokens)} / ${compact(r.output_tokens)}`
                  : '—'}
              </td>
              <td className="px-2 py-2 tabular-nums whitespace-nowrap">{r.duration_ms}ms</td>
              <td className="px-2 py-2">
                {r.error !== undefined && r.error !== '' && (
                  <TextButton onClick={() => setShown(r)}>Log</TextButton>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}

      {data.length > 0 && (
        <div className="mt-4 flex items-center gap-3 border-t border-outline-variant pt-4">
          {cursor !== '' ? (
            <TonalButton onClick={() => void load(cursor)} disabled={loadingMore}>
              {loadingMore && <Spinner />}
              {loadingMore ? 'Loading…' : 'Load more'}
            </TonalButton>
          ) : (
            <span className="text-xs text-on-surface-variant">
              That is everything kept — history goes back {' '}
              <code>usage.retention-days</code>.
            </span>
          )}
          <span className="text-xs text-on-surface-variant" aria-live="polite">
            {data.length} shown
          </span>
        </div>
      )}

      {shown !== null && shown.error !== undefined && shown.error !== '' && (
        <RequestLog row={shown} onClose={() => setShown(null)} />
      )}
    </>
  )
}

/**
 * One failed request, in full.
 *
 * A dialog rather than a panel under the table: the row it belongs to is
 * usually scrolled well out of view by the time the text appears below fifty
 * others, and reading an upstream error means reading all of it — which is the
 * one thing a strip at the bottom of a long list makes hard.
 *
 * The identifying columns come along because the operator opened this from a
 * row they can no longer see, and "which request was that" is the first thing
 * they lose.
 */
function RequestLog({ row, onClose }: { row: RequestRow; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)

  const tokens =
    row.input_tokens + row.output_tokens > 0
      ? `${compact(row.input_tokens)} in / ${compact(row.output_tokens)} out`
      : '—'

  const copy = async () => {
    setCopyFailed(false)
    if (await copyText(asText(row))) {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
      return
    }
    // Not a modal on top of this one: the text is already on screen and
    // selectable, so saying which keys to press is the whole remedy.
    setCopyFailed(true)
  }

  return (
    <ErrorModal
      title="Request log"
      size="lg"
      onClose={onClose}
      message={
        <>
          The upstream's own words, unmodified — it is the only thing that separates an expired
          token from a plan restriction.
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {row.error_kind === 'content_check' && (
          <Banner tone="warn">
            <strong>This is a content check, not a quota limit.</strong> The message below talks
            about billing, but adding usage credit will not fix it — the upstream classified this
            request's <em>content</em> as coming from a third-party app rather than from Claude
            Code. The same key usually succeeds on the next request with slightly different
            content, which is what makes it look like a flaky quota problem.
            <br />
            <br />
            Two triggers are known and already rewritten on the way out: a tool named{' '}
            <code>mcp_x</code> instead of <code>mcp__x</code>, and Claude Code's own{' '}
            <code>Is directory a git repo:</code> line inside a foreign system prompt. Seeing this
            anyway means a third trigger — bisect the request body to find it.
          </Banner>
        )}

        <KeyValue
          items={[
            // The exact instant, not "3m ago": this is the value that gets
            // matched against an upstream request_id or somebody else's log.
            ['Timestamp', <span className="tabular-nums">{row.at}</span>],
            [
              'Status',
              <Chip tone={statusTone(row)}>{row.status === 0 ? 'failed' : row.status}</Chip>,
            ],
            ['Model', dash(row.model)],
            ['Key', dash(row.key_name)],
            ['Account', dash(row.account_email)],
            ['Path', row.path],
            ['Streaming', row.streaming ? 'yes' : 'no'],
            ['Tokens', tokens],
            ['Duration', `${row.duration_ms}ms`],
          ]}
        />

        <div>
          <div className="mb-2 flex items-center justify-between gap-4">
            <span className="text-xs font-medium tracking-wide text-on-surface-variant uppercase">
              Error
            </span>
            {/* The whole log, not just the error text: what gets pasted into an
                issue is useless without the model and the status beside it. */}
            <TonalButton onClick={() => void copy()}>
              {copied ? 'Copied' : 'Copy log'}
            </TonalButton>
          </div>
          <Verbatim>{row.error ?? ''}</Verbatim>
          {copyFailed && (
            <p className="mt-2 mb-0 text-xs text-error">
              The browser only gives a page the clipboard over HTTPS, and this gateway is being
              served over plain HTTP. Select the text above and press{' '}
              <span className="font-mono">⌘C</span> or <span className="font-mono">Ctrl-C</span>.
            </p>
          )}
        </div>
      </div>
    </ErrorModal>
  )
}

/**
 * An absolute local timestamp, seconds kept.
 *
 * "3m ago" reads well until the moment it matters, which is lining a request
 * up against something outside this page — an upstream request_id, a service
 * log, whoever reported the error. Then it is the one thing that cannot be
 * matched. Seconds stay because a retry storm puts a dozen rows in one minute.
 *
 * The date is dropped for today's rows, which is nearly all of them at a
 * fortnight's retention, and the full instant is on the row's title either way.
 */
function stamp(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return iso
  const time = at.toLocaleTimeString(undefined, { hour12: false })
  const now = new Date()
  const today =
    at.getFullYear() === now.getFullYear() &&
    at.getMonth() === now.getMonth() &&
    at.getDate() === now.getDate()
  if (today) return time
  return `${at.toLocaleDateString(undefined, { day: '2-digit', month: 'short' })} ${time}`
}

/** The row as something worth pasting somewhere else. */
function asText(r: RequestRow): string {
  // Both spellings of the instant: the ISO one to read, the epoch to grep a
  // log with.
  const epoch = Math.round(new Date(r.at).getTime() / 1000)
  return [
    `timestamp: ${r.at}`,
    `epoch:     ${Number.isNaN(epoch) ? '—' : epoch}`,
    `status:    ${r.status === 0 ? 'failed (no response)' : r.status}`,
    `model:     ${dash(r.model)}`,
    `key:       ${dash(r.key_name)}`,
    `account:   ${dash(r.account_email)}`,
    `path:      ${r.path}`,
    `streaming: ${r.streaming ? 'yes' : 'no'}`,
    `tokens:    ${r.input_tokens} in / ${r.output_tokens} out / ${r.cache_tokens} cache`,
    `duration:  ${r.duration_ms}ms`,
    '',
    r.error ?? '',
  ].join('\n')
}

type SortKey = 'at' | 'model' | 'key_name' | 'status' | 'tokens' | 'duration'
type Sort = { key: SortKey; dir: 'asc' | 'desc' }

/**
 * What a row is worth for a given column.
 *
 * Status is the one that is not simply its own value. A request that never got
 * an answer is recorded as 0, which sorts below 200 and would put the worst
 * failures at the far end from the 4xx and 5xx ones — so sorting by status,
 * the thing you do to find what went wrong, would scatter the failures to both
 * ends of the table. Ranked above every real status instead, so one click puts
 * every failure together at the top.
 */
function sortValue(r: RequestRow, key: SortKey): string | number {
  switch (key) {
    case 'at':
      return new Date(r.at).getTime()
    case 'model':
      return r.model ?? ''
    case 'key_name':
      return r.key_name ?? ''
    case 'status':
      return r.status === 0 ? 1000 : r.status
    case 'tokens':
      return r.input_tokens + r.output_tokens
    case 'duration':
      return r.duration_ms
  }
}

function sortRows(rows: RequestRow[], sort: Sort): RequestRow[] {
  return [...rows].sort((a, b) => {
    const av = sortValue(a, sort.key)
    const bv = sortValue(b, sort.key)
    const cmp =
      typeof av === 'number' && typeof bv === 'number'
        ? av - bv
        : String(av).localeCompare(String(bv))
    if (cmp !== 0) return sort.dir === 'asc' ? cmp : -cmp
    // Ties always fall back to newest first, whichever way the column runs.
    // Without it, rows sharing a status would shuffle on every re-render.
    return new Date(b.at).getTime() - new Date(a.at).getTime()
  })
}

function dash(v: string | undefined): string {
  return v === undefined || v === '' ? '—' : v
}

function statusTone(r: RequestRow): 'ok' | 'warn' | 'error' {
  if (r.error !== undefined && r.error !== '') return 'error'
  if (r.status === 0 || r.status >= 500) return 'error'
  if (r.status >= 400) return 'warn'
  return 'ok'
}
