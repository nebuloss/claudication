import { api, type Surface } from '../../../api/client'
import { useAction } from '../../hooks'
import { Banner, Card, ErrorBanner, Note, SettingSwitch, Stack } from '../../primitives'

/**
 * The switches for the client-facing APIs.
 *
 * Its own card, above the configuration table, because these are the only
 * settings on this screen that can be changed here: everything else comes from
 * a file the gateway never writes back. A switch and a read-only row make
 * different promises, and putting them in one table would make the read-only
 * rows look editable.
 *
 * Turning every API off is allowed and is not treated as a mistake. It is what
 * "stop serving anything, now" means, and a control that talks you out of the
 * state you asked for is not a control. The warning says what the state does,
 * and then gets out of the way.
 */
export default function Surfaces({
  surfaces,
  onChanged,
}: {
  surfaces: Surface[]
  onChanged: () => void
}) {
  const { busy, error, run } = useAction()

  const toggle = (id: string, enabled: boolean) =>
    run(id, async () => {
      await api.gateway.setSurface(id, enabled)
      onChanged()
    })

  const allOff = surfaces.length > 0 && surfaces.every((s) => !s.enabled)

  return (
    <Card
      info={
        <p>
          Which APIs this gateway answers on. A change takes effect on the next request — nothing
          restarts, and requests already in flight are left alone.
        </p>
      }
    >
      <ErrorBanner error={error} className="mb-4" />

      {allOff && (
        <Banner tone="warn" className="mb-4">
          Every API is off. The gateway still listens and the admin UI still works, but no client
          can get an answer from it.
        </Banner>
      )}

      <Stack gap={3}>
        {surfaces.map((s) => (
          <SettingSwitch
            key={s.id}
            checked={s.enabled}
            disabled={busy !== ''}
            onChange={(v) => void toggle(s.id, v)}
            label={s.title}
            // The paths it governs, where a switch's explanation goes.
            info={
              <div className="flex flex-wrap gap-x-3 gap-y-1">
                {s.routes.map((r) => (
                  <code key={r} className="text-[11px] text-on-surface-variant">
                    {r}
                  </code>
                ))}
              </div>
            }
          >
            {!s.enabled && (
              <Note>
                Requests to these paths are answered 404, in this API&rsquo;s own error format, so a
                client reports it and stops rather than retrying against a wall.
              </Note>
            )}
          </SettingSwitch>
        ))}
      </Stack>
    </Card>
  )
}
