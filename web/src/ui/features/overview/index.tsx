import {
  api,
  type MemorySnapshot,
  type Overview as OverviewData,
} from '../../../api/client'
import { NoticesCard } from '../notices'
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
import { READS } from '../../../api/live'

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
    <Page>
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
      {/* What needs the operator, each a link to where it is dealt with:
          the gateway switched off, an account to sign in again, a model
          failing everywhere, a client whose requests keep failing. */}
      <NoticesCard />

      <Card
        title="Status"
        aside={
          <Freshness updatedAt={updatedAt} refreshing={refreshing} />
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
