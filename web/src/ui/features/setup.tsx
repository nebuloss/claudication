import { useState } from 'react'
import { api, type GatewayConfig, type Overview as OverviewData } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  Card,
  CardTitle,
  CopyField,
  Spinner,
  SubNav,
} from '../primitives'

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
 * Setup: everything about pointing something at this gateway, in one place.
 *
 * It used to be two halves of two other screens — a card at the bottom of
 * Overview and another at the bottom of Settings — which meant the one job a
 * new user actually arrives to do was split across two tabs, under the ones
 * that administer a gateway they have not connected to yet.
 *
 * The per-client configuration is the part that was missing. Every client
 * disagrees about whether the base URL carries `/v1`, and none of them says so
 * when you get it wrong: you just get 404s. Printing the exact stanza with the
 * gateway's own address already in it removes the whole class of mistake.
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
  // Loaded separately and allowed to fail: the switches are useful context but
  // not the point of the screen, and a config this admin cannot read should
  // not blank out the instructions.
  const { data: config } = useLoader<GatewayConfig>(() => api.config())

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
          <ClientGuide client={client} base={base} />
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
            <div key={symptom} className="border-b border-outline-variant pb-3 last:border-0 last:pb-0">
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
    'Only haiku works; opus and sonnet 429',
    'The same attribution gate, seen from the other side.',
  ],
  [
    'Codex: "stream closed before response.completed"',
    'The gateway sends a terminal event on every path, including failures — so this points at something between it and Codex, usually a proxy buffering the stream.',
  ],
]

/** The per-client stanza, with this gateway's address already in it. */
function ClientGuide({ client, base }: { client: Client; base: string }) {
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
            value={`export ANTHROPIC_BASE_URL=${base}\nexport ANTHROPIC_AUTH_TOKEN=clc_…\nclaude`}
          />
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
model = "claude-sonnet-5"

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
            Codex warns that it has no metadata for a Claude model and falls back to conservative
            defaults, which makes it compact the conversation far too early.{' '}
            <code>scripts/codex-model-catalog.py</code> in the repository writes a catalog that
            fixes it. Codex also needs <code>bubblewrap</code> on the host before it can run any
            shell command.
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
            models come from <code>/v1/models</code> and nothing needs listing by hand.
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
  "model": "anthropic/claude-opus-5"
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
            , and the model list has to be spelled out: crush never calls <code>/v1/models</code>,
            so a model missing from this file cannot be chosen however well the gateway serves it.
          </Note>
          <CopyField
            label="~/.config/crush/crush.json"
            value={`{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "claudication": {
      "name": "Claudication Gateway",
      "base_url": "${base}",
      "type": "anthropic",
      "api_key": "clc_…",
      "models": [
        {
          "id": "claude-opus-5",
          "name": "Claude Opus 5",
          "context_window": 1000000,
          "default_max_tokens": 128000,
          "can_reason": true,
          "supports_attachments": true,
          "cost_per_1m_in": 0,
          "cost_per_1m_out": 0,
          "cost_per_1m_in_cached": 0,
          "cost_per_1m_out_cached": 0,
          "reasoning_levels": ["low", "medium", "high", "xhigh", "max"],
          "default_reasoning_effort": "medium"
        },
        {
          "id": "claude-haiku-4-5-20251001",
          "name": "Claude Haiku 4.5",
          "context_window": 200000,
          "default_max_tokens": 64000,
          "can_reason": false,
          "supports_attachments": true,
          "cost_per_1m_in": 0,
          "cost_per_1m_out": 0,
          "cost_per_1m_in_cached": 0,
          "cost_per_1m_out_cached": 0
        }
      ]
    }
  },
  "models": {
    "large": { "provider": "claudication", "model": "claude-opus-5", "think": true },
    "small": { "provider": "claudication", "model": "claude-haiku-4-5-20251001" }
  }
}`}
          />
          <Note>
            The zero costs are deliberate. A subscription is not billed per token, and leaving real
            prices in makes crush display a running total that is fiction.
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
  -d '{"model":"claude-haiku-4-5","max_tokens":64,
       "messages":[{"role":"user","content":"hello"}]}'`}
          />
          <CopyField
            label="OpenAI Responses"
            value={`curl -N ${base}/v1/responses \\
  -H "authorization: Bearer clc_…" \\
  -H "content-type: application/json" \\
  -d '{"model":"claude-sonnet-5","stream":true,
       "input":[{"type":"message","role":"user",
                 "content":[{"type":"input_text","text":"say hi"}]}]}'`}
          />
        </div>
      )
  }
}

/** A line of prose between snippets, quieter than the snippet itself. */
function Note({ children }: { children: React.ReactNode }) {
  return <p className="m-0 text-sm text-on-surface-variant">{children}</p>
}
