import AccountCard, { status } from './account-card'
import { Window } from './accounts'
import { DetailHeader, detailSection } from './detail'
import QuotaMeter from './quota-meter'
import ScopedUsage, { accountHref } from './usage'
import { api } from '../../api/client'
import { useLoader } from '../hooks'
import { navigate } from '../route'
import { Banner, Card, CardTitle, Chip, Empty, Spinner } from '../primitives'

/** As on the list: the server says how often it re-reads the quota. */
const REFRESH_MS = 20_000

/**
 * One Claude account, on two pages.
 *
 * /accounts/<id> is what it is doing: the subscription's own windows as
 * Anthropic reports them, and below them what this gateway sent it — totals,
 * traffic over time, which models and keys, the chats. /accounts/<id>/settings
 * is the card with everything that changes it: its token, its models, testing,
 * reconnecting, pausing, removing.
 *
 * Read from the account list rather than an endpoint of its own: the list is
 * a handful of rows and already carries everything shown here, and one source
 * means the page and the list cannot disagree about an account.
 */
export default function AccountPage({
  id,
  section: path,
  onExpired,
}: {
  id: string
  section: string
  onExpired: () => void
}) {
  const section = detailSection(path)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    () => api.listAccounts(),
    onExpired,
    [],
    REFRESH_MS,
  )

  const accounts = data?.accounts ?? []
  const rank = accounts.findIndex((a) => a.id === id)
  const account = rank >= 0 ? accounts[rank] : undefined

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (account === undefined) {
    return (
      <div className="flex flex-col gap-5">
        {error !== '' && <Banner tone="error">{error}</Banner>}
        <Empty>
          No such account — it may have been removed. Its past usage is still in the request log
          under the email it was recorded with.
        </Empty>
      </div>
    )
  }

  const chip = status(account)

  return (
    <div className="flex flex-col gap-5">
      <DetailHeader
        back="/accounts"
        backLabel="All accounts"
        title={account.email}
        subtitle={`Priority ${rank + 1} of ${accounts.length} · ${account.provider}`}
        aside={
          <>
            {account.serving && <Chip tone="ok">serving</Chip>}
            <Chip tone={chip.tone}>{chip.label}</Chip>
            {account.quota !== undefined && (
              <span className="flex items-center gap-3 text-xs tabular-nums text-on-surface">
                <Window label="5h" util={account.quota.five_hour_util} />
                <Window label="7d" util={account.quota.seven_day_util} />
              </span>
            )}
          </>
        }
        base={accountHref(account.id)}
        section={section}
        freshness={{ updatedAt, refreshing, live, onRefresh: () => void reload() }}
      />

      {error !== '' && <Banner tone="error">{error}</Banner>}

      {section === 'settings' ? (
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
      ) : (
        <>
          <Card>
            <CardTitle>Subscription</CardTitle>
            <QuotaMeter quota={account.quota} pending={false} />
            <p className="mt-3 mb-0 text-xs text-on-surface-variant">
              As Anthropic reports it, counting every client on this account — not only this
              gateway.
            </p>
          </Card>
          <ScopedUsage scope={{ account: account.id }} breakdown="key" onExpired={onExpired} />
        </>
      )}
    </div>
  )
}
