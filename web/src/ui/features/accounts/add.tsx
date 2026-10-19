import { useEffect, useRef, useState } from 'react'
import {
  api,
  ApiError,
  messageOf,
  type Account,
  type OAuthResult,
  type OAuthStart,
} from '../../../api/client'
import {
  Actions,
  Banner,
  Button,
  ButtonLink,
  Card,
  CopyField,
  ErrorBanner,
  Field,
  InlineLink,
  Muted,
  Spinner,
  Stack,
} from '../../primitives'
import { PROVIDERS, providerOf } from '../../providers'
import { useSession } from '../../session'

type Phase = 'idle' | 'starting' | 'waiting' | 'exchanging'

/**
 * An account's sign-in, as its provider runs it: a consent page and a pasted
 * code for Claude, a device code for ChatGPT (see providers.ts). With
 * `account`, the same flow reconnects that account instead of adding one.
 */
export function ConsentFlow({
  account,
  provider = account?.provider ?? 'anthropic',
  onDone,
  onCancel,
}: {
  account?: Account
  /** Whose login: the account's own when reconnecting. */
  provider?: string
  onDone: (r: OAuthResult) => void
  onCancel: () => void
}) {
  if (providerOf(provider)?.signIn === 'device') {
    return <DeviceFlow account={account} provider={provider} onDone={onDone} onCancel={onCancel} />
  }
  return <PasteFlow account={account} provider={provider} onDone={onDone} onCancel={onCancel} />
}

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
 * On a reconnect the gateway names the account to Anthropic and refuses the
 * result if it comes back as anyone else — which is what happens when the
 * browser is signed in to a different Claude account, because the browser,
 * not the gateway, decides who approves. So a reconnect says who to sign in
 * as, and offers the link to open in a private window where no one is signed
 * in.
 */
function PasteFlow({
  account,
  provider,
  onDone,
  onCancel,
}: {
  account?: Account
  provider: string
  onDone: (r: OAuthResult) => void
  onCancel: () => void
}) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [start, setStart] = useState<OAuthStart | null>(null)
  const [pasted, setPasted] = useState('')
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)
  const session = useSession()
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
      .accounts.startOAuth(provider, account?.id)
      .then((s) => {
        setStart(s)
        setPhase('waiting')
        if (tab && !tab.closed) {
          try {
            tab.opener = null
          } catch {
            // Some browsers make opener read-only; navigation still works.
          }
          tab.location.replace(s.auth_url ?? '')
        } else {
          setBlocked(true)
        }
      })
      .catch((err: unknown) => {
        tab?.close()
        setPhase('idle')
        if (!session.report(err)) setError(messageOf(err))
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
      const result = await api.accounts.completeOAuth(provider, start.state, pasted.trim())
      reset()
      onDone(result)
    } catch (err) {
      if (session.report(err)) return
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
      <ErrorBanner error={error} className="mb-4" />

      {start === null ? (
        <>
          {who !== undefined ? (
            <p className="mb-4 text-sm text-on-surface-variant">
              Sign in as <span className="font-medium text-on-surface">{who}</span> to give this
              account fresh credentials. It keeps its place in the list and its history. If your
              browser is signed in to a different Claude account, Anthropic approves as that one
              and the gateway refuses it — sign out of claude.ai first, or use the private-window
              link offered on the next step.
            </p>
          ) : (
            <p className="mb-4 text-sm text-on-surface-variant">
              Opens the Claude consent screen in a new tab and authorises this gateway using OAuth
              with PKCE. Add one account per subscription: they are served in the order listed
              below, and the next takes over when Anthropic says one has run out. The account
              added is whichever one your browser is signed in to on claude.ai.
            </p>
          )}
          <Button onClick={begin} busy={phase === 'starting'}>
            {phase === 'starting'
              ? 'Opening…'
              : who !== undefined
                ? 'Start reconnect'
                : 'Start Claude login'}
          </Button>
        </>
      ) : (
        <form onSubmit={finish}>
          <Stack>
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
                <InlineLink href={start.auth_url ?? ''} primary>
                  (reopen it)
                </InlineLink>
              </li>
              <li>Anthropic then shows you an authorization code.</li>
              <li>Paste it below — the whole callback URL works too.</li>
            </ol>

            {who !== undefined && (
              <CopyField
                label="Signed in as someone else? Open this in a private window"
                value={start.auth_url ?? ''}
              />
            )}

            <Field
              label="Callback URL or code"
              value={pasted}
              onChange={setPasted}
              autoFocus
              mono
            />

            <Actions>
              <Button type="submit" busy={phase === 'exchanging'}>
                {phase === 'exchanging'
                  ? 'Exchanging…'
                  : who !== undefined
                    ? 'Finish reconnect'
                    : 'Finish login'}
              </Button>
              <Button
                variant="text"
                onClick={() => {
                  reset()
                  onCancel()
                }}
              >
                Cancel
              </Button>
            </Actions>
          </Stack>
        </form>
      )}
    </>
  )
}

/** How long a device sign-in waits between questions when the provider names no interval. */
const DEFAULT_POLL_S = 5

/**
 * ChatGPT's: a device code. The gateway asks OpenAI for a short code; the
 * operator enters it on OpenAI's own page, in whichever browser is signed in
 * to the account to connect; this page asks the gateway, at the interval
 * OpenAI named, whether it has been approved, and is done when it has.
 *
 * Nothing comes back from that browser to this one, so it works the same
 * wherever the gateway runs — Codex's own browser login redirects to a
 * listener on the machine running it, which for a gateway is nobody.
 *
 * With `account`, the same flow reconnects it, and the gateway refuses the
 * result if it comes back as another workspace: one ChatGPT sign-in can reach
 * a personal and a Team workspace under the same address, and they are two
 * accounts here.
 */
function DeviceFlow({
  account,
  provider,
  onDone,
  onCancel,
}: {
  account?: Account
  provider: string
  onDone: (r: OAuthResult) => void
  onCancel: () => void
}) {
  const [start, setStart] = useState<OAuthStart | null>(null)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const session = useSession()
  const who = account?.email
  const label = providerOf(provider)?.label ?? provider
  // The latest callbacks, so the polling effect does not restart when the
  // parent re-renders with new ones.
  const done = useRef(onDone)
  done.current = onDone

  const begin = () => {
    setStarting(true)
    setError('')
    api.accounts
      .startOAuth(provider, account?.id)
      .then((s) => setStart(s))
      .catch((err: unknown) => {
        if (!session.report(err)) setError(messageOf(err))
      })
      .finally(() => setStarting(false))
  }

  // One question at a time, each after the interval: the gateway asks
  // OpenAI once per call, and asking faster than OpenAI said is refused.
  useEffect(() => {
    if (start === null) return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const ask = () => {
      api.accounts
        .pollOAuth(provider, start.state)
        .then((result) => {
          if (cancelled) return
          if (result === null) {
            timer = setTimeout(ask, (start.interval_s ?? DEFAULT_POLL_S) * 1000)
            return
          }
          setStart(null)
          done.current(result)
        })
        .catch((err: unknown) => {
          if (cancelled || session.report(err)) return
          setError(messageOf(err))
          // Spent, refused as someone else, or expired: back to the start,
          // where a new code is one click away.
          if (err instanceof ApiError && err.type !== 'exchange_failed') setStart(null)
          else timer = setTimeout(ask, (start.interval_s ?? DEFAULT_POLL_S) * 1000)
        })
    }
    timer = setTimeout(ask, (start.interval_s ?? DEFAULT_POLL_S) * 1000)
    return () => {
      cancelled = true
      if (timer !== undefined) clearTimeout(timer)
    }
  }, [start, provider, session])

  return (
    <>
      <ErrorBanner error={error} className="mb-4" />

      {start === null ? (
        <>
          <p className="mb-4 text-sm text-on-surface-variant">
            {who !== undefined ? (
              <>
                Sign in as <span className="font-medium text-on-surface">{who}</span>, in the same
                workspace, to give this account fresh credentials. It keeps its place in the list
                and its history.
              </>
            ) : (
              <>
                Gives you a code to enter on OpenAI&rsquo;s own page, in any browser signed in to
                the {label} account to add. Each workspace is an account of its own: a personal one
                and a Team one under the same address are added separately. These accounts serve
                Codex, over the OpenAI Responses API.
              </>
            )}
          </p>
          <Button onClick={begin} busy={starting}>
            {starting ? 'Asking OpenAI…' : who !== undefined ? 'Start reconnect' : `Start ${label} login`}
          </Button>
        </>
      ) : (
        <Stack>
          <ol className="m-0 flex list-decimal flex-col gap-2 pl-5 text-sm text-on-surface-variant">
            <li>
              Open OpenAI&rsquo;s device page and sign in
              {who !== undefined && (
                <>
                  {' '}
                  as <span className="font-medium text-on-surface">{who}</span>
                </>
              )}
              .
            </li>
            <li>Enter the code below, and approve.</li>
            <li>This page notices on its own and adds the account.</li>
          </ol>

          <CopyField label="Your code" value={start.user_code ?? ''} />

          <Actions>
            <ButtonLink href={start.verification_uri ?? ''} external variant="filled">
              Open the device page
            </ButtonLink>
            <Button
              variant="text"
              onClick={() => {
                setStart(null)
                setError('')
                onCancel()
              }}
            >
              Cancel
            </Button>
          </Actions>

          <Muted size="xs">
            <span className="inline-flex items-center gap-2">
              <Spinner />
              Waiting for the code to be entered — it works for fifteen minutes.
            </span>
          </Muted>
        </Stack>
      )}
    </>
  )
}

/**
 * The new-account form at the top of the Accounts page: which provider, then
 * that provider's login.
 */
export default function AddAccount({
  onAdded,
  onCancel,
}: {
  onAdded: (r: OAuthResult) => void
  onCancel: () => void
}) {
  const [provider, setProvider] = useState(PROVIDERS[0].id)
  return (
    <Card
      title="Add an account"
      aside={
        <Button variant="text" onClick={onCancel}>
          Cancel
        </Button>
      }
    >
      <div role="radiogroup" aria-label="Provider" className="mb-5 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {PROVIDERS.map((p) => {
          const on = p.id === provider
          return (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => setProvider(p.id)}
              className={`state-layer flex flex-col items-start gap-1 rounded-md3-m border p-4 text-left ${
                on
                  ? 'border-2 border-primary bg-secondary-container p-[15px] text-on-secondary-container'
                  : 'border-outline text-on-surface'
              }`}
            >
              <span className="text-sm font-medium">{p.label}</span>
              <span className="text-xs opacity-80">{p.detail}</span>
            </button>
          )
        })}
      </div>
      <ConsentFlow key={provider} provider={provider} onDone={onAdded} onCancel={onCancel} />
    </Card>
  )
}
