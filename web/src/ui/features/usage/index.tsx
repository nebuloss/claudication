import { useState } from 'react'
import { api, type Usage, type UsageBucket, type UsageScope } from '../../../api/client'
import { RankedBars } from '../../charts'
import { Traffic } from './traffic'
import { useLoader } from '../../hooks'
import { accountHref, keyHref, navigate } from '../../route'
import { requestsHref } from '../requests/filter'
import {
  AppLink,
  Button,
  Card,
  Empty,
  ErrorState,
  Freshness,
  Loading,
  Muted,
  Overline,
  Stat,
  StatGrid,
  WindowPicker,
} from '../../primitives'
import { compact } from '../../format'
import { providerLabel } from '../../providers'

const WINDOWS = [1, 7, 30] as const

const REPORT_REFRESH_MS = 30_000

/** How many rows a breakdown shows before it offers the rest. */
const TOP = 5


/** A breakdown row that opens the page of what it counts, when it has one. */
function linked(b: UsageBucket, href: ((id: string) => string) | undefined) {
  if (href === undefined || b.id === undefined || b.id === '') return {}
  const to = href(b.id)
  return { href: to, onOpen: () => navigate(to) }
}

const tokensOf = (b: UsageBucket) => b.input_tokens + b.output_tokens + b.cache_tokens

/**
 * One breakdown as a ranked list under a heading: the top few, and the rest
 * a click away rather than gone.
 */
function Breakdown({
  title,
  buckets,
  measure,
  href,
}: {
  title: string
  buckets: UsageBucket[]
  /** Tokens, or requests where tokens were never recorded (status codes). */
  measure: 'tokens' | 'requests'
  href?: (id: string) => string
}) {
  const [all, setAll] = useState(false)
  const value = measure === 'tokens' ? tokensOf : (b: UsageBucket) => b.requests
  // Accounts say whose they are once there is more than one provider among
  // them: two providers' accounts can share an address.
  const providers = new Set(buckets.map((b) => b.provider ?? '').filter((p) => p !== ''))
  const labelOf = (b: UsageBucket) =>
    providers.size > 1 && b.provider !== undefined && b.provider !== ''
      ? `${b.label} · ${providerLabel(b.provider)}`
      : b.label
  const sorted = [...buckets].sort((a, b) => value(b) - value(a))
  const shown = all ? sorted : sorted.slice(0, TOP)
  return (
    <div className="min-w-0">
      <div className="mb-2">
        <Overline as="h3">{title}</Overline>
      </div>
      {shown.length === 0 ? (
        <Muted>Nothing.</Muted>
      ) : (
        <RankedBars
          scale="log"
          format={compact}
          rows={shown.map((b) => ({
            label: labelOf(b),
            value: value(b),
            right:
              measure === 'tokens'
                ? `${compact(tokensOf(b))} tok · ${compact(b.requests)} req`
                : `${compact(b.requests)} req`,
            ...linked(b, href),
          }))}
        />
      )}
      {sorted.length > TOP && (
        <div className="mt-2">
          <Button variant="outlined" size="sm" onClick={() => setAll(!all)}>
            {all ? 'Show fewer' : `Show all ${sorted.length}`}
          </Button>
        </div>
      )}
    </div>
  )
}

/**
 * What one account or one key spent, on its own page.
 *
 * Sections rather than tabs: the figures, then traffic over time, then where
 * it went — each a card with a heading, read top to bottom. The chats are a
 * page of their own beside this one.
 * The window chosen at the top applies to all of them. Only the chart keeps a
 * row of choices, because which dimension to draw is the one question with
 * several answers worth switching between.
 */
export default function ScopedUsage({
  scope,
  breakdown,
}: {
  scope: UsageScope
  /** The other side: an account's page breaks down by key, a key's by account. */
  breakdown: 'account' | 'key'
}) {
  const [days, setDays] = useState<number>(7)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader<Usage>(
    (signal) => api.traffic.usage(days, scope, signal),
    {
      deps: [days, scope.account, scope.key],
      refreshMs: REPORT_REFRESH_MS,
    },
  )

  if (error !== '') {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }
  if (data !== null && !data.enabled) {
    return (
      <Card title="Usage">
        <Empty>
          Per-request history is turned off. Set <code>usage.retention-days</code> above 0 to record
          it.
        </Empty>
      </Card>
    )
  }

  const report = data?.report
  const totals = report?.totals
  const busy = report === undefined || totals === undefined || totals.requests === 0

  // The request log, narrowed to the same scope.
  const logHref = requestsHref({
    ...(scope.account ? { account: [scope.account] } : {}),
    ...(scope.key ? { key: [scope.key] } : {}),
  })

  return (
    <>
      <Card
        title="Totals"
        aside={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <WindowPicker value={days} windows={WINDOWS} onChange={setDays} />
            <Freshness
              updatedAt={updatedAt}
              refreshing={refreshing}
              live={live}
              onRefresh={() => void reload()}
            />
          </div>
        }
      >
        {loading && data === null ? (
          <Loading />
        ) : busy ? (
          <Empty>Nothing proxied in the last {days === 1 ? 'day' : `${days} days`}.</Empty>
        ) : (
          <>
            <StatGrid columns={5}>
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
                hint={`${compact(totals.cache_read_tokens)} read · ${compact(totals.cache_write_tokens)} written`}
              />
              <Stat label="Latency" value={`${totals.median_ms}ms`} hint={`p95 ${totals.p95_ms}ms`} />
            </StatGrid>
            <div className="mt-3">
              <Muted size="xs">
                Kept for {data?.retention_days ?? 0} days.{' '}
                <AppLink href={logHref}>Every request in the log</AppLink>
              </Muted>
            </div>
          </>
        )}
      </Card>

      {!busy && (
        <>
          <Card title="Over time">
            <Traffic cross={report.cross} days={days} hide={[breakdown === 'key' ? 'account' : 'key']} />
          </Card>

          <Card title="Where it went">
            <div className="grid gap-6 lg:grid-cols-3">
              <Breakdown title="Models" buckets={report.by_model} measure="tokens" />
              {breakdown === 'key' ? (
                <Breakdown title="API keys" buckets={report.by_key} measure="tokens" href={keyHref} />
              ) : (
                <Breakdown
                  title="Accounts"
                  buckets={report.by_account}
                  measure="tokens"
                  href={accountHref}
                />
              )}
              <Breakdown title="Status codes" buckets={report.by_status} measure="requests" />
            </div>
          </Card>
        </>
      )}
    </>
  )
}
