import ConfigTable from './configtable'
import Security from './security'
import { type Session } from '../../api/client'
import { Card, CardTitle } from '../primitives'

/**
 * Settings: the admin account, how the gateway is configured, and the things
 * only a shell can do.
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
  return (
    <div className="flex flex-col gap-5">
      <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />

      <ConfigTable />

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
