import { type GatewayConfig } from '../../api/client'
import { Banner, TonalButton, saveAs } from '../primitives'
import { CLIENTS } from './docs/content'
import { EXAMPLE_BASE } from './docs/model'

/**
 * Where a client should point, as far as the admin UI can tell.
 *
 * public-url when it is configured. Otherwise this browser's origin — but only
 * while one listener serves everything: once admin-listen splits them, this
 * page is on the admin address and the relay is somewhere this gateway cannot
 * see, so the honest answer is "unknown" rather than an address that 404s.
 */
export function relayAddress(config: GatewayConfig | null): string {
  const setting = (key: string) => config?.settings.find((s) => s.key === key)?.value ?? ''
  const configured = setting('public-url').replace(/\/+$/, '')
  if (configured !== '') return configured
  if (config !== null && setting('admin-listen') === '') return window.location.origin
  return ''
}

/**
 * Every client's config file, with the key just minted already in it.
 *
 * Here and not on the public docs page, because this is the one moment the
 * plaintext exists: the store keeps only its hash, so a file downloaded now is
 * the only config that will ever be complete without someone pasting the key
 * into it by hand. The files are the committed ones under configs/clients, the
 * same the docs page shows, with the address and the key swapped in — so what
 * is downloaded here and what the docs describe cannot drift apart.
 */
export function KeyConfigs({
  plaintext,
  config,
}: {
  plaintext: string
  config: GatewayConfig | null
}) {
  const address = relayAddress(config)
  const gateway = address !== '' ? address : EXAMPLE_BASE

  return (
    <section className="mt-6">
      <h3 className="m-0 text-sm font-medium text-on-surface">Configure a client</h3>
      <p className="mt-1 mb-3 text-xs text-on-surface-variant">
        Each file already carries this key and the gateway&rsquo;s address. Treat a downloaded file
        like the key itself.
      </p>

      {address === '' && (
        <Banner tone="warn" className="mb-3">
          The admin UI is on its own listener and <code className="font-mono">public-url</code> is
          not set, so these files use <code className="font-mono">{EXAMPLE_BASE}</code>. Replace it
          with the relay&rsquo;s address before using them.
        </Banner>
      )}

      <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant rounded-[var(--radius-md3-m)] border border-outline-variant p-0">
        {CLIENTS.map((client) => {
          const missing = client.missingSurfaces(config?.surfaces ?? [])
          return (
            <li key={client.id} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
              <div className="min-w-0 flex-1">
                <div className="text-sm text-on-surface">{client.label}</div>
                <div className="truncate font-mono text-[11px] text-on-surface-variant">
                  {client.files(gateway).map((f) => f.label).join(' · ')}
                </div>
                {missing.length > 0 && (
                  <div className="mt-0.5 text-[11px] text-error">
                    {missing.map((s) => s.title).join(' and ')} is switched off — this client will
                    get 404s until it is on.
                  </div>
                )}
              </div>
              <span className="flex flex-wrap gap-2">
                {client.files(gateway, plaintext).map((f) => (
                  <TonalButton key={f.filename} onClick={() => saveAs(f.filename, f.body)}>
                    {f.filename}
                  </TonalButton>
                ))}
              </span>
            </li>
          )
        })}
      </ul>
    </section>
  )
}
