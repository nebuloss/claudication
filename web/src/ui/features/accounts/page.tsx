import { useState, type ReactNode } from 'react'
import AccountSettings, { type AccountSettingsSection } from './settings'
import { ChatsPage } from '../chats'
import { DetailHeader } from '../detail'
import QuotaMeter, { QuotaGauges } from './quota-meter'
import RequestsPanel from '../requests'
import ScopedUsage from '../usage'
import { api, type Account } from '../../../api/client'
import { useLoader } from '../../hooks'
import { navigate, pathOf, type AccountPageId } from '../../route'
import { providerLabel } from '../../providers'
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
} from '../../primitives'
import { accountStatus } from '../../domain/account'
import { compact, dateTime, relative } from '../../format'

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
  page: AccountPageId
}) {
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
  const settings = (section: AccountSettingsSection) => () => (
    <AccountSettings
      key={section}
      account={account}
      section={section}
      onChanged={() => void reload()}
      onRemoved={() => navigate('/accounts')}
    />
  )
  // One entry per page the registry names, so a page added to ACCOUNT_PAGES
  // without a view here is a compile error rather than a blank screen.
  const pages: Record<AccountPageId, () => ReactNode> = {
    '': () => (
      <AccountOverview
        account={account}
        rank={rank}
        total={accounts.length}
        windowDays={data?.windowDays ?? 7}
        onChanged={() => void reload()}
      />
    ),
    usage: () => <ScopedUsage scope={{ account: account.id }} breakdown="key" />,
    chats: () => <ChatsPage scope={{ account: account.id }} />,
    requests: () => <RequestsPanel scope={{ account: account.id }} />,
    models: settings('models'),
    connection: settings('connection'),
    advanced: settings('advanced'),
  }

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

      {pages[page]()}
    </Page>
  )
}

/**
 * An account at a glance, on its own path: its figures, the subscription's
 * windows as Anthropic reports them, and the facts about it.
 */
function AccountOverview({
  account,
  rank,
  total,
  windowDays,
  onChanged,
}: {
  account: Account
  rank: number
  total: number
  windowDays: number
  onChanged: () => void
}) {
  const [reading, setReading] = useState(false)
  const reload = onChanged
  return (
    <>
      <StatGrid>
        <Stat label="Requests" value={compact(account.requests)} hint={`over ${windowDays}d`} />
        <Stat label="Tokens" value={compact(account.tokens)} hint={`over ${windowDays}d`} />
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
            ['Priority', `${rank + 1} of ${total}`],
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
  )
}
