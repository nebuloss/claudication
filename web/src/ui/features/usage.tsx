import { useState } from 'react'
import { api, type RequestRow, type Usage } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Bar,
  Banner,
  ErrorState,
  Card,
  CardTitle,
  Chip,
  Empty,
  Spinner,
  Segmented,
  Stat,
  Table,
  TextButton,
  Verbatim,
  ago,
  compact,
} from '../primitives'

const WINDOWS = [1, 7, 30] as const

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

      {report !== undefined && report.by_day.length > 0 && (
        <Card>
          <CardTitle>By day</CardTitle>
          <Breakdown
            identity={false}
            rows={report.by_day.map((b) => ({
              label: b.label,
              value: b.requests,
              right: `${compact(b.requests)} req`,
            }))}
          />
        </Card>
      )}

      {report !== undefined && report.by_model.length > 0 && (
        <Card>
          <CardTitle>By model</CardTitle>
          <Breakdown
            rows={report.by_model.map((b) => ({
              label: b.label,
              value: b.input_tokens + b.output_tokens + b.cache_tokens,
              right: `${compact(b.input_tokens + b.output_tokens + b.cache_tokens)} tok`,
            }))}
          />
        </Card>
      )}

      {report !== undefined && report.by_account.length > 0 && (
        <Card>
          <CardTitle>By Claude account</CardTitle>
          <Breakdown
            rows={report.by_account.map((b) => ({
              label: b.label,
              value: b.input_tokens + b.output_tokens + b.cache_tokens,
              right: `${compact(b.requests)} req`,
            }))}
          />
          <p className="mt-3 mb-0 text-xs text-on-surface-variant">
            What this gateway sent to each account. The subscription's own 5-hour and 7-day
            utilisation is on the Claude accounts tab — it counts every client, not only this one.
          </p>
        </Card>
      )}

      {report !== undefined && report.by_key.length > 0 && (
        <Card>
          <CardTitle>By key</CardTitle>
          <Breakdown
            rows={report.by_key.map((b) => ({
              label: b.label,
              value: b.requests,
              right: `${compact(b.requests)} req`,
            }))}
          />
        </Card>
      )}

      <RecentRequests onExpired={onExpired} />
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

function RecentRequests({ onExpired }: { onExpired: () => void }) {
  const { data, error, loading, reload } = useLoader<RequestRow[]>(
    () => api.recentRequests(50),
    onExpired,
  )
  const [shown, setShown] = useState<RequestRow | null>(null)

  return (
    <Card>
      <CardTitle aside={<TextButton onClick={() => void reload()}>Refresh</TextButton>}>
        Recent requests
      </CardTitle>

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
        <Table head={['When', 'Model', 'Key', 'Status', 'Tokens', 'Took', '']}>
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
                  <TextButton onClick={() => setShown(shown === r ? null : r)}>Why</TextButton>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}

      {shown !== null && shown.error !== undefined && shown.error !== '' && (
        <div className="mt-4">
          <p className="mt-0 mb-2 text-xs text-on-surface-variant">
            The upstream's own words, unmodified — it is the only thing that separates an expired
            token from a plan restriction.
          </p>
          <Verbatim>{shown.error}</Verbatim>
        </div>
      )}
    </Card>
  )
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
