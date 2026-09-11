import { useState } from 'react'
import claudeCodeSh from '#configs/clients/claude-code.sh?raw'
import codexToml from '#configs/clients/codex.toml?raw'
import codexModels from '#configs/clients/codex-models.json?raw'
import crushJson from '#configs/clients/crush.json?raw'
import opencodeJsonc from '#configs/clients/opencode.jsonc?raw'
import {
  api,
  type GatewayConfig,
  type Model,
  type Overview as OverviewData,
} from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Card, CardTitle, CopyField, Spinner, SubNav } from '../primitives'

/**
 * The example address inside configs/clients/*. Swapping it for this gateway's
 * own is the only thing this screen does to those files.
 *
 * They are plain, complete, committed files — readable in the repository,
 * linked from the README, and usable as they stand after one edit. Importing
 * them here as text means there is one copy rather than a second set pasted
 * into a component, and the substitution is a string replace rather than a
 * template language with a renderer at each end.
 */
const EXAMPLE_BASE = 'https://claudication.example.com'

type Client = {
  id: string
  label: string
  /** Which API surface it talks to, so a switched-off one can warn. */
  surface: 'anthropic' | 'openai' | 'both'
  lead: string
  files: { label: string; filename: string; body: string }[]
  notes?: string[]
}

const CLIENTS: Client[] = [
  {
    id: 'claude-code',
    label: 'Claude Code',
    surface: 'anthropic',
    lead: 'No /v1 — Claude Code appends the path itself.',
    files: [{ label: 'claudication.sh', filename: 'claude-code.sh', body: claudeCodeSh }],
  },
  {
    id: 'codex',
    label: 'Codex CLI',
    surface: 'openai',
    lead:
      'With /v1, and the only client that goes through the OpenAI Responses API rather than ' +
      'the Anthropic one.',
    files: [
      { label: '~/.codex/config.toml', filename: 'config.toml', body: codexToml },
      { label: '~/.codex/claude-models.json', filename: 'claude-models.json', body: codexModels },
    ],
    notes: [
      'Codex needs bubblewrap installed before it can run any shell command. Without it the ' +
        'first tool call panics and the model then explains, convincingly, that nothing works.',
      'The catalog carries a short stand-in for Codex’s own system prompt, because Codex ' +
        'refuses an entry without one and its real prompt lives in its binary. That is about ' +
        'nine thousand tokens cheaper per turn, and less good at editing. ' +
        'scripts/codex-model-catalog.py clones the real one out of your own Codex if you would ' +
        'rather have that.',
    ],
  },
  {
    id: 'opencode',
    label: 'opencode',
    surface: 'anthropic',
    lead:
      'With /v1. It overrides the built-in Anthropic provider rather than declaring a new one, ' +
      'so every model the gateway serves is available without listing any of them.',
    files: [
      {
        label: '~/.config/opencode/opencode.jsonc',
        filename: 'opencode.jsonc',
        body: opencodeJsonc,
      },
    ],
  },
  {
    id: 'crush',
    label: 'crush',
    surface: 'anthropic',
    lead:
      'Without /v1, and every model spelled out: crush never calls /v1/models, so one missing ' +
      'from this file cannot be selected however well the gateway serves it.',
    files: [{ label: '~/.config/crush/crush.json', filename: 'crush.json', body: crushJson }],
  },
]

const SHELL: [string, string][] = [
  ['claudication keys add -name NAME', 'Mint an API key, for a provisioning script.'],
  ['claudication passwd', 'Reset the admin password. The recovery path when it is lost.'],
  ['claudication login-url', 'A single-use link that signs a browser in. Spent on first use.'],
  [
    'claudication backup FILE',
    'A consistent snapshot without stopping the service. Holds the sealing key and every stored ' +
      'token, so it is exactly as sensitive as the state directory.',
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

/**
 * Setup: everything about pointing something at this gateway, in one place.
 *
 * It used to be two halves of two other screens — a card at the bottom of
 * Overview and another at the bottom of Settings — which meant the one job a
 * new user actually arrives to do was split across two tabs, under the ones
 * that administer a gateway they have not connected to yet.
 */
export default function Setup({
  onExpired,
  onGoTo,
}: {
  onExpired: () => void
  onGoTo: (tab: 'accounts' | 'keys') => void
}) {
  const [client, setClient] = useState(CLIENTS[0].id)
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
  // handing out files that point at the wrong host.
  const configured = data.public_url ?? ''
  const unknown = configured === '' && data.admin_split === true
  const base = configured !== '' ? configured : window.location.origin

  const models = modelList?.data ?? []
  const recipe = CLIENTS.find((c) => c.id === client) ?? CLIENTS[0]
  const surfaces = config?.surfaces ?? []
  const off = surfaces.filter(
    (s) => !s.enabled && (recipe.surface === 'both' || s.id === recipe.surface),
  )

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
            relay on — set <code>public-url</code> and the files below will carry it.
          </Banner>
        )}

        <CopyField label="Base URL" value={base} />

        {base.startsWith('http://') && (
          <p className="mt-4 mb-0 text-xs text-on-surface-variant">
            Plain HTTP unless something in front terminates TLS, so the key travels in the clear on
            this network. A tunnel or a reverse proxy is the fix. It is also why every file below
            has a Download button: a browser will not give a page the clipboard over HTTP.
          </p>
        )}
      </Card>

      <Card>
        <CardTitle>Models</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          What your connected accounts can serve, read from the upstream just now. The gateway has
          no allowlist of its own — it relays whatever model a client asks for — so anything here
          works on both APIs. The files below list the models that existed when they were written;
          anything newer shows up here first.
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
          options={CLIENTS.map((c) => ({ id: c.id, label: c.label }))}
        />

        {off.length > 0 && (
          <Banner tone="warn" className="mt-4">
            {recipe.label} needs the {off.map((s) => s.title).join(' and ')}, which is switched off.
            Turn it back on under Settings, or this configuration will answer 404.
          </Banner>
        )}

        <div className="mt-4 flex flex-col gap-4">
          <p className="m-0 text-sm text-on-surface-variant">{recipe.lead}</p>
          {recipe.files.map((f) => (
            <CopyField
              key={f.filename}
              label={f.label}
              value={f.body.split(EXAMPLE_BASE).join(base)}
              download={f.filename}
            />
          ))}
          {(recipe.notes ?? []).map((n) => (
            <p key={n} className="m-0 text-sm text-on-surface-variant">
              {n}
            </p>
          ))}
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
