import { useState } from 'react'
import { api, type Account } from '../../../api/client'
import { useLoader } from '../../hooks'
import { Card, Empty, ErrorBanner, Loading, Stat, StatGrid, WindowPicker } from '../../primitives'
import { accountStatus } from '../../domain/account'
import { ago, compact, lastDays } from '../../format'
import { byTokens } from '../usage/rollup'
import { ClientTable, ShareNote, accountShareColumns } from '../usage/tables'
import QuotaMeter, { ReadAgain, useQuotaRead } from './quota-meter'

const WINDOWS = [1, 7, 30] as const

/**
 * An account's Usage page: the account itself first — its windows and what
 * it is doing now — then who spent it.
 *
 * The windows used to be on the overview only, so the page that answers "who
 * is using this account up" did not say how used up it was; it showed only the
 * clients, each as a run-on sentence over its own table.
 */
export function AccountUsage({ account, onChanged }: { account: Account; onChanged: () => void }) {
  return (
    <>
      <AccountNow account={account} onChanged={onChanged} />
      <AccountClients accountId={account.id} />
    </>
  )
}

/**
 * The account's state as it stands: whether it is serving and why not, what
 * it has open, when it last answered, and its windows with their resets.
 * Live, from the page's own reading of the account list.
 */
function AccountNow({ account: a, onChanged }: { account: Account; onChanged: () => void }) {
  const { reading, read } = useQuotaRead(a.id, onChanged)
  const state = accountStatus(a)
  return (
    <Card title="Now" aside={<ReadAgain reading={reading} onRead={read} />}>
      <StatGrid columns={3}>
        <Stat label="Status" value={state.headline} hint={state.detail} tone={state.tone === 'ok' ? undefined : state.tone} />
        <Stat
          label="In flight"
          value={compact(a.in_flight)}
          hint={a.in_flight === 1 ? 'request open now' : 'requests open now'}
        />
        <Stat label="Last request" value={ago(a.last_used_at)} />
      </StatGrid>
      <div className="mt-5">
        <QuotaMeter quota={a.quota} pending={reading} provider={a.provider} />
      </div>
    </Card>
  )
}

/**
 * Which clients drew on the account over a window, a row each: its share of
 * the account's 5-hour and 7-day windows, what it sent, when it last did, and
 * — unfolded — the names and models it used here with every token kind.
 *
 * The same rows as a client's own Usage page, read for one account, so the
 * two add up; and the same table as Activity's, which reads them for the
 * whole gateway.
 */
function AccountClients({ accountId }: { accountId: string }) {
  const [days, setDays] = useState<number>(7)
  const { data, error, loading } = useLoader((signal) => api.accounts.clients(accountId, days, signal), {
    deps: [accountId, days],
  })
  const rows = data?.rows ?? []
  const shares = data?.shares ?? {}
  const shared = Object.keys(shares).length > 0
  return (
    <Card title="By client" aside={<WindowPicker value={days} windows={WINDOWS} onChange={setDays} />}>
      <ErrorBanner error={error} />
      {loading && data === null ? (
        <Loading />
      ) : rows.length === 0 ? (
        <Empty>No client used this account in {lastDays(days)}.</Empty>
      ) : (
        <>
          <ClientTable
            rows={rows}
            provider={false}
            shares={shared ? accountShareColumns((id) => shares[id]) : []}
            sort={(x, y) =>
              // By the share of the windows, the 7-day one first: that is the
              // question the page is opened to answer, and it is the window
              // that runs out over a week. Tokens break the ties, which with
              // no shares at all (usage history off, or a quota never read)
              // is every row.
              (shares[y.id]?.seven_day ?? 0) - (shares[x.id]?.seven_day ?? 0) ||
              (shares[y.id]?.five_hour ?? 0) - (shares[x.id]?.five_hour ?? 0) ||
              byTokens(x, y)
            }
          />
          {shared && (
            <div className="mt-4">
              <ShareNote />
            </div>
          )}
        </>
      )}
    </Card>
  )
}
