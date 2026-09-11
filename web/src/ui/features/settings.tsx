import ConfigTable from './configtable'
import Security from './security'
import Surfaces from './surfaces'
import { api, type GatewayConfig, type Session } from '../../api/client'
import { useLoader } from '../hooks'
import { Card, Empty, Spinner } from '../primitives'

/**
 * Settings: the admin account, which APIs are being served, how the gateway is
 * configured, and the things only a shell can do.
 *
 * The configuration is loaded here rather than inside the table, because the
 * same request carries the API switches and the settings rows. One fetch, one
 * error state, and a switch that reloads both — so the switch card and the
 * provenance column can never disagree about what is in effect.
 *
 * There used to be an "Instance" card above the configuration table with the
 * listen address, the admin split and the public URL in it. Every one of those
 * is a configuration value, so once the table started reporting all of them —
 * with where each came from, which the card could not say — the card was four
 * rows of the same facts and a note that had moved too.
 *
 * The shell commands that used to sit at the bottom have gone to Setup, which
 * is where someone looks when they are trying to make something work rather
 * than trying to change how it is configured.
 */
export default function Settings({
  session,
  onSessionChanged,
}: {
  session: Session
  onSessionChanged: () => void
}) {
  const { data, error, loading, reload } = useLoader<GatewayConfig>(() => api.config())

  return (
    <div className="flex flex-col gap-5">
      <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />

      {loading && data === null ? (
        <Card>
          <p className="flex items-center gap-2 text-sm text-on-surface-variant">
            <Spinner /> Loading…
          </p>
        </Card>
      ) : error !== '' || data === null ? (
        <Card>
          <Empty>Could not read the configuration.</Empty>
        </Card>
      ) : (
        <>
          <Surfaces surfaces={data.surfaces} onChanged={() => void reload()} />
          <ConfigTable config={data} />
        </>
      )}
    </div>
  )
}
