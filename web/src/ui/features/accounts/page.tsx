import type { ReactNode } from 'react'
import AccountSettings, { type AccountSettingsSection } from './settings'
import { DetailHeader } from '../detail'
import QuotaMeter, { QuotaGauges, ReadAgain, headline, useQuotaRead } from './quota-meter'
import RequestsPanel from '../requests'
import { AccountUsage } from './usage'
import { AccountSwitch } from './index'
import { api, type Account } from '../../../api/client'
import { useLoader } from '../../hooks'
import { navigate, pathOf, type AccountPageId } from '../../route'
import { planLabel, providerLabel, providerOf } from '../../providers'
import {
  AppLink,
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
import { accountName, accountStatus, isNamed } from '../../domain/account'
import { compact, dateTime, relative } from '../../format'
import { windowTitle } from '../../domain/quota'

/** As on the list: the server says how often it re-reads the quota. */
const REFRESH_MS = 20_000

/**
 * One account, Claude or ChatGPT, on its pages:
 *
 *   /accounts/<id>/usage     its windows and state now, then which clients
 *                            drew on it and each one's share of its windows
 *   /accounts/<id>/requests  the request log, narrowed to it: errors,
 *                            cooldowns and refusals on this account
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

  const all = data?.accounts ?? []
  const account = all.find((a) => a.id === id)
  // Priority is within the account's own provider: a Claude request is
  // served by the first Claude account with room, whatever the ChatGPT ones
  // are doing, and the list orders each provider apart.
  const accounts = all.filter((a) => a.provider === account?.provider)
  const rank = accounts.findIndex((a) => a.id === id)

  if (loading && data === null) return <Loading />
  if (account === undefined) {
    return (
      <Page>
        <ErrorBanner error={error} />
        <Empty>
          No such account — it may have been removed. Its past usage is still in the request log
          under the name it was recorded with.
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
    usage: () => <AccountUsage account={account} onChanged={() => void reload()} />,
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
        title={accountName(account)}
        rename={{
          value: account.name ?? '',
          // An unnamed account's field is empty with its address in grey:
          // what it is called until it is named, and what Enter on an empty
          // field keeps.
          placeholder: account.email,
          label: 'Account name',
          clearLabel: 'Clear the name — call it by its address',
          onSave: async (name) => {
            await api.accounts.setName(account.id, name)
            void reload()
          },
        }}
        subtitle={[
          // The address under a name, as the one thing that tells two
          // accounts named alike apart, and the account the provider knows.
          isNamed(account) ? account.email : '',
          `Priority ${rank + 1} of ${accounts.length}`,
          providerLabel(account.provider),
          planLabel(account.plan),
        ]
          .filter((part) => part !== '')
          .join(' · ')}
        aside={
          <>
            {account.serving && <Chip tone="ok">serving</Chip>}
            <Chip tone={chip.tone}>{chip.label}</Chip>
            {account.quota !== undefined && (
              <Figures>
                <QuotaGauges quota={account.quota} />
              </Figures>
            )}
            <AccountSwitch key={String(account.disabled)} account={account} onChanged={() => void reload()} />
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
 * windows as its provider reports them, and the facts about it.
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
  const { reading, read } = useQuotaRead(account.id, onChanged)
  return (
    <>
      <StatGrid>
        <Stat label="Requests" value={compact(account.requests)} hint={`over ${windowDays}d`} />
        <Stat label="Tokens" value={compact(account.tokens)} hint={`over ${windowDays}d`} />
        {account.quota !== undefined ? (
          headline(account.quota).map((h) => (
            <Stat
              key={h.seconds}
              label={windowTitle(h.seconds)}
              value={h.window.percent === null ? '—' : h.window.label}
              hint="used"
            />
          ))
        ) : (
          <Stat label="Windows" value="—" hint="not read yet" />
        )}
      </StatGrid>
      <Card
        title="Subscription"
        info={
          <p>
            As {providerOf(account.provider)?.usage.source ?? account.provider} reports it, counting
            every client on this account — not only this gateway.
          </p>
        }
        aside={<ReadAgain reading={reading} onRead={read} />}
      >
        <QuotaMeter quota={account.quota} pending={reading} provider={account.provider} />
      </Card>
      <Card>
        <KeyValue
          items={[
            // The sign-in's own, whatever the account is called here.
            ['Address', account.email],
            ['Provider', providerLabel(account.provider)],
            ...(account.plan !== undefined && account.plan !== ''
              ? ([['Plan', planLabel(account.plan)]] as [string, string][])
              : []),
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
