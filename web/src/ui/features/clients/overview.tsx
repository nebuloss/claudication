import { api, type GatewayConfig } from '../../../api/client'
import { useAction } from '../../hooks'
import { clientHref, navigate } from '../../route'
import {
  AppLink,
  Button,
  Card,
  Chip,
  DangerZone,
  ErrorBanner,
  Note,
  SettingSwitch,
  Stack,
  useConfirm,
} from '../../primitives'
import { followLabel, providerTitle, type Client, type ClientTypes } from '../../clients/specs'
import { SetupCard } from './setup'

/**
 * A client at a glance, on its own path: how to set it up (or that it is), its
 * model names and what each serves, its switches, and deleting it. Its name,
 * type and API are the page's header, where it is renamed. Its usage and
 * limits, chats and requests are a page away, and so is changing what its
 * names serve: the Models page, beside the models it is allowed.
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
      <NamesSummary client={client} />
      <SettingsCard client={client} onChanged={onChanged} />
      <DeleteCard client={client} />
    </>
  )
}

/**
 * What each of the client's names serves now, read-only: the editor is on its
 * Models page, beside the switches that bound what the names may serve, and
 * a second copy of it here would be two places to change one thing.
 *
 * One row per name, in columns: the name the client asks for and what it is
 * for, the model it reaches and on which provider, and how it is bound —
 * following a family's releases, pinned, or unavailable and why. It was a
 * wrapped line of "name → id" pairs, which read as one run-on sentence once a
 * client had more than three names.
 */
function NamesSummary({ client: c }: { client: Client }) {
  const href = `${clientHref(c.id)}/models`
  return (
    <Card title="Model names" aside={<AppLink href={href}>Change on Models</AppLink>}>
      {!c.namesSlots ? (
        <p className="text-sm text-on-surface-variant">
          In direct mode: it names real models, the ones it is allowed on its <AppLink href={href}>Models</AppLink>{' '}
          page.
        </p>
      ) : c.slots.length === 0 ? (
        <p className="text-sm text-on-surface-variant">No model names yet.</p>
      ) : (
        <ul className="m-0 flex list-none flex-col divide-y divide-outline-variant p-0">
          {c.slots.map((s) => {
            const unavailable = s.unavailable ?? ''
            const follows = s.bound ?? ''
            return (
              <li
                key={s.id}
                className="grid grid-cols-1 gap-x-4 gap-y-1 py-2.5 text-sm sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] sm:items-center"
              >
                <span className="flex min-w-0 flex-col">
                  <code className="truncate font-medium text-on-surface">{s.name}</code>
                  <span className="truncate text-xs text-on-surface-variant">{s.label}</span>
                </span>
                <span className="flex min-w-0 items-center gap-2">
                  <code className="truncate text-on-surface">{s.serving || '—'}</code>
                  {s.provider !== undefined && <Chip>{providerTitle(s.provider)}</Chip>}
                </span>
                <span className="text-xs text-on-surface-variant sm:text-right">
                  {unavailable !== '' ? (
                    <Chip tone="error">{unavailable}</Chip>
                  ) : follows !== '' ? (
                    followLabel(follows, s.title).replace(/^Follow/, 'Follows')
                  ) : (
                    'Pinned'
                  )}
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}

/**
 * How the gateway treats the client's requests: its switches, and nothing
 * else.
 *
 * This was the "Client" card, which also held the name as a field and the
 * type and API as facts. The name is renamed where it is read now, in the
 * page's header (EditableTitle). The type and API are the header's subtitle:
 * a client keeps both for life — its key was issued for that one client, and
 * its model names carry what its API needs — so they are facts to read, not
 * settings, and a card restating them said nothing the header did not.
 */
function SettingsCard({ client: c, onChanged }: { client: Client; onChanged: () => void }) {
  const { busy, error, run } = useAction()
  const k = c.wire

  const switchChats = (on: boolean) =>
    run('chats', async () => {
      await api.clients.setNameChats(c.id, on)
      onChanged()
    })
  const switchImages = (on: boolean) =>
    run('images', async () => {
      await api.clients.setFitImages(c.id, on)
      onChanged()
    })
  const switchAgent = (on: boolean) =>
    run('agent', async () => {
      await api.clients.setAgentManage(c.id, on)
      onChanged()
    })
  const setMode = (mode: 'binding' | 'direct') =>
    run('mode', async () => {
      await api.clients.setMode(c.id, mode)
      onChanged()
    })

  return (
    <Card title="Settings">
      <Stack>
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

        {/* Each client's rather than the gateway's: whether it sends enough
            screenshots to cross the threshold, and whether the detail lost
            matters to it, is a fact about the client. Every type has it — any
            can send images — and it acts only where Claude serves, whose rule
            it is. */}
        <SettingSwitch
          checked={k.fit_images}
          onChange={(v) => void switchImages(v)}
          disabled={busy !== ''}
          label="Shrink images when a request carries more than 20 — Claude refuses images over 2000 px then"
          info="Only on a request Claude serves, and only the images over the limit; everything else goes up byte for byte. Images from earlier turns count, so a long conversation crosses the line on its own. It is the one change the gateway makes to what the model is shown, which is why it is off until switched on. The cost is bounded: Claude already downscales to 2576 px on 4.7 and later, and to 1568 px before."
        />

        {/* Off until the operator says so: with it, a model decides what this
            client runs. The read tools need nothing, and nothing here reaches
            its limits, its key, its type or any other client. */}
        <SettingSwitch
          checked={k.agent_manage}
          onChange={(v) => void switchAgent(v)}
          disabled={busy !== ''}
          label="Agent may manage this client — lets its agent switch its models and update its config through the gateway's MCP server"
          info="The agent inside this client reaches the gateway's MCP server with the client's own key. Without this it can read what the client serves, its limits and its chats, and name its chats. With it, it can also bind this client's model names to any model it is allowed on its Models page, add and remove names of its own, and make the client's keyless update command. Never its limits, its key, its switches, the accounts or another client. Every change it makes is in the request log, under this client."
        />
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
