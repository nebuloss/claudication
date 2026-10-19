import { useState } from 'react'
import { ConsentFlow } from './add-account'
import AccountModels from './account-models'
import { useAction } from '../hooks'
import { api, type Account, type ProbeResult } from '../../api/client'
import { dateTime, relative } from '../format'
import {
  Actions,
  Banner,
  Button,
  Card,
  DangerZone,
  ErrorBanner,
  KeyValue,
  Verbatim,
  tile,
  useConfirm,
} from '../primitives'

type Busy = 'test' | 'refresh' | 'disable'

/**
 * An account's settings: everything that changes it, a page per section
 * (/accounts/<id>/settings/<section>).
 *
 * Models first, because which models an account may serve is the setting an
 * operator comes here for most. Then the connection — its token, testing it,
 * reconnecting — and last, under Advanced, pausing it and removing it: the
 * two ways out of use, on a page of their own where neither can be hit on the
 * way to anything else.
 *
 * Every action asks the page to reload rather than patching a local copy:
 * the account list is the one source, and a copy that drifted from it once
 * put the "serving" badge on the wrong account.
 */
export default function AccountSettings({
  account,
  section,
  onChanged,
  onRemoved,
}: {
  account: Account
  /** Which of the account's settings pages this is: models, connection or advanced. */
  section: string
  onChanged: () => void
  onRemoved: () => void
}) {
  const [probe, setProbe] = useState<ProbeResult | null>(null)
  const { busy, error, setError, run: act } = useAction<Busy>()
  const confirm = useConfirm()
  const [reconnecting, setReconnecting] = useState(false)

  // Reconnecting is the fix for a dead refresh token and the only cure for an
  // expiring one, so it leads the actions when either is the case.
  const reauthDue = account.needs_reauth === true || account.needs_reauth_soon === true

  const run = async (what: Busy, fn: () => Promise<unknown>) => {
    await act(what, fn)
    onChanged()
  }

  const runDelete = () =>
    confirm({
      title: `Remove ${account.email}?`,
      body: 'The stored credentials are destroyed.',
      confirmLabel: 'Remove account',
      tone: 'danger',
      action: async () => {
        await api.deleteAccount(account.id)
        onRemoved()
      },
    })

  if (section === 'models') {
    return (
      <Card>
        <AccountModels account={account} onChanged={onChanged} />
      </Card>
    )
  }

  if (section === 'connection') {
    return (
      <Card>
        <KeyValue
          items={[
            ['Token expires', `${dateTime(account.expires_at)} (${relative(account.expires_at)})`],
            ['Refreshed', dateTime(account.last_refresh_at)],
            ['Last used', dateTime(account.last_used_at)],
            ['Connected', dateTime(account.created_at)],
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
          <div className={`mt-5 ${tile} p-4`}>
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

        <div className="mt-5">
          <Actions>
            {reauthDue && !reconnecting && (
              <Button onClick={() => setReconnecting(true)} disabled={busy !== ''}>
                Reconnect
              </Button>
            )}
            <Button
              variant="tonal"
              onClick={() =>
                void run('test', async () => {
                  setProbe(null)
                  setProbe(await api.testAccount(account.id))
                })
              }
              busy={busy === 'test'}
              disabled={busy !== ''}
            >
              {busy === 'test' ? 'Testing…' : 'Test'}
            </Button>
            <Button
              variant="outlined"
              onClick={() => void run('refresh', () => api.refreshAccount(account.id))}
              busy={busy === 'refresh'}
              disabled={busy !== ''}
            >
              {busy === 'refresh' ? 'Refreshing…' : 'Refresh token'}
            </Button>
            {!reauthDue && !reconnecting && (
              <Button variant="outlined" onClick={() => setReconnecting(true)} disabled={busy !== ''}>
                Reconnect
              </Button>
            )}
          </Actions>
        </div>
        {probe && <ProbeReport probe={probe} />}
        <ErrorBanner error={error} className="mt-4" />
      </Card>
    )
  }

  // Advanced: taking the account out of use, for a while or for good.
  return (
    <>
      <Card
        title={account.disabled ? 'Resume this account' : 'Pause this account'}
        info={
          <p>
            Pausing takes an account out of rotation without signing it out: it serves nothing, and
            keeps its credentials for when it is resumed in its place in the priority order.
          </p>
        }
      >
        <Button
          variant="outlined"
          onClick={() =>
            void run('disable', () => api.setAccountDisabled(account.id, !account.disabled))
          }
          busy={busy === 'disable'}
          disabled={busy !== ''}
        >
          {account.disabled ? 'Resume' : 'Pause'}
        </Button>
      </Card>

      {/* Removing destroys the credentials and revokes the token upstream, so
          getting the account back means the browser consent flow again. */}
      <DangerZone
        title="Remove this account"
        action={
          <Button variant="outlined" tone="error" onClick={() => void runDelete()} disabled={busy !== ''}>
            Remove {account.email}
          </Button>
        }
      >
        The stored credentials are destroyed and the token is revoked with Anthropic. Its usage
        stays in the history, under its email.
      </DangerZone>

      <ErrorBanner error={error} />
    </>
  )
}

function ProbeReport({ probe }: { probe: ProbeResult }) {
  if (!probe.ok) {
    return (
      <div className="mt-4 rounded-md3-m bg-error-container p-4">
        <p className="mb-2 text-sm font-medium text-on-error-container">
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
    <div className="mt-4 rounded-md3-m bg-success-container p-4">
      <p className="mb-2 text-sm font-medium text-on-success-container">
        Works — {facts.join(' · ')}
      </p>
      {probe.reply && <Verbatim>{probe.reply}</Verbatim>}
    </div>
  )
}
