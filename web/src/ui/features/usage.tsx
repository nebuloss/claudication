import { useState } from 'react'
import { api, type Usage, type UsageBucket, type UsageScope } from '../../api/client'
import { RankedBars } from '../charts'
import ChatsPanel from './chats'
import { Traffic } from './traffic'
import { useHashPanel, useLoader } from '../hooks'
import { navigate } from '../route'
import { requestsHref } from './requests'
import {
  AppLink,
  ErrorState,
  Card,
  CardTitle,
  Empty,
  Freshness,
  Spinner,
  Segmented,
  Stat,
  SubNav,
  compact,
} from '../primitives'

const WINDOWS = [1, 7, 30] as const

const REPORT_REFRESH_MS = 30_000

// The hash values, and the order the sub-nav lists them. A URL surface —
// /accounts/<id>#chats is a link someone can send — so renaming one breaks a
// bookmark.
const VIEWS = ['day', 'model', 'account', 'key', 'chats'] as const

export type UsageView = (typeof VIEWS)[number]

const VIEW_LABELS: Record<UsageView, string> = {
  day: 'Over time',
  model: 'Model',
  account: 'Account',
  key: 'Key',
  chats: 'Chats',
}

/** Where an account's or a key's own page lives. */
export const accountHref = (id: string) => `/accounts/${encodeURIComponent(id)}`
export const keyHref = (id: string) => `/keys/${encodeURIComponent(id)}`

/** A breakdown row that opens the page of what it counts, when it has one. */
function linked(b: UsageBucket, href: ((id: string) => string) | undefined) {
  if (href === undefined || b.id === undefined || b.id === '') return {}
  const to = href(b.id)
  return { href: to, onOpen: () => navigate(to) }
}

/**
 * What a scope spent: the whole gateway, one account, or one key.
 *
 * One component for all three, so an account's page and the Overview read the
 * same way and a number means the same thing on both. `views` picks the
 * breakdowns worth offering there — on an account's page "by account" is one
 * row, so it offers "by key" instead, and the other way round on a key's.
 */
export default function ScopedUsage({
  scope = {},
  views: offered,
  onExpired,
  title = 'Usage',
}: {
  scope?: UsageScope
  views: readonly UsageView[]
  onExpired: () => void
  title?: string
}) {
  const [days, setDays] = useState<number>(7)
  // In the hash, so a page opens straight onto a panel.
  const [chosen, setChosen] = useHashPanel<UsageView>(VIEWS, offered[0] ?? 'day')
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader<Usage>(
    () => api.usage(days, scope),
    onExpired,
    [days, scope.account, scope.key],
    REPORT_REFRESH_MS,
  )

  if (loading && data === null) {
    return (
      <Card>
        <CardTitle>{title}</CardTitle>
        <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
          <Spinner /> Loading…
        </p>
      </Card>
    )
  }
  if (error !== '') {
    return <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
  }
  if (data !== null && !data.enabled) {
    return (
      <Card>
        <CardTitle>{title}</CardTitle>
        <Empty>
          Per-request history is turned off. Set <code>usage.retention-days</code> above 0 to record
          it.
        </Empty>
      </Card>
    )
  }

  const report = data?.report
  const totals = report?.totals

  // Only the views with something in them, so a quiet window does not offer
  // empty tabs. Chats is offered whenever anything was proxied: it fetches its
  // own list and says "no conversations" for itself.
  const views = offered.filter((v) => {
    if (report === undefined) return false
    switch (v) {
      case 'chats':
        return report.totals.requests > 0
      case 'day':
        return report.by_day.length > 0
      case 'model':
        return report.by_model.length > 0
      case 'account':
        return report.by_account.length > 0
      case 'key':
        return report.by_key.length > 0
    }
  })
  // Narrowing the window can take the chosen tab away with it; fall back
  // rather than render a panel that is no longer on the bar.
  const view = views.includes(chosen) ? chosen : views[0]

  // The request log, narrowed to the same scope.
  const logHref = requestsHref({
    ...(scope.account ? { account: [scope.account] } : {}),
    ...(scope.key ? { key: [scope.key] } : {}),
  })

  return (
    <Card>
      <CardTitle
        aside={
          <div className="flex items-center gap-2">
            <Segmented
              label="Reporting window"
              value={String(days)}
              onChange={(v) => setDays(Number(v))}
              options={WINDOWS.map((d) => ({
                id: String(d),
                label: `${d} days`,
                content: `${d}d`,
              }))}
            />
            <Freshness
              updatedAt={updatedAt}
              refreshing={refreshing}
              live={live}
              onRefresh={() => void reload()}
            />
          </div>
        }
      >
        {title}
      </CardTitle>

      {totals === undefined || totals.requests === 0 ? (
        <Empty>Nothing proxied in this window.</Empty>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Stat
              label="Requests"
              value={compact(totals.requests)}
              hint={`${totals.errors} failed`}
              tone={totals.errors > 0 ? 'error' : undefined}
            />
            <Stat label="Input" value={compact(totals.input_tokens)} hint="tokens" />
            <Stat label="Output" value={compact(totals.output_tokens)} hint="tokens" />
            <Stat
              label="Latency"
              value={`${totals.median_ms}ms`}
              hint={`p95 ${totals.p95_ms}ms`}
            />
          </div>
          <p className="mt-3 mb-0 text-xs text-on-surface-variant">
            Cache: {compact(totals.cache_read_tokens)} read,{' '}
            {compact(totals.cache_write_tokens)} written. Kept for {data?.retention_days ?? 0}{' '}
            days. <AppLink href={logHref}>Open in the request log</AppLink>
          </p>
        </>
      )}

      {view !== undefined && report !== undefined && (
        <div className="mt-5 border-t border-outline-variant pt-4">
          <SubNav
            label="Usage view"
            value={view}
            onChange={setChosen}
            options={views.map((id) => ({ id, label: VIEW_LABELS[id] }))}
          />

          <div className="mt-4">
            {view === 'chats' && <ChatsPanel days={days} scope={scope} onExpired={onExpired} />}

            {view === 'day' && <Traffic cross={report.cross} days={days} />}

            {view === 'model' && (
              <RankedBars
                scale="log"
                format={compact}
                rows={report.by_model.map((b) => ({
                  label: b.label,
                  value: b.input_tokens + b.output_tokens + b.cache_tokens,
                  right: `${compact(b.input_tokens + b.output_tokens + b.cache_tokens)} tok`,
                }))}
              />
            )}

            {view === 'account' && (
              <>
                <RankedBars
                  scale="log"
                  format={compact}
                  rows={report.by_account.map((b) => ({
                    label: b.label,
                    value: b.input_tokens + b.output_tokens + b.cache_tokens,
                    right: `${compact(b.requests)} req`,
                    ...linked(b, accountHref),
                  }))}
                />
                <p className="mt-3 mb-0 text-xs text-on-surface-variant">
                  What this gateway sent to each account. The subscription&rsquo;s own 5-hour and
                  7-day utilisation is on each account&rsquo;s page — it counts every client, not
                  only this one.
                </p>
              </>
            )}

            {view === 'key' && (
              <RankedBars
                scale="log"
                format={compact}
                rows={report.by_key.map((b) => ({
                  label: b.label,
                  value: b.requests,
                  right: `${compact(b.requests)} req`,
                  ...linked(b, keyHref),
                }))}
              />
            )}
          </div>
        </div>
      )}
    </Card>
  )
}
