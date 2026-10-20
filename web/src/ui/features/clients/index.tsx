import { useState } from 'react'
import { api, type ClientWire, type DialectId } from '../../../api/client'
import { useAction, useLoader } from '../../hooks'
import { clientHref, navigate } from '../../route'
import {
  Actions,
  AddButton,
  Button,
  Card,
  Chip,
  Empty,
  ErrorBanner,
  Field,
  Freshness,
  ListRow,
  ListSummary,
  ListToolbar,
  Loading,
  Modal,
  Note,
  Page,
  RowBadge,
  Segmented,
  Select,
  Stack,
  Switch,
} from '../../primitives'
import { ago, compact } from '../../format'
import { Client, type ClientTypes } from '../../clients/specs'
import { KeyFigures, REFRESH_MS, SUBSCRIPTION_NOTE } from './parts'
import { useClientTypes } from './types'

export { ClientPage } from './page'

/**
 * The clients: each thing set up to use the gateway — a laptop's Claude Code,
 * a crush, a script — with the one key it holds and the model names that are
 * its own. Adding one is choosing its type and naming it; everything else is
 * on its page.
 */
export default function Clients() {
  const [adding, setAdding] = useState(false)
  const { types } = useClientTypes()
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.clients.list(signal),
    { refreshMs: REFRESH_MS },
  )

  return (
    <Page>
      <ListToolbar
        action={
          <AddButton disabled={adding || types === null} onClick={() => setAdding(true)}>
            New client
          </AddButton>
        }
      >
        {data !== null && data.clients.length > 0 && <ListSummary>traffic over {data.windowDays}d</ListSummary>}
        <Freshness updatedAt={updatedAt} refreshing={refreshing} live={live} onRefresh={() => void reload()} />
      </ListToolbar>

      {adding && types !== null && <NewClient types={types} onClose={() => setAdding(false)} />}

      <Card
        info={
          <>
            <p>
              Open a client to see its model names and what each serves, set it up with one command,
              change its limits, or see what it spent.
            </p>
            <p>{SUBSCRIPTION_NOTE}</p>
          </>
        }
      >
        <ErrorBanner error={error} className="mb-4" />
        {loading || types === null ? (
          <Loading />
        ) : data === null || data.clients.length === 0 ? (
          <Empty>No clients yet. Add one above: its page then has its configuration and key.</Empty>
        ) : (
          <Stack gap={2} as="ul">
            {data.clients.map((c) => (
              <li key={c.id}>
                <ClientRow client={new Client(c, types.type(c.type))} types={types} windowDays={data.windowDays}
                  onChanged={() => void reload()} />
              </li>
            ))}
          </Stack>
        )}
      </Card>
    </Page>
  )
}

/** One client in the list: what it is, what its names serve, its figures, its switch. */
function ClientRow({
  client: c,
  types,
  windowDays,
  onChanged,
}: {
  client: Client
  types: ClientTypes
  windowDays: number
  onChanged: () => void
}) {
  const k = c.wire
  return (
    <ListRow
      href={clientHref(c.id)}
      leading={<RowBadge>{c.type.label.slice(0, 2)}</RowBadge>}
      title={c.name}
      subtitle={
        <>
          {c.type.label} · {types.dialectTitle(c.dialect)} · {c.summary} · {compact(k.requests)} requests over{' '}
          {windowDays}d · used {ago(k.last_used_at)}
        </>
      }
      chips={<ClientChips client={c} />}
      meters={<KeyFigures k={k} />}
      actions={<EnableSwitch client={k} onChanged={onChanged} />}
    />
  )
}

/** What sets a client apart, as chips. */
export function ClientChips({ client: c }: { client: Client }) {
  return (
    <>
      {c.wire.disabled && <Chip tone="error">off</Chip>}
      {!c.configured && <Chip tone="neutral">not set up</Chip>}
      {c.unavailable.length > 0 && <Chip tone="warn">a slot is unavailable</Chip>}
    </>
  )
}

/**
 * A client's on/off switch, the same in its list row and its page's header.
 * Off, its key is refused before anything is routed, and its history stays.
 */
export function EnableSwitch({ client, onChanged }: { client: ClientWire; onChanged: () => void }) {
  const [on, setOn] = useState(!client.disabled)
  const { busy, error, run } = useAction()
  const flip = async (v: boolean) => {
    setOn(v)
    const ok = await run('switch', async () => {
      await api.clients.setDisabled(client.id, !v)
      onChanged()
    })
    if (!ok) setOn(!v)
  }
  return (
    <span title={error || (on ? 'Serving this client' : 'This client is switched off')}>
      <Switch checked={on} onChange={(v) => void flip(v)} label={`Serve ${client.name}`} disabled={busy !== ''} quiet />
    </span>
  )
}

/**
 * Adding a client: its type, its name, and its API where its type has two. The
 * rest — the model names its type has, each following its default — is made
 * with it, and its page opens on its key and configuration.
 */
function NewClient({ types, onClose }: { types: ClientTypes; onClose: () => void }) {
  const [name, setName] = useState('')
  const [typeId, setTypeId] = useState(types.all[0]?.id ?? 'custom')
  const type = types.type(typeId)
  const [dialect, setDialect] = useState<DialectId>(type.defaultDialect)
  const { pending: busy, error, setError, run } = useAction()

  const choose = (id: string) => {
    setTypeId(id)
    setDialect(types.type(id).defaultDialect)
  }
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (name.trim() === '') {
      setError('Give the client a name — "laptop", "work crush" — it is how you will recognise it.')
      return
    }
    await run('create', async () => {
      const made = await api.clients.create(name.trim(), typeId, type.choosesDialect ? dialect : '', {
        rpmLimit: 0, ratePeriodS: 60, tokenBudget: 0,
      })
      onClose()
      navigate(clientHref(made.client.id))
    })
  }

  return (
    <Modal title="New client" onClose={onClose}>
      <form onSubmit={submit}>
        <Stack>
          <Select
            label="Type"
            value={typeId}
            onChange={choose}
            options={types.all.map((t) => ({
              value: t.id,
              label: t.label,
              hint: t.custom ? 'a tool with no recipe: its key and the two addresses' : t.slots.map((s) => s.label).join(', '),
            }))}
          />
          <Field label="Name" value={name} onChange={setName} autoFocus />
          {type.choosesDialect && (
            <Stack gap={2}>
              <Segmented
                label="API"
                value={dialect}
                onChange={setDialect}
                options={type.dialects.map((d) => ({ id: d, label: types.dialectTitle(d), content: types.dialectTitle(d) }))}
              />
              {type.why !== '' && <Note>{type.why}</Note>}
            </Stack>
          )}
          <ErrorBanner error={error} />
          <Actions end>
            <Button variant="outlined" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" busy={busy}>
              {busy ? 'Creating…' : 'Create client'}
            </Button>
          </Actions>
        </Stack>
      </form>
    </Modal>
  )
}
