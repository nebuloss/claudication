import { useState, type ReactNode } from 'react'
import { api, type GatewayConfig } from '../../../api/client'
import { useLoader } from '../../hooks'
import { type ClientPageId } from '../../route'
import { DetailHeader } from '../detail'
import { ChatsPage } from '../chats'
import RequestsPanel from '../requests'
import { KeyModels } from '../models/switches'
import { Card, Empty, ErrorBanner, Figures, Loading, Note, Page } from '../../primitives'
import { SlotsCard } from './slots'
import { UpdateConfigCard } from './setup'
import { Client } from '../../clients/specs'
import { ClientChips, EnableSwitch } from './index'
import { ClientOverview } from './overview'
import { KeyFigures } from './parts'
import { useClientTypes } from './types'
import { ClientUsage } from './usage'
import { READS } from '../../../api/live'
import { ClientMark } from '../../brands'

/**
 * One client, on its overview and the pages beside it: its usage and limits,
 * its chats, its requests, and its models — its names and what they serve,
 * and the models it is allowed.
 */
export function ClientPage({
  id,
  page,
  config,
}: {
  id: string
  page: ClientPageId
  /** The gateway's configuration, for the address its files name. */
  config: GatewayConfig | null
}) {
  const { types } = useClientTypes()
  const { data, error, loading, reload, refreshing, updatedAt } = useLoader(
    (signal) => api.clients.get(id, signal),
    { on: READS.clients, deps: [id] },
  )
  if ((loading && data === null) || types === null) return <Loading />
  if (data === null) {
    return (
      <Page>
        <ErrorBanner error={error} />
        <Empty>
          No such client — it may have been deleted. What it spent is still in the request log, under the
          name it was recorded with.
        </Empty>
      </Page>
    )
  }
  const client = new Client(data, types.type(data.type))
  const changed = () => void reload()

  const pages: Record<ClientPageId, () => ReactNode> = {
    '': () => <ClientOverview client={client} types={types} config={config} onChanged={changed} />,
    usage: () => <ClientUsage client={client} onChanged={changed} />,
    chats: () => <ChatsPage scope={{ key: client.id }} />,
    requests: () => <RequestsPanel scope={{ key: client.id }} />,
    models: () => <ClientModels client={client} config={config} onChanged={changed} />,
  }

  return (
    <Page>
      <DetailHeader
        kind="clients"
        id={client.id}
        title={client.name}
        mark={<ClientMark type={data.type} />}
        rename={{
          value: client.name,
          label: 'Client name',
          // The key's limits go back as they are: the endpoint takes the
          // whole of what a key is, and a rename is not a change to them.
          // The model names stay too — a rename never touches the client's
          // configuration.
          onSave: async (name) => {
            const k = client.wire
            await api.clients.update(client.id, name, {
              rpmLimit: k.rpm_limit, ratePeriodS: k.rate_period_s, tokenBudget: k.token_budget,
            })
            changed()
          },
        }}
        subtitle={`${client.type.label} · ${types.apiTitle(client.type)}`}
        aside={
          <Figures>
            <ClientChips client={client} />
            <KeyFigures k={client.wire} always={false} />
            <EnableSwitch key={String(client.wire.disabled)} client={client.wire} onChanged={changed} />
          </Figures>
        }
        freshness={{ updatedAt, refreshing }}
      />
      <ErrorBanner error={error} />
      {pages[page]()}
    </Page>
  )
}

/**
 * Its names and the models they may serve, on one page: the switches below
 * bound the menus above, so changing one is seen in the other. Between them
 * the update command that takes the names to the machine the client is set
 * up on, naming the ones added or renamed here since it last ran.
 */
function ClientModels({
  client,
  config,
  onChanged,
}: {
  client: Client
  config: GatewayConfig | null
  onChanged: () => void
}) {
  const [named, setNamed] = useState<{ names: string[]; at: number }>({ names: [], at: 0 })
  const used = client.wire.update.used
  // An update run since is carrying them now.
  const pending = used !== undefined && new Date(used.at).getTime() >= named.at ? [] : named.names
  const onNamed = (name: string) =>
    setNamed(() => ({ names: [...pending.filter((n) => n !== name), name], at: Date.now() }))
  return (
    <>
      <SlotsCard client={client} onChanged={onChanged} onNamed={onNamed} />
      <UpdateConfigCard client={client} config={config} pending={pending} onChanged={onChanged} />
      <Card title="Allowed models">
        <Note className="mb-4">
          The agent can only switch to allowed models. A model unchecked here is out of this client&rsquo;s
          menus above, out of what its agent sees through the gateway&rsquo;s MCP server and may bind, and
          refused for it{client.type.custom ? ', named directly or through a name' : ''}; a name bound to it
          shows as unavailable until it is allowed again or rebound. Every other client is unaffected.
        </Note>
        <KeyModels apiKey={client.wire} onChanged={onChanged} />
      </Card>
    </>
  )
}
