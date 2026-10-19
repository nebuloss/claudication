import { useState } from 'react'
import {
  api,
  type ApiKey,
  type KeyLimits,
} from '../../../api/client'
import { useAction } from '../../hooks'
import type { ReactNode } from 'react'
import {
  Actions,
  Button,
  Card,
  ErrorBanner,
  Field,
  Muted,
  Overline,
  Segmented,
  Stack,
  StepSlider,
  Switch,
  Tile,
} from '../../primitives'
import { compact } from '../../format'

/** A key's name and limits: the controls that set them, for a new key and an existing one. */

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
export const BUDGET_STEPS = [
  10_000, 25_000, 50_000, 100_000, 250_000, 500_000, 1_000_000, 2_500_000, 5_000_000, 10_000_000,
  25_000_000, 50_000_000,
]

export const RPM_STEPS = [10, 20, 50, 100, 200, 500, 1_000, 2_000, 5_000, 10_000]

/**
 * What the request count is measured over.
 *
 * A real period rather than arithmetic in the browser. "Two hundred an hour"
 * converted to three a minute is a different limit: it refuses a burst of ten
 * in twenty seconds, which is exactly what two hundred an hour should permit.
 * The gateway stores the period and the bucket refills over it.
 */
export const RATE_PERIODS = [
  { id: '60', label: 'minute', seconds: 60 },
  { id: '3600', label: 'hour', seconds: 3600 },
  { id: '86400', label: 'day', seconds: 86_400 },
]

export const DEFAULT_BUDGET = 1_000_000

export const DEFAULT_RPM_CHOICE = 200

export const DEFAULT_PERIOD_S = 60

/**
 * How a period reads in a table cell.
 *
 * A period the UI does not offer is still shown rather than mislabelled: the
 * API takes any duration up to a week, so a key configured through it must not
 * be displayed as something it is not.
 */
export function periodName(seconds: number): string {
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
export function LimitControl({
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
    <div className={`flex flex-col gap-3 rounded-md3-m border p-4 ${skin}`}>
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
        <Muted size="xs">{off}</Muted>
      )}
    </div>
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
export function KeyLimitFields({
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
        <p className="text-xs text-on-primary-container">
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
        <p className="text-xs text-on-tertiary-container">
          Counted over a rolling 24 hours, so it frees up gradually rather than all at once. Input,
          output and cache tokens all count, because all four are billed.
        </p>
      </LimitControl>
    </>
  )
}

export function KeyEditor({
  apiKey,
  defaultRpm,
  busy,
  onSave,
}: {
  apiKey: ApiKey
  defaultRpm: number
  busy: boolean
  onSave: (name: string, limits: KeyLimits) => void
}) {
  const [name, setName] = useState(apiKey.name)
  const [rpm, setRpm] = useState(apiKey.rpm_limit)
  const [periodS, setPeriodS] = useState(apiKey.rate_period_s || DEFAULT_PERIOD_S)
  const [budget, setBudget] = useState(apiKey.token_budget)

  const valid = name.trim() !== ''
  const changed =
    name.trim() !== apiKey.name ||
    rpm !== apiKey.rpm_limit ||
    periodS !== (apiKey.rate_period_s || DEFAULT_PERIOD_S) ||
    budget !== apiKey.token_budget

  return (
    <Stack>
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

        <Tile>
          <div className="mb-1">
            <Overline>Key</Overline>
          </div>
          <code className="text-xs text-on-surface">{apiKey.display}</code>
          <div className="mt-1">
            <Muted size="xs">Unchanged by anything here, so nothing holding it needs updating.</Muted>
          </div>
        </Tile>

        <Actions end>
          <Button
            variant="outlined"
            onClick={() => {
              setName(apiKey.name)
              setRpm(apiKey.rpm_limit)
              setPeriodS(apiKey.rate_period_s || DEFAULT_PERIOD_S)
              setBudget(apiKey.token_budget)
            }}
            disabled={busy || !changed}
          >
            Undo changes
          </Button>
          <Button
            busy={busy}
            disabled={!valid || !changed}
            onClick={() => onSave(name.trim(), { rpmLimit: rpm, ratePeriodS: periodS, tokenBudget: budget })}
          >
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </Actions>
    </Stack>
  )
}

export function NewKey({
  onCreated,
  onClose,
  defaultRpm,
}: {
  onCreated: (m: { key: ApiKey; plaintext: string }) => void
  onClose: () => void
  defaultRpm: number
}) {
  const [name, setName] = useState('')
  const [rpm, setRpm] = useState(0)
  const [periodS, setPeriodS] = useState(DEFAULT_PERIOD_S)
  const [budget, setBudget] = useState(0)
  const { pending: busy, error, setError, run } = useAction()

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (name.trim() === '') {
      setError('Give the key a name — it is how you will recognise it later.')
      return
    }
    await run('create', async () => {
      onCreated(
        await api.keys.create(name.trim(), { rpmLimit: rpm, ratePeriodS: periodS, tokenBudget: budget }),
      )
    })
  }

  return (
    <Card title="New API key">
      <form onSubmit={submit}>
        <Stack>
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
          <ErrorBanner error={error} />
          <Actions>
            <Button type="submit" busy={busy}>
              {busy ? 'Creating…' : 'Create key'}
            </Button>
            <Button variant="outlined" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
          </Actions>
        </Stack>
      </form>
    </Card>
  )
}
