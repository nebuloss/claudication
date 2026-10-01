import { useState } from 'react'
import {
  api,
  type MemorySnapshot,
  type Usage,
  type Overview as OverviewData,
} from '../../api/client'
import { Traffic } from './traffic'
import { useLoader } from '../hooks'
import {
  Banner,
  ErrorState,
  Card,
  CardTitle,
  Freshness,
  Spinner,
  Stat,
  compact,
  duration,
} from '../primitives'

/**
 * How often the status figures re-read themselves.
 *
 * This is the screen left open on a second monitor, so it is the one that has
 * to be current: "is the proxy ready" is worth nothing if the answer on screen
 * is from whenever the tab was opened. Ten seconds is under the interval at
 * which the request counters visibly move on a working gateway.
 */
const REFRESH_MS = 10_000

/** The traffic chart aggregates whole days, so it barely moves minute to minute. */
const TRAFFIC_REFRESH_MS = 60_000


/**
 * The gateway's own memory, against its ceiling.
 *
 * On a small container memory is the first thing to run out, and when it does
 * the kernel kills the process with every stream open — so the figure worth
 * watching is resident memory against the limit, not the Go heap alone. The
 * heap and the idle memory not yet returned are there to say why the
 * resident figure is what it is: idle memory goes back to the system after a
 * burst, so a high reading that falls a minute later is the trimmer working.
 */
function MemoryCard({ memory }: { memory: MemorySnapshot }) {
  const limit = memory.limit_bytes
  const pct = limit > 0 ? Math.min(100, Math.round((memory.rss_bytes / limit) * 100)) : undefined
  const tight = pct !== undefined && pct >= 90
  const colour = tight ? 'bg-error' : pct !== undefined && pct >= 75 ? 'bg-warning' : 'bg-primary'

  return (
    <Card>
      <CardTitle>Memory</CardTitle>
      {memory.rss_bytes > 0 && (
        <div className="mb-3">
          <div className="mb-1 flex items-baseline justify-between gap-3 text-xs">
            <span className="text-on-surface-variant">Process (resident)</span>
            <span className={`tabular-nums ${tight ? 'font-medium text-error' : 'text-on-surface'}`}>
              {mib(memory.rss_bytes)}
              {limit > 0 ? ` of ${mib(limit)}` : ''}
              {pct !== undefined && (
                <span className="ml-2 font-normal text-on-surface-variant">{pct}%</span>
              )}
            </span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-surface-high">
            <div
              className={`h-full rounded-full ${colour}`}
              style={{ width: `${Math.max(pct ?? 0, 1)}%` }}
            />
          </div>
          {limit === 0 && (
            <p className="mt-1 mb-0 text-[11px] text-on-surface-variant">
              No limit known. Set <code>memory-limit</code> on a container that cannot see its
              own, so the runtime collects before the kernel kills it.
            </p>
          )}
        </div>
      )}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat
          label="Heap in use"
          value={mib(memory.heap_in_use_bytes)}
          hint={
            memory.heap_limit_bytes > 0 ? `target ${mib(memory.heap_limit_bytes)}` : 'no target'
          }
        />
        <Stat label="Idle" value={mib(memory.heap_idle_bytes)} hint="not yet returned" />
        <Stat label="Goroutines" value={memory.goroutines} hint="streams and workers" />
        <Stat label="Collections" value={compact(memory.gc_cycles)} hint="since start" />
      </div>
    </Card>
  )
}

/** Bytes as MiB, the unit memory limits are set in. */
function mib(bytes: number): string {
  const v = bytes / (1 << 20)
  return v >= 100 ? `${Math.round(v)} MiB` : `${v.toFixed(1)} MiB`
}

/** How many days of history the chart can show. */
const WINDOWS = [7, 14, 30] as const

/**
 * What the gateway has been doing, over the chosen window.
 *
 * The chart itself is features/traffic.tsx, shared with the Usage tab — the
 * two screens asked the same question in two shapes before, which meant two
 * places deciding what a colour meant.
 */
function TrafficCard({
  usage,
  days,
  onDays,
}: {
  usage: Usage | null
  days: number
  onDays: (d: number) => void
}) {
  return (
    <Card>
      <CardTitle>Traffic</CardTitle>
      {usage === null ? (
        <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      ) : usage.enabled === false ? (
        <p className="m-0 text-sm text-on-surface-variant">
          Per-request history is off — <code>usage.retention-days</code> is 0, so the gateway
          records nothing to chart.
        </p>
      ) : (
        <Traffic
          cross={usage.report?.cross}
          days={days}
          onDays={onDays}
          windows={WINDOWS}
        />
      )}
    </Card>
  )
}

/**
 * The first screen: is the gateway able to serve a request, and what has it
 * been doing.
 *
 * It used to carry the client instructions too, which meant the one job a new
 * user arrives to do was a card at the bottom of a dashboard. Those live on
 * the public docs page now, with the per-client configuration that was
 * missing from them.
 */
export default function Overview({
  onExpired,
  onGoTo,
  docsURL,
}: {
  onExpired: () => void
  onGoTo: (tab: 'accounts' | 'keys') => void
  /**
   * The public docs page, or empty when there is none. Setup used to be a tab
   * and this used to be a link to it; now it is a page of its own, on its own
   * listener, and an operator with no docs listener configured has nowhere for
   * this to point.
   */
  docsURL: string
}) {
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader<OverviewData>(
    () => api.overview(),
    onExpired,
    [],
    REFRESH_MS,
  )
  // The traffic series, on its own loader so changing the window does not
  // re-fetch the status above it, and so a usage table that is off or empty
  // cannot take the whole screen down with it.
  const [days, setDays] = useState(14)
  const { data: usage } = useLoader<Usage>(
    () => api.usage(days),
    undefined,
    [days],
    TRAFFIC_REFRESH_MS,
  )

  if (loading) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '' || data === null) {
    return (
      <ErrorState
        message={error || 'Could not load the overview.'}
        onRetry={() => void reload()}
        busy={loading}
      />
    )
  }

  // Whether the gateway can say where its relay is.
  //
  // While one listener serves everything, the origin this browser used is
  // reachable by definition and Setup can hand it out. Once admin-listen
  // splits them this page is on the admin address, the relay is somewhere else
  // — usually a different hostname on a different proxy — and nothing the
  // gateway can see tells it that name. Then public-url has to say, and until
  // it does, every client instruction on Setup is a guess.
  const unknown = (data.public_url ?? '') === '' && data.admin_split === true
  const day = data.last_24h

  return (
    <div className="flex flex-col gap-5">
      {!data.ready && (
        <Banner tone="warn">
          No usable Claude account, so the proxy cannot serve a request yet.{' '}
          <button
            type="button"
            className="underline underline-offset-2"
            onClick={() => onGoTo('accounts')}
          >
            Connect one
          </button>
          .
        </Banner>
      )}
      {data.ready && (day?.requests ?? 0) === 0 && (
        <Banner>
          Ready, and nothing has called it yet.
          {docsURL !== '' && (
            <>
              {' '}
              <a
                href={docsURL}
                target="_blank"
                rel="noreferrer"
                className="underline underline-offset-2"
              >
                Point a client at it
              </a>
              .
            </>
          )}
        </Banner>
      )}
      {data.accounts.needs_reauth_soon > 0 && (
        <Banner tone="warn">
          {data.accounts.needs_reauth_soon === 1
            ? 'An account needs re-authorising soon.'
            : `${data.accounts.needs_reauth_soon} accounts need re-authorising soon.`}{' '}
          Refreshing cannot push that date back.
        </Banner>
      )}

      <Card>
        <CardTitle
          aside={
            <Freshness
              updatedAt={updatedAt}
              refreshing={refreshing}
              live={live}
              onRefresh={() => void reload()}
            />
          }
        >
          Status
        </CardTitle>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Stat
            label="Proxy"
            value={data.ready ? 'Ready' : 'Not ready'}
            tone={data.ready ? 'ok' : 'error'}
            hint={`up ${duration(data.uptime_s)}`}
          />
          <Stat
            label="Accounts"
            value={data.accounts.usable}
            hint={`of ${data.accounts.total} connected`}
          />
          <Stat label="API keys" value={data.keys.total} hint="in use" />
          <Stat
            label="Requests 24h"
            value={day ? compact(day.requests) : '—'}
            hint={day && day.errors > 0 ? `${day.errors} failed` : 'no failures'}
            tone={day && day.errors > 0 ? 'error' : undefined}
          />
        </div>
        {day !== undefined && day.requests > 0 && (
          <div className="mt-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Stat label="In 24h" value={compact(day.input_tokens)} hint="input tokens" />
            <Stat label="Out 24h" value={compact(day.output_tokens)} hint="output tokens" />
            <Stat
              label="Cached 24h"
              value={compact(day.cache_read_tokens + day.cache_write_tokens)}
              hint="cache tokens"
            />
            <Stat label="Latency" value={`${day.median_ms}ms`} hint={`p95 ${day.p95_ms}ms`} />
          </div>
        )}
      </Card>

      {data.memory !== undefined && <MemoryCard memory={data.memory} />}

      {unknown && (
        <Banner tone="warn">
          <strong>This page is not the relay.</strong> The admin UI is on its own listener
          (<code>admin-listen</code>), so the address in your browser answers 404 to{' '}
          <code>/v1/messages</code>. Set <code>public-url</code> so Setup can tell clients where
          the relay actually is.
        </Banner>
      )}

      <TrafficCard usage={usage} days={days} onDays={setDays} />
    </div>
  )
}
