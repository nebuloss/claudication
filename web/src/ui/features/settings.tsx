import ConfigTable from './configtable'
import Security from './security'
import Surfaces from './surfaces'
import { api, type GatewayConfig, type Session } from '../../api/client'
import { useLoader } from '../hooks'
import { Card, CardTitle, Empty, Spinner } from '../primitives'

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

      <Card>
        <CardTitle>From the shell</CardTitle>
        <dl className="m-0 flex flex-col gap-3">
          {[
            [
              'claudication passwd',
              'Reset the admin password. The recovery path when it is lost — no old password ' +
                'needed, since shell access to the state directory is already the higher privilege.',
            ],
            [
              'claudication login-url',
              'A single-use link that signs a browser in without typing the password. Spent the ' +
                'first time it is used.',
            ],
            [
              'claudication keys add -name NAME',
              'The same thing the API keys tab does, for a provisioning script.',
            ],
          ].map(([cmd, what]) => (
            <div
              key={cmd}
              className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3"
            >
              <dt className="font-mono text-xs text-on-surface">{cmd}</dt>
              <dd className="m-0 mt-1 text-sm text-on-surface-variant">{what}</dd>
            </div>
          ))}
        </dl>
      </Card>
    </div>
  )
}
