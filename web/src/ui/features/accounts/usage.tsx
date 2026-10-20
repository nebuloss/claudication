import { useState } from 'react'
import { api, type SlotUse } from '../../../api/client'
import { useLoader } from '../../hooks'
import { clientHref } from '../../route'
import { AppLink, Card, ErrorBanner, Loading, Muted, Stack, WindowPicker } from '../../primitives'
import { ago, compact, smallPercent } from '../../format'
import { SlotTable } from '../clients/usage'

const WINDOWS = [1, 7, 30] as const

/**
 * Which clients drew on one account: each with what it sent, its share of the
 * account's 5-hour and 7-day windows now (the cost-weighted estimate the
 * client pages use), its last request, and — under it — the names and models
 * it used here. The same rows as a client's own Usage page, read for one
 * account, so the two add up.
 */
export function AccountClients({ accountId }: { accountId: string }) {
  const [days, setDays] = useState<number>(7)
  const { data, error, loading } = useLoader((signal) => api.accounts.clients(accountId, days, signal), {
    deps: [accountId, days],
  })
  const rows: SlotUse[] = data?.rows ?? []
  const byClient = new Map<string, SlotUse[]>()
  for (const r of rows) byClient.set(r.client_id, [...(byClient.get(r.client_id) ?? []), r])

  return (
    <Card title="By client" aside={<WindowPicker value={days} windows={WINDOWS} onChange={setDays} />}>
      <ErrorBanner error={error} />
      {loading && data === null ? (
        <Loading />
      ) : byClient.size === 0 ? (
        <Muted>No client used this account in this window.</Muted>
      ) : (
        <Stack gap={4}>
          {[...byClient.entries()].map(([id, own]) => {
            const share = data?.shares[id]
            const sum = (f: (r: SlotUse) => number) => own.reduce((n, r) => n + f(r), 0)
            const last = own.map((r) => r.last).sort().at(-1)
            const first = own[0]
            return (
              <Stack key={id} gap={2}>
                <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
                  <AppLink href={clientHref(id)} strong>
                    {first?.client || id}
                  </AppLink>
                  <Muted as="span" size="xs">
                    {first?.type || 'no longer a client'} · {compact(sum((r) => r.requests))} requests ·{' '}
                    {compact(sum((r) => r.input_tokens))} in · {compact(sum((r) => r.output_tokens))} out ·{' '}
                    {compact(sum((r) => r.cache_tokens))} cache
                    {sum((r) => r.reasoning_tokens) > 0 && ` · ${compact(sum((r) => r.reasoning_tokens))} reasoning`} ·
                    last {ago(last)}
                    {share !== undefined &&
                      ` · ${smallPercent(share.five_hour)} of its 5-hour window, ${smallPercent(share.seven_day)} of its 7-day`}
                  </Muted>
                </div>
                <SlotTable rows={own} />
              </Stack>
            )
          })}
        </Stack>
      )}
    </Card>
  )
}
