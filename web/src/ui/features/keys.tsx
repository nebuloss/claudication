import { useState } from 'react'
import { api, messageOf, type ApiKey } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  Card,
  CardTitle,
  Empty,
  Field,
  FilledButton,
  OutlinedButton,
  Spinner,
  StepSlider,
  Switch,
  Table,
  TextButton,
  TonalButton,
  ago,
  compact,
} from '../primitives'

/**
 * Client API keys — the credential you hand to Claude Code.
 *
 * The store keeps sha256(key) and a lookup prefix, never the key, so the
 * plaintext is shown once at creation and is genuinely unrecoverable after
 * that. The UI has to be honest about it rather than implying it can be looked
 * up later.
 */
export default function Keys({
  onExpired,
  onMinted,
}: {
  onExpired: () => void
  /**
   * Hands a freshly created key up to the app shell.
   *
   * It cannot live here. This panel unmounts the moment the operator changes
   * tab — or presses Back, since the tabs are hash routes — and the plaintext
   * is genuinely unrecoverable once it is gone, so a stray click would destroy
   * the only copy of a credential with no warning at all.
   */
  onMinted: (m: { key: ApiKey; plaintext: string }) => void
}) {
  const { data, error, loading, reload } = useLoader(() => api.listKeys(), onExpired)
  const [busy, setBusy] = useState('')
  const [editing, setEditing] = useState('')
  const [actionError, setActionError] = useState('')

  const act = async (id: string, fn: () => Promise<unknown>) => {
    setBusy(id)
    setActionError('')
    try {
      await fn()
      await reload()
    } catch (err) {
      setActionError(messageOf(err))
    }
    setBusy('')
  }

  return (
    <div className="flex flex-col gap-5">
      <NewKey onCreated={(m) => { onMinted(m); void reload() }} />

      <Card>
        <CardTitle
          aside={
            data !== null && data.keys.length > 0 ? (
              <span className="text-sm text-on-surface-variant">
                traffic over {data.windowDays}d
              </span>
            ) : null
          }
        >
          Keys
        </CardTitle>

        {error !== '' && (
          <Banner tone="error" className="mb-4">
            {error}
          </Banner>
        )}
        {actionError !== '' && (
          <Banner tone="error" className="mb-4">
            {actionError}
          </Banner>
        )}

        {loading ? (
          <p className="flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : data === null || data.keys.length === 0 ? (
          <Empty>No API keys yet. Create one above and give it to a client.</Empty>
        ) : (
          <Table head={['Name', 'Key', 'Requests', 'Tokens', 'Last used', 'Limit', 'Tokens/day', '']}>
            {data.keys.map((k) =>
              editing === k.id ? (
                <EditKeyRow
                  key={k.id}
                  apiKey={k}
                  busy={busy === k.id}
                  onCancel={() => setEditing('')}
                  onSave={(name, rpm, tokenBudget) =>
                    void act(k.id, async () => {
                      await api.updateKey(k.id, name, rpm, tokenBudget)
                      setEditing('')
                    })
                  }
                />
              ) : (
                <tr key={k.id} className="border-b border-outline-variant last:border-0">
                  <td className="px-2 py-3">
                    <div className="text-on-surface">{k.name}</div>
                    <div className="text-xs text-on-surface-variant">
                      created {ago(k.created_at)}
                    </div>
                  </td>
                  <td className="px-2 py-3 font-mono text-xs text-on-surface-variant">
                    {k.display}
                  </td>
                  <td className="px-2 py-3 tabular-nums">{compact(k.requests)}</td>
                  <td className="px-2 py-3 tabular-nums">{compact(k.tokens)}</td>
                  <td className="px-2 py-3 text-on-surface-variant">{ago(k.last_used_at)}</td>
                  <td className="px-2 py-3 text-on-surface-variant">
                    {k.rpm_limit > 0 ? `${k.rpm_limit}/min` : 'default'}
                  </td>
                  <td className="px-2 py-3">
                    <Budget budget={k.token_budget} spent={k.spent_today} />
                  </td>
                  <td className="px-2 py-3">
                    <div className="flex justify-end gap-2">
                      <TextButton disabled={busy === k.id} onClick={() => setEditing(k.id)}>
                        Edit
                      </TextButton>
                      <TextButton
                        tone="error"
                        disabled={busy === k.id}
                        onClick={() => void act(k.id, () => api.deleteKey(k.id))}
                      >
                        Delete
                      </TextButton>
                    </div>
                  </td>
                </tr>
              ),
            )}
          </Table>
        )}

        <p className="mt-4 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          Deleting stops a key working immediately and cannot be undone. The Usage tab keeps
          showing what it spent, under the name it was recorded with.
        </p>
      </Card>
    </div>
  )
}

/**
 * The ladder the budget slider moves along.
 *
 * Round numbers an order of magnitude apart at the bottom, closing up towards
 * the top where the difference matters less. A single Claude Code turn resends
 * the whole transcript, so the useful floor is higher than it looks: 10k is
 * roughly one small exchange.
 */
const BUDGET_STEPS = [
  10_000, 25_000, 50_000, 100_000, 250_000, 500_000,
  1_000_000, 2_500_000, 5_000_000, 10_000_000, 25_000_000, 50_000_000,
]

const DEFAULT_BUDGET = 1_000_000

/**
 * "Is there a limit at all" is a switch, not a number you have to know to type
 * as zero — which reads as "no tokens allowed" rather than "no ceiling".
 * Unlimited is the default, because that is what a key does until someone
 * decides otherwise.
 */
function BudgetControl({
  budget,
  onChange,
}: {
  budget: number
  onChange: (v: number) => void
}) {
  const limited = budget > 0
  return (
    <div className="flex flex-col gap-3 rounded-[var(--radius-md3-m)] border border-outline-variant p-4">
      <Switch
        checked={limited}
        onChange={(on) => onChange(on ? DEFAULT_BUDGET : 0)}
        label="Limit how many tokens this key can spend per day"
      />
      {limited ? (
        <>
          <StepSlider
            label="Tokens per day"
            steps={BUDGET_STEPS}
            value={budget}
            onChange={onChange}
            format={(v) => compact(v)}
          />
          <p className="m-0 text-xs text-on-surface-variant">
            Counted over a rolling 24 hours, so it frees up gradually rather than
            all at once. Input, output and cache tokens all count, because all
            four are billed.
          </p>
        </>
      ) : (
        <p className="m-0 text-xs text-on-surface-variant">
          This key can spend as much as the connected accounts allow.
        </p>
      )}
    </div>
  )
}

/**
 * A key's daily token ceiling, and how close it is to it.
 *
 * "Unlimited" spelled out rather than shown as 0, which reads as the opposite
 * of what it means. When there is a budget the spend is shown against it, since
 * "is this key about to be cut off" is the question the column exists for.
 */
function Budget({ budget, spent }: { budget: number; spent: number }) {
  if (budget <= 0) {
    return <span className="text-on-surface-variant">unlimited</span>
  }
  const share = spent / budget
  const tone =
    share >= 1 ? 'text-error' : share >= 0.8 ? 'text-on-warning-container' : 'text-on-surface'
  return (
    <span className={`tabular-nums ${tone}`}>
      {compact(spent)} / {compact(budget)}
    </span>
  )
}

/** The same row, in edit mode. The key itself is not editable — only its label
 *  and its limits, which is the point: changing the secret means visiting every
 *  client that holds it. */
function EditKeyRow({
  apiKey,
  busy,
  onSave,
  onCancel,
}: {
  apiKey: ApiKey
  busy: boolean
  onSave: (name: string, rpmLimit: number, tokenBudget: number) => void
  onCancel: () => void
}) {
  const [name, setName] = useState(apiKey.name)
  const [rpm, setRpm] = useState(apiKey.rpm_limit > 0 ? String(apiKey.rpm_limit) : '')
  const [budget, setBudget] = useState(apiKey.token_budget)

  const limit = rpm.trim() === '' ? 0 : Math.floor(Number(rpm))
  const valid = name.trim() !== '' && Number.isFinite(limit) && limit >= 0

  // One cell spanning the row: the budget control is a switch and a slider,
  // which will not sit in a table column without being unusable.
  return (
    <tr className="border-b border-outline-variant last:border-0">
      <td className="px-2 py-4" colSpan={8}>
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-start gap-4">
            <Field label="Name" value={name} onChange={setName} autoFocus className="max-w-xs" />
            <Field
              label="Requests per minute"
              value={rpm}
              onChange={setRpm}
              type="number"
              className="max-w-[12rem]"
            />
          </div>
          <BudgetControl budget={budget} onChange={setBudget} />
          <div className="flex items-center gap-2">
            <FilledButton
              type="button"
              disabled={busy || !valid}
              onClick={() => onSave(name.trim(), limit, budget)}
            >
              {busy && <Spinner />}
              {busy ? 'Saving…' : 'Save'}
            </FilledButton>
            <OutlinedButton disabled={busy} onClick={onCancel}>
              Cancel
            </OutlinedButton>
            <span className="text-xs text-on-surface-variant">
              The key itself never changes, so nothing holding it needs updating.
            </span>
          </div>
        </div>
      </td>
    </tr>
  )
}

function NewKey({ onCreated }: { onCreated: (m: { key: ApiKey; plaintext: string }) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [rpm, setRpm] = useState('')
  const [budget, setBudget] = useState(0)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (!open) {
    return (
      <div>
        <TonalButton onClick={() => setOpen(true)}>New API key</TonalButton>
      </div>
    )
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (name.trim() === '') {
      setError('Give the key a name — it is how you will recognise it later.')
      return
    }
    const limit = rpm.trim() === '' ? 0 : Number(rpm)
    if (!Number.isFinite(limit) || limit < 0) {
      setError('The rate limit must be a number of requests per minute, or blank.')
      return
    }

    setBusy(true)
    setError('')
    try {
      onCreated(await api.createKey(name.trim(), Math.floor(limit), budget))
      setOpen(false)
      setName('')
      setRpm('')
      setBudget(0)
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy(false)
  }

  return (
    <Card>
      <CardTitle>New API key</CardTitle>
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <Field label="Name" value={name} onChange={setName} autoFocus />
        <Field
          label="Requests per minute (blank = server default)"
          value={rpm}
          onChange={setRpm}
          type="number"
        />
        <BudgetControl budget={budget} onChange={setBudget} />
        {error !== '' && <Banner tone="error">{error}</Banner>}
        <div className="flex items-center gap-2">
          <FilledButton disabled={busy}>
            {busy && <Spinner />}
            {busy ? 'Creating…' : 'Create key'}
          </FilledButton>
          <OutlinedButton onClick={() => setOpen(false)} disabled={busy}>
            Cancel
          </OutlinedButton>
        </div>
      </form>
    </Card>
  )
}
