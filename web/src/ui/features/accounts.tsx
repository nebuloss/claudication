import { useEffect, useState } from 'react'
import { status } from './account-card'
import AddAccount from './add-account'
import { accountHref } from './usage'
import { api, messageOf, type Account } from '../../api/client'
import { useLoader } from '../hooks'
import {
  AppLink,
  Banner,
  Card,
  CardTitle,
  Chip,
  Empty,
  Freshness,
  IconButton,
  Spinner,
  TonalButton,
  openRow,
  compact,
} from '../primitives'

/**
 * Until the server has said how often it re-reads the subscription figures,
 * ask at the rate it used before it started saying. Polling faster than the
 * server polls upstream only re-renders the same numbers.
 */
const FALLBACK_REFRESH_MS = 20_000

/**
 * The Claude accounts the pool draws on, in priority order.
 *
 * The order is the operator's decision, not something inferred: a personal
 * subscription and a work one are not interchangeable, and spreading evenly is
 * the wrong answer when one of them is the account you would rather not spend.
 * The gateway serves from the top and falls to the next when Anthropic says
 * the one above is out of room.
 */
export default function Accounts({ onExpired }: { onExpired: () => void }) {
  const [adding, setAdding] = useState(false)
  // What the last consent flow did, when that was not what was asked for.
  const [notice, setNotice] = useState('')
  const [ordering, setOrdering] = useState(false)
  const [dragging, setDragging] = useState<number | null>(null)
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

  const { data, error, loading, reload, setError, refreshing, updatedAt, live } = useLoader(
    () => api.listAccounts(),
    onExpired,
    [],
    settled ? pollMs : 0,
  )

  useEffect(() => {
    const advertised = data?.usagePollS ?? 0
    if (advertised > 0) setPollMs(advertised * 1000)
  }, [data?.usagePollS])

  const accounts = data?.accounts ?? []
  const windowDays = data?.windowDays ?? 7
  const orderable = accounts.length > 1

  /** Moves one account and saves the whole resulting order. */
  const move = async (from: number, to: number) => {
    if (to < 0 || to >= accounts.length || from === to) return
    const ids = accounts.map((a) => a.id)
    const [moved] = ids.splice(from, 1)
    ids.splice(to, 0, moved)

    setOrdering(true)
    try {
      await api.reorderAccounts(ids)
      await reload()
    } catch (err) {
      setError(messageOf(err))
    }
    setOrdering(false)
  }

  return (
    <div className="flex flex-col gap-5">
      {adding ? (
        <AddAccount
          onAdded={({ account, renewed }) => {
            setAdding(false)
            // The browser decides which account approves, so "add" in a browser
            // still signed in to a connected account renews that one. Say so,
            // or it looks as though nothing happened.
            setNotice(
              renewed
                ? `${account.email} was already connected, so it was renewed rather than added. ` +
                    'To add a different account, sign in to claude.ai as it first, or open the ' +
                    'login link in a private window.'
                : '',
            )
            void reload()
          }}
          onCancel={() => setAdding(false)}
        />
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <TonalButton
            onClick={() => {
              setNotice('')
              setAdding(true)
            }}
          >
            Add account
          </TonalButton>
          {orderable && (
            <span className="text-sm text-on-surface-variant">
              Served top-down — the next one takes over when Anthropic says the one above is out.
            </span>
          )}
        </div>
      )}

      {notice !== '' && <Banner>{notice}</Banner>}

      <Card>
        <CardTitle
          aside={
            <div className="flex items-center gap-3">
              {accounts.length > 0 && (
                <span className="hidden text-sm whitespace-nowrap text-on-surface-variant sm:inline">
                  {compact(accounts.reduce((n, a) => n + a.requests, 0))} requests over{' '}
                  {windowDays}d
                </span>
              )}
              <Freshness
                updatedAt={updatedAt}
                refreshing={refreshing}
                live={live}
                onRefresh={() => void reload()}
              />
            </div>
          }
        >
          Claude accounts
        </CardTitle>

        {error !== '' && (
          <Banner tone="error" className="mb-4">
            {error}
          </Banner>
        )}

        {loading ? (
          <p className="flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : accounts.length === 0 ? (
          <Empty>
            No Claude accounts yet. Add one to give the proxy something to talk to — add several and
            it falls through to the next when one runs out of quota.
          </Empty>
        ) : (
          <ul className="m-0 flex list-none flex-col gap-2 p-0">
            {accounts.map((a, i) => (
              <li
                key={a.id}
                draggable={orderable && !ordering}
                onDragStart={() => setDragging(i)}
                onDragOver={(e) => {
                  e.preventDefault()
                  setOver(i)
                }}
                onDragEnd={() => {
                  if (dragging !== null && over !== null && dragging !== over) {
                    void move(dragging, over)
                  }
                  setDragging(null)
                  setOver(null)
                }}
                className={`rounded-[var(--radius-md3-l)] transition-[opacity,box-shadow] ${
                  dragging === i ? 'opacity-40' : ''
                } ${over === i && dragging !== null && dragging !== i ? 'ring-2 ring-primary' : ''}`}
              >
                <AccountRow
                  account={a}
                  rank={i}
                  total={accounts.length}
                  windowDays={windowDays}
                  orderable={orderable}
                  busy={ordering}
                  onMoveUp={() => void move(i, i - 1)}
                  onMoveDown={() => void move(i, i + 1)}
                />
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  )
}

/** A subscription window's fill, as a short bar and a figure. */
export function Window({ label, util }: { label: string; util: number }) {
  const pct = Math.max(0, Math.min(100, Math.round(util)))
  const tone = pct >= 90 ? 'bg-error' : pct >= 70 ? 'bg-warning' : 'bg-success'
  return (
    <span className="flex items-center gap-1.5" title={`${label}: ${pct}% used`}>
      <span className="w-5 text-on-surface-variant">{label}</span>
      <span className="h-1.5 w-12 overflow-hidden rounded-full bg-surface-high">
        <span className={`block h-full ${tone}`} style={{ width: `${pct}%` }} />
      </span>
      <span className="w-8 text-right">{pct}%</span>
    </span>
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
}: {
  account: Account
  rank: number
  total: number
  windowDays: number
  orderable: boolean
  busy: boolean
  onMoveUp: () => void
  onMoveDown: () => void
}) {
  const chip = status(a)
  const href = accountHref(a.id)
  return (
    <div
      onClick={openRow(href)}
      className={`state-layer flex cursor-pointer flex-wrap items-center gap-x-4 gap-y-2 rounded-[var(--radius-md3-l)] px-4 py-3 ${
        a.serving ? 'bg-secondary-container/40' : 'bg-surface-low'
      }`}
    >
      <div className="flex min-w-0 flex-1 items-center gap-3">
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
        <span className="grid size-7 shrink-0 place-items-center rounded-full bg-surface-high text-xs font-medium text-on-surface-variant">
          {rank + 1}
        </span>
        <div className="min-w-0">
          <AppLink href={href} className="block truncate font-medium text-on-surface no-underline">
            {a.email}
          </AppLink>
          <span className="text-xs text-on-surface-variant tabular-nums">
            {compact(a.requests)} requests · {compact(a.tokens)} tokens over {windowDays}d
          </span>
        </div>
      </div>

      <div className="flex items-center gap-1">
        {a.serving && <Chip tone="ok">serving</Chip>}
        <Chip tone={chip.tone}>{chip.label}</Chip>
      </div>

      {a.quota !== undefined && (
        <div className="flex items-center gap-3 text-xs tabular-nums text-on-surface">
          <Window label="5h" util={a.quota.five_hour_util} />
          <Window label="7d" util={a.quota.seven_day_util} />
        </div>
      )}

      <div className="flex items-center gap-1">
        {orderable && (
          <>
            <IconButton label="Higher priority" disabled={busy || rank === 0} onClick={onMoveUp}>
              <path d="M7.4 15.4 12 10.8l4.6 4.6L18 14l-6-6-6 6z" />
            </IconButton>
            <IconButton
              label="Lower priority"
              disabled={busy || rank === total - 1}
              onClick={onMoveDown}
            >
              <path d="M7.4 8.6 12 13.2l4.6-4.6L18 10l-6 6-6-6z" />
            </IconButton>
          </>
        )}
        <span aria-hidden className="grid size-9 place-items-center text-on-surface-variant">
          <svg viewBox="0 0 24 24" className="size-5 fill-current">
            <path d="M8.6 16.6 13.2 12 8.6 7.4 10 6l6 6-6 6z" />
          </svg>
        </span>
      </div>
    </div>
  )
}

export type { Account }
