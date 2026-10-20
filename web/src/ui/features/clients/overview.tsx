import { useState } from 'react'
import { api, type GatewayConfig } from '../../../api/client'
import { useAction } from '../../hooks'
import { navigate } from '../../route'
import {
  Button,
  Card,
  DangerZone,
  ErrorBanner,
  Field,
  KeyValue,
  Note,
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
 * What the client is: its name, its type and API, and its switches.
 *
 * The type and the API are shown, not offered: a client is made with them and
 * keeps them. Its key was handed out for that one client, and its model names
 * carry what its API needs, so another type or API is another client — made
 * on its own, with a key of its own.
 */
function AboutCard({ client: c, types, onChanged }: { client: Client; types: ClientTypes; onChanged: () => void }) {
  const [name, setName] = useState(c.name)
  const { busy, error, run } = useAction()
  const k = c.wire

  const rename = () =>
    run('name', async () => {
      await api.clients.update(c.id, name.trim(), {
        rpmLimit: k.rpm_limit, ratePeriodS: k.rate_period_s, tokenBudget: k.token_budget,
      })
      onChanged()
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

        <KeyValue
          items={[
            ['Type', c.type.label],
            ['API', types.dialectTitle(c.dialect)],
          ]}
        />
        <Note>
          A client keeps the type and API it was made with: its key was issued for it. For another, add a new
          client.
        </Note>

        {c.type.custom && (
          <SettingSwitch
            checked={k.mode === 'direct'}
            onChange={(v) => void setMode(v ? 'direct' : 'binding')}
            disabled={busy !== ''}
            label="Name real models directly"
            info="For a tool with a model list of its own: it asks for real model ids, less the ones switched off on its Models page, and none of its model names. Switching back keeps both."
          />
        )}

        {/* A type that names its own sessions has no switch: reading that name
            spends nothing, so there is nothing to turn off. The switch is for
            the gateway's own title request, which is this client's spend. */}
        {c.type.namesItsChats ? (
          <Note>
            {c.type.label} names its own sessions: the gateway reads the name off its answer, which costs nothing.
          </Note>
        ) : (
          <SettingSwitch
            checked={k.name_chats}
            onChange={(v) => void switchChats(v)}
            disabled={busy !== ''}
            label="Name chats"
            info="The gateway asks the chat’s own model for a name, under this client’s key and on the account that served the chat, so it is this client’s spend — and skips it when the client is at a limit."
          />
        )}
        <ErrorBanner error={error} />
      </Stack>
    </Card>
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
