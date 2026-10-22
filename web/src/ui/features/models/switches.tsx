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
  SearchField,
  Stack,
  Tree,
  TreeGroup,
  TreeItem,
  useConfirm,
  useFolds,
} from '../../primitives'
import { compact, monthYear, timeOfDay } from '../../format'
import { brief, eachBounded, explain, failing, testOf } from '../../domain/model-tests'
import {
  changes,
  filterTree,
  groupState,
  modelTree,
  modelsOf,
  newestOnly,
  openByDefault,
} from '../../domain/model-tree'

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
 * its last result, kept by the gateway, stays at the end of its row. A model
 * that failed for a reason that lasts can be switched off from here in one
 * go; never automatically, because a test is one answer and an operator
 * decides.
 *
 * Folded into provider › family › model (domain/model-tree), so a list that
 * gains a model per release reads as the few families it is, with the older
 * ones folded out of the way; folds are remembered per list (`folds`) in this
 * browser. Above it, the quick ways through a long list: a search, All and
 * None, and Newest only.
 *
 * A box moves at once, then the server is told; on a failure it moves back
 * and says why. Anything that sets several models — a folded group's box, a
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
  folds,
}: {
  load: (signal: AbortSignal) => Promise<{ models: AccountModel[] }>
  set: (model: string, enabled: boolean) => Promise<unknown>
  test?: (model: string) => Promise<ProbeResult>
  loadingText: string
  explain: (on: number, total: number) => ReactNode
  onChanged: () => void
  deps: unknown[]
  /** Which list this is, for remembering its folds. */
  folds: string
}) {
  const { data, error, loading, reload } = useLoader(load, { deps })
  const tree = useMemo(() => modelTree(data?.models ?? []), [data])
  const defaults = useMemo(() => openByDefault(tree), [tree])
  const fold = useFolds(folds, defaults)
  const [query, setQuery] = useState('')
  const shown = useMemo(() => filterTree(tree, query), [tree, query])
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
  const visible = modelsOf(shown)
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
  const counted = (models: readonly AccountModel[]) => `${models.filter(on).length} of ${models.length}`

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

  const row = (m: AccountModel) => {
    const t = lastTest(m)
    const name = m.display_name ?? m.id
    return (
      <TreeItem
        key={m.id}
        level={2}
        label={name}
        name={
          <>
            {name}
            {!m.listed && <span className="ml-2 text-xs text-warning">no longer listed upstream</span>}
          </>
        }
        checked={on(m)}
        onChange={(v) => void apply(m.id, [{ model: m, enabled: v }])}
        disabled={writing}
        columns={COLUMNS}
        facts={[
          <code key="id" title={m.id}>
            {m.id}
          </code>,
          m.max_input_tokens !== undefined ? (
            <span key="size" className="tabular-nums">
              {compact(m.max_input_tokens)} in{m.max_tokens !== undefined && ` · ${compact(m.max_tokens)} out`}
            </span>
          ) : null,
          monthYear(m.created_at) !== '' ? <span key="date">{monthYear(m.created_at)}</span> : null,
        ]}
        end={
          test === undefined ? undefined : (
            <>
              {t !== undefined && (
                <span
                  className="min-w-0 truncate"
                  title={`${explain(t)}${t.error !== undefined && t.error !== explain(t) ? `\n${t.error}` : ''}\nTested at ${timeOfDay(t.at)}`}
                >
                  <Chip tone={t.ok ? 'ok' : 'error'}>{brief(t)}</Chip>
                </span>
              )}
              {m.listed && (
                <Button variant="text" size="sm" busy={testing[m.id] === true} onClick={() => void testOne(m.id)}>
                  Test
                </Button>
              )}
            </>
          )
        }
      />
    )
  }

  return (
    <Stack gap={3}>
      <p className="max-w-[80ch] text-sm text-on-surface">{say(count, data.models.length)}</p>
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
      <Tree label="Models">
        {shown.length === 0 && (
          <li className="list-none px-3 py-6 text-sm text-on-surface-variant">No model matches “{query.trim()}”.</li>
        )}
        {shown.map((p) => (
          // Open while searching, every group: a match folded away is a
          // match not found.
          <TreeGroup key={p.key} title={p.title} count={counted(modelsOf([p]))}
            open={filtering || fold.isOpen(p.key)} onToggle={filtering ? undefined : () => fold.toggle(p.key)}
            state={groupState(modelsOf([p]).map(on))} onSet={(v) => setAll(p.key, modelsOf([p]), v)}
            disabled={writing}>
            {p.families.map((f) => (
              <TreeGroup key={f.key} level={1} title={f.title} count={counted(f.models)}
                open={filtering || fold.isOpen(f.key)} onToggle={filtering ? undefined : () => fold.toggle(f.key)}
                state={groupState(f.models.map(on))} onSet={(v) => setAll(f.key, f.models, v)} disabled={writing}>
                {f.models.map(row)}
              </TreeGroup>
            ))}
          </TreeGroup>
        ))}
      </Tree>
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
      folds="clients/models"
    />
  )
}
