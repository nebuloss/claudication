import { useState } from 'react'
import {
  api,
  ApiError,
  messageOf,
  type Account,
  type OAuthResult,
  type OAuthStart,
} from '../../api/client'
import {
  Banner,
  Card,
  CardTitle,
  CopyField,
  Field,
  FilledButton,
  Spinner,
  TextButton,
} from '../primitives'

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
 *
 * With `account`, the same flow reconnects that account instead of adding one.
 * The gateway names the account to Anthropic and refuses the result if it
 * comes back as anyone else — which is what happens when the browser is signed
 * in to a different Claude account, because the browser, not the gateway,
 * decides who approves. So a reconnect says who to sign in as, and offers the
 * link to open in a private window where no one is signed in.
 */
export function ConsentFlow({
  account,
  onDone,
  onCancel,
}: {
  account?: Account
  onDone: (r: OAuthResult) => void
  onCancel: () => void
}) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [start, setStart] = useState<OAuthStart | null>(null)
  const [pasted, setPasted] = useState('')
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)
  const who = account?.email

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
      .startOAuth('anthropic', account?.id)
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
      const result = await api.completeOAuth('anthropic', start.state, pasted.trim())
      reset()
      onDone(result)
    } catch (err) {
      setError(messageOf(err))
      // A redeemed code cannot be pasted again, and a refused reconnect has
      // been redeemed: back to the start rather than to a form that can only
      // fail the same way. A refused exchange leaves the attempt open, so the
      // corrected paste can still be retried.
      if (err instanceof ApiError && (err.type === 'wrong_account' || err.type === 'not_found')) {
        setStart(null)
        setPasted('')
        setPhase('idle')
      } else {
        setPhase('waiting')
      }
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
    <>
      {error !== '' && (
        <Banner tone="error" className="mb-4">
          {error}
        </Banner>
      )}

      {start === null ? (
        <>
          {who !== undefined ? (
            <p className="mt-0 mb-4 text-sm text-on-surface-variant">
              Sign in as <span className="font-medium text-on-surface">{who}</span> to give this
              account fresh credentials. It keeps its place in the list and its history. If your
              browser is signed in to a different Claude account, Anthropic approves as that one
              and the gateway refuses it — sign out of claude.ai first, or use the private-window
              link offered on the next step.
            </p>
          ) : (
            <p className="mt-0 mb-4 text-sm text-on-surface-variant">
              Opens the Claude consent screen in a new tab and authorises this gateway using OAuth
              with PKCE. Add one account per subscription: they are served in the order listed
              below, and the next takes over when Anthropic says one has run out. The account
              added is whichever one your browser is signed in to on claude.ai.
            </p>
          )}
          <FilledButton type="button" onClick={begin} disabled={phase === 'starting'}>
            {phase === 'starting' && <Spinner />}
            {phase === 'starting'
              ? 'Opening…'
              : who !== undefined
                ? 'Start reconnect'
                : 'Start Claude login'}
          </FilledButton>
        </>
      ) : (
        <form className="flex flex-col gap-4" onSubmit={finish}>
          {blocked && (
            <Banner tone="warn">Your browser blocked the new tab. Open the link below by hand.</Banner>
          )}

          <ol className="m-0 flex list-decimal flex-col gap-2 pl-5 text-sm text-on-surface-variant">
            <li>
              Approve access
              {who !== undefined && (
                <>
                  {' '}
                  as <span className="font-medium text-on-surface">{who}</span>
                </>
              )}{' '}
              in the tab that just opened{' '}
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

          {who !== undefined && (
            <CopyField
              label="Signed in as someone else? Open this in a private window"
              value={start.auth_url}
            />
          )}

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
              {phase === 'exchanging'
                ? 'Exchanging…'
                : who !== undefined
                  ? 'Finish reconnect'
                  : 'Finish login'}
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
    </>
  )
}

/** The new-account form at the top of the Accounts tab. */
export default function AddAccount({
  onAdded,
  onCancel,
}: {
  onAdded: (r: OAuthResult) => void
  onCancel: () => void
}) {
  return (
    <Card>
      <CardTitle aside={<TextButton onClick={onCancel}>Cancel</TextButton>}>
        Add a Claude account
      </CardTitle>
      <ConsentFlow onDone={onAdded} onCancel={onCancel} />
    </Card>
  )
}
