import {
  api,
  type MemorySnapshot,
  type Overview as OverviewData,
} from '../../../api/client'
import { AvailabilityCard } from '../status'
import { useLoader } from '../../hooks'
import {
  Banner,
  Card,
  ErrorState,
  Freshness,
  InlineLink,
  Loading,
  MeterRow,
  Page,
  Stat,
  StatGrid,
  UsageBar,
} from '../../primitives'
import { compact, duration, mib } from '../../format'

/**
 * How often the status figures re-read themselves.
 *
 * This is the screen left open on a second monitor, so it is the one that has
 * to be current: "is the proxy ready" is worth nothing if the answer on screen
 * is from whenever the tab was opened. Ten seconds is under the interval at
 * which the request counters visibly move on a working gateway.
 */
const REFRESH_MS = 10_000

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
  const tone = tight ? 'error' : pct !== undefined && pct >= 75 ? 'warn' : 'neutral'

  return (
    <Card title="Memory">
      {memory.rss_bytes > 0 && (
        <div className="mb-3">
          <MeterRow
            title="Process (resident)"
            figure={`${mib(memory.rss_bytes)}${limit > 0 ? ` of ${mib(limit)}` : ''}`}
            hint={pct !== undefined ? `${pct}%` : undefined}
            alert={tight}
          >
            <UsageBar percent={pct ?? 0} tone={tone} />
            {limit === 0 && (
              <p className="mt-1 text-[11px] text-on-surface-variant">
                No limit known. Set <code>memory-limit</code> on a container that cannot see its
                own, so the runtime collects before the kernel kills it.
              </p>
            )}
          </MeterRow>
        </div>
      )}
      <StatGrid>
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
      </StatGrid>
    </Card>
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
export default function Overview({
  serving = true,
}: {
  /** The gateway switch: off, no client is served, and the overview says so first. */
  serving?: boolean
}) {
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader<OverviewData>(
    (signal) => api.gateway.overview(signal),
    { refreshMs: REFRESH_MS },
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
    <Page>
      {!serving && (
        <Banner tone="error">
          The gateway is switched off: every client is refused until it is switched back on.{' '}
          <InlineLink href="/settings">Settings</InlineLink>.
        </Banner>
      )}
      {!data.ready && (
        <Banner tone="warn">
          No usable Claude account, so the proxy cannot serve a request yet.{' '}
          <InlineLink href="/accounts">Connect one</InlineLink>.
        </Banner>
      )}
      {data.ready && (day?.requests ?? 0) === 0 && (
        <Banner>
          Ready, and nothing has called it yet.{' '}
          <InlineLink href="/keys">Point a client at it</InlineLink>.
        </Banner>
      )}
      {(data.accounts.needs_reauth ?? 0) > 0 && (
        <Banner tone="error">
          {data.accounts.needs_reauth === 1
            ? 'An account has lost its login. It serves until its current token expires,'
            : `${data.accounts.needs_reauth} accounts have lost their login. They serve until their current tokens expire,`}{' '}
          and nothing the gateway does brings a refused login back.{' '}
          <InlineLink href="/accounts">Reconnect</InlineLink>.
        </Banner>
      )}
      {data.accounts.needs_reauth_soon > 0 && (
        <Banner tone="warn">
          {data.accounts.needs_reauth_soon === 1
            ? 'An account needs re-authorising soon.'
            : `${data.accounts.needs_reauth_soon} accounts need re-authorising soon.`}{' '}
          Refreshing cannot push that date back.{' '}
          <InlineLink href="/accounts">Reconnect</InlineLink>.
        </Banner>
      )}

      <Card
        title="Status"
        aside={
          <Freshness
            updatedAt={updatedAt}
            refreshing={refreshing}
            live={live}
            onRefresh={() => void reload()}
          />
        }
      >
        <StatGrid>
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
          <Stat label="Clients" value={data.keys.total} hint="set up" />
          <Stat
            label="Requests 24h"
            value={day ? compact(day.requests) : '—'}
            hint={day && day.errors > 0 ? `${day.errors} failed` : 'no failures'}
            tone={day && day.errors > 0 ? 'error' : undefined}
          />
        </StatGrid>
        {day !== undefined && day.requests > 0 && (
          <div className="mt-3">
            <StatGrid>
            <Stat label="In 24h" value={compact(day.input_tokens)} hint="input tokens" />
            <Stat label="Out 24h" value={compact(day.output_tokens)} hint="output tokens" />
            <Stat
              label="Cached 24h"
              value={compact(day.cache_read_tokens + day.cache_write_tokens)}
              hint="cache tokens"
            />
            <Stat label="Latency" value={`${day.median_ms}ms`} hint={`p95 ${day.p95_ms}ms`} />
            </StatGrid>
          </div>
        )}
      </Card>

      <AvailabilityCard />

      {data.memory !== undefined && <MemoryCard memory={data.memory} />}

      {unknown && (
        <Banner tone="warn">
          <strong>This page is not the relay.</strong> The admin UI is on its own listener
          (<code>admin-listen</code>), so the address in your browser answers 404 to{' '}
          <code>/v1/messages</code>. Set <code>public-url</code> so Setup can tell clients where
          the relay actually is.
        </Banner>
      )}

    </Page>
  )
}
