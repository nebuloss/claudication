import { useState } from 'react'
import { api, type Usage, type UsageBucket, type Overview as OverviewData } from '../../api/client'
import { ModelMix, TrafficChart } from './charts'
import { useLoader } from '../hooks'
import {
  Banner,
  ErrorState,
  Card,
  CardTitle,
  Segmented,
  Spinner,
  Stat,
  TextButton,
  compact,
  duration,
} from '../primitives'


/** How many days of history the charts can show. */
const WINDOWS = [7, 14, 30] as const

/**
 * What the gateway has been doing, over the chosen window.
 *
 * Two pictures rather than one, because they answer different questions and
 * the second is the one a subscription is actually spent on. The columns say
 * whether traffic is steady, growing or stopped, and whether any of it is
 * failing. The mix underneath says which models consumed the tokens, which is
 * what explains a week that ran out early.
 *
 * Both come from the same request. Loading them separately from the status
 * above means changing the window never re-reads the status, and a usage table
 * that is switched off or empty cannot take the screen down with it.
 */
function Traffic({
  usage,
  days,
  onDays,
}: {
  usage: Usage | null
  days: number
  onDays: (d: number) => void
}) {
  const [metric, setMetric] = useState<'requests' | 'tokens'>('requests')
  const byDay: UsageBucket[] = usage?.report?.by_day ?? []
  const byModel: UsageBucket[] = usage?.report?.by_model ?? []
  const requests = byDay.reduce((n, b) => n + b.requests, 0)

  return (
    <Card>
      <CardTitle
        aside={
          <div className="flex flex-wrap items-center gap-2">
            <Segmented
              label="Metric"
              value={metric}
              onChange={(v) => setMetric(v)}
              options={[
                { id: 'requests' as const, label: 'Requests', content: 'Requests' },
                { id: 'tokens' as const, label: 'Tokens', content: 'Tokens' },
              ]}
            />
            <Segmented
              label="History window"
              value={String(days)}
              onChange={(v) => onDays(Number(v))}
              options={WINDOWS.map((d) => ({
                id: String(d),
                label: `${d} days`,
                content: `${d}d`,
              }))}
            />
          </div>
        }
      >
        Traffic
      </CardTitle>

      {usage === null ? (
        <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      ) : usage.enabled === false ? (
        <p className="m-0 text-sm text-on-surface-variant">
          Per-request history is off — <code>usage.retention-days</code> is 0, so the gateway
          records nothing to chart.
        </p>
      ) : requests === 0 ? (
        <p className="m-0 text-sm text-on-surface-variant">Nothing in the last {days} days.</p>
      ) : (
        <>
          <TrafficChart buckets={byDay} metric={metric} />
          {byModel.length > 0 && (
            <div className="mt-6 border-t border-outline-variant pt-4">
              <p className="mt-0 mb-3 text-sm text-on-surface-variant">
                Tokens by model over the same {days} days. A subscription is spent in tokens rather
                than requests, so this is the half that explains a week.
              </p>
              <ModelMix buckets={byModel} />
            </div>
          )}
        </>
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
 * Setup now, with the per-client configuration that was missing from them.
 */
export default function Overview({
  onExpired,
  onGoTo,
}: {
  onExpired: () => void
  onGoTo: (tab: 'accounts' | 'keys' | 'setup') => void
}) {
  const { data, error, loading, reload } = useLoader<OverviewData>(() => api.overview(), onExpired)
  // The traffic series, on its own loader so changing the window does not
  // re-fetch the status above it, and so a usage table that is off or empty
  // cannot take the whole screen down with it.
  const [days, setDays] = useState(14)
  const { data: usage } = useLoader<Usage>(() => api.usage(days), undefined, [days])

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
          Ready, and nothing has called it yet.{' '}
          <button
            type="button"
            className="underline underline-offset-2"
            onClick={() => onGoTo('setup')}
          >
            Point a client at it
          </button>
          .
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
        <CardTitle aside={<TextButton onClick={() => void reload()}>Refresh</TextButton>}>
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

      {unknown && (
        <Banner tone="warn">
          <strong>This page is not the relay.</strong> The admin UI is on its own listener
          (<code>admin-listen</code>), so the address in your browser answers 404 to{' '}
          <code>/v1/messages</code>. Set <code>public-url</code> so Setup can tell clients where
          the relay actually is.
        </Banner>
      )}

      <Traffic usage={usage} days={days} onDays={setDays} />
    </div>
  )
}
