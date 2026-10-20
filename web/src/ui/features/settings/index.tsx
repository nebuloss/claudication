import ConfigTable from './config-table'
import GatewaySwitch from './gateway-switch'
import Security from './security'
import { type GatewayConfig, type Session } from '../../../api/client'
import { Card, Empty, Loading, Page } from '../../primitives'

/**
 * Settings: one page, with nothing under it — whether the gateway serves, the
 * admin account, and the configuration as loaded, in that order.
 *
 * One page because that is all that is left of the gateway's own settings.
 * Model names, chat naming and image fitting are each client's, on its page,
 * and what remains is a switch, an account and a read-only table: a sub-page
 * apiece was a click and a Back more than any of them is worth. The order is
 * how often each is reached for — the switch first, because it is the one
 * wanted in a hurry; the table last, because it is long and only read.
 *
 * The configuration is loaded by the shell, because the same request carries
 * the switch and the settings rows, and the header reads it too.
 */
export default function Settings({
  session,
  onSessionChanged,
  config,
  onConfigChanged,
}: {
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
      {configured('Gateway', (d) => (
        <GatewaySwitch enabled={d.gateway_enabled} onChanged={() => void onConfigChanged()} />
      ))}
      <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />
      {configured('Configuration', (d) => (
        <ConfigTable config={d} />
      ))}
    </Page>
  )
}
