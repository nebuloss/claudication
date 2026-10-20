import { useState } from 'react'
import { api, type ClientChange, type DialectId, type GatewayConfig } from '../../../api/client'
import { useAction } from '../../hooks'
import { navigate } from '../../route'
import {
  Button,
  Card,
  DangerZone,
  ErrorBanner,
  Field,
  Note,
  Segmented,
  Select,
  SettingSwitch,
  Stack,
  useConfirm,
} from '../../primitives'
import type { Client, ClientTypes } from '../../clients/specs'
import { SetupCard } from './setup'
import { SlotsCard } from './slots'

/**
 * A client at a glance, on its own path: how to set it up (or that it is), its
 * model names and what each serves, what it is — its name, type and API — and
 * deleting it. Its usage and limits, chats and requests are a page away.
 */
export function ClientOverview({
  client,
  types,
  config,
  onChanged,
}: {
  client: Client
  types: ClientTypes
  config: GatewayConfig | null
  onChanged: () => void
}) {
  return (
    <>
      <SetupCard client={client} types={types} config={config} onChanged={onChanged} />
      <SlotsCard client={client} types={types} onChanged={onChanged} />
      <AboutCard client={client} types={types} onChanged={onChanged} />
      <DeleteCard client={client} />
    </>
  )
}

/**
 * What the client is: its name, its type and API — a change of either shown
 * as what it does to the model names before it is made — and its switches.
 */
function AboutCard({ client: c, types, onChanged }: { client: Client; types: ClientTypes; onChanged: () => void }) {
  const [name, setName] = useState(c.name)
  const [typeId, setTypeId] = useState(c.type.id)
  const { busy, error, run } = useAction()
  const confirm = useConfirm()
  const k = c.wire

  const rename = () =>
    run('name', async () => {
      await api.clients.update(c.id, name.trim(), {
        rpmLimit: k.rpm_limit, ratePeriodS: k.rate_period_s, tokenBudget: k.token_budget,
      })
      onChanged()
    })

  // A change of type or API is planned by the server and shown before it is
  // made: which names stay (renamed where the new API needs a mark), which
  // are added following their defaults, and which go.
  const change = (type: string, dialect: DialectId) =>
    run('plan', async () => {
      const plan = await api.clients.plan(c.id, type, dialect)
      await confirm({
        title: type === c.type.id ? `Point ${c.name} at the ${types.dialectTitle(plan.dialect)} API?` :
          `Make ${c.name} a ${types.type(type).label} client?`,
        body: <ChangeSummary change={plan} />,
        confirmLabel: 'Apply',
        action: async () => {
          await api.clients.change(c.id, type, dialect)
          onChanged()
        },
      })
      setTypeId(c.type.id)
    })

  const switchChats = (on: boolean) =>
    run('chats', async () => {
      await api.clients.setNameChats(c.id, on)
      onChanged()
    })
  const setMode = (mode: 'binding' | 'direct') =>
    run('mode', async () => {
      await api.clients.setMode(c.id, mode)
      onChanged()
    })

  return (
    <Card title="Client">
      <Stack>
        <div className="flex flex-wrap items-center gap-3">
          <Field label="Name" value={name} onChange={setName} grow />
          <Button variant="outlined" busy={busy === 'name'} disabled={name.trim() === '' || name.trim() === c.name}
            onClick={() => void rename()}>
            Rename
          </Button>
        </div>
        <Note>The model names stay as they are: a rename never touches the client&rsquo;s configuration.</Note>

        <div className="flex flex-wrap items-center gap-3">
          <Select
            label="Type"
            value={typeId}
            width="fixed"
            onChange={(t) => {
              setTypeId(t)
              void change(t, c.dialect)
            }}
            options={types.all.map((t) => ({ value: t.id, label: t.label }))}
          />
          {c.type.choosesDialect && (
            <Segmented
              label="API"
              value={c.dialect}
              onChange={(d) => void change(c.type.id, d)}
              options={c.type.dialects.map((d) => ({ id: d, label: types.dialectTitle(d), content: types.dialectTitle(d) }))}
            />
          )}
        </div>
        {c.type.why !== '' && <Note>{c.type.why}</Note>}

        {c.type.custom && (
          <SettingSwitch
            checked={k.mode === 'direct'}
            onChange={(v) => void setMode(v ? 'direct' : 'binding')}
            disabled={busy !== ''}
            label="Name real models directly"
            info="For a tool with a model list of its own: it asks for real model ids, less the ones switched off on its Models page, and none of its model names. Switching back keeps both."
          />
        )}

        <SettingSwitch
          checked={k.name_chats}
          onChange={(v) => void switchChats(v)}
          disabled={busy !== ''}
          label="Name chats"
          info={
            c.type.namesItsChats
              ? `${c.type.label} names its own sessions: the gateway reads the name off its answer, which costs nothing.`
              : 'The gateway asks the chat’s own model for a name, under this client’s key and on the account that served the chat, so it is this client’s spend — and skips it when the client is at a limit.'
          }
        />
        <ErrorBanner error={error} />
      </Stack>
    </Card>
  )
}

/** What a change of type or API does to the model names, as the confirmation shows it. */
function ChangeSummary({ change }: { change: ClientChange }) {
  const kept = change.keep.map((k) => (k.renamed !== undefined && k.renamed !== '' ? `${k.name} → ${k.renamed}` : k.name))
  return (
    <Stack gap={2}>
      {kept.length > 0 && <p className="m-0">These model names stay, with what they serve: {kept.join(', ')}.</p>}
      {change.add.length > 0 && (
        <p className="m-0">Added, following their defaults: {change.add.map((a) => a.name).join(', ')}.</p>
      )}
      {change.remove.length > 0 && (
        <p className="m-0">Removed, and their names freed: {change.remove.map((r) => r.name).join(', ')}.</p>
      )}
      <Note>Re-run the install command on the client afterwards: its files name the new set.</Note>
    </Stack>
  )
}

/** Deleting the client: its key stops at once; its history stays under its name. */
function DeleteCard({ client: c }: { client: Client }) {
  const confirm = useConfirm()
  const { busy, error, run } = useAction()
  const remove = () =>
    run('delete', () =>
      confirm({
        title: `Delete ${c.name}?`,
        body: 'Its key stops working immediately, and its model names go with it.',
        confirmLabel: 'Delete client',
        tone: 'danger',
        action: async () => {
          await api.clients.remove(c.id)
          navigate('/clients')
        },
      }),
    )
  return (
    <>
      <DangerZone
        title="Delete this client"
        action={
          <Button variant="outlined" tone="error" disabled={busy !== ''} onClick={() => void remove()}>
            Delete {c.name}
          </Button>
        }
      >
        Its key stops working immediately, and it cannot be undone. Its usage stays, under the name it was
        recorded with.
      </DangerZone>
      <ErrorBanner error={error} />
    </>
  )
}
