import { api, type Overview as OverviewData } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  ErrorState,
  Card,
  CardTitle,
  CopyField,
  Spinner,
  Stat,
  TextButton,
  compact,
  duration,
} from '../primitives'

/**
 * The first screen: is the gateway able to serve a request, and what does a
 * client have to be told to use it.
 *
 * That second half is the reason this tab exists. Everything else in the UI
 * administers the gateway; this is the only part that helps you actually point
 * something at it.
 */
export default function Overview({
  onExpired,
  onGoTo,
}: {
  onExpired: () => void
  onGoTo: (tab: 'accounts' | 'keys') => void
}) {
  const { data, error, loading, reload } = useLoader<OverviewData>(() => api.overview(), onExpired)

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

  // What to tell a client to point at.
  //
  // The origin this browser used is the right answer while one listener serves
  // everything: it is reachable by definition, and better than the bind
  // address, which is often 0.0.0.0 and resolves for nobody.
  //
  // It is the wrong answer once admin-listen splits them. Then this page is on
  // the admin address and the relay is somewhere else — usually a different
  // hostname on a different proxy — and nothing the gateway can see tells it
  // that name. So it is configured, and until it is, this says so rather than
  // handing out a URL that answers 404 to /v1/messages.
  const baseURL = data.public_url ?? ''
  const guessed = window.location.origin
  const unknown = baseURL === '' && data.admin_split === true
  const shown = baseURL !== '' ? baseURL : guessed
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

      <Card>
        <CardTitle>Point a client at it</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface">
          Claude Code talks to the gateway exactly as it talks to Anthropic, and Codex talks to it
          in OpenAI&rsquo;s Responses shape. Either way it needs the base URL and one of your{' '}
          <button
            type="button"
            className="text-primary underline underline-offset-2"
            onClick={() => onGoTo('keys')}
          >
            API keys
          </button>
          .
        </p>
        {unknown && (
          <Banner tone="warn" className="mb-4">
            <strong>This page is not the relay.</strong> The admin UI is on its own listener
            (<code>admin-listen</code>), so the address in your browser serves the UI and answers
            404 to <code>/v1/messages</code>. The gateway cannot see the hostname clients reach the
            relay on — set <code>public-url</code> in the config and it will be shown here instead
            of the guess below.
          </Banner>
        )}
        <div className="flex flex-col gap-4">
          <CopyField label="Base URL" value={shown} />
          <CopyField
            label="Claude Code"
            value={`export ANTHROPIC_BASE_URL=${shown}\nexport ANTHROPIC_AUTH_TOKEN=clc_…\nclaude`}
          />
          <CopyField
            label="Codex CLI (~/.codex/config.toml)"
            value={`model_provider = "claudication"
model = "claude-sonnet-5"

[model_providers.claudication]
name = "claudication"
base_url = "${shown}/v1"
env_key = "CLAUDICATION_API_KEY"
wire_api = "responses"`}
          />
          <CopyField
            label="curl"
            value={`curl ${shown}/v1/messages \\
  -H "x-api-key: clc_…" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "content-type: application/json" \\
  -d '{"model":"claude-haiku-4-5","max_tokens":64,
       "messages":[{"role":"user","content":"hello"}]}'`}
          />
        </div>
        <p className="mt-4 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          Codex reads the key from the environment variable <code>env_key</code> names, so export{' '}
          <code>CLAUDICATION_API_KEY</code> before running it — and <code>wire_api</code> must be{' '}
          <code>responses</code>, which is the only shape Codex speaks.
        </p>
        <p className="mt-2 mb-0 text-xs text-on-surface-variant">
          Served over plain HTTP unless something in front terminates TLS, so the key travels in the
          clear on this network. A tunnel or a reverse proxy is the fix.
        </p>
      </Card>

      <Card>
        <CardTitle>Build</CardTitle>
        {/* Which binary is running, and for how long. Listen and History used
            to sit here too; both are configuration, and Settings now reports
            every setting with where its value came from — which this card
            could not say, so it was the worse of the two places to read it. */}
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <Stat
            label="Version"
            value={<span className="font-mono text-base">{data.version}</span>}
          />
          <Stat label="Commit" value={<span className="font-mono text-base">{data.commit}</span>} />
          <Stat label="Started" value={duration(data.uptime_s)} hint="ago" />
        </div>
      </Card>
    </div>
  )
}
