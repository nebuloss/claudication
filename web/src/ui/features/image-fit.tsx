import { useEffect, useState } from 'react'
import { api } from '../../api/client'
import { Banner, Card, CardTitle, Switch } from '../primitives'

/**
 * The switch for capping oversized images.
 *
 * Its own card rather than a row in the configuration table, for the reason
 * the API switches and the chat-name switches have one: this decides whether
 * the relay may change what the model is shown, and that is a decision, not a
 * setting.
 *
 * Off by default, and the copy says what it costs rather than only what it
 * fixes. Every other rewrite the relay makes repairs an envelope the upstream
 * refuses over its shape; this one re-encodes the caller's own picture.
 */
export default function ImageFit({
  enabled,
  onChanged,
}: {
  enabled: boolean
  onChanged: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Leads the server rather than following it, like the chat-name switches: a
  // control that sits where you did not put it until a write and a refetch
  // have both returned reads as a missed click.
  const [shown, setShown] = useState(enabled)

  useEffect(() => {
    setShown(enabled)
  }, [enabled])

  async function toggle(next: boolean) {
    setShown(next)
    setBusy(true)
    setError('')
    try {
      await api.setFitImages(next)
      onChanged()
    } catch (e) {
      setShown(!next)
      setError(e instanceof Error ? e.message : 'Could not change the setting.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardTitle>Oversized images</CardTitle>

      <p className="mt-0 mb-4 text-sm text-on-surface-variant">
        A request carrying more than 20 images is held to a stricter limit: every image in it must
        be 2000&nbsp;px or less on a side. Images from earlier turns count, so a conversation that
        has been fine for hours starts being refused over a screenshot already in its history.
      </p>

      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      <div className="rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
        <Switch
          checked={shown}
          disabled={busy}
          onChange={(v) => void toggle(v)}
          label="Shrink images that would be refused"
        />
        <div className="mt-2 sm:ml-16">
          <p className="mt-0 mb-2 text-xs text-on-surface-variant">
            Only when a request carries more than 20 images, and only the ones over the limit —
            everything else is relayed untouched, byte for byte.{' '}
            <strong className="font-medium text-on-surface">
              This is the one thing the gateway changes about what the model is shown
            </strong>
            , rather than about the shape of the request, which is why it is off until you ask for
            it.
          </p>
          <p className="m-0 text-xs text-on-surface-variant">
            What it costs is bounded: Claude already downscales to a 2576&nbsp;px long edge on 4.7
            and later, and to 1568&nbsp;px before that — so capping at 2000 loses at most the band
            between the two, and on an older model loses nothing. The rescale happens once per
            image and is remembered, so a conversation that resends its history does not pay for it
            again.
          </p>
        </div>
      </div>
    </Card>
  )
}
