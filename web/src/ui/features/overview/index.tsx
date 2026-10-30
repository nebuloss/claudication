import {
  api,
  type MemorySnapshot,
  type Overview as OverviewData,
} from '../../../api/client'
import { AvailabilityCard } from '../status'
import { useLoader } from '../../hooks'
import type { ReactNode } from 'react'
import {
  Banner,
  Card,
  ErrorState,
  Freshness,
  InfoTip,
  InlineLink,
  Loading,
  Stat,
  StatGrid,
  tile,
  type Tone,
} from '../../primitives'
import { compact, duration, mib } from '../../format'
import { READS } from '../../../api/live'

/**
 * The gateway's own process: its memory against its ceiling, and what that
 * memory is.
 *
 * On a small container memory is the first thing to run out, and when it does
 * the kernel kills the process with every stream open — so the figure worth
 * watching is resident memory against the limit, not the Go heap alone. The
 * bar under it says why the resident figure is what it is: the heap in use,
 * idle heap not yet returned — it goes back to the system after a burst, so a
 * high reading that falls a minute later is the trimmer working — and the
 * rest, the runtime's own stacks and tables.
 */
function ProcessCard({ memory, fill = false }: { memory: MemorySnapshot; fill?: boolean }) {
  const rss = memory.rss_bytes
  const limit = memory.limit_bytes
  const pct = limit > 0 && rss > 0 ? Math.min(100, (rss / limit) * 100) : undefined
  const tone = pct === undefined ? 'neutral' : pct >= 90 ? 'error' : pct >= 75 ? 'warn' : 'ok'
  // What resident memory is made of; "other" is what the heap does not
  // explain, never below zero when the figures were read a moment apart.
  const heap = memory.heap_in_use_bytes
  const idle = memory.heap_idle_bytes
  const other = Math.max(0, rss - heap - idle)
  const whole = Math.max(rss, heap + idle, 1)
  const parts = [
    { label: 'heap', tip: 'Heap in use', bytes: heap, fill: 'bg-primary' },
    { label: 'idle', tip: 'Idle heap, not yet returned to the system', bytes: idle, fill: 'bg-primary/40' },
    { label: 'runtime', tip: 'The runtime and its stacks', bytes: other, fill: 'bg-outline' },
  ]

  return (
    <Card title="Process" fill={fill}>
      <div className={`${tile} grid gap-5 p-4 sm:grid-cols-[auto_1fr] sm:items-center ${fill ? 'min-h-0' : ''}`}>
        <Gauge percent={pct} tone={tone}>
          <span className="text-lg leading-6 text-on-surface tabular-nums">{rss > 0 ? mib(rss) : '—'}</span>
          <span className="text-[11px] text-on-surface-variant">
            {limit > 0 ? `of ${mib(limit)}` : 'resident'}
          </span>
        </Gauge>
        <div className="flex min-w-0 flex-col gap-4">
          {rss > 0 && (
            <div>
              <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-surface-high">
                {parts.map((p) => (
                  <span key={p.label} className={p.fill} style={{ width: `${(p.bytes / whole) * 100}%` }} />
                ))}
              </div>
              <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-on-surface-variant">
                {parts.map((p) => (
                  <li key={p.label} className="flex items-center gap-1.5" title={p.tip}>
                    <span className={`size-2 rounded-full ${p.fill}`} aria-hidden />
                    {p.label} <span className="text-on-surface tabular-nums">{mib(p.bytes)}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          {/* One line of figures, each its word after it: as four labelled
              tiles at half the width their labels ran into each other. */}
          <p className="m-0 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-on-surface-variant">
            <span>
              limit{' '}
              {limit > 0 ? (
                <span className="text-on-surface tabular-nums">{Math.round(pct ?? 0)}%</span>
              ) : (
                // The advice behind an ⓘ: three lines of it under the figures
                // were what made the Overview scroll.
                <span className="inline-flex items-center gap-1 text-on-surface">
                  none
                  <InfoTip label="About the memory limit">
                    No limit known. Set <code>memory-limit</code> on a container that cannot see its own, so the
                    runtime collects before the kernel kills it.
                  </InfoTip>
                </span>
              )}
            </span>
            <span>
              target{' '}
              <span className="text-on-surface tabular-nums">
                {memory.heap_limit_bytes > 0 ? mib(memory.heap_limit_bytes) : 'none'}
              </span>
            </span>
            <span>
              <span className="text-on-surface tabular-nums">{memory.goroutines}</span> goroutines
            </span>
            <span>
              <span className="text-on-surface tabular-nums">{compact(memory.gc_cycles)}</span> collections
            </span>
          </p>
        </div>
      </div>
    </Card>
  )
}

/**
 * A ring filled to `percent`, in the meaning colour of `tone`, with a
 * reading inside it. An empty ring when there is nothing to measure against.
 */
function Gauge({ percent, tone, children }: { percent: number | undefined; tone: Tone; children: ReactNode }) {
  const r = 42
  const c = 2 * Math.PI * r
  const stroke = { ok: 'stroke-success', warn: 'stroke-warning', error: 'stroke-error', neutral: 'stroke-primary' }[tone]
  return (
    <div className="relative grid size-28 shrink-0 place-items-center justify-self-center">
      <svg viewBox="0 0 100 100" className="absolute inset-0 -rotate-90" aria-hidden>
        <circle cx="50" cy="50" r={r} fill="none" strokeWidth="9" className="stroke-surface-high" />
        {percent !== undefined && percent > 0 && (
          <circle
            cx="50"
            cy="50"
            r={r}
            fill="none"
            strokeWidth="9"
            strokeLinecap="round"
            strokeDasharray={`${(Math.max(2, percent) / 100) * c} ${c}`}
            className={stroke}
          />
        )}
      </svg>
      <span className="relative flex flex-col items-center">{children}</span>
    </div>
  )
}

/**
 * The first screen: is the gateway able to serve a request, and what has it
 * been doing.
 *
 * It used to carry the client instructions too, which meant the one job a new
 * user arrives to do was a card at the bottom of a dashboard. Those live with
 * the key a client is given, so this links there rather than repeating them.
 */
export default function Overview() {
  const { data, error, loading, reload, refreshing, updatedAt } = useLoader<OverviewData>(
    (signal) => api.gateway.overview(signal),
    { on: READS.overview },
  )

  if (loading) return <Loading />
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
    // The window's height on a wide screen, as the welcome page is (the
    // owner's ask: no scrollbar, whatever the window): the status at its own
    // height, and availability and the process sharing what is left, the
    // incidents listing as many as it holds. The shell gives it that height
    // (app.tsx); a window too short even for that scrolls inside it.
    <div className="flex flex-col gap-5 lg:min-h-0 lg:flex-1">
      {!data.ready && (
        <Banner tone="warn">
          No usable account, so the gateway cannot serve a request yet.{' '}
          <InlineLink href="/accounts">Connect one</InlineLink>.
        </Banner>
      )}
      {/* Only for a gateway nothing has ever called: a quiet day says so in
          the figures below, and is nothing to act on. */}
      {data.ready && data.served === false && (
        <Banner>
          Ready, and nothing has called it yet.{' '}
          <InlineLink href="/clients">Point a client at it</InlineLink>.
        </Banner>
      )}

      <Card
        title="Status"
        aside={
          <Freshness updatedAt={updatedAt} refreshing={refreshing} />
        }
      >
        <StatGrid>
          <Stat dense
            label="Proxy"
            value={data.ready ? 'Ready' : 'Not ready'}
            tone={data.ready ? 'ok' : 'error'}
            hint={`up ${duration(data.uptime_s)}`}
          />
          <Stat dense
            label="Accounts"
            value={data.accounts.usable}
            hint={`of ${data.accounts.total} connected`}
          />
          <Stat dense label="Clients" value={data.keys.total} hint="set up" />
          <Stat dense
            label="Requests 24h"
            value={day ? compact(day.requests) : '—'}
            hint={day && day.errors > 0 ? `${day.errors} failed` : 'no failures'}
            tone={day && day.errors > 0 ? 'error' : undefined}
          />
        </StatGrid>
        {day !== undefined && day.requests > 0 && (
          <div className="mt-3">
            <StatGrid>
            <Stat dense label="In 24h" value={compact(day.input_tokens)} hint="input tokens" />
            <Stat dense label="Out 24h" value={compact(day.output_tokens)} hint="output tokens" />
            <Stat dense
              label="Cached 24h"
              value={compact(day.cache_read_tokens + day.cache_write_tokens)}
              hint="cache tokens"
            />
            <Stat dense label="Latency" value={`${day.median_ms}ms`} hint={`p95 ${day.p95_ms}ms`} />
            </StatGrid>
          </div>
        )}
      </Card>

      {/* Whether it has been up and what the process holds, side by side on
          a wide screen so the Overview fits a laptop's window without
          scrolling; one above the other on a narrow one. The figures and the
          90 days here, the rest behind History; an incident of the last day
          is a notification too. */}
      {/* Capped as the welcome page's grid is: on a tall screen the
          incidents have their room and the rest of the window stays empty,
          rather than the process's figures floating in a tall white box. */}
      <div className="grid gap-4 lg:min-h-64 lg:max-h-[28rem] lg:flex-1 lg:grid-cols-2">
        <AvailabilityCard fill />
        {data.memory !== undefined && <ProcessCard memory={data.memory} fill />}
      </div>

      {unknown && (
        <Banner tone="warn">
          <strong>This page is not the relay.</strong> The admin UI is on its own listener
          (<code>admin-listen</code>), so the address in your browser answers 404 to{' '}
          <code>/v1/messages</code>. Set <code>public-url</code> so Setup can tell clients where
          the relay actually is.
        </Banner>
      )}

    </div>
  )
}
