import { useState } from 'react'
import { api, messageOf, type Account, type OAuthStart } from '../../api/client'
import { Banner, Card, CardTitle, Field, FilledButton, Spinner, TextButton } from '../primitives'

type Phase = 'idle' | 'starting' | 'waiting' | 'exchanging'

/**
 * The Claude OAuth login, driven from the browser.
 *
 * "Start" opens the consent screen in a new tab; the operator approves, and an
 * authorization code comes back to be pasted here.
 *
 * Anthropic renders the code on its own page and it is pasted back here. The
 * client also has a loopback variant for its own local listener, but nothing
 * listens on the operator's machine, so that only ever produced an address bar
 * to copy from.
 */
export default function AddAccount({
  onAdded,
  onCancel,
}: {
  onAdded: (a: Account) => void
  onCancel: () => void
}) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [start, setStart] = useState<OAuthStart | null>(null)
  const [pasted, setPasted] = useState('')
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)

  /**
   * Deliberately not an async function. The tab has to be opened inside the
   * click gesture: a window.open issued after an await counts as unsolicited
   * and every browser blocks it. So the tab opens blank and is navigated once
   * the server has minted the PKCE challenge.
   *
   * "noopener" is absent on purpose — with it, window.open returns null by
   * spec and there is no handle left to navigate. The opener reference is
   * severed by hand instead.
   */
  const begin = () => {
    const tab = window.open('about:blank', '_blank')
    setPhase('starting')
    setError('')
    setBlocked(false)

    api
      .startOAuth('anthropic')
      .then((s) => {
        setStart(s)
        setPhase('waiting')
        if (tab && !tab.closed) {
          try {
            tab.opener = null
          } catch {
            // Some browsers make opener read-only; navigation still works.
          }
          tab.location.replace(s.auth_url)
        } else {
          setBlocked(true)
        }
      })
      .catch((err: unknown) => {
        tab?.close()
        setError(messageOf(err))
        setPhase('idle')
      })
  }

  const finish = async (e: React.FormEvent) => {
    e.preventDefault()
    if (start === null) return
    if (pasted.trim() === '') {
      setError('Paste the authorization code, or the whole callback URL.')
      return
    }
    setPhase('exchanging')
    setError('')
    try {
      const account = await api.completeOAuth('anthropic', start.state, pasted.trim())
      reset()
      onAdded(account)
    } catch (err) {
      setError(messageOf(err))
      setPhase('waiting')
    }
  }

  const reset = () => {
    setPhase('idle')
    setStart(null)
    setPasted('')
    setError('')
    setBlocked(false)
  }

  return (
    <Card>
      <CardTitle aside={<TextButton onClick={onCancel}>Cancel</TextButton>}>
        Add a Claude account
      </CardTitle>

      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      {start === null ? (
        <>
          <p className="mt-0 mb-4 text-sm text-on-surface-variant">
            Opens the Claude consent screen in a new tab and authorises this gateway using OAuth
            with PKCE. You can add as many accounts as you have subscriptions; each keeps its own
            5-hour and 7-day quota, and the proxy uses whichever has the most left.
          </p>
          <FilledButton type="button" onClick={begin} disabled={phase === 'starting'}>
            {phase === 'starting' && <Spinner />}
            {phase === 'starting' ? 'Opening…' : 'Start Claude login'}
          </FilledButton>
        </>
      ) : (
        <form className="flex flex-col gap-4" onSubmit={finish}>
          {blocked && (
            <Banner tone="warn">Your browser blocked the new tab. Open the link below by hand.</Banner>
          )}

          <ol className="m-0 flex list-decimal flex-col gap-2 pl-5 text-sm text-on-surface-variant">
            <li>
              Approve access in the tab that just opened{' '}
              <a
                href={start.auth_url}
                target="_blank"
                rel="noopener noreferrer"
                className="font-medium text-primary underline underline-offset-2"
              >
                (reopen it)
              </a>
            </li>
            <li>Anthropic then shows you an authorization code.</li>
            <li>Paste it below — the whole callback URL works too.</li>
          </ol>

          <Field
            label="Callback URL or code"
            value={pasted}
            onChange={setPasted}
            autoFocus
            mono
          />

          <div className="flex flex-wrap items-center gap-2">
            <FilledButton disabled={phase === 'exchanging'}>
              {phase === 'exchanging' && <Spinner />}
              {phase === 'exchanging' ? 'Exchanging…' : 'Finish login'}
            </FilledButton>
            <TextButton
              onClick={() => {
                reset()
                onCancel()
              }}
            >
              Cancel
            </TextButton>
          </div>
        </form>
      )}
    </Card>
  )
}
