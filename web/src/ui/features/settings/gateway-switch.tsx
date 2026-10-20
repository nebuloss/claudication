import { api } from '../../../api/client'
import { useAction } from '../../hooks'
import { Card, ErrorBanner, Note, SettingSwitch } from '../../primitives'

/**
 * The gateway switch: whether any client is served at all.
 *
 * One switch, not one per API. With each client pointed at the API its type
 * speaks, turning one API off was only ever a way to break some clients and
 * not others; what an operator reaches for at three in the morning is "stop
 * serving". Off is allowed and not second-guessed: the admin UI stays up,
 * which is how it is switched back on.
 */
export default function GatewaySwitch({ enabled, onChanged }: { enabled: boolean; onChanged: () => void }) {
  const { busy, error, run } = useAction()
  const set = (on: boolean) =>
    run('serving', async () => {
      await api.gateway.setServing(on)
      onChanged()
    })
  return (
    <Card title="Gateway">
      <SettingSwitch
        checked={enabled}
        disabled={busy !== ''}
        onChange={(v) => void set(v)}
        label="Serve clients"
        info="Every client-facing route: /v1/messages, count_tokens, /v1/models and /v1/responses. Takes effect on the next request; requests in flight finish."
      >
        {!enabled && (
          <Note tone="warn">
            Switched off. Every client is answered 404, in its own API&rsquo;s error format, so it
            reports it and stops rather than retrying against a wall. This page keeps working.
          </Note>
        )}
      </SettingSwitch>
      <ErrorBanner error={error} className="mt-4" />
    </Card>
  )
}
