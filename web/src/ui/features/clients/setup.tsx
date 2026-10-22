import { useState } from 'react'
import { api, type ClientFile, type GatewayConfig, type InstallState } from '../../../api/client'
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
  Section,
  Segmented,
  Stack,
  useConfirm,
} from '../../primitives'
import { countdown, dateTime } from '../../format'
import { relayAddress } from '../../clients/address'
import { ClientBody } from '../../clients/client-body'
import { EXAMPLE_BASE, MCP_PATH } from '../../clients/model'
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
 * new one. A configured typed client gets an update command instead of the
 * install one: its files without the key, for a machine set up already. The
 * page re-reads the client every few seconds while it is open, so a command
 * run on another machine turns this into the "configured" note without a
 * reload.
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
        <Stack>
          <Note>
            Configured on {dateTime(c.wire.configured_at)}
            {used !== undefined && ` — its install command ran from ${used.ip || 'an unknown address'} (${platformName(used.shell)})`}
            . Its key is not shown again. To set it up afresh, regenerate the key: the current one stops working at
            once.
          </Note>
          <div>
            <Button variant="outlined" tone="error" busy={busy === 'rotate'} onClick={() => void regenerate()}>
              Regenerate key
            </Button>
          </div>
          <ErrorBanner error={error} />
          {c.installs && <UpdateConfig client={c} gateway={gateway} onChanged={onChanged} />}
        </Stack>
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
 * A client's files as the gateway writes them for this address: its names,
 * each described as the model it serves now. Read again when a name or what
 * it serves changes.
 */
function useFiles(c: Client, gateway: string): { files: ClientFile[] | null; error: string } {
  const files = useLoader((signal) => api.clients.files(c.id, gateway, signal), {
    deps: [c.id, gateway, c.slots.map((s) => `${s.name}=${s.serving}`).join()],
  })
  return { files: files.data, error: files.error }
}

/**
 * A typed client's files, written with its own names and the facts of the
 * models they serve now, and the one command that installs them.
 */
function TypedSetup({ client: c, gateway, onChanged }: { client: Client; gateway: string; onChanged: () => void }) {
  const recipe = recipeFor(c.type.id)
  const secret = useSecret(c)
  const { files, error } = useFiles(c, gateway)
  if (recipe === undefined) return null
  if (error !== '') return <ErrorBanner error={error} />
  if (secret === '' || files === null) return <Loading>Reading the key…</Loading>
  return (
    <>
      <InstallCommand client={c} gateway={gateway} state={c.wire.install} onChanged={onChanged} />
      <Note>
        Each file carries this client&rsquo;s key and its model names, each described as the model it serves
        now. Switching a name to a model with other limits — a smaller window, other effort levels — means
        running the install command again, or once the client is set up, its update command, which carries no
        key.
      </Note>
      <Note>
        Each also registers the gateway&rsquo;s MCP server, <code>{gateway + MCP_PATH}</code>, under the name{' '}
        <code>claudication</code> and with this client&rsquo;s key: tools for its own agent to read what it
        serves and its limits — and, with <em>Agent may manage this client</em> on, to switch its names and make
        its update command. Your other MCP servers stay as they are.
      </Note>
      <ClientBody recipe={recipe} gateway={gateway} secret={secret} files={files} />
    </>
  )
}

/**
 * Updating a configured client's files on a machine already set up: its
 * model names and what each serves, written with the facts of the models
 * they serve now, and no key. The command keeps the key that is on the
 * machine — Claude Code's line, crush's and opencode's provider key — and
 * refuses a machine that has none, so it is safe to put on a screen: it is
 * the one way to catch a client up after a name is added or rebound without
 * regenerating its key.
 */
function UpdateConfig({ client: c, gateway, onChanged }: { client: Client; gateway: string; onChanged: () => void }) {
  const recipe = recipeFor(c.type.id)
  if (recipe === undefined) return null
  const used = c.wire.update.used
  return (
    <Section title="Update config">
      <Stack gap={3}>
        <Muted>Updates the model names and what they serve on a machine already set up; the key stays as it is.</Muted>
        <InstallCommand client={c} gateway={gateway} state={c.wire.update} onChanged={onChanged} kind="update" />
        {used !== undefined && (
          <KeyValue items={[['Last update', `${dateTime(used.at)} from ${used.ip || 'an unknown address'} (${platformName(used.shell)})`]]} />
        )}
      </Stack>
    </Section>
  )
}

type Platform = 'unix' | 'windows'

const platformName = (shell: string) => (shell === 'ps1' ? 'Windows' : 'Linux or macOS')

/**
 * One command that writes the client's configuration into place: a link made
 * on asking, good for one fetch and a quarter of an hour, and its state as it
 * goes — waiting, with the time left; used, from where; expired. An install
 * carries the key; an update the same files without it (UpdateConfig).
 */
function InstallCommand({
  client: c,
  gateway,
  state,
  onChanged,
  kind = 'install',
}: {
  client: Client
  gateway: string
  state: InstallState
  onChanged: () => void
  kind?: 'install' | 'update'
}) {
  const [platform, setPlatform] = useState<Platform>(() => (/windows/i.test(navigator.userAgent) ? 'windows' : 'unix'))
  const { busy, error, run } = useAction()
  const now = useNow(1000)

  const create = () =>
    run('create', async () => {
      await (kind === 'update' ? api.clients.createUpdate : api.clients.createInstall)(c.id, gateway)
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
          <KeyValue items={[['Waiting', `to be run — ${countdown(left)} left, once`]]} />
        </>
      ) : (
        <>
          {(state.state === 'expired' || (state.state === 'waiting' && left === 0)) && (
            <Note tone="warn">The last command expired without being run.</Note>
          )}
          <div>
            <Button busy={busy === 'create'} onClick={() => void create()}>
              {state.state === 'none' ? (kind === 'update' ? 'Create update command' : 'Create install command') : 'New command'}
            </Button>
          </div>
        </>
      )}
      <Muted size="xs">
        A command works once, for fifteen minutes. A client&rsquo;s own config gets only the gateway&rsquo;s
        sections rewritten — its other providers and settings stay — and every file it changes is kept beside
        it as a .bak.
        {kind === 'update' && ' It carries no key, and changes nothing on a machine that was never set up with this client.'}
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
          ['MCP server', `${gateway}${MCP_PATH} — Streamable HTTP, Authorization: Bearer with the same key`],
        ]}
      />
      <Note>
        The key works on both, and both reach every model: one served by the other API&rsquo;s provider is
        translated. Paste the key in place of <code>clc_…</code>.
      </Note>
      <CopyField label="SDK environment" value={env} />
      <CopyField label="curl, Anthropic Messages" value={curlMessages} />
      <CopyField label="curl, OpenAI Responses" value={curlResponses} />
      <Note>
        The gateway&rsquo;s MCP server gives an agent tools about this client: what its names serve, the models
        it may use, its limits and usage, its chats — and, with <em>Agent may manage this client</em> on, switching
        its names. Point an MCP client at it with the key as a bearer token.
      </Note>
      <CopyField label="MCP server (Streamable HTTP)" value={`${gateway}${MCP_PATH}`} />
      <CopyField label="MCP header" value="Authorization: Bearer clc_…" />
    </Stack>
  )
}
