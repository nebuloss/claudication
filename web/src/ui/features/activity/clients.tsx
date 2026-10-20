import type { SubscriptionShare } from '../../../api/client'
import { Muted } from '../../primitives'
import { PROVIDERS, providerLabel } from '../../providers'
import { ClientTable, type ShareColumn } from '../usage/tables'
import { GatewayUsage } from './slots'

/**
 * Usage by client across the gateway: the same table as an account's Usage
 * page, read for every account at once, with each client's share of each
 * provider's pooled windows — the estimate its own page shows — and unfolded
 * to the model names and models it used.
 */
export default function ActivityClients() {
  return (
    <GatewayUsage title="By client">
      {({ rows, shares }) => {
        const columns = poolColumns(shares)
        return (
          <>
            <ClientTable rows={rows} shares={columns} />
            {columns.length > 0 && (
              <div className="mt-4">
                <Muted size="xs">
                  A share is an estimate of each provider&rsquo;s windows as they stand now, whatever the
                  period above: on each account, the window&rsquo;s use split between clients by their
                  cost-weighted part of what this gateway sent it, averaged over the provider&rsquo;s
                  accounts. A dash is no traffic in that window.
                </Muted>
              </div>
            )}
          </>
        )
      }}
    </GatewayUsage>
  )
}

/**
 * A 5-hour and a 7-day column per provider whose accounts report their
 * windows, listed providers first. Named for the provider only when there is
 * more than one: a gateway on Claude alone has two columns that need no
 * qualifier.
 */
function poolColumns(shares: Record<string, SubscriptionShare>): ShareColumn[] {
  const present = new Set(
    Object.values(shares).flatMap((s) => s.providers.filter((p) => p.accounts > 0).map((p) => p.provider)),
  )
  const order = (id: string) => {
    const i = PROVIDERS.findIndex((p) => p.id === id)
    return i < 0 ? PROVIDERS.length : i
  }
  const providers = [...present].sort((a, b) => order(a) - order(b) || a.localeCompare(b))
  const pool = (id: string, provider: string) => shares[id]?.providers.find((p) => p.provider === provider)
  return providers.flatMap((p) => {
    const whose = providers.length > 1 ? `${providerLabel(p)} ` : ''
    return [
      { label: `${whose}5-hour share`, points: (id: string) => pool(id, p)?.five_hour },
      { label: `${whose}7-day share`, points: (id: string) => pool(id, p)?.seven_day },
    ]
  })
}
