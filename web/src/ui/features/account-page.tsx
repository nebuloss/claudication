import { useState } from 'react'
import AccountSettings from './account-card'
import { ChatsPage } from './chats'
import { DetailHeader } from './detail'
import QuotaMeter, { QuotaGauges } from './quota-meter'
import RequestsPanel from './requests'
import ScopedUsage from './usage'
import { api } from '../../api/client'
import { useLoader } from '../hooks'
import { navigate, pathOf } from '../route'
import { providerLabel } from '../providers'
import {
  AppLink,
  Button,
  Card,
  Chip,
  Empty,
  ErrorBanner,
  Figures,
  KeyValue,
  Loading,
  Page,
  Stat,
  StatGrid,
} from '../primitives'
import { accountStatus } from '../domain/account'
import { compact, dateTime, relative } from '../format'

/** As on the list: the server says how often it re-reads the quota. */
const REFRESH_MS = 20_000

/**
 * One Claude account, on its pages:
 *
 *   /accounts/<id>/usage     the subscription's own windows as Anthropic
 *                            reports them, and what this gateway sent it
 *   /accounts/<id>/chats     the conversations it served
 *   /accounts/<id>/requests  the request log, narrowed to it
 *   /accounts/<id>/models, /connection, /advanced  its settings, a page each
 *
 * Read from the account list rather than an endpoint of its own: the list is
 * a handful of rows and already carries everything shown here, and one source
 * means the page and the list cannot disagree about an account.
 */
export default function AccountPage({
  id,
  page,
}: {
  id: string
  /** An id from ACCOUNT_PAGES, or empty for the account's overview. */
  page: string
}) {
  const [reading, setReading] = useState(false)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.accounts.list(signal),
    { refreshMs: REFRESH_MS },
  )

  const accounts = data?.accounts ?? []
  const rank = accounts.findIndex((a) => a.id === id)
  const account = rank >= 0 ? accounts[rank] : undefined

  if (loading && data === null) return <Loading />
  if (account === undefined) {
    return (
      <Page>
        <ErrorBanner error={error} />
        <Empty>
          No such account — it may have been removed. Its past usage is still in the request log
          under the email it was recorded with.
        </Empty>
      </Page>
    )
  }

  const chip = accountStatus(account)

  return (
    <Page>
      <DetailHeader
        kind="accounts"
        id={account.id}
        title={account.email}
        subtitle={`Priority ${rank + 1} of ${accounts.length} · ${account.provider}`}
        aside={
          <>
            {account.serving && <Chip tone="ok">serving</Chip>}
            <Chip tone={chip.tone}>{chip.label}</Chip>
            {account.quota !== undefined && (
              <Figures>
                <QuotaGauges quota={account.quota} />
              </Figures>
            )}
          </>
        }
        freshness={{ updatedAt, refreshing, live, onRefresh: () => void reload() }}
      />

      <ErrorBanner error={error} />

      {(page === 'models' || page === 'connection' || page === 'advanced') && (
        <AccountSettings
          key={page}
          account={account}
          section={page}
          onChanged={() => void reload()}
          onRemoved={() => navigate('/accounts')}
        />
      )}
      {page === 'chats' && <ChatsPage scope={{ account: account.id }} />}
      {page === 'requests' && <RequestsPanel scope={{ account: account.id }} />}
      {page === '' && (
        <>
          <StatGrid>
            <Stat label="Requests" value={compact(account.requests)} hint={`over ${data?.windowDays ?? 7}d`} />
            <Stat label="Tokens" value={compact(account.tokens)} hint={`over ${data?.windowDays ?? 7}d`} />
            <Stat
              label="5-hour window"
              value={account.quota !== undefined ? `${Math.round(account.quota.five_hour_util)}%` : '—'}
              hint="used"
            />
            <Stat
              label="7-day window"
              value={account.quota !== undefined ? `${Math.round(account.quota.seven_day_util)}%` : '—'}
              hint="used"
            />
          </StatGrid>
          <Card
            title="Subscription"
            info={
              <p>
                As Anthropic reports it, counting every client on this account — not only this
                gateway.
              </p>
            }
            aside={
              <Button
                variant="outlined"
                size="sm"
                busy={reading}
                onClick={() => {
                    setReading(true)
                    void api
                      .accounts.refreshUsage(account.id)
                      .catch(() => {})
                      .finally(() => {
                        setReading(false)
                        void reload()
                      })
                }}
              >
                {reading ? 'Reading…' : 'Read again'}
              </Button>
            }
          >
            <QuotaMeter quota={account.quota} pending={reading} />
          </Card>
          <Card>
            <KeyValue
              items={[
                ['Provider', providerLabel(account.provider)],
                ['Priority', `${rank + 1} of ${accounts.length}`],
                ['Serving', account.disabled ? 'paused' : account.serving ? 'yes, now' : 'when its turn comes'],
                [
                  'Models',
                  (account.models_off ?? []).length === 0 ? (
                    'all on'
                  ) : (
                    <AppLink href={`${pathOf('accounts', account.id)}/models`}>
                      {(account.models_off ?? []).length} switched off
                    </AppLink>
                  ),
                ],
                ['Token expires', relative(account.expires_at)],
                ['Last used', dateTime(account.last_used_at)],
                ['Connected', dateTime(account.created_at)],
              ]}
            />
          </Card>
        </>
      )}
      {page === 'usage' && <ScopedUsage scope={{ account: account.id }} breakdown="key" />}
    </Page>
  )
}
