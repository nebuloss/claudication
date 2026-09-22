import { useEffect, useState } from 'react'
import { api } from '../../api/client'
import { Banner, Card, CardTitle, Switch } from '../primitives'

/**
 * The switch for the public setup page.
 *
 * Off by default, and this is the one switch here whose default is about
 * posture rather than cost. The page is served at the root of the relay, which
 * otherwise answers 404 to everything that is not an API call — so turning it
 * on changes that address from silent-unless-you-have-a-key to
 * self-describing. It grants no access and carries no secret, but it is not a
 * thing that should start happening because someone upgraded.
 */
export default function PublicDocs({
  enabled,
  url,
  onChanged,
}: {
  enabled: boolean
  url: string
  onChanged: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [shown, setShown] = useState(enabled)

  useEffect(() => {
    setShown(enabled)
  }, [enabled])

  async function toggle(next: boolean) {
    setShown(next)
    setBusy(true)
    setError('')
    try {
      await api.setDocs(next)
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
      <CardTitle>Public setup page</CardTitle>

      <p className="mt-0 mb-4 text-sm text-on-surface-variant">
        The instructions for pointing a client at this gateway &mdash; the config files with its
        own address already in them. Served at the relay&rsquo;s root, where someone who has the
        address is already looking, and with no sign-in, so reading the instructions does not need
        the password that can add Claude accounts and mint keys.
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
          label="Serve the setup page"
        />
        <div className="mt-2 sm:ml-16">
          <p className="mt-0 mb-2 text-xs text-on-surface-variant">
            Off, that address answers 404 to everything again, as it does today.{' '}
            <strong className="font-medium text-on-surface">
              On, it tells anyone who asks that a claudication gateway is here
            </strong>{' '}
            and how to configure a client for it. Nothing on it is a secret and nothing on it can
            be changed &mdash; the relay&rsquo;s address, which APIs are on, whether an account is
            connected, and the model list &mdash; but a request still needs a key, and this page
            does not hand one out.
          </p>
          {url !== '' && (
            <p className="m-0 text-xs text-on-surface-variant">
              At{' '}
              <a
                href={url}
                target="_blank"
                rel="noreferrer"
                className="text-primary underline underline-offset-2"
              >
                {url}
              </a>
              . Set <code className="font-mono">docs-listen</code> to put it on an address of its
              own instead, for a deployment that wants the instructions somewhere{' '}
              <code className="font-mono">/v1</code> is not.
            </p>
          )}
        </div>
      </div>
    </Card>
  )
}
