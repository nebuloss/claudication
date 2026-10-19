import { api } from '../../api/client'
import { useAction, useOptimistic } from '../hooks'
import { Card, ErrorBanner, InlineLink, Note, SettingSwitch } from '../primitives'

/**
 * The switch for the public docs page.
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
  const { pending: busy, error, run } = useAction()
  const [shown, apply] = useOptimistic(enabled)

  const toggle = (next: boolean) =>
    apply(next, () =>
      run('toggle', async () => {
        await api.gateway.setDocs(next)
        onChanged()
      }),
    )

  return (
    <Card
      info={
        <p>
          The instructions for pointing a client at this gateway &mdash; the config files with its
          own address already in them. Served at the relay&rsquo;s root, where someone who has the
          address is already looking, and with no sign-in, so reading the instructions does not need
          the password that can add Claude accounts and mint keys.
        </p>
      }
    >
      <ErrorBanner error={error} className="mb-4" />
      <SettingSwitch
        checked={shown}
        disabled={busy}
        onChange={(v) => void toggle(v)}
        label="Serve the docs page"
        info={
          <p>
            Off, that address answers 404 to everything again.{' '}
            <strong className="font-medium text-on-surface">
              On, it tells anyone who asks that a claudication gateway is here
            </strong>{' '}
            and how to configure a client for it. Nothing on it is a secret and nothing on it can
            be changed &mdash; the relay&rsquo;s address, which APIs are on, whether an account is
            connected, and the model list &mdash; but a request still needs a key, and this page
            does not hand one out.
          </p>
        }
      >
        {url !== '' && (
          <Note>
            At{' '}
            <strong className="font-medium">
              <InlineLink href={url}>{url}</InlineLink>
            </strong>
            . Set <code>docs-listen</code> to put it on an address of its own instead, for a
            deployment that wants the instructions somewhere <code>/v1</code> is not.
          </Note>
        )}
      </SettingSwitch>
    </Card>
  )
}
