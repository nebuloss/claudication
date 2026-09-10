import Security from './security'
import { api, type Overview, type Session } from '../../api/client'
import { useLoader } from '../hooks'
import { Card, CardTitle, KeyValue } from '../primitives'

/** Settings: the admin account, and the facts about this instance. */
export default function Settings({
  session,
  onSessionChanged,
}: {
  session: Session
  onSessionChanged: () => void
}) {
  const { data } = useLoader<Overview>(() => api.overview())

  return (
    <div className="flex flex-col gap-5">
      <Security session={session} onChanged={onSessionChanged} onDeleted={onSessionChanged} />

      <Card>
        <CardTitle>Instance</CardTitle>
        <div className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
        <KeyValue
          items={[
            // Version lives in the header now, where it is visible from every
            // screen. The commit stays: it is what identifies a build between
            // two releases, and it is not worth a line in the header.
            ['Commit', <span className="font-mono text-xs">{data?.commit ?? '…'}</span>],
            [
              'Relay',
              <span className="font-mono text-xs">{data?.listen ?? '…'}</span>,
            ],
            // Whether the admin surface is separated is the one setting worth
            // seeing at a glance on an exposed gateway: it decides whether
            // publishing the relay publishes this page too.
            [
              'Admin',
              data === null ? (
                '…'
              ) : data.admin_split === true ? (
                <span className="text-success">on its own listener</span>
              ) : (
                <span>
                  shares the relay listener —{' '}
                  <span className="text-on-surface-variant">
                    set <code>admin-listen</code> if this gateway is exposed
                  </span>
                </span>
              ),
            ],
            [
              'Public URL',
              data === null ? (
                '…'
              ) : data.public_url ? (
                <span className="font-mono text-xs">{data.public_url}</span>
              ) : (
                <span className="text-on-surface-variant">
                  unset — the Overview page guesses from your browser's address
                </span>
              ),
            ],
            [
              'History',
              data === null
                ? '…'
                : data.usage_enabled
                  ? 'per-request, kept as configured'
                  : 'off (usage.retention-days: 0)',
            ],
          ]}
        />
        </div>
        <p className="mt-4 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          Configuration is read from config.yaml and never written back, so the file keeps its
          comments. Credentials live in the state database, not in config.
        </p>
      </Card>

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
