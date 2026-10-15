import { useState } from 'react'
import { api, messageOf, type ApiKey, type SubscriptionShare } from '../../api/client'
import { useLoader } from '../hooks'
import { navigate } from '../route'
import ScopedUsage, { accountHref, keyHref } from './usage'
import { DetailHeader, detailSection } from './detail'
import { KeyModels } from './account-models'
import { KeyVirtualModels } from './virtual-models'
import type { ReactNode } from 'react'
import {
  AppLink,
  Banner,
  Card,
  CardTitle,
  Chip,
  Empty,
  Field,
  Freshness,
  Segmented,
  FilledButton,
  OutlinedButton,
  Spinner,
  Stat,
  StepSlider,
  Switch,
  Table,
  TonalButton,
  ago,
  compact,
  openRow,
} from '../primitives'

/**
 * Keys change when someone mints one, but "last used" and the spent budget
 * move with traffic, and the budget is the figure an operator watches when
 * they are deciding whether a limit is set too low.
 */
const REFRESH_MS = 20_000

/**
 * Why the gateway's own key has no working controls.
 *
 * Deleting it never did what the button implied — the next chat needing a name
 * issued a fresh one under a new id and orphaned the usage attributed to the
 * old one — and editing it is the operator changing limits on something they
 * did not create and do not call. The control that governs what it spends is
 * the switch that turns the feature on.
 */
const MANAGED_HINT =
  'Issued by the gateway for its own requests. Turn off chat names in Settings to stop it.'

/**
 * Client API keys — the credential you hand to Claude Code.
 *
 * A key authenticates by its hash, and is kept sealed beside it (migration
 * 026) so its settings page can show it and its configs again. Keys from
 * before that have only the hash, and say so there.
 */
export default function Keys({
  onExpired,
  onMinted,
}: {
  onExpired: () => void
  /**
   * Hands a freshly created key up to the app shell, whose dialog outlives
   * this panel unmounting on a tab change or Back.
   */
  onMinted: (m: { key: ApiKey; plaintext: string }) => void
}) {
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    () => api.listKeys(),
    onExpired,
    [],
    REFRESH_MS,
  )

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
            <div className="flex items-center gap-3">
              {data !== null && data.keys.length > 0 && (
                <span className="hidden text-sm whitespace-nowrap text-on-surface-variant sm:inline">
                  traffic over {data.windowDays}d
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
          Keys
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
        ) : data === null || data.keys.length === 0 ? (
          <Empty>No API keys yet. Create one above and give it to a client.</Empty>
        ) : (
          <Table
            head={[
              'Name',
              'Key',
              'Requests',
              'Tokens',
              'Subscription',
              'Last used',
              'Limit',
              'Tokens/day',
              '',
            ]}
          >
            {data.keys.map((k) => (
              <tr
                key={k.id}
                onClick={openRow(keyHref(k.id))}
                className="cursor-pointer border-b border-outline-variant last:border-0 hover:bg-surface-container"
              >
                <td className="px-2 py-3">
                  <AppLink href={keyHref(k.id)} className="text-on-surface">
                    {k.name}
                  </AppLink>
                  <div className="text-xs text-on-surface-variant">created {ago(k.created_at)}</div>
                </td>
                <td className="px-2 py-3 font-mono text-xs text-on-surface-variant">{k.display}</td>
                <td className="px-2 py-3 tabular-nums">{compact(k.requests)}</td>
                <td className="px-2 py-3 tabular-nums">{compact(k.tokens)}</td>
                <td className="px-2 py-3">
                  <SubscriptionCell share={k.subscription} />
                </td>
                <td className="px-2 py-3 text-on-surface-variant">{ago(k.last_used_at)}</td>
                <td className="px-2 py-3 text-on-surface-variant">
                  {k.rpm_limit > 0
                    ? `${compact(k.rpm_limit)} / ${periodName(k.rate_period_s)}`
                    : 'default'}
                </td>
                <td className="px-2 py-3">
                  <Budget budget={k.token_budget} spent={k.spent_today} />
                </td>
                <td className="px-2 py-3 text-right">
                  <span aria-hidden className="inline-grid size-9 place-items-center text-on-surface-variant">
                    <svg viewBox="0 0 24 24" className="size-5 fill-current">
                      <path d="M8.6 16.6 13.2 12 8.6 7.4 10 6l6 6-6 6z" />
                    </svg>
                  </span>
                </td>
              </tr>
            ))}
          </Table>
        )}

        <p className="mt-4 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          Open a key to change its limits, delete it, or see what it spent and on which accounts.{' '}
          {SUBSCRIPTION_NOTE}
        </p>
      </Card>
    </div>
  )
}

/** What the subscription figures are, said once wherever they appear. */
const SUBSCRIPTION_NOTE =
  'Subscription is an estimate: each account’s 5-hour and 7-day utilisation, split ' +
  'between keys by their cost-weighted share of what this gateway sent it, as a share of ' +
  'every account’s capacity together. Anything else using an account is counted against ' +
  'the keys that used it here.'

function pct(v: number): string {
  if (v > 0 && v < 0.1) return '<0.1%'
  return `${v < 10 ? v.toFixed(1) : Math.round(v)}%`
}

/** A key's estimated part of the subscription, both windows, for a table cell. */
function SubscriptionCell({ share }: { share: SubscriptionShare | undefined }) {
  if (share === undefined) return <span className="text-on-surface-variant">—</span>
  return (
    <span className="whitespace-nowrap text-xs tabular-nums text-on-surface">
      <span className="text-on-surface-variant">5h</span> {pct(share.five_hour)}
      <span className="mx-1.5 text-on-surface-variant">·</span>
      <span className="text-on-surface-variant">7d</span> {pct(share.seven_day)}
    </span>
  )
}

/**
 * The same estimate on a key's page, account by account: how many points of
 * each account's windows this key accounts for, and its share of what the
 * gateway sent that account.
 */
function SubscriptionCard({ share }: { share: SubscriptionShare }) {
  return (
    <Card>
      <CardTitle>Share of the Claude subscription</CardTitle>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat label="5-hour" value={pct(share.five_hour)} hint="of all accounts" />
        <Stat label="7-day" value={pct(share.seven_day)} hint="of all accounts" />
      </div>
      {share.accounts.length > 0 && (
        <div className="mt-4">
          <Table head={['Account', '5-hour window', '7-day window']}>
            {share.accounts.map((a) => (
              <tr key={a.account_id} className="border-b border-outline-variant last:border-0">
                <td className="px-2 py-2">
                  <AppLink href={accountHref(a.account_id)} className="text-on-surface">
                    {a.email}
                  </AppLink>
                </td>
                <td className="px-2 py-2 tabular-nums">
                  {pct(a.five_hour)}{' '}
                  <span className="text-xs text-on-surface-variant">
                    ({Math.round(a.five_hour_share * 100)}% of its traffic here)
                  </span>
                </td>
                <td className="px-2 py-2 tabular-nums">
                  {pct(a.seven_day)}{' '}
                  <span className="text-xs text-on-surface-variant">
                    ({Math.round(a.seven_day_share * 100)}% of its traffic here)
                  </span>
                </td>
              </tr>
            ))}
          </Table>
        </div>
      )}
      <p className="mt-4 mb-0 text-xs text-on-surface-variant">{SUBSCRIPTION_NOTE}</p>
    </Card>
  )
}

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
  section: path,
  onExpired,
  onMinted,
}: {
  id: string
  section: string
  onExpired: () => void
  /** Shows a new secret and its client configs, as for a key just created. */
  onMinted: (m: { key: ApiKey; plaintext: string }) => void
}) {
  const section = detailSection(path)
  // Not on the settings page: the form is filled from the loaded key, and a
  // poll landing mid-edit would reset the fields under the cursor.
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    () => api.listKeys(),
    onExpired,
    [],
    section === 'settings' ? 0 : REFRESH_MS,
  )
  const k = data?.keys.find((key) => key.id === id)

  if (loading && data === null) {
    return (
      <p className="flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (k === undefined) {
    return (
      <div className="flex flex-col gap-5">
        {error !== '' && <Banner tone="error">{error}</Banner>}
        <Empty>
          No such key — it may have been deleted. What it spent is still in the request log, under
          the name it was recorded with.
        </Empty>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-5">
      <DetailHeader
        back="/keys"
        backLabel="All keys"
        title={k.name}
        subtitle={
          <>
            <code className="font-mono text-xs">{k.display}</code> · created {ago(k.created_at)} ·
            last used {ago(k.last_used_at)}
          </>
        }
        aside={
          <span className="flex flex-wrap items-center gap-3 text-xs text-on-surface">
            {k.managed && <Chip tone="neutral">gateway&rsquo;s own</Chip>}
            {k.models_off.length > 0 && (
              <Chip tone="warn">
                {k.models_off.length} model{k.models_off.length === 1 ? '' : 's'} off
              </Chip>
            )}
            <span>
              <span className="text-on-surface-variant">Rate</span>{' '}
              {k.rpm_limit > 0
                ? `${compact(k.rpm_limit)} / ${periodName(k.rate_period_s)}`
                : 'default'}
            </span>
            <span>
              <span className="text-on-surface-variant">Today</span>{' '}
              <Budget budget={k.token_budget} spent={k.spent_today} />
            </span>
            {k.subscription !== undefined && <SubscriptionCell share={k.subscription} />}
          </span>
        }
        base={keyHref(k.id)}
        section={section}
        freshness={{ updatedAt, refreshing, live, onRefresh: () => void reload() }}
      />

      {error !== '' && <Banner tone="error">{error}</Banner>}

      {section === 'settings' ? (
        <KeySettings
          apiKey={k}
          defaultRpm={data?.defaultRpm ?? 0}
          onSaved={() => void reload()}
          onMinted={onMinted}
        />
      ) : (
        <>
          {k.subscription !== undefined && <SubscriptionCard share={k.subscription} />}
          <ScopedUsage scope={{ key: k.id }} breakdown="account" onExpired={onExpired} />
        </>
      )}
    </div>
  )
}

/**
 * A key's settings page: the models it may ask for, its name and limits in a
 * form, and deleting it apart from them, where it cannot be hit while saving
 * a limit.
 */
function KeySettings({
  apiKey: k,
  defaultRpm,
  onSaved,
  onMinted,
}: {
  apiKey: ApiKey
  defaultRpm: number
  onSaved: () => void
  onMinted: (m: { key: ApiKey; plaintext: string }) => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  if (k.managed) {
    return (
      <Card>
        <CardTitle>Settings</CardTitle>
        <p className="m-0 text-sm text-on-surface-variant">{MANAGED_HINT}</p>
      </Card>
    )
  }

  const save = async (name: string, rpm: number, periodS: number, tokenBudget: number) => {
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      await api.updateKey(k.id, name, rpm, periodS, tokenBudget)
      setSaved(true)
      onSaved()
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy(false)
  }

  const show = async () => {
    setBusy(true)
    setError('')
    try {
      const { plaintext } = await api.keySecret(k.id)
      onMinted({ key: k, plaintext })
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy(false)
  }

  const rotate = async () => {
    if (
      !window.confirm(
        `Issue a new secret for ${k.name}? The current one stops working immediately, ` +
          'so every client using it needs the new config.',
      )
    )
      return
    setBusy(true)
    setError('')
    try {
      onMinted(await api.rotateKey(k.id))
      onSaved()
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy(false)
  }

  const remove = async () => {
    if (!window.confirm(`Delete ${k.name}? Clients using it stop working immediately.`)) return
    setBusy(true)
    setError('')
    try {
      await api.deleteKey(k.id)
      navigate('/keys')
    } catch (err) {
      setError(messageOf(err))
      setBusy(false)
    }
  }

  return (
    <>
      <Card>
        <CardTitle>Models</CardTitle>
        <KeyModels apiKey={k} onChanged={onSaved} />
      </Card>

      <Card>
        <CardTitle>Virtual models by app</CardTitle>
        <KeyVirtualModels apiKey={k} />
      </Card>

      <Card>
        <CardTitle>Name and limits</CardTitle>
        <KeyEditor apiKey={k} defaultRpm={defaultRpm} busy={busy} onSave={save} />
        {error !== '' && (
          <Banner tone="error" className="mt-4">
            {error}
          </Banner>
        )}
        {saved && error === '' && (
          <p className="mt-3 mb-0 text-sm text-on-surface-variant">Saved.</p>
        )}
      </Card>

      {/* The secret is kept sealed beside the account tokens (migration 026),
          so the configs can be written again at any time. Keys from before
          that have only a hash, and need a new secret once. */}
      <Card>
        <CardTitle>Client configurations</CardTitle>
        {k.secret_kept ? (
          <>
            <p className="mt-0 mb-4 text-sm text-on-surface-variant">
              The key, and ready-made configs for Claude Code, Codex, opencode and crush with it
              inside. Nothing changes for the clients already using it.
            </p>
            <div className="flex flex-wrap gap-2">
              <FilledButton type="button" disabled={busy} onClick={() => void show()}>
                {busy && <Spinner />}
                Show key and configs
              </FilledButton>
              <OutlinedButton disabled={busy} onClick={() => void rotate()}>
                Issue a new secret
              </OutlinedButton>
            </div>
            <p className="mt-3 mb-0 text-xs text-on-surface-variant">
              A new secret is for a key that has leaked: the current one stops working at once,
              and every client needs the new config.
            </p>
          </>
        ) : (
          <>
            <p className="mt-0 mb-4 text-sm text-on-surface-variant">
              This key was made before the gateway kept keys, so only a fingerprint of it exists
              and it cannot be shown. Issue it a new secret once — it keeps its name, limits,
              models and history; the current secret stops working — and from then on its configs
              can be downloaded here at any time.
            </p>
            <TonalButton disabled={busy} onClick={() => void rotate()}>
              {busy && <Spinner />}
              New secret and configs
            </TonalButton>
          </>
        )}
      </Card>

      <Card>
        <CardTitle>Delete this key</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          Clients holding it stop working immediately, and it cannot be undone. Its usage stays,
          under the name it was recorded with.
        </p>
        <OutlinedButton tone="error" disabled={busy} onClick={() => void remove()}>
          Delete {k.name}
        </OutlinedButton>
      </Card>
    </>
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

function KeyEditor({
  apiKey,
  defaultRpm,
  busy,
  onSave,
}: {
  apiKey: ApiKey
  defaultRpm: number
  busy: boolean
  onSave: (name: string, rpmLimit: number, periodS: number, tokenBudget: number) => void
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

        <div className="flex items-center justify-end gap-2">
          <OutlinedButton
            onClick={() => {
              setName(apiKey.name)
              setRpm(apiKey.rpm_limit)
              setPeriodS(apiKey.rate_period_s || DEFAULT_PERIOD_S)
              setBudget(apiKey.token_budget)
            }}
            disabled={busy || !changed}
          >
            Undo changes
          </OutlinedButton>
          <FilledButton
            type="button"
            disabled={busy || !valid || !changed}
            onClick={() => onSave(name.trim(), rpm, periodS, budget)}
          >
            {busy && <Spinner />}
            {busy ? 'Saving…' : 'Save'}
          </FilledButton>
        </div>
    </div>
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
