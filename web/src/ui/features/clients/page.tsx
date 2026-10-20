import { useEffect, type ReactNode } from 'react'
import { api, type GatewayConfig } from '../../../api/client'
import { useLoader } from '../../hooks'
import { omitPages } from '../../names'
import { type ClientPageId } from '../../route'
import { DetailHeader } from '../detail'
import { ChatsPage } from '../chats'
import RequestsPanel from '../requests'
import { KeyModels } from '../models/switches'
import { Card, Empty, ErrorBanner, Figures, Loading, Note, Page } from '../../primitives'
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
 * its chats, its requests, and — for a custom client — the models a direct
 * mode lets it name.
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
  const custom = data !== null && types !== null && types.type(data.type).custom
  useEffect(() => {
    if (data !== null) omitPages('clients', id, custom ? [] : ['models'])
  }, [data, custom, id])

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
    models: () =>
      client.type.custom ? (
        <Card title="Models it may name in direct mode">
          {client.namesSlots && (
            <Note className="mb-4">
              This client is in binding mode and names its own model names; these switches apply once
              it is switched to direct mode, and are kept until then.
            </Note>
          )}
          <KeyModels apiKey={client.wire} onChanged={changed} />
        </Card>
      ) : (
        <Card>
          <Empty>A {client.type.label} client names its own model names; they are on its overview.</Empty>
        </Card>
      ),
  }

  return (
    <Page>
      <DetailHeader
        kind="clients"
        id={client.id}
        title={client.name}
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
