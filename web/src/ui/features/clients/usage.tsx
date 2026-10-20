import { useEffect, useState } from 'react'
import { api, type KeyLimits, type LimitReplay, type LimitSuggestion, type SlotUse } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import {
  Actions,
  Button,
  Card,
  ErrorBanner,
  Loading,
  Muted,
  Note,
  Stack,
  Stat,
  StatGrid,
  Table,
  Td,
  Tr,
  WindowPicker,
} from '../../primitives'
import { ColumnChart, MAGNITUDE, Stack as ChartStack } from '../../charts'
import { ago, compact } from '../../format'
import { providerTitle, type Client } from '../../clients/specs'
import { BudgetLimit, DEFAULT_PERIOD_S, RateLimit, periodName } from './limits'
import { Budget, SubscriptionCard } from './parts'

/** How often the live figures are re-read: the request log's own pace. */
const LIVE_MS = 5_000

/** The windows the breakdown can be read over, in days. */
const WINDOWS = [1, 7, 30] as const

/**
 * A client's usage, with its limits beside the figures they bound: its rate
 * beside its requests per minute, its daily budget beside its tokens today,
 * its share of each provider's subscription below; then what it spent, by
 * model name and the model each served.
 *
 * Each limit can be set from the client's own history — a preset read off its
 * busiest minutes and days — and is tried against its last week before it is
 * saved: what it would have refused, had it been in force.
 */
export function ClientUsage({ client: c, onChanged }: { client: Client; onChanged: () => void }) {
  const k = c.wire
  const live = useLoader((signal) => api.clients.live(c.id, signal), { refreshMs: LIVE_MS, deps: [c.id] })
  const suggest = useLoader((signal) => api.clients.suggest(c.id, signal), { deps: [c.id] })
  const [rpm, setRpm] = useState(k.rpm_limit)
  const [periodS, setPeriodS] = useState(k.rate_period_s || DEFAULT_PERIOD_S)
  const [budget, setBudget] = useState(k.token_budget)
  const limits: KeyLimits = { rpmLimit: rpm, ratePeriodS: periodS, tokenBudget: budget }
  const changed = rpm !== k.rpm_limit || periodS !== (k.rate_period_s || DEFAULT_PERIOD_S) || budget !== k.token_budget
  const replay = usePreview(c.id, limits, changed)
  const { busy, error, run } = useAction()

  const save = () =>
    run('save', async () => {
      await api.clients.update(c.id, c.name, limits)
      onChanged()
    })
  const preset = (s: LimitSuggestion) => {
    if (s.rate_per_minute > 0) {
      setRpm(s.rate_per_minute)
      setPeriodS(60)
    }
    if (s.daily_tokens > 0) setBudget(s.daily_tokens)
  }

  const l = live.data
  const minutes = l?.per_minute ?? []
  const chart = new ChartStack(
    minutes.map((n, i) => ({ i, n })),
    [{ key: 'requests', label: 'Requests', paint: MAGNITUDE, value: (d: { n: number }) => d.n }],
  )
  const rateCap = l === null ? 0 : l.rpm_limit > 0 ? l.rpm_limit : l.default_rpm

  return (
    <>
      <Card title="Requests" aside={<Muted as="span" size="xs">per minute, the last hour</Muted>}>
        {l === null ? (
          <Loading />
        ) : (
          <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
            <Stack gap={3}>
              <ColumnChart stack={chart} label={(d) => (d.i % 10 === 0 ? `−${60 - d.i}m` : '')} format={compact}
                height={160} describe={(d) => `${d.n} requests`} />
              <StatGrid columns={3}>
                <Stat label="In flight" value={compact(l.in_flight)} hint="open now" />
                <Stat label={`This ${periodName(l.period_s)}`} value={compact(l.in_period)}
                  hint={`of ${compact(rateCap)} allowed`}
                  tone={rateCap > 0 && l.in_period >= rateCap ? 'error' : undefined} />
                <Stat label="Last used" value={ago(k.last_used_at)} />
              </StatGrid>
            </Stack>
            <RateLimit defaultRpm={l.default_rpm} rpm={rpm} setRpm={setRpm} periodS={periodS} setPeriodS={setPeriodS} />
          </div>
        )}
      </Card>

      <Card title="Tokens" aside={<Muted as="span" size="xs">the last 24 hours</Muted>}>
        {l === null ? (
          <Loading />
        ) : (
          <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
            <StatGrid columns={3}>
              <Stat label="Today" value={k.token_budget > 0 ? <Budget budget={k.token_budget} spent={l.tokens_day} /> : compact(l.tokens_day)}
                hint={k.token_budget > 0 ? 'of its daily budget' : 'tokens, no daily budget'} />
              <Stat label="Budget" value={k.token_budget > 0 ? compact(k.token_budget) : 'none'}
                hint={`a rolling ${l.budget_hours} hours`} />
            </StatGrid>
            <BudgetLimit budget={budget} setBudget={setBudget} />
          </div>
        )}
        {l !== null && !l.history_recorded && (
          <Note tone="warn" className="mt-4">Usage history is off, so a budget is not enforced and nothing is suggested.</Note>
        )}
      </Card>

      <Card title="Limits">
        <Stack gap={3}>
          {suggest.data !== null && suggest.data.some((s) => s.active_days > 0) ? (
            <div className="flex flex-wrap items-center gap-3">
              <Muted as="span">From its own history:</Muted>
              {suggest.data.filter((s) => s.active_days > 0).map((s) => (
                <Button key={s.days} variant="outlined" size="sm" onClick={() => preset(s)}>
                  {s.days} days — {compact(s.rate_per_minute)}/min, {compact(s.daily_tokens)} tokens/day
                </Button>
              ))}
            </div>
          ) : (
            <Muted>Nothing to suggest from yet: this client has no history.</Muted>
          )}
          {suggest.data !== null && suggest.data.some((s) => s.active_days > 0) && (
            <Muted size="xs">
              A preset is the 95th percentile of its busy minutes with half again on top, and of its days with a
              quarter on top — its busiest minute was {compact(suggest.data[1]?.peak_per_minute ?? 0)} requests and
              its busiest day {compact(suggest.data[1]?.peak_daily ?? 0)} tokens over 30 days.
            </Muted>
          )}
          {changed && replay !== null && <ReplayNote replay={replay} />}
          <ErrorBanner error={error} />
          <Actions end>
            <Button variant="outlined" disabled={!changed || busy !== ''} onClick={() => {
              setRpm(k.rpm_limit)
              setPeriodS(k.rate_period_s || DEFAULT_PERIOD_S)
              setBudget(k.token_budget)
            }}>
              Undo changes
            </Button>
            <Button busy={busy === 'save'} disabled={!changed} onClick={() => void save()}>
              Save limits
            </Button>
          </Actions>
        </Stack>
      </Card>

      {k.subscription !== undefined && <SubscriptionCard share={k.subscription} />}
      <SlotBreakdown clientId={c.id} />
    </>
  )
}

/** What the limits being edited would have refused of the last week. */
function usePreview(id: string, limits: KeyLimits, changed: boolean): LimitReplay | null {
  const [replay, setReplay] = useState<LimitReplay | null>(null)
  const { rpmLimit, ratePeriodS, tokenBudget } = limits
  useEffect(() => {
    if (!changed) return
    let current = true
    const t = window.setTimeout(() => {
      api.clients
        .preview(id, { rpmLimit, ratePeriodS, tokenBudget })
        .then((r) => current && setReplay(r.replay))
        .catch(() => current && setReplay(null))
    }, 300)
    return () => {
      current = false
      window.clearTimeout(t)
    }
  }, [id, rpmLimit, ratePeriodS, tokenBudget, changed])
  return replay
}

function ReplayNote({ replay: r }: { replay: LimitReplay }) {
  const refused = r.rate_refused + r.budget_refused
  return (
    <Note tone={refused > 0 ? 'warn' : 'info'}>
      Over the last 7 days, these limits would have refused{' '}
      {refused === 0
        ? `none of its ${compact(r.requests)} requests.`
        : `${compact(r.rate_refused)} of its ${compact(r.requests)} requests for rate` +
          ` and ${compact(r.budget_refused)} for budget, on ${r.budget_days} day${r.budget_days === 1 ? '' : 's'}.`}
    </Note>
  )
}

/** What the client spent, by model name and the model each served. */
function SlotBreakdown({ clientId }: { clientId: string }) {
  const [days, setDays] = useState<number>(7)
  const { data, error, loading } = useLoader((signal) => api.clients.usage(clientId, days, signal), {
    deps: [clientId, days],
  })
  const rows: SlotUse[] = data?.rows ?? []
  return (
    <Card title="By model name" aside={<WindowPicker value={days} windows={WINDOWS} onChange={setDays} />}>
      <ErrorBanner error={error} />
      {loading && data === null ? (
        <Loading />
      ) : rows.length === 0 ? (
        <Muted>Nothing in this window.</Muted>
      ) : (
        <SlotTable rows={rows} />
      )}
    </Card>
  )
}

/** Rows of usage by name and model: a client's own, or one client's on an account. */
export function SlotTable({ rows }: { rows: SlotUse[] }) {
  return (
    <Table head={['Name', 'Model', 'Requests', 'Input', 'Output', 'Cache', 'Last']}>
      {rows.map((r) => (
        <Tr key={`${r.client_id}/${r.slot}/${r.model}/${r.provider}`}>
          <Td>
            <code>{r.slot || '—'}</code>
          </Td>
          <Td>
            <code>{r.model || '—'}</code>{' '}
            <Muted as="span" size="xs">
              {providerTitle(r.provider)}
            </Muted>
          </Td>
          <Td nums>
            {compact(r.requests)}
            {r.errors > 0 && (
              <Muted as="span" size="xs">
                {' '}
                ({compact(r.errors)} failed)
              </Muted>
            )}
          </Td>
          <Td nums>{compact(r.input_tokens)}</Td>
          <Td nums>
            {compact(r.output_tokens)}
            {r.reasoning_tokens > 0 && (
              <Muted as="span" size="xs">
                {' '}
                ({compact(r.reasoning_tokens)} reasoning)
              </Muted>
            )}
          </Td>
          <Td nums>{compact(r.cache_tokens)}</Td>
          <Td>{ago(r.last)}</Td>
        </Tr>
      ))}
    </Table>
  )
}
