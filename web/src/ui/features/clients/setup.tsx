import { useMemo, useState } from 'react'
import { api, type GatewayConfig, type InstallState } from '../../../api/client'
import { useAction, useLoader, useNow } from '../../hooks'
import {
  Button,
  Card,
  CopyField,
  ErrorBanner,
  KeyValue,
  Loading,
  Muted,
  Note,
  Segmented,
  Stack,
  useConfirm,
} from '../../primitives'
import { dateTime, duration } from '../../format'
import { relayAddress } from '../../clients/address'
import { ClientBody } from '../../clients/client-body'
import type { Setup } from '../../clients/configs'
import { EXAMPLE_BASE } from '../../clients/model'
import { recipeFor } from '../../clients/recipes'
import type { Client, ClientTypes } from '../../clients/specs'

/**
 * Setting a client up — its key and its configuration — or, once it is set
 * up, that it is.
 *
 * The key and the install command are offered until the key is first used:
 * the install command fetched, or a first request made with it by hand. After
 * that nothing here shows the key again, and the gateway refuses to — a key in
 * use somewhere has no reason to be on a screen. Regenerating it is how a
 * client is set up again: the old key stops, and this card comes back with a
 * new one. The page re-reads the client every few seconds while it is open, so
 * a command run on another machine turns this into the "configured" note
 * without a reload.
 */
export function SetupCard({
  client: c,
  types,
  config,
  onChanged,
}: {
  client: Client
  types: ClientTypes
  config: GatewayConfig | null
  onChanged: () => void
}) {
  const confirm = useConfirm()
  const { busy, error, run } = useAction()
  const regenerate = () =>
    run('rotate', () =>
      confirm({
        title: `Regenerate the key for ${c.name}?`,
        body: 'The current one stops working immediately, so the client needs setting up again.',
        confirmLabel: 'Regenerate',
        tone: 'danger',
        action: async () => {
          await api.clients.rotate(c.id)
          onChanged()
        },
      }),
    )
  const address = relayAddress(config)
  const gateway = address !== '' ? address : EXAMPLE_BASE

  if (c.configured) {
    const used = c.wire.install.used
    return (
      <Card title="Set up">
        <Note>
          Configured on {dateTime(c.wire.configured_at)}
          {used !== undefined && ` — its install command ran from ${used.ip || 'an unknown address'} (${platformName(used.shell)})`}
          . To set it up again, regenerate its key: the current one stops working at once.
        </Note>
        <div className="mt-4">
          <Button variant="outlined" tone="error" busy={busy === 'rotate'} onClick={() => void regenerate()}>
            Regenerate key
          </Button>
        </div>
        <ErrorBanner error={error} className="mt-4" />
      </Card>
    )
  }

  return (
    <Card title="Set up">
      <Stack>
        <KeyField client={c} />
        {address === '' && (
          <Note tone="warn">
            The admin UI is on its own listener and <code>public-url</code> is not set, so the addresses here
            are <code>{EXAMPLE_BASE}</code>. Replace it with the relay&rsquo;s address.
          </Note>
        )}
        {c.installs ? (
          <TypedSetup client={c} gateway={gateway} onChanged={onChanged} />
        ) : (
          <CustomSetup client={c} types={types} gateway={gateway} />
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="text" tone="error" busy={busy === 'rotate'} onClick={() => void regenerate()}>
            Regenerate key
          </Button>
          <Muted as="span">For a key that has leaked: the current one stops working at once.</Muted>
        </div>
        <ErrorBanner error={error} />
      </Stack>
    </Card>
  )
}

/** The client's key while it is still offered, hidden until revealed. */
function KeyField({ client: c }: { client: Client }) {
  const secret = useLoader((signal) => api.clients.secret(c.id, signal), { deps: [c.id, c.wire.display] })
  if (secret.error !== '') return <ErrorBanner error={secret.error} />
  if (secret.data === null) return <Loading>Reading the key…</Loading>
  return <CopyField label="API key" value={secret.data.plaintext} secret />
}

/** The secret for the files, read once alongside the key field's. */
function useSecret(c: Client): string {
  const secret = useLoader((signal) => api.clients.secret(c.id, signal), { deps: [c.id, c.wire.display] })
  return secret.data?.plaintext ?? ''
}

/**
 * A typed client's files, written with its own names and the facts of the
 * models they serve now, and the one command that installs them.
 */
function TypedSetup({ client: c, gateway, onChanged }: { client: Client; gateway: string; onChanged: () => void }) {
  const recipe = recipeFor(c.type.id)
  const secret = useSecret(c)
  // /v1/models as this client sees it: its names, each with its model's facts.
  const offered = useLoader((signal) => api.clients.offeredModels(c.id, signal), {
    deps: [c.id, c.slots.map((s) => `${s.name}=${s.serving}`).join()],
  })
  const setup = useMemo<Setup>(
    () => ({
      dialect: c.dialect,
      slots: c.slots.map((s) => ({
        id: s.id, role: s.role, field: s.field, name: s.name,
        served: offered.data?.find((m) => m.id === s.name),
      })),
    }),
    [c, offered.data],
  )
  if (recipe === undefined) return null
  if (secret === '') return <Loading>Reading the key…</Loading>
  return (
    <>
      <InstallCommand client={c} gateway={gateway} state={c.wire.install} onChanged={onChanged}
        files={() => recipe.installFiles(c.type.label, gateway, secret, setup)} />
      <Note>
        Each file carries this client&rsquo;s key and its model names, each described as the model it serves
        now. Switching a name to a model with other limits — a smaller window, other effort levels — means
        running the install command again.
      </Note>
      <ClientBody recipe={recipe} gateway={gateway} dialect={c.dialect} secret={secret} setup={setup} />
    </>
  )
}

type Platform = 'unix' | 'windows'

const platformName = (shell: string) => (shell === 'ps1' ? 'Windows' : 'Linux or macOS')

/**
 * One command that writes the client's configuration into place: a link made
 * on asking, good for one fetch and a quarter of an hour, and its state as it
 * goes — waiting, with the time left; used, from where; expired.
 */
function InstallCommand({
  client: c,
  gateway,
  state,
  files,
  onChanged,
}: {
  client: Client
  gateway: string
  state: InstallState
  files: () => ReturnType<NonNullable<ReturnType<typeof recipeFor>>['installFiles']>
  onChanged: () => void
}) {
  const [platform, setPlatform] = useState<Platform>(() => (/windows/i.test(navigator.userAgent) ? 'windows' : 'unix'))
  const { busy, error, run } = useAction()
  const now = useNow(1000)

  const create = () =>
    run('create', async () => {
      await api.clients.createInstall(c.id, files())
      onChanged()
    })

  const url = (token: string) => `${gateway}/install?token=${token}&shell=${platform === 'windows' ? 'ps1' : 'sh'}`
  const command = (token: string) =>
    platform === 'windows' ? `irm '${url(token)}' | iex` : `curl -fsSL '${url(token)}' | sh`
  const left = Math.max(0, Math.round((new Date(state.expires_at).getTime() - now) / 1000))

  return (
    <Stack gap={3}>
      <Segmented
        label="Platform"
        value={platform}
        onChange={setPlatform}
        options={[
          { id: 'unix', label: 'Linux and macOS', content: 'Linux & macOS' },
          { id: 'windows', label: 'Windows', content: 'Windows' },
        ]}
      />
      {state.state === 'waiting' && state.token !== undefined && left > 0 ? (
        <>
          <CopyField label={platform === 'windows' ? 'PowerShell' : 'Terminal'} value={command(state.token)}
            conceal={state.token} />
          <KeyValue items={[['Waiting', `to be run — ${duration(left)} left, once`]]} />
        </>
      ) : (
        <>
          {(state.state === 'expired' || (state.state === 'waiting' && left === 0)) && (
            <Note tone="warn">The last command expired without being run.</Note>
          )}
          <div>
            <Button busy={busy === 'create'} onClick={() => void create()}>
              {state.state === 'none' ? 'Create install command' : 'New command'}
            </Button>
          </div>
        </>
      )}
      <Muted size="xs">
        A command works once, for fifteen minutes. A client&rsquo;s own config gets only the gateway&rsquo;s
        sections rewritten — its other providers and settings stay — and every file it changes is kept beside
        it as a .bak.
        {c.type.id === 'claude-code' &&
          (platform === 'windows'
            ? ' Claude Code gets its settings as user environment variables, for new terminals.'
            : ' Claude Code’s file is sourced from your shell profile, for new shells.')}
      </Muted>
      <ErrorBanner error={error} />
    </Stack>
  )
}

/**
 * A custom client's setup: no files, but everything a tool needs to be
 * pointed here by hand — both APIs' addresses, with and without /v1, the
 * header each takes, its model names, and a snippet for each SDK and a curl
 * per API naming its first name.
 */
function CustomSetup({ client: c, types, gateway }: { client: Client; types: ClientTypes; gateway: string }) {
  const model = c.namesSlots ? (c.slots[0]?.name ?? 'large') : (c.slots[0]?.serving || 'claude-opus-5-5')
  const env = [
    `export ANTHROPIC_BASE_URL=${gateway}`,
    'export ANTHROPIC_API_KEY=clc_…',
    `export OPENAI_BASE_URL=${gateway}/v1`,
    'export OPENAI_API_KEY=clc_…',
  ].join('\n')
  const curlMessages =
    `curl ${gateway}/v1/messages -H 'x-api-key: clc_…' -H 'anthropic-version: 2023-06-01' ` +
    `-H 'content-type: application/json' -d '{"model":"${model}","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'`
  const curlResponses =
    `curl ${gateway}/v1/responses -H 'Authorization: Bearer clc_…' -H 'content-type: application/json' ` +
    `-d '{"model":"${model}","input":"hi","stream":true}'`
  return (
    <Stack gap={3}>
      <KeyValue
        items={[
          [types.dialectTitle('anthropic'), `${gateway} for SDKs that add /v1 themselves (Anthropic's), ${gateway}/v1 for the rest — x-api-key or Authorization: Bearer`],
          [types.dialectTitle('openai'), `${gateway}/v1 — Authorization: Bearer`],
          ['Models', c.namesSlots ? c.slots.map((s) => s.name).join(', ') || 'none yet' : 'real model ids, less the ones switched off'],
        ]}
      />
      <Note>
        Anthropic Messages reaches the Claude accounts only; OpenAI Responses reaches both, a Claude model
        translated. Paste the key in place of <code>clc_…</code>.
      </Note>
      <CopyField label="SDK environment" value={env} />
      <CopyField label="curl, Anthropic Messages" value={curlMessages} />
      <CopyField label="curl, OpenAI Responses" value={curlResponses} />
    </Stack>
  )
}
