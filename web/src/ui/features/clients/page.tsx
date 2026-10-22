import { type ReactNode } from 'react'
import { api, type GatewayConfig } from '../../../api/client'
import { useLoader } from '../../hooks'
import { type ClientPageId } from '../../route'
import { DetailHeader } from '../detail'
import { ChatsPage } from '../chats'
import RequestsPanel from '../requests'
import { KeyModels } from '../models/switches'
import { Card, Empty, ErrorBanner, Figures, Loading, Note, Page } from '../../primitives'
import { SlotsCard } from './slots'
import { Client } from '../../clients/specs'
import { ClientChips, EnableSwitch } from './index'
import { ClientOverview } from './overview'
import { KeyFigures } from './parts'
import { useClientTypes } from './types'
import { ClientUsage } from './usage'

/**
 * How often the overview re-reads its client: its install link's state and
 * whether it has been set up are what change while it is open — a command
 * run on another machine — and the page has to say so without a reload.
 */
const LIVE_MS = 4_000

/** The other pages read it less often: their own data polls on its own. */
const QUIET_MS = 20_000

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
  const { data, error, loading, reload, refreshing, updatedAt, live } = useLoader(
    (signal) => api.clients.get(id, signal),
    { refreshMs: page === '' ? LIVE_MS : QUIET_MS, deps: [id] },
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
    // Its names and the models they may serve, on one page: the switches
    // below bound the menus above, so changing one is seen in the other.
    models: () => (
      <>
        <SlotsCard client={client} onChanged={changed} />
        <Card title="Allowed models">
          <Note className="mb-4">
            The agent can only switch to allowed models. A model unchecked here is out of this client&rsquo;s
            menus above, out of what its agent sees through the gateway&rsquo;s MCP server and may bind, and
            refused for it{client.type.custom ? ', named directly or through a name' : ''}; a name bound to it
            shows as unavailable until it is allowed again or rebound. Every other client is unaffected.
          </Note>
          <KeyModels apiKey={client.wire} onChanged={changed} />
        </Card>
      </>
    ),
  }

  return (
    <Page>
      <DetailHeader
        kind="clients"
        id={client.id}
        title={client.name}
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
        freshness={{ updatedAt, refreshing, live, onRefresh: changed }}
      />
      <ErrorBanner error={error} />
      {pages[page]()}
    </Page>
  )
}
