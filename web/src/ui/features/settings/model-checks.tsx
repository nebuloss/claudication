import { api } from '../../../api/client'
import { useAction } from '../../hooks'
import { Card, ErrorBanner, Note, SettingSwitch } from '../../primitives'

/**
 * Whether the gateway checks each account's models itself
 * (internal/service/availability): every model when an account is added,
 * and a failing one again every quarter of an hour. Each check is one short
 * request on the account, a few tokens of its subscription — what the switch
 * is for. Off, a model is still marked failing or working by client traffic
 * and by the Test button, and still routed around while failing.
 */
export default function ModelChecks({ enabled, onChanged }: { enabled: boolean; onChanged: () => void }) {
  const { busy, error, run } = useAction()
  const set = (on: boolean) =>
    run('checks', async () => {
      await api.gateway.setModelChecks(on)
      onChanged()
    })
  return (
    <Card title="Model checks">
      <SettingSwitch
        checked={enabled}
        disabled={busy !== ''}
        onChange={(v) => void set(v)}
        label="Check models automatically"
        info="Each account's models are tried when it is added, and a failing one again every 15 minutes, with one short request each. A model failing on an account is routed to another account that has it working."
      >
        {!enabled && (
          <Note>
            Switched off: nothing is checked. Client requests still mark a model failing or working on an account,
            and the Test button on an account&rsquo;s Models page still does, and a failing model is still routed
            around.
          </Note>
        )}
      </SettingSwitch>
      <ErrorBanner error={error} className="mt-4" />
    </Card>
  )
}
