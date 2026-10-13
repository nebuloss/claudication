import { useState } from 'react'
import { api, type Usage, type UsageBucket, type UsageScope } from '../../api/client'
import { RankedBars } from '../charts'
import ChatsPanel from './chats'
import { Traffic } from './traffic'
import { useLoader } from '../hooks'
import { navigate } from '../route'
import { requestsHref } from './requests'
import {
  AppLink,
  ErrorState,
  Card,
  CardTitle,
  Empty,
  Freshness,
  Spinner,
  Segmented,
  Stat,
  compact,
} from '../primitives'

const WINDOWS = [1, 7, 30] as const

const REPORT_REFRESH_MS = 30_000

/** How many rows a breakdown shows; the rest are one line saying so. */
const TOP = 5

/** Where an account's or a key's own page lives. */
export const accountHref = (id: string) => `/accounts/${encodeURIComponent(id)}`
export const keyHref = (id: string) => `/keys/${encodeURIComponent(id)}`

/** A breakdown row that opens the page of what it counts, when it has one. */
function linked(b: UsageBucket, href: ((id: string) => string) | undefined) {
  if (href === undefined || b.id === undefined || b.id === '') return {}
  const to = href(b.id)
  return { href: to, onOpen: () => navigate(to) }
}

const tokensOf = (b: UsageBucket) => b.input_tokens + b.output_tokens + b.cache_tokens

/** One breakdown as a short ranked list, under a heading. */
function Breakdown({
  title,
  buckets,
  href,
}: {
  title: string
  buckets: UsageBucket[]
  href?: (id: string) => string
}) {
  const shown = [...buckets].sort((a, b) => tokensOf(b) - tokensOf(a)).slice(0, TOP)
  return (
    <div className="min-w-0">
      <h3 className="mt-0 mb-2 text-xs font-medium tracking-wide text-on-surface-variant uppercase">
        {title}
      </h3>
      {shown.length === 0 ? (
        <p className="m-0 text-sm text-on-surface-variant">Nothing.</p>
      ) : (
        <RankedBars
          scale="log"
          format={compact}
          rows={shown.map((b) => ({
            label: b.label,
            value: tokensOf(b),
            right: `${compact(tokensOf(b))} tok`,
            ...linked(b, href),
          }))}
        />
      )}
      {buckets.length > TOP && (
        <p className="mt-2 mb-0 text-xs text-on-surface-variant">and {buckets.length - TOP} more</p>
      )}
    </div>
  )
}

/**
 * What one account or one key spent, on its own page.
 *
 * One screen, no tabs. The figures, a chart over time, the two breakdowns
 * that mean something here side by side — models, and the other side of the
 * account/key pair — and the chats below in a card of their own. It used to
 * be a row of sub-tabs, one of which held a chart with a second row of tabs
 * and two toggles: three levels of choosing before a number.
 */
export default function ScopedUsage({
  scope,
  breakdown,
  onExpired,
  title = 'Usage',
}: {
  scope: UsageScope
  /** The other side: an account's page breaks down by key, a key's by account. */
  breakdown: 'account' | 'key'
  onExpired: () => void
  title?: string
}) {
  const [days, setDays] = useState<number>(7)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader<Usage>(
    () => api.usage(days, scope),
    onExpired,
    [days, scope.account, scope.key],
    REPORT_REFRESH_MS,
  )

  if (error !== '') {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }
  if (data !== null && !data.enabled) {
    return (
      <Card>
        <CardTitle>{title}</CardTitle>
        <Empty>
          Per-request history is turned off. Set <code>usage.retention-days</code> above 0 to record
          it.
        </Empty>
      </Card>
    )
  }

  const report = data?.report
  const totals = report?.totals

  // The request log, narrowed to the same scope.
  const logHref = requestsHref({
    ...(scope.account ? { account: [scope.account] } : {}),
    ...(scope.key ? { key: [scope.key] } : {}),
  })

  return (
    <>
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
              <Freshness
                updatedAt={updatedAt}
                refreshing={refreshing}
                live={live}
                onRefresh={() => void reload()}
              />
            </div>
          }
        >
          {title}
        </CardTitle>

        {loading && data === null ? (
          <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : report === undefined || totals === undefined || totals.requests === 0 ? (
          <Empty>Nothing proxied in the last {days === 1 ? 'day' : `${days} days`}.</Empty>
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
                label="Cache"
                value={compact(totals.cache_read_tokens + totals.cache_write_tokens)}
                hint={`${compact(totals.cache_read_tokens)} read`}
              />
            </div>

            {days > 1 && (
              <div className="mt-5">
                <Traffic cross={report.cross} days={days} plain />
              </div>
            )}

            <div className="mt-5 grid gap-6 sm:grid-cols-2">
              <Breakdown title="By model" buckets={report.by_model} />
              {breakdown === 'key' ? (
                <Breakdown title="By key" buckets={report.by_key} href={keyHref} />
              ) : (
                <Breakdown title="By account" buckets={report.by_account} href={accountHref} />
              )}
            </div>

            <p className="mt-5 mb-0 text-xs text-on-surface-variant">
              Latency {totals.median_ms}ms median, {totals.p95_ms}ms p95. Kept for{' '}
              {data?.retention_days ?? 0} days.{' '}
              <AppLink href={logHref}>Every request in the log</AppLink>
            </p>
          </>
        )}
      </Card>

      <Card>
        <CardTitle>Chats</CardTitle>
        <ChatsPanel days={days} scope={scope} onExpired={onExpired} />
      </Card>
    </>
  )
}
