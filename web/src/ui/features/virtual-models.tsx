import { useState } from 'react'
import {
  ApiError,
  CLIENT_APPS,
  api,
  messageOf,
  type ApiKey,
  type Model,
  type VirtualModel,
} from '../../api/client'
import { useLoader } from '../hooks'
import {
  Banner,
  Card,
  CardTitle,
  Chip,
  Empty,
  ErrorState,
  Field,
  FilledButton,
  IconButton,
  OutlinedButton,
  SmallButton,
  Spinner,
  SubNav,
  TextButton,
  TonalButton,
} from '../primitives'

/**
 * Virtual models: names clients are configured with, mapped here to the real
 * models that answer them.
 *
 * Changing which model every client uses becomes one edit in the gateway
 * rather than a round of config files. A mapping lists its targets in order;
 * a request gets the first one an account has on and the key may use. The
 * client is answered in the name it asked for, and the request log keeps both.
 *
 * Scoped, most specific first: one app on one key, then one key, then the
 * gateway. The Models tab holds the gateway's; a key's settings hold its own
 * and its apps'.
 */

type Draft = { name: string; display_name: string; targets: string[] }

/** One mapping being made or changed. */
function VirtualEditor({
  initial,
  real,
  lockName,
  busy,
  error,
  onSave,
  onCancel,
}: {
  initial: Draft
  real: Model[]
  /** Overriding a wider mapping keeps its name: that is what is overridden. */
  lockName: boolean
  busy: boolean
  error: string
  onSave: (d: Draft) => void
  onCancel: () => void
}) {
  const [draft, setDraft] = useState<Draft>(initial)
  const [adding, setAdding] = useState('')
  const free = real.filter((m) => !draft.targets.includes(m.id))
  const move = (i: number, by: number) => {
    const t = [...draft.targets]
    const [x] = t.splice(i, 1)
    t.splice(i + by, 0, x)
    setDraft({ ...draft, targets: t })
  }

  return (
    <div className="flex flex-col gap-4 rounded-[var(--radius-md3-m)] border border-outline bg-surface-high p-4">
      <div className="grid gap-3 sm:grid-cols-2">
        {lockName ? (
          <div>
            <div className="mb-1 text-xs text-on-surface-variant">Name</div>
            <code className="font-mono text-sm text-on-surface">{draft.name}</code>
          </div>
        ) : (
          <Field
            label="Name clients ask for"
            value={draft.name}
            onChange={(v) => setDraft({ ...draft, name: v.toLowerCase() })}
            autoFocus
          />
        )}
        <Field
          label="Display name"
          value={draft.display_name}
          onChange={(v) => setDraft({ ...draft, display_name: v })}
        />
      </div>
      {!lockName && !draft.name.includes('claude') && draft.name !== '' && (
        <p className="m-0 text-xs text-warning">
          Claude Code&rsquo;s model picker shows only names containing &ldquo;claude&rdquo; — call it
          claude-&hellip; to see it there. Other clients take any name.
        </p>
      )}

      <div>
        <div className="mb-2 text-xs text-on-surface-variant">
          Served by, in order — the first one an account has on and the key may use answers
        </div>
        {draft.targets.length === 0 ? (
          <p className="m-0 mb-2 text-sm text-on-surface-variant">No model yet.</p>
        ) : (
          <ol className="m-0 mb-2 flex list-none flex-col gap-1 p-0">
            {draft.targets.map((t, i) => (
              <li
                key={t}
                className="flex items-center gap-2 rounded-[var(--radius-md3-s)] bg-surface-lowest px-3 py-1"
              >
                <span className="w-5 text-xs text-on-surface-variant tabular-nums">{i + 1}.</span>
                <code className="flex-1 font-mono text-sm text-on-surface">{t}</code>
                <IconButton label="Earlier" disabled={i === 0} onClick={() => move(i, -1)}>
                  <path d="M7.4 15.4 12 10.8l4.6 4.6L18 14l-6-6-6 6z" />
                </IconButton>
                <IconButton
                  label="Later"
                  disabled={i === draft.targets.length - 1}
                  onClick={() => move(i, 1)}
                >
                  <path d="M7.4 8.6 12 13.2l4.6-4.6L18 10l-6 6-6-6z" />
                </IconButton>
                <IconButton
                  label="Remove"
                  onClick={() =>
                    setDraft({ ...draft, targets: draft.targets.filter((x) => x !== t) })
                  }
                >
                  <path d="M18.3 5.7 12 12l6.3 6.3-1.4 1.4L10.6 13.4 4.3 19.7l-1.4-1.4L9.2 12 2.9 5.7l1.4-1.4 6.3 6.3 6.3-6.3z" />
                </IconButton>
              </li>
            ))}
          </ol>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <select
            aria-label="Add a model"
            value={adding}
            onChange={(e) => setAdding(e.target.value)}
            className="h-9 rounded-[var(--radius-md3-xs)] border border-outline bg-surface-lowest px-2 text-sm text-on-surface"
          >
            <option value="">Add a model…</option>
            {free.map((m) => (
              <option key={m.id} value={m.id}>
                {m.display_name ? `${m.display_name} — ${m.id}` : m.id}
              </option>
            ))}
          </select>
          <SmallButton
            disabled={adding === ''}
            onClick={() => {
              setDraft({ ...draft, targets: [...draft.targets, adding] })
              setAdding('')
            }}
          >
            Add
          </SmallButton>
        </div>
      </div>

      {error !== '' && <Banner tone="error">{error}</Banner>}
      <div className="flex justify-end gap-2">
        <OutlinedButton onClick={onCancel} disabled={busy}>
          Cancel
        </OutlinedButton>
        <FilledButton
          type="button"
          disabled={busy || draft.name.trim() === '' || draft.targets.length === 0}
          onClick={() => onSave(draft)}
        >
          {busy && <Spinner />}
          Save
        </FilledButton>
      </div>
    </div>
  )
}

/** A mapping's targets, the one serving now marked, unavailable ones dimmed. */
function Targets({ v }: { v: VirtualModel }) {
  return (
    <span className="flex flex-wrap items-center gap-1 text-xs">
      {v.targets.map((t, i) => (
        <span key={t} className="flex items-center gap-1">
          {i > 0 && <span className="text-on-surface-variant">→</span>}
          <code
            className={`font-mono ${
              t === v.serving
                ? 'rounded bg-primary-container px-1 text-on-primary-container'
                : 'text-on-surface-variant'
            }`}
          >
            {t}
          </code>
        </span>
      ))}
      {v.serving === '' && <Chip tone="error">nothing available</Chip>}
    </span>
  )
}

/** The real models, for picking targets from; empty while unread. */
function useRealModels() {
  const { data } = useLoader(() => api.models(), undefined, [], 0)
  return (data?.data ?? []).filter((m) => m.virtual !== true)
}

/**
 * The Models tab: the gateway-wide virtual models, which every key and app
 * gets unless its own settings say otherwise.
 */
export default function ModelsTab({ onExpired }: { onExpired: () => void }) {
  const { data, error, loading, reload } = useLoader(() => api.virtualModels(), onExpired, [], 0)
  const real = useRealModels()
  const [editing, setEditing] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState('')

  const global = (data?.models ?? []).filter((v) => v.key_id === '')
  const overridden = (name: string) =>
    (data?.models ?? []).filter((v) => v.key_id !== '' && v.name === name).length

  const save = async (d: Draft) => {
    setBusy(true)
    setFailure('')
    try {
      await api.putVirtualModel({ key_id: '', client: '', ...d })
      setEditing(null)
      await reload()
    } catch (err) {
      if (err instanceof ApiError && err.isUnauthenticated) onExpired()
      setFailure(messageOf(err))
    }
    setBusy(false)
  }
  const remove = async (v: VirtualModel) => {
    if (!window.confirm(`Remove ${v.name}? Clients configured with it will be told no such model.`))
      return
    try {
      await api.deleteVirtualModel('', '', v.name)
      await reload()
    } catch (err) {
      setFailure(messageOf(err))
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardTitle
          aside={
            editing === null && (
              <TonalButton onClick={() => setEditing('')}>New virtual model</TonalButton>
            )
          }
        >
          Virtual models
        </CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          Names clients are configured with, mapped here to the real models that answer them — so
          moving every client to a new model is one edit here. A client is answered in the name it
          asked for. A key can map a name differently for itself, or for one of its apps, in its
          own settings.
        </p>

        {editing === '' && (
          <div className="mb-4">
            <VirtualEditor
              initial={{ name: 'claude-', display_name: '', targets: [] }}
              real={real}
              lockName={false}
              busy={busy}
              error={failure}
              onSave={(d) => void save(d)}
              onCancel={() => {
                setEditing(null)
                setFailure('')
              }}
            />
          </div>
        )}

        {loading && data === null ? (
          <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        ) : error !== '' ? (
          <ErrorState message={error} onRetry={() => void reload()} busy={loading} />
        ) : global.length === 0 && editing !== '' ? (
          <Empty>No virtual models yet. Clients ask for real model names.</Empty>
        ) : (
          <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
            {global.map((v) =>
              editing === v.name ? (
                <li key={v.name} className="py-3">
                  <VirtualEditor
                    initial={{ name: v.name, display_name: v.display_name, targets: v.targets }}
                    real={real}
                    lockName
                    busy={busy}
                    error={failure}
                    onSave={(d) => void save(d)}
                    onCancel={() => {
                      setEditing(null)
                      setFailure('')
                    }}
                  />
                </li>
              ) : (
                <li key={v.name} className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <code className="font-mono text-sm text-on-surface">{v.name}</code>
                      {v.display_name !== '' && (
                        <span className="text-sm text-on-surface-variant">{v.display_name}</span>
                      )}
                      {overridden(v.name) > 0 && (
                        <Chip tone="neutral">
                          overridden on {overridden(v.name)} key{overridden(v.name) === 1 ? '' : 's'}
                        </Chip>
                      )}
                    </div>
                    <div className="mt-1">
                      <Targets v={v} />
                    </div>
                  </div>
                  <div className="flex gap-1">
                    <TextButton onClick={() => setEditing(v.name)}>Edit</TextButton>
                    <TextButton tone="error" onClick={() => void remove(v)}>
                      Remove
                    </TextButton>
                  </div>
                </li>
              ),
            )}
          </ul>
        )}
        {failure !== '' && editing === null && (
          <Banner tone="error" className="mt-4">
            {failure}
          </Banner>
        )}
      </Card>
    </div>
  )
}

const SCOPES = ['', ...CLIENT_APPS] as const
type Scope = (typeof SCOPES)[number]

/**
 * A key's own mappings, for the key as a whole or for one of its client apps.
 *
 * Each scope shows every name the app would be offered and where its mapping
 * comes from — the gateway, this key, or this app — and lets that be
 * overridden here, or an override removed to let the wider one through.
 */
export function KeyVirtualModels({ apiKey: k }: { apiKey: ApiKey }) {
  const { data, error, loading, reload } = useLoader(() => api.virtualModels(), undefined, [], 0)
  const real = useRealModels()
  const [scope, setScope] = useState<Scope>('')
  const [editing, setEditing] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState('')

  if (loading && data === null) {
    return (
      <p className="m-0 flex items-center gap-2 text-sm text-on-surface-variant">
        <Spinner /> Loading…
      </p>
    )
  }
  if (error !== '' || data === null) {
    return <ErrorState message={error || 'Could not read the virtual models.'} onRetry={() => void reload()} busy={loading} />
  }

  // The effective mapping per name for this scope, and where it comes from.
  const rank = (v: VirtualModel) =>
    v.key_id === k.id && v.client === scope && scope !== ''
      ? 0
      : v.key_id === k.id && v.client === ''
        ? 1
        : v.key_id === '' && v.client === ''
          ? 2
          : -1
  const effective = new Map<string, VirtualModel>()
  for (const v of data.models) {
    const r = rank(v)
    if (r < 0) continue
    const have = effective.get(v.name)
    if (have === undefined || rank(have) > r) effective.set(v.name, v)
  }
  const rows = [...effective.values()].sort((a, b) => a.name.localeCompare(b.name))
  const here = (v: VirtualModel) => v.key_id === k.id && v.client === scope
  const source = (v: VirtualModel) =>
    v.key_id === '' ? 'gateway' : v.client === '' ? 'this key' : v.client

  const save = async (d: Draft) => {
    setBusy(true)
    setFailure('')
    try {
      await api.putVirtualModel({ key_id: k.id, client: scope, ...d })
      setEditing(null)
      await reload()
    } catch (err) {
      setFailure(messageOf(err))
    }
    setBusy(false)
  }
  const removeOverride = async (v: VirtualModel) => {
    try {
      await api.deleteVirtualModel(k.id, scope, v.name)
      await reload()
    } catch (err) {
      setFailure(messageOf(err))
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <SubNav
        label="Client app"
        value={scope}
        onChange={(s) => {
          setScope(s)
          setEditing(null)
          setFailure('')
        }}
        options={SCOPES.map((s) => ({ id: s, label: s === '' ? 'All apps' : s }))}
      />
      <p className="m-0 text-xs text-on-surface-variant">
        {scope === ''
          ? 'What this key’s apps get unless an app below says otherwise. Names come from the Models tab until overridden here.'
          : `What ${scope} gets with this key. The gateway’s or this key’s mapping applies until overridden here.`}
      </p>

      {editing === '' && (
        <VirtualEditor
          initial={{ name: 'claude-', display_name: '', targets: [] }}
          real={real}
          lockName={false}
          busy={busy}
          error={failure}
          onSave={(d) => void save(d)}
          onCancel={() => setEditing(null)}
        />
      )}

      {rows.length === 0 && editing !== '' ? (
        <Empty>No virtual models apply here.</Empty>
      ) : (
        <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
          {rows.map((v) =>
            editing === v.name ? (
              <li key={v.name} className="py-3">
                <VirtualEditor
                  initial={{ name: v.name, display_name: v.display_name, targets: v.targets }}
                  real={real}
                  lockName
                  busy={busy}
                  error={failure}
                  onSave={(d) => void save(d)}
                  onCancel={() => setEditing(null)}
                />
              </li>
            ) : (
              <li key={v.name} className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <code className="font-mono text-sm text-on-surface">{v.name}</code>
                    <Chip tone={here(v) ? 'ok' : 'neutral'}>from {source(v)}</Chip>
                  </div>
                  <div className="mt-1">
                    <Targets v={v} />
                  </div>
                </div>
                <div className="flex gap-1">
                  <TextButton onClick={() => setEditing(v.name)}>
                    {here(v) ? 'Edit' : 'Override'}
                  </TextButton>
                  {here(v) && (
                    <TextButton tone="error" onClick={() => void removeOverride(v)}>
                      Remove override
                    </TextButton>
                  )}
                </div>
              </li>
            ),
          )}
        </ul>
      )}
      {editing === null && (
        <div>
          <SmallButton onClick={() => setEditing('')}>
            New name for {scope === '' ? 'this key' : scope}
          </SmallButton>
        </div>
      )}
      {failure !== '' && editing === null && <Banner tone="error">{failure}</Banner>}
    </div>
  )
}
