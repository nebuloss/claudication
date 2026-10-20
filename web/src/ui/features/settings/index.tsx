import ConfigTable from './config-table'
import GatewaySwitch from './gateway-switch'
import ImageFit from './image-fit'
import Security from './security'
import { type GatewayConfig, type Session } from '../../../api/client'
import { Card, Empty, Loading, Page } from '../../primitives'
import type { SettingsSectionId } from '../../route'

/**
 * Settings: the gateway switch and the admin account on /settings itself, and
 * a page per other section (/settings/<section>) — image fitting and the
 * configuration as loaded.
 *
 * Model names and chat naming are not here: both are each client's, on its
 * page. The configuration is loaded by the shell, because the same request
 * carries the switches and the settings rows, and the header reads it too.
 */
export default function Settings({
  section,
  session,
  onSessionChanged,
  config,
  onConfigChanged,
}: {
  section: SettingsSectionId
  session: Session
  onSessionChanged: () => void
  /**
   * Loaded by the shell rather than here: the header carries a link that one
   * of these switches turns on, and with two copies of the configuration the
   * card knew it had changed and the header did not.
   */
  config: { data: GatewayConfig | null; error: string; loading: boolean }
  onConfigChanged: () => void
}) {
  const { data, error, loading } = config
  const reload = onConfigChanged

  const configured = (render: (d: GatewayConfig) => JSX.Element) => {
    if (loading && data === null) {
      return (
        <Card>
          <Loading />
        </Card>
      )
    }
    if (error !== '' || data === null) {
      return (
        <Card>
          <Empty>Could not read the configuration.</Empty>
        </Card>
      )
    }
    return render(data)
  }

  const page = (() => {
    switch (section) {
      // The settings' own page: whether the gateway serves at all, and the
      // admin account, its password and its sessions.
      case '':
        return (
          <>
            {configured((d) => (
              <GatewaySwitch enabled={d.gateway_enabled} onChanged={() => void reload()} />
            ))}
            <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />
          </>
        )
      case 'images':
        return configured((d) => <ImageFit enabled={d.fit_images} onChanged={() => void reload()} />)
      case 'configuration':
        return configured((d) => <ConfigTable config={d} />)
    }
  })()

  return <Page>{page}</Page>
}
