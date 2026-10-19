import { useState } from 'react'
import { api, type ClientRole, type Model, type ModelRole } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import { providerLabel } from '../../providers'
import {
  Button,
  Card,
  Chip,
  ErrorBanner,
  ErrorState,
  Field,
  IconButton,
  Loading,
  MenuButton,
  MenuRow,
  Muted,
  Note,
  Page,
  Section,
  Select,
  Spinner,
  Stack,
  tile,
  type SelectOption,
  useConfirm,
} from '../../primitives'

/**
 * Model aliases: names the gateway's configs ask for instead of real models.
 *
 * Four are roles, the two slots every client has twice over —
 * claude-large and gpt-large for the model doing the work, claude-small and
 * gpt-small for titles and summaries — and the rest are the Claude families,
 * which is how Claude Code names its tiers. Which real model answers each is
 * decided here, once, instead of in every client: for the whole gateway, per
 * client — Codex's claude-large apart from Claude Code's — and per key in its
 * settings. A key's binding wins over its client's, and a client's over the
 * gateway's.
 *
 * Any alias may serve any provider's model: the request goes to whichever
 * provider's accounts serve the model it resolves to. Claude Code speaks only
 * Anthropic's API, so an alias it uses bound to a ChatGPT model is refused to
 * it, and the row says so before anyone meets the refusal.
 */

const PURPOSE: Record<string, string> = {
  'claude-large': 'The model doing the work: every app’s main model, and its sub-agents.',
  'claude-small': 'Titles, summaries and other quick calls, run many times a session.',
  'claude-opus': 'Claude Code’s opus tier, and anything asking for Opus by name.',
  'claude-sonnet': 'Claude Code’s sonnet tier.',
  'claude-haiku': 'Claude Code’s haiku tier.',
  'claude-fable': 'Claude Code’s fable tier, and its “best”.',
  'gpt-large':
    'Codex’s main model on your ChatGPT accounts. Unbound, the backend’s own first choice of the models they list.',
  'gpt-small':
    'Quick calls on your ChatGPT accounts. Unbound, the last-ranked model of the first choice’s generation — its light one.',
}

/** A real model's family, from its id: claude-opus-5-5 is opus. */
function familyOf(id: string): string {
  return /^claude-([a-z]+)-/.exec(id)?.[1] ?? ''
}

/** Whose accounts serve a model: the list says, for any but Anthropic's. */
function providerOfModel(m: Model): string {
  return m.provider ?? 'anthropic'
}

/**
 * The models an alias can be bound to, those it follows first — its own
 * family's, or its provider's in the provider's ranking — then the rest, each
 * in the order the accounts list them. A model of another provider is marked
 * as one, since binding to it moves the alias to that provider's accounts.
 */
function choicesFor(r: { family: string; provider?: string }, real: Model[]): SelectOption[] {
  const follows = (m: Model) =>
    r.family !== '' ? familyOf(m.id) === r.family : r.provider !== undefined && providerOfModel(m) === r.provider
  const own = real.filter(follows)
  const rest = real.filter((m) => !follows(m))
  const home = r.provider ?? 'anthropic'
  return [...own, ...rest].map((m) => ({
    value: m.id,
    label: m.display_name ?? m.id,
    hint: providerOfModel(m) !== home ? `${m.id} · ${providerLabel(providerOfModel(m))}` : m.id,
  }))
}

/**
 * Why a client will be refused an alias it is configured with: a Claude alias
 * — the names Claude Code asks for — serving a ChatGPT model, which Claude
 * Code cannot reach. Empty when nothing is wrong.
 */
function reachWarning(r: ModelRole, real: Model[]): string {
  if (!r.role.startsWith('claude-') || r.serving === '') return ''
  const served = real.find((m) => m.id === r.serving)
  if (served === undefined || providerOfModel(served) === 'anthropic') return ''
  return (
    `${r.role} serves ${r.serving}, a ${providerLabel(providerOfModel(served))} model. ` +
    'Codex can use it; Claude Code and other Anthropic API clients asking for ' +
    `${r.role} are refused, because only an OpenAI Responses client can reach those accounts.`
  )
}

/** The aliases as a short form: for the gateway (keyId empty) or one key. */
export function RolesEditor({ keyId = '' }: { keyId?: string }) {
  const { data, error, loading, reload } = useLoader((signal) => api.models.roles(keyId, signal), {
    deps: [keyId],
  })
  const { data: models } = useLoader((signal) => api.models.list(signal))
  const real = (models ?? []).filter((m) => m.virtual !== true)
  const { busy, error: failure, run } = useAction()
  const confirm = useConfirm()

  if (loading && data === null) return <Loading />
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
      await api.models.setRole({ keyId }, r.role, model)
      await reload()
    })

  const bindFor = (r: ModelRole, client: string, model: string) =>
    run(`${r.role}/${client}`, async () => {
      await api.models.setRole({ client }, r.role, model)
      await reload()
    })

  const remove = (r: ModelRole) =>
    confirm({
      title: `Delete ${r.role}?`,
      body: 'Clients configured with it will be told there is no such model.',
      confirmLabel: 'Delete alias',
      tone: 'danger',
      action: async () => {
        await api.models.deleteAlias(r.role)
        await reload()
      },
    })

  // One alias as a row like any list's: what it is and what it is for on
  // the left, the model it serves on the right, and — when it cannot serve —
  // why, in the error colour where it is read rather than skimmed.
  const row = (r: ModelRole) => {
    const choices = choicesFor(r, real)
    // A key's list starts with following the gateway; the gateway's shows
    // what the alias serves until something else is chosen.
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
    const value = keyId === '' ? r.model || r.serving || options[0]?.value || '' : r.model
    const purpose =
      PURPOSE[r.role] ??
      (r.builtin
        ? `The newest ${r.family} model, found from what your accounts serve.`
        : 'Your own alias.')
    const broken = r.problem !== undefined && r.problem !== ''
    const unreachable = reachWarning(r, real)
    const perClient = keyId === '' && (r.clients ?? []).length > 0
    return (
      <li key={r.role} className={`${tile} flex flex-col gap-3 px-4 py-3`}>
        {/* On the gateway's view the right-hand column grows a Select per
            client under the gateway's own, so the text keeps to the top. */}
        <div
          className={`flex flex-wrap gap-x-6 gap-y-3 ${keyId === '' ? 'items-start' : 'items-center'}`}
        >
          <div className="min-w-0 flex-1 basis-64">
            <div className="flex flex-wrap items-center gap-2">
              <code className="font-mono text-sm font-medium text-on-surface">{r.role}</code>
              {broken && <Chip tone="error">not served</Chip>}
            </div>
            <div className="mt-1">
              <Muted>{purpose}</Muted>
            </div>
          </div>
          <div className="flex w-full flex-col gap-2 sm:w-96">
            <div className="flex items-center gap-2">
              <Select
                label={perClient ? 'Every other client' : 'Model'}
                value={value}
                options={options}
                disabled={busy !== '' || options.length === 0}
                onChange={(v) => void bind(r, v)}
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
            {keyId === '' && (
              <ClientBindings
                role={r}
                choices={choices}
                clients={data.clients}
                busy={busy}
                bind={(client, model) => bindFor(r, client, model)}
              />
            )}
          </div>
        </div>
        {broken && <Note tone="error">{r.problem}</Note>}
        {!broken && unreachable !== '' && <Note tone="warn">{unreachable}</Note>}
      </li>
    )
  }

  // The roles every client has, then one per family, then the operator's
  // own.
  const all = data.roles
  const ROLES = ['claude-large', 'claude-small', 'gpt-large', 'gpt-small']
  const roles = all.filter((r) => ROLES.includes(r.role))
  const families = all.filter((r) => r.builtin && !roles.includes(r))
  const custom = all.filter((r) => !r.builtin)
  const group = (title: string, list: ModelRole[]) => (
    <Section title={title}>
      <Stack gap={2} as="ul">
        {list.map(row)}
      </Stack>
    </Section>
  )

  return (
    <Stack gap={6}>
      {group('Roles', roles)}
      {families.length > 0 && group('Families', families)}

      <Section title="Custom aliases">
        <Stack gap={2}>
          {custom.length > 0 && (
            <Stack gap={2} as="ul">
              {custom.map(row)}
            </Stack>
          )}
          {keyId === '' ? (
            <NewAlias
              real={real}
              busy={busy !== ''}
              onCreate={(name, model) =>
                run(name, async () => {
                  await api.models.setRole({}, name, model)
                  await reload()
                })
              }
            />
          ) : (
            custom.length === 0 && <Note>None. Custom aliases are made in Settings › Aliases.</Note>
          )}
        </Stack>
      </Section>
      <ErrorBanner error={failure} />
    </Stack>
  )
}

/**
 * The options with the bound model among them. A binding can name a model no
 * account lists now — one switched off, or a target from another provider —
 * and a Select showing the first option instead would say it is bound to
 * something it is not.
 */
function withBound(options: SelectOption[], model: string): SelectOption[] {
  if (model === '' || options.some((o) => o.value === model)) return options
  return [...options, { value: model, label: model }]
}

/**
 * One alias's bindings per client, under its gateway binding and in the same
 * column: a Select per client, labelled with it, starting with following the
 * gateway as a key's do, and a menu of the clients not yet bound. A client
 * picked from the menu shows the gateway's model until a model is chosen, so
 * nothing is stored before there is something to store.
 *
 * The clients are named as the request log names them, from the User-Agent;
 * the menu has the four the gateway recognises and any other it has seen.
 */
function ClientBindings({
  role: r,
  choices,
  clients,
  busy,
  bind,
}: {
  role: ModelRole
  choices: SelectOption[]
  clients: string[]
  busy: string
  bind: (client: string, model: string) => Promise<boolean>
}) {
  const [picked, setPicked] = useState<string[]>([])
  const bound = r.clients ?? []
  const shown: ClientRole[] = [
    ...bound,
    ...picked
      .filter((c) => !bound.some((b) => b.client === c))
      .map((c) => ({ client: c, model: '', serving: r.serving })),
  ]
  const free = clients.filter((c) => !shown.some((o) => o.client === c))
  const options: SelectOption[] = [
    { value: '', label: 'Same as the gateway', hint: r.serving !== '' ? r.serving : undefined },
    ...choices,
  ]
  const drop = (client: string) => setPicked((p) => p.filter((c) => c !== client))

  if (shown.length === 0 && free.length === 0) return null
  return (
    <div className="flex flex-col gap-2">
      {shown.length > 0 && (
        <Stack gap={2} as="ul">
          {shown.map((o) => (
            <li key={o.client} className="flex flex-col gap-2">
              <div className="flex items-center gap-2">
                <Select
                  label={o.client}
                  value={o.model}
                  options={withBound(options, o.model)}
                  disabled={busy !== '' || choices.length === 0}
                  onChange={(v) =>
                    void bind(o.client, v).then((ok) => {
                      if (ok) drop(o.client)
                    })
                  }
                />
                <IconButton
                  label={`Stop binding ${r.role} for ${o.client}`}
                  disabled={busy !== ''}
                  onClick={() =>
                    o.model === '' ? drop(o.client) : void bind(o.client, '')
                  }
                >
                  <path d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z" />
                </IconButton>
                {busy === `${r.role}/${o.client}` && <Spinner className="size-4 shrink-0" />}
              </div>
              {o.problem !== undefined && o.problem !== '' && (
                <Note tone="error">{o.problem}</Note>
              )}
            </li>
          ))}
        </Stack>
      )}
      {free.length > 0 && (
        <MenuButton
          label="For one client"
          menuLabel={`Bind ${r.role} for a client`}
          icon="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z"
          disabled={busy !== '' || choices.length === 0}
        >
          {(close) =>
            free.map((c) => (
              <MenuRow
                key={c}
                onClick={() => {
                  close()
                  setPicked((p) => [...p, c])
                }}
              >
                {c}
              </MenuRow>
            ))
          }
        </MenuButton>
      )}
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
  const options = choicesFor({ family: '' }, real)
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
          grow
        />
        <Select label="Model" value={chosen} options={options} onChange={setModel} width="fixed" />
        <Button
          disabled={busy || !valid || chosen === ''}
          onClick={() =>
            void onCreate(name, chosen).then((ok) => {
              if (ok) setName('')
            })
          }
        >
          Add
        </Button>
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
    <Page>
      <Card
        info={
          <>
            <p>
              Every client config this gateway hands out asks for these names instead of a real
              model, so moving every client to a new model is one change here. Each list starts with
              what the alias follows — the newest model of a Claude family, or for{' '}
              <code>gpt-large</code> and <code>gpt-small</code> the ChatGPT backend&rsquo;s own
              ranking — which is what it serves until you choose.
            </p>
            <p>
              Any alias can be bound to either provider&rsquo;s model, and the request goes to the
              accounts that serve it. Codex reaches both; Claude Code reaches only Claude accounts,
              and is refused an alias bound to a ChatGPT model.
            </p>
            <p>
              An alias can serve one client differently — Codex&rsquo;s{' '}
              <code>claude-large</code> on one model while Claude Code&rsquo;s stays on another. The
              client is the one the request log names, from its User-Agent. A key can choose
              differently again in its own settings, and a key&rsquo;s choice wins over its
              client&rsquo;s.
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
    </Page>
  )
}
