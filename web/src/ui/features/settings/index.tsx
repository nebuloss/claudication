import ConfigTable from './config-table'
import GatewaySwitch from './gateway-switch'
import McpSettings from './mcp'
import ModelSettings from './models'
import Security from './security'
import { type GatewayConfig, type Session } from '../../../api/client'
import { type SettingsPageId } from '../../route'
import { Card, Empty, Loading, Page } from '../../primitives'

/**
 * Settings: the gateway's own, a page each (route.ts, SETTINGS_PAGES).
 *
 *   General        whether the gateway serves, and the admin account
 *   Models         each provider's mode, its families and who serves which
 *   MCP            the MCP server's switch and address
 *   Configuration  the configuration as loaded, read-only
 *
 * The configuration is loaded by the shell, because the same request carries
 * the switches and the settings rows, and the header reads it too.
 */
export default function Settings({
  page,
  session,
  onSessionChanged,
  config,
  onConfigChanged,
}: {
  page: SettingsPageId
  session: Session
  onSessionChanged: () => void
  /**
   * Loaded by the shell rather than here: the header carries a link that the
   * switch turns on, and with two copies of the configuration the card knew
   * it had changed and the header did not.
   */
  config: { data: GatewayConfig | null; error: string; loading: boolean }
  onConfigChanged: () => void
}) {
  const { data, error, loading } = config

  const configured = (title: string, render: (d: GatewayConfig) => JSX.Element) => {
    if (loading && data === null) {
      return (
        <Card title={title}>
          <Loading />
        </Card>
      )
    }
    if (error !== '' || data === null) {
      return (
        <Card title={title}>
          <Empty>Could not read the configuration.</Empty>
        </Card>
      )
    }
    return render(data)
  }

  return (
    <Page>
      {page === 'general' && (
        <>
          {configured('Gateway', (d) => (
            <GatewaySwitch enabled={d.gateway_enabled} onChanged={() => void onConfigChanged()} />
          ))}
          <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />
        </>
      )}
      {page === 'models' && <ModelSettings />}
      {page === 'mcp' &&
        configured('MCP server', (d) => <McpSettings config={d} onChanged={() => void onConfigChanged()} />)}
      {page === 'configuration' && configured('Configuration', (d) => <ConfigTable config={d} />)}
    </Page>
  )
}
