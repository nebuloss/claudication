import ConfigTable from './config-table'
import Diagnostics from './diagnostics'
import GatewaySwitch from './gateway-switch'
import McpSettings from './mcp'
import ModelChecks from './model-checks'
import ModelSettings from './models'
import Security from './security'
import { type GatewayConfig, type Session } from '../../../api/client'
import { type SettingsPageId } from '../../route'
import { Card, Empty, Loading, Page } from '../../primitives'

/**
 * Settings: the gateway's own (route.ts, SETTINGS_PAGES).
 *
 *   /settings      whether the gateway serves, the MCP server's switch and
 *                  address, and the admin account
 *   Models         whether the gateway checks models itself; each
 *                  provider's mode, its families and who serves which
 *   Configuration  the configuration as loaded, read-only
 *   Diagnostics    how changes reach the screens: topics, tabs, cached reads
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
  page: SettingsPageId | ''
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
      {page === '' && (
        <>
          {configured('Gateway', (d) => (
            <GatewaySwitch enabled={d.gateway_enabled} onChanged={() => void onConfigChanged()} />
          ))}
          {configured('MCP server', (d) => (
            <McpSettings config={d} onChanged={() => void onConfigChanged()} />
          ))}
          <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />
        </>
      )}
      {page === 'models' && (
        <>
          {configured('Model checks', (d) => (
            <ModelChecks enabled={d.model_checks_enabled} onChanged={() => void onConfigChanged()} />
          ))}
          <ModelSettings />
        </>
      )}
      {page === 'configuration' && configured('Configuration', (d) => <ConfigTable config={d} />)}
      {page === 'diagnostics' && <Diagnostics />}
    </Page>
  )
}
