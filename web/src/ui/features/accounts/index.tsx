import { useEffect, useState } from 'react'
import AddAccount from './add'
import { QuotaGauges } from './quota-meter'
import { byProvider, planLabel, providerLabel } from '../../providers'
import { accountHref } from '../../route'
import { api, type Account } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import {
  AddButton,
  Banner,
  Card,
  Chip,
  Empty,
  ErrorBanner,
  Freshness,
  IconButton,
  ListRow,
  ListSummary,
  ListToolbar,
  Loading,
  Page,
  RowBadge,
  Section,
  Stack,
  Switch,
} from '../../primitives'
import { accountName, accountStatus, isNamed } from '../../domain/account'
import { compact } from '../../format'

/**
 * Until the server has said how often it re-reads the subscription figures,
 * ask at the rate it used before it started saying. Polling faster than the
 * server polls upstream only re-renders the same numbers.
 */
const FALLBACK_REFRESH_MS = 20_000

/**
 * The accounts the pool draws on, grouped by provider, each group in its own
 * priority order.
 *
 * The order is the operator's decision, not something inferred: a personal
 * subscription and a work one are not interchangeable, and spreading evenly is
 * the wrong answer when one of them is the account you would rather not spend.
 * The gateway serves from the top and falls to the next when Anthropic says
 * the one above is out of room.
 */
export default function Accounts() {
  const [adding, setAdding] = useState(false)
  // What the last consent flow did, when that was not what was asked for.
  const [notice, setNotice] = useState('')
  const order = useAction()
  const ordering = order.pending
  // A drag is within one provider's group: priority is per provider, since a
  // request for a Claude model can only be served by a Claude account.
  const [dragging, setDragging] = useState<{ provider: string; at: number } | null>(null)
  const [over, setOver] = useState<number | null>(null)

  // Nothing may reload under a hand mid-gesture: a list that reorders itself
  // while it is being dragged drops the card somewhere nobody chose. Adding an
  // account is a form, and a poll that re-rendered around it would be just as
  // unwelcome.
  const settled = !adding && !ordering && dragging === null

  // The server tells us how often it re-reads the subscription usage. Held
  // separately from `data` because it is an input to the loader that produces
  // `data`, and it changes far less often than the figures do.
  const [pollMs, setPollMs] = useState(FALLBACK_REFRESH_MS)

  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.accounts.list(signal),
    { refreshMs: settled ? pollMs : 0 },
  )

  useEffect(() => {
    const advertised = data?.usagePollS ?? 0
    if (advertised > 0) setPollMs(advertised * 1000)
  }, [data?.usagePollS])

  const accounts = data?.accounts ?? []
  const windowDays = data?.windowDays ?? 7
  const groups = byProvider(accounts)

  /**
   * Moves one account within its provider's group and saves the whole
   * resulting order: the group's slots in the overall order are refilled in
   * its new order, so the other groups do not move.
   */
  const move = async (provider: string, from: number, to: number) => {
    const group = groups.find((g) => g.provider === provider)?.items ?? []
    if (to < 0 || to >= group.length || from === to) return
    const reordered = group.map((a) => a.id)
    const [moved] = reordered.splice(from, 1)
    if (moved === undefined) return
    reordered.splice(to, 0, moved)
    let next = 0
    // The group's slots, refilled in its new order: as many as it had.
    const ids = accounts.map((a) => (a.provider === provider ? (reordered[next++] ?? a.id) : a.id))

    await order.run('order', async () => {
      await api.accounts.reorder(ids)
      await reload()
    })
  }

  return (
    <Page>
      <ListToolbar
        action={
          <AddButton
            disabled={adding}
            onClick={() => {
              setNotice('')
              setAdding(true)
            }}
          >
            Add account
          </AddButton>
        }
      >
        {accounts.length > 0 && (
          <ListSummary>
            {compact(accounts.reduce((n, a) => n + a.requests, 0))} requests over {windowDays}d
          </ListSummary>
        )}
        <Freshness
          updatedAt={updatedAt}
          refreshing={refreshing}
          live={live}
          onRefresh={() => void reload()}
        />
      </ListToolbar>

      {adding && (
        <AddAccount
          onAdded={({ account, renewed }) => {
            setAdding(false)
            // The browser decides which account approves, so "add" in a browser
            // still signed in to a connected account renews that one. Say so,
            // or it looks as though nothing happened.
            const site = account.provider === 'openai' ? 'chatgpt.com' : 'claude.ai'
            setNotice(
              renewed
                ? `${accountName(account)} was already connected, so it was renewed rather than added. ` +
                    `To add a different account, sign in to ${site} as it first, or open the ` +
                    'login link in a private window.'
                : `${accountName(account)} is connected, at the bottom of the list. ` +
                    'Move it up to have it serve first.',
            )
            void reload()
          }}
          onCancel={() => setAdding(false)}
        />
      )}

      {notice !== '' && <Banner>{notice}</Banner>}

      <Card
        info={
          <p>
            Accounts are served top-down: the next one takes over when the one above is out of room.
            Drag a row, or use its arrows, to change the order.
          </p>
        }
      >
        <ErrorBanner error={error || order.error} className="mb-4" />

        {loading ? (
          <Loading />
        ) : accounts.length === 0 ? (
          <Empty>
            No accounts yet. Add one to give the proxy something to talk to — add several and
            it falls through to the next when one runs out of quota.
          </Empty>
        ) : (
          <Stack gap={6}>
            {groups.map((g) => {
              const orderable = g.items.length > 1
              const mine = dragging?.provider === g.provider
              return (
                <Section
                  key={g.provider}
                  label={providerLabel(g.provider)}
                  // Named once there is more than one provider to tell apart.
                  title={groups.length > 1 ? `${providerLabel(g.provider)} · ${g.items.length}` : undefined}
                >
                  <Stack gap={2} as="ul">
                    {g.items.map((a, i) => (
                      <li
                        key={a.id}
                        draggable={orderable && !ordering}
                        onDragStart={() => setDragging({ provider: g.provider, at: i })}
                        onDragOver={(e) => {
                          if (!mine) return
                          e.preventDefault()
                          setOver(i)
                        }}
                        onDragEnd={() => {
                          if (dragging !== null && mine && over !== null && dragging.at !== over) {
                            void move(g.provider, dragging.at, over)
                          }
                          setDragging(null)
                          setOver(null)
                        }}
                        className={`rounded-md3-l transition-[opacity,box-shadow] ${
                          mine && dragging?.at === i ? 'opacity-40' : ''
                        } ${mine && over === i && dragging?.at !== i ? 'ring-2 ring-primary' : ''}`}
                      >
                        <AccountRow
                          account={a}
                          rank={i}
                          total={g.items.length}
                          windowDays={windowDays}
                          orderable={orderable}
                          busy={ordering}
                          onMoveUp={() => void move(g.provider, i, i - 1)}
                          onMoveDown={() => void move(g.provider, i, i + 1)}
                          onChanged={() => void reload()}
                        />
                      </li>
                    ))}
                  </Stack>
                </Section>
              )
            })}
          </Stack>
        )}
      </Card>
    </Page>
  )
}

/**
 * One account in the priority list: enough to see which is serving and which
 * is running out, and a way into its page for everything else.
 *
 * The full card — tokens, models, test, reconnect, pause, remove — and the
 * account's own usage are on /accounts/<id>. The list keeps only what is about
 * the list: the order, and moving an account within it.
 */
function AccountRow({
  account: a,
  rank,
  total,
  windowDays,
  orderable,
  busy,
  onMoveUp,
  onMoveDown,
  onChanged,
}: {
  account: Account
  rank: number
  total: number
  windowDays: number
  orderable: boolean
  busy: boolean
  onMoveUp: () => void
  onMoveDown: () => void
  onChanged: () => void
}) {
  const chip = accountStatus(a)
  return (
    <ListRow
      href={accountHref(a.id)}
      highlight={a.serving}
      leading={
        <>
          {orderable && (
            <span
              className="hidden cursor-grab text-on-surface-variant active:cursor-grabbing sm:block"
              title="Drag to reorder"
              aria-hidden
            >
              <svg viewBox="0 0 24 24" className="size-5 fill-current">
                <path d="M9 4h2v2H9V4zm4 0h2v2h-2V4zM9 9h2v2H9V9zm4 0h2v2h-2V9zm-4 5h2v2H9v-2zm4 0h2v2h-2v-2zm-4 5h2v2H9v-2zm4 0h2v2h-2v-2z" />
              </svg>
            </span>
          )}
          <RowBadge>{rank + 1}</RowBadge>
        </>
      }
      title={accountName(a)}
      subtitle={[
        // A named account's address under its name: what tells two named
        // alike apart, and which sign-in it is.
        isNamed(a) ? a.email : '',
        planLabel(a.plan),
        `${compact(a.requests)} requests · ${compact(a.tokens)} tokens over ${windowDays}d`,
      ]
        .filter((part) => part !== '')
        .join(' · ')}
      chips={
        <>
          {a.serving && <Chip tone="ok">serving</Chip>}
          <Chip tone={chip.tone}>{chip.label}</Chip>
        </>
      }
      meters={a.quota !== undefined ? <QuotaGauges quota={a.quota} /> : undefined}
      actions={
        <>
          {orderable && (
            <>
              <IconButton label="Higher priority" disabled={busy || rank === 0} onClick={onMoveUp}>
                <path d="M7.4 15.4 12 10.8l4.6 4.6L18 14l-6-6-6 6z" />
              </IconButton>
              <IconButton label="Lower priority" disabled={busy || rank === total - 1} onClick={onMoveDown}>
                <path d="M7.4 8.6 12 13.2l4.6-4.6L18 10l-6 6-6-6z" />
              </IconButton>
            </>
          )}
          <AccountSwitch account={a} onChanged={onChanged} />
        </>
      }
    />
  )
}

/**
 * An account's on/off switch, the same in its list row and its page's
 * header, and the client's twin. Off pauses it without signing it out: it
 * serves nothing and keeps its credentials and its place in the order.
 */
export function AccountSwitch({ account, onChanged }: { account: Account; onChanged: () => void }) {
  const [on, setOn] = useState(!account.disabled)
  const { busy, error, run } = useAction()
  const flip = async (v: boolean) => {
    setOn(v)
    const ok = await run('switch', async () => {
      await api.accounts.setDisabled(account.id, !v)
      onChanged()
    })
    if (!ok) setOn(!v)
  }
  return (
    <span title={error || (on ? 'Serving from this account' : 'This account is paused')}>
      <Switch checked={on} onChange={(v) => void flip(v)} label={`Serve from ${accountName(account)}`} disabled={busy !== ''} quiet />
    </span>
  )
}

export type { Account }
