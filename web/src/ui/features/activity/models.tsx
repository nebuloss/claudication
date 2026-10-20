import { InlineMeter } from '../../primitives'
import { smallPercent } from '../../format'
import { providerTitle } from '../../clients/specs'
import { byTokens, percentOf, rollup, tokensOf, type Rollup } from '../usage/rollup'
import { ClientTable, TokenSplit, UsageTable } from '../usage/tables'
import { GatewayUsage } from './slots'

type ModelUse = Rollup & { model: string; provider: string }

/**
 * Usage by model across the gateway: which models the traffic went to, by
 * tokens, with each one's part of all requests and of all tokens, and —
 * unfolded — the clients that asked for it.
 *
 * By the model that served, not the name a client asked by: a slot is a
 * client's word for a model, and two clients' "large" can be different
 * models. The model with its provider, because two providers can serve a
 * model of the same name and they are separate budgets.
 */
export default function ActivityModels() {
  return (
    <GatewayUsage title="Models" by="model">
      {({ rows }) => {
        const models: ModelUse[] = rollup(rows, (r) => JSON.stringify([r.provider, r.model]))
          .map((u) => ({ ...u, model: u.rows[0]!.model, provider: u.rows[0]!.provider }))
          .sort(byTokens)
        const requests = models.reduce((n, m) => n + m.requests, 0)
        const tokens = models.reduce((n, m) => n + tokensOf(m), 0)
        const part = (p: number) => <InlineMeter percent={p} tone="neutral" figure={smallPercent(p)} />
        return (
          <UsageTable
            groups={models}
            name={{
              head: 'Model',
              label: (m) => `the clients of ${m.model || 'no model'}`,
              cell: (m) => (
                <>
                  <code className="break-all">{m.model || '—'}</code>
                  <div className="text-xs text-on-surface-variant">{providerTitle(m.provider)}</div>
                </>
              ),
            }}
            columns={[
              { head: { label: 'Of requests', align: 'end' }, cell: (m) => part(percentOf(m.requests, requests)) },
              { head: { label: 'Of tokens', align: 'end' }, cell: (m) => part(percentOf(tokensOf(m), tokens)) },
            ]}
            detail={(m) => (
              <>
                <TokenSplit of={m} />
                {/* The clients without their own unfolding: their model names are
                    one level further, on the Clients page or their own. */}
                <ClientTable rows={m.rows} detail={false} />
              </>
            )}
          />
        )
      }}
    </GatewayUsage>
  )
}
