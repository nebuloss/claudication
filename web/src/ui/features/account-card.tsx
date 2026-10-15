import { useState } from 'react'
import { ConsentFlow } from './add-account'
import AccountModels from './account-models'
import { api, messageOf, type Account, type ProbeResult } from '../../api/client'
import {
  Banner,
  Card,
  CardTitle,
  FilledButton,
  KeyValue,
  OutlinedButton,
  Spinner,
  TonalButton,
  Verbatim,
  type ChipTone,
} from '../primitives'

type Busy = '' | 'test' | 'refresh' | 'disable' | 'delete'

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

export function status(a: Account): { tone: ChipTone; label: string } {
  if (a.disabled) return { tone: 'error', label: 'disabled' }
  // Terminal, and the only state a human has to act on: nothing the gateway
  // does brings a refused refresh token back.
  if (a.needs_reauth === true) return { tone: 'error', label: 'reconnect this account' }
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

/**
 * An account's settings page: everything that changes it, a section each.
 *
 * Models first, because which models an account may serve is the setting an
 * operator comes here for most. Then the connection — its token, testing it,
 * reconnecting — then whether it serves at all, and removing it last and
 * apart, where it cannot be hit on the way to anything else.
 *
 * Every action asks the page to reload rather than patching a local copy:
 * the account list is the one source, and a copy that drifted from it once
 * put the "serving" badge on the wrong account.
 */
export default function AccountSettings({
  account,
  onChanged,
  onRemoved,
}: {
  account: Account
  onChanged: () => void
  onRemoved: () => void
}) {
  const [probe, setProbe] = useState<ProbeResult | null>(null)
  const [busy, setBusy] = useState<Busy>('')
  const [error, setError] = useState('')
  const [reconnecting, setReconnecting] = useState(false)

  // Reconnecting is the fix for a dead refresh token and the only cure for an
  // expiring one, so it leads the actions when either is the case.
  const reauthDue = account.needs_reauth === true || account.needs_reauth_soon === true

  const run = async (what: Busy, fn: () => Promise<unknown>) => {
    setBusy(what)
    setError('')
    try {
      await fn()
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
    <>
      <Card>
        <CardTitle>Models</CardTitle>
        <AccountModels account={account} onChanged={onChanged} />
      </Card>

      <Card>
        <CardTitle>Connection</CardTitle>
        <KeyValue
          items={[
            ['Token expires', `${formatDate(account.expires_at)} (${relative(account.expires_at)})`],
            ['Refreshed', formatDate(account.last_refresh_at)],
            ['Last used', formatDate(account.last_used_at)],
            ['Connected', formatDate(account.created_at)],
          ]}
        />

        {account.needs_reauth_soon === true && (
          <Banner tone="warn" className="mt-4">
            Re-authorisation needed
            {account.reauth_days_left !== undefined
              ? ` in ${account.reauth_days_left} day${account.reauth_days_left === 1 ? '' : 's'}`
              : ' soon'}
            . Refreshing the token cannot push this back — reconnect the account.
          </Banner>
        )}
        {account.last_error && (
          <div className="mt-4">
            <Verbatim>{account.last_error}</Verbatim>
          </div>
        )}

        {reconnecting && (
          <div className="mt-5 rounded-[var(--radius-md3-m)] border border-outline bg-surface-high p-4">
            <ConsentFlow
              account={account}
              onDone={() => {
                setReconnecting(false)
                setError('')
                setProbe(null)
                onChanged()
              }}
              onCancel={() => setReconnecting(false)}
            />
          </div>
        )}

        <div className="mt-5 flex flex-wrap items-center gap-2">
          {reauthDue && !reconnecting && (
            <FilledButton type="button" onClick={() => setReconnecting(true)} disabled={busy !== ''}>
              Reconnect
            </FilledButton>
          )}
          <TonalButton
            onClick={() =>
              void run('test', async () => {
                setProbe(null)
                setProbe(await api.testAccount(account.id))
              })
            }
            disabled={busy !== ''}
          >
            {busy === 'test' && <Spinner />}
            {busy === 'test' ? 'Testing…' : 'Test'}
          </TonalButton>
          <OutlinedButton
            onClick={() => void run('refresh', () => api.refreshAccount(account.id))}
            disabled={busy !== ''}
          >
            {busy === 'refresh' && <Spinner />}
            {busy === 'refresh' ? 'Refreshing…' : 'Refresh token'}
          </OutlinedButton>
          {!reauthDue && !reconnecting && (
            <OutlinedButton onClick={() => setReconnecting(true)} disabled={busy !== ''}>
              Reconnect
            </OutlinedButton>
          )}
        </div>
        {probe && <ProbeReport probe={probe} />}
        {error !== '' && (
          <Banner tone="error" className="mt-4">
            {error}
          </Banner>
        )}
      </Card>

      <Card>
        <CardTitle>Availability</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          {account.disabled
            ? 'Paused: this account serves nothing, and keeps its credentials for when it is resumed.'
            : 'Serving, in its place in the priority order. Pausing takes it out of rotation without signing it out.'}
        </p>
        <OutlinedButton
          onClick={() =>
            void run('disable', () => api.setAccountDisabled(account.id, !account.disabled))
          }
          disabled={busy !== ''}
        >
          {busy === 'disable' && <Spinner />}
          {account.disabled ? 'Resume' : 'Pause'}
        </OutlinedButton>
      </Card>

      {/* Removing destroys the credentials and revokes the token upstream, so
          getting the account back means the browser consent flow again. */}
      <Card>
        <CardTitle>Remove this account</CardTitle>
        <p className="mt-0 mb-4 text-sm text-on-surface-variant">
          The stored credentials are destroyed and the token is revoked with Anthropic. Its usage
          stays in the history, under its email.
        </p>
        <OutlinedButton onClick={() => void runDelete()} disabled={busy !== ''} tone="error">
          {busy === 'delete' && <Spinner />}
          Remove {account.email}
        </OutlinedButton>
      </Card>
    </>
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
