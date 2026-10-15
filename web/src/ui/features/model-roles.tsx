import { useState } from 'react'
import { api, messageOf, type ModelRole } from '../../api/client'
import { useLoader } from '../hooks'
import { Banner, Card, CardTitle, ErrorState, Spinner } from '../primitives'

/**
 * Model roles: claude-large and claude-small.
 *
 * Every config the gateway writes asks for these two names rather than a real
 * model — the model doing the work, and the one for titles and summaries, the
 * two slots every client has — so which real model answers is decided here,
 * once, instead of in every client. Bound for the whole gateway on the Models
 * tab (automatic follows the newest of the family), and per key in its
 * settings.
 */

const PURPOSE: Record<string, string> = {
  'claude-large': 'The model doing the work: the main model in every app, and its sub-agents.',
  'claude-small': 'Titles, summaries and other quick calls, run many times a session.',
}

const FAMILY: Record<string, string> = { opus: 'Opus', haiku: 'Haiku' }

/**
 * The two roles as a short form: for the gateway (keyId empty) or one key.
 */
export function RolesEditor({ keyId = '' }: { keyId?: string }) {
  const { data, error, loading, reload } = useLoader(() => api.modelRoles(keyId), undefined, [keyId], 0)
  const { data: models } = useLoader(() => api.models(), undefined, [], 0)
  const real = (models?.data ?? []).filter((m) => m.virtual !== true)
  const [busy, setBusy] = useState('')
  const [failure, setFailure] = useState('')

  if (loading && data === null) {
    return (
      <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '' || data === null) {
    return (
      <ErrorState
        message={error || 'Could not read the model roles.'}
        onRetry={() => void reload()}
        busy={loading}
      />
    )
  }

  const bind = async (r: ModelRole, model: string) => {
    setBusy(r.role)
    setFailure('')
    try {
      await api.setModelRole(keyId, r.role, model)
      await reload()
    } catch (err) {
      setFailure(messageOf(err))
    }
    setBusy('')
  }

  const unset = (r: ModelRole) =>
    keyId === ''
      ? `Automatic — the newest ${FAMILY[r.family] ?? r.family}`
      : 'Same as the gateway'

  return (
    <div className="flex flex-col gap-3">
      <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
        {data.roles.map((r) => (
          <li key={r.role} className="flex flex-wrap items-center gap-x-6 gap-y-2 py-3">
            <div className="min-w-0 flex-1">
              <code className="font-mono text-sm text-on-surface">{r.role}</code>
              <p className="mt-0.5 mb-0 text-xs text-on-surface-variant">{PURPOSE[r.role]}</p>
            </div>
            <div className="flex flex-col items-end gap-1">
              <select
                aria-label={`${r.role} model`}
                value={r.model}
                disabled={busy !== ''}
                onChange={(e) => void bind(r, e.target.value)}
                className="h-9 max-w-72 rounded-[var(--radius-md3-xs)] border border-outline bg-surface-lowest px-2 text-sm text-on-surface"
              >
                <option value="">{unset(r)}</option>
                {real.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.display_name ? `${m.display_name} — ${m.id}` : m.id}
                  </option>
                ))}
                {r.model !== '' && !real.some((m) => m.id === r.model) && (
                  <option value={r.model}>{r.model}</option>
                )}
              </select>
              <span
                className={`text-xs ${r.problem ? 'text-error' : 'text-on-surface-variant'}`}
              >
                {busy === r.role ? (
                  <Spinner className="size-3" />
                ) : r.problem ? (
                  r.problem
                ) : (
                  <>
                    serving <code className="font-mono">{r.serving}</code>
                  </>
                )}
              </span>
            </div>
          </li>
        ))}
      </ul>
      {failure !== '' && <Banner tone="error">{failure}</Banner>}
    </div>
  )
}

/** The Models tab: the gateway's bindings, which every key follows by default. */
export default function ModelsTab() {
  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardTitle>Model roles</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          Every client config this gateway hands out asks for <code>claude-large</code> and{' '}
          <code>claude-small</code> instead of a real model, so moving every client to a new model
          is one change here. Automatic follows the newest model of the family as your accounts
          list it. A key can bind either differently in its own settings.
        </p>
        <RolesEditor />
        <p className="mt-4 mb-0 text-xs text-on-surface-variant">
          Clients size their requests from the model a role served when their config was written.
          Moving a role to a model with a smaller context window means downloading the configs
          again from each key&rsquo;s settings.
        </p>
      </Card>
    </div>
  )
}
