import { api, type Account } from '../../../api/client'
import { useLoader } from '../../hooks'
import { accountHref } from '../../route'
import { AppLink, Chip, ErrorState, Loading, Muted } from '../../primitives'
import { accountName, accountStatus, isNamed } from '../../domain/account'
import { planLabel, providerLabel } from '../../providers'
import { byTokens, emptyRollup, rollup, type Rollup } from '../usage/rollup'
import { ClientTable, ShareNote, TokenSplit, UsageTable, accountShareColumns } from '../usage/tables'
import { QuotaGauges } from '../accounts/quota-meter'
import { GatewayUsage } from './slots'

/** The account list is polled at the pace the accounts page polls it, for the status and the windows. */
const ACCOUNTS_REFRESH_MS = 20_000

/** An account's rollup, with the account where it still exists, or the name its history kept. */
type AccountUse = Rollup & { account?: Account; name: string }

/**
 * Usage by account: every account, with its state and its windows now, what
 * was sent through it over the window, and — unfolded — the clients that drew
 * on it, each with its share of the account's windows, as the account's own
 * Usage page has them.
 *
 * Every account is listed, idle ones too: an account that served nothing
 * this week is worth seeing beside the one that served everything. An
 * account since removed is listed by the name its history kept, for as long
 * as it has traffic in the window.
 */
export default function ActivityAccounts() {
  const list = useLoader((signal) => api.accounts.list(signal), { refreshMs: ACCOUNTS_REFRESH_MS })
  const accounts = list.data?.accounts ?? null
  if (accounts === null) {
    return list.error !== '' ? (
      <ErrorState message={list.error} onRetry={() => void list.reload()} busy={list.loading} />
    ) : (
      <Loading />
    )
  }
  return (
    <GatewayUsage title="By account" always={accounts.length > 0}>
      {({ rows, shares }) => {
        const used = new Map(rollup(rows, (r) => r.account_id ?? '').map((u) => [u.id, u]))
        const out: AccountUse[] = accounts.map((a) => ({
          ...(used.get(a.id) ?? emptyRollup(a.id)),
          account: a,
          name: accountName(a),
        }))
        for (const [id, u] of used) {
          if (!accounts.some((a) => a.id === id)) {
            out.push({ ...u, name: u.rows[0]!.account || (id === '' ? 'no account' : id) })
          }
        }
        // Busiest first; the sort is stable, so idle accounts keep their priority order.
        out.sort(byTokens)
        return (
          <UsageTable
            groups={out}
            name={{
              head: 'Account',
              label: (u) => `the clients of ${u.name}`,
              cell: (u) => <AccountName use={u} />,
            }}
            columns={[
              {
                head: 'Status',
                nowrap: true,
                cell: (u) =>
                  u.account === undefined ? (
                    <Muted as="span">removed</Muted>
                  ) : (
                    <Chip tone={accountStatus(u.account).tone}>{accountStatus(u.account).label}</Chip>
                  ),
              },
              {
                head: 'Windows',
                nowrap: true,
                cell: (u) =>
                  u.account?.quota === undefined ? (
                    <Muted as="span">–</Muted>
                  ) : (
                    <div className="flex flex-col gap-1">
                      <QuotaGauges quota={u.account.quota} />
                    </div>
                  ),
              },
            ]}
            detail={(u) => {
              if (u.rows.length === 0) return <Muted size="xs">No client used it in this window.</Muted>
              const share = (client: string) => shares[client]?.accounts.find((a) => a.account_id === u.id)
              const shared = u.rows.some((r) => share(r.client_id) !== undefined)
              return (
                <>
                  <TokenSplit of={u} />
                  <ClientTable
                    rows={u.rows}
                    detail={false}
                    provider={false}
                    shares={shared ? accountShareColumns(share) : []}
                  />
                  {shared && (
                    <div className="mt-2">
                      <ShareNote />
                    </div>
                  )}
                </>
              )
            }}
          />
        )
      }}
    </GatewayUsage>
  )
}

/** An account as its list row names it: its name linked to its page, then its address, provider and plan. */
function AccountName({ use: u }: { use: AccountUse }) {
  const a = u.account
  if (a === undefined) return <span className="font-medium">{u.name}</span>
  const detail = [isNamed(a) ? a.email : '', providerLabel(a.provider), planLabel(a.plan)].filter((s) => s !== '')
  return (
    <>
      <AppLink href={accountHref(a.id)} strong>
        {u.name}
      </AppLink>
      <div className="truncate text-xs text-on-surface-variant">{detail.join(' · ')}</div>
    </>
  )
}
