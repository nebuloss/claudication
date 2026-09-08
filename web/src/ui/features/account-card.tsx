import { useState } from 'react'
import QuotaMeter from './quota-meter'
import { api, messageOf, type Account, type ProbeResult } from '../../api/client'
import {
  Banner,
  Chip,
  KeyValue,
  OutlinedButton,
  Spinner,
  TonalButton,
  Verbatim,
  IconButton,
  SmallButton,
  compact,
  type ChipTone,
} from '../primitives'

type Busy = '' | 'test' | 'refresh' | 'usage' | 'disable' | 'delete'

function formatDate(iso?: string): string {
  if (!iso) return 'never'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** "in 7h", "12m ago" — enough to judge a token at a glance. */
function relative(iso: string): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return 'unknown'
  const delta = then - Date.now()
  const mins = Math.round(Math.abs(delta) / 60000)
  const text = mins < 60 ? `${mins}m` : mins < 1440 ? `${Math.round(mins / 60)}h` : `${Math.round(mins / 1440)}d`
  return delta < 0 ? `${text} ago` : `in ${text}`
}

function status(a: Account): { tone: ChipTone; label: string } {
  if (a.disabled) return { tone: 'error', label: 'disabled' }
  // Cooling down comes first among the recoverable states, because it is the
  // only one that answers "why is this account not being used right now" —
  // and it is the one the card could not show at all until the pool started
  // reporting it.
  if (a.cooling_until !== undefined && a.cooling_until !== '') {
    return { tone: 'warn', label: `cooling down, back ${relative(a.cooling_until)}` }
  }
  // A subscription window the upstream is refusing outranks a stale token:
  // refreshing fixes one and does nothing at all for the other.
  if (a.quota !== undefined && !a.quota.allowed) return { tone: 'error', label: 'limit reached' }
  if (a.expired) return { tone: 'warn', label: 'token expired' }
  if (a.last_error) return { tone: 'warn', label: 'last call failed' }
  return { tone: 'ok', label: 'ready' }
}

export default function AccountCard({
  account: initial,
  rank,
  total,
  windowDays,
  orderable,
  busy: reordering,
  onMoveUp,
  onMoveDown,
  onChanged,
  onRemoved,
}: {
  account: Account
  rank: number
  total: number
  windowDays: number
  orderable: boolean
  busy: boolean
  onMoveUp: () => void
  onMoveDown: () => void
  onChanged: () => void
  onRemoved: () => void
}) {
  // The prop is the only source of truth. Copying it into state and updating
  // that copy locally left the card showing whatever it last fetched for
  // itself: after a reorder, `serving` and the rank came from the old render
  // and the wrong account wore the badge. Every action that changes an account
  // now asks the parent to reload instead.
  const account = initial
  const [probe, setProbe] = useState<ProbeResult | null>(null)
  const [busy, setBusy] = useState<Busy>('')
  const [error, setError] = useState('')

  const chip = status(account)

  const runTest = async () => {
    setBusy('test')
    setError('')
    setProbe(null)
    try {
      setProbe(await api.testAccount(account.id))
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy('')
    onChanged()
  }

  const runUsage = async () => {
    setBusy('usage')
    setError('')
    try {
      await api.refreshUsage(account.id)
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy('')
    onChanged()
  }

  const runRefresh = async () => {
    setBusy('refresh')
    setError('')
    try {
      await api.refreshAccount(account.id)
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy('')
    onChanged()
  }

  const runToggleDisabled = async () => {
    setBusy('disable')
    setError('')
    try {
      await api.setAccountDisabled(account.id, !account.disabled)
    } catch (err) {
      setError(messageOf(err))
    }
    setBusy('')
    onChanged()
  }

  const runDelete = async () => {
    if (!window.confirm(`Remove ${account.email}? The stored credentials are destroyed.`)) return
    setBusy('delete')
    setError('')
    try {
      await api.deleteAccount(account.id)
      onRemoved()
    } catch (err) {
      setError(messageOf(err))
      setBusy('')
    }
  }

  return (
    <article
      className={`rounded-[var(--radius-md3-l)] p-5 ${
        account.serving ? 'bg-secondary-container/40' : 'bg-surface-low'
      }`}
    >
      <header className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          {orderable && (
            <span
              className="mt-0.5 hidden cursor-grab text-on-surface-variant active:cursor-grabbing sm:block"
              title="Drag to reorder"
              aria-hidden
            >
              <svg viewBox="0 0 24 24" className="size-5 fill-current">
                <path d="M9 4h2v2H9V4zm4 0h2v2h-2V4zM9 9h2v2H9V9zm4 0h2v2h-2V9zm-4 5h2v2H9v-2zm4 0h2v2h-2v-2zm-4 5h2v2H9v-2zm4 0h2v2h-2v-2z" />
              </svg>
            </span>
          )}
          <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-full bg-surface-high text-xs font-medium text-on-surface-variant">
            {rank + 1}
          </span>
          <div className="min-w-0">
            <h3 className="m-0 text-base leading-6 font-medium break-all text-on-surface">
              {account.email}
            </h3>
            <p className="mt-0.5 mb-0 text-xs text-on-surface-variant">
              {account.provider} · added {formatDate(account.created_at)}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-1">
          {account.serving && <Chip tone="ok">serving</Chip>}
          <Chip tone={chip.tone}>{chip.label}</Chip>
          {orderable && (
            <>
              <IconButton
                label="Higher priority"
                disabled={reordering || rank === 0}
                onClick={onMoveUp}
              >
                <path d="M7.4 15.4 12 10.8l4.6 4.6L18 14l-6-6-6 6z" />
              </IconButton>
              <IconButton
                label="Lower priority"
                disabled={reordering || rank === total - 1}
                onClick={onMoveDown}
              >
                <path d="M7.4 8.6 12 13.2l4.6-4.6L18 10l-6 6-6-6z" />
              </IconButton>
            </>
          )}
        </div>
      </header>

      <div className="mb-5 rounded-[var(--radius-md3-m)] border border-outline bg-surface-high px-4 py-3">
        <div className="mb-3 flex items-baseline justify-between gap-3">
          <span className="text-xs tracking-wide text-on-surface-variant uppercase">
            Subscription usage
          </span>
          <SmallButton onClick={() => void runUsage()} disabled={busy !== ''}>
            {busy === 'usage' && <Spinner className="size-3" />}
            {busy === 'usage' ? 'Reading…' : 'Refresh'}
          </SmallButton>
        </div>
        <QuotaMeter quota={account.quota} pending={busy === 'usage'} />
      </div>

      <KeyValue
        items={[
          ['Expires', `${formatDate(account.expires_at)} (${relative(account.expires_at)})`],
          ['Refreshed', formatDate(account.last_refresh_at)],
          ['Last used', formatDate(account.last_used_at)],
          [
            `Via this gateway (${windowDays}d)`,
            `${compact(account.requests)} requests · ${compact(account.tokens)} tokens`,
          ],
        ]}
      />

      {account.needs_reauth_soon === true && (
        <Banner tone="warn" className="mt-4">
          Re-authorisation needed
          {account.reauth_days_left !== undefined
            ? ` in ${account.reauth_days_left} day${account.reauth_days_left === 1 ? '' : 's'}`
            : ' soon'}
          . Refreshing the token cannot push this back — add the account again.
        </Banner>
      )}

      {account.last_error && (
        <div className="mt-4">
          <Verbatim>{account.last_error}</Verbatim>
        </div>
      )}
      {error !== '' && (
        <Banner tone="error" className="mt-4">
          {error}
        </Banner>
      )}

      <div className="mt-5 flex flex-wrap items-center gap-2">
        <TonalButton onClick={runTest} disabled={busy !== ''}>
          {busy === 'test' && <Spinner />}
          {busy === 'test' ? 'Testing…' : 'Test'}
        </TonalButton>
        <OutlinedButton onClick={runRefresh} disabled={busy !== ''}>
          {busy === 'refresh' && <Spinner />}
          {busy === 'refresh' ? 'Refreshing…' : 'Refresh token'}
        </OutlinedButton>
        {/* Pausing keeps the credentials; removing destroys them and revokes
            the token upstream, so getting the account back means going through
            the browser consent flow again. */}
        <OutlinedButton onClick={runToggleDisabled} disabled={busy !== ''}>
          {busy === 'disable' && <Spinner />}
          {account.disabled ? 'Resume' : 'Pause'}
        </OutlinedButton>
        <OutlinedButton onClick={runDelete} disabled={busy !== ''} tone="error">
          Remove
        </OutlinedButton>
      </div>

      {probe && <ProbeReport probe={probe} />}
    </article>
  )
}

function ProbeReport({ probe }: { probe: ProbeResult }) {
  if (!probe.ok) {
    return (
      <div className="mt-4 rounded-[var(--radius-md3-m)] bg-error-container p-4">
        <p className="mt-0 mb-2 text-sm font-medium text-on-error-container">
          Failed — HTTP {probe.status} in {probe.latency_ms} ms
          {probe.request_id ? ` · ${probe.request_id}` : ''}
        </p>
        <Verbatim>{probe.error ?? 'no detail returned'}</Verbatim>
      </div>
    )
  }

  const facts = [
    `model ${probe.model ?? '?'}`,
    `${probe.input_tokens ?? 0} in / ${probe.output_tokens ?? 0} out`,
    `${probe.latency_ms} ms`,
  ]
  return (
    <div className="mt-4 rounded-[var(--radius-md3-m)] bg-success-container p-4">
      <p className="mt-0 mb-2 text-sm font-medium text-on-success-container">
        Works — {facts.join(' · ')}
      </p>
      {probe.reply && <Verbatim>{probe.reply}</Verbatim>}
    </div>
  )
}
