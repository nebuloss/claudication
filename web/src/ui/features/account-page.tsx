import { useState } from 'react'
import AccountSettings, { status } from './account-card'
import { Window } from './accounts'
import { ChatsPage } from './chats'
import { DetailHeader, ItemSettings } from './detail'
import QuotaMeter from './quota-meter'
import RequestsPanel from './requests'
import ScopedUsage from './usage'
import { api } from '../../api/client'
import { useLoader } from '../hooks'
import { ACCOUNT_SETTINGS, navigate, pathOf } from '../route'
import { Banner, Card, CardTitle, Chip, Empty, SmallButton, Spinner } from '../primitives'

/** As on the list: the server says how often it re-reads the quota. */
const REFRESH_MS = 20_000

/**
 * One Claude account, on its pages:
 *
 *   /accounts/<id>/usage     the subscription's own windows as Anthropic
 *                            reports them, and what this gateway sent it
 *   /accounts/<id>/chats     the conversations it served
 *   /accounts/<id>/requests  the request log, narrowed to it
 *   /accounts/<id>/settings/<section>  a page per setting
 *
 * Read from the account list rather than an endpoint of its own: the list is
 * a handful of rows and already carries everything shown here, and one source
 * means the page and the list cannot disagree about an account.
 */
export default function AccountPage({
  id,
  page,
  section,
  onExpired,
}: {
  id: string
  /** An id from ITEM_PAGES, and under settings an id from ACCOUNT_SETTINGS. */
  page: string
  section: string
  onExpired: () => void
}) {
  const base = pathOf('accounts', id)
  const sectionLabel = ACCOUNT_SETTINGS.find((x) => x.id === section)?.label
  const [reading, setReading] = useState(false)
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
        trail={[{ label: 'Claude accounts', href: '/accounts' }]}
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
        base={base}
        page={page}
        section={page === 'settings' ? sectionLabel : undefined}
        freshness={{ updatedAt, refreshing, live, onRefresh: () => void reload() }}
      />

      {error !== '' && <Banner tone="error">{error}</Banner>}

      {page === 'settings' && (
        <ItemSettings base={base} sections={ACCOUNT_SETTINGS} current={section} danger="remove">
          <AccountSettings
            key={section}
            account={account}
            section={section}
            onChanged={() => void reload()}
            onRemoved={() => navigate('/accounts')}
          />
        </ItemSettings>
      )}
      {page === 'chats' && <ChatsPage scope={{ account: account.id }} onExpired={onExpired} />}
      {page === 'requests' && <RequestsPanel scope={{ account: account.id }} onExpired={onExpired} />}
      {page === 'usage' && (
        <>
          <Card>
            <CardTitle
              aside={
                <SmallButton
                  disabled={reading}
                  onClick={() => {
                    setReading(true)
                    void api
                      .refreshUsage(account.id)
                      .catch(() => {})
                      .finally(() => {
                        setReading(false)
                        void reload()
                      })
                  }}
                >
                  {reading && <Spinner className="size-3" />}
                  {reading ? 'Reading…' : 'Read again'}
                </SmallButton>
              }
            >
              Subscription
            </CardTitle>
            <QuotaMeter quota={account.quota} pending={reading} />
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
