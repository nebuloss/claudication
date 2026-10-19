import { useState, type ReactNode } from 'react'
import { api, type Account, type AccountModel, type ApiKey } from '../../api/client'
import { useAction, useLoader } from '../hooks'
import { Actions, Button, ErrorBanner, ErrorState, Loading, Stack, Switch, tile } from '../primitives'
import { compact, monthYear } from '../format'

/**
 * A list of models, each with a switch: what an account may serve, or what a
 * key may ask for. One component for both, because the rule is one rule —
 * kept as what is off, matched by store.ModelKey so a dated id and its alias
 * are one switch, and new models arrive on.
 *
 * A switch moves at once, then the server is told; on a failure it moves back
 * and says why.
 */
function ModelSwitches({
  load,
  set,
  loadingText,
  explain,
  onChanged,
  deps,
}: {
  load: (signal: AbortSignal) => Promise<{ models: AccountModel[] }>
  set: (model: string, enabled: boolean) => Promise<unknown>
  loadingText: string
  explain: (on: number, total: number) => ReactNode
  onChanged: () => void
  deps: unknown[]
}) {
  const { data, error, loading, reload } = useLoader(load, { deps })
  // What was switched here since the list was read, so a switch moves at once
  // rather than after a round trip and a re-read.
  const [flipped, setFlipped] = useState<Record<string, boolean>>({})
  const { busy, error: failure, run } = useAction()

  if (loading && data === null) return <Loading>{loadingText}</Loading>
  if (error !== '' || data === null) {
    return (
      <ErrorState
        message={error || 'Could not read the models.'}
        onRetry={() => void reload()}
        busy={loading}
      />
    )
  }

  const on = (m: AccountModel) => flipped[m.id] ?? m.enabled
  const count = data.models.filter(on).length

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

  return (
    <Stack gap={3}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="max-w-[80ch] text-sm text-on-surface">{explain(count, data.models.length)}</p>
        <Actions>
          <Button
            variant="outlined"
            size="sm"
            onClick={() => void all(true)}
            disabled={busy !== '' || count === data.models.length}
          >
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
        className={`${tile} m-0 flex max-h-[min(28rem,60vh)] list-none flex-col divide-y divide-outline-variant overflow-y-auto overscroll-contain px-4 py-1`}
      >
        {data.models.map((m) => (
          <li key={m.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2">
            <Switch
              checked={on(m)}
              onChange={(v) => void toggle(m, v)}
              label={m.display_name ?? m.id}
              disabled={busy !== ''}
            />
            <span className="flex flex-wrap items-baseline gap-x-3 text-xs text-on-surface-variant">
              <code>{m.id}</code>
              {m.max_input_tokens !== undefined && (
                <span className="tabular-nums">
                  {compact(m.max_input_tokens)} context
                  {m.max_tokens !== undefined && ` · ${compact(m.max_tokens)} out`}
                </span>
              )}
              {monthYear(m.created_at) !== '' && <span>{monthYear(m.created_at)}</span>}
              {!m.listed && <span className="text-warning">no longer listed upstream</span>}
            </span>
          </li>
        ))}
      </ul>
    </Stack>
  )
}

/**
 * The models one account serves, each with a switch.
 *
 * Read from Anthropic with this account's own token, because subscriptions
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
      load={(signal) => api.accountModels(account.id, signal)}
      set={(model, enabled) => api.setAccountModel(account.id, model, enabled)}
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
 * The models one key may ask for: every model the gateway serves, each with a
 * switch. Off is refused for this key alone, before anything reaches an
 * account, and left out of the model list this key's clients are given.
 */
export function KeyModels({ apiKey, onChanged }: { apiKey: ApiKey; onChanged: () => void }) {
  return (
    <ModelSwitches
      load={(signal) => api.keyModels(apiKey.id, signal)}
      set={(model, enabled) => api.setKeyModel(apiKey.id, model, enabled)}
      loadingText="Reading the models this gateway serves…"
      explain={(on, total) =>
        `${on} of ${total} allowed. A model off here is refused for this key — every other key ` +
        'is unaffected — and left out of the list its clients are shown. New models arrive allowed.'
      }
      onChanged={onChanged}
      deps={[apiKey.id]}
    />
  )
}
