import { useState } from 'react'
import AccountCard from './account-card'
import AddAccount from './add-account'
import { api, messageOf, type Account } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Card, CardTitle, Empty, Spinner, TonalButton, compact } from '../primitives'

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
  const { data, error, loading, reload, setError } = useLoader(
    () => api.listAccounts(),
    onExpired,
  )
  const [adding, setAdding] = useState(false)
  const [ordering, setOrdering] = useState(false)
  const [dragging, setDragging] = useState<number | null>(null)
  const [over, setOver] = useState<number | null>(null)

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
          onAdded={() => {
            setAdding(false)
            void reload()
          }}
          onCancel={() => setAdding(false)}
        />
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <TonalButton onClick={() => setAdding(true)}>Add account</TonalButton>
          {orderable && (
            <span className="text-sm text-on-surface-variant">
              Served top-down — the next one takes over when Anthropic says the one above is out.
            </span>
          )}
        </div>
      )}

      <Card>
        <CardTitle
          aside={
            accounts.length > 0 ? (
              <span className="text-sm text-on-surface-variant">
                {compact(accounts.reduce((n, a) => n + a.requests, 0))} requests over {windowDays}d
              </span>
            ) : null
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
          <ul className="m-0 flex list-none flex-col gap-4 p-0">
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
                <AccountCard
                  account={a}
                  rank={i}
                  total={accounts.length}
                  windowDays={windowDays}
                  orderable={orderable}
                  busy={ordering}
                  onMoveUp={() => void move(i, i - 1)}
                  onMoveDown={() => void move(i, i + 1)}
                  onChanged={() => void reload()}
                  onRemoved={() => void reload()}
                />
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  )
}

export type { Account }
