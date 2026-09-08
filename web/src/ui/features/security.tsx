import { useState } from 'react'
import { api, messageOf, type Session } from '../../api/client'
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
      <CardTitle
        aside={
          session.password_updated_at !== undefined ? (
            <span className="text-xs text-on-surface-variant">
              password set {new Date(session.password_updated_at).toLocaleDateString()}
            </span>
          ) : null
        }
      >
        Account
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
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (next !== confirm) {
      setError('The two new passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.changePassword(current, next)
      onDone()
    } catch (err) {
      setError(messageOf(err))
    } finally {
      // Not only on the error path. onDone() re-renders Settings rather than
      // unmounting this form, so without this the button stays on "Changing…"
      // for ever with both passwords still on screen — and the operator, who
      // has no way to tell it worked, tries again with a password that is now
      // the old one and concludes they are locked out.
      setBusy(false)
    }
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
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.deleteAdminAccount(password)
      onDone()
    } catch (err) {
      setError(messageOf(err))
      setBusy(false)
    }
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
