import { useState } from 'react'
import { api, messageOf } from '../../api/client'
import ThemeToggle from './theme-toggle'
import {
  Banner,
  BrandMark,
  Card,
  Field,
  FilledButton,
  Note,
  Spinner,
  TextLink,
} from '../primitives'

/**
 * One screen, two moments: claiming a gateway nobody has set up yet, and
 * signing in to one that is. They differ only in the copy and in whether the
 * password is confirmed, and keeping them together means the transition after
 * setup is a re-render rather than a navigation.
 */
export default function SignIn({
  mode,
  minLength = 8,
  docsURL = '',
  onSignedIn,
}: {
  mode: 'setup' | 'sign-in'
  /** Only meaningful while setting up; the server is the authority either way. */
  minLength?: number
  /** The public docs page, linked when it is published. */
  docsURL?: string
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
    <div className="flex min-h-dvh flex-col">
      {/* The admin header's shape, so signing in and being signed in are one
          product: the mark and name, and on the right the docs and the theme. */}
      <header className="mx-auto flex w-full max-w-[110rem] items-center justify-between gap-4 px-4 pt-4 sm:px-6">
        <div className="flex min-w-0 items-center gap-3">
          <BrandMark />
          <h1 className="m-0 truncate text-lg leading-10 font-normal text-on-surface">
            claudication gateway
          </h1>
        </div>
        <div className="flex items-center gap-2">
          {docsURL !== '' && <TextLink href={docsURL}>Docs</TextLink>}
          <ThemeToggle />
        </div>
      </header>

      <main className="mx-auto flex w-full max-w-md flex-1 flex-col justify-center gap-4 px-4 py-10">
        <Card>
          <h2 className="mt-0 mb-1 text-2xl leading-8 font-normal text-on-surface">
            {setup ? 'Claim this gateway' : 'Sign in'}
          </h2>
          <p className="mt-0 mb-6 text-sm text-on-surface">
            {setup
              ? 'Nobody has claimed it yet. Choose the admin password.'
              : 'To manage its accounts, keys and settings.'}
          </p>

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
            <FilledButton disabled={busy} className="justify-center">
              {busy && <Spinner />}
              {busy ? (setup ? 'Setting up…' : 'Signing in…') : setup ? 'Set password' : 'Sign in'}
            </FilledButton>
          </form>

          {setup ? (
            <Note tone="warn" className="mt-6">
              Whoever sets this password administers the gateway. Do it now if the port is
              reachable by anyone else.
            </Note>
          ) : (
            <Note className="mt-6">
              Forgotten it? On the host that runs the gateway, run{' '}
              <code className="font-mono font-medium">claudication passwd</code>.
            </Note>
          )}
        </Card>

        {/* Someone who reached the admin address wanting to set up a client
            is looking for this page, not that one. */}
        {docsURL !== '' && (
          <p className="m-0 text-center text-sm text-on-surface">
            Configuring a client?{' '}
            <a
              href={docsURL}
              target="_blank"
              rel="noopener noreferrer"
              className="font-medium text-primary underline underline-offset-2"
            >
              Read the documentation
            </a>
          </p>
        )}
      </main>
    </div>
  )
}
