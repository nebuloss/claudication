import { useMemo, useState } from 'react'
import { api, type ApiKey, type GatewayConfig, type InstallFile } from '../../api/client'
import { useAction, useLoader } from '../hooks'
import {
  Button,
  Card,
  CopyField,
  ErrorBanner,
  ErrorState,
  Loading,
  Muted,
  Note,
  Segmented,
  Stack,
  useConfirm,
} from '../primitives'
import { Catalog } from './docs/catalog'
import { ClientBody } from './docs/client-body'
import { CLIENTS } from './docs/content'
import { EXAMPLE_BASE } from './docs/model'
import { relayAddress } from './key-configs'

/**
 * A key's Clients page: every client's configuration with the key already
 * inside, each file shown — the key masked — and a click from being
 * downloaded. The key itself, and regenerating it, are on the overview
 * (KeySecret).
 *
 * The secret is kept sealed beside the account tokens (migration 026), so it
 * is read whenever this page opens rather than shown once in a dialog and
 * lost: a client set up next month is set up from here, with the same key.
 *
 * The files are the committed ones under configs/clients, as the public docs
 * page shows them, with this gateway's address, this key and the models this
 * key is offered filled in. They ask for the model aliases, never a real
 * model: which model claude-large or claude-opus is, is decided in the gateway
 * (Settings › Aliases, or this key's Models page), so changing it reaches
 * every client without a new file.
 */
/**
 * A key's secret, read again from its sealed copy; '' for a key from before
 * secrets were kept. Keyed on the display as well as the id, so a regenerated
 * key is read afresh.
 */
function useKeySecret(k: ApiKey) {
  return useLoader(
    (signal) => (k.secret_kept ? api.keys.secret(k.id, signal) : Promise.resolve({ plaintext: '' })),
    { deps: [k.id, k.secret_kept, k.display] },
  )
}

export default function KeyClients({
  apiKey: k,
  config,
}: {
  apiKey: ApiKey
  config: GatewayConfig | null
}) {
  const secret = useKeySecret(k)
  // What this key is offered — /v1/models as it sees it, the aliases first,
  // each with the facts of the model it serves for this key. Without it the
  // files are the committed ones, which are complete.
  const offered = useLoader((signal) => api.keys.offeredModels(k.id, signal), { deps: [k.id] })
  const catalog = useMemo(() => new Catalog(offered.data), [offered.data])
  const address = relayAddress(config)
  const gateway = address !== '' ? address : EXAMPLE_BASE
  const plaintext = secret.data?.plaintext ?? ''

  if (secret.error !== '') {
    return <ErrorState message={secret.error} onRetry={() => void secret.reload()} busy={secret.loading} />
  }

  return (
    <>
      {!k.secret_kept && (
        <Note tone="warn">
          This key&rsquo;s secret is not stored, so no configuration can be written with it.
          Regenerate it on its overview page.
        </Note>
      )}
      {k.secret_kept && secret.loading && plaintext === '' && <Loading>Reading the key…</Loading>}

      {plaintext !== '' && (
        <>
          <Note>
            Each file carries this key and the gateway&rsquo;s address: treat a downloaded file like
            the key itself. They ask for the model aliases —{' '}
            <code>claude-large</code>,{' '}
            <code>claude-small</code> and one per family — never a real
            model, so changing what an alias serves, in Settings › Aliases or on this key&rsquo;s
            Models page, reaches every client without a new file.
          </Note>
          {address === '' && (
            <Note tone="warn">
              The admin UI is on its own listener and <code>public-url</code>{' '}
              is not set, so these files use <code>{EXAMPLE_BASE}</code>.
              Replace it with the relay&rsquo;s address before using them.
            </Note>
          )}
          <InstallCommand
            keyId={k.id}
            gateway={gateway}
            files={() =>
              CLIENTS.flatMap((client) =>
                client.files(gateway, plaintext, catalog).map((f) => ({
                  client_id: client.id,
                  client: client.label,
                  path: f.install.path,
                  windows_path: f.install.windowsPath,
                  env: f.install.env,
                  body: f.body,
                })),
              )
            }
          />
          {CLIENTS.map((client) => (
            <Card key={client.id} title={client.label}>
              <ClientBody
                client={client}
                gateway={gateway}
                secret={plaintext}
                catalog={catalog}
                surfaces={config?.surfaces ?? []}
                notes={false}
              />
            </Card>
          ))}
        </>
      )}
    </>
  )
}

/**
 * A key's secret, on its overview: hidden until the eye is pressed, copyable
 * either way, and regenerated here for a key that has leaked — the old one
 * stops at once, and its clients fetch new configurations from the Clients
 * page.
 */
export function KeySecret({ apiKey: k, onChanged }: { apiKey: ApiKey; onChanged: () => void }) {
  const secret = useKeySecret(k)
  const { pending: busy, error, run } = useAction()
  const confirm = useConfirm()
  const plaintext = secret.data?.plaintext ?? ''

  const rotate = async () => {
    await api.keys.rotate(k.id)
    onChanged()
    await secret.reload()
  }
  // Asked only when there is a working secret to lose: a key whose secret was
  // never kept has nothing a client could still be using.
  const regenerate = () =>
    k.secret_kept
      ? confirm({
          title: `Regenerate the key for ${k.name}?`,
          body: 'The current one stops working immediately, so every client using it needs its configuration again.',
          confirmLabel: 'Regenerate',
          tone: 'danger',
          action: rotate,
        })
      : run('rotate', rotate)

  return (
    <Card>
      {!k.secret_kept ? (
        <Note tone="warn" className="mb-4">
          This key&rsquo;s secret is not stored, so it cannot be shown. Regenerate it to get one.
        </Note>
      ) : secret.error !== '' ? (
        <ErrorBanner error={secret.error} className="mb-4" />
      ) : secret.loading && plaintext === '' ? (
        <div className="mb-4">
          <Loading>Reading the key…</Loading>
        </div>
      ) : (
        <div className="mb-4">
          <CopyField label="API key" value={plaintext} secret />
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        {/* Filled when there is no secret to lose: then it is the only way to
            a working key, rather than the destructive way out of one. */}
        <Button
          variant={k.secret_kept ? 'outlined' : 'filled'}
          tone={k.secret_kept ? 'error' : undefined}
          busy={busy}
          onClick={() => void regenerate()}
        >
          Regenerate key
        </Button>
        <Muted as="span">For a key that has leaked: the current one stops working at once.</Muted>
      </div>
      <ErrorBanner error={error} className="mt-4" />
    </Card>
  )
}

type System = 'unix' | 'windows'

/**
 * One command that writes a client's configuration into place: a token for
 * every client's files, kept a quarter of an hour, and the address that
 * fetches them with the shell and the client chosen here.
 *
 * The token is made once, on asking, and covers every client; choosing a
 * system or a client after that only rewrites the command around it, so the
 * same token sets up Codex on one machine and Claude Code on another.
 */
function InstallCommand({
  keyId,
  gateway,
  files,
}: {
  keyId: string
  gateway: string
  files: () => InstallFile[]
}) {
  const [system, setSystem] = useState<System>(() =>
    /windows/i.test(navigator.userAgent) ? 'windows' : 'unix',
  )
  const [client, setClient] = useState('all')
  const [link, setLink] = useState<{ token: string; expires: Date } | null>(null)
  const { pending: busy, error, run } = useAction()

  const create = () =>
    run('create', async () => {
      const made = await api.keys.createInstall(keyId, files())
      setLink({ token: made.token, expires: new Date(made.expires_at) })
    })

  const url =
    link === null
      ? ''
      : `${gateway}/install?token=${link.token}&shell=${system === 'windows' ? 'ps1' : 'sh'}` +
        (client === 'all' ? '' : `&client=${client}`)
  const command =
    system === 'windows' ? `irm '${url}' | iex` : `curl -fsSL '${url}' | sh`

  return (
    <Card title="Install with one command">
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Segmented
          label="System"
          value={system}
          onChange={setSystem}
          options={[
            { id: 'unix', label: 'Linux and macOS', content: 'Linux & macOS' },
            { id: 'windows', label: 'Windows', content: 'Windows' },
          ]}
        />
        <Segmented
          label="Client"
          value={client}
          onChange={setClient}
          options={[
            { id: 'all', label: 'Every client', content: 'All' },
            ...CLIENTS.map((c) => ({ id: c.id, label: c.label, content: c.label })),
          ]}
        />
      </div>
      {link === null ? (
        <Button busy={busy} onClick={() => void create()}>
          Create command
        </Button>
      ) : (
        <Stack gap={3}>
          <CopyField
            label={system === 'windows' ? 'PowerShell' : 'Terminal'}
            value={command}
            conceal={link.token}
          />
          <Note>
            Works until {link.expires.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })},
            on as many machines as it is run on. It writes each file in place and keeps whatever was
            there beside it as a .bak, so running a newer command is an update.
            {system === 'windows'
              ? ' Claude Code gets its settings as user environment variables, for new terminals.'
              : ' Claude Code\u2019s file is sourced from your shell profile, for new shells.'}
          </Note>
          <div>
            <Button variant="text" onClick={() => void create()}>
              New command
            </Button>
          </div>
        </Stack>
      )}
      <ErrorBanner error={error} className="mt-4" />
    </Card>
  )
}
