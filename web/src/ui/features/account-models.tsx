import { useState, type ReactNode } from 'react'
import { api, messageOf, type Account, type AccountModel, type ApiKey } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, ErrorState, SmallButton, Spinner, Switch, compact } from '../primitives'

function released(at?: string): string {
  if (at === undefined) return ''
  const d = new Date(at)
  return Number.isNaN(d.getTime())
    ? ''
    : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short' })
}

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
  load: () => Promise<{ models: AccountModel[] }>
  set: (model: string, enabled: boolean) => Promise<unknown>
  loadingText: string
  explain: (on: number, total: number) => ReactNode
  onChanged: () => void
  deps: unknown[]
}) {
  const { data, error, loading, reload } = useLoader(load, undefined, deps, 0)
  // What was switched here since the list was read, so a switch moves at once
  // rather than after a round trip and a re-read.
  const [flipped, setFlipped] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState('')
  const [failure, setFailure] = useState('')

  if (loading && data === null) {
    return (
      <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> {loadingText}
      </p>
    )
  }
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
    setBusy(m.id)
    setFailure('')
    setFlipped((f) => ({ ...f, [m.id]: enabled }))
    try {
      await set(m.id, enabled)
      onChanged()
    } catch (err) {
      setFlipped((f) => ({ ...f, [m.id]: !enabled }))
      setFailure(messageOf(err))
    }
    setBusy('')
  }

  // Every model at once, one switch at a time: the API takes one model per
  // call, and the list is a dozen at most.
  const all = async (enabled: boolean) => {
    for (const m of data.models) {
      if (on(m) !== enabled) await toggle(m, enabled)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="m-0 text-xs text-on-surface-variant">{explain(count, data.models.length)}</p>
        <div className="flex gap-2">
          <SmallButton onClick={() => void all(true)} disabled={busy !== '' || count === data.models.length}>
            All on
          </SmallButton>
          <SmallButton onClick={() => void all(false)} disabled={busy !== '' || count === 0}>
            All off
          </SmallButton>
        </div>
      </div>
      {failure !== '' && <Banner tone="error">{failure}</Banner>}
      <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
        {data.models.map((m) => (
          <li key={m.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 py-2">
            <Switch
              checked={on(m)}
              onChange={(v) => void toggle(m, v)}
              label={m.display_name ?? m.id}
              disabled={busy !== ''}
            />
            <span className="flex flex-wrap items-baseline gap-x-3 text-xs text-on-surface-variant">
              <code className="font-mono">{m.id}</code>
              {m.max_input_tokens !== undefined && (
                <span className="tabular-nums">
                  {compact(m.max_input_tokens)} context
                  {m.max_tokens !== undefined && ` · ${compact(m.max_tokens)} out`}
                </span>
              )}
              {released(m.created_at) !== '' && <span>{released(m.created_at)}</span>}
              {!m.listed && <span className="text-warning">no longer listed upstream</span>}
            </span>
          </li>
        ))}
      </ul>
    </div>
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
      load={() => api.accountModels(account.id)}
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
      load={() => api.keyModels(apiKey.id)}
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
