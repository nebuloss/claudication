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
  CopyButton,
  CopyField,
  ErrorBanner,
  Field,
  InlineLink,
  Modal,
  Muted,
  Spinner,
  Stack,
  Steps,
} from '../../primitives'
import { ProviderMark } from '../../brands'
import { PROVIDERS, providerOf } from '../../providers'
import { useSession } from '../../session'
import { useNow } from '../../hooks'
import { countdown } from '../../format'

type Phase = 'idle' | 'starting' | 'waiting' | 'exchanging'

/**
 * An account's sign-in, as its provider runs it: a consent page and a pasted
 * code for Claude and Gemini, a device code for ChatGPT (see providers.ts). With
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
 * A pasted-code OAuth login — Claude's, or Gemini's — driven from the browser.
 *
 * "Start" opens the consent screen in a new tab; the operator approves, and an
 * authorization code comes back to be pasted here.
 *
 * Anthropic and Google each render the code on a page of their own and it is
 * pasted back here. Both clients also have a loopback variant for a local
 * listener, but nothing listens on the operator's machine, so that only ever
 * produced an address bar to copy from.
 *
 * On a reconnect the gateway names the account to the provider and refuses the
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
  const p = providerOf(provider)
  const label = p?.label ?? provider
  const vendor = p?.vendor ?? provider
  const site = p?.site ?? vendor

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

  // Where the operator is in it: nothing opened yet, approving in the tab,
  // pasting what came back.
  const opened = start !== null
  return (
    <Stack>
      <ErrorBanner error={error} />
      {blocked && <Banner tone="warn">Your browser blocked the new tab. Open the link below by hand.</Banner>}
      <Muted size="xs">
        {who !== undefined ? (
          <>
            Sign in as <span className="font-medium text-on-surface">{who}</span> to give this account
            fresh credentials; it keeps its place in the list and its history. If your browser is signed in
            to a different {label} account, {vendor} approves as that one and the gateway refuses it — sign
            out of {site} first, or use the private-window link below.
          </>
        ) : (
          <>
            Authorises this gateway with OAuth and PKCE, as whichever {label} account your browser is
            signed in to on {site}. Add one account per subscription: they serve in the order listed, the
            next taking over when {vendor} says one has run out.
          </>
        )}
      </Muted>
      <form onSubmit={finish}>
        <Steps
          steps={[
            {
              title: `Open ${vendor}'s sign-in`,
              detail: opened ? (
                <>
                  Opened in a new tab.{' '}
                  <InlineLink href={start.auth_url ?? ''} primary>
                    Open it again
                  </InlineLink>
                </>
              ) : (
                'In a new tab, where you sign in and approve.'
              ),
              state: opened ? 'done' : 'active',
              children: (
                <Button icon="launch" onClick={begin} busy={phase === 'starting'}>
                  {phase === 'starting' ? 'Opening…' : who !== undefined ? 'Start reconnect' : `Sign in with ${label}`}
                </Button>
              ),
            },
            {
              title: who !== undefined ? <>Approve as {who}</> : 'Approve access',
              detail: p?.pasteHint ?? `${vendor} then shows you an authorization code.`,
              state: opened ? 'active' : 'todo',
              children:
                who !== undefined && opened ? (
                  <CopyField
                    label="Signed in as someone else? Open this in a private window"
                    value={start.auth_url ?? ''}
                  />
                ) : undefined,
            },
            {
              title: 'Paste the code here',
              detail: 'The whole callback URL works too.',
              state: opened ? 'active' : 'todo',
              children: (
                <Stack gap={3}>
                  <Field label="Callback URL or code" value={pasted} onChange={setPasted} autoFocus mono />
                  <Actions>
                    <Button type="submit" icon="login" busy={phase === 'exchanging'}>
                      {phase === 'exchanging'
                        ? 'Signing in…'
                        : who !== undefined
                          ? 'Finish reconnect'
                          : 'Finish sign-in'}
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
              ),
            },
          ]}
        />
      </form>
    </Stack>
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
  const now = useNow(1000)
  const left = start === null ? 0 : Math.max(0, Math.round((new Date(start.expires_at).getTime() - now) / 1000))
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
      // Past its expiry the code is dead at OpenAI too; asking again only
      // earns an error the countdown has already shown.
      if (Date.now() >= new Date(start.expires_at).getTime()) {
        setStart(null)
        setError('The code expired before it was entered. Get a new one to try again.')
        return
      }
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
          // exchange_failed is a poll that failed before anyone approved —
          // the network — and is asked again. Anything else ends the
          // attempt: expired, approved but not redeemed (sign_in_failed),
          // refused as someone else. Back to the start, where a new code is
          // one click away.
          if (err instanceof ApiError && err.type === 'exchange_failed') {
            timer = setTimeout(ask, (start.interval_s ?? DEFAULT_POLL_S) * 1000)
          } else {
            setStart(null)
          }
        })
    }
    timer = setTimeout(ask, (start.interval_s ?? DEFAULT_POLL_S) * 1000)
    return () => {
      cancelled = true
      if (timer !== undefined) clearTimeout(timer)
    }
  }, [start, provider, session])

  const coded = start !== null
  return (
    <Stack>
      <ErrorBanner error={error} />
      <Muted size="xs">
        {who !== undefined ? (
          <>
            Sign in as <span className="font-medium text-on-surface">{who}</span>, in the same workspace, to
            give this account fresh credentials; it keeps its place in the list and its history.
          </>
        ) : (
          <>
            A code to enter on OpenAI&rsquo;s own page, in any browser signed in to the {label} account to
            add. Each workspace is an account of its own: a personal one and a Team one under the same
            address are added separately.
          </>
        )}
      </Muted>
      <Steps
        steps={[
          {
            title: 'Get a code',
            detail: coded ? 'From OpenAI, for this sign-in only.' : 'OpenAI gives one for this sign-in.',
            state: coded ? 'done' : 'active',
            children: (
              <Button icon={error !== '' ? 'refresh' : 'login'} onClick={begin} busy={starting}>
                {starting
                  ? 'Asking OpenAI…'
                  : error !== ''
                    ? 'Get a new code'
                    : who !== undefined
                      ? 'Start reconnect'
                      : `Sign in with ${label}`}
              </Button>
            ),
          },
          {
            title: who !== undefined ? <>Enter it on OpenAI&rsquo;s page, as {who}</> : 'Enter it on OpenAI’s page',
            detail: coded ? `Then approve. The code works for ${countdown(left)} more.` : 'Then approve.',
            state: coded ? 'active' : 'todo',
            children: coded ? (
              <Stack gap={3}>
                {/* Large, because it is read off this screen and typed into
                    another one, often on another device; copied, for the same
                    browser. */}
                <div className="flex flex-wrap items-center gap-3">
                  <code
                    aria-label="Your code"
                    className="rounded-md3-m bg-surface-lowest px-4 py-2 font-mono text-3xl font-medium tracking-[0.2em] text-on-surface ring-1 ring-outline-variant select-all"
                  >
                    {start.user_code ?? ''}
                  </code>
                  <CopyButton label="Copy code" value={start.user_code ?? ''} />
                </div>
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
              </Stack>
            ) : undefined,
          },
          {
            title: 'Added here on its own',
            detail: coded ? (
              // tabular-nums: the clock ticks every second, and proportional
              // digits made the line twitch as they changed width.
              <span className="inline-flex items-center gap-2 tabular-nums">
                <Spinner className="shrink-0" />
                Waiting for the approval — {countdown(left)} left.
              </span>
            ) : (
              'This page notices the approval and adds the account.'
            ),
            state: 'todo',
          },
        ]}
      />
    </Stack>
  )
}

/**
 * The new-account dialog on the Accounts page: which provider, then that
 * provider's login. A dialog, as a new client is, rather than a card pushed
 * above the list: it is one task with a start and an end, and the list under
 * it is not what the operator is looking at while they sign in.
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
    <Modal title="Add an account" onClose={onCancel} size="lg">
      {/* Which product to sign in to: each with its mark, its plan, and a
          radio's dot, so the choice reads as one before anything else. */}
      <div role="radiogroup" aria-label="Provider" className="mb-6 grid gap-3 sm:grid-cols-3">
        {PROVIDERS.map((p) => {
          const on = p.id === provider
          return (
            <button
              key={p.id}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => setProvider(p.id)}
              className={`state-layer relative flex flex-col items-start gap-3 rounded-md3-l p-4 text-left ring-inset ${
                on
                  ? 'bg-secondary-container text-on-secondary-container ring-2 ring-primary'
                  : 'bg-surface-lowest text-on-surface ring-1 ring-outline-variant'
              }`}
            >
              <span className="flex w-full items-center justify-between">
                <span className="grid size-10 place-items-center rounded-full bg-surface-high">
                  <ProviderMark provider={p.id} size="lg" />
                </span>
                <span
                  aria-hidden
                  className={`grid size-5 place-items-center rounded-full border-2 ${
                    on ? 'border-primary' : 'border-outline'
                  }`}
                >
                  {on && <span className="size-2.5 rounded-full bg-primary" />}
                </span>
              </span>
              <span>
                <span className="block text-sm font-medium">{p.label}</span>
                <span className="mt-0.5 block text-xs opacity-80">{p.detail}</span>
              </span>
            </button>
          )
        })}
      </div>
      <ConsentFlow key={provider} provider={provider} onDone={onAdded} onCancel={onCancel} />
    </Modal>
  )
}
