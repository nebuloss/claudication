import { useState } from 'react'
import {
  api,
  type ApiKey,
  type GatewayConfig,
  type KeyLimits,
  type SubscriptionShare,
} from '../../api/client'
import { useAction, useLoader } from '../hooks'
import { navigate, pathOf } from '../route'
import ScopedUsage, { accountHref, keyHref } from './usage'
import { DetailHeader } from './detail'
import { ChatsPage } from './chats'
import RequestsPanel from './requests'
import KeyClients, { KeySecret } from './key-clients'
import { KeyModels } from './account-models'
import { RolesEditor } from './model-roles'
import type { ReactNode } from 'react'
import {
  Actions,
  AddButton,
  AppLink,
  Button,
  Card,
  Chip,
  CopyField,
  DangerZone,
  Empty,
  ErrorBanner,
  Field,
  Figure,
  Figures,
  Freshness,
  ListRow,
  ListSummary,
  ListToolbar,
  Loading,
  Modal,
  Muted,
  Note,
  Overline,
  Page,
  RowBadge,
  Segmented,
  Stack,
  Stat,
  StatGrid,
  StepSlider,
  Switch,
  Table,
  Td,
  Tile,
  Tr,
  useConfirm,
} from '../primitives'
import { ago, compact, smallPercent } from '../format'

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
 * 026) so its Clients page can show it and its configs again. Keys from
 * before that have only the hash, and say so there.
 */
export default function Keys() {
  const [creating, setCreating] = useState(false)
  // The key just made, shown once over the list: its secret, and the way to
  // its Clients page, where its configurations are.
  const [created, setCreated] = useState<{ key: ApiKey; plaintext: string } | null>(null)
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.keys.list(signal),
    { refreshMs: REFRESH_MS },
  )

  return (
    <Page>
      <ListToolbar
        action={
          <AddButton disabled={creating} onClick={() => setCreating(true)}>
            New API key
          </AddButton>
        }
      >
        {data !== null && data.keys.length > 0 && (
          <ListSummary>traffic over {data.windowDays}d</ListSummary>
        )}
        <Freshness
          updatedAt={updatedAt}
          refreshing={refreshing}
          live={live}
          onRefresh={() => void reload()}
        />
      </ListToolbar>

      {created !== null && (
        <Modal title={`${created.key.name} is ready`} onClose={() => setCreated(null)}>
          <Stack>
            <CopyField label="API key" value={created.plaintext} secret />
            <Note>
              Every client&rsquo;s configuration, with this key already inside, is on its Clients
              page — and stays there, so it can be fetched again whenever a client is set up.
            </Note>
            <Actions end>
              <Button variant="outlined" onClick={() => setCreated(null)}>
                Done
              </Button>
              <Button
                onClick={() => {
                  const to = `${pathOf('keys', created.key.id)}/clients`
                  setCreated(null)
                  navigate(to)
                }}
              >
                Open its clients
              </Button>
            </Actions>
          </Stack>
        </Modal>
      )}

      {creating && (
        <NewKey
          defaultRpm={data?.defaultRpm ?? 0}
          onClose={() => setCreating(false)}
          onCreated={(m) => {
            setCreating(false)
            setCreated(m)
            void reload()
          }}
        />
      )}

      <Card
        info={
          <>
            <p>
              Open a key to change its limits, delete it, or see what it spent and on which
              accounts.
            </p>
            <p>{SUBSCRIPTION_NOTE}</p>
          </>
        }
      >
        <ErrorBanner error={error} className="mb-4" />
        {loading ? (
          <Loading />
        ) : data === null || data.keys.length === 0 ? (
          <Empty>No API keys yet. Create one above and give it to a client.</Empty>
        ) : (
          <Stack gap={2} as="ul">
            {data.keys.map((k) => (
              <li key={k.id}>
                <ListRow
                  href={keyHref(k.id)}
                  leading={
                    <RowBadge>
                      <svg viewBox="0 0 24 24" aria-hidden className="size-4 fill-current">
                        <path d="M12.65 10A6 6 0 1 0 12.65 14H17v4h4v-4h2v-4H12.65zM7 14a2 2 0 1 1 0-4 2 2 0 0 1 0 4z" />
                      </svg>
                    </RowBadge>
                  }
                  title={k.name}
                  subtitle={
                    <>
                      <span className="font-mono">{k.display}</span> · {compact(k.requests)} requests ·{' '}
                      {compact(k.tokens)} tokens over {data.windowDays}d · used {ago(k.last_used_at)}
                    </>
                  }
                  chips={k.managed || k.models_off.length > 0 ? <KeyChips k={k} /> : undefined}
                  meters={<KeyFigures k={k} />}
                />
              </li>
            ))}
          </Stack>
        )}
      </Card>
    </Page>
  )
}

/** What sets a key apart, as chips: the gateway's own, or models switched off. */
function KeyChips({ k }: { k: ApiKey }) {
  return (
    <>
      {k.managed && <Chip tone="neutral">gateway&rsquo;s own</Chip>}
      {k.models_off.length > 0 && (
        <Chip tone="warn">
          {k.models_off.length} model{k.models_off.length === 1 ? '' : 's'} off
        </Chip>
      )}
    </>
  )
}

/**
 * A key's limits and spend, as one row of figures: the same in the list and
 * on the key's own page, so the two cannot disagree about it.
 */
function KeyFigures({ k, always = true }: { k: ApiKey; always?: boolean }) {
  return (
    <>
      <Figure label="Rate">
        {k.rpm_limit > 0 ? `${compact(k.rpm_limit)} / ${periodName(k.rate_period_s)}` : 'default'}
      </Figure>
      <Figure label="Today">
        <Budget budget={k.token_budget} spent={k.spent_today} />
      </Figure>
      {/* A dash keeps the list's figures in step; alone in a page's header it
          would say nothing, so there it is left out. */}
      {(always || k.subscription !== undefined) && <SubscriptionCell share={k.subscription} />}
    </>
  )
}

/** What the subscription figures are, said once wherever they appear. */
const SUBSCRIPTION_NOTE =
  'Subscription is an estimate: each account’s 5-hour and 7-day utilisation, split ' +
  'between keys by their cost-weighted share of what this gateway sent it, as a share of ' +
  'every account’s capacity together. Anything else using an account is counted against ' +
  'the keys that used it here.'

/** A key's estimated part of the subscription, both windows, for a table cell. */
function SubscriptionCell({ share }: { share: SubscriptionShare | undefined }) {
  if (share === undefined) return <Muted as="span" size="xs">—</Muted>
  return (
    <span className="whitespace-nowrap text-xs tabular-nums text-on-surface">
      <span className="text-on-surface-variant">5h</span> {smallPercent(share.five_hour)}
      <span className="mx-1.5 text-on-surface-variant">·</span>
      <span className="text-on-surface-variant">7d</span> {smallPercent(share.seven_day)}
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
    <Card title="Share of the Claude subscription" info={<p>{SUBSCRIPTION_NOTE}</p>}>
      <StatGrid>
        <Stat label="5-hour" value={smallPercent(share.five_hour)} hint="of all accounts" />
        <Stat label="7-day" value={smallPercent(share.seven_day)} hint="of all accounts" />
      </StatGrid>
      {share.accounts.length > 0 && (
        <div className="mt-4">
          <Table head={['Account', '5-hour window', '7-day window']}>
            {share.accounts.map((a) => (
              <Tr key={a.account_id}>
                <Td>
                  <AppLink href={accountHref(a.account_id)} strong>
                    {a.email}
                  </AppLink>
                </Td>
                <Td nums>
                  {smallPercent(a.five_hour)}{' '}
                  <Muted as="span" size="xs">
                    ({Math.round(a.five_hour_share * 100)}% of its traffic here)
                  </Muted>
                </Td>
                <Td nums>
                  {smallPercent(a.seven_day)}{' '}
                  <Muted as="span" size="xs">
                    ({Math.round(a.seven_day_share * 100)}% of its traffic here)
                  </Muted>
                </Td>
              </Tr>
            ))}
          </Table>
        </div>
      )}
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
  page,
  config,
}: {
  id: string
  /** An id from KEY_PAGES, or empty for the key's overview. */
  page: string
  /** The gateway's configuration, for the address and APIs its client files name. */
  config: GatewayConfig | null
}) {
  // Not on the settings page: the form is filled from the loaded key, and a
  // poll landing mid-edit would reset the fields under the cursor.
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.keys.list(signal),
    { refreshMs: SETTINGS_PAGES.has(page) ? 0 : REFRESH_MS },
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

      {SETTINGS_PAGES.has(page) && (
        <KeySettings
          key={page}
          apiKey={k}
          section={page}
          defaultRpm={data?.defaultRpm ?? 0}
          config={config}
          onSaved={() => void reload()}
        />
      )}
      {page === 'chats' && <ChatsPage scope={{ key: k.id }} />}
      {page === 'requests' && <RequestsPanel scope={{ key: k.id }} />}
      {page === '' && (
        <>
          {!k.managed && <KeySecret apiKey={k} onChanged={() => void reload()} />}
          {/* The key at a glance, every figure a tile: what it did, what it
              may do, and when it was last used. The rest is a page away. */}
          <StatGrid columns={3}>
            <Stat label="Requests" value={compact(k.requests)} hint={`over ${data?.windowDays ?? 7}d`} />
            <Stat label="Tokens" value={compact(k.tokens)} hint={`over ${data?.windowDays ?? 7}d`} />
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
      )}
      {page === 'usage' && <ScopedUsage scope={{ key: k.id }} breakdown="account" />}
    </Page>
  )
}

/** The key's pages that change it, rather than report on it. */
const SETTINGS_PAGES = new Set(['clients', 'models', 'general', 'advanced'])

/**
 * A key's settings, a page each (/keys/<id>/clients, /models, /general, /advanced): the
 * models it may ask for, what its aliases serve, its name and limits, its
 * client configs, and deleting it on a page of its own, where it cannot be
 * hit while saving a limit.
 */
function KeySettings({
  apiKey: k,
  section,
  defaultRpm,
  config,
  onSaved,
}: {
  apiKey: ApiKey
  /** Which of the key's settings pages this is. */
  section: string
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
      default:
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
 * A key's daily token ceiling, and how close it is to it.
 *
 * "Unlimited" spelled out rather than shown as 0, which reads as the opposite
 * of what it means. When there is a budget the spend is shown against it, since
 * "is this key about to be cut off" is the question the column exists for.
 */
function Budget({ budget, spent }: { budget: number; spent: number }) {
  if (budget <= 0) return <Muted as="span" size="xs">unlimited</Muted>
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

function KeyEditor({
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

function NewKey({
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
