import AccountCard from './account-card'
import ScopedUsage from './usage'
import { api } from '../../api/client'
import { useLoader } from '../hooks'
import { navigate } from '../route'
import { AppLink, Banner, Empty, Freshness, Spinner } from '../primitives'

/** As on the list: the server says how often it re-reads the quota. */
const REFRESH_MS = 20_000

/**
 * One Claude account: its credentials and subscription on top, and below them
 * what this gateway sent it — the usage report narrowed to this account,
 * broken down by the keys that paid for it.
 *
 * Read from the account list rather than an endpoint of its own: the list is
 * a handful of rows and already carries everything the card shows, and one
 * source means the page and the list cannot disagree about an account.
 */
export default function AccountPage({ id, onExpired }: { id: string; onExpired: () => void }) {
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    () => api.listAccounts(),
    onExpired,
    [],
    REFRESH_MS,
  )

  const accounts = data?.accounts ?? []
  const rank = accounts.findIndex((a) => a.id === id)
  const account = rank >= 0 ? accounts[rank] : undefined

  return (
    <div className="flex flex-col gap-5">
      <div className="flex items-center justify-between gap-3">
        <AppLink href="/accounts" className="text-sm text-on-surface-variant">
          ‹ All accounts
        </AppLink>
        <Freshness
          updatedAt={updatedAt}
          refreshing={refreshing}
          live={live}
          onRefresh={() => void reload()}
        />
      </div>

      {error !== '' && <Banner tone="error">{error}</Banner>}

      {loading && data === null ? (
        <p className="flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      ) : account === undefined ? (
        <Empty>
          No such account — it may have been removed. Its past usage is still in the request log
          under the email it was recorded with.
        </Empty>
      ) : (
        <>
          <AccountCard
            account={account}
            rank={rank}
            total={accounts.length}
            windowDays={data?.windowDays ?? 7}
            orderable={false}
            busy={false}
            onMoveUp={() => {}}
            onMoveDown={() => {}}
            onChanged={() => void reload()}
            onRemoved={() => navigate('/accounts')}
          />
          <ScopedUsage
            title={`Usage of ${account.email}`}
            scope={{ account: account.id }}
            breakdown="key"
            onExpired={onExpired}
          />
        </>
      )}
    </div>
  )
}
