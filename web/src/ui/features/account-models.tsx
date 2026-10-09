import { useState } from 'react'
import { api, messageOf, type Account, type AccountModel } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, ErrorState, Spinner, Switch, compact } from '../primitives'

function released(at?: string): string {
  if (at === undefined) return ''
  const d = new Date(at)
  return Number.isNaN(d.getTime())
    ? ''
    : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short' })
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
 *
 * Loaded when opened rather than with the account list, which re-reads itself
 * every few seconds: this is one upstream call per account, asked for when
 * someone wants to look.
 */
export default function AccountModels({
  account,
  onChanged,
}: {
  account: Account
  onChanged: () => void
}) {
  const { data, error, loading, reload } = useLoader(
    () => api.accountModels(account.id),
    undefined,
    [account.id],
    0,
  )
  // What was switched here since the list was read, so a switch moves at once
  // rather than after a round trip and a re-read.
  const [flipped, setFlipped] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState('')
  const [failure, setFailure] = useState('')

  if (loading && data === null) {
    return (
      <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Reading this account&rsquo;s models…
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
    try {
      await api.setAccountModel(account.id, m.id, enabled)
      setFlipped((f) => ({ ...f, [m.id]: enabled }))
      onChanged()
    } catch (err) {
      setFailure(messageOf(err))
    }
    setBusy('')
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="m-0 text-xs text-on-surface-variant">
        {count} of {data.models.length} on. A model off here is served by another account that has
        it on; off on every account, clients are told this gateway does not serve it. New models
        arrive on.
      </p>
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
