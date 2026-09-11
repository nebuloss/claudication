import { useState } from 'react'
import { api, type Usage, type UsageBucket, type Overview as OverviewData } from '../../api/client'
import {
  ColumnChart,
  CompositionBar,
  Legend,
  MAGNITUDE,
  SERIES_COLOURS,
  Solid,
  Stack,
  type Series,
} from '../charts'
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
 * Requests, as two states of one quantity.
 *
 * Both are primary, and the failed one is `shaded()` — a darker, striped tone
 * of the same colour rather than a hue of its own. Hue in this card means
 * identity, and the mix below spends the whole categorical palette on exactly
 * that; a red for a *state* would take one of those away and put the same
 * colour in two legends meaning two different things. See charts/paint.tsx.
 */
const REQUESTS: Series<UsageBucket>[] = [
  {
    key: 'ok',
    label: 'Succeeded',
    paint: MAGNITUDE,
    value: (b) => b.requests - b.errors,
  },
  {
    key: 'failed',
    label: 'Failed',
    paint: MAGNITUDE.shaded(),
    value: (b) => b.errors,
  },
]

/** Tokens, which are three genuine kinds and so three hues. */
const TOKENS: Series<UsageBucket>[] = [
  { key: 'in', label: 'Input', paint: new Solid(SERIES_COLOURS[0]), value: (b) => b.input_tokens },
  {
    key: 'out',
    label: 'Output',
    paint: new Solid(SERIES_COLOURS[2]),
    value: (b) => b.output_tokens,
  },
  {
    key: 'cache',
    label: 'Cache',
    paint: new Solid(SERIES_COLOURS[3]),
    value: (b) => b.cache_tokens,
  },
]

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
  const [hovered, setHovered] = useState<number | null>(null)

  const byDay: UsageBucket[] = usage?.report?.by_day ?? []
  const byModel: UsageBucket[] = usage?.report?.by_model ?? []
  const stack = new Stack(byDay, metric === 'requests' ? REQUESTS : TOKENS)
  const shown = hovered !== null ? byDay[hovered] : undefined

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
      ) : stack.sum === 0 ? (
        <p className="m-0 text-sm text-on-surface-variant">Nothing in the last {days} days.</p>
      ) : (
        <>
          {/* The readout sits above the plot rather than floating over it: a
              tooltip near the right-hand edge either clips or covers the
              columns it is describing, and this has a fixed place to be. */}
          <div className="mb-2 flex flex-wrap items-baseline gap-x-4 gap-y-1">
            <span className="text-2xl font-medium tabular-nums text-on-surface">
              {compact(hovered !== null ? stack.total(hovered) : stack.sum)}
            </span>
            <span className="text-xs text-on-surface-variant">
              {metric} {shown === undefined ? `over ${stack.length} days` : `on ${shown.label}`}
            </span>
            <span className="ml-auto">
              <Legend
                stack={stack}
                format={compact}
                values={shown === undefined ? undefined : (s) => s.value(shown)}
              />
            </span>
          </div>

          <ColumnChart
            stack={stack}
            label={(b) => b.label}
            format={compact}
            hovered={hovered}
            onHover={setHovered}
            describe={(b) =>
              `${b.label}: ${b.requests} requests, ${b.errors} failed, ` +
              `${compact(b.input_tokens + b.output_tokens + b.cache_tokens)} tokens`
            }
          />

          {byModel.length > 0 && (
            <div className="mt-6 border-t border-outline-variant pt-4">
              <p className="mt-0 mb-3 text-sm text-on-surface-variant">
                Tokens by model over the same {days} days. A subscription is spent in tokens rather
                than requests, so this is the half that explains a week.
              </p>
              <CompositionBar
                rows={byModel.map((b) => ({
                  label: b.label === '' ? 'unknown' : b.label,
                  value: b.input_tokens + b.output_tokens + b.cache_tokens,
                }))}
                format={compact}
              />
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
