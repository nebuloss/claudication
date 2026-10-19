import { useState } from 'react'
import { api, type Model, type ModelRole } from '../../api/client'
import { useAction, useLoader } from '../hooks'
import {
  Banner,
  Card,
  Chip,
  ErrorState,
  Field,
  FilledButton,
  IconButton,
  Note,
  Select,
  Spinner,
  tile,
  type SelectOption,
  useConfirm,
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
  const { data, error, loading, reload } = useLoader((signal) => api.modelRoles(keyId, signal), {
    deps: [keyId],
  })
  const { data: models } = useLoader((signal) => api.models(signal))
  const real = (models?.data ?? []).filter((m) => m.virtual !== true)
  const { busy, error: failure, run } = useAction()
  const confirm = useConfirm()

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

  const bind = (r: ModelRole, model: string) =>
    run(r.role, async () => {
      await api.setModelRole(keyId, r.role, model)
      await reload()
    })

  const remove = (r: ModelRole) =>
    confirm({
      title: `Delete ${r.role}?`,
      body: 'Clients configured with it will be told there is no such model.',
      confirmLabel: 'Delete alias',
      tone: 'danger',
      action: async () => {
        await api.deleteAlias(r.role)
        await reload()
      },
    })

  // One alias as a row like any list's: what it is and what it is for on
  // the left, the model it serves on the right, and — when it cannot serve —
  // why, in the error colour where it is read rather than skimmed.
  const row = (r: ModelRole) => {
    const choices = choicesFor(r.family, real)
    // A key's list starts with following the gateway; the gateway's starts
    // with the newest of the family, which is what it serves until something
    // else is chosen.
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
    const broken = r.problem !== undefined && r.problem !== ''
    return (
      <li key={r.role} className={`${tile} flex flex-col gap-3 px-4 py-3`}>
        <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
          <div className="min-w-0 flex-1 basis-64">
            <div className="flex flex-wrap items-center gap-2">
              <code className="font-mono text-sm font-medium text-on-surface">{r.role}</code>
              {broken && <Chip tone="error">not served</Chip>}
            </div>
            <p className="mt-1 mb-0 text-sm text-on-surface-variant">{purpose}</p>
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
              <IconButton
                label={`Delete ${r.role}`}
                disabled={busy !== ''}
                onClick={() => void remove(r)}
              >
                <path d="M6 19a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z" />
              </IconButton>
            )}
            {busy === r.role && <Spinner className="size-4 shrink-0" />}
          </div>
        </div>
        {broken && <Note tone="error">{r.problem}</Note>}
      </li>
    )
  }

  // The two roles every client has, then one per family, then the
  // operator's own.
  const roles = data.roles.filter((r) => r.role === 'claude-large' || r.role === 'claude-small')
  const families = data.roles.filter((r) => r.builtin && !roles.includes(r))
  const custom = data.roles.filter((r) => !r.builtin)
  const group = (title: string, list: ModelRole[]) => (
    <section>
      <h3 className="mt-0 mb-2 text-sm font-medium text-on-surface">{title}</h3>
      <ul className="m-0 flex list-none flex-col gap-2 p-0">{list.map(row)}</ul>
    </section>
  )

  return (
    <div className="flex flex-col gap-6">
      {group('Roles', roles)}
      {families.length > 0 && group('Families', families)}

      <section>
        <h3 className="mt-0 mb-2 text-sm font-medium text-on-surface">Custom aliases</h3>
        <div className="flex flex-col gap-2">
          {custom.length > 0 && (
            <ul className="m-0 flex list-none flex-col gap-2 p-0">{custom.map(row)}</ul>
          )}
          {keyId === '' ? (
            <NewAlias
              real={real}
              busy={busy !== ''}
              onCreate={(name, model) =>
                run(name, async () => {
                  await api.setModelRole('', name, model)
                  await reload()
                })
              }
            />
          ) : (
            custom.length === 0 && <Note>None. Custom aliases are made in Settings › Aliases.</Note>
          )}
        </div>
      </section>
      {failure !== '' && <Banner tone="error">{failure}</Banner>}
    </div>
  )
}

/**
 * Making a custom alias: a name, and the one model it points to. A row like
 * the aliases above it, so the one being made reads as the next of them.
 */
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
    <div className={`${tile} flex flex-col gap-3 px-4 py-3`}>
      <div className="flex flex-wrap items-center gap-3">
        <Field
          label="New alias"
          value={name}
          onChange={(v) => setName(v.toLowerCase())}
          mono
          className="min-w-0 flex-1 basis-64"
        />
        <Select
          label="Model"
          value={chosen}
          options={options}
          onChange={setModel}
          className="w-full sm:w-96"
        />
        <FilledButton
          type="button"
          disabled={busy || !valid || chosen === ''}
          onClick={() =>
            void onCreate(name, chosen).then((ok) => {
              if (ok) setName('')
            })
          }
        >
          Add
        </FilledButton>
      </div>
      {name !== '' && !name.includes('claude') && (
        <Note tone="warn">
          Claude Code&rsquo;s model picker lists only names containing &ldquo;claude&rdquo;, and
          opencode treats other names as non-Claude models.
        </Note>
      )}
    </div>
  )
}

/** Settings › Aliases: the gateway's aliases, which every key follows by default. */
export default function ModelsTab() {
  return (
    <div className="flex flex-col gap-5">
      <Card
        info={
          <>
            <p>
              Every client config this gateway hands out asks for these names instead of a real
              model, so moving every client to a new model is one change here. Each list starts with
              the newest model of the alias&rsquo;s family, which is what the alias serves until you
              choose. A key can choose differently in its own settings.
            </p>
            <p>
              Clients size their requests from the model an alias served when their config was
              written. Moving one to a model with a smaller context window means downloading the
              configs again from each key&rsquo;s Clients page.
            </p>
          </>
        }
      >
        <RolesEditor />
      </Card>
    </div>
  )
}
