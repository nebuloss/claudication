import { useMemo, useState } from 'react'
import { api, messageOf, type ApiKey, type GatewayConfig } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  Card,
  CardTitle,
  CopyField,
  ErrorState,
  FilledButton,
  Note,
  OutlinedButton,
  Spinner,
} from '../primitives'
import { CodeViewer } from '../primitives/code'
import { Catalog } from './docs/catalog'
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
export default function KeyClients({
  apiKey: k,
  config,
}: {
  apiKey: ApiKey
  config: GatewayConfig | null
}) {
  const secret = useLoader(
    () => (k.secret_kept ? api.keySecret(k.id) : Promise.resolve({ plaintext: '' })),
    undefined,
    [k.id, k.secret_kept, k.display],
    0,
  )
  // What this key is offered — /v1/models as it sees it, the aliases first,
  // each with the facts of the model it serves for this key. Without it the
  // files are the committed ones, which are complete.
  const offered = useLoader(() => api.keyOfferedModels(k.id), undefined, [k.id], 0)
  const catalog = useMemo(() => new Catalog(offered.data?.data), [offered.data])
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
      {k.secret_kept && secret.loading && plaintext === '' && (
        <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Reading the key…
        </p>
      )}

      {plaintext !== '' && (
        <>
          <Note>
            Each file carries this key and the gateway&rsquo;s address: treat a downloaded file like
            the key itself. They ask for the model aliases —{' '}
            <code className="font-mono">claude-large</code>,{' '}
            <code className="font-mono">claude-small</code> and one per family — never a real
            model, so changing what an alias serves, in Settings › Aliases or on this key&rsquo;s
            Models page, reaches every client without a new file.
          </Note>
          {address === '' && (
            <Note tone="warn">
              The admin UI is on its own listener and <code className="font-mono">public-url</code>{' '}
              is not set, so these files use <code className="font-mono">{EXAMPLE_BASE}</code>.
              Replace it with the relay&rsquo;s address before using them.
            </Note>
          )}
          {CLIENTS.map((client) => {
            const missing = client.missingSurfaces(config?.surfaces ?? [])
            return (
              <Card key={client.id}>
                <CardTitle>{client.label}</CardTitle>
                <div className="mb-4">
                  <CopyField label="Base URL" value={client.baseURL(gateway)} />
                </div>
                <p className="mt-0 mb-4 text-sm leading-relaxed text-on-surface">{client.lead}</p>
                {missing.length > 0 && (
                  <Note tone="warn" className="mb-4">
                    {client.label} needs the {missing.map((s) => s.title).join(' and ')}, which is
                    switched off on this gateway. Until it is on, this configuration answers 404.
                  </Note>
                )}
                <div className="flex flex-col gap-4">
                  {client.files(gateway, plaintext, catalog).map((f) => (
                    <CodeViewer
                      key={f.filename}
                      label={f.label}
                      filename={f.filename}
                      lang={f.lang}
                      value={f.body}
                      conceal={plaintext}
                    />
                  ))}
                </div>
              </Card>
            )
          })}
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
  const secret = useLoader(
    () => (k.secret_kept ? api.keySecret(k.id) : Promise.resolve({ plaintext: '' })),
    undefined,
    [k.id, k.secret_kept, k.display],
    0,
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const plaintext = secret.data?.plaintext ?? ''

  const regenerate = async () => {
    if (
      k.secret_kept &&
      !window.confirm(
        `Regenerate the key for ${k.name}? The current one stops working immediately, ` +
          'so every client using it needs its configuration again.',
      )
    )
      return
    setBusy(true)
    setError('')
    try {
      await api.rotateKey(k.id)
      onChanged()
      await secret.reload()
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy(false)
  }

  return (
    <Card>
      {!k.secret_kept ? (
        <Note tone="warn" className="mb-4">
          This key&rsquo;s secret is not stored, so it cannot be shown. Regenerate it to get one.
        </Note>
      ) : secret.error !== '' ? (
        <Banner tone="error" className="mb-4">
          {secret.error}
        </Banner>
      ) : secret.loading && plaintext === '' ? (
        <p className="mt-0 mb-4 flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Reading the key…
        </p>
      ) : (
        <div className="mb-4">
          <CopyField label="API key" value={plaintext} secret />
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        {k.secret_kept ? (
          <OutlinedButton tone="error" disabled={busy} onClick={() => void regenerate()}>
            {busy && <Spinner />}
            Regenerate key
          </OutlinedButton>
        ) : (
          <FilledButton type="button" disabled={busy} onClick={() => void regenerate()}>
            {busy && <Spinner />}
            Regenerate key
          </FilledButton>
        )}
        <span className="text-sm text-on-surface-variant">
          For a key that has leaked: the current one stops working at once.
        </span>
      </div>
      {error !== '' && (
        <Banner tone="error" className="mt-4">
          {error}
        </Banner>
      )}
    </Card>
  )
}
