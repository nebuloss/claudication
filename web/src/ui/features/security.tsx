import { useState } from 'react'
import { api, type Session } from '../../api/client'
import { useAction } from '../hooks'
import {
  Banner,
  Card,
  CardTitle,
  Field,
  FilledButton,
  OutlinedButton,
  Spinner,
  TextButton,
} from '../primitives'

/** Both destructive actions live behind a click, so neither is one keystroke away. */
type Open = 'none' | 'password' | 'delete'

/**
 * The account card: change the password, or give the gateway back.
 *
 * Both forms ask for the current password even though the caller is already
 * signed in. The session is a cookie in a browser that may not be the owner's
 * any more; the password is the thing only they know.
 */
export default function Security({
  session,
  onChanged,
  onDeleted,
}: {
  session: Session
  onChanged: () => void
  onDeleted: () => void
}) {
  const [open, setOpen] = useState<Open>('none')

  return (
    <Card>
      {/* Titled: it is the Settings page itself, which no entry in the
          navigation names. */}
      <CardTitle
        aside={
          session.password_updated_at !== undefined ? (
            <span className="text-sm text-on-surface-variant">
              Password set {new Date(session.password_updated_at).toLocaleDateString()}
            </span>
          ) : undefined
        }
      >
        Admin account
      </CardTitle>

      {open === 'none' && (
        <div className="flex flex-wrap items-center gap-2">
          <OutlinedButton onClick={() => setOpen('password')}>Change password</OutlinedButton>
          <TextButton tone="error" onClick={() => setOpen('delete')}>
            Delete account
          </TextButton>
        </div>
      )}

      {open === 'password' && (
        <ChangePassword onDone={onChanged} onCancel={() => setOpen('none')} />
      )}
      {open === 'delete' && <DeleteAccount onDone={onDeleted} onCancel={() => setOpen('none')} />}
    </Card>
  )
}

function ChangePassword({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  // useAction clears busy however the call ends, and this form is the reason
  // it must: onDone() re-renders Settings rather than unmounting the form, so
  // a reset on the error path alone left the button on "Changing…" for ever
  // with both passwords still on screen — and the operator, who had no way to
  // tell it worked, tried again with a password that was now the old one and
  // concluded they were locked out.
  const { pending: busy, error, setError, run } = useAction({ asksPassword: true })

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (next !== confirm) {
      setError('The two new passwords do not match.')
      return
    }
    await run('change', async () => {
      await api.changePassword(current, next)
      onDone()
    })
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={submit}>
      <Field
        label="Current password"
        type="password"
        value={current}
        onChange={setCurrent}
        autoFocus
        autoComplete="current-password"
      />
      <Field
        label="New password"
        type="password"
        value={next}
        onChange={setNext}
        autoComplete="new-password"
      />
      <Field
        label="Repeat new password"
        type="password"
        value={confirm}
        onChange={setConfirm}
        autoComplete="new-password"
      />
      {error !== '' && <Banner tone="error">{error}</Banner>}
      <Banner>
        Every other signed-in browser is signed out. This one stays.
      </Banner>
      <div className="flex items-center gap-2">
        <FilledButton disabled={busy}>
          {busy && <Spinner />}
          {busy ? 'Changing…' : 'Change password'}
        </FilledButton>
        <TextButton onClick={onCancel} disabled={busy}>
          Cancel
        </TextButton>
      </div>
    </form>
  )
}

function DeleteAccount({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const [password, setPassword] = useState('')
  const { pending: busy, error, run } = useAction({ asksPassword: true })

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    await run('delete', async () => {
      await api.deleteAdminAccount(password)
      onDone()
    })
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={submit}>
      <Banner tone="warn">
        The gateway goes back to its first-run state, and the next person to reach
        it chooses the password. Upstream accounts and API keys are left alone, so
        whoever that is inherits them.
      </Banner>
      <Field
        label="Confirm your password"
        type="password"
        value={password}
        onChange={setPassword}
        autoFocus
        autoComplete="current-password"
      />
      {error !== '' && <Banner tone="error">{error}</Banner>}
      <div className="flex items-center gap-2">
        <FilledButton disabled={busy || password === ''}>
          {busy && <Spinner />}
          {busy ? 'Deleting…' : 'Delete account'}
        </FilledButton>
        <TextButton onClick={onCancel} disabled={busy}>
          Cancel
        </TextButton>
      </div>
    </form>
  )
}
