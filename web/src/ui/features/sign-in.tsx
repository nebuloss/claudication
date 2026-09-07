import { useState } from 'react'
import { api, messageOf } from '../../api/client'
import ThemeToggle from './theme-toggle'
import { Banner, Card, Field, FilledButton, Spinner } from '../primitives'

/**
 * One screen, two moments: claiming a gateway nobody has set up yet, and
 * signing in to one that is. They differ only in the copy and in whether the
 * password is confirmed, and keeping them together means the transition after
 * setup is a re-render rather than a navigation.
 */
export default function SignIn({
  mode,
  minLength = 8,
  onSignedIn,
}: {
  mode: 'setup' | 'sign-in'
  /** Only meaningful while setting up; the server is the authority either way. */
  minLength?: number
  onSignedIn: () => void
}) {
  const setup = mode === 'setup'
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (password === '') {
      setError(setup ? 'Choose a password.' : 'Enter your password.')
      return
    }
    // Checked here as well as on the server: the length rule is not a secret,
    // and a round trip to be told the obvious is a worse first run.
    if (setup && password.length < minLength) {
      setError(`Use at least ${minLength} characters.`)
      return
    }
    if (setup && password !== confirm) {
      setError('The two passwords do not match.')
      return
    }

    setBusy(true)
    setError('')
    try {
      await (setup ? api.setup(password) : api.signIn(password))
      onSignedIn()
    } catch (err) {
      setError(messageOf(err))
      setBusy(false)
    }
  }

  return (
    <main className="mx-auto flex min-h-dvh max-w-md items-center px-5 py-10">
      <Card className="w-full">
        <div className="mb-6 flex items-start justify-between gap-4">
          <div>
          <h1 className="m-0 text-2xl leading-8 font-normal text-on-surface">claudication</h1>
          <p className="mt-1 mb-0 text-sm text-on-surface-variant">
            {setup
              ? 'Nobody has claimed this gateway yet. Choose the admin password.'
              : 'Sign in to manage upstream accounts.'}
          </p>
          </div>
          <ThemeToggle />
        </div>

        <form className="flex flex-col gap-4" onSubmit={submit}>
          <Field
            label="Password"
            type="password"
            value={password}
            onChange={setPassword}
            autoFocus
            autoComplete={setup ? 'new-password' : 'current-password'}
          />
          {setup && (
            <Field
              label="Repeat password"
              type="password"
              value={confirm}
              onChange={setConfirm}
              autoComplete="new-password"
            />
          )}
          {error !== '' && <Banner tone="error">{error}</Banner>}
          <FilledButton disabled={busy} className="self-start">
            {busy && <Spinner />}
            {busy ? (setup ? 'Setting up…' : 'Signing in…') : setup ? 'Set password' : 'Sign in'}
          </FilledButton>
        </form>

        <p className="mt-6 mb-0 border-t border-outline-variant pt-4 text-xs text-on-surface-variant">
          {setup ? (
            <>
              Whoever sets this password administers the gateway. Do it now if the
              port is reachable by anyone else.
            </>
          ) : (
            <>
              Forgotten it? On the host that runs the gateway:
              <br />
              <span className="font-mono">claudication passwd</span>
            </>
          )}
        </p>
      </Card>
    </main>
  )
}
