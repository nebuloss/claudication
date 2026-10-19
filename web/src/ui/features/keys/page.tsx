import { useState, type ReactNode } from 'react'
import {
  api,
  type ApiKey,
  type GatewayConfig,
  type KeyLimits,
} from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import { KEY_PAGES, isSettingsPage, navigate, pathOf, type KeyPageId } from '../../route'
import ScopedUsage from '../usage'
import { DetailHeader } from '../detail'
import { ChatsPage } from '../chats'
import RequestsPanel from '../requests'
import KeyClients, { KeySecret } from './clients'
import { KeyModels } from '../models/switches'
import { RolesEditor } from '../models/roles'
import {
  AppLink,
  Button,
  Card,
  DangerZone,
  Empty,
  ErrorBanner,
  Figures,
  Loading,
  Muted,
  Page,
  Stat,
  StatGrid,
  useConfirm,
} from '../../primitives'
import { ago, compact } from '../../format'
import { KeyChips, KeyFigures, MANAGED_HINT, REFRESH_MS, SubscriptionCard } from './parts'
import { KeyEditor, periodName } from './limits'

/**
 * One key, on two pages.
 *
 * /keys/<id> is what it spent: its share of the subscription, and the usage
 * report narrowed to it — totals, traffic over time, which models and
 * accounts, the chats. /keys/<id>/settings is changing it: its name and
 * limits, and deleting it.
 *
 * Read from the key list rather than an endpoint of its own, for the reason
 * the account page is: one source, so the page and the list cannot disagree.
 */
export function KeyPage({
  id,
  page,
  config,
}: {
  id: string
  page: KeyPageId
  /** The gateway's configuration, for the address and APIs its client files name. */
  config: GatewayConfig | null
}) {
  // Not on the settings page: the form is filled from the loaded key, and a
  // poll landing mid-edit would reset the fields under the cursor.
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.keys.list(signal),
    { refreshMs: isSettingsPage(KEY_PAGES, page) ? 0 : REFRESH_MS },
  )
  const k = data?.keys.find((key) => key.id === id)

  if (loading && data === null) return <Loading />
  if (k === undefined) {
    return (
      <Page>
        <ErrorBanner error={error} />
        <Empty>
          No such key — it may have been deleted. What it spent is still in the request log, under
          the name it was recorded with.
        </Empty>
      </Page>
    )
  }

  const settings = (section: KeySettingsSection) => () => (
    <KeySettings
      key={section}
      apiKey={k}
      section={section}
      defaultRpm={data?.defaultRpm ?? 0}
      config={config}
      onSaved={() => void reload()}
    />
  )
  // One entry per page the registry names, so a page added to KEY_PAGES
  // without a view here is a compile error rather than a blank screen.
  const pages: Record<KeyPageId, () => ReactNode> = {
    '': () => <KeyOverview k={k} windowDays={data?.windowDays ?? 7} onChanged={() => void reload()} />,
    usage: () => <ScopedUsage scope={{ key: k.id }} breakdown="account" />,
    chats: () => <ChatsPage scope={{ key: k.id }} />,
    requests: () => <RequestsPanel scope={{ key: k.id }} />,
    clients: settings('clients'),
    models: settings('models'),
    general: settings('general'),
    advanced: settings('advanced'),
  }

  return (
    <Page>
      <DetailHeader
        kind="keys"
        id={k.id}
        title={k.name}
        aside={
          <Figures>
            <KeyChips k={k} />
            <KeyFigures k={k} always={false} />
          </Figures>
        }
        freshness={{ updatedAt, refreshing, live, onRefresh: () => void reload() }}
      />

      <ErrorBanner error={error} />

      {pages[page]()}
    </Page>
  )
}

/** Which of the key's settings pages this is. */
type KeySettingsSection = 'clients' | 'models' | 'general' | 'advanced'

/**
 * A key's settings, a page each (/keys/<id>/clients, /models, /general, /advanced): the
 * models it may ask for, what its aliases serve, its name and limits, its
 * client configs, and deleting it on a page of its own, where it cannot be
 * hit while saving a limit.
 */
export function KeySettings({
  apiKey: k,
  section,
  defaultRpm,
  config,
  onSaved,
}: {
  apiKey: ApiKey
  section: KeySettingsSection
  defaultRpm: number
  config: GatewayConfig | null
  onSaved: () => void
}) {
  const { pending: busy, error, run } = useAction()
  const confirm = useConfirm()
  const [saved, setSaved] = useState(false)

  if (k.managed) {
    return (
      <Card>
        <Muted>{MANAGED_HINT}</Muted>
      </Card>
    )
  }

  const save = async (name: string, limits: KeyLimits) => {
    setSaved(false)
    await run('save', async () => {
      await api.keys.update(k.id, name, limits)
      setSaved(true)
      onSaved()
    })
  }

  const remove = () =>
    confirm({
      title: `Delete ${k.name}?`,
      body: 'Clients using it stop working immediately.',
      confirmLabel: 'Delete key',
      tone: 'danger',
      action: async () => {
        await api.keys.remove(k.id)
        navigate('/keys')
      },
    })

  const card = (() => {
    switch (section) {
      // Which models the key may ask for, and what its aliases serve: one
      // question, so one page, a card for each half.
      case 'models':
        return (
          <>
            <Card title="Allowed models">
              <KeyModels apiKey={k} onChanged={onSaved} />
            </Card>
            <Card title="Aliases">
              <RolesEditor keyId={k.id} />
            </Card>
          </>
        )
      case 'general':
        return (
          <Card>
            <KeyEditor apiKey={k} defaultRpm={defaultRpm} busy={busy} onSave={save} />
            {saved && error === '' && (
              <div className="mt-3">
                <Muted>Saved.</Muted>
              </div>
            )}
          </Card>
        )
      // The secret is kept sealed beside the account tokens (migration 026),
      // so the configs can be written again at any time. Keys from before
      // that have only a hash, and need a new secret once.
      case 'clients':
        return <KeyClients apiKey={k} config={config} />
      case 'advanced':
        return (
          <DangerZone
            title="Delete this key"
            action={
              <Button variant="outlined" tone="error" disabled={busy} onClick={() => void remove()}>
                Delete {k.name}
              </Button>
            }
          >
            Clients holding it stop working immediately, and it cannot be undone. Its usage stays,
            under the name it was recorded with.
          </DangerZone>
        )
    }
  })()

  return (
    <>
      {card}
      <ErrorBanner error={error} />
    </>
  )
}

/**
 * A key at a glance, on its own path: its secret, every figure a tile — what
 * it did, what it may do, and when it was last used — and its share of the
 * subscription. The rest is a page away.
 */
function KeyOverview({ k, windowDays, onChanged }: { k: ApiKey; windowDays: number; onChanged: () => void }) {
  return (
    <>
      {!k.managed && <KeySecret apiKey={k} onChanged={onChanged} />}
      {/* The key at a glance, every figure a tile: what it did, what it
          may do, and when it was last used. The rest is a page away. */}
      <StatGrid columns={3}>
        <Stat label="Requests" value={compact(k.requests)} hint={`over ${windowDays}d`} />
        <Stat label="Tokens" value={compact(k.tokens)} hint={`over ${windowDays}d`} />
        <Stat
          label="Today"
          value={compact(k.spent_today)}
          hint={k.token_budget > 0 ? `of ${compact(k.token_budget)} tokens` : 'tokens, no daily cap'}
          tone={k.token_budget > 0 && k.spent_today >= k.token_budget ? 'error' : undefined}
        />
        <Stat
          label="Rate"
          value={k.rpm_limit > 0 ? compact(k.rpm_limit) : 'default'}
          hint={k.rpm_limit > 0 ? `requests a ${periodName(k.rate_period_s)}` : 'the gateway’s limit'}
        />
        <Stat label="Last used" value={ago(k.last_used_at)} hint={`created ${ago(k.created_at)}`} />
        <Stat
          label="Models"
          value={k.models_off.length === 0 ? 'all' : `${k.models_off.length} off`}
          hint={
            <AppLink href={`${pathOf('keys', k.id)}/models`}>
              {k.models_off.length === 0 ? 'every model allowed' : 'see which'}
            </AppLink>
          }
          tone={k.models_off.length > 0 ? 'error' : undefined}
        />
      </StatGrid>
      {k.subscription !== undefined && <SubscriptionCard share={k.subscription} />}
    </>
  )
}
