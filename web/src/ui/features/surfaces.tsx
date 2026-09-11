import { useState } from 'react'
import { api, type Surface } from '../../api/client'
import { Banner, Card, CardTitle, Switch } from '../primitives'

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
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  async function toggle(id: string, enabled: boolean) {
    setBusy(id)
    setError('')
    try {
      await api.setSurface(id, enabled)
      onChanged()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not change the setting.')
    } finally {
      setBusy('')
    }
  }

  const allOff = surfaces.length > 0 && surfaces.every((s) => !s.enabled)

  return (
    <Card>
      <CardTitle>API surfaces</CardTitle>

      <p className="mt-0 mb-4 text-sm text-on-surface-variant">
        Which APIs this gateway answers on. A change takes effect on the next request — nothing
        restarts, and requests already in flight are left alone.
      </p>

      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      {allOff && (
        <Banner tone="warn" className="mb-4">
          Every API is off. The gateway still listens and the admin UI still works, but no client
          can get an answer from it.
        </Banner>
      )}

      <div className="flex flex-col gap-3">
        {surfaces.map((s) => (
          <div
            key={s.id}
            className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3"
          >
            <Switch
              checked={s.enabled}
              disabled={busy !== ''}
              onChange={(v) => void toggle(s.id, v)}
              label={s.title}
            />
            <div className="mt-2 flex flex-wrap gap-x-3 gap-y-1 sm:ml-16">
              {s.routes.map((r) => (
                <code key={r} className="font-mono text-[11px] text-on-surface-variant">
                  {r}
                </code>
              ))}
            </div>
            {!s.enabled && (
              <p className="mt-2 mb-0 text-xs text-on-surface-variant sm:ml-16">
                Requests to these paths are answered 404, in this API&rsquo;s own error format, so a
                client reports it and stops rather than retrying against a wall.
              </p>
            )}
          </div>
        ))}
      </div>
    </Card>
  )
}
