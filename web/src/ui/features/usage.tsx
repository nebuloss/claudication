import { useCallback, useEffect, useState } from 'react'
import { ApiError, api, messageOf, type RequestRow, type Usage } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Bar,
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
  TonalButton,
  TextButton,
  Verbatim,
  ago,
  compact,
  copyText,
} from '../primitives'

const WINDOWS = [1, 7, 30] as const

type View = 'requests' | 'day' | 'model' | 'account' | 'key'

const VIEW_LABELS: Record<View, string> = {
  requests: 'Requests',
  day: 'Day',
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
            <Breakdown
              identity={false}
              rows={report.by_day.map((b) => ({
                label: b.label,
                value: b.requests,
                right: `${compact(b.requests)} req`,
              }))}
            />
          )}

          {view === 'model' && report !== undefined && (
            <Breakdown
              rows={report.by_model.map((b) => ({
                label: b.label,
                value: b.input_tokens + b.output_tokens + b.cache_tokens,
                right: `${compact(b.input_tokens + b.output_tokens + b.cache_tokens)} tok`,
              }))}
            />
          )}

          {view === 'account' && report !== undefined && (
            <>
              <Breakdown
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
            <Breakdown
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

/**
 * Colour follows the entity, not its position in the list.
 *
 * The lists arrive sorted by traffic, so indexing by row would repaint every
 * bar the moment the ranking changed — the same model would be blue this hour
 * and orange the next. Sorting the labels gives each name a slot that only
 * moves when the set of names does.
 */
function slotFor(labels: string[]): Map<string, number> {
  const stable = [...new Set(labels)].sort()
  return new Map(stable.map((l, i) => [l, i]))
}

function Breakdown({
  rows,
  identity = true,
}: {
  rows: { label: string; value: number; right: string }[]
  /** False where the bar means magnitude over time rather than which thing. */
  identity?: boolean
}) {
  const max = rows.reduce((m, r) => Math.max(m, r.value), 0)
  const slots = identity ? slotFor(rows.map((r) => r.label)) : null
  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((r) => (
        <Bar
          key={r.label}
          label={r.label}
          value={r.value}
          max={max}
          right={r.right}
          series={slots?.get(r.label)}
        />
      ))}
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

  const data = rows

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
        <Table cap head={['When', 'Model', 'Key', 'Status', 'Tokens', 'Took', '']}>
          {data.map((r, i) => (
            <tr key={`${r.at}-${i}`} className="border-b border-outline-variant last:border-0">
              <td className="px-2 py-2 whitespace-nowrap text-on-surface-variant">{ago(r.at)}</td>
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
        <KeyValue
          items={[
            ['When', <span title={row.at}>{ago(row.at)}</span>],
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
            ['Took', `${row.duration_ms}ms`],
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

/** The row as something worth pasting somewhere else. */
function asText(r: RequestRow): string {
  return [
    `when:      ${r.at}`,
    `status:    ${r.status === 0 ? 'failed (no response)' : r.status}`,
    `model:     ${dash(r.model)}`,
    `key:       ${dash(r.key_name)}`,
    `account:   ${dash(r.account_email)}`,
    `path:      ${r.path}`,
    `streaming: ${r.streaming ? 'yes' : 'no'}`,
    `tokens:    ${r.input_tokens} in / ${r.output_tokens} out / ${r.cache_tokens} cache`,
    `took:      ${r.duration_ms}ms`,
    '',
    r.error ?? '',
  ].join('\n')
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
