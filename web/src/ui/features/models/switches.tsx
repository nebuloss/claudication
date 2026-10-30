import { useMemo, useState, type ReactNode } from 'react'
import { api, type Account, type AccountModel, type ApiKey, type ModelTest, type ProbeResult } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import {
  Actions,
  Button,
  Chip,
  ErrorBanner,
  ErrorState,
  Loading,
  Note,
  SearchField,
  CheckTree,
  Stack,
  Tip,
  shownLeaves,
  useConfirm,
  type CheckNode,
} from '../../primitives'
import { compact, monthYear, relative, timeOfDay } from '../../format'
import { brief, eachBounded, explain, failing, sourceOf, testOf } from '../../domain/model-tests'
import { changes, modelNodes, modelTree, newestOnly } from '../../domain/model-tree'
import { READS, type Topic } from '../../../api/live'

/** How many model tests run at once: quick for a dozen models, and no burst. */
const TESTS_AT_ONCE = 2

/**
 * The facts' columns on a wide list — id, size, release. Fixed, because each
 * row is its own grid and only fixed widths line up down the list; sized for
 * the longest of each the providers list (claude-sonnet-4-5-20250929,
 * "1.0M in · 128k out", "Sep 2026").
 */
const COLUMNS = '11.5rem 7rem 4rem'

/** One write of a batch: this model in or out. */
type Change = { model: AccountModel; enabled: boolean }

/**
 * A checklist of models: what an account may serve, or what a client may be
 * bound to (in direct mode, may name). One component for both, because the
 * rule is one rule — kept as what is off, matched by store.ModelKey so a
 * dated id and its short form are one box, and new models arrive on — and
 * the account's list is the same widget over that account's models alone.
 *
 * With `test`, each model can be tried on the spot — one tiny turn on the
 * account's own credential, no client's key, nothing in the request log — and
 * its last known state stays at the end of its row: the last Test, the
 * gateway's own check (every model when the account is added, a failing one
 * again every quarter of an hour), or the last client request it served or
 * was refused for (internal/service/availability). A failing model is routed
 * around by the gateway already; switching it off — never automatic, since
 * the gateway goes on checking it and an operator decides — takes it out for
 * good, from here in one go.
 *
 * Folded into provider › family › model (domain/model-tree builds the
 * nodes, primitives/check-tree draws them), so a list that gains a model per
 * release reads as the few families it is, with the older ones folded out of
 * the way; folds are remembered per list (`folds`) in this browser. Above it, the quick ways through a long list: a search, All and
 * None, and Newest only.
 *
 * A box moves at once, then the server is told; on a failure it moves back
 * and says why. Anything that sets several models — a group's box, a
 * preset — is the same per-model call, one after another under one busy
 * state: the API takes a model per call, the list is a dozen or two, and a
 * failure stops the rest and puts their boxes back.
 */
function ModelChecklist({
  load,
  set,
  test,
  loadingText,
  explain: say,
  onChanged,
  deps,
  topics,
  folds,
}: {
  load: (signal: AbortSignal) => Promise<{ models: AccountModel[]; scanning?: boolean }>
  set: (model: string, enabled: boolean) => Promise<unknown>
  test?: (model: string) => Promise<ProbeResult>
  loadingText: string
  explain: (on: number, total: number) => ReactNode
  onChanged: () => void
  deps: unknown[]
  /** What the list is read from, to read it again when it changes. */
  topics: readonly Topic[]
  /** Which list this is, for remembering its folds. */
  folds: string
}) {
  // Read again as each check lands, and as a model is switched anywhere.
  const { data, error, loading, reload } = useLoader(load, { deps, on: topics })
  const tree = useMemo(() => modelTree(data?.models ?? []), [data])
  const [query, setQuery] = useState('')
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
  const filtering = query.trim() !== ''
  const byId = new Map(data.models.map((m) => [m.id, m]))
  // Writing anything holds every box still, so two batches never interleave;
  // a test running does not, since it changes nothing.
  const writing = busy !== '' && busy !== 'test-all'

  const apply = (key: string, batch: readonly Change[]) =>
    run(key, async () => {
      setFlipped((f) => ({ ...f, ...Object.fromEntries(batch.map((c) => [c.model.id, c.enabled])) }))
      let done = 0
      try {
        for (const c of batch) {
          await set(c.model.id, c.enabled)
          done++
        }
      } catch (err) {
        // What was not written goes back to where it was; what was, stays.
        setFlipped((f) => ({ ...f, ...Object.fromEntries(batch.slice(done).map((c) => [c.model.id, !c.enabled])) }))
        throw err
      } finally {
        if (done > 0) onChanged()
      }
    })
  const setAll = (key: string, models: readonly AccountModel[], enabled: boolean) =>
    void apply(key, changes(models, on, () => enabled))
  const newest = newestOnly(tree)
  const toNewest = changes(data.models, on, (m) => newest.get(m.id))

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
      action: () => apply('failing', changes(data.models, on, (m) => (failed.includes(m.id) ? false : undefined))),
    })

  // What a model's row says. The tree places it, boxes its groups and counts
  // them; none of that is worked out here.
  const leaf = (m: AccountModel): CheckNode => {
    const t = lastTest(m)
    const name = m.display_name ?? m.id
    return {
      key: m.id,
      label: name,
      search: m.id,
      name: (
        <>
          {name}
          {!m.listed && <span className="ml-2 text-xs text-warning">no longer listed upstream</span>}
          {m.auto_off === true && !on(m) && (
            <Tip tip="It kept failing on this account, so the gateway switched it off. It is still checked every 15 minutes and switched back on the first time it works. Switching it on yourself makes it yours again.">
              <span className="ml-2 text-xs text-warning">switched off automatically</span>
            </Tip>
          )}
        </>
      ),
      checked: on(m),
      facts: [
        <Tip tip={m.id}>
          <code>{m.id}</code>
        </Tip>,
        m.max_input_tokens !== undefined ? (
          <span className="tabular-nums">
            {compact(m.max_input_tokens)} in{m.max_tokens !== undefined && ` · ${compact(m.max_tokens)} out`}
          </span>
        ) : null,
        monthYear(m.created_at) !== '' ? <span>{monthYear(m.created_at)}</span> : null,
      ],
      end:
        test === undefined ? undefined : (
          <>
            {t !== undefined && (
              <Tip
                tip={
                  `${explain(t)}${t.error !== undefined && t.error !== explain(t) ? `\n${t.error}` : ''}` +
                  `\n${sourceOf(t)}, ${timeOfDay(t.at)}` +
                  (!t.ok && t.ok_at !== undefined ? `\nLast worked ${relative(t.ok_at)}` : '')
                }
              >
                <span className="min-w-0 truncate">
                  <Chip tone={t.ok ? 'ok' : 'error'}>{brief(t)}</Chip>
                </span>
              </Tip>
            )}
            {m.listed && (
              <Button variant="text" size="sm" icon="play" busy={testing[m.id] === true} onClick={() => void testOne(m.id)}>
                Test
              </Button>
            )}
          </>
        ),
    }
  }
  const nodes = modelNodes(tree, leaf)
  const visible = shownLeaves(nodes, query).flatMap((id) => byId.get(id) ?? [])
  // A box reports the models it stands for: one, or every one under a group.
  const onBoxes = (ids: string[], enabled: boolean) =>
    setAll(ids.length === 1 ? (ids[0] ?? 'group') : 'group', ids.flatMap((id) => byId.get(id) ?? []), enabled)

  return (
    <Stack gap={3}>
      <p className="max-w-[80ch] text-sm text-on-surface">{say(count, data.models.length)}</p>
      {data.scanning === true && (
        <Note>Checking each model on this account — one short request each. Results appear as they come in.</Note>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <SearchField value={query} onChange={setQuery} label="Find a model" />
        <Actions>
          {/* Over what the search shows while there is one, so "find the
              Haikus, None" is two steps; and said so on the button. */}
          <Button variant="outlined" size="sm" busy={busy === 'all'} disabled={busy !== '' || visible.every(on)}
            onClick={() => setAll('all', visible, true)}>
            {filtering ? 'All shown' : 'All'}
          </Button>
          <Button variant="outlined" size="sm" busy={busy === 'none'} disabled={busy !== '' || !visible.some(on)}
            onClick={() => setAll('none', visible, false)}>
            {filtering ? 'None shown' : 'None'}
          </Button>
          {!filtering && (
            <Button variant="outlined" size="sm" busy={busy === 'newest'} disabled={busy !== '' || toNewest.length === 0}
              title="The newest model of each family on, every older one off"
              onClick={() => void apply('newest', toNewest)}>
              Newest only
            </Button>
          )}
        </Actions>
        {test !== undefined && (
          <Actions>
            <Button variant="tonal" size="sm" onClick={() => void testAll()} busy={busy === 'test-all'}
              disabled={busy !== ''}>
              Test all
            </Button>
            {failed.length > 0 && (
              <Button variant="outlined" size="sm" tone="error" busy={busy === 'failing'}
                onClick={() => void switchOffFailing()} disabled={busy !== ''}>
                Switch off failing
              </Button>
            )}
          </Actions>
        )}
      </div>
      <ErrorBanner error={failure} />
      <CheckTree label="Models" nodes={nodes} onChange={onBoxes} columns={COLUMNS} storageKey={folds}
        filter={query} disabled={writing} />
    </Stack>
  )
}

/**
 * The models one account serves, each with a box and a test.
 *
 * Read from its provider with this account's own token, because subscriptions
 * differ in what they serve and a box only means something over a model the
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
    <ModelChecklist
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
      topics={READS.accountModels}
      folds="accounts/models"
    />
  )
}

/**
 * The models a client is allowed: every model available to it, each with a
 * box. Unchecked is what its names may not be bound to — by the operator, or
 * by its agent through the MCP server — and, in a custom client's direct
 * mode, what it may not name; refused for this client alone, before anything
 * reaches an account, and left out of every list it is given.
 */
export function KeyModels({ apiKey, onChanged }: { apiKey: ApiKey; onChanged: () => void }) {
  return (
    <ModelChecklist
      load={(signal) => api.clients.models(apiKey.id, signal)}
      set={(model, enabled) => api.clients.setModel(apiKey.id, model, enabled)}
      loadingText="Reading the models this gateway serves…"
      explain={(on, total) => `${on} of ${total} allowed. New models arrive allowed.`}
      onChanged={onChanged}
      deps={[apiKey.id]}
      topics={READS.keyModels}
      folds="clients/models"
    />
  )
}
