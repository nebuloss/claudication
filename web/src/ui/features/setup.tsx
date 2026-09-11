import { useState } from 'react'
import {
  api,
  type GatewayConfig,
  type Model,
  type Overview as OverviewData,
} from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Card, CardTitle, CopyField, Spinner, SubNav } from '../primitives'

/** The clients this gateway has been set up against, in order of likelihood. */
const CLIENTS = ['claude-code', 'codex', 'opencode', 'crush', 'curl'] as const
type Client = (typeof CLIENTS)[number]

const CLIENT_LABELS: Record<Client, string> = {
  'claude-code': 'Claude Code',
  codex: 'Codex CLI',
  opencode: 'opencode',
  crush: 'crush',
  curl: 'curl',
}

/** Which API surface each client talks to, so a switched-off one can warn. */
const CLIENT_SURFACE: Record<Client, 'anthropic' | 'openai' | 'both'> = {
  'claude-code': 'anthropic',
  codex: 'openai',
  opencode: 'anthropic',
  crush: 'anthropic',
  curl: 'both',
}

/**
 * Per-model figures the upstream model list does not carry.
 *
 * Only crush needs them — it never calls /v1/models, so every model it can
 * select has to be described in its config file. Getting one wrong changes
 * what crush displays and when it truncates; it changes nothing about what the
 * gateway will serve, which is why a plain fallback is safe for a model that
 * appears after this was written.
 */
const LARGE_CONTEXT = 1_000_000
const SMALL_CONTEXT = 200_000

function modelFacts(id: string): { context: number; maxTokens: number; reasons: boolean } {
  // The 4.5-era models are the 200k ones; everything newer is a million.
  const small = id.startsWith('claude-opus-4-5') || id.startsWith('claude-haiku-4-5')
  const reasons = !id.startsWith('claude-haiku') && !id.startsWith('claude-sonnet-4-5')
  return {
    context: small ? SMALL_CONTEXT : LARGE_CONTEXT,
    maxTokens: id.startsWith('claude-opus-5') || id.startsWith('claude-sonnet-5') ? 128_000 : 64_000,
    reasons,
  }
}

/** The model to put in an example when nothing better is known. */
function preferred(models: Model[]): string {
  for (const want of ['claude-opus-5', 'claude-sonnet-5', 'claude-fable-5-1']) {
    if (models.some((m) => m.id === want)) return want
  }
  return models[0]?.id ?? 'claude-opus-5'
}

function smallest(models: Model[]): string {
  const haiku = models.find((m) => m.id.startsWith('claude-haiku'))
  return haiku?.id ?? preferred(models)
}

/**
 * Setup: everything about pointing something at this gateway, in one place.
 *
 * It used to be two halves of two other screens — a card at the bottom of
 * Overview and another at the bottom of Settings — which meant the one job a
 * new user actually arrives to do was split across two tabs, under the ones
 * that administer a gateway they have not connected to yet.
 *
 * The per-client configuration is the part that was missing, and it is built
 * from this gateway's own address and its own model list rather than written
 * out here. Every client disagrees about whether the base URL carries `/v1`
 * and none of them says so when you get it wrong, and a hardcoded model list
 * would start lying the day an account gains a model.
 */
export default function Setup({
  onExpired,
  onGoTo,
}: {
  onExpired: () => void
  onGoTo: (tab: 'accounts' | 'keys') => void
}) {
  const [client, setClient] = useState<Client>('claude-code')
  const { data, error, loading } = useLoader<OverviewData>(() => api.overview(), onExpired)
  // Both loaded separately and both allowed to fail: they are context for the
  // instructions, not the instructions themselves, and neither should be able
  // to blank the screen.
  const { data: config } = useLoader<GatewayConfig>(() => api.config())
  const { data: modelList, error: modelError } = useLoader<{ data: Model[] | null }>(() =>
    api.models(),
  )

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '' || data === null) {
    return <Banner tone="error">Could not read the gateway&rsquo;s address.</Banner>
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
  const configured = data.public_url ?? ''
  const unknown = configured === '' && data.admin_split === true
  const base = configured !== '' ? configured : window.location.origin
  const insecure = base.startsWith('http://')

  const models = modelList?.data ?? []
  const surfaces = config?.surfaces ?? []
  const needed = CLIENT_SURFACE[client]
  const off = surfaces.filter((s) => !s.enabled && (needed === 'both' || s.id === needed))

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardTitle>What a client needs</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface">
          Two things, the same for every client: the address below, and one of your{' '}
          <button
            type="button"
            className="text-primary underline underline-offset-2"
            onClick={() => onGoTo('keys')}
          >
            API keys
          </button>
          . One key works on both APIs — nothing needs to look like an OpenAI key.
        </p>

        {!data.ready && (
          <Banner tone="warn" className="mb-4">
            No account is connected yet, so the gateway cannot answer a request however a client is
            configured.{' '}
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

        {unknown && (
          <Banner tone="warn" className="mb-4">
            <strong>This page is not the relay.</strong> The admin UI is on its own listener
            (<code>admin-listen</code>), so the address in your browser serves this page and answers
            404 to <code>/v1/messages</code>. The gateway cannot see the hostname clients reach the
            relay on — set <code>public-url</code> and it will be shown here instead of the guess
            below.
          </Banner>
        )}

        <CopyField label="Base URL" value={base} />

        {insecure && (
          <p className="mt-4 mb-0 text-xs text-on-surface-variant">
            Plain HTTP unless something in front terminates TLS, so the key travels in the clear on
            this network. A tunnel or a reverse proxy is the fix.
          </p>
        )}
      </Card>

      <Card>
        <CardTitle>Models</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          What your connected accounts can serve, read from the upstream just now. The gateway has
          no allowlist of its own — it relays whatever model a client asks for — so anything here
          works on both APIs, and anything absent is a subscription question rather than a gateway
          one.
        </p>
        {modelError !== '' ? (
          <Banner tone="warn">
            Could not reach the upstream model list. The configurations below still work; only this
            list is missing.
          </Banner>
        ) : models.length === 0 ? (
          <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {models.map((m) => (
              <span
                key={m.id}
                className="rounded-[var(--radius-md3-s)] border border-outline bg-surface-high px-2.5 py-1.5"
                title={m.display_name ?? m.id}
              >
                <code className="font-mono text-xs text-on-surface">{m.id}</code>
              </span>
            ))}
          </div>
        )}
      </Card>

      <Card>
        <CardTitle>Configure a client</CardTitle>
        <SubNav
          label="Client"
          value={client}
          onChange={setClient}
          options={CLIENTS.map((id) => ({ id, label: CLIENT_LABELS[id] }))}
        />

        {off.length > 0 && (
          <Banner tone="warn" className="mt-4">
            {CLIENT_LABELS[client]} needs the {off.map((s) => s.title).join(' and ')}, which is
            switched off. Turn it back on under Settings, or this configuration will answer 404.
          </Banner>
        )}

        <div className="mt-4">
          <ClientGuide client={client} base={base} models={models} />
        </div>
      </Card>

      <Card>
        <CardTitle>From the shell</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          On the machine running the gateway. Shell access to the state directory is already the
          higher privilege, so none of these asks for the admin password.
        </p>
        <dl className="m-0 flex flex-col gap-3">
          {SHELL.map(([cmd, what]) => (
            <div
              key={cmd}
              className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3"
            >
              <dt className="font-mono text-xs text-on-surface">{cmd}</dt>
              <dd className="m-0 mt-1 text-sm text-on-surface-variant">{what}</dd>
            </div>
          ))}
        </dl>
      </Card>

      <Card>
        <CardTitle>When it does not work</CardTitle>
        <dl className="m-0 flex flex-col gap-3">
          {TROUBLE.map(([symptom, cause]) => (
            <div
              key={symptom}
              className="border-b border-outline-variant pb-3 last:border-0 last:pb-0"
            >
              <dt className="text-sm font-medium text-on-surface">{symptom}</dt>
              <dd className="m-0 mt-1 text-sm text-on-surface-variant">{cause}</dd>
            </div>
          ))}
        </dl>
      </Card>
    </div>
  )
}

const SHELL: [string, string][] = [
  [
    'claudication keys add -name NAME',
    'Mint an API key. The same thing the API keys tab does, for a provisioning script.',
  ],
  [
    'claudication passwd',
    'Reset the admin password. The recovery path when it is lost — no old password needed.',
  ],
  [
    'claudication login-url',
    'A single-use link that signs a browser in without typing the password. Spent the first time it is used.',
  ],
  [
    'claudication backup FILE',
    'A consistent snapshot without stopping the service. Holds the sealing key and every stored token, so it is exactly as sensitive as the state directory.',
  ],
]

const TROUBLE: [string, string][] = [
  [
    '404 on every request',
    'Almost always the /v1 question. Claude Code and crush want the base URL without it; opencode and Codex want it with. Neither says so when it is wrong.',
  ],
  [
    '404 saying the API is turned off',
    'That surface is switched off under Settings → API surfaces. The message names which one.',
  ],
  [
    '401',
    'The key is wrong, revoked, or in a header this client does not send. Either x-api-key or a bearer token works, on either API.',
  ],
  [
    '429 whose message is the single word "Error"',
    'Not a rate limit. The subscription backend refuses opus and sonnet to anything that is not Claude Code, and says so misleadingly. Leave passthrough.claude-code-attribution on and it is handled for you.',
  ],
  [
    '"Third-party apps now draw from your extra usage"',
    'Also not a billing message. A content check refused the request. The Usage tab labels these; scripts/bisect-refusal.py finds the trigger.',
  ],
  [
    'A model works in one client and not another',
    'The gateway does not filter models, so this is the client: crush needs every model listed in its own config, and Codex needs one in its catalog. The others discover them.',
  ],
  [
    'Codex: "stream closed before response.completed"',
    'The gateway sends a terminal event on every path, including failures — so this points at something between it and Codex, usually a proxy buffering the stream.',
  ],
]

/** The per-client stanza, with this gateway's address and models already in it. */
function ClientGuide({
  client,
  base,
  models,
}: {
  client: Client
  base: string
  models: Model[]
}) {
  const big = preferred(models)
  const small = smallest(models)

  switch (client) {
    case 'claude-code':
      return (
        <div className="flex flex-col gap-4">
          <Note>
            No <code>/v1</code> — Claude Code appends the path itself. Use{' '}
            <code>ANTHROPIC_AUTH_TOKEN</code> rather than <code>ANTHROPIC_API_KEY</code>: it is the
            variable the client documents for a custom base URL.
          </Note>
          <CopyField
            label="Environment"
            value={`export ANTHROPIC_BASE_URL=${base}
export ANTHROPIC_AUTH_TOKEN=clc_…
export ANTHROPIC_MODEL=${big}
export ANTHROPIC_SMALL_FAST_MODEL=${small}
claude`}
          />
          <Note>
            The two model variables are optional — without them Claude Code picks its own defaults
            from <code>/v1/models</code>, which the gateway proxies. Set them to pin a model, and
            keep the small one small: it is used for titles and summaries many times a session.
          </Note>
        </div>
      )

    case 'codex':
      return (
        <div className="flex flex-col gap-4">
          <Note>
            <strong>
              With <code>/v1</code>
            </strong>
            , and this is the one client that goes through the OpenAI Responses API rather than the
            Anthropic one. <code>wire_api = &quot;responses&quot;</code> is not optional — Responses
            is the only wire format Codex has.
          </Note>
          <CopyField
            label="~/.codex/config.toml"
            value={`model_provider = "claudication"
model = "${big}"
model_catalog_json = "~/.codex/claude-models.json"

[model_providers.claudication]
name = "claudication"
base_url = "${base}/v1"
env_key = "CLAUDICATION_API_KEY"
wire_api = "responses"`}
          />
          <CopyField label="Then" value={`export CLAUDICATION_API_KEY=clc_…\ncodex`} />
          <Note>
            <code>env_key</code> names an environment variable, not a key — putting{' '}
            <code>clc_…</code> there directly does not work. Set <code>model</code> to a Claude
            model too: Codex asks for <code>gpt-5-codex</code> by default, which means nothing
            upstream, and the gateway then substitutes whatever <code>openai.model</code> says.
          </Note>
          <Note>
            Codex looks its model up in a catalog compiled into its own binary, so a Claude name
            falls back to conservative limits and long sessions start shedding context early.
            Generate the catalog <code>model_catalog_json</code> points at with{' '}
            <code>scripts/codex-model-catalog.py</code> from the repository. Codex also needs{' '}
            <code>bubblewrap</code> installed before it can run any shell command — without it the
            first tool call panics and the model explains, convincingly, that nothing works.
          </Note>
        </div>
      )

    case 'opencode':
      return (
        <div className="flex flex-col gap-4">
          <Note>
            <strong>
              With <code>/v1</code>
            </strong>
            . It overrides the built-in Anthropic provider rather than declaring a new one, so
            every model above is available without listing any of them.
          </Note>
          <CopyField
            label="~/.config/opencode/opencode.jsonc"
            value={`{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "anthropic": {
      "options": {
        "baseURL": "${base}/v1",
        "apiKey": "clc_…"
      }
    }
  },
  "model": "anthropic/${big}",
  "small_model": "anthropic/${small}"
}`}
          />
          <Note>
            If opencode announces a model from some other vendor at startup, it is not using this
            provider at all and a stale key elsewhere is winning. Read what it prints before
            believing a test that passed.
          </Note>
        </div>
      )

    case 'crush':
      return (
        <div className="flex flex-col gap-4">
          <Note>
            <strong>
              Without <code>/v1</code>
            </strong>
            , and every model has to be spelled out: crush never calls <code>/v1/models</code>, so
            one missing from this file cannot be chosen however well the gateway serves it. This is
            generated from the list above, so it already has all of them.
          </Note>
          <CopyField label="~/.config/crush/crush.json" value={crushConfig(base, models, big, small)} />
          <Note>
            The zero costs are deliberate. A subscription is not billed per token, and leaving real
            prices in makes crush display a running total that is fiction. The context windows are
            a best guess per model — they affect what crush displays and when it truncates, never
            what the gateway will serve.
          </Note>
        </div>
      )

    case 'curl':
      return (
        <div className="flex flex-col gap-4">
          <Note>
            For checking the plumbing without a client in the way. Both APIs accept either
            credential header.
          </Note>
          <CopyField
            label="Anthropic Messages"
            value={`curl ${base}/v1/messages \\
  -H "x-api-key: clc_…" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "content-type: application/json" \\
  -d '{"model":"${small}","max_tokens":64,
       "messages":[{"role":"user","content":"hello"}]}'`}
          />
          <CopyField
            label="OpenAI Responses"
            value={`curl -N ${base}/v1/responses \\
  -H "authorization: Bearer clc_…" \\
  -H "content-type: application/json" \\
  -d '{"model":"${big}","stream":true,
       "input":[{"type":"message","role":"user",
                 "content":[{"type":"input_text","text":"say hi"}]}]}'`}
          />
          <CopyField label="Every model this gateway can serve" value={`curl ${base}/v1/models \\
  -H "x-api-key: clc_…"`} />
        </div>
      )
  }
}

/** crush's whole provider stanza, every model included. */
function crushConfig(base: string, models: Model[], big: string, small: string): string {
  const entries = models.map((m) => {
    const f = modelFacts(m.id)
    const lines = [
      `        {`,
      `          "id": "${m.id}",`,
      `          "name": "${m.display_name ?? m.id}",`,
      `          "context_window": ${f.context},`,
      `          "default_max_tokens": ${f.maxTokens},`,
      `          "can_reason": ${f.reasons},`,
      `          "supports_attachments": true,`,
      `          "cost_per_1m_in": 0,`,
      `          "cost_per_1m_out": 0,`,
      `          "cost_per_1m_in_cached": 0,`,
      `          "cost_per_1m_out_cached": 0`,
    ]
    if (f.reasons) {
      lines[lines.length - 1] += ','
      lines.push(`          "reasoning_levels": ["low", "medium", "high", "xhigh", "max"],`)
      lines.push(`          "default_reasoning_effort": "medium"`)
    }
    lines.push(`        }`)
    return lines.join('\n')
  })

  return `{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "claudication": {
      "name": "Claudication Gateway",
      "base_url": "${base}",
      "type": "anthropic",
      "api_key": "clc_…",
      "models": [
${entries.join(',\n')}
      ]
    }
  },
  "models": {
    "large": { "provider": "claudication", "model": "${big}", "think": true },
    "small": { "provider": "claudication", "model": "${small}" }
  }
}`
}

/** A line of prose between snippets, quieter than the snippet itself. */
function Note({ children }: { children: React.ReactNode }) {
  return <p className="m-0 text-sm text-on-surface-variant">{children}</p>
}
