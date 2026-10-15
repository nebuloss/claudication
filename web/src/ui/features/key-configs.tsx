import { useMemo } from 'react'
import { api, type GatewayConfig } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Spinner, TonalButton, saveAs } from '../primitives'
import { Catalog } from './docs/catalog'
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
 * Every client's config file, with a key already in it.
 *
 * Here and not on the public docs page, because this needs the key's secret,
 * which only an operator may see. The files are the committed ones under
 * configs/clients, the same the docs page shows, with the address, the key
 * and that app's models swapped in — so what is downloaded here and what the
 * docs describe cannot drift apart.
 */
/** Each client here as the gateway tells it apart, for its own model list. */
const APP_OF: Record<string, string> = {
  'claude-code': 'Claude Code',
  codex: 'Codex',
  opencode: 'opencode',
  crush: 'crush',
}

export function KeyConfigs({
  plaintext,
  config,
  keyId,
}: {
  plaintext: string
  config: GatewayConfig | null
  keyId: string
}) {
  const address = relayAddress(config)
  const gateway = address !== '' ? address : EXAMPLE_BASE

  // Each app's own model list for this key — what /v1/models answers that app
  // — read once when the dialog opens, so a file lists the virtual models that
  // app is mapped to and defaults to them. A failure is not an error worth
  // showing over the key: the files fall back to the committed ones, which
  // are complete, and the line below says so.
  const { data: lists, loading } = useLoader(
    async () => {
      const entries = await Promise.all(
        CLIENTS.map(async (c) => {
          const app = APP_OF[c.id] ?? ''
          try {
            const res = await api.keyClientModels(keyId, app)
            return [c.id, res.data] as const
          } catch {
            return [c.id, null] as const
          }
        }),
      )
      return Object.fromEntries(entries)
    },
    undefined,
    [keyId],
    0,
  )
  const catalogs = useMemo(() => {
    const out: Record<string, Catalog> = {}
    for (const c of CLIENTS) out[c.id] = new Catalog(lists?.[c.id])
    return out
  }, [lists])
  const models = lists
  const catalog = catalogs[CLIENTS[0].id] ?? new Catalog(null)

  return (
    <section className="mt-6">
      <h3 className="m-0 text-sm font-medium text-on-surface">Configure a client</h3>
      <p className="mt-1 mb-3 text-xs text-on-surface-variant">
        Each file already carries this key and the gateway&rsquo;s address. Treat a downloaded file
        like the key itself.{' '}
        {loading && models === null ? (
          <span className="inline-flex items-center gap-1">
            <Spinner className="size-3" /> Reading the model list…
          </span>
        ) : catalog.known ? (
          'Models: what each app is offered with this key — its virtual models first — read just now.'
        ) : (
          'Models: the list shipped with this release, because the live one could not be read.'
        )}
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
                {client.files(gateway, plaintext, catalogs[client.id] ?? catalog).map((f) => (
                  <TonalButton
                    key={f.filename}
                    onClick={() => saveAs(f.filename, f.body)}
                    disabled={loading && models === null}
                  >
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
