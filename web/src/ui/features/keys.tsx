import { useState } from 'react'
import { api, messageOf, type ApiKey } from '../../api/client'
import { useLoader } from '../hooks'
import type { ReactNode } from 'react'
import {
  Banner,
  Card,
  CardTitle,
  Empty,
  Field,
  Modal,
  Segmented,
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

  // Resolved from the loaded list rather than held separately, so a reload
  // cannot leave the dialog editing a key that no longer exists.
  const edited = data?.keys.find((k) => k.id === editing)

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
      <NewKey
        defaultRpm={data?.defaultRpm ?? 0}
        onCreated={(m) => {
          onMinted(m)
          void reload()
        }}
      />

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
        {actionError !== '' && editing === '' && (
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
            {data.keys.map((k) => (
              <tr key={k.id} className="border-b border-outline-variant last:border-0">
                <td className="px-2 py-3">
                  <div className="text-on-surface">{k.name}</div>
                  <div className="text-xs text-on-surface-variant">created {ago(k.created_at)}</div>
                </td>
                <td className="px-2 py-3 font-mono text-xs text-on-surface-variant">{k.display}</td>
                <td className="px-2 py-3 tabular-nums">{compact(k.requests)}</td>
                <td className="px-2 py-3 tabular-nums">{compact(k.tokens)}</td>
                <td className="px-2 py-3 text-on-surface-variant">{ago(k.last_used_at)}</td>
                <td className="px-2 py-3 text-on-surface-variant">
                  {k.rpm_limit > 0
                    ? `${compact(k.rpm_limit)} / ${periodName(k.rate_period_s)}`
                    : 'default'}
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
            ))}
          </Table>
        )}

        <p className="mt-4 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          Deleting stops a key working immediately and cannot be undone. The Usage tab keeps
          showing what it spent, under the name it was recorded with.
        </p>
      </Card>

      {edited !== undefined && (
        <EditKeyModal
          apiKey={edited}
          defaultRpm={data?.defaultRpm ?? 0}
          busy={busy === edited.id}
          error={actionError}
          onClose={() => {
            setEditing('')
            setActionError('')
          }}
          onSave={(name, rpm, periodS, tokenBudget) =>
            void act(edited.id, async () => {
              await api.updateKey(edited.id, name, rpm, periodS, tokenBudget)
              setEditing('')
            })
          }
        />
      )}
    </div>
  )
}

/**
 * The ladders the sliders move along.
 *
 * Round numbers roughly a factor apart rather than a linear range: both of
 * these span three or four orders of magnitude, so a linear slider spends most
 * of its travel on values nobody wants and cannot land on a round one. Stepping
 * through a 1-2-5 ladder makes every position a value worth choosing.
 *
 * A Claude Code turn resends the whole transcript, so the useful floor for a
 * budget is higher than it looks: 10k is roughly one small exchange.
 */
const BUDGET_STEPS = [
  10_000, 25_000, 50_000, 100_000, 250_000, 500_000, 1_000_000, 2_500_000, 5_000_000, 10_000_000,
  25_000_000, 50_000_000,
]
const RPM_STEPS = [10, 20, 50, 100, 200, 500, 1_000, 2_000, 5_000, 10_000]

/**
 * What the request count is measured over.
 *
 * A real period rather than arithmetic in the browser. "Two hundred an hour"
 * converted to three a minute is a different limit: it refuses a burst of ten
 * in twenty seconds, which is exactly what two hundred an hour should permit.
 * The gateway stores the period and the bucket refills over it.
 */
const RATE_PERIODS = [
  { id: '60', label: 'minute', seconds: 60 },
  { id: '3600', label: 'hour', seconds: 3600 },
  { id: '86400', label: 'day', seconds: 86_400 },
]

const DEFAULT_BUDGET = 1_000_000
const DEFAULT_RPM_CHOICE = 200
const DEFAULT_PERIOD_S = 60

/**
 * How a period reads in a table cell.
 *
 * A period the UI does not offer is still shown rather than mislabelled: the
 * API takes any duration up to a week, so a key configured through it must not
 * be displayed as something it is not.
 */
function periodName(seconds: number): string {
  const known = RATE_PERIODS.find((p) => p.seconds === (seconds || DEFAULT_PERIOD_S))
  if (known !== undefined) return known.label
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${seconds}s`
}

/**
 * A limit that can be switched off, and adjusted when it is on.
 *
 * "Is there a limit at all" is a switch rather than a number you have to know
 * to type as zero — which reads as "none allowed", the opposite of what zero
 * means in both of these fields.
 *
 * What "off" means differs between the two, and the caption has to say which:
 * no token budget is no ceiling at all, while no rate limit means the server's
 * own, not an unlimited one.
 */
function LimitControl({
  label,
  sliderLabel,
  steps,
  value,
  whenOn,
  onChange,
  off,
  format,
  tone,
  children,
}: {
  label: string
  sliderLabel: string
  steps: number[]
  value: number
  /** What to set when the switch is turned on. */
  whenOn: number
  onChange: (v: number) => void
  /** What it means to have this switched off. */
  off: string
  format: (v: number) => string
  /** Which container role tints the section when the limit is on. */
  tone: 'primary' | 'tertiary'
  /** Anything the limit needs beyond an amount, such as a period. */
  children?: ReactNode
}) {
  const on = value > 0
  // Tinted when it is doing something, plain when it is not — so which limits
  // are actually set is legible before reading a word of it. Off is deliberately
  // the quiet state rather than a second colour competing for attention.
  const skin = on
    ? tone === 'primary'
      ? 'border-primary/40 bg-primary-container/30'
      : 'border-tertiary/40 bg-tertiary-container/30'
    : 'border-outline-variant bg-surface-low'
  return (
    <div className={`flex flex-col gap-3 rounded-[var(--radius-md3-m)] border p-4 ${skin}`}>
      <Switch checked={on} onChange={(v) => onChange(v ? whenOn : 0)} label={label} />
      {on ? (
        <>
          <StepSlider
            label={sliderLabel}
            steps={steps}
            value={value}
            onChange={onChange}
            format={format}
          />
          {children}
        </>
      ) : (
        <p className="m-0 text-xs text-on-surface-variant">{off}</p>
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

/**
 * Editing a key, in a dialog.
 *
 * A dialog rather than a row that unfolds inside the table: the controls are a
 * switch and a slider each, which do not fit a table column without becoming
 * unusable, and unfolding a row pushes everything below it down while you are
 * reading it.
 *
 * The secret is not editable and says so. That is the point of being able to
 * edit at all — a typo in a name, or a limit set too low, should not cost a trip
 * round every client holding the key.
 */
function KeyLimitFields({
  defaultRpm,
  rpm,
  setRpm,
  periodS,
  setPeriodS,
  budget,
  setBudget,
}: {
  defaultRpm: number
  rpm: number
  setRpm: (v: number) => void
  periodS: number
  setPeriodS: (v: number) => void
  budget: number
  setBudget: (v: number) => void
}) {
  const period = RATE_PERIODS.find((p) => p.seconds === periodS) ?? RATE_PERIODS[0]
  return (
    <>
      <LimitControl
        tone="primary"
        label="Limit how often this key can make requests"
        sliderLabel={`Requests per ${period.label}`}
        steps={RPM_STEPS}
        value={rpm}
        whenOn={DEFAULT_RPM_CHOICE}
        onChange={setRpm}
        off={`Uses the gateway's own limit of ${compact(defaultRpm)} a minute.`}
        format={(v) => `${compact(v)} / ${period.label}`}
      >
        <Segmented
          label="Period"
          value={String(periodS)}
          onChange={(v) => setPeriodS(Number(v))}
          options={RATE_PERIODS.map((p) => ({
            id: p.id,
            label: `per ${p.label}`,
            content: `per ${p.label}`,
          }))}
        />
        <p className="m-0 text-xs text-on-primary-container">
          The whole allowance is available at once and refills over the period, so{' '}
          {compact(rpm)} per {period.label} permits a burst of {compact(rpm)} and then runs dry.
        </p>
      </LimitControl>

      <LimitControl
        tone="tertiary"
        label="Limit how many tokens this key can spend per day"
        sliderLabel="Tokens per day"
        steps={BUDGET_STEPS}
        value={budget}
        whenOn={DEFAULT_BUDGET}
        onChange={setBudget}
        off="This key can spend as much as the connected accounts allow."
        format={compact}
      >
        <p className="m-0 text-xs text-on-tertiary-container">
          Counted over a rolling 24 hours, so it frees up gradually rather than all at once. Input,
          output and cache tokens all count, because all four are billed.
        </p>
      </LimitControl>
    </>
  )
}

function EditKeyModal({
  apiKey,
  defaultRpm,
  busy,
  error,
  onSave,
  onClose,
}: {
  apiKey: ApiKey
  defaultRpm: number
  busy: boolean
  error: string
  onSave: (name: string, rpmLimit: number, periodS: number, tokenBudget: number) => void
  onClose: () => void
}) {
  const [name, setName] = useState(apiKey.name)
  const [rpm, setRpm] = useState(apiKey.rpm_limit)
  const [periodS, setPeriodS] = useState(apiKey.rate_period_s || DEFAULT_PERIOD_S)
  const [budget, setBudget] = useState(apiKey.token_budget)

  const valid = name.trim() !== ''

  return (
    <Modal title={`Edit ${apiKey.name}`} size="lg" onClose={onClose}>
      <div className="flex flex-col gap-4">
        <Field label="Name" value={name} onChange={setName} autoFocus />

        <KeyLimitFields
          defaultRpm={defaultRpm}
          rpm={rpm}
          setRpm={setRpm}
          periodS={periodS}
          setPeriodS={setPeriodS}
          budget={budget}
          setBudget={setBudget}
        />

        <div className="rounded-[var(--radius-md3-m)] border border-outline-variant bg-surface-low px-4 py-3">
          <div className="mb-1 text-xs font-medium tracking-wide text-on-surface-variant uppercase">
            Key
          </div>
          <code className="font-mono text-xs text-on-surface">{apiKey.display}</code>
          <p className="mt-1 mb-0 text-xs text-on-surface-variant">
            Unchanged by anything here, so nothing holding it needs updating.
          </p>
        </div>

        {error !== '' && <Banner tone="error">{error}</Banner>}

        <div className="flex items-center justify-end gap-2">
          <OutlinedButton onClick={onClose} disabled={busy}>
            Cancel
          </OutlinedButton>
          <FilledButton
            type="button"
            disabled={busy || !valid}
            onClick={() => onSave(name.trim(), rpm, periodS, budget)}
          >
            {busy && <Spinner />}
            {busy ? 'Saving…' : 'Save'}
          </FilledButton>
        </div>
      </div>
    </Modal>
  )
}

function NewKey({
  onCreated,
  defaultRpm,
}: {
  onCreated: (m: { key: ApiKey; plaintext: string }) => void
  defaultRpm: number
}) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [rpm, setRpm] = useState(0)
  const [periodS, setPeriodS] = useState(DEFAULT_PERIOD_S)
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
    setBusy(true)
    setError('')
    try {
      onCreated(await api.createKey(name.trim(), rpm, periodS, budget))
      setOpen(false)
      setName('')
      setRpm(0)
      setPeriodS(DEFAULT_PERIOD_S)
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
        <KeyLimitFields
          defaultRpm={defaultRpm}
          rpm={rpm}
          setRpm={setRpm}
          periodS={periodS}
          setPeriodS={setPeriodS}
          budget={budget}
          setBudget={setBudget}
        />
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
