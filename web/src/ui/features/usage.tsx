import { useState } from 'react'
import { api, type Usage } from '../../api/client'
import { RankedBars } from '../charts'
import ChatsPanel from './chats'
import { Traffic } from './traffic'
import { useHashPanel, useLoader } from '../hooks'
import {
  ErrorState,
  Card,
  CardTitle,
  Empty,
  Spinner,
  Segmented,
  Stat,
  SubNav,
  TextButton,
  compact,
} from '../primitives'

const WINDOWS = [1, 7, 30] as const

// The hash values, and the order the sub-nav lists them. These are a URL
// surface now — /usage#chats is a link someone can send — so renaming one
// breaks a bookmark, the same way renaming a tab id would.
const VIEWS = ['chats', 'day', 'model', 'account', 'key'] as const

type View = (typeof VIEWS)[number]

const VIEW_LABELS: Record<View, string> = {
  chats: 'Chats',
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
  // In the hash, so /usage#chats opens straight onto that panel.
  const [chosen, setChosen] = useHashPanel<View>(VIEWS, 'chats')
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
  const views: View[] = []
  if (report !== undefined) {
    // Offered whenever anything was proxied, rather than gated on a count this
    // report does not carry: the chat list fetches its own data and says "no
    // conversations" for itself, which is a real answer worth being able to
    // reach.
    if (report.totals.requests > 0) views.push('chats')
    if (report.by_day.length > 0) views.push('day')
    if (report.by_model.length > 0) views.push('model')
    if (report.by_account.length > 0) views.push('account')
    if (report.by_key.length > 0) views.push('key')
  }
  // Narrowing the window can take the chosen tab away with it; fall back
  // rather than render a panel that is no longer on the bar.
  const view = views.includes(chosen) ? chosen : (views[0] ?? 'chats')

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
        />

        <div className="mt-4">
          {/* Its own fetch, so opening Usage does not pay for a rollup most
              visits never look at. */}
          {view === 'chats' && <ChatsPanel days={days} onExpired={onExpired} />}

          {view === 'day' && report !== undefined && (
            <Traffic cross={report.cross} days={days} />
          )}

          {view === 'model' && report !== undefined && (
            <RankedBars
              scale="log"
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
                scale="log"
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
              scale="log"
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

