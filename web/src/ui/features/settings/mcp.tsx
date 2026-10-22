import { api, type GatewayConfig } from '../../../api/client'
import { relayAddress } from '../../clients/address'
import { useAction, useLoader } from '../../hooks'
import { Card, CopyField, ErrorBanner, Muted, Note, SettingSwitch, Stack } from '../../primitives'

/**
 * The gateway's MCP server: its switch, and where it is.
 *
 * Off, /mcp answers every client 404, as a switched-off gateway does (the
 * server's reasons are in internal/httpapi/agent): a client reports a server
 * that failed to connect and goes on without its tools. On, each client's
 * own "Agent may manage this client" switch still decides whether its agent
 * may change it; reading is every client's. The clients' files keep their
 * MCP entry either way, so turning it back on needs nothing reinstalled.
 */
export default function McpSettings({ config, onChanged }: { config: GatewayConfig; onChanged: () => void }) {
  const { busy, error, run } = useAction()
  // Where the server is, from the server's one copy of its path.
  const types = useLoader((signal) => api.clients.types(signal))
  const set = (on: boolean) =>
    run('mcp', async () => {
      await api.gateway.setMCP(on)
      onChanged()
    })
  const base = relayAddress(config)
  const path = types.data?.mcp_path ?? '/mcp'
  const on = config.mcp_enabled

  return (
    <Card title="MCP server">
      <Stack gap={4}>
        <SettingSwitch
          checked={on}
          disabled={busy !== ''}
          onChange={(v) => void set(v)}
          label="Serve the MCP server"
          info="The tools a client's own agent calls about the client it runs in: what its model names serve, what it may switch them to, its limits, usage and chats. Each client reaches it with its own key."
        >
          {on ? (
            <Note>
              On. Every client may read its own: its names and what they serve, its limits and its chats. Changing
              itself — rebinding a name, adding one, updating its config — still needs that client&rsquo;s{' '}
              <em>Agent may manage this client</em>, on its page, which is off until you turn it on.
            </Note>
          ) : (
            <Note tone="warn">
              Switched off. <code>{path}</code> answers every client 404, whatever its <em>Agent may manage</em>{' '}
              switch says; a client reports the server as failed to connect and goes on without its tools. Clients
              keep the server in their files, so turning it back on needs nothing reinstalled: a new session
              connects again.
            </Note>
          )}
        </SettingSwitch>
        {base !== '' ? (
          <CopyField label="Endpoint (Streamable HTTP, Authorization: Bearer with the client's key)" value={`${base}${path}`} />
        ) : (
          <Muted size="xs">
            At <code>{path}</code> on the relay&rsquo;s address, which this page cannot see while the admin UI has
            a listener of its own: set <code>public-url</code> to have it shown here.
          </Muted>
        )}
        <ErrorBanner error={error} />
      </Stack>
    </Card>
  )
}
