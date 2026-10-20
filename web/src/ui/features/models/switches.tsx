import { useState, type ReactNode } from 'react'
import { api, type Account, type AccountModel, type ApiKey, type ModelTest, type ProbeResult } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import { Actions, Button, Chip, ErrorBanner, ErrorState, Loading, Stack, Switch, tile, useConfirm } from '../../primitives'
import { compact, monthYear, timeOfDay } from '../../format'
import { eachBounded, explain, failing, testOf } from '../../domain/model-tests'

/** How many model tests run at once: quick for a dozen models, and no burst. */
const TESTS_AT_ONCE = 2

/**
 * A list of models, each with a switch: what an account may serve, or what a
 * client in direct mode may ask for. One component for both, because the rule
 * is one rule — kept as what is off, matched by store.ModelKey so a dated id
 * and its short form are one switch, and new models arrive on.
 *
 * With `test`, each model can be tried on the spot — one tiny turn on the
 * account's own credential, no client's key, nothing in the request log — and
 * its last result, kept by the gateway, stays beside its switch. A model that
 * failed for a reason that lasts can be switched off from here in one go; never
 * automatically, because a test is one answer and an operator decides.
 *
 * A switch moves at once, then the server is told; on a failure it moves back
 * and says why.
 */
function ModelSwitches({
  load,
  set,
  test,
  loadingText,
  explain: say,
  onChanged,
  deps,
}: {
  load: (signal: AbortSignal) => Promise<{ models: AccountModel[] }>
  set: (model: string, enabled: boolean) => Promise<unknown>
  test?: (model: string) => Promise<ProbeResult>
  loadingText: string
  explain: (on: number, total: number) => ReactNode
  onChanged: () => void
  deps: unknown[]
}) {
  const { data, error, loading, reload } = useLoader(load, { deps })
  // What was switched or tested here since the list was read, so the row
  // moves at once rather than after a round trip and a re-read.
  const [flipped, setFlipped] = useState<Record<string, boolean>>({})
  const [tested, setTested] = useState<Record<string, ModelTest>>({})
  const [testing, setTesting] = useState<Record<string, boolean>>({})
  const { busy, error: failure, run } = useAction()
  const confirm = useConfirm()

  if (loading && data === null) return <Loading>{loadingText}</Loading>
  if (error !== '' || data === null) {
    return (
      <ErrorState message={error || 'Could not read the models.'} onRetry={() => void reload()} busy={loading} />
    )
  }

  const on = (m: AccountModel) => flipped[m.id] ?? m.enabled
  const lastTest = (m: AccountModel) => tested[m.id] ?? m.test
  const count = data.models.filter(on).length
  const failed = failing(data.models.map((m) => ({ id: m.id, enabled: on(m), test: lastTest(m) })))

  const toggle = async (m: AccountModel, enabled: boolean) => {
    setFlipped((f) => ({ ...f, [m.id]: enabled }))
    const ok = await run(m.id, async () => {
      await set(m.id, enabled)
      onChanged()
    })
    if (!ok) setFlipped((f) => ({ ...f, [m.id]: !enabled }))
  }

  // Every model at once, one switch at a time: the API takes one model per
  // call, and the list is a dozen at most.
  const all = async (enabled: boolean) => {
    for (const m of data.models) {
      if (on(m) !== enabled) await toggle(m, enabled)
    }
  }

  const testOne = async (id: string): Promise<void> => {
    if (test === undefined) return
    setTesting((t) => ({ ...t, [id]: true }))
    try {
      const p = await test(id)
      setTested((t) => ({ ...t, [id]: testOf(id, p) }))
    } catch (err) {
      setTested((t) => ({
        ...t,
        [id]: { model: id, at: new Date().toISOString(), ok: false, status: 0, latency_ms: 0, error: String(err) },
      }))
    } finally {
      setTesting((t) => ({ ...t, [id]: false }))
    }
  }
  const testAll = () =>
    run('test-all', () =>
      eachBounded(
        data.models.filter((m) => m.listed).map((m) => m.id),
        TESTS_AT_ONCE,
        testOne,
        () => {},
      ),
    )
  const switchOffFailing = () =>
    confirm({
      title: `Switch off ${failed.length} failing model${failed.length === 1 ? '' : 's'}?`,
      body: `Their last test failed: ${failed.join(', ')}. Another account with them on serves them instead.`,
      confirmLabel: 'Switch off',
      action: async () => {
        for (const id of failed) {
          const m = data.models.find((x) => x.id === id)
          if (m !== undefined) await toggle(m, false)
        }
      },
    })

  return (
    <Stack gap={3}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="max-w-[80ch] text-sm text-on-surface">{say(count, data.models.length)}</p>
        <Actions>
          {test !== undefined && (
            <Button variant="tonal" size="sm" onClick={() => void testAll()} busy={busy === 'test-all'}
              disabled={busy !== ''}>
              Test all
            </Button>
          )}
          {failed.length > 0 && (
            <Button variant="outlined" size="sm" tone="error" onClick={() => void switchOffFailing()}
              disabled={busy !== ''}>
              Switch off failing models
            </Button>
          )}
          <Button variant="outlined" size="sm" onClick={() => void all(true)}
            disabled={busy !== '' || count === data.models.length}>
            All on
          </Button>
          <Button variant="outlined" size="sm" onClick={() => void all(false)} disabled={busy !== '' || count === 0}>
            All off
          </Button>
        </Actions>
      </div>
      <ErrorBanner error={failure} />
      {/* Scrolls in its own box: an account lists a model per release, and a
          page that grows with them puts All on and All off a long scroll above
          the switch being looked for. */}
      <ul
        className={`${tile} m-0 flex max-h-[min(32rem,65vh)] list-none flex-col divide-y divide-outline-variant overflow-y-auto overscroll-contain px-4 py-1`}
      >
        {data.models.map((m) => {
          const t = lastTest(m)
          return (
            <li key={m.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2">
              <Switch checked={on(m)} onChange={(v) => void toggle(m, v)} label={m.display_name ?? m.id}
                disabled={busy !== '' && busy !== 'test-all'} />
              <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-on-surface-variant">
                <code>{m.id}</code>
                {m.max_input_tokens !== undefined && (
                  <span className="tabular-nums">
                    {compact(m.max_input_tokens)} context
                    {m.max_tokens !== undefined && ` · ${compact(m.max_tokens)} out`}
                  </span>
                )}
                {monthYear(m.created_at) !== '' && <span>{monthYear(m.created_at)}</span>}
                {!m.listed && <span className="text-warning">no longer listed upstream</span>}
                {t !== undefined && (
                  <span title={t.error ?? ''} className="inline-flex items-center gap-2">
                    <Chip tone={t.ok ? 'ok' : 'error'}>{explain(t)}</Chip> {timeOfDay(t.at)}
                  </span>
                )}
                {test !== undefined && m.listed && (
                  <Button variant="text" size="sm" busy={testing[m.id] === true} onClick={() => void testOne(m.id)}>
                    Test
                  </Button>
                )}
              </span>
            </li>
          )
        })}
      </ul>
    </Stack>
  )
}

/**
 * The models one account serves, each with a switch and a test.
 *
 * Read from its provider with this account's own token, because subscriptions
 * differ in what they serve and a switch only means something over a model the
 * account has. Off takes the model out of this account's rotation on the next
 * request: another account with it on serves instead, and with it off on every
 * account, clients are told the gateway does not serve it — and it leaves the
 * model lists and the configs the gateway hands out.
 */
export default function AccountModels({
  account,
  onChanged,
}: {
  account: Account
  onChanged: () => void
}) {
  return (
    <ModelSwitches
      load={(signal) => api.accounts.models(account.id, signal)}
      set={(model, enabled) => api.accounts.setModel(account.id, model, enabled)}
      test={(model) => api.accounts.test(account.id, model)}
      loadingText="Reading this account’s models…"
      explain={(on, total) =>
        `${on} of ${total} on. A model off here is served by another account that has it on; ` +
        'off on every account, clients are told this gateway does not serve it. New models arrive on.'
      }
      onChanged={onChanged}
      deps={[account.id]}
    />
  )
}

/**
 * The models a custom client in direct mode may ask for: every model the
 * gateway serves, each with a switch. Off is refused for this client alone,
 * before anything reaches an account, and left out of the model list it is
 * given.
 */
export function KeyModels({ apiKey, onChanged }: { apiKey: ApiKey; onChanged: () => void }) {
  return (
    <ModelSwitches
      load={(signal) => api.clients.models(apiKey.id, signal)}
      set={(model, enabled) => api.clients.setModel(apiKey.id, model, enabled)}
      loadingText="Reading the models this gateway serves…"
      explain={(on, total) =>
        `${on} of ${total} allowed. A model off here is refused for this client — every other client ` +
        'is unaffected — and left out of the list it is shown. New models arrive allowed.'
      }
      onChanged={onChanged}
      deps={[apiKey.id]}
    />
  )
}
