import { Fragment, useState } from 'react'
import { api, type Account, type AccountShare, type SlotUse } from '../../../api/client'
import { useLoader } from '../../hooks'
import { clientHref } from '../../route'
import {
  AppLink,
  Card,
  Empty,
  ErrorBanner,
  InlineMeter,
  Loading,
  Muted,
  RowToggle,
  Stat,
  StatGrid,
  Table,
  Td,
  Tr,
  TrDetail,
  WindowPicker,
  type Column,
} from '../../primitives'
import { accountStatus } from '../../domain/account'
import { ago, compact, lastDays, smallPercent } from '../../format'
import { SlotTable } from '../clients/usage'
import { useClientTypes } from '../clients/types'
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

/** One client's traffic on this account, its rows by name and model added up. */
type ClientUse = {
  id: string
  name: string
  type: string
  requests: number
  errors: number
  input: number
  output: number
  cache: number
  reasoning: number
  last: string
  share: AccountShare | undefined
  rows: SlotUse[]
}

/** The rows by client, most of this account's windows first. */
function byClient(rows: SlotUse[], shares: Record<string, AccountShare>): ClientUse[] {
  const out = new Map<string, ClientUse>()
  for (const r of rows) {
    let c = out.get(r.client_id)
    if (c === undefined) {
      c = {
        id: r.client_id,
        name: r.client || r.client_id,
        type: r.type,
        requests: 0,
        errors: 0,
        input: 0,
        output: 0,
        cache: 0,
        reasoning: 0,
        last: '',
        share: shares[r.client_id],
        rows: [],
      }
      out.set(r.client_id, c)
    }
    c.requests += r.requests
    c.errors += r.errors
    c.input += r.input_tokens
    c.output += r.output_tokens
    c.cache += r.cache_tokens
    c.reasoning += r.reasoning_tokens
    if (r.last > c.last) c.last = r.last
    c.rows.push(r)
  }
  // By the share of the windows, the 7-day one first: that is the question
  // the page is opened to answer, and it is the window that runs out over a
  // week. Tokens break the ties, which with no shares at all (usage history
  // off, or a quota never read) is every row.
  return [...out.values()].sort(
    (x, y) =>
      (y.share?.seven_day ?? 0) - (x.share?.seven_day ?? 0) ||
      (y.share?.five_hour ?? 0) - (x.share?.five_hour ?? 0) ||
      tokens(y) - tokens(x),
  )
}

/** Every token a client sent and got, as the account list counts them; reasoning is inside output. */
function tokens(c: ClientUse): number {
  return c.input + c.output + c.cache
}

/**
 * Which clients drew on the account over a window, a row each: its share of
 * the account's 5-hour and 7-day windows, what it sent, when it last did, and
 * — unfolded — the names and models it used here with every token kind.
 *
 * The same rows as a client's own Usage page, read for one account, so the
 * two add up.
 */
function AccountClients({ accountId }: { accountId: string }) {
  const [days, setDays] = useState<number>(7)
  const [open, setOpen] = useState<string | null>(null)
  const { types } = useClientTypes()
  const { data, error, loading } = useLoader((signal) => api.accounts.clients(accountId, days, signal), {
    deps: [accountId, days],
  })
  const clients = byClient(data?.rows ?? [], data?.shares ?? {})
  const shared = clients.some((c) => c.share !== undefined)
  const end = (label: string): Column => ({ label, align: 'end' })
  const typeLabel = (t: string) => (t === '' ? 'no longer a client' : (types?.type(t).label ?? t))

  return (
    <Card title="By client" aside={<WindowPicker value={days} windows={WINDOWS} onChange={setDays} />}>
      <ErrorBanner error={error} />
      {loading && data === null ? (
        <Loading />
      ) : clients.length === 0 ? (
        <Empty>No client used this account in {lastDays(days)}.</Empty>
      ) : (
        <>
          <Table
            stack
            head={[
              'Client',
              ...(shared ? [end('5-hour share'), end('7-day share')] : []),
              end('Requests'),
              end('Tokens'),
              end('Last request'),
            ]}
          >
            {clients.map((c) => {
              const unfolded = open === c.id
              const toggle = () => setOpen(unfolded ? null : c.id)
              return (
                <Fragment key={c.id}>
                  <Tr onClick={toggle} selected={unfolded}>
                    <Td>
                      <div className="flex items-center gap-2">
                        <RowToggle
                          open={unfolded}
                          onToggle={toggle}
                          label={unfolded ? `Hide ${c.name}'s models` : `Show ${c.name}'s models`}
                        />
                        <div className="min-w-0">
                          <AppLink href={clientHref(c.id)} strong>
                            {c.name}
                          </AppLink>
                          <div className="truncate text-xs text-on-surface-variant">
                            {typeLabel(c.type)} · {slotsOf(c.rows)}
                          </div>
                        </div>
                      </div>
                    </Td>
                    {shared && (
                      <>
                        <Td label="5-hour share" align="end">
                          <Share points={c.share?.five_hour} />
                        </Td>
                        <Td label="7-day share" align="end">
                          <Share points={c.share?.seven_day} />
                        </Td>
                      </>
                    )}
                    <Td label="Requests" nums align="end">
                      {compact(c.requests)}
                      {c.errors > 0 && (
                        <div className="text-xs text-on-surface-variant">{compact(c.errors)} failed</div>
                      )}
                    </Td>
                    <Td
                      label="Tokens"
                      nums
                      align="end"
                      title={`${compact(c.input)} in · ${compact(c.output)} out${
                        c.reasoning > 0 ? ` (${compact(c.reasoning)} reasoning)` : ''
                      } · ${compact(c.cache)} cache`}
                    >
                      {compact(tokens(c))}
                    </Td>
                    <Td label="Last request" nowrap align="end">
                      {ago(c.last)}
                    </Td>
                  </Tr>
                  {unfolded && (
                    <TrDetail colSpan={shared ? 6 : 4}>
                      <SlotTable rows={c.rows} provider={false} />
                    </TrDetail>
                  )}
                </Fragment>
              )
            })}
          </Table>
          {shared && (
            <div className="mt-4">
              <Muted size="xs">
                A share is an estimate of each window as it stands now, whatever the period above: the
                window&rsquo;s use, split between clients by their cost-weighted part of what this
                gateway sent the account since the window opened. A dash is no traffic in that window.
              </Muted>
            </div>
          )}
        </>
      )}
    </Card>
  )
}

/** The model names a client used here, in the order it used them most; a real model for a request that named one. */
function slotsOf(rows: SlotUse[]): string {
  const names = [...new Set(rows.map((r) => r.slot || r.model))]
  return names.join(', ')
}

/**
 * A client's part of one window, in points of it: a bar on the window's own
 * 0-100 scale, so a client's bar and the window's meter above are the same
 * length for the same use, and the clients' bars add up to the window's.
 */
function Share({ points }: { points: number | undefined }) {
  if (points === undefined || points <= 0) return <Muted as="span">–</Muted>
  return <InlineMeter percent={points} tone="neutral" figure={smallPercent(points)} />
}
