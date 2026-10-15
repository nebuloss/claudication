import { type GatewayConfig } from '../../api/client'

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
