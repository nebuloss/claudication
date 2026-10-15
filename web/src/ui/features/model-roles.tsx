import { useState } from 'react'
import { api, messageOf, type Model, type ModelRole } from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  Card,
  ErrorState,
  Field,
  IconButton,
  Select,
  Spinner,
  TonalButton,
  type SelectOption,
} from '../primitives'

/**
 * Model aliases: names the gateway's configs ask for instead of real models.
 *
 * Two are roles, the two slots every client has — claude-large for the model
 * doing the work, claude-small for titles and summaries — and four are the
 * families, which is how Claude Code names its tiers. Which real model
 * answers each is decided here, once, instead of in every client: for the
 * whole gateway on the Models tab, and per key in its settings.
 */

const PURPOSE: Record<string, string> = {
  'claude-large': 'The model doing the work: every app’s main model, and its sub-agents.',
  'claude-small': 'Titles, summaries and other quick calls, run many times a session.',
  'claude-opus': 'Claude Code’s opus tier, and anything asking for Opus by name.',
  'claude-sonnet': 'Claude Code’s sonnet tier.',
  'claude-haiku': 'Claude Code’s haiku tier.',
  'claude-fable': 'Claude Code’s fable tier, and its “best”.',
}

/** A real model's family, from its id: claude-opus-5-5 is opus. */
function familyOf(id: string): string {
  return /^claude-([a-z]+)-/.exec(id)?.[1] ?? ''
}

/**
 * The models an alias can be bound to, its own family first, each in the
 * order the accounts list them — newest first. So the first entry is what an
 * alias that has not been bound serves.
 */
function choicesFor(family: string, real: Model[]): SelectOption[] {
  const own = real.filter((m) => familyOf(m.id) === family)
  const rest = real.filter((m) => familyOf(m.id) !== family)
  return [...own, ...rest].map((m) => ({
    value: m.id,
    label: m.display_name ?? m.id,
    hint: m.id,
  }))
}

/** The aliases as a short form: for the gateway (keyId empty) or one key. */
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
        message={error || 'Could not read the model aliases.'}
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

  const remove = async (r: ModelRole) => {
    if (!window.confirm(`Delete ${r.role}? Clients configured with it will be told no such model.`))
      return
    setBusy(r.role)
    setFailure('')
    try {
      await api.deleteAlias(r.role)
      await reload()
    } catch (err) {
      setFailure(messageOf(err))
    }
    setBusy('')
  }

  const row = (r: ModelRole) => {
          const choices = choicesFor(r.family, real)
          // A key's list starts with following the gateway; the gateway's
          // starts with the newest of the family, which is what it serves
          // until something else is chosen.
          const options =
            keyId === ''
              ? choices
              : [
                  {
                    value: '',
                    label: 'Same as the gateway',
                    hint: r.serving !== '' ? r.serving : undefined,
                  },
                  ...choices,
                ]
          const value = keyId === '' ? r.model || options[0]?.value || '' : r.model
          const purpose =
            PURPOSE[r.role] ??
            (r.builtin
              ? `The newest ${r.family} model, found from what your accounts serve.`
              : 'Your own alias.')
          return (
            <li key={r.role} className="flex flex-wrap items-center gap-x-6 gap-y-2 py-3">
              <div className="min-w-0 flex-1">
                <code className="font-mono text-sm text-on-surface">{r.role}</code>
                <p className="mt-0.5 mb-0 text-xs text-on-surface-variant">{purpose}</p>
                {r.problem && <p className="mt-1 mb-0 text-xs text-error">{r.problem}</p>}
              </div>
              <div className="flex w-full items-center gap-2 sm:w-96">
                <Select
                  label="Model"
                  value={value}
                  options={options}
                  disabled={busy !== '' || options.length === 0}
                  onChange={(v) => void bind(r, v)}
                  className="flex-1"
                />
                {!r.builtin && keyId === '' && (
                  <IconButton label={`Delete ${r.role}`} disabled={busy !== ''} onClick={() => void remove(r)}>
                    <path d="M6 19a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z" />
                  </IconButton>
                )}
                {busy === r.role && <Spinner className="size-4 shrink-0" />}
              </div>
            </li>
          )
  }

  const builtin = data.roles.filter((r) => r.builtin)
  const custom = data.roles.filter((r) => !r.builtin)

  return (
    <div className="flex flex-col gap-3">
      <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
        {builtin.map(row)}
      </ul>

      <h3 className="mt-4 mb-0 text-sm font-medium text-on-surface">Custom aliases</h3>
      {custom.length === 0 ? (
        <p className="m-0 text-sm text-on-surface-variant">
          {keyId === ''
            ? 'None yet. A custom alias is a name of your own pointing to one model.'
            : 'None. Custom aliases are made in Settings › Model aliases.'}
        </p>
      ) : (
        <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
          {custom.map(row)}
        </ul>
      )}
      {keyId === '' && (
        <NewAlias
          real={real}
          busy={busy !== ''}
          onCreate={async (name, model) => {
            setBusy(name)
            setFailure('')
            try {
              await api.setModelRole('', name, model)
              await reload()
              return true
            } catch (err) {
              setFailure(messageOf(err))
              return false
            } finally {
              setBusy('')
            }
          }}
        />
      )}
      {failure !== '' && <Banner tone="error">{failure}</Banner>}
    </div>
  )
}

/** Making a custom alias: a name, and the one model it points to. */
function NewAlias({
  real,
  busy,
  onCreate,
}: {
  real: Model[]
  busy: boolean
  onCreate: (name: string, model: string) => Promise<boolean>
}) {
  const [name, setName] = useState('')
  const options = choicesFor('', real)
  const [model, setModel] = useState('')
  const chosen = model || options[0]?.value || ''
  const valid = /^[a-z0-9][a-z0-9.-]{0,63}$/.test(name)
  return (
    <div className="flex flex-wrap items-start gap-3">
      <Field label="New alias" value={name} onChange={(v) => setName(v.toLowerCase())} mono className="w-full sm:w-64" />
      <Select label="Model" value={chosen} options={options} onChange={setModel} className="w-full sm:w-96" />
      <TonalButton
        disabled={busy || !valid || chosen === ''}
        onClick={() =>
          void onCreate(name, chosen).then((ok) => {
            if (ok) setName('')
          })
        }
      >
        Add
      </TonalButton>
      {name !== '' && !name.includes('claude') && (
        <p className="m-0 w-full text-xs text-warning">
          Claude Code&rsquo;s model picker lists only names containing &ldquo;claude&rdquo;, and
          opencode treats other names as non-Claude models.
        </p>
      )}
    </div>
  )
}

/** Settings › Model aliases: the gateway's aliases, which every key follows by default. */
export default function ModelsTab() {
  return (
    <div className="flex flex-col gap-5">
      <Card>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          Every client config this gateway hands out asks for these names instead of a real model,
          so moving every client to a new model is one change here. Each list starts with the newest
          model of the alias&rsquo;s family, which is what the alias serves until you choose. A key
          can choose differently in its own settings.
        </p>
        <RolesEditor />
        <p className="mt-4 mb-0 text-xs text-on-surface-variant">
          Clients size their requests from the model an alias served when their config was
          written. Moving one to a model with a smaller context window means downloading the
          configs again from each key&rsquo;s settings.
        </p>
      </Card>
    </div>
  )
}
